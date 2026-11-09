package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseSize(t *testing.T) {
	if w, h, err := parseSize("640x360"); err != nil || w != 640 || h != 352 {
		t.Fatalf("parseSize = %d,%d,%v", w, h, err)
	}
	if _, _, err := parseSize("1x1"); err == nil {
		t.Fatal("a tiny size was accepted")
	}
	if _, _, err := parseSize("nonsense"); err == nil {
		t.Fatal("nonsense was accepted")
	}
}

func TestTheProbeTicketComesFromTheEnvironmentOrStdinNeverArgv(t *testing.T) {
	t.Setenv("OPENVIBE_STREAM_TICKET", "wpt_0123456789abcdefghijklmnopqrstuvwxyzABCDEFG")
	tk, err := probeTicket(nil, false)
	if err != nil || tk.Reveal() == "" {
		t.Fatalf("env ticket: %v", err)
	}
	t.Setenv("OPENVIBE_STREAM_TICKET", "")
	tk, err = probeTicket(strings.NewReader("wpt_0123456789abcdefghijklmnopqrstuvwxyzABCDEFG\n"), true)
	if err != nil || tk.Reveal() == "" {
		t.Fatalf("stdin ticket: %v", err)
	}
	if _, err := probeTicket(nil, false); err == nil {
		t.Fatal("no ticket was accepted")
	}
	if _, err := probeTicket(strings.NewReader("not-a-ticket\n"), true); err == nil {
		t.Fatal("a bad ticket was accepted")
	}
}

func TestCmdStreamNeedsAURLAndATicket(t *testing.T) {
	t.Setenv("OPENVIBE_STREAM_TICKET", "")
	var out, errb bytes.Buffer
	if code := cmdStream([]string{"--probe", "--seconds", "1"}, nil, &out, &errb); code != 2 {
		t.Fatalf("missing URL exit = %d", code)
	}
	if strings.Contains(out.String()+errb.String(), "wpt_0123456789") {
		t.Fatal("a ticket was printed")
	}
	if code := cmdStream([]string{"--probe", "--whip-url", "https://whip.openvibe.live/whip/agent/ags_x", "--seconds", "1"},
		nil, &out, &errb); code != 2 {
		t.Fatalf("missing ticket exit = %d", code)
	}
}
