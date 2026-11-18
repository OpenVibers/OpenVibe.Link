package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPreviewFor(t *testing.T) {
	if d, err := previewFor(""); err != nil || d != 2*time.Hour {
		t.Fatalf("empty is two hours: %v %v", d, err)
	}
	if d, err := previewFor("30m"); err != nil || d != 30*time.Minute {
		t.Fatalf("30m: %v %v", d, err)
	}
	for _, bad := range []string{"1m", "25h", "soon", "-1h"} {
		if _, err := previewFor(bad); err == nil {
			t.Fatalf("%q should be refused", bad)
		}
	}
}

func TestPreviewRootRefusesHomeAndRoot(t *testing.T) {
	if _, err := previewRoot("/"); err == nil {
		t.Fatal("the filesystem root is refused")
	}
	if home, err := os.UserHomeDir(); err == nil {
		if _, err := previewRoot(home); err == nil {
			t.Fatal("the home folder itself is refused")
		}
	}
	if _, err := previewRoot(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a missing folder is refused")
	}
	f := filepath.Join(t.TempDir(), "file.txt")
	_ = os.WriteFile(f, []byte("x"), 0o644)
	if _, err := previewRoot(f); err == nil {
		t.Fatal("a file is not a folder")
	}
}

// The folder is served read-only: files and index.html, never a listing, a dotfile, a link out, or a write.
func TestPreviewHandler(t *testing.T) {
	base := t.TempDir()
	site := filepath.Join(base, "dist")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(site, "assets"), 0o755))
	must(os.MkdirAll(filepath.Join(site, "empty"), 0o755))
	must(os.MkdirAll(filepath.Join(site, ".git"), 0o755))
	must(os.WriteFile(filepath.Join(site, "index.html"), []byte("<h1>Built</h1>"), 0o644))
	must(os.WriteFile(filepath.Join(site, "assets", "app.js"), []byte("console.log(1)"), 0o644))
	must(os.WriteFile(filepath.Join(site, ".env"), []byte("SECRET=1"), 0o644))
	must(os.WriteFile(filepath.Join(site, ".git", "config"), []byte("[core]"), 0o644))
	must(os.WriteFile(filepath.Join(base, "outside.txt"), []byte("not yours"), 0o644))
	must(os.Symlink(filepath.Join(base, "outside.txt"), filepath.Join(site, "escape.txt")))
	root, err := previewRoot(site)
	must(err)
	srv := httptest.NewServer(previewHandler(root))
	defer srv.Close()
	get := func(method, p string) (int, string, http.Header) {
		req, _ := http.NewRequest(method, srv.URL+p, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}
	if code, body, h := get("GET", "/"); code != 200 || !strings.Contains(body, "Built") || h.Get("X-Robots-Tag") != "noindex, nofollow" || !strings.HasPrefix(h.Get("Content-Security-Policy"), "sandbox") || !strings.Contains(h.Get("Content-Security-Policy"), "allow-same-origin") {
		t.Fatalf("index: %d %q %v", code, body, h)
	}
	if code, body, _ := get("GET", "/assets/app.js"); code != 200 || body != "console.log(1)" {
		t.Fatalf("a file: %d %q", code, body)
	}
	for _, p := range []string{"/.env", "/.git/config", "/assets/../.env", "/escape.txt", "/empty/", "/../outside.txt"} {
		if code, body, _ := get("GET", p); code != 404 || strings.Contains(body, "SECRET") || strings.Contains(body, "not yours") {
			t.Fatalf("%s should be 404: %d %q", p, code, body)
		}
	}
	if code, _, h := get("POST", "/"); code != 405 || h.Get("Allow") != "GET, HEAD" {
		t.Fatalf("a write: %d %v", code, h)
	}
}

func TestPreviewAPIAndOpen(t *testing.T) {
	if got := previewAPI(""); got != "https://connect.openvibe.link" {
		t.Fatal(got)
	}
	if got := previewAPI("wss://connect.example/tunnel"); got != "https://connect.example" {
		t.Fatal(got)
	}
	var seen map[string]any
	var auth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&seen)
		if seen["network_token"] == "refused" {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"code":"preview.owner","detail":"Network did not confirm this computer"}`))
			return
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"share":{"id":"shr_01JABCDEFGHJKMNPQRSTVWXYZ0","host":"pa1b2c3--box.openvibe.link","url":"https://pa1b2c3--box.openvibe.link","local_port":4321,"mode":"owner"},"expires_at":"2026-11-18T16:00:00Z"}`))
	}))
	defer api.Close()
	out, err := openPreview(context.Background(), api.URL, "node-token", "network-token", 4321, 2*time.Hour)
	if err != nil || out.Share.ID != "shr_01JABCDEFGHJKMNPQRSTVWXYZ0" || out.ExpiresAt != "2026-11-18T16:00:00Z" {
		t.Fatalf("open: %+v %v", out, err)
	}
	if auth != "Bearer node-token" || seen["port"].(float64) != 4321 || seen["expires_in"].(float64) != 7200 {
		t.Fatalf("what Connect got: %s %v", auth, seen)
	}
	if _, err := openPreview(context.Background(), api.URL, "node-token", "refused", 4321, time.Hour); err == nil || !strings.Contains(err.Error(), "Network did not confirm") {
		t.Fatalf("a refusal says why: %v", err)
	}
}

func TestPreviewDetachArgs(t *testing.T) {
	got := previewArgsWithoutDetach([]string{"preview", "dist", "--for", "30m", "--detach", "-detach=true"})
	if strings.Join(got, " ") != "preview dist --for 30m" {
		t.Fatalf("the background copy runs the same command without --detach: %v", got)
	}
}

// The command that started a background preview waits for its one line, its end, or the time limit.
func TestPreviewWaitReady(t *testing.T) {
	dir := t.TempDir()
	ok := filepath.Join(dir, "ok")
	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = os.WriteFile(ok, []byte("ok\thttps://pa1b2c3--box.openvibe.link\t2026-11-18T16:00:00Z\n"), 0o600)
	}()
	_ = os.WriteFile(ok, nil, 0o600)
	line, err := waitPreviewReady(ok, make(chan struct{}), 5*time.Second)
	if err != nil || !strings.HasPrefix(line, "ok\thttps://pa1b2c3--box.openvibe.link") {
		t.Fatalf("ready: %q %v", line, err)
	}

	gone := make(chan struct{})
	close(gone)
	if _, err := waitPreviewReady(filepath.Join(dir, "empty"), gone, 5*time.Second); err == nil || !strings.Contains(err.Error(), "stopped before") {
		t.Fatalf("a copy that died says so: %v", err)
	}
	failed := filepath.Join(dir, "failed")
	_ = os.WriteFile(failed, []byte("error\tOpenVibe Connect said no: five previews open\n"), 0o600)
	if line, err := waitPreviewReady(failed, gone, 5*time.Second); err != nil || !strings.HasPrefix(line, "error\t") {
		t.Fatalf("its reason comes through even after it ended: %q %v", line, err)
	}
	if _, err := waitPreviewReady(filepath.Join(dir, "never"), make(chan struct{}), 300*time.Millisecond); err == nil || !strings.Contains(err.Error(), "within a minute") {
		t.Fatalf("a time limit: %v", err)
	}
}
