package plugins

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/mono"
	"github.com/OpenVibers/OpenVibe.Link/internal/protocol"
)

func TestApprovalBindsPayloadProcessAndExpiry(t *testing.T) {
	p := newPlugin(Spec{Name: "computer"}, slog.Default(), make(chan Output, 8))
	p.state, p.queue = StateReady, make(chan line, 16)
	payload := json.RawMessage(`{"argv":["original"],"number":9007199254740993}`)
	wait := p.Begin(context.Background(), "cloud", "exec", payload, 10000)
	a := p.approval("cloud", 10)
	if a == nil || len(a.Nonce) != 64 || a.Nonce == "cloud" {
		t.Fatal("no independent nonce", a)
	}
	queued := <-p.queue
	payload[10] = 'X'
	var sent map[string]json.RawMessage
	if err := json.Unmarshal(queued.b, &sent); err != nil {
		t.Fatal(err)
	}
	if string(sent["value"]) != `{"argv":["original"],"number":9007199254740993}` {
		t.Fatal("payload changed", string(sent["value"]))
	}
	// A restart invalidates even a delayed event whose cloud ID is reused.
	p.failPending(protocol.FaultPluginDown)
	p.queue = make(chan line, 16)
	p.Begin(context.Background(), "cloud", "exec", json.RawMessage(`{"argv":["replacement"]}`), 10000)
	replacement := p.approval("cloud", 10)
	if replacement.Nonce == a.Nonce || replacement.Digest == a.Digest {
		t.Fatal("replacement reused approval binding")
	}
	if p.ApprovalValid(a) || p.Answer(a, true) == nil {
		t.Fatal("stale process approval accepted")
	}
	if r := wait(); r.FaultCode != protocol.FaultPluginDown {
		t.Fatal(r)
	}
	if !p.ApprovalValid(replacement) {
		t.Fatal("old waiter deleted replacement")
	}
	replacement.expires = mono.Now().Add(-time.Millisecond)
	if p.Answer(replacement, true) == nil {
		t.Fatal("expired approval accepted")
	}
	if len(p.queue) != 1 {
		t.Fatal("stale answer reached plugin")
	}
}

func TestApprovalAnswerIsSingleUse(t *testing.T) {
	p := newPlugin(Spec{Name: "computer"}, slog.Default(), make(chan Output, 8))
	p.state, p.queue = StateReady, make(chan line, 8)
	p.Begin(context.Background(), "cloud", "exec", json.RawMessage(`{}`), 10000)
	a := p.approval("cloud", 10)
	if err := p.Answer(a, false); err != nil {
		t.Fatal(err)
	}
	if err := p.Answer(a, true); err == nil {
		t.Fatal("answer replayed")
	}
	if len(p.queue) != 2 {
		t.Fatal("unexpected messages", len(p.queue))
	}
}
