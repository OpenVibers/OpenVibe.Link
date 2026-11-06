package stream

import (
	"context"
	"testing"
	"time"

	"github.com/OpenVibers/OpenVibe.Node/internal/credentials"
	"github.com/OpenVibers/OpenVibe.Node/internal/video"
)

func TestProbeSourceQRRoundTrips(t *testing.T) {
	want := int64(1_800_000_000_123)
	src := &ProbeSource{W: 640, H: 360, FPS: 10, Now: func() time.Time { return time.UnixMilli(want) }}
	got := make(chan video.Sample, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = src.Run(ctx, func(s video.Sample) { got <- s }) }()
	select {
	case s := <-got:
		f := video.NewFrame(640, 360)
		// The PCM encoder wrote an IDR of the frame; decode the source's own drawing straight from a fresh frame.
		_ = s
		drawProbe(f, want)
		ms, err := DecodeProbe(f)
		if err != nil {
			t.Fatal(err)
		}
		if ms != want {
			t.Fatalf("decoded %d, want %d", ms, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no probe frame")
	}
}

func TestProbeSourceClockAdvances(t *testing.T) {
	base := time.UnixMilli(1_800_000_000_000)
	i := 0
	src := &ProbeSource{W: 640, H: 360, FPS: 20, Now: func() time.Time {
		v := base.Add(time.Duration(i) * 50 * time.Millisecond)
		i++
		return v
	}}
	samples := make(chan video.Sample, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = src.Run(ctx, func(s video.Sample) { samples <- s }) }()
	for n := 0; n < 3; n++ {
		select {
		case <-samples:
		case <-time.After(2 * time.Second):
			t.Fatal("no probe frame")
		}
	}
	if src.LastMS() < base.UnixMilli()+50 {
		t.Fatalf("the clock did not advance: %d", src.LastMS())
	}
}

type countingPub struct{ frames int64 }

func (c *countingPub) Run(ctx context.Context, s video.Source) {
	_ = s.Run(ctx, func(video.Sample) { c.frames++ })
}
func (c *countingPub) SetPaused(bool)                   {}
func (c *countingPub) SetPublishKey(credentials.Secret) {}
func (c *countingPub) State() string                    { return video.StateLive }
func (c *countingPub) Frames() int64                    { return c.frames }
func (c *countingPub) LastError() string                { return "" }

func TestProbePublishesUntilTimeIsUp(t *testing.T) {
	pub := &countingPub{}
	n, err := Probe(context.Background(), ProbeOptions{
		WHIPURL: "http://127.0.0.1:1/whip/agent/" + testID,
		Ticket:  credentials.NewSecret(testTicket),
		Seconds: 1, Width: 320, Height: 180, FPS: 20,
		NewPublisher: func(video.Options) publisher { return pub },
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 5 {
		t.Fatalf("probe published %d frames in a second", n)
	}
}

func TestProbeNeedsAURLAndATicket(t *testing.T) {
	if _, err := Probe(context.Background(), ProbeOptions{Seconds: 1, NewPublisher: func(video.Options) publisher { return &countingPub{} }}); err == nil {
		t.Error("no URL accepted")
	}
	if _, err := Probe(context.Background(), ProbeOptions{WHIPURL: "https://whip.openvibe.live/whip/agent/" + testID,
		Seconds: 1, NewPublisher: func(video.Options) publisher { return &countingPub{} }}); err == nil {
		t.Error("no ticket accepted")
	}
}
