//go:build !windows

package node

import (
	"encoding/json"
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
