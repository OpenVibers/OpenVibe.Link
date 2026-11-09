package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/OpenVibers/OpenVibe.Link/internal/connect"
)

// `openvibe-link share`: usage, add, list (the key only redacted), remove, and the file only the Link's account reads.
func TestShareCommands(t *testing.T) {
	home := t.TempDir()
	key := "ovc_" + strings.Repeat("K", 43)
	var out, errb bytes.Buffer

	if code := run([]string{"share", "--home", home}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "connect.openvibe.link") {
		t.Fatalf("usage: %d %q", code, errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"share", "add", "--key", key, "--port", "8790", "--home", home}, &out, &errb); code != 0 {
		t.Fatalf("add: %d %s", code, errb.String())
	}
	if strings.Contains(out.String(), key) {
		t.Fatal("add printed the whole key")
	}
	out.Reset()
	if code := run([]string{"share", "list", "--home", home}, &out, &errb); code != 0 || !strings.Contains(out.String(), "localhost:8790") || strings.Contains(out.String(), key) {
		t.Fatalf("list: %d %q", code, out.String())
	}
	matches, _ := filepath.Glob(filepath.Join(home, "**", "connect-shares.json"))
	if len(matches) == 0 {
		matches, _ = filepath.Glob(filepath.Join(home, "*", "connect-shares.json"))
	}
	if len(matches) == 0 {
		matches, _ = filepath.Glob(filepath.Join(home, "connect-shares.json"))
	}
	if len(matches) != 1 {
		t.Fatalf("the saved file: %v", matches)
	}
	if fi, _ := os.Stat(matches[0]); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 { // Windows keeps no Unix mode bits
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	out.Reset()
	if code := run([]string{"share", "remove", "8790", "--home", home}, &out, &errb); code != 0 || !strings.Contains(out.String(), "Removed 1") {
		t.Fatalf("remove: %d %q", code, out.String())
	}
	errb.Reset()
	if code := run([]string{"share", "add", "--key", "nope", "--port", "1", "--home", home}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "ovc_") {
		t.Fatalf("a bad key: %d %q", code, errb.String())
	}
}

// A share by id needs this computer added to OpenVibe (a Network pairing); without it the commands say where to do that.
func TestShareByIDNeedsThePairing(t *testing.T) {
	home := t.TempDir()
	var out, errb bytes.Buffer
	for _, args := range [][]string{
		{"share", "--share", "shr_01K9ZZZZZZZZZZZZZZZZZZZZZZ", "--home", home},
		{"share", "add", "--share", "shr_01K9ZZZZZZZZZZZZZZZZZZZZZZ", "--port", "8790", "--home", home},
		{"share", "8790", "--home", home},
		{"share", "add", "8790", "--home", home},
		{"share", "mine", "--home", home},
	} {
		errb.Reset()
		if code := run(args, &out, &errb); code != 1 || !strings.Contains(errb.String(), "openvibe.bot/machines") {
			t.Fatalf("%v: %d %q", args, code, errb.String())
		}
	}
	errb.Reset()
	if code := run([]string{"share", "--share", "shr_nope", "--home", home}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "shr_") {
		t.Fatalf("a bad id: %d %q", code, errb.String())
	}
}

// `openvibe-link status` lists the shares the service keeps up (CON-4).
func TestSharesText(t *testing.T) {
	if got := sharesText(nil); got != "" {
		t.Fatalf("no shares: %q", got)
	}
	got := sharesText([]connect.Status{
		{ID: "shr_01K9ZZZZZZZZZZZZZZZZZZZZZZ", Port: 8790, Host: "bot--alex.openvibe.link", URL: "https://bot--alex.openvibe.link", Connected: true, Requests: 3},
		{Port: 9001, LastError: "dial: connection refused"},
	})
	for _, want := range []string{"Shares:   2 kept up", "localhost:8790", "https://bot--alex.openvibe.link", "connected, 3 requests", "localhost:9001", "not connected", "dial: connection refused"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ovc_") {
		t.Fatalf("status printed a key:\n%s", got)
	}
}
