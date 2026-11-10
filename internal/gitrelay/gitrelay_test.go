package gitrelay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// fileURL is a file:// URL git understands on this system: file:///C:/… on Windows, file:///tmp/… elsewhere.
func fileURL(dir string) string {
	p := filepath.ToSlash(dir)
	if runtime.GOOS == "windows" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

func TestParseEndpoint(t *testing.T) {
	id := "clr_01JQZ8M4T7Y2N5K3B6V9C1X0DA"
	token := strings.Repeat("a", 43)
	cases := []struct {
		in   string
		kind string
		ok   bool
	}{
		{"https://git.openvibe.codes/api/v1/link/clone-requests/" + id + "?token=" + token, "clone", true},
		{"openvibe-link://clone?url=" + "https%3A%2F%2Fgit.openvibe.codes%2Fapi%2Fv1%2Flink%2Fclone-requests%2F" + id + "%3Ftoken%3D" + token, "clone", true},
		{"http://127.0.0.1:5270/api/v1/link/machines/mch_01JQZ8M4T7Y2N5K3B6V9C1X0DA?token=" + token, "machine", true},
		{"http://git.openvibe.codes/api/v1/link/clone-requests/" + id + "?token=" + token, "", false},
		{"https://git.openvibe.codes/api/v1/link/clone-requests/clone_1?token=" + token, "", false},
		{"https://git.openvibe.codes/api/v1/link/clone-requests/" + id, "", false},
		{"https://git.openvibe.codes/elsewhere/" + id + "?token=" + token, "", false},
	}
	for _, c := range cases {
		ep, err := ParseEndpoint(c.in)
		if c.ok != (err == nil) {
			t.Fatalf("%s: err=%v", c.in, err)
		}
		if err == nil && ep.Kind != c.kind {
			t.Fatalf("%s: kind=%s", c.in, ep.Kind)
		}
	}
}

func TestRepoName(t *testing.T) {
	if _, n, err := RepoName("openvibers/openvibe.codes"); err != nil || n != "openvibe.codes" {
		t.Fatalf("name=%q err=%v", n, err)
	}
	for _, bad := range []string{"../x", "a/../b", "a/b.git", "a", "A/b", "a/b c", "/b"} {
		if _, _, err := RepoName(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// gitRepo makes a small source repository with one commit and returns its folder and file:// URL.
func gitRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=A", "GIT_AUTHOR_EMAIL=a@example.test", "GIT_COMMITTER_NAME=A", "GIT_COMMITTER_EMAIL=a@example.test")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-q", "-m", "first")
	return dir, fileURL(dir)
}

type fakeGit struct {
	mu      sync.Mutex
	request CloneRequest
	posts   []map[string]any
	server  *httptest.Server
}

func newFakeGit(t *testing.T, req CloneRequest, token string) *fakeGit {
	f := &fakeGit{request: req}
	mux := http.NewServeMux()
	path := "/api/v1/link/clone-requests/" + req.ID
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != token {
			http.Error(w, `{"code":"link.token","detail":"bad token"}`, 401)
			return
		}
		switch r.Method {
		case http.MethodGet:
			f.mu.Lock()
			defer f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(f.request)
		case http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.posts = append(f.posts, body)
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.Error(w, "no", 405)
		}
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeGit) link(req CloneRequest, token string) string {
	return f.server.URL + "/api/v1/link/clone-requests/" + req.ID + "?token=" + token
}

func TestCloneApproved(t *testing.T) {
	_, srcURL := gitRepo(t)
	root := t.TempDir()
	req := CloneRequest{ID: "clr_01JQZ8M4T7Y2N5K3B6V9C1X0DA", Repo: "openvibers/demo", State: "pending",
		CloneURL: srcURL, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(2 * time.Minute)}
	token := strings.Repeat("b", 43)
	f := newFakeGit(t, req, token)
	res, err := Clone(context.Background(), f.link(req, token), CloneOptions{
		Root: root, AllowLocal: true,
		Approve: func(context.Context, CloneRequest) (CloneDecision, error) { return CloneDecision{Approve: true}, nil },
	})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	want, _ := filepath.EvalSymlinks(filepath.Join(root, "demo"))
	if res.Folder != want && filepath.Base(res.Folder) != "demo" && res.Folder != filepath.Join(root, "demo") {
		t.Fatalf("folder=%q want %q", res.Folder, want)
	}
	if b, err := os.ReadFile(filepath.Join(want, "README.md")); err != nil || string(b) != "hello\n" {
		t.Fatalf("clone contents: %q %v", b, err)
	}
	if len(f.posts) == 0 || f.posts[len(f.posts)-1]["state"] != "done" {
		t.Fatalf("last post=%v", f.posts[len(f.posts)-1])
	}
}

func TestCloneExpiredDoesNothing(t *testing.T) {
	_, srcURL := gitRepo(t)
	root := t.TempDir()
	req := CloneRequest{ID: "clr_01JQZ8M4T7Y2N5K3B6V9C1X0DA", Repo: "openvibers/demo", State: "pending",
		CloneURL: srcURL, CreatedAt: time.Now().Add(-3 * time.Minute), ExpiresAt: time.Now().Add(-time.Minute)}
	token := strings.Repeat("c", 43)
	f := newFakeGit(t, req, token)
	approved := false
	_, err := Clone(context.Background(), f.link(req, token), CloneOptions{
		Root: root, AllowLocal: true,
		Approve: func(context.Context, CloneRequest) (CloneDecision, error) {
			approved = true
			return CloneDecision{Approve: true}, nil
		},
	})
	if err == nil || approved {
		t.Fatalf("expired request: err=%v approved=%v", err, approved)
	}
	if _, err := os.Stat(filepath.Join(root, "demo")); !os.IsNotExist(err) {
		t.Fatal("expired request made a folder")
	}
	if len(f.posts) == 0 || f.posts[0]["state"] != "refused" {
		t.Fatalf("posts=%v", f.posts)
	}
}

func TestCloneFolderOutsideRootRefused(t *testing.T) {
	_, srcURL := gitRepo(t)
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "elsewhere")
	req := CloneRequest{ID: "clr_01JQZ8M4T7Y2N5K3B6V9C1X0DA", Repo: "openvibers/demo", State: "pending",
		CloneURL: srcURL, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(2 * time.Minute)}
	token := strings.Repeat("d", 43)
	f := newFakeGit(t, req, token)
	_, err := Clone(context.Background(), f.link(req, token), CloneOptions{
		Root: root, AllowLocal: true,
		Approve: func(context.Context, CloneRequest) (CloneDecision, error) {
			return CloneDecision{Approve: true, Folder: outside}, nil
		},
	})
	if err == nil {
		t.Fatal("a folder outside the root was accepted")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatal("something was made outside the root")
	}
}

func TestCloneNonEmptyNeedsSecondApproval(t *testing.T) {
	_, srcURL := gitRepo(t)
	root := t.TempDir()
	folder := filepath.Join(root, "demo")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "keep.txt"), []byte("mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := CloneRequest{ID: "clr_01JQZ8M4T7Y2N5K3B6V9C1X0DA", Repo: "openvibers/demo", State: "pending",
		CloneURL: srcURL, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(2 * time.Minute)}
	token := strings.Repeat("e", 43)
	f := newFakeGit(t, req, token)
	_, err := Clone(context.Background(), f.link(req, token), CloneOptions{
		Root: root, AllowLocal: true,
		Approve:          func(context.Context, CloneRequest) (CloneDecision, error) { return CloneDecision{Approve: true}, nil },
		ApproveOverwrite: func(context.Context, CloneRequest, string) (bool, error) { return false, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(folder, "keep.txt")); string(b) != "mine\n" {
		t.Fatal("the folder was overwritten without the second approval")
	}
}

func TestMachineBrowsing(t *testing.T) {
	src, _ := gitRepo(t)
	if err := os.MkdirAll(filepath.Join(src, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	repos, err := scanRepos(filepath.Dir(src), "git")
	if err != nil || len(repos) == 0 {
		t.Fatalf("repos=%v err=%v", repos, err)
	}
	root := filepath.Dir(src)
	repoName := strings.ToLower(filepath.Base(src))
	treeOut := answer(context.Background(), root, "git", MachineRequest{ID: "r1", Kind: "tree", Repo: repoName, Ref: "main"})
	if treeOut["ok"] != true {
		t.Fatalf("tree: %v", treeOut)
	}
	fileOut := answer(context.Background(), root, "git", MachineRequest{ID: "r2", Kind: "file", Repo: repoName, Ref: "main", Path: "README.md"})
	if fileOut["ok"] != true {
		t.Fatalf("file: %v", fileOut)
	}
	if f := fileOut["result"].(File); !strings.Contains(f.Text, "hello") {
		t.Fatalf("file text=%q", f.Text)
	}
	logOut := answer(context.Background(), root, "git", MachineRequest{ID: "r3", Kind: "log", Repo: repoName, Ref: "main"})
	if logOut["ok"] != true {
		t.Fatalf("log: %v", logOut)
	}
	// A path that escapes the repository is refused, never read.
	bad := answer(context.Background(), root, "git", MachineRequest{ID: "r4", Kind: "file", Repo: repoName, Ref: "main", Path: "../../etc/passwd"})
	if bad["ok"] == true {
		t.Fatal("a traversing path was read")
	}
}
