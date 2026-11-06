package node

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/OpenVibers/OpenVibe.Node/internal/credentials"
	"github.com/OpenVibers/OpenVibe.Node/internal/plugins"
	"github.com/OpenVibers/OpenVibe.Node/internal/protocol"
	"github.com/OpenVibers/OpenVibe.Node/internal/stream"
	"github.com/OpenVibers/OpenVibe.Node/internal/video"
)

type watchFakeLive struct{}

func (watchFakeLive) State(context.Context, string, credentials.Secret) (stream.State, error) {
	return stream.State{Encode: true}, nil
}
func (watchFakeLive) Renew(context.Context, string, credentials.Secret) (stream.Publish, error) {
	return stream.Publish{}, nil
}
func (watchFakeLive) Curtain(context.Context, string, credentials.Secret, bool, string) error {
	return nil
}
func (watchFakeLive) PostFrame(context.Context, string, credentials.Secret, []byte) error { return nil }
func (watchFakeLive) End(context.Context, string, credentials.Secret) error               { return nil }
func (watchFakeLive) Revoke(context.Context, string, credentials.Secret) error            { return nil }

func newWatchAgent(t *testing.T) (*Node, *stream.Agent) {
	t.Helper()
	a, err := stream.NewAgent(stream.AgentOptions{Client: watchFakeLive{}, ID: "ags_01J8Z4M2Q0R7T9YV3K6N8P1W2X",
		Publish: stream.Publish{WHIPURL: "https://whip.example/whip/agent/ags_01J8Z4M2Q0R7T9YV3K6N8P1W2X",
			Ticket: credentials.NewSecret("wpt_0123456789abcdefghijklmnopqrstuvwxyzABCDEFG"), ExpiresAt: time.Now().Add(time.Hour)},
		Plugin: "computer", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		CurtainFrames: []video.Sample{{Data: []byte("C"), Key: true}}})
	if err != nil {
		t.Fatal(err)
	}
	n := &Node{}
	n.deskStream = a
	return n, a
}

func TestAPluginStateThatIsNotReadyRaisesTheCurtain(t *testing.T) {
	n, a := newWatchAgent(t)
	if a.Status().Curtain {
		t.Fatal("the stream started with the curtain up")
	}
	n.deskPluginState(plugins.Output{Plugin: "computer", Data: map[string]any{"state": plugins.StateDown}})
	if !a.Status().Curtain {
		t.Fatal("the plugin going down did not raise the curtain")
	}
	// Another plugin's state is not this stream's business.
	n.deskPluginState(plugins.Output{Plugin: "dryrun", Data: map[string]any{"state": plugins.StateReady}})
	if !a.Status().Curtain {
		t.Fatal("another plugin's state lowered it")
	}
	n.deskPluginFault(plugins.Output{Plugin: "computer", Data: map[string]any{"fault_code": protocol.FaultPluginDown}})
	if !a.Status().Curtain {
		t.Fatal("plugin_down did not keep it up")
	}
}

func TestACurtainHeartbeatFromThePluginIsObserved(t *testing.T) {
	n, a := newWatchAgent(t)
	n.deskCurtainEvent("dryrun", map[string]any{"up": true, "reason": "password_field"})
	if a.Status().Curtain {
		t.Fatal("another plugin's curtain moved this stream")
	}
	n.deskCurtainEvent("computer", map[string]any{"up": true, "reason": "password_field", "heartbeat": true})
	if !a.Status().Curtain {
		t.Fatal("a heartbeat saying up did not raise the curtain")
	}
	n.deskCurtainEvent("computer", map[string]any{"up": false, "reason": "detector", "heartbeat": true})
	if !a.Status().Curtain {
		t.Fatal("one clear heartbeat lowered the curtain without the hold")
	}
}
