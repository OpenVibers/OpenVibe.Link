package deskdisplay

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"strconv"
	"strings"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
)

// ErrFallback says an input this package does not do natively (a character with no key on the keymap, an xdotool
// verb it does not know): the caller may run the real xdotool for it.
var ErrFallback = errors.New("not done natively")

type key struct {
	code  xproto.Keycode
	shift bool
}

// Display is one X display, driven in-process: its screen read through MIT-SHM (else GetImage), its mouse and keyboard
// through XTest. No program is started per action (Link v0.67: ImageMagick and xdotool were forked for every picture
// and click).
type Display struct {
	conn   *xgb.Conn
	root   xproto.Window
	width  int
	height int
	keys   map[uint32]key
	shiftK xproto.Keycode
	shm    *shmFrame // nil: the screen comes over the socket
	scaled []byte    // the last shrunk frame's pixels, reused
}

// Open connects to the display (":0") and reads its size and keyboard map.
func Open(name string) (*Display, error) {
	c, err := xgb.NewConnDisplay(name)
	if err != nil {
		return nil, fmt.Errorf("display %s: %w", name, err)
	}
	if err := xtest.Init(c); err != nil {
		c.Close()
		return nil, fmt.Errorf("display %s has no XTEST: %w", name, err)
	}
	setup := xproto.Setup(c)
	scr := setup.DefaultScreen(c)
	d := &Display{conn: c, root: scr.Root, width: int(scr.WidthInPixels), height: int(scr.HeightInPixels), keys: map[uint32]key{}}
	min, max := setup.MinKeycode, setup.MaxKeycode
	km, err := xproto.GetKeyboardMapping(c, min, byte(max-min+1)).Reply()
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("keyboard map: %w", err)
	}
	per := int(km.KeysymsPerKeycode)
	for i := 0; i*per < len(km.Keysyms); i++ {
		code := xproto.Keycode(int(min) + i)
		for col := 0; col < per && col < 2; col++ {
			ks := uint32(km.Keysyms[i*per+col])
			if ks == 0 {
				continue
			}
			if _, seen := d.keys[ks]; !seen {
				d.keys[ks] = key{code: code, shift: col == 1}
			}
		}
	}
	if k, ok := d.keys[0xffe1]; ok {
		d.shiftK = k.code
	}
	if bitsPerPixel(setup, scr.RootDepth) == 32 {
		d.shm, _ = openShm(c, d.width, d.height)
	}
	return d, nil
}

// bitsPerPixel is how many bits a pixel of this depth takes in a ZPixmap picture (0: the server has no such format).
func bitsPerPixel(setup *xproto.SetupInfo, depth byte) byte {
	for _, f := range setup.PixmapFormats {
		if f.Depth == depth {
			return f.BitsPerPixel
		}
	}
	return 0
}

// Close ends the connection.
func (d *Display) Close() {
	if d.shm != nil {
		d.shm.close()
	}
	d.conn.Close()
}

// Paths says how pictures are made here: the screen read ("shm" or "socket") and the encoder ("libjpeg-turbo" or "go").
func (d *Display) Paths() (capture, encoder string) {
	capture, encoder = "socket", "go"
	if d.shm != nil {
		capture = "shm"
	}
	if t, _ := loadTurbo(); t != nil {
		encoder = "libjpeg-turbo"
	}
	return
}

// Size is the screen's width and height in pixels.
func (d *Display) Size() (int, int) { return d.width, d.height }

// Shot is the whole screen as a JPEG no wider than maxWidth (0: its own width) and, by lowering the quality and then
// the width, no larger than maxBytes (0: no bound). It answers the picture and its size.
//
// Link v0.68: the frame is X's own 32-bit BGRX pixels end to end. It is read through MIT-SHM, shrunk (when it must be)
// as BGRX, and handed to libjpeg-turbo as it is; only where the library is missing is it turned into RGBA for Go's
// encoder. A 1280×800 frame went from about 28 ms (GetImage, convert, image/jpeg) to about 3.3 ms.
func (d *Display) Shot(maxWidth, maxBytes int) ([]byte, int, int, error) {
	frame, err := d.read()
	if err != nil {
		return nil, 0, 0, err
	}
	w := d.width
	if maxWidth > 0 && maxWidth < w {
		w = maxWidth
	}
	quality, madeW := 75, -1
	var pix []byte
	var fw, fh, pitch int
	for {
		if w != madeW {
			madeW = w
			if w < d.width {
				d.scaled, fh = shrinkBGRX(d.scaled, frame, d.width, d.height, w)
				pix, fw, pitch = d.scaled, w, w*4
			} else {
				pix, fw, fh, pitch = frame, d.width, d.height, d.width*4
			}
		}
		pic, err := encodeBGRX(pix, fw, fh, pitch, quality)
		if err != nil {
			return nil, 0, 0, err
		}
		if maxBytes <= 0 || len(pic) <= maxBytes || w <= 320 {
			return pic, fw, fh, nil
		}
		if quality > 45 {
			quality -= 15
		} else {
			w = max(320, w*3/4)
		}
	}
}

// read is the whole screen as X's 32-bit pixels: through the shared segment, or (where that is not set up, or once it
// fails) over the socket.
func (d *Display) read() ([]byte, error) {
	if d.shm != nil {
		if data, err := d.shm.read(d.root, d.width, d.height); err == nil {
			return data, nil
		}
		d.shm.close()
		d.shm = nil
	}
	img, err := xproto.GetImage(d.conn, xproto.ImageFormatZPixmap, xproto.Drawable(d.root), 0, 0, uint16(d.width), uint16(d.height), 0xffffffff).Reply()
	if err != nil {
		return nil, fmt.Errorf("the screen could not be read: %w", err)
	}
	if len(img.Data) < d.width*d.height*4 {
		return nil, fmt.Errorf("the screen came back short (%d bytes for %d×%d: depth %d)", len(img.Data), d.width, d.height, img.Depth)
	}
	return img.Data, nil
}

// encodeBGRX is a BGRX frame (w×h, pitch bytes a row) as a JPEG: libjpeg-turbo where it is installed, else Go's encoder.
func encodeBGRX(pix []byte, w, h, pitch, quality int) ([]byte, error) {
	if t, _ := loadTurbo(); t != nil {
		if pic, err := t.encode(pix, w, h, pitch, quality); err == nil {
			return pic, nil
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, rgbaFromBGRX(pix, w, h, pitch), &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// rgbaFromBGRX turns X's 32-bit BGRX pixels into the RGBA image Go's encoder reads.
func rgbaFromBGRX(src []byte, w, h, pitch int) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		s, o := src[y*pitch:y*pitch+w*4], out.Pix[y*out.Stride:y*out.Stride+w*4]
		for i := 0; i < w*4; i += 4 {
			o[i], o[i+1], o[i+2], o[i+3] = s[i+2], s[i+1], s[i], 0xff
		}
	}
	return out
}

// shrinkBGRX shrinks a BGRX frame (sw×sh, tightly packed) to w wide, keeping its shape, into dst (grown when it is too
// small). Each output pixel averages the source pixels it covers, so a picture of text stays legible. It answers the
// pixels and the new height. A source pixel is one 32-bit load and two adds, and each average a multiply by a
// reciprocal, not a division (1280→1024: 11 ms down to about 5). The default picture is the desk's own width: no shrink.
func shrinkBGRX(dst, src []byte, sw, sh, w int) ([]byte, int) {
	if w >= sw {
		return append(dst[:0], src[:sw*sh*4]...), sh
	}
	h := max(1, sh*w/sw)
	if cap(dst) < w*h*4 {
		dst = make([]byte, w*h*4)
	}
	dst = dst[:w*h*4]
	// The spans tile the source: output x covers source columns [xs[x], xs[x+1]), output y rows [y*sh/h, (y+1)*sh/h).
	xs := make([]int, w+1)
	for x := range w + 1 {
		xs[x] = x * sw / w
	}
	maxN := (sw/w + 1) * (sh/h + 1)
	inv := make([]uint64, maxN+1) // inv[n] = 2^24/n, rounded
	for n := 1; n <= maxN; n++ {
		inv[n] = (1<<24 + uint64(n)/2) / uint64(n)
	}
	// Each pixel is read as one 32-bit word (B G R X, low byte first); blue and red add up in the two 32-bit halves of
	// one 64-bit sum and green in another, so a source pixel is one load and two adds.
	br := make([]uint64, w)
	gg := make([]uint64, w)
	for y := range h {
		y0, y1 := y*sh/h, (y+1)*sh/h
		clear(br)
		clear(gg)
		for yy := y0; yy < y1; yy++ {
			row := src[yy*sw*4 : (yy+1)*sw*4]
			for x := range w {
				seg := row[xs[x]*4 : xs[x+1]*4]
				var a, b uint64
				for len(seg) >= 4 {
					v := binary.LittleEndian.Uint32(seg)
					a += uint64(v&0xff) | uint64(v&0xff0000)<<16
					b += uint64(v>>8) & 0xff
					seg = seg[4:]
				}
				br[x] += a
				gg[x] += b
			}
		}
		out := dst[y*w*4 : (y+1)*w*4]
		for x := range w {
			r := inv[(xs[x+1]-xs[x])*(y1-y0)]
			o := out[x*4 : x*4+4 : x*4+4]
			o[0] = byte(min(255, ((br[x]&0xffffffff)*r+1<<23)>>24))
			o[1] = byte(min(255, (gg[x]*r+1<<23)>>24))
			o[2] = byte(min(255, ((br[x]>>32)*r+1<<23)>>24))
			o[3] = 0xff
		}
	}
	return dst, h
}

// Move puts the pointer at (x, y), clamped to the screen.
func (d *Display) Move(x, y int) error {
	x, y = min(max(0, x), d.width-1), min(max(0, y), d.height-1)
	return xtest.FakeInputChecked(d.conn, xproto.MotionNotify, 0, 0, d.root, int16(x), int16(y), 0).Check()
}

// Click presses and releases a button (1 left, 2 middle, 3 right, 4/5 the wheel) repeat times.
func (d *Display) Click(button, repeat int) error {
	if button < 1 || button > 9 || repeat < 1 || repeat > 50 {
		return fmt.Errorf("click: button 1-9, repeat 1-50")
	}
	for i := 0; i < repeat; i++ {
		if err := xtest.FakeInputChecked(d.conn, xproto.ButtonPress, byte(button), 0, d.root, 0, 0, 0).Check(); err != nil {
			return err
		}
		if err := xtest.FakeInputChecked(d.conn, xproto.ButtonRelease, byte(button), 0, d.root, 0, 0, 0).Check(); err != nil {
			return err
		}
	}
	return nil
}

func (d *Display) press(code xproto.Keycode, down bool) error {
	t := byte(xproto.KeyRelease)
	if down {
		t = xproto.KeyPress
	}
	return xtest.FakeInputChecked(d.conn, t, byte(code), 0, d.root, 0, 0, 0).Check()
}

// Key presses a chord ("ctrl+s", "Return", "alt+Tab"): each key down in order, then up in reverse.
func (d *Display) Key(spec string) error {
	syms, ok := chord(spec)
	if !ok {
		return ErrFallback
	}
	var codes []key
	for _, ks := range syms {
		k, ok := d.keys[ks]
		if !ok {
			return ErrFallback
		}
		codes = append(codes, k)
	}
	shift := len(codes) == 1 && codes[0].shift && d.shiftK != 0
	if shift {
		if err := d.press(d.shiftK, true); err != nil {
			return err
		}
	}
	for _, k := range codes {
		if err := d.press(k.code, true); err != nil {
			return err
		}
	}
	for i := len(codes) - 1; i >= 0; i-- {
		if err := d.press(codes[i].code, false); err != nil {
			return err
		}
	}
	if shift {
		return d.press(d.shiftK, false)
	}
	return nil
}

// Type types text a character at a time; a character with no key on this keymap is ErrFallback (before any is typed).
func (d *Display) Type(text string) error {
	plan := make([]key, 0, len(text))
	for _, r := range text {
		ks := uint32(r)
		switch r {
		case '\n':
			ks = 0xff0d
		case '\t':
			ks = 0xff09
		}
		if r > 0x7e && r != '\n' && r != '\t' {
			return ErrFallback
		}
		k, ok := d.keys[ks]
		if !ok {
			return ErrFallback
		}
		plan = append(plan, k)
	}
	for _, k := range plan {
		if k.shift && d.shiftK != 0 {
			if err := d.press(d.shiftK, true); err != nil {
				return err
			}
		}
		if err := d.press(k.code, true); err != nil {
			return err
		}
		if err := d.press(k.code, false); err != nil {
			return err
		}
		if k.shift && d.shiftK != 0 {
			if err := d.press(d.shiftK, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// Xdotool does, in-process, the xdotool commands the computer plugin sends (computer plugin _input_argv): mousemove,
// click [--repeat N], mousemove … click …, key [--] CHORD, type [--delay N] [--] TEXT, getdisplaygeometry. Anything
// else is ErrFallback. It answers what xdotool would print.
func (d *Display) Xdotool(argv []string) (string, error) {
	if len(argv) < 2 || argv[0] != "xdotool" {
		return "", ErrFallback
	}
	args := argv[1:]
	for len(args) > 0 {
		switch args[0] {
		case "getdisplaygeometry":
			return fmt.Sprintf("%d %d\n", d.width, d.height), nil
		case "mousemove":
			if len(args) < 3 {
				return "", ErrFallback
			}
			x, e1 := strconv.Atoi(args[1])
			y, e2 := strconv.Atoi(args[2])
			if e1 != nil || e2 != nil {
				return "", ErrFallback
			}
			if err := d.Move(x, y); err != nil {
				return "", err
			}
			args = args[3:]
		case "click":
			repeat, rest := 1, args[1:]
			if len(rest) >= 2 && rest[0] == "--repeat" {
				n, err := strconv.Atoi(rest[1])
				if err != nil {
					return "", ErrFallback
				}
				repeat, rest = n, rest[2:]
			}
			if len(rest) < 1 {
				return "", ErrFallback
			}
			b, err := strconv.Atoi(rest[0])
			if err != nil {
				return "", ErrFallback
			}
			if err := d.Click(b, repeat); err != nil {
				return "", err
			}
			args = rest[1:]
		case "key":
			rest := args[1:]
			if len(rest) > 0 && rest[0] == "--" {
				rest = rest[1:]
			}
			if len(rest) != 1 {
				return "", ErrFallback
			}
			return "", d.Key(rest[0])
		case "type":
			rest := args[1:]
			if len(rest) >= 2 && rest[0] == "--delay" {
				rest = rest[2:]
			}
			if len(rest) > 0 && rest[0] == "--" {
				rest = rest[1:]
			}
			if len(rest) != 1 {
				return "", ErrFallback
			}
			return "", d.Type(rest[0])
		default:
			return "", ErrFallback
		}
	}
	return "", nil
}

// trimmed keeps argv short in an error.
func trimmed(argv []string) string {
	s := strings.Join(argv, " ")
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}
