package stream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/OpenVibers/OpenVibe.Node/internal/video"
)

// Capture kinds, one per platform the design names plus the browser desk's own surface and a synthetic source for the
// probe and tests.
const (
	CaptureX11          = "x11grab"
	CapturePipeWire     = "pipewire"
	CaptureDDAGrab      = "ddagrab"
	CaptureAVFoundation = "avfoundation"
	CaptureBrowser      = "browser"
	CaptureTest         = "test"
)

// Capture is how the core grabs the desk's picture. The computer plugin builds it (it knows the desk's display and
// whether it is a browser desk) and the core runs ffmpeg with it.
type Capture struct {
	Kind     string `json:"kind"`
	Display  string `json:"display,omitempty"`  // x11grab/pipewire: :99
	Pipe     string `json:"pipe,omitempty"`     // browser: the FIFO the page's screencast writes JPEGs into
	Device   string `json:"device,omitempty"`   // avfoundation: "1:none"
	PipeWire string `json:"pipewire,omitempty"` // pipewire: the node id, when the plugin has one
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
}

// EncodeOptions is the design's encoder recipe. The zero value is 1080p30, CBR 4 Mb/s, a keyframe every two seconds,
// 0 B-frames, zerolatency and stillimage, and VFR 5–30 with duplicate frames dropped.
type EncodeOptions struct {
	Width, Height int
	FPS           int
	BitrateKbps   int
	Keyframe      time.Duration
	// NoDecimate keeps every captured frame (probe and tests); the desk otherwise drops duplicates, which is what makes
	// the stream variable-rate.
	NoDecimate bool
	// Still keeps x264's stillimage tuning (mostly static screens). Off for the probe, where the QR moves.
	Still bool
}

// DefaultEncode is the recipe measured in the design.
func DefaultEncode() EncodeOptions {
	return EncodeOptions{Width: 1920, Height: 1080, FPS: 30, BitrateKbps: 4000, Keyframe: 2 * time.Second, Still: true}
}

// LowBandwidth is the 720p rung the desk falls to on a low bandwidth estimate.
func (o EncodeOptions) LowBandwidth() EncodeOptions {
	o.Width, o.Height = 1280, 720
	return o
}

func (o EncodeOptions) withDefaults() EncodeOptions {
	if o.FPS <= 0 {
		o.FPS = 30
	}
	if o.Keyframe <= 0 {
		o.Keyframe = 2 * time.Second
	}
	if o.BitrateKbps <= 0 {
		o.BitrateKbps = 4000
	}
	if o.Width <= 0 || o.Height <= 0 {
		o.Width, o.Height = 1920, 1080
	}
	o.Width, o.Height = even(o.Width), even(o.Height)
	return o
}

func even(n int) int {
	if n%2 != 0 {
		return n - 1
	}
	return n
}

// Args builds the ffmpeg command line for a capture and the design's recipe. It is pure so the recipe can be checked
// without a display or a camera.
func Args(ffmpeg string, c Capture, o EncodeOptions) ([]string, error) {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	o = o.withDefaults()
	if c.Width > 0 && c.Height > 0 { // the desk's own size, unless the caller asked for a ladder rung
		if o.Width == 1920 && o.Height == 1080 {
			o.Width, o.Height = even(c.Width), even(c.Height)
		}
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}

	var filter []string
	switch c.Kind {
	case CaptureX11:
		display := c.Display
		if display == "" {
			return nil, errors.New("stream: x11grab needs a display (:N)")
		}
		if !strings.Contains(display, ".") {
			display += ".0" // the plugin may already name the screen (":90.0")
		}
		args = append(args, "-f", "x11grab", "-draw_mouse", "1", "-framerate", strconv.Itoa(o.FPS),
			"-video_size", fmt.Sprintf("%dx%d", o.Width, o.Height), "-i", display)
	case CapturePipeWire:
		// ffmpeg 7.1's pipewire input; the plugin only chooses it when ffmpeg reports the device.
		node := c.PipeWire
		if node == "" {
			return nil, errors.New("stream: pipewire needs a node id")
		}
		args = append(args, "-f", "pipewire", "-framerate", strconv.Itoa(o.FPS), "-i", node)
	case CaptureDDAGrab:
		args = append(args, "-f", "lavfi", "-i",
			fmt.Sprintf("ddagrab=output_idx=0:framerate=%d:video_size=%dx%d", o.FPS, o.Width, o.Height))
		filter = append(filter, "hwdownload", "format=bgra")
	case CaptureAVFoundation:
		device := c.Device
		if device == "" {
			device = "1:none"
		}
		args = append(args, "-f", "avfoundation", "-capture_cursor", "1", "-framerate", strconv.Itoa(o.FPS), "-i", device)
	case CaptureBrowser:
		if c.Pipe == "" {
			return nil, errors.New("stream: a browser desk needs its screencast pipe")
		}
		args = append(args, "-f", "image2pipe", "-framerate", strconv.Itoa(o.FPS), "-i", c.Pipe)
	case CaptureTest:
		args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=%d", o.Width, o.Height, o.FPS))
	default:
		return nil, fmt.Errorf("stream: unknown capture %q", c.Kind)
	}

	// Variable rate: drop frames that did not change. -fps_mode vfr keeps the capture's timestamps, so a still screen
	// costs about 5 frames a second and a moving one up to 30. The source's timing is the only clock; nothing is
	// duplicated to a fixed rate.
	if !o.NoDecimate {
		filter = append(filter, "mpdecimate=hi=64*12:lo=64*5:frac=0.33")
	}
	if len(filter) > 0 {
		args = append(args, "-vf", strings.Join(filter, ","))
	}
	// The cap, not a constant rate: -fps_mode vfr keeps the capture's own timestamps and -r would contradict it.
	args = append(args, "-fps_mode", "vfr")

	// H.264 main, no B-frames, zerolatency, keyframe every two seconds of wall clock even when frames were dropped.
	args = append(args,
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-tune", tune(o.Still),
		"-profile:v", "main",
		"-pix_fmt", "yuv420p",
		"-bf", "0",
		"-g", strconv.Itoa(o.FPS*int(o.Keyframe.Seconds())),
		"-keyint_min", strconv.Itoa(o.FPS*int(o.Keyframe.Seconds())),
		"-sc_threshold", "0",
		"-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%g)", o.Keyframe.Seconds()),
		"-b:v", fmt.Sprintf("%dk", o.BitrateKbps),
		"-maxrate", fmt.Sprintf("%dk", o.BitrateKbps),
		"-bufsize", fmt.Sprintf("%dk", o.BitrateKbps),
		"-x264-params", "nal-hrd=cbr",
		"-f", "h264", "pipe:1",
	)
	return args, nil
}

func tune(still bool) string {
	if still {
		return "zerolatency,stillimage"
	}
	return "zerolatency"
}

// FFmpegSource runs one ffmpeg capture+encode pipeline and hands its Annex B access units to the publisher. It restarts
// the child with backoff when it exits, like a camera source does.
type FFmpegSource struct {
	FFmpeg  string
	Capture Capture
	Options EncodeOptions
	Log     *slog.Logger
	keyReq  atomic.Bool
}

func (s *FFmpegSource) Name() string     { return "ffmpeg:" + s.Capture.Kind }
func (s *FFmpegSource) RequestKeyframe() { s.keyReq.Store(true) }

// Run restarts ffmpeg until ctx ends. A keyframe request cannot reach an already-running ffmpeg; the two-second
// force_key_frames expression covers a new viewer, and a restart covers the rest.
func (s *FFmpegSource) Run(ctx context.Context, sink func(video.Sample)) error {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := s.runOnce(ctx, sink)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		if s.Log != nil {
			s.Log.Warn("stream capture exited; restarting", "capture", s.Capture.Kind, "err", err, "in", backoff)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
	return nil
}

func (s *FFmpegSource) runOnce(ctx context.Context, sink func(video.Sample)) error {
	ff := s.FFmpeg
	if ff == "" {
		ff = "ffmpeg"
	}
	args, err := Args(ff, s.Capture, s.Options)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, ff, args...)
	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{w: &stderr, n: 4096}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	last := time.Now()
	perr := video.AccessUnits(out, func(au []byte, key bool) {
		now := time.Now()
		d := now.Sub(last)
		if d < time.Millisecond {
			d = time.Millisecond
		}
		if d > time.Second {
			d = time.Second
		}
		last = now
		sink(video.Sample{Data: au, Key: key, Duration: d})
	})
	werr := cmd.Wait()
	if perr != nil {
		return perr
	}
	if werr != nil {
		return fmt.Errorf("%w: %s", werr, strings.TrimSpace(stderr.String()))
	}
	return errors.New("ffmpeg exited")
}

type limitedWriter struct {
	w interface{ Write([]byte) (int, error) }
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	q := p
	if len(q) > l.n {
		q = q[:l.n]
	}
	l.n -= len(q)
	_, _ = l.w.Write(q)
	return len(p), nil
}

// probeCapture is the source the latency probe draws its QR clock on.
func probeCapture(w, h int) Capture { return Capture{Kind: CaptureTest, Width: w, Height: h} }
