package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeCaller struct {
	name   string
	args   json.RawMessage
	refuse bool
}

func (f *fakeCaller) Call(name string, args json.RawMessage) (json.RawMessage, error) {
	f.name, f.args = name, args
	if f.refuse {
		return nil, errors.New("grant refused")
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func TestEveryToolAndRefusals(t *testing.T) {
	cases := []struct{ name, args string }{
		{"agents_list", `{}`}, {"agent_ask", `{"agent":"agt_1","text":"question","wait_seconds":2}`},
		{"agent_tell", `{"agent":"agt_1","text":"hello"}`}, {"agent_inbox", `{}`},
		{"agent_reply", `{"task":"tsk_1","text":"answer"}`}, {"machines_list", `{}`},
		{"machine_request_access", `{"machine":"lnk_1","scopes":["agent","send"],"for_seconds":3600}`},
		{"machine_use", `{"machine":"lnk_1","scope":"screen","action":"status","data":{}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeCaller{}
			input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tc.name + `","arguments":` + tc.args + `}}` + "\n"
			var output bytes.Buffer
			if err := Serve(strings.NewReader(input), &output, f); err != nil {
				t.Fatal(err)
			}
			if f.name != tc.name || string(f.args) != tc.args {
				t.Fatalf("forwarded %q %s", f.name, f.args)
			}
			if !strings.Contains(output.String(), `"structuredContent":{"data":{"ok":true}}`) {
				t.Fatalf("missing result: %s", output.String())
			}
			f.refuse = true
			output.Reset()
			if err := Serve(strings.NewReader(input), &output, f); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), `"isError":true`) {
				t.Fatalf("refusal missing: %s", output.String())
			}
		})
	}
	for _, tc := range []struct{ name, args string }{
		{"agent_ask", `{"agent":"agt_1","text":""}`},
		{"agent_ask", `{"agent":"agt_1","text":"x","wait_seconds":121}`},
		{"machine_request_access", `{"machine":"lnk_1","scopes":["admin"]}`},
		{"machine_use", `{"machine":"lnk_1","scope":"computer","action":""}`},
		{"agent_reply", `{"task":"tsk_1","text":"ok","grant":"fake"}`},
	} {
		t.Run("invalid-"+tc.name, func(t *testing.T) {
			if validate(tc.name, json.RawMessage(tc.args)) == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestHandshakeAndToolsList(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"
	var out bytes.Buffer
	if err := Serve(strings.NewReader(input), &out, &fakeCaller{}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("responses: %d", len(lines))
	}
	if !strings.Contains(lines[0], Version) {
		t.Fatal(lines[0])
	}
	for _, tool := range Tools {
		if !strings.Contains(lines[1], `"name":"`+tool.Name+`"`) {
			t.Fatalf("missing %s", tool.Name)
		}
	}
}
