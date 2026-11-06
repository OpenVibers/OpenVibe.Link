package stream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenVibers/OpenVibe.Node/internal/credentials"
	"github.com/OpenVibers/OpenVibe.Node/internal/video"
)

type fakeAPI struct {
	mu       sync.Mutex
	state    func() State
	renews   []credentials.Secret
	newKey   string
	curtains []string
	frames   int32
	ended    int32
	revoked  int32
}

func (f *fakeAPI) State(context.Context, string, credentials.Secret) (State, error) {
	if f.state != nil {
		return f.state(), nil
	}
	return State{Encode: true, Curtain: Curtain{}}, nil
}
func (f *fakeAPI) Renew(_ context.Context, _ string, old credentials.Secret) (Publish, error) {
	f.mu.Lock()
	f.renews = append(f.renews, old)
	f.mu.Unlock()
	return Publish{WHIPURL: "https://whip.example/x", Ticket: credentials.NewSecret(f.newKey),
		ExpiresAt: time.Now().Add(4 * time.Hour)}, nil
}
func (f *fakeAPI) Curtain(_ context.Context, _ string, _ credentials.Secret, up bool, reason string) error {
	f.mu.Lock()
	f.curtains = append(f.curtains, reason)
	f.mu.Unlock()
	return nil
}
func (f *fakeAPI) PostFrame(context.Context, string, credentials.Secret, []byte) error {
	atomic.AddInt32(&f.frames, 1)
	return nil
}
func (f *fakeAPI) End(context.Context, string, credentials.Secret) error {
	atomic.AddInt32(&f.ended, 1)
	return nil
}
func (f *fakeAPI) Revoke(context.Context, string, credentials.Secret) error {
	atomic.AddInt32(&f.revoked, 1)
	return nil
}

type fakePub struct {
	paused atomic.Bool
	muted  atomic.Bool
	key    atomic.Value
	state  atomic.Value
}

func (p *fakePub) Run(ctx context.Context, src video.Source) {
	go func() { _ = src.Run(ctx, func(video.Sample) {}) }()
	<-ctx.Done()
}
func (p *fakePub) SetPaused(v bool) { p.paused.Store(v) }
func (p *fakePub) SetMuted(v bool)  { p.muted.Store(v) }
func (p *fakePub) SetPublishKey(k credentials.Secret) {
	if !k.IsZero() {
		p.key.Store(k)
	}
}
func (p *fakePub) State() string {
	if s, _ := p.state.Load().(string); s != "" {
		return s
	}
	return video.StateLive
}
func (p *fakePub) Frames() int64     { return 1 }
func (p *fakePub) LastError() string { return "" }

type fakeSource struct {
	out  chan string
	once sync.Once
}

func (s *fakeSource) Name() string     { return "fake" }
func (s *fakeSource) RequestKeyframe() {}
func (s *fakeSource) Run(ctx context.Context, sink func(video.Sample)) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case v := <-s.out:
			sink(video.Sample{Data: []byte(v), Duration: 10 * time.Millisecond})
		}
	}
}

func newTestAgent(t *testing.T, api *fakeAPI, o AgentOptions) (*Agent, *fakePub) {
	t.Helper()
	if o.ID == "" {
		o.ID = testID
	}
	if o.Publish.WHIPURL == "" {
		o.Publish = Publish{WHIPURL: "https://whip.example/whip/agent/" + testID, Ticket: credentials.NewSecret(testTicket),
			ExpiresAt: time.Now().Add(4 * time.Hour)}
	}
	if o.Capture.Kind == "" {
		o.Capture = Capture{Kind: CaptureTest, Width: 320, Height: 240}
	}
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.Snapshot == nil {
		o.Snapshot = func(context.Context) ([]byte, error) { return blackJPEG(64, 36), nil }
	}
	var pub *fakePub
	if o.NewPublisher == nil {
		pub = &fakePub{}
		o.NewPublisher = func(video.Options) publisher { return pub }
	}
	if o.NewSource == nil {
		o.NewSource = func(Capture, EncodeOptions) video.Source { return &fakeSource{out: make(chan string, 8)} }
	}
	a, err := NewAgent(o)
	if err != nil {
		t.Fatal(err)
	}
	return a, pub
}

func TestAgentSnapshotModeWhenLiveSaysNoEncode(t *testing.T) {
	var no atomic.Bool
	api := &fakeAPI{}
	api.state = func() State {
		if no.Load() {
			return State{Encode: false}
		}
		return State{Encode: true}
	}
	a, pub := newTestAgent(t, api, AgentOptions{Client: api, StateEvery: 10 * time.Millisecond,
		SnapshotEvery: 15 * time.Millisecond, CurtainFrames: []video.Sample{{Data: []byte("C"), Key: true}},
		CurtainJPEG: blackJPEG(16, 9)})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	no.Store(true)
	deadline := time.Now().Add(5 * time.Second)
	for a.Status().Encode {
		if time.Now().After(deadline) {
			t.Fatal("encode never went false")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond)
	before := atomic.LoadInt32(&api.frames)
	time.Sleep(80 * time.Millisecond)
	after := atomic.LoadInt32(&api.frames)
	if after <= before {
		t.Fatalf("no snapshot while encode is false (%d → %d)", before, after)
	}
	if !pub.paused.Load() {
		t.Fatal("the publisher was not paused in snapshot mode")
	}
	cancel()
	<-done
	if atomic.LoadInt32(&api.ended) != 1 {
		t.Fatalf("End called %d times", api.ended)
	}
}

func TestAgentRenewsAtThreeHours(t *testing.T) {
	api := &fakeAPI{newKey: testTicket[:len(testTicket)-1] + "Z"}
	a, pub := newTestAgent(t, api, AgentOptions{Client: api, Now: func() time.Time { return time.Unix(1_800_000_000, 0) }})
	if d := a.renewDelay(); d != 3*time.Hour {
		t.Fatalf("renewDelay = %v, want 3h", d)
	}
	// A ticket that dies sooner than three hours is renewed before it does.
	a.expiresAt = a.opt.Now().Add(1 * time.Hour)
	if d := a.renewDelay(); d != 45*time.Minute {
		t.Fatalf("renewDelay with a one-hour ticket = %v, want 45m", d)
	}
	// And the renewal replaces the key the next WHIP session reconnects with.
	a2, pub2 := newTestAgent(t, api, AgentOptions{Client: api, RenewAfter: 20 * time.Millisecond,
		CurtainFrames: []video.Sample{{Data: []byte("C"), Key: true}}, CurtainJPEG: blackJPEG(16, 9)})
	ctx, cancel := context.WithCancel(context.Background())
	go a2.Run(ctx)
	time.Sleep(80 * time.Millisecond)
	cancel()
	api.mu.Lock()
	n := len(api.renews)
	api.mu.Unlock()
	if n == 0 {
		t.Fatal("the ticket was never renewed")
	}
	got, _ := pub2.key.Load().(credentials.Secret)
	if got.Reveal() != api.newKey {
		t.Fatalf("publisher key after renewal = %v", got)
	}
	_ = pub
}

func TestDeskSourceCurtainBlanksEveryFrame(t *testing.T) {
	api := &fakeAPI{}
	src := &fakeSource{out: make(chan string, 8)}
	a, _ := newTestAgent(t, api, AgentOptions{Client: api,
		NewSource:     func(Capture, EncodeOptions) video.Source { return src },
		CurtainFrames: []video.Sample{{Data: []byte("CURTAIN"), Key: true, Duration: 100 * time.Millisecond}}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var seen []string
	done := make(chan struct{})
	go func() {
		_ = a.ds.Run(ctx, func(s video.Sample) {
			mu.Lock()
			seen = append(seen, string(s.Data))
			mu.Unlock()
		})
		close(done)
	}()
	send := func(v string) {
		select {
		case src.out <- v:
		case <-time.After(time.Second):
			t.Fatal("source blocked")
		}
	}
	time.Sleep(20 * time.Millisecond)
	send("RAW1")
	time.Sleep(20 * time.Millisecond)
	a.SetCurtain(true, "manual")
	time.Sleep(20 * time.Millisecond)
	send("RAW2")
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	got := append([]string(nil), seen...)
	mu.Unlock()
	if !containsStr(got, "RAW1") {
		t.Fatalf("no frame before the curtain: %v", got)
	}
	if containsStr(got, "RAW2") {
		t.Fatalf("a captured frame leaked through the curtain: %v", got)
	}
	if !containsStr(got, "CURTAIN") {
		t.Fatalf("the curtain frame was not sent: %v", got)
	}
	a.SetCurtain(false, "manual")
	time.Sleep(30 * time.Millisecond)
	send("RAW3")
	time.Sleep(30 * time.Millisecond)
	mu.Lock()
	got = append([]string(nil), seen...)
	mu.Unlock()
	if !containsStr(got, "RAW3") {
		t.Fatalf("no frame after the curtain came down: %v", got)
	}
	cancel()
	<-done
}

func TestTheTicketNeverReachesALogOrStatus(t *testing.T) {
	var buf bytes.Buffer
	api := &fakeAPI{}
	a, _ := newTestAgent(t, api, AgentOptions{Client: api, Log: slog.New(slog.NewTextHandler(&buf, nil)),
		CurtainFrames: []video.Sample{{Data: []byte("C"), Key: true}}, CurtainJPEG: blackJPEG(16, 9)})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	time.Sleep(40 * time.Millisecond)
	cancel()
	<-done
	if strings.Contains(buf.String(), "wpt_") || strings.Contains(buf.String(), testTicket) {
		t.Fatalf("a ticket reached the log: %s", buf.String())
	}
	st := a.Status()
	b, _ := json.Marshal(st)
	if strings.Contains(string(b), "wpt_") {
		t.Fatalf("a ticket reached status: %s", b)
	}
	// The Secret type is enough on its own: %v, %+v and JSON all redact it.
	pub := Publish{WHIPURL: "https://whip.example/x", Ticket: credentials.NewSecret(testTicket)}
	for _, s := range []string{fmt.Sprintf("%v", pub.Ticket), fmt.Sprintf("%+v", pub), fmt.Sprintf("%#v", pub.Ticket)} {
		if strings.Contains(s, testTicket) {
			t.Fatalf("a Secret printed its value: %s", s)
		}
	}
}

func TestRepeatedRetriesDownshiftTo720p(t *testing.T) {
	api := &fakeAPI{}
	var widths []int
	var mu sync.Mutex
	pub := &fakePub{}
	pub.state.Store(video.StateRetrying)
	a, _ := newTestAgent(t, api, AgentOptions{Client: api, StateEvery: 5 * time.Millisecond,
		CurtainFrames: []video.Sample{{Data: []byte("C"), Key: true}}, CurtainJPEG: blackJPEG(16, 9),
		NewPublisher: func(video.Options) publisher { return pub },
		NewSource: func(_ Capture, e EncodeOptions) video.Source {
			mu.Lock()
			widths = append(widths, e.Width)
			mu.Unlock()
			return &fakeSource{out: make(chan string, 1)}
		}})
	ctx, cancel := context.WithCancel(context.Background())
	go a.Run(ctx)
	time.Sleep(200 * time.Millisecond)
	cancel()
	mu.Lock()
	got := append([]int(nil), widths...)
	mu.Unlock()
	seen720 := false
	for _, w := range got {
		if w == 1280 {
			seen720 = true
		}
	}
	if !seen720 {
		t.Fatalf("no 720p restart: %v", got)
	}
}

type testClock struct{ t time.Time }

func (c *testClock) now() time.Time      { return c.t }
func (c *testClock) add(d time.Duration) { c.t = c.t.Add(d) }

func waitCurtains(t *testing.T, api *fakeAPI, want int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		api.mu.Lock()
		got := append([]string(nil), api.curtains...)
		api.mu.Unlock()
		if len(got) >= want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d curtain reports: %v", len(got), got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPluginSilentForThreeSecondsBlanksAndMutes(t *testing.T) {
	clock := &testClock{t: time.Unix(1_800_000_000, 0)}
	api := &fakeAPI{}
	pub := &fakePub{}
	a, _ := newTestAgent(t, api, AgentOptions{Client: api, Now: clock.now, Plugin: "computer",
		CurtainLostAfter: 2 * time.Second, CurtainClearHold: 2 * time.Second,
		CurtainFrames: []video.Sample{{Data: []byte("CURTAIN"), Key: true}}, CurtainJPEG: blackJPEG(16, 9)})
	a.pub = pub
	clock.add(3 * time.Second)
	a.watchCurtain()
	if !a.curtainNow2() {
		t.Fatal("a silent plugin did not raise the curtain")
	}
	if !pub.muted.Load() {
		t.Fatal("a silent plugin did not mute the audio")
	}
	if !a.ds.isUp() {
		t.Fatal("the curtain gate is not up")
	}
	got := waitCurtains(t, api, 1)
	if got[0] != "detector" {
		t.Fatalf("reported reason %q, want detector", got[0])
	}
}

func TestPluginExitRaisesAtOnceAndRefusesADown(t *testing.T) {
	clock := &testClock{t: time.Unix(1_800_000_000, 0)}
	api := &fakeAPI{}
	pub := &fakePub{}
	a, _ := newTestAgent(t, api, AgentOptions{Client: api, Now: clock.now, Plugin: "computer",
		CurtainLostAfter: 2 * time.Second, CurtainClearHold: 2 * time.Second,
		CurtainFrames: []video.Sample{{Data: []byte("CURTAIN"), Key: true}}, CurtainJPEG: blackJPEG(16, 9)})
	a.pub = pub
	a.PluginLost()
	if !a.curtainNow2() || !pub.muted.Load() {
		t.Fatal("PluginLost did not blank and mute at once")
	}
	// The owner's Curtain now must not uncover the desk while the plugin is gone.
	a.SetCurtain(false, "manual")
	if !a.curtainNow2() {
		t.Fatal("a down was accepted while the plugin is lost")
	}
}

func TestPluginBackClearsAfterTwoSeconds(t *testing.T) {
	clock := &testClock{t: time.Unix(1_800_000_000, 0)}
	api := &fakeAPI{}
	pub := &fakePub{}
	a, _ := newTestAgent(t, api, AgentOptions{Client: api, Now: clock.now, Plugin: "computer",
		CurtainLostAfter: 2 * time.Second, CurtainClearHold: 2 * time.Second,
		CurtainFrames: []video.Sample{{Data: []byte("CURTAIN"), Key: true}}, CurtainJPEG: blackJPEG(16, 9)})
	a.pub = pub
	a.PluginLost()
	waitCurtains(t, api, 1)
	clock.add(3 * time.Second)
	a.PluginBack()
	a.ObserveCurtain(false, "detector")
	// The plugin keeps beating once a second; each clear report refreshes its liveness without resetting the hold.
	clock.add(time.Second)
	a.ObserveCurtain(false, "detector")
	clock.add(900 * time.Millisecond)
	a.ObserveCurtain(false, "detector")
	a.watchCurtain()
	if !a.curtainNow2() {
		t.Fatal("the curtain came down before two clear seconds")
	}
	clock.add(150 * time.Millisecond)
	a.ObserveCurtain(false, "detector")
	a.watchCurtain()
	if a.curtainNow2() {
		t.Fatal("the curtain did not come down after two clear seconds")
	}
	if pub.muted.Load() {
		t.Fatal("the audio stayed muted after the curtain came down")
	}
	got := waitCurtains(t, api, 2)
	if got[len(got)-1] != "detector" {
		t.Fatalf("last reason %q", got[len(got)-1])
	}
}

func TestAPluginThatIsBackButReportsUpKeepsTheCurtainUp(t *testing.T) {
	clock := &testClock{t: time.Unix(1_800_000_000, 0)}
	api := &fakeAPI{}
	a, _ := newTestAgent(t, api, AgentOptions{Client: api, Now: clock.now, Plugin: "computer",
		CurtainLostAfter: 2 * time.Second, CurtainClearHold: 2 * time.Second,
		CurtainFrames: []video.Sample{{Data: []byte("CURTAIN"), Key: true}}, CurtainJPEG: blackJPEG(16, 9)})
	a.PluginLost()
	clock.add(5 * time.Second)
	a.PluginBack()
	a.ObserveCurtain(true, "password_field")
	clock.add(5 * time.Second)
	a.watchCurtain()
	if !a.curtainNow2() {
		t.Fatal("a clear hold lowered a curtain the plugin says is up")
	}
}

func (a *Agent) curtainNow2() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.curtain
}

func TestCurtainUpBeforeRunBlanksTheFirstFrames(t *testing.T) {
	api := &fakeAPI{}
	src := &fakeSource{out: make(chan string, 4)}
	a, _ := newTestAgent(t, api, AgentOptions{Client: api,
		NewSource:     func(Capture, EncodeOptions) video.Source { return src },
		CurtainFrames: []video.Sample{{Data: []byte("CURTAIN"), Key: true, Duration: 100 * time.Millisecond}}})
	a.SetCurtain(true, "password_field")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var seen []string
	go func() {
		_ = a.ds.Run(ctx, func(s video.Sample) {
			mu.Lock()
			seen = append(seen, string(s.Data))
			mu.Unlock()
		})
	}()
	time.Sleep(20 * time.Millisecond)
	select {
	case src.out <- "RAW":
	default:
	}
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	got := append([]string(nil), seen...)
	mu.Unlock()
	if containsStr(got, "RAW") || !containsStr(got, "CURTAIN") {
		t.Fatalf("frames before the first poll: %v", got)
	}
}

func TestAgentRevokeIsTheKillSwitch(t *testing.T) {
	api := &fakeAPI{}
	a, _ := newTestAgent(t, api, AgentOptions{Client: api, CurtainFrames: []video.Sample{{Data: []byte("C"), Key: true}},
		CurtainJPEG: blackJPEG(16, 9)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	time.Sleep(20 * time.Millisecond)
	a.Revoke()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Revoke did not end the agent")
	}
	time.Sleep(20 * time.Millisecond)
	if atomic.LoadInt32(&api.revoked) == 0 {
		t.Fatal("Revoke was not sent to Live")
	}
	if atomic.LoadInt32(&api.ended) != 0 {
		t.Fatal("End was sent after the kill switch")
	}
}

func containsStr(xs []string, want string) bool {
	for _, x := range xs {
		if strings.Contains(x, want) {
			return true
		}
	}
	return false
}
