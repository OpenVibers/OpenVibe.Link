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

func TestScaleBGRX(t *testing.T) {
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
	img := scaleBGRX(src, 4, 2, 2)
	if b := img.Bounds(); b.Dx() != 2 || b.Dy() != 1 {
		t.Fatalf("size %v", b)
	}
	if r, g, bl := img.Pix[0], img.Pix[1], img.Pix[2]; r != 0 || g != 0 || bl != 200 {
		t.Fatalf("left %d %d %d", r, g, bl)
	}
	if r, bl := img.Pix[4], img.Pix[6]; r != 100 || bl != 0 {
		t.Fatalf("right %d %d", r, bl)
	}
	full := scaleBGRX(src, 4, 2, 4)
	if full.Bounds().Dx() != 4 || full.Pix[2] != 200 || full.Pix[3] != 0xff {
		t.Fatal("full size: BGRX to RGBA")
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
