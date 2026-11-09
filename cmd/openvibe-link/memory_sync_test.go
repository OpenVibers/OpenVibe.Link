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

	"github.com/OpenVibers/OpenVibe.Link/internal/memorystore"
	"github.com/OpenVibers/OpenVibe.Link/internal/memoryupload"
)

// syncStore is a fake memory store: one profile per scope and one list of
// items per scope, with every request recorded.
type syncStore struct {
	mu             sync.Mutex
	requests       []string
	profiles       map[string]string
	items          map[string][]memorystore.ListItem
	profileFailure int
}

func newSyncStore(t *testing.T) (*syncStore, *httptest.Server) {
	t.Helper()
	t.Setenv("OPENVIBE_TOKEN", "the-person-token")
	ms := &syncStore{
		profiles: map[string]string{"user": "I prefer short answers."},
		items:    map[string][]memorystore.ListItem{},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ms.mu.Lock()
		ms.requests = append(ms.requests, r.URL.Path+"?"+r.URL.RawQuery)
		ms.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/memory/profile":
			if ms.profileFailure != 0 {
				w.WriteHeader(ms.profileFailure)
				_, _ = io.WriteString(w, `{"code":"memory.unavailable","detail":"the store is not configured"}`)
				return
			}
			scope := r.URL.Query().Get("scope")
			if scope == "" {
				scope = "user"
			}
			text := ms.profiles[scope]
			if text == "" && scope != "user" {
				text = "This project is a demo project."
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"profile": map[string]any{"scope": scope, "text": text, "built_at": "2026-11-01T00:00:00Z"},
			})
		case "/api/v1/memory/items":
			scope := r.URL.Query().Get("scope")
			_ = json.NewEncoder(w).Encode(map[string]any{"items": ms.items[scope], "count": len(ms.items[scope])})
		default:
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"code":"not_found","detail":"no such route"}`)
		}
	}))
	t.Cleanup(srv.Close)
	return ms, srv
}

func (ms *syncStore) profile(scope, text string) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.profiles[scope] = text
}

func (ms *syncStore) calls() []string {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return append([]string(nil), ms.requests...)
}

// syncTree is a home and a project with hand-written text already there.
type syncTree struct {
	home    string
	project string
	state   string
	hand    map[string]string
}

func newSyncTree(t *testing.T) syncTree {
	t.Helper()
	home, project := t.TempDir(), t.TempDir()
	state := filepath.Join(t.TempDir(), "memory-sync.json")
	tree := syncTree{home: home, project: project, state: state, hand: map[string]string{}}
	for _, rel := range []string{".codex/AGENTS.md", ".claude/CLAUDE.md", ".gemini/GEMINI.md"} {
		tree.write(t, home, rel, "my hand-written text\n")
	}
	for _, rel := range []string{"AGENTS.md", "CLAUDE.md", "GEMINI.md"} {
		tree.write(t, project, rel, "my hand-written text\n")
	}
	return tree
}

func (s syncTree) write(t *testing.T, root, rel, text string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.hand[path]; !ok {
		s.hand[path] = text
	}
}

func (s syncTree) read(t *testing.T, root, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// runSync runs the memory command and gives back the exit code and the output.
func runSync(t *testing.T, args []string, stdin string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := cmdMemory(append([]string{"sync"}, args...), strings.NewReader(stdin), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestMemorySyncIsOffByDefault(t *testing.T) {
	tree := newSyncTree(t)
	ms, srv := newSyncStore(t)
	code, out, errOut := runSync(t, []string{"--user-home", tree.home, "--project", tree.project, "--state", tree.state, "--endpoint", srv.URL, "--yes"}, "")
	if code != 0 {
		t.Fatalf("sync failed with %d: %s", code, errOut)
	}
	if !strings.Contains(out, "No tool is on") {
		t.Fatalf("the command did not say nothing is on:\n%s", out)
	}
	if len(ms.calls()) != 0 {
		t.Fatalf("the store was called %v with no tool on", ms.calls())
	}
	if got := tree.read(t, tree.home, ".codex/AGENTS.md"); got != tree.hand[filepath.Join(tree.home, ".codex/AGENTS.md")] {
		t.Fatalf("a file changed with no tool on: %q", got)
	}
	if _, err := os.Stat(tree.state); !os.IsNotExist(err) {
		t.Fatalf("the state was written with no tool on: %v", err)
	}
}

func TestMemorySyncDryRunWritesNothing(t *testing.T) {
	tree := newSyncTree(t)
	_, srv := newSyncStore(t)
	code, out, errOut := runSync(t, []string{"--tools", "agents", "--user-home", tree.home, "--project", tree.project,
		"--state", tree.state, "--endpoint", srv.URL, "--dry-run", "--yes"}, "")
	if code != 0 {
		t.Fatalf("dry run failed with %d: %s", code, errOut)
	}
	if !strings.Contains(out, "+++ ") || !strings.Contains(out, "About you") {
		t.Fatalf("the dry run did not show the diff:\n%s", out)
	}
	if got := tree.read(t, tree.home, ".codex/AGENTS.md"); !strings.HasSuffix(got, "my hand-written text\n") {
		t.Fatalf("a dry run wrote a file: %q", got)
	}
	if _, err := os.Stat(tree.state); !os.IsNotExist(err) {
		t.Fatalf("a dry run wrote the state: %v", err)
	}
}

func TestMemorySyncShowsTheDiffAndWaitsForTheFirstWrite(t *testing.T) {
	tree := newSyncTree(t)
	_, srv := newSyncStore(t)
	args := []string{"--tools", "agents", "--user-home", tree.home, "--project", tree.project, "--state", tree.state, "--endpoint", srv.URL}
	code, out, errOut := runSync(t, args, "n\nn\n")
	if code != 0 {
		t.Fatalf("sync failed with %d: %s", code, errOut)
	}
	if !strings.Contains(out, "Write this file for the first time?") {
		t.Fatalf("the first write did not ask:\n%s", out)
	}
	if got := tree.read(t, tree.home, ".codex/AGENTS.md"); !strings.HasSuffix(got, "my hand-written text\n") {
		t.Fatalf("a refused write changed the file: %q", got)
	}

	code, out, errOut = runSync(t, args, "y\ny\n")
	if code != 0 {
		t.Fatalf("sync failed with %d: %s", code, errOut)
	}
	homeText := tree.read(t, tree.home, ".codex/AGENTS.md")
	if !strings.HasPrefix(homeText, "my hand-written text\n") {
		t.Fatalf("the hand-written text changed:\n%s", homeText)
	}
	for _, want := range []string{"<!-- openvibe-memory:start -->", "I prefer short answers.", "<!-- openvibe-memory:end -->"} {
		if !strings.Contains(homeText, want) {
			t.Fatalf("the home file does not hold %q:\n%s", want, homeText)
		}
	}
	projectText := tree.read(t, tree.project, "AGENTS.md")
	if !strings.HasPrefix(projectText, "my hand-written text\n") || !strings.Contains(projectText, "This project is a demo project.") {
		t.Fatalf("the project file looks wrong:\n%s", projectText)
	}
	state, err := os.ReadFile(tree.state)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(state), `"confirmed": true`) {
		t.Fatalf("the first write was not confirmed in the state:\n%s", state)
	}
}

func TestMemorySyncDoesNotAskAgainOnceAFileIsConfirmed(t *testing.T) {
	tree := newSyncTree(t)
	ms, srv := newSyncStore(t)
	args := []string{"--tools", "agents", "--user-home", tree.home, "--project", tree.project, "--state", tree.state, "--endpoint", srv.URL}
	if code, _, errOut := runSync(t, args, "y\ny\n"); code != 0 {
		t.Fatalf("the first sync failed: %s", errOut)
	}
	ms.profile("user", "I prefer long answers now.")
	ms.mu.Lock()
	ms.items["project:"+memoryupload.Slug(tree.project)] = []memorystore.ListItem{{
		Scope: "project:" + memoryupload.Slug(tree.project), Kind: "project_rule", Status: "active", Text: "run the narrow test first",
	}}
	ms.mu.Unlock()
	code, out, errOut := runSync(t, args, "")
	if code != 0 {
		t.Fatalf("the second sync failed: %s", errOut)
	}
	if strings.Contains(out, "first time") {
		t.Fatalf("a confirmed file asked again:\n%s", out)
	}
	text := tree.read(t, tree.home, ".codex/AGENTS.md")
	if !strings.Contains(text, "I prefer long answers now.") || strings.Contains(text, "I prefer short answers.") {
		t.Fatalf("the block was not updated:\n%s", text)
	}
	projectText := tree.read(t, tree.project, "AGENTS.md")
	if !strings.Contains(projectText, "- run the narrow test first") {
		t.Fatalf("the project rule is not in the project file:\n%s", projectText)
	}
}

func TestMemorySyncUndoRestoresEveryFile(t *testing.T) {
	tree := newSyncTree(t)
	_, srv := newSyncStore(t)
	args := []string{"--tools", "agents,claude-code,gemini,cursor", "--user-home", tree.home, "--project", tree.project,
		"--state", tree.state, "--endpoint", srv.URL, "--yes"}
	if code, _, errOut := runSync(t, args, ""); code != 0 {
		t.Fatalf("sync failed: %s", errOut)
	}
	cursor := filepath.Join(tree.project, ".cursor", "rules", "openvibe-memory.mdc")
	if _, err := os.Stat(cursor); err != nil {
		t.Fatalf("the Cursor rule was not created: %v", err)
	}
	// The project must not be named again: undo reads the paths from the state.
	code, out, errOut := runSync(t, []string{"--undo", "--user-home", tree.home, "--state", tree.state}, "")
	if code != 0 {
		t.Fatalf("undo failed with %d: %s\n%s", code, errOut, out)
	}
	for path, want := range tree.hand {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s is gone after undo: %v", path, err)
		}
		if string(raw) != want {
			t.Fatalf("undo did not restore %s:\nwant %q\ngot  %q", path, want, string(raw))
		}
	}
	if _, err := os.Stat(cursor); !os.IsNotExist(err) {
		t.Fatalf("undo left the created Cursor rule behind: %v", err)
	}
	st, err := os.ReadFile(tree.state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(st), `"enabled": true`) {
		t.Fatalf("a tool is still on after undo:\n%s", st)
	}
}

func TestMemorySyncDisableRemovesOnlyThatToolsBlock(t *testing.T) {
	tree := newSyncTree(t)
	_, srv := newSyncStore(t)
	base := []string{"--user-home", tree.home, "--project", tree.project, "--state", tree.state, "--endpoint", srv.URL}
	if code, _, errOut := runSync(t, append(base, "--tools", "agents,gemini", "--yes"), ""); code != 0 {
		t.Fatalf("sync failed: %s", errOut)
	}
	if code, _, errOut := runSync(t, append(base, "--disable", "agents"), ""); code != 0 {
		t.Fatalf("disable failed: %s", errOut)
	}
	if got := tree.read(t, tree.home, ".codex/AGENTS.md"); got != tree.hand[filepath.Join(tree.home, ".codex/AGENTS.md")] {
		t.Fatalf("disable did not restore the AGENTS.md file: %q", got)
	}
	if got := tree.read(t, tree.home, ".gemini/GEMINI.md"); !strings.Contains(got, "<!-- openvibe-memory:start -->") {
		t.Fatalf("disable touched another tool's file:\n%s", got)
	}
}

func TestMemorySyncWritesAThinClaudeImport(t *testing.T) {
	tree := newSyncTree(t)
	_, srv := newSyncStore(t)
	args := []string{"--tools", "claude-code", "--user-home", tree.home, "--project", tree.project, "--state", tree.state, "--endpoint", srv.URL, "--yes"}
	if code, _, errOut := runSync(t, args, ""); code != 0 {
		t.Fatalf("sync failed: %s", errOut)
	}
	projectText := tree.read(t, tree.project, "CLAUDE.md")
	if !strings.Contains(projectText, "@AGENTS.md") || strings.Contains(projectText, "I prefer short answers.") {
		t.Fatalf("the project CLAUDE.md is not a thin import:\n%s", projectText)
	}
	homeText := tree.read(t, tree.home, ".claude/CLAUDE.md")
	want := "@" + filepath.Join(tree.home, ".codex", "AGENTS.md")
	if !strings.Contains(homeText, want) {
		t.Fatalf("the home CLAUDE.md does not import %q:\n%s", want, homeText)
	}
}

func TestMemorySyncNeedsAProjectForCursor(t *testing.T) {
	tree := newSyncTree(t)
	_, srv := newSyncStore(t)
	code, _, errOut := runSync(t, []string{"--tools", "cursor", "--user-home", tree.home, "--state", tree.state, "--endpoint", srv.URL, "--yes"}, "")
	if code == 0 || !strings.Contains(errOut, "at least one --project") {
		t.Fatalf("cursor without a project was accepted: code=%d err=%s", code, errOut)
	}
}

func TestMemorySyncStoreFailureWritesNothing(t *testing.T) {
	tree := newSyncTree(t)
	ms, srv := newSyncStore(t)
	ms.profileFailure = 503
	code, _, errOut := runSync(t, []string{"--tools", "agents", "--user-home", tree.home, "--project", tree.project,
		"--state", tree.state, "--endpoint", srv.URL, "--yes"}, "")
	if code == 0 {
		t.Fatal("a failed store read was not an error")
	}
	if !strings.Contains(errOut, "memory.unavailable") {
		t.Fatalf("the error does not say what failed: %s", errOut)
	}
	if got := tree.read(t, tree.home, ".codex/AGENTS.md"); !strings.HasSuffix(got, "my hand-written text\n") {
		t.Fatalf("a failed read still wrote a file: %q", got)
	}
}
