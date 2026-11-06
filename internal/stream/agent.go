package stream

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/OpenVibers/OpenVibe.Node/internal/credentials"
	"github.com/OpenVibers/OpenVibe.Node/internal/video"
)

// liveAPI is the part of Client the agent uses; tests substitute it.
type liveAPI interface {
	State(context.Context, string, credentials.Secret) (State, error)
	Renew(context.Context, string, credentials.Secret) (Publish, error)
	Curtain(context.Context, string, credentials.Secret, bool, string) error
	PostFrame(context.Context, string, credentials.Secret, []byte) error
	End(context.Context, string, credentials.Secret) error
	Revoke(context.Context, string, credentials.Secret) error
}

// publisher is the part of video.Publisher the agent uses; tests substitute it.
type publisher interface {
	Run(context.Context, video.Source)
	SetPaused(bool)
	SetPublishKey(credentials.Secret)
	State() string
	Frames() int64
	LastError() string
}

// AgentOptions configure one desk stream.
type AgentOptions struct {
	Client  liveAPI
	ID      string
	Publish Publish
	Capture Capture
	Encode  EncodeOptions
	FFmpeg  string
	Log     *slog.Logger

	// Snapshot returns a JPEG of the desk for the card while nobody watches. The node wires it to the computer plugin
	// (a browser desk's page) or to a one-shot ffmpeg capture (a display).
	Snapshot func(context.Context) ([]byte, error)
	// CurtainJPEG is the "Private screen" picture posted as the snapshot while the curtain is up.
	CurtainJPEG []byte
	// CurtainFrames are pre-encoded H.264 access units of the curtain, looped while it is up.
	CurtainFrames []video.Sample

	StateEvery    time.Duration // default 10 s
	SnapshotEvery time.Duration // default 5 s
	RenewAfter    time.Duration // default 3 h (the design's renewal point)
	Now           func() time.Time

	// NewPublisher and NewSource are for tests; production uses video.NewPublisher and FFmpegSource.
	NewPublisher func(video.Options) publisher
	NewSource    func(Capture, EncodeOptions) video.Source
}

// Agent is one desk stream: a WHIP publisher, the capture pipeline, Live's state, the curtain and the ticket.
type Agent struct {
	opt AgentOptions
	log *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	pub    publisher
	ds     *deskSource

	mu          sync.Mutex
	ticket      credentials.Secret
	encode      bool
	curtain     bool
	reason      string
	viewers     int
	ended       bool
	revoked     bool
	expiresAt   time.Time
	retries     int
	downshifted bool

	snapMu sync.Mutex
}

// NewAgent validates and returns an agent; Run starts it.
func NewAgent(o AgentOptions) (*Agent, error) {
	if o.Client == nil || o.ID == "" {
		return nil, errors.New("stream: an agent needs a client and a stream id")
	}
	if err := checkID(o.ID); err != nil {
		return nil, err
	}
	if o.Publish.WHIPURL == "" || o.Publish.Ticket.IsZero() {
		return nil, errors.New("stream: an agent needs a WHIP URL and a publish ticket")
	}
	if o.Publish.ExpiresAt.IsZero() {
		return nil, errors.New("stream: the publish part has no expiry")
	}
	if o.StateEvery <= 0 {
		o.StateEvery = 10 * time.Second
	}
	if o.SnapshotEvery <= 0 {
		o.SnapshotEvery = 5 * time.Second
	}
	if o.RenewAfter <= 0 {
		o.RenewAfter = 3 * time.Hour
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.NewPublisher == nil {
		o.NewPublisher = func(vo video.Options) publisher { return video.NewPublisher(vo) }
	}
	if o.NewSource == nil {
		o.NewSource = func(c Capture, e EncodeOptions) video.Source {
			return &FFmpegSource{FFmpeg: o.FFmpeg, Capture: c, Options: e, Log: o.Log}
		}
	}
	a := &Agent{opt: o, log: o.Log.With("component", "desk-stream"), ticket: o.Publish.Ticket, encode: true,
		expiresAt: o.Publish.ExpiresAt}
	a.ds = &deskSource{a: a}
	return a, nil
}

// Run publishes until ctx ends or the stream ends. It ends the stream at Live (so the card stays), unless Revoke was
// called, which is the kill switch.
func (a *Agent) Run(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	a.mu.Lock()
	a.ctx, a.cancel = ctx, cancel
	revoked := a.revoked
	a.mu.Unlock()
	if revoked {
		cancel()
		return nil
	}
	if len(a.opt.CurtainFrames) == 0 || len(a.opt.CurtainJPEG) == 0 {
		cctx, ccancel := context.WithTimeout(ctx, 20*time.Second)
		frames, jpg := MakeCurtain(cctx, a.opt.FFmpeg, a.opt.Capture.Width, a.opt.Capture.Height)
		ccancel()
		if len(a.opt.CurtainFrames) == 0 {
			a.opt.CurtainFrames = frames
		}
		if len(a.opt.CurtainJPEG) == 0 {
			a.opt.CurtainJPEG = jpg
		}
	}
	pub := a.opt.NewPublisher(video.Options{WHIPURL: a.opt.Publish.WHIPURL, PublishKey: a.ticket,
		Audio: true, UserAgent: "openvibe-node/stream", Log: a.opt.Log})
	a.mu.Lock()
	a.pub = pub
	a.mu.Unlock()
	defer cancel()
	go pub.Run(ctx, a.ds)

	state := time.NewTicker(a.opt.StateEvery)
	defer state.Stop()
	snap := time.NewTicker(a.opt.SnapshotEvery)
	defer snap.Stop()
	renew := time.NewTimer(a.renewDelay())
	defer renew.Stop()

	a.log.Info("desk stream started", "stream", a.opt.ID, "capture", a.opt.Capture.Kind)
	a.postSnapshot(ctx) // the card has a face before the first state poll

	for {
		select {
		case <-ctx.Done():
			a.finish()
			return nil
		case <-renew.C:
			a.renew(ctx)
			renew.Reset(a.renewDelay())
		case <-state.C:
			if err := a.poll(ctx); err != nil {
				a.log.Warn("desk stream state poll failed", "stream", a.opt.ID, "err", err)
			}
		case <-snap.C:
			if !a.encodeNow() {
				a.postSnapshot(ctx)
			}
		}
	}
}

func (a *Agent) renewDelay() time.Duration {
	// The design renews at three hours; never wait past the ticket's own expiry (with a margin for the round trip).
	a.mu.Lock()
	exp := a.expiresAt
	a.mu.Unlock()
	if d := a.opt.RenewAfter; d > 0 {
		if left := exp.Sub(a.opt.Now()) - 15*time.Minute; left > 0 && left < d {
			return left
		}
		return d
	}
	return 3 * time.Hour
}

func (a *Agent) poll(ctx context.Context) error {
	st, err := a.opt.Client.State(ctx, a.opt.ID, a.ticketNow())
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.viewers = st.Viewers
	curtainUp := a.curtain
	a.mu.Unlock()
	if st.Curtain.Up && !curtainUp {
		// The owner raised it on another surface. Only the desk may bring it down.
		a.SetCurtain(true, reasonOr(st.Curtain.Reason))
	}
	if st.Encode != a.encodeNow() {
		a.SetEncode(st.Encode)
	}
	a.checkBandwidth()
	if st.EndsAt != nil && !st.EndsAt.After(a.opt.Now()) {
		a.log.Info("desk stream ended at Live", "stream", a.opt.ID)
		a.cancel()
	}
	return nil
}

func reasonOr(r string) string {
	if validReason(r) {
		return r
	}
	return "detector"
}

func (a *Agent) renew(ctx context.Context) {
	p, err := a.opt.Client.Renew(ctx, a.opt.ID, a.ticketNow())
	if err != nil {
		a.log.Warn("desk stream ticket renewal failed; retrying in a minute", "stream", a.opt.ID, "err", err)
		a.mu.Lock()
		runCtx := a.ctx
		a.mu.Unlock()
		if runCtx != nil {
			time.AfterFunc(time.Minute, func() {
				if runCtx.Err() == nil {
					a.renew(runCtx)
				}
			})
		}
		return
	}
	a.mu.Lock()
	a.ticket = p.Ticket
	if !p.ExpiresAt.IsZero() {
		a.expiresAt = p.ExpiresAt
	}
	a.mu.Unlock()
	a.pub.SetPublishKey(p.Ticket)
	a.log.Info("desk stream ticket renewed", "stream", a.opt.ID, "expires_at", p.ExpiresAt.UTC().Format(time.RFC3339))
}

// checkBandwidth is the design's 720p rung: two WHIP retries close together mean the link is struggling, so the
// capture restarts at 720p. It never goes back up (S2's simulcast is the real answer).
func (a *Agent) checkBandwidth() {
	pub := a.pub
	if pub == nil {
		return
	}
	if pub.State() != video.StateRetrying {
		a.mu.Lock()
		a.retries = 0
		a.mu.Unlock()
		return
	}
	a.mu.Lock()
	if a.downshifted {
		a.mu.Unlock()
		return
	}
	a.retries++
	down := a.retries >= 2
	if down {
		a.downshifted = true
		a.opt.Encode = a.opt.Encode.LowBandwidth()
	}
	a.mu.Unlock()
	if down {
		a.log.Info("desk stream downshifted to 720p after repeated WHIP retries", "stream", a.opt.ID)
		a.ds.restart()
	}
}

func (a *Agent) ticketNow() credentials.Secret {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ticket
}

func (a *Agent) encodeNow() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.encode
}

func (a *Agent) curtainNow() (bool, []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.curtain, a.opt.CurtainJPEG
}

// SetEncode is Live's encode flag: false stops the capture and turns the stream into snapshots. The WHIP session and
// its silent audio stay up, so Live does not see the publisher drop.
func (a *Agent) SetEncode(on bool) {
	a.mu.Lock()
	if a.encode == on {
		a.mu.Unlock()
		return
	}
	a.encode = on
	a.mu.Unlock()
	a.ds.setEncode(on)
	a.pub.SetPaused(!on)
	a.log.Info("desk stream encoding changed", "stream", a.opt.ID, "encode", on)
}

// SetCurtain raises or lowers the desk's privacy screen. Raising blanks every frame and reports it to Live; lowering
// restarts the capture so the first frame a viewer sees is a keyframe.
func (a *Agent) SetCurtain(up bool, reason string) {
	a.mu.Lock()
	if a.curtain == up {
		a.mu.Unlock()
		return
	}
	a.curtain = up
	if up {
		a.reason = reasonOr(reason)
	} else {
		a.reason = reasonOr(reason)
	}
	r := a.reason
	a.mu.Unlock()
	a.ds.curtainChanged(up)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.opt.Client.Curtain(ctx, a.opt.ID, a.ticketNow(), up, r); err != nil {
			a.log.Warn("desk stream curtain report failed", "stream", a.opt.ID, "up", up, "err", err)
		}
	}()
	a.log.Info("desk stream curtain", "stream", a.opt.ID, "up", up, "reason", r)
}

// Stop ends an ordinary stream: Live keeps the card, with its last frame and worked time.
func (a *Agent) Stop() {
	a.mu.Lock()
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Revoke is the kill switch: Live ends the WHIP session at once and no End is sent afterwards.
func (a *Agent) Revoke() {
	a.mu.Lock()
	a.revoked = true
	a.mu.Unlock()
	// Best effort: the caller is a latch, so it must not wait on the network.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.opt.Client.Revoke(ctx, a.opt.ID, a.ticketNow()); err != nil {
			a.log.Warn("desk stream revoke failed", "stream", a.opt.ID, "err", err)
		}
	}()
	a.mu.Lock()
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *Agent) finish() {
	a.mu.Lock()
	if a.ended || a.revoked {
		a.mu.Unlock()
		return
	}
	a.ended = true
	a.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.opt.Client.End(ctx, a.opt.ID, a.ticketNow()); err != nil {
		a.log.Warn("desk stream end failed", "stream", a.opt.ID, "err", err)
	}
}

func (a *Agent) postSnapshot(ctx context.Context) {
	if a.opt.Snapshot == nil {
		return
	}
	if !a.snapMu.TryLock() {
		return // a slow capture must not pile snapshots up
	}
	defer a.snapMu.Unlock()
	var (
		frame []byte
		err   error
	)
	if up, blank := a.curtainNow(); up {
		frame = blank
	} else {
		frame, err = a.opt.Snapshot(ctx)
	}
	if err != nil || len(frame) == 0 {
		if err != nil {
			a.log.Warn("desk stream snapshot failed", "stream", a.opt.ID, "err", err)
		}
		return
	}
	if err := a.opt.Client.PostFrame(ctx, a.opt.ID, a.ticketNow(), frame); err != nil {
		a.log.Warn("desk stream snapshot post failed", "stream", a.opt.ID, "err", err)
	}
}

// Status is what `openvibe-node status` shows for one stream.
type Status struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	Encode    bool   `json:"encode"`
	Curtain   bool   `json:"curtain"`
	Viewers   int    `json:"viewers"`
	Frames    int64  `json:"frames"`
	LastError string `json:"last_error,omitempty"`
}

// Status reports the stream without a ticket.
func (a *Agent) Status() Status {
	a.mu.Lock()
	st := Status{ID: a.opt.ID, Encode: a.encode, Curtain: a.curtain, Viewers: a.viewers}
	pub := a.pub
	a.mu.Unlock()
	if pub != nil {
		st.State = pub.State()
		st.Frames = pub.Frames()
		st.LastError = pub.LastError()
	}
	return st
}

// ---- the source the publisher runs ----

// deskSource feeds the publisher. It runs the capture only while Live wants video, and loops the pre-encoded curtain
// while the curtain is up, so no captured frame can slip through.
type deskSource struct {
	a *Agent

	mu      sync.Mutex
	runCtx  context.Context
	sink    func(video.Sample)
	encStop context.CancelFunc
	encGen  int
	encRun  bool
	up      bool
}

func (d *deskSource) Name() string     { return "desk-stream" }
func (d *deskSource) RequestKeyframe() { d.a.ds.restart() }

func (d *deskSource) Run(ctx context.Context, sink func(video.Sample)) error {
	d.mu.Lock()
	d.runCtx, d.sink = ctx, sink
	up := d.up
	enc := d.a.encodeNow()
	d.mu.Unlock()
	if enc && !up {
		d.startEncoder()
	}
	// While the curtain is up the publisher gets only the curtain loop; the capture keeps running so the picture is
	// back the instant the detectors clear.
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			d.stopEncoder()
			return nil
		case <-tick.C:
			if d.isUp() {
				d.forwardCurtain()
			}
		}
	}
}

func (d *deskSource) isUp() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.up
}

func (d *deskSource) forwardCurtain() {
	d.mu.Lock()
	sink := d.sink
	frames := d.a.opt.CurtainFrames
	d.mu.Unlock()
	if sink == nil || len(frames) == 0 {
		return
	}
	d.mu.Lock()
	f := frames[0]
	d.mu.Unlock()
	sink(f)
}

func (d *deskSource) startEncoder() {
	d.mu.Lock()
	if d.encRun || d.runCtx == nil || d.up {
		d.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(d.runCtx)
	d.encGen++
	gen := d.encGen
	d.encRun, d.encStop = true, cancel
	d.a.mu.Lock()
	enc := d.a.opt.Encode
	d.a.mu.Unlock()
	src := d.a.opt.NewSource(d.a.opt.Capture, enc)
	d.mu.Unlock()
	go func() {
		_ = src.Run(ctx, func(s video.Sample) {
			d.mu.Lock()
			up, out := d.up, d.sink
			d.mu.Unlock()
			if up || out == nil {
				return
			}
			out(s)
		})
		d.mu.Lock()
		if d.encGen == gen {
			d.encRun, d.encStop = false, nil
		}
		d.mu.Unlock()
	}()
}

func (d *deskSource) stopEncoder() {
	d.mu.Lock()
	cancel := d.encStop
	d.encRun, d.encStop = false, nil
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (d *deskSource) restart() {
	d.mu.Lock()
	was := d.encRun
	d.mu.Unlock()
	if !was {
		return
	}
	d.stopEncoder()
	time.Sleep(10 * time.Millisecond) // let the old ffmpeg release the display
	d.startEncoder()
}

func (d *deskSource) setEncode(on bool) {
	if on {
		d.startEncoder()
	} else {
		d.stopEncoder()
	}
}

func (d *deskSource) curtainChanged(up bool) {
	d.mu.Lock()
	d.up = up
	d.mu.Unlock()
	if up {
		d.stopEncoder()
	} else if d.a.encodeNow() {
		d.startEncoder()
	}
}
