package node

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// jpegConfig decodes the JPEG the snapshot path produced (it is a normal image, not an encoder detail).
func jpegConfig(b []byte) (image.Config, error) {
	return jpeg.DecodeConfig(bytes.NewReader(b))
}

func TestDeskStreamStatusAndStopAreSafeBeforeAStream(t *testing.T) {
	n := &Node{}
	if st := n.deskStreamStatus(); st != nil {
		t.Fatalf("status before a stream = %+v", st)
	}
	if n.stopDeskStream(false) || n.stopDeskStream(true) {
		t.Fatal("stop reported a stream that never ran")
	}
	n.deskCurtainEvent("computer", map[string]any{"up": true, "reason": "manual"})
}

func TestPNGToJPEG(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2000, 1000))
	for y := 0; y < 1000; y++ {
		for x := 0; x < 2000; x++ {
			src.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 40, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	out, err := pngToJPEG(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpegConfig(out)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != 1280 {
		t.Fatalf("snapshot width = %d, want 1280", cfg.Width)
	}
	if len(out) > 200*1024 {
		t.Fatalf("snapshot is %d bytes", len(out))
	}
}
