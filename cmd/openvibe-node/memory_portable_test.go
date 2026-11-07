package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const portableFixture = `{"format":"openvibe.memory-export@1","version":1,"exported_at":"2026-11-07T09:30:00Z","items":[
 {"text":"Prefers short answers.","kind":"preference","scope":"user","source":"claude-code","status":"active","confidence":1},
 {"text":"Never rewrite history on main.","kind":"project_rule","scope":"project:demo","source":"agents","status":"active","confidence":1}
]}`

// portableStore is a fake store for the export and import commands: it answers the two portable routes and records
// the person's token, the method and the body.
type portableStore struct {
	mu       sync.Mutex
	requests []struct {
		method string
		path   string
		query  string
		auth   string
		body   []byte
	}
	exportBody string
	importBody string
}

func newPortableStore(t *testing.T) (*portableStore, *httptest.Server) {
	t.Helper()
	ps := &portableStore{exportBody: portableFixture, importBody: `{"imported":2,"created":1,"deduped":1,"skipped":0}`}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		ps.mu.Lock()
		ps.requests = append(ps.requests, struct {
			method string
			path   string
			query  string
			auth   string
			body   []byte
		}{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), raw})
		answer := ps.importBody
		if r.URL.Path == "/api/v1/memory/export" {
			answer = ps.exportBody
		}
		ps.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(srv.Close)
	return ps, srv
}

func (ps *portableStore) last() (method, path, query, auth string, body []byte) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if len(ps.requests) == 0 {
		return "", "", "", "", nil
	}
	r := ps.requests[len(ps.requests)-1]
	return r.method, r.path, r.query, r.auth, r.body
}

func TestMemoryExportWritesThePortableFile(t *testing.T) {
	ps, srv := newPortableStore(t)
	t.Setenv("OPENVIBE_TOKEN", "the-person-token")
	dir := t.TempDir()
	out := filepath.Join(dir, "memory.json")
	var stdout, stderr bytes.Buffer
	code := cmdMemory([]string{"export", "--out", out, "--endpoint", srv.URL}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("export exited %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Wrote 2 notes") {
		t.Fatalf("stdout: %s", stdout.String())
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != portableFixture {
		t.Fatalf("the file is not the store's answer: %s", raw)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("the portable file is %o, not 0600", info.Mode().Perm())
	}
	method, path, query, auth, _ := ps.last()
	if method != http.MethodGet || path != "/api/v1/memory/export" || query != "" {
		t.Fatalf("the store saw %s %s?%s", method, path, query)
	}
	if auth != "Bearer the-person-token" {
		t.Fatalf("the store saw auth %q", auth)
	}
	if strings.Contains(stdout.String(), "the-person-token") {
		t.Fatal("the token was printed")
	}
}

func TestMemoryExportRefusesToOverwriteWithoutForce(t *testing.T) {
	_, srv := newPortableStore(t)
	t.Setenv("OPENVIBE_TOKEN", "the-person-token")
	dir := t.TempDir()
	out := filepath.Join(dir, "memory.json")
	if err := os.WriteFile(out, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := cmdMemory([]string{"export", "--out", out, "--endpoint", srv.URL}, strings.NewReader(""), &stdout, &stderr)
	if code == 0 {
		t.Fatal("an existing file was replaced without --force")
	}
	raw, _ := os.ReadFile(out)
	if string(raw) != "keep me" {
		t.Fatalf("the file was replaced: %s", raw)
	}
	stdout.Reset()
	stderr.Reset()
	code = cmdMemory([]string{"export", "--out", out, "--force", "--endpoint", srv.URL}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("--force exited %d: %s", code, stderr.String())
	}
}

func TestMemoryExportScopeReachesTheStore(t *testing.T) {
	ps, srv := newPortableStore(t)
	t.Setenv("OPENVIBE_TOKEN", "the-person-token")
	var stdout, stderr bytes.Buffer
	code := cmdMemory([]string{"export", "--out", "-", "--scope", "project:demo", "--endpoint", srv.URL}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("export exited %d: %s", code, stderr.String())
	}
	_, _, query, _, _ := ps.last()
	if query != "scope=project%3Ademo" {
		t.Fatalf("query: %s", query)
	}
	if !strings.Contains(stdout.String(), `"openvibe.memory-export@1"`) {
		t.Fatalf("stdout did not carry the file: %s", stdout.String())
	}
}

func TestMemoryImportAsksAndSendsTheFile(t *testing.T) {
	ps, srv := newPortableStore(t)
	t.Setenv("OPENVIBE_TOKEN", "the-person-token")
	dir := t.TempDir()
	in := filepath.Join(dir, "memory.json")
	if err := os.WriteFile(in, []byte(portableFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := cmdMemory([]string{"import", "--file", in, "--endpoint", srv.URL}, strings.NewReader("n\n"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("import exited %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Nothing was imported") {
		t.Fatalf("a no did not stop the import: %s", stdout.String())
	}
	if method, _, _, _, _ := ps.last(); method != "" {
		t.Fatalf("the store was called after a no: %s", method)
	}

	stdout.Reset()
	stderr.Reset()
	code = cmdMemory([]string{"import", "--file", in, "--yes", "--endpoint", srv.URL}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("import --yes exited %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "1 new, 1 already there") {
		t.Fatalf("stdout: %s", stdout.String())
	}
	method, path, _, auth, body := ps.last()
	if method != http.MethodPost || path != "/api/v1/memory/import" {
		t.Fatalf("the store saw %s %s", method, path)
	}
	if auth != "Bearer the-person-token" {
		t.Fatalf("auth: %s", auth)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil || doc["format"] != "openvibe.memory-export@1" {
		t.Fatalf("the body is not the portable file: %s", body)
	}
}

func TestMemoryImportDryRunSendsNothing(t *testing.T) {
	ps, srv := newPortableStore(t)
	t.Setenv("OPENVIBE_TOKEN", "the-person-token")
	dir := t.TempDir()
	in := filepath.Join(dir, "memory.json")
	if err := os.WriteFile(in, []byte(portableFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := cmdMemory([]string{"import", "--file", in, "--dry-run", "--endpoint", srv.URL}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("dry run exited %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "2 notes") {
		t.Fatalf("stdout: %s", stdout.String())
	}
	if method, _, _, _, _ := ps.last(); method != "" {
		t.Fatalf("a dry run called the store: %s", method)
	}
}

func TestMemoryImportRefusesAnotherFormatBeforeTheStore(t *testing.T) {
	ps, srv := newPortableStore(t)
	t.Setenv("OPENVIBE_TOKEN", "the-person-token")
	dir := t.TempDir()
	in := filepath.Join(dir, "not-memory.json")
	if err := os.WriteFile(in, []byte(`{"format":"something.else@1","version":1,"items":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := cmdMemory([]string{"import", "--file", in, "--yes", "--endpoint", srv.URL}, strings.NewReader(""), &stdout, &stderr)
	if code == 0 {
		t.Fatal("another format was imported")
	}
	if method, _, _, _, _ := ps.last(); method != "" {
		t.Fatalf("the store was called for another format: %s", method)
	}
}

func TestMemoryUsageNamesExportAndImport(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := cmdMemory([]string{"help"}, strings.NewReader(""), &stdout, &stderr); code != 2 {
		t.Fatalf("help exited %d", code)
	}
	usage := stderr.String()
	for _, want := range []string{"memory export", "memory import", "--out FILE", "--file FILE"} {
		if !strings.Contains(usage, want) {
			t.Fatalf("the usage does not name %q:\n%s", want, usage)
		}
	}
}
