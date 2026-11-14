package deskdisplay

import (
	"bytes"
	"image/jpeg"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

func TestKeysAndChords(t *testing.T) {
	cases := map[string][]uint32{
		"Return": {0xff0d}, "ctrl+s": {0xffe3, 's'}, "alt+Tab": {0xffe9, 0xff09}, "ctrl+shift+t": {0xffe3, 0xffe1, 't'},
		"a": {'a'}, "F5": {0xffc2}, "super": {0xffeb}, "ctrl++": {0xffe3, '+'},
	}
	for spec, want := range cases {
		got, ok := chord(spec)
		if !ok || len(got) != len(want) {
			t.Fatalf("%q: %v %v", spec, got, ok)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%q: %x, want %x", spec, got, want)
			}
		}
	}
	for _, bad := range []string{"hyper+q", "ctrl+nosuchkey", "a+b+c+d+e+f"} {
		if _, ok := chord(bad); ok {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

func TestParseApps(t *testing.T) {
	apps, err := ParseApps(`[["wm", ["/usr/bin/openbox"]], ["terminal", ["/usr/bin/xterm", "-e", "/bin/bash"]]]`)
	if err != nil || len(apps) != 2 || apps[1].Name != "terminal" || apps[1].Argv[2] != "/bin/bash" {
		t.Fatalf("%+v %v", apps, err)
	}
	for _, bad := range []string{`{}`, `[["wm"]]`, `[["wm", []]]`, `[["wm", "openbox"]]`} {
		if _, err := ParseApps(bad); err == nil {
			t.Fatalf("%s was accepted", bad)
		}
	}
}

func TestShrinkBGRX(t *testing.T) {
	// 4×2 BGRX: the left half blue, the right half red; at width 2 each output pixel averages a 2×2 block.
	src := make([]byte, 4*2*4)
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			p := (y*4 + x) * 4
			if x < 2 {
				src[p] = 200 // blue
			} else {
				src[p+2] = 100 // red
			}
		}
	}
	px, h := shrinkBGRX(nil, src, 4, 2, 2)
	if h != 1 || len(px) != 2*4 {
		t.Fatalf("size 2×%d, %d bytes", h, len(px))
	}
	if b, g, r := px[0], px[1], px[2]; b != 200 || g != 0 || r != 0 {
		t.Fatalf("left %d %d %d", b, g, r)
	}
	if b, r := px[4], px[6]; b != 0 || r != 100 {
		t.Fatalf("right %d %d", b, r)
	}
	again, _ := shrinkBGRX(px, src, 4, 2, 2)
	if &again[0] != &px[0] {
		t.Fatal("a buffer big enough is reused")
	}
	// Go's encoder reads RGBA: a padded row (pitch 20 for 4 pixels) is read to its width only.
	padded := make([]byte, 20*2)
	copy(padded[0:16], src[0:16])
	copy(padded[20:36], src[16:32])
	img := rgbaFromBGRX(padded, 4, 2, 20)
	if img.Pix[2] != 200 || img.Pix[3] != 0xff || img.Pix[img.Stride+8] != 100 {
		t.Fatal("BGRX to RGBA")
	}
}

// TestEncodeBGRX makes a JPEG of a real-looking frame with whichever encoder is here (OPENVIBE_TURBOJPEG names the
// library for a run that has it unpacked somewhere), and with Go's: both decode to the frame's size and colour.
func TestEncodeBGRX(t *testing.T) {
	const w, h = 320, 200
	frame := make([]byte, w*h*4)
	for i := 0; i < w*h; i++ {
		frame[i*4], frame[i*4+1], frame[i*4+2] = 40, 120, 220 // B G R: an orange
	}
	ok, why := TurboJPEG()
	t.Logf("libjpeg-turbo: %v (%s)", ok, why)
	pic, err := encodeBGRX(frame, w, h, w*4, 75)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(pic))
	if err != nil || img.Bounds().Dx() != w || img.Bounds().Dy() != h {
		t.Fatalf("decode %v %v", img, err)
	}
	r, g, b, _ := img.At(w/2, h/2).RGBA()
	if d := int(r>>8) - 220; d < -8 || d > 8 || int(g>>8) < 112 || int(g>>8) > 128 || int(b>>8) > 48 || int(b>>8) < 32 {
		t.Fatalf("colour %d %d %d", r>>8, g>>8, b>>8)
	}
	if ok {
		t0 := time.Now()
		for i := 0; i < 20; i++ {
			_, _ = encodeBGRX(frame, w, h, w*4, 75)
		}
		t.Logf("libjpeg-turbo %v a frame of %d×%d", time.Since(t0)/20, w, h)
	}
}

// TestRealDisplay drives a real Xvfb in-process where one is installed: the size, a picture within its budget, the
// pointer, keys and typing; skipped without Xvfb (CI's root step runs a whole lent desktop through it).
func TestRealDisplay(t *testing.T) {
	xvfb, err := exec.LookPath("Xvfb")
	if err != nil {
		t.Skip("no Xvfb here")
	}
	n := 90 + os.Getpid()%9
	name := ":" + strconv.Itoa(n)
	x := exec.Command(xvfb, name, "-screen", "0", "800x600x24", "-nolisten", "tcp")
	if err := x.Start(); err != nil {
		t.Skip("Xvfb did not start:", err)
	}
	defer func() { _ = x.Process.Kill(); _, _ = x.Process.Wait() }()
	var d *Display
	for i := 0; i < 50 && d == nil; i++ {
		time.Sleep(100 * time.Millisecond)
		d, _ = Open(name)
	}
	if d == nil {
		t.Skip("could not open the test display")
	}
	defer d.Close()
	if w, h := d.Size(); w != 800 || h != 600 {
		t.Fatalf("size %d×%d", w, h)
	}
	capture, encoder := d.Paths()
	t.Logf("capture %s, encoder %s", capture, encoder)
	if os.Getenv("OPENVIBE_EXPECT_FAST") == "1" && (capture != "shm" || encoder != "libjpeg-turbo") {
		t.Fatalf("expected MIT-SHM and libjpeg-turbo, got %s and %s", capture, encoder)
	}
	t0 := time.Now()
	for i := 0; i < 10; i++ {
		if _, _, _, err := d.Shot(0, 0); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("a full 800×600 picture: %v", time.Since(t0)/10)
	pic, w, h, err := d.Shot(400, 20000)
	if err != nil || w != 400 || h != 300 || len(pic) > 20000 {
		t.Fatalf("shot %d×%d %d bytes %v", w, h, len(pic), err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(pic)); err != nil {
		t.Fatalf("not a JPEG: %v", err)
	}
	if out, err := d.Xdotool([]string{"xdotool", "getdisplaygeometry"}); err != nil || out != "800 600\n" {
		t.Fatalf("geometry %q %v", out, err)
	}
	for _, argv := range [][]string{
		{"xdotool", "mousemove", "10", "20"}, {"xdotool", "mousemove", "10", "20", "click", "1"}, {"xdotool", "click", "--repeat", "2", "3"},
		{"xdotool", "key", "--", "ctrl+a"}, {"xdotool", "type", "--delay", "12", "--", "Hello, World!"},
	} {
		if _, err := d.Xdotool(argv); err != nil {
			t.Fatalf("%v: %v", argv, err)
		}
	}
	if _, err := d.Xdotool([]string{"xdotool", "type", "--", "héllo"}); err != ErrFallback {
		t.Fatalf("a character with no key falls back to xdotool: %v", err)
	}
	if _, err := d.Xdotool([]string{"xdotool", "windowactivate", "1"}); err != ErrFallback {
		t.Fatalf("an unknown verb falls back: %v", err)
	}
}
