package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// `openvibe-node share`: usage, add, list (the key only redacted), remove, and the file only the Node's account reads.
func TestShareCommands(t *testing.T) {
	home := t.TempDir()
	key := "ovc_" + strings.Repeat("K", 43)
	var out, errb bytes.Buffer

	if code := run([]string{"share", "--home", home}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "connect.openvibe.website") {
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
