package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/OpenVibers/OpenVibe.Link/internal/localctl"
)

func TestMCPInstallOffersAndPrintedConfigs(t *testing.T) {
	for _, cli := range []string{"claude", "codex", "muse", "opencode"} {
		t.Run(cli, func(t *testing.T) {
			var printed bytes.Buffer
			if err := printMCPConfig(&globals{}, cli, &printed); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(printed.String(), "openvibe-link") || !strings.Contains(printed.String(), "--agent") {
				t.Fatalf("config: %s", printed.String())
			}
			var offered bytes.Buffer
			if err := installMCP(&globals{}, cli, strings.NewReader("n\n"), &offered); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(offered.String(), "[y/N]") || !strings.Contains(offered.String(), "Nothing changed") {
				t.Fatalf("offer: %s", offered.String())
			}
		})
	}
}

func TestMCPStdioUsesTheLocalSocket(t *testing.T) {
	g := reviewSocket(t, func(r localctl.Request) localctl.Response {
		if r.Cmd != localctl.CmdMCP || r.Agent != "codex" {
			return localctl.Response{Error: "wrong local command"}
		}
		if r.ID == "agents_list" {
			return localctl.Response{OK: true, Data: json.RawMessage(`{"agents":[{"id":"agt_01J8Z4M2Q0R7T9YV3K6N8P1W2X","name":"Codex","status":"online"}]}`)}
		}
		return localctl.Response{Error: "no active grant for this machine and scope"}
	})
	call := func(name, args string) string {
		t.Helper()
		input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}` + "\n"
		var out bytes.Buffer
		if err := mcpCommand(g, nil, "codex", strings.NewReader(input), &out); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if got := call("agents_list", `{}`); !strings.Contains(got, `agt_01J8Z4M2Q0R7T9YV3K6N8P1W2X`) {
		t.Fatalf("agents_list: %s", got)
	}
	if got := call("machine_use", `{"machine":"lnk_01J8Z4M2Q0R7T9YV3K6N8P1W2Z","scope":"screen","action":"screen","data":{}}`); !strings.Contains(got, `"isError":true`) || !strings.Contains(got, "no active grant") {
		t.Fatalf("refusal: %s", got)
	}
}
