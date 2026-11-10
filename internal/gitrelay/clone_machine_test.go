package gitrelay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMachine is Git's end of one published computer: the clone reports the machine posts, in order.
type fakeMachine struct {
	mu    sync.Mutex
	posts []map[string]any
	path  string
}

func newFakeMachine(t *testing.T) (*fakeMachine, string) {
	f := &fakeMachine{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/link/machines/mch_01JQZ8M4T7Y2N5K3B6V9C1X0DA/clone-requests/clr_01JQZ8M4T7Y2N5K3B6V9C1X0DA", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.posts = append(f.posts, body)
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	f.path = "/api/v1/link/machines/mch_01JQZ8M4T7Y2N5K3B6V9C1X0DA/clone-requests/clr_01JQZ8M4T7Y2N5K3B6V9C1X0DA"
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func (f *fakeMachine) states() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.posts))
	for _, p := range f.posts {
		s, _ := p["state"].(string)
		out = append(out, s)
	}
	return out
}

func (f *fakeMachine) last() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.posts) == 0 {
		return nil
	}
	return f.posts[len(f.posts)-1]
}

func machineRequest(srcURL string, expires time.Time) MachineRequest {
	return MachineRequest{
		ID: "req_01JQZ8M4T7Y2N5K3B6V9C1X0DA", Kind: "clone", Repo: "openvibers/demo",
		CloneID: "clr_01JQZ8M4T7Y2N5K3B6V9C1X0DA", URL: srcURL, RequestedBy: "sam", ExpiresAt: expires,
	}
}

func machineEndpoint(base string) Endpoint {
	return Endpoint{Base: base, Kind: "machine", ID: "mch_01JQZ8M4T7Y2N5K3B6V9C1X0DA", Token: strings.Repeat("a", 43)}
}

func TestMachineCloneApproved(t *testing.T) {
	_, srcURL := gitRepo(t)
	root := t.TempDir()
	f, base := newFakeMachine(t)
	opts := MachineOptions{
		Root: root, AllowLocal: true,
		Approve: func(context.Context, CloneRequest) (CloneDecision, error) { return CloneDecision{Approve: true}, nil },
	}
	serveMachineClone(context.Background(), machineEndpoint(base), nil, opts, machineRequest(srcURL, time.Now().Add(2*time.Minute)))

	want := []string{"waiting", "approved", "cloning", "done"}
	if got := f.states(); len(got) < len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[len(got)-1] != want[3] {
		t.Fatalf("states=%v", got)
	}
	if done := f.last(); done["path"] != filepath.Join(root, "demo") {
		t.Fatalf("done path=%v", done["path"])
	}
	if b, err := os.ReadFile(filepath.Join(root, "demo", "README.md")); err != nil || strings.ReplaceAll(string(b), "\r\n", "\n") != "hello\n" {
		t.Fatalf("clone contents: %q %v", b, err)
	}
}

func TestMachineCloneRefusalsWriteNothing(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "elsewhere")
	cases := []struct {
		name     string
		expires  time.Time
		approve  func(context.Context, CloneRequest) (CloneDecision, error)
		over     func(context.Context, CloneRequest, string) (bool, error)
		nonEmpty bool
		want     string
	}{
		{name: "nobody to ask", expires: time.Now().Add(2 * time.Minute), want: "refused"},
		{name: "refused in Link", expires: time.Now().Add(2 * time.Minute), approve: func(context.Context, CloneRequest) (CloneDecision, error) {
			return CloneDecision{Approve: false}, nil
		}, want: "refused"},
		{name: "expired", expires: time.Now().Add(-time.Minute), approve: func(context.Context, CloneRequest) (CloneDecision, error) {
			return CloneDecision{Approve: true}, nil
		}, want: "refused"},
		{name: "outside the root", expires: time.Now().Add(2 * time.Minute), approve: func(context.Context, CloneRequest) (CloneDecision, error) {
			return CloneDecision{Approve: true, Folder: outside}, nil
		}, want: "refused"},
		{name: "folder has files", expires: time.Now().Add(2 * time.Minute), nonEmpty: true,
			approve: func(context.Context, CloneRequest) (CloneDecision, error) { return CloneDecision{Approve: true}, nil },
			over:    func(context.Context, CloneRequest, string) (bool, error) { return false, nil }, want: "refused"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, srcURL := gitRepo(t)
			root := t.TempDir()
			if c.nonEmpty {
				if err := os.MkdirAll(filepath.Join(root, "demo"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "demo", "keep.txt"), []byte("mine\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			f, base := newFakeMachine(t)
			opts := MachineOptions{Root: root, AllowLocal: true, Approve: c.approve, ApproveOverwrite: c.over}
			serveMachineClone(context.Background(), machineEndpoint(base), nil, opts, machineRequest(srcURL, c.expires))
			last := f.last()
			if last == nil || last["state"] != c.want {
				t.Fatalf("last post=%v states=%v", last, f.states())
			}
			if c.name != "folder has files" {
				if _, err := os.Stat(filepath.Join(root, "demo")); !os.IsNotExist(err) {
					t.Fatal("something was written without an approval that allowed it")
				}
			}
			if _, err := os.Stat(outside); !os.IsNotExist(err) {
				t.Fatal("something was made outside the root")
			}
			if c.nonEmpty {
				if b, _ := os.ReadFile(filepath.Join(root, "demo", "keep.txt")); string(b) != "mine\n" {
					t.Fatal("the folder was changed without the second approval")
				}
			}
		})
	}
}

func TestMachineCloneWithoutIDIsIgnored(t *testing.T) {
	f, base := newFakeMachine(t)
	req := machineRequest("https://example.invalid/x.git", time.Now().Add(2*time.Minute))
	req.CloneID = "../../elsewhere"
	serveMachineClone(context.Background(), machineEndpoint(base), nil, MachineOptions{
		Root: t.TempDir(), AllowLocal: true,
		Approve: func(context.Context, CloneRequest) (CloneDecision, error) { return CloneDecision{Approve: true}, nil },
	}, req)
	if got := f.states(); len(got) != 0 {
		t.Fatalf("a request with no usable id posted %v", got)
	}
}

func TestServeCloneExpiredRefusesBeforeAsking(t *testing.T) {
	asked := false
	posts := []map[string]any{}
	res, err := ServeClone(context.Background(), CloneOptions{
		Root: t.TempDir(), AllowLocal: true,
		Approve: func(context.Context, CloneRequest) (CloneDecision, error) {
			asked = true
			return CloneDecision{Approve: true}, nil
		},
	}, CloneRequest{ID: "clr_01JQZ8M4T7Y2N5K3B6V9C1X0DA", Repo: "openvibers/demo",
		CloneURL: "https://example.invalid/x.git", ExpiresAt: time.Now().Add(-time.Second)},
		func(_ context.Context, body map[string]any) error { posts = append(posts, body); return nil })
	if err == nil || asked || res.Reason != "" {
		t.Fatalf("err=%v asked=%v reason=%q", err, asked, res.Reason)
	}
	if len(posts) != 1 || posts[0]["state"] != "refused" {
		t.Fatalf("posts=%v", posts)
	}
}
