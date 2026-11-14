package deskdisplay

import (
	"bytes"
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

// Display is one X display, driven in-process: its screen read with GetImage, its mouse and keyboard through XTest.
// No program is started per action (Link v0.67: ImageMagick and xdotool were forked for every picture and click).
type Display struct {
	conn   *xgb.Conn
	root   xproto.Window
	width  int
	height int
	keys   map[uint32]key
	shiftK xproto.Keycode
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
	return d, nil
}

// Close ends the connection.
func (d *Display) Close() { d.conn.Close() }

// Size is the screen's width and height in pixels.
func (d *Display) Size() (int, int) { return d.width, d.height }

// Shot is the whole screen as a JPEG no wider than maxWidth (0: its own width) and, by lowering the quality and then
// the width, no larger than maxBytes (0: no bound). It answers the picture and its size.
func (d *Display) Shot(maxWidth, maxBytes int) ([]byte, int, int, error) {
	img, err := xproto.GetImage(d.conn, xproto.ImageFormatZPixmap, xproto.Drawable(d.root), 0, 0, uint16(d.width), uint16(d.height), 0xffffffff).Reply()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("the screen could not be read: %w", err)
	}
	if len(img.Data) < d.width*d.height*4 {
		return nil, 0, 0, fmt.Errorf("the screen came back short (%d bytes for %d×%d: depth %d)", len(img.Data), d.width, d.height, img.Depth)
	}
	w := d.width
	if maxWidth > 0 && maxWidth < w {
		w = maxWidth
	}
	quality := 75
	for {
		frame := scaleBGRX(img.Data, d.width, d.height, w)
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, frame, &jpeg.Options{Quality: quality}); err != nil {
			return nil, 0, 0, err
		}
		if maxBytes <= 0 || buf.Len() <= maxBytes || w <= 320 {
			return buf.Bytes(), frame.Bounds().Dx(), frame.Bounds().Dy(), nil
		}
		if quality > 45 {
			quality -= 15
		} else {
			w = max(320, w*3/4)
		}
	}
}

// scaleBGRX turns the X server's 32-bit BGRX pixels into an RGBA image w wide, averaging the source pixels each one
// covers (a picture of text stays legible when it shrinks).
func scaleBGRX(src []byte, sw, sh, w int) *image.RGBA {
	if w >= sw {
		out := image.NewRGBA(image.Rect(0, 0, sw, sh))
		for i := 0; i < sw*sh; i++ {
			out.Pix[i*4], out.Pix[i*4+1], out.Pix[i*4+2], out.Pix[i*4+3] = src[i*4+2], src[i*4+1], src[i*4], 0xff
		}
		return out
	}
	h := max(1, sh*w/sw)
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		y0, y1 := y*sh/h, max(y*sh/h+1, (y+1)*sh/h)
		for x := 0; x < w; x++ {
			x0, x1 := x*sw/w, max(x*sw/w+1, (x+1)*sw/w)
			var r, g, b, n int
			for yy := y0; yy < y1; yy++ {
				row := yy * sw * 4
				for xx := x0; xx < x1; xx++ {
					p := row + xx*4
					b += int(src[p])
					g += int(src[p+1])
					r += int(src[p+2])
					n++
				}
			}
			o := (y*w + x) * 4
			out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = byte(r/n), byte(g/n), byte(b/n), 0xff
		}
	}
	return out
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
