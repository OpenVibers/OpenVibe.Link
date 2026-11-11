package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/OpenVibers/OpenVibe.Link/internal/localctl"
)

func TestAccessListShowsDesignStates(t *testing.T) {
	value := map[string]any{
		"requests": map[string]any{"requests": []any{map[string]any{"id": "req_123", "from_principal": "lnk_a", "to_principal": "lnk_b", "status": "pending", "scopes": []any{"screen", "desk"}}}},
		"grants":   map[string]any{"grants": []any{map[string]any{"id": "gra_123", "from_principal": "lnk_a", "to_principal": "lnk_b", "scopes": []any{"send"}}}},
	}
	var out bytes.Buffer
	if err := printAccessList(&out, value); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, part := range []string{"Waiting\n", "Watch the screen and use a private desk", "Allowed\n", "Send you files", "Ended\n  None"} {
		if !strings.Contains(got, part) {
			t.Fatalf("missing %q in %q", part, got)
		}
	}
	if accessTime(3600) != "1 hour" || accessTime(0) != "until you take it back" {
		t.Fatal("duration words")
	}
}

func TestAccessRequestWaitsForTheAnswer(t *testing.T) {
	var polls int
	g := reviewSocket(t, func(r localctl.Request) localctl.Response {
		if r.Cmd != localctl.CmdAccess {
			return localctl.Response{Error: "unexpected command"}
		}
		switch r.ID {
		case "request":
			return localctl.Response{OK: true, Data: json.RawMessage(`{"request":{"id":"req_12345678","status":"pending"}}`)}
		case "list":
			polls++
			if polls == 1 {
				return localctl.Response{OK: true, Data: json.RawMessage(`{"requests":{"requests":[{"id":"req_12345678","status":"pending"}]},"grants":{"grants":[]}}`)}
			}
			return localctl.Response{OK: true, Data: json.RawMessage(`{"requests":{"requests":[{"id":"req_12345678","status":"approved"}]},"grants":{"grants":[{"id":"gra_12345678","request_id":"req_12345678","scopes":["screen"],"expires_at":"2030-01-01T12:40:00Z"}]}}`)}
		}
		return localctl.Response{Error: "unexpected action"}
	})
	var out bytes.Buffer
	if err := cmdAccess(g, []string{"request", "lnk_target"}, accessArgs{Scope: "screen,desk", For: "1h"}, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "Asked lnk_target for: Watch the screen and use a private desk (1 hour).") ||
		!strings.Contains(got, "Allowed, narrowed to: Watch the screen.") {
		t.Fatalf("request output: %q", got)
	}
}

func TestAccessRequestReportsDenied(t *testing.T) {
	g := reviewSocket(t, func(r localctl.Request) localctl.Response {
		if r.ID == "request" {
			return localctl.Response{OK: true, Data: json.RawMessage(`{"request":{"id":"req_12345678"}}`)}
		}
		return localctl.Response{OK: true, Data: json.RawMessage(`{"requests":{"requests":[{"id":"req_12345678","status":"denied"}]},"grants":{"grants":[]}}`)}
	})
	var out bytes.Buffer
	if err := cmdAccess(g, []string{"request", "lnk_target"}, accessArgs{Scope: "agent", For: "1h"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Waiting for their owner…") || !strings.Contains(out.String(), "Denied.") {
		t.Fatalf("denied output: %q", out.String())
	}
}
