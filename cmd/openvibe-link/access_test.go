package main

import (
	"bytes"
	"strings"
	"testing"
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
	for _, part := range []string{"Waiting\n", "Watch the screen and Use a private desk", "Allowed\n", "Send you files", "Ended\n  None"} {
		if !strings.Contains(got, part) {
			t.Fatalf("missing %q in %q", part, got)
		}
	}
	if accessTime(3600) != "1 hour" || accessTime(0) != "until you take it back" {
		t.Fatal("duration words")
	}
}
