package main

import (
	"bytes"
	"strings"
	"testing"
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
