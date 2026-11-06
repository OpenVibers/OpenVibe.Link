package stream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/OpenVibers/OpenVibe.Node/internal/credentials"
	"github.com/OpenVibers/OpenVibe.Node/internal/video"
)

// Probe QR: each frame carries the millisecond clock the desk drew it. The headless viewer decodes it and subtracts
// the instant it was decoded, which is the glass-to-glass latency.
const (
	ProbeDefaultSeconds = 60
	ProbeDefaultWidth   = 640
	ProbeDefaultHeight  = 360
	ProbeDefaultFPS     = 10
)

// ProbeOptions configure `openvibe-node stream --probe`.
type ProbeOptions struct {
	WHIPURL  string
	Ticket   credentials.Secret
	Seconds  int
	Width    int
	Height   int
	FPS      int
	Loopback bool
	Log      *slog.Logger
	Now      func() time.Time
	// NewPublisher is for tests.
	NewPublisher func(video.Options) publisher
}

// ProbeSource draws a QR of the current millisecond clock in the top-left corner of every frame, on the built-in PCM
// encoder (no ffmpeg, no display). It is the clock the viewer reads.
type ProbeSource struct {
	W, H, FPS int
	Now       func() time.Time
	mu        sync.Mutex
	lastMS    int64
	keyReq    bool
}

func (p *ProbeSource) Name() string { return "probe" }

func (p *ProbeSource) RequestKeyframe() {
	p.mu.Lock()
	p.keyReq = true
	p.mu.Unlock()
}

// LastMS is the clock value of the last frame drawn.
func (p *ProbeSource) LastMS() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastMS
}

func (p *ProbeSource) Run(ctx context.Context, sink func(video.Sample)) error {
	if p.W <= 0 || p.H <= 0 {
		p.W, p.H = ProbeDefaultWidth, ProbeDefaultHeight
	}
	if p.FPS <= 0 {
		p.FPS = ProbeDefaultFPS
	}
	if p.Now == nil {
		p.Now = time.Now
	}
	w, h := mbSize(p.W), mbSize(p.H)
	enc, err := video.NewPCMEncoder(w, h)
	if err != nil {
		return err
	}
	frame := time.Second / time.Duration(p.FPS)
	tick := time.NewTicker(frame)
	defer tick.Stop()
	var prev *video.Frame
	for n := 0; ; n++ {
		now := p.Now()
		f := video.NewFrame(w, h)
		drawProbe(f, now.UnixMilli())
		p.mu.Lock()
		p.lastMS = now.UnixMilli()
		key := prev == nil || n%10 == 0 || p.keyReq
		p.keyReq = false
		p.mu.Unlock()
		var coded []int
		if !key {
			coded = video.ChangedMBs(prev, f, 4)
		}
		sink(video.Sample{Data: enc.Encode(f, key, coded), Key: key, Duration: frame})
		prev = f
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// mbSize rounds a picture to the PCM encoder's macroblock grid.
func mbSize(n int) int {
	if n < 16 {
		return 16
	}
	return n / 16 * 16
}

// drawProbe paints the QR on white with a quiet zone, plus a moving marker so the encoder sees motion.
func drawProbe(f *video.Frame, ms int64) {
	for i := range f.Y {
		f.Y[i] = 235
	}
	for i := range f.Cb {
		f.Cb[i], f.Cr[i] = 128, 128
	}
	q, err := qrcode.New(fmt.Sprintf("%d", ms), qrcode.Medium)
	if err != nil {
		return
	}
	bmp := q.Bitmap()
	// Four modules of quiet zone, then whole-pixel modules. At 640×360 the code is about 200 px square.
	scale := min(f.W, f.H) / (len(bmp) + 8)
	if scale < 2 {
		scale = 2
	}
	size := len(bmp) * scale
	x0, y0 := 8, 8
	for y := 0; y < size && y0+y < f.H; y++ {
		for x := 0; x < size && x0+x < f.W; x++ {
			if bmp[y/scale][x/scale] {
				f.Y[(y0+y)*f.W+(x0+x)] = 16
			}
		}
	}
	// A marker sweeping along the bottom shows the stream is moving (and keeps VFR from dropping every frame).
	pos := int(ms/100) % (f.W - 32)
	for y := f.H - 24; y < f.H-8; y++ {
		for x := pos; x < pos+32; x++ {
			f.Y[y*f.W+x] = 16
		}
	}
}

// Probe publishes the QR clock until seconds elapse (or ctx ends) and reports the frames written.
func Probe(ctx context.Context, o ProbeOptions) (int64, error) {
	if o.Seconds <= 0 {
		o.Seconds = ProbeDefaultSeconds
	}
	if o.Width <= 0 {
		o.Width = ProbeDefaultWidth
	}
	if o.Height <= 0 {
		o.Height = ProbeDefaultHeight
	}
	if o.FPS <= 0 {
		o.FPS = ProbeDefaultFPS
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if err := checkProbeURL(o.WHIPURL); err != nil {
		return 0, err
	}
	if o.Ticket.IsZero() || !strings.HasPrefix(o.Ticket.Reveal(), "wpt_") {
		return 0, errors.New("stream: --probe needs the stream's publish ticket (--ticket wpt_…)")
	}
	opts := video.Options{WHIPURL: o.WHIPURL, PublishKey: o.Ticket, Audio: true,
		UserAgent: "openvibe-node/stream-probe", IncludeLoopback: o.Loopback, Log: o.Log}
	src := &ProbeSource{W: o.Width, H: o.Height, FPS: o.FPS, Now: o.Now}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(o.Seconds)*time.Second)
	defer cancel()
	var pub publisher
	if o.NewPublisher != nil {
		pub = o.NewPublisher(opts)
	} else {
		pub = video.NewPublisher(opts)
	}
	done := make(chan struct{})
	go func() { pub.Run(ctx, src); close(done) }()
	start := time.Now()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			<-done
			return pub.Frames(), nil
		case <-tick.C:
			o.Log.Info("stream probe publishing", "elapsed", time.Since(start).Round(time.Second).String())
		}
	}
}

func checkProbeURL(raw string) error {
	if raw == "" {
		return errors.New("stream: --probe needs a WHIP URL (--whip-url)")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return errors.New("stream: --whip-url is not a URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname())) {
		return errors.New("stream: --whip-url must be https")
	}
	return nil
}
