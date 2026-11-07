//go:build !windows

package node

import (
	"encoding/json"
	"runtime"
	"testing"
	"time"

	"github.com/OpenVibers/OpenVibe.Node/internal/firstrun"
	"github.com/OpenVibers/OpenVibe.Node/internal/localctl"
	"github.com/OpenVibers/OpenVibe.Node/internal/protocol"
)

// The running Node takes the guide's events on the local socket and sends them on the device link it already has:
// one telemetry frame per step, so the installer's service is not replaced by a second connection.
func TestFirstRunEventsGoOnTheExistingLink(t *testing.T) {
	e := start(t)
	event := protocol.Event{Name: firstrun.EventName, TS: time.Now().UnixMilli(),
		Fields: map[string]any{"step": firstrun.StepInstalled, "version": "test", "os": "linux", "arch": "amd64"}}
	data, _ := json.Marshal(protocol.Telemetry{Events: []protocol.Event{event}})
	if resp := e.node.control(localctl.Request{Cmd: localctl.CmdFirstRun, Data: data}); !resp.OK {
		t.Fatalf("%+v", resp)
	}
	f, err := e.conn.Expect(protocol.TypeTelemetry, 3*time.Second, func(m protocol.Message) bool {
		tel, ok := m.(protocol.Telemetry)
		return ok && len(tel.Events) == 1 && tel.Events[0].Name == firstrun.EventName && tel.Events[0].Fields["step"] == firstrun.StepInstalled
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.Msg.(protocol.Telemetry).Events[0].Fields["version"] != "test" {
		t.Fatalf("%+v", f.Msg)
	}
}

// The window's answer goes to Bot as one node.first_run event with the uses, on the link the Node already has.
func TestFirstRunAnswerIsOneEventWithTheUses(t *testing.T) {
	e := start(t)
	if !e.node.panelState().FirstRun {
		t.Fatal("a fresh machine is not asked what it is for")
	}
	if err := e.node.applyFirstRun([]string{"codes", "earn"}, false); err != nil {
		t.Fatal(err)
	}
	var got protocol.Event
	_, err := e.conn.Expect(protocol.TypeTelemetry, 3*time.Second, func(m protocol.Message) bool {
		tel, ok := m.(protocol.Telemetry)
		if !ok {
			return false
		}
		for _, ev := range tel.Events {
			if ev.Name == firstrun.EventName && ev.Fields["step"] == firstrun.UsesStep {
				got = ev
				return true
			}
		}
		return false
	})
	if err != nil {
		t.Fatal(err)
	}
	uses, _ := json.Marshal(got.Fields["uses"])
	if string(uses) != `["codes","earn"]` || got.Fields["version"] != "test" || got.Fields["os"] != runtime.GOOS {
		t.Fatalf("answer event %+v", got)
	}
	if e.node.panelState().FirstRun {
		t.Fatal("the question is still shown after the answer")
	}
}

func TestFirstRunControlRefusesAnythingElse(t *testing.T) {
	e := start(t)
	bad := func(data []byte, why string) {
		t.Helper()
		if resp := e.node.control(localctl.Request{Cmd: localctl.CmdFirstRun, Data: data}); resp.OK {
			t.Fatalf("%s was accepted", why)
		}
	}
	bad(nil, "empty")
	bad([]byte(`not json`), "bad JSON")
	other := protocol.Event{Name: "something.else", TS: 1, Fields: map[string]any{"step": firstrun.StepInstalled, "version": "test", "os": "linux"}}
	b, _ := json.Marshal(protocol.Telemetry{Events: []protocol.Event{other}})
	bad(b, "another event")
	unknown := protocol.Event{Name: firstrun.EventName, TS: 1, Fields: map[string]any{"step": "nope", "version": "test", "os": "linux"}}
	b, _ = json.Marshal(protocol.Telemetry{Events: []protocol.Event{unknown}})
	bad(b, "an unknown step")
}
