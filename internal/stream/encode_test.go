package stream

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/OpenVibers/OpenVibe.Node/internal/video"
)

func hasArg(args []string, want string) bool {
	for i := 0; i < len(args); i++ {
		if args[i] == want {
			return true
		}
	}
	return false
}

func argAfter(args []string, flag string) string {
	out := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			out = args[i+1]
		}
	}
	return out
}

func TestEncoderRecipe(t *testing.T) {
	args, err := Args("ffmpeg", Capture{Kind: CaptureX11, Display: ":99", Width: 1600, Height: 900}, DefaultEncode())
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]string{
		"-bf":               "0",
		"-profile:v":        "main",
		"-preset":           "ultrafast",
		"-tune":             "zerolatency,stillimage",
		"-g":                "60",
		"-keyint_min":       "60",
		"-force_key_frames": "expr:gte(t,n_forced*2)",
		"-b:v":              "4000k",
		"-maxrate":          "4000k",
		"-bufsize":          "4000k",
		"-fps_mode":         "vfr",
		"-x264-params":      "nal-hrd=cbr",
		"-f":                "h264",
	}
	for flag, want := range checks {
		if got := argAfter(args, flag); got != want {
			t.Errorf("%s = %q, want %q (args %v)", flag, got, want, args)
		}
	}
	if !strings.Contains(argAfter(args, "-vf"), "mpdecimate") {
		t.Errorf("no duplicate-frame dropping: %v", args)
	}
	if got := argAfter(args, "-video_size"); got != "1600x900" {
		t.Errorf("capture size = %q", got)
	}
	if !hasArg(args, "pipe:1") {
		t.Errorf("no Annex B output: %v", args)
	}
}

func TestEncoderLowBandwidthRung(t *testing.T) {
	args, err := Args("ffmpeg", Capture{Kind: CaptureX11, Display: ":99", Width: 1920, Height: 1080},
		DefaultEncode().LowBandwidth())
	if err != nil {
		t.Fatal(err)
	}
	if got := argAfter(args, "-video_size"); got != "1280x720" {
		t.Errorf("720p rung = %q", got)
	}
}

func TestEachCaptureKind(t *testing.T) {
	cases := []Capture{
		{Kind: CaptureX11, Display: ":97", Width: 800, Height: 600},
		{Kind: CaptureX11, Display: ":97.0", Width: 800, Height: 600},
		{Kind: CapturePipeWire, PipeWire: "42", Width: 800, Height: 600},
		{Kind: CaptureDDAGrab, Width: 800, Height: 600},
		{Kind: CaptureAVFoundation, Device: "2:none"},
		{Kind: CaptureBrowser, Pipe: "/run/ov/page.fifo"},
		{Kind: CaptureTest, Width: 800, Height: 600},
	}
	for _, c := range cases {
		args, err := Args("ffmpeg", c, EncodeOptions{Width: 800, Height: 600, FPS: 30})
		if err != nil {
			t.Errorf("%s: %v", c.Kind, err)
			continue
		}
		joined := strings.Join(args, " ")
		switch c.Kind {
		case CaptureX11:
			if !strings.Contains(joined, "-f x11grab") || !strings.Contains(joined, ":97.0") || strings.Contains(joined, ":97.0.0") {
				t.Errorf("x11grab args: %s", joined)
			}
		case CapturePipeWire:
			if !strings.Contains(joined, "-f pipewire") || !strings.Contains(joined, " 42 ") {
				t.Errorf("pipewire args: %s", joined)
			}
		case CaptureDDAGrab:
			if !strings.Contains(joined, "ddagrab=output_idx=0") || !strings.Contains(argAfter(args, "-vf"), "hwdownload") {
				t.Errorf("ddagrab args: %s", joined)
			}
		case CaptureAVFoundation:
			if !strings.Contains(joined, "-f avfoundation") || !strings.Contains(joined, "2:none") {
				t.Errorf("avfoundation args: %s", joined)
			}
		case CaptureBrowser:
			if !strings.Contains(joined, "-f image2pipe") || !strings.Contains(joined, "/run/ov/page.fifo") {
				t.Errorf("browser args: %s", joined)
			}
		case CaptureTest:
			if !strings.Contains(joined, "testsrc2=") {
				t.Errorf("test args: %s", joined)
			}
		}
	}
}

func TestCaptureNeedsItsInput(t *testing.T) {
	if _, err := Args("ffmpeg", Capture{Kind: CaptureX11}, DefaultEncode()); err == nil {
		t.Error("x11grab without a display was accepted")
	}
	if _, err := Args("ffmpeg", Capture{Kind: CaptureBrowser}, DefaultEncode()); err == nil {
		t.Error("a browser desk without a pipe was accepted")
	}
	if _, err := Args("ffmpeg", Capture{Kind: "nope"}, DefaultEncode()); err == nil {
		t.Error("an unknown capture was accepted")
	}
}

func TestFFmpegSourcePublishesAccessUnits(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg on this machine")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	src := &FFmpegSource{FFmpeg: ff, Capture: Capture{Kind: CaptureTest, Width: 320, Height: 240},
		Options: EncodeOptions{Width: 320, Height: 240, FPS: 15, Keyframe: time.Second}}
	samples := make(chan video.Sample, 8)
	go func() {
		_ = src.Run(ctx, func(s video.Sample) {
			select {
			case samples <- s:
			default:
			}
		})
	}()
	select {
	case s := <-samples:
		if len(s.Data) == 0 || s.Duration <= 0 {
			t.Fatalf("empty sample: %+v", s)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no access units from ffmpeg")
	}
}
