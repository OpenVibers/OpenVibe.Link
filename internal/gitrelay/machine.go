package gitrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MachineRepo is one repository under the root the person chose in Link, as the page lists it.
type MachineRepo struct {
	Name        string    `json:"name"`
	Branch      string    `json:"branch"`
	Head        string    `json:"head"`
	HeadSubject string    `json:"head_subject,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

// MachineOptions tune a published machine.
type MachineOptions struct {
	Root   string
	Name   string // the computer's name; defaults to the host name
	OS     string // linux, darwin or windows; defaults to runtime.GOOS
	Client *http.Client
	Log    *slog.Logger
	GitBin string
	// PollTimeout is how long Git may hold one watch; it is under the client's deadline.
	PollTimeout time.Duration
	// Approve asks the person on this machine to allow one clone the page addressed here, and ApproveOverwrite asks
	// again when the folder already has files. A nil Approve refuses every clone: a page's wish alone writes nothing.
	Approve          func(ctx context.Context, req CloneRequest) (CloneDecision, error)
	ApproveOverwrite func(ctx context.Context, req CloneRequest, folder string) (bool, error)
	Now              func() time.Time
	// AllowLocal lets a test clone a file:// repository. Production requests are https only.
	AllowLocal bool
}

// MachineRequest is one question from the page, carried on the watch the machine opened: a read-only tree, file or
// log, or a clone the person asked for on their repository page ("Clone to <this computer>").
type MachineRequest struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // tree, file or log, or clone
	Repo string `json:"repo"`
	Ref  string `json:"ref,omitempty"`
	Path string `json:"path,omitempty"`
	// A clone request: its clr_… id (the row the page follows), the https URL with a short read token (only this
	// machine ever sees it), who asked on the page, and when the two minutes end.
	CloneID     string    `json:"clone_id,omitempty"`
	URL         string    `json:"url,omitempty"`
	RequestedBy string    `json:"requested_by,omitempty"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
}

// Watch is Git's answer to one watch.
type Watch struct {
	Online  bool            `json:"online"`
	Request *MachineRequest `json:"request,omitempty"`
}

// MachineConnect publishes this computer's repositories, read-only, until ctx ends. The machine polls; Git can
// never open a connection to it. A tree, file or log answer is sent back over the same poll.
func MachineConnect(ctx context.Context, raw string, o MachineOptions) error {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.GitBin == "" {
		o.GitBin = "git"
	}
	if o.PollTimeout <= 0 {
		o.PollTimeout = 25 * time.Second
	}
	ep, err := ParseEndpoint(raw)
	if err != nil {
		return err
	}
	if ep.Kind != "machine" {
		return errors.New("that link is for a clone request, not a published machine")
	}
	root, err := prepareRoot(o.Root)
	if err != nil {
		return err
	}
	if o.Name == "" {
		o.Name, _ = os.Hostname()
	}
	if o.Name == "" {
		o.Name = "this computer"
	}
	if len([]rune(o.Name)) > 80 {
		o.Name = string([]rune(o.Name)[:80])
	}
	if o.OS == "" {
		o.OS = osName()
	}
	client := o.Client
	if client == nil {
		client = &http.Client{}
	}
	wait := 2 * time.Second
	for {
		repos, err := scanRepos(root, o.GitBin)
		if err != nil {
			o.Log.Warn("gitrelay: the root could not be read", "err", err)
			repos = nil
		}
		if err := call(ctx, client, "POST", linkURL(ep, "/hello"), map[string]any{"name": o.Name, "os": o.OS, "repos": repos}, nil); err != nil {
			if terminal(err) {
				return err
			}
			o.Log.Warn("gitrelay: hello failed; trying again", "err", err)
			if !sleep(ctx, wait) {
				return ctx.Err()
			}
			wait = nextWait(wait)
			continue
		}
		wait = 2 * time.Second
		for ctx.Err() == nil {
			pollCtx, cancel := context.WithTimeout(ctx, o.PollTimeout+10*time.Second)
			var w Watch
			err := call(pollCtx, client, "GET", linkURL(ep, "/watch"), nil, &w)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if terminal(err) {
					return err
				}
				o.Log.Warn("gitrelay: watch failed; trying again", "err", err)
				break
			}
			if w.Request == nil {
				continue
			}
			if w.Request.Kind == "clone" {
				serveMachineClone(ctx, ep, client, o, *w.Request)
				continue
			}
			body := answer(ctx, root, o.GitBin, *w.Request)
			if err := call(ctx, client, "POST", linkURL(ep, "/results"), body, nil); err != nil {
				if terminal(err) {
					return err
				}
				o.Log.Warn("gitrelay: an answer was not delivered", "err", err)
			}
		}
		if !sleep(ctx, wait) {
			return ctx.Err()
		}
		wait = nextWait(wait)
	}
}

// serveMachineClone runs one clone the page addressed to this computer. The request came over the same watch the
// browsing questions do; the person here is asked before anything runs (ServeClone refuses without an Approve), the
// folder is checked against the root they chose, and each step goes back over the machine's own link.
func serveMachineClone(ctx context.Context, ep Endpoint, client *http.Client, o MachineOptions, req MachineRequest) {
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	if !requestIDRe.MatchString(req.CloneID) {
		log.Warn("gitrelay: a clone request with no usable id was ignored", "repo", req.Repo)
		return
	}
	clone := CloneRequest{ID: req.CloneID, Repo: req.Repo, State: "waiting", CloneURL: req.URL, ExpiresAt: req.ExpiresAt, RequestedBy: req.RequestedBy}
	target := linkURL(ep, "/clone-requests/"+req.CloneID)
	post := func(ctx context.Context, body map[string]any) error {
		// The last answer should not be lost because the watch's context ended.
		postCtx := ctx
		if ctx.Err() != nil {
			var cancel context.CancelFunc
			postCtx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
		}
		return call(postCtx, client, "POST", target, body, nil)
	}
	// The page says "waiting for your answer in Link" from here until the person answers.
	if err := post(ctx, map[string]any{"state": "waiting"}); err != nil {
		log.Warn("gitrelay: the clone request could not be acknowledged", "err", err)
		return
	}
	log.Info("gitrelay: a clone was asked for here", "repo", req.Repo, "by", req.RequestedBy, "expires", req.ExpiresAt)
	res, err := ServeClone(ctx, CloneOptions{
		Root: o.Root, Client: client, Approve: o.Approve, ApproveOverwrite: o.ApproveOverwrite,
		Log: log, Now: o.Now, GitBin: o.GitBin, AllowLocal: o.AllowLocal,
	}, clone, post)
	switch {
	case err != nil:
		log.Warn("gitrelay: the clone did not finish", "repo", req.Repo, "err", err)
	case res.Reason != "":
		log.Info("gitrelay: the clone was refused", "repo", req.Repo, "reason", res.Reason)
	default:
		log.Info("gitrelay: cloned", "repo", req.Repo, "into", res.Folder)
	}
}

func osName() string { return runtime.GOOS }

func terminal(err error) bool {
	var he *httpError
	return errors.As(err, &he) && (he.Status == 401 || he.Status == 403 || he.Status == 404 || he.Status == 410)
}

func nextWait(d time.Duration) time.Duration {
	d *= 2
	if d > 30*time.Second {
		return 30 * time.Second
	}
	return d
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// scanRepoPaths lists the folders under the root that are a repository (a .git directory or file), at the top
// level and one level down, with the path used to read it. Only the name, branch, head and date are sent.
func scanRepoPaths(root, gitBin string) []struct {
	MachineRepo
	Path string
} {
	var out []struct {
		MachineRepo
		Path string
	}
	seen := map[string]bool{}
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := strings.ToLower(e.Name())
			if strings.HasPrefix(name, ".") || seen[name] {
				continue
			}
			full := filepath.Join(dir, e.Name())
			if isRepo(full) {
				if r, ok := readRepo(gitBin, full, name); ok {
					seen[name] = true
					out = append(out, struct {
						MachineRepo
						Path string
					}{r, full})
				}
				continue
			}
			if depth > 0 {
				walk(full, depth-1)
			}
		}
	}
	walk(root, 1)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if len(out) > 500 {
		out = out[:500]
	}
	return out
}

func scanRepos(root, gitBin string) ([]MachineRepo, error) {
	scanned := scanRepoPaths(root, gitBin)
	out := make([]MachineRepo, 0, len(scanned))
	for _, s := range scanned {
		out = append(out, s.MachineRepo)
	}
	return out, nil
}

func isRepo(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && (fi.IsDir() || fi.Mode().IsRegular())
}

var machineRepoNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)

func readRepo(gitBin, dir, name string) (MachineRepo, bool) {
	if !machineRepoNameRe.MatchString(name) || strings.HasSuffix(name, ".git") {
		return MachineRepo{}, false
	}
	branch, ok := gitOut(gitBin, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if !ok {
		return MachineRepo{}, false
	}
	head, ok := gitOut(gitBin, dir, "rev-parse", "HEAD")
	if !ok {
		return MachineRepo{}, false
	}
	r := MachineRepo{Name: name, Branch: strings.TrimSpace(branch), Head: strings.TrimSpace(head)}
	if line, ok := gitOut(gitBin, dir, "log", "-1", "--format=%s%x00%cI", "HEAD"); ok {
		parts := strings.SplitN(line, "\x00", 2)
		r.HeadSubject = short(parts[0])
		if len(parts) == 2 {
			r.UpdatedAt, _ = time.Parse(time.RFC3339, strings.TrimSpace(parts[1]))
		}
	}
	return r, true
}

func gitOut(gitBin, dir string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, gitBin, append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil || out.Len() > 256*1024 {
		return "", false
	}
	return out.String(), true
}

func repoPath(root string, repos map[string]string, name string) (string, error) {
	if !machineRepoNameRe.MatchString(name) {
		return "", errors.New("not a repository on this computer")
	}
	p, ok := repos[name]
	if !ok {
		return "", errors.New("not a repository on this computer")
	}
	return p, nil
}

func answer(ctx context.Context, root, gitBin string, req MachineRequest) map[string]any {
	repos := map[string]string{}
	for _, s := range scanRepoPaths(root, gitBin) {
		repos[s.Name] = s.Path
	}
	out := map[string]any{"request_id": req.ID, "ok": false}
	p, err := repoPath(root, repos, req.Repo)
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	ref, err := validRef(req.Ref)
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	switch req.Kind {
	case "tree":
		rel, err := validRel(req.Path)
		if err != nil {
			out["error"] = err.Error()
			return out
		}
		entries, err := tree(gitBin, p, ref, rel)
		if err != nil {
			out["error"] = short(err.Error())
			return out
		}
		out["ok"], out["result"] = true, map[string]any{"ref": ref, "path": rel, "entries": entries}
	case "file":
		rel, err := validRel(req.Path)
		if err != nil || rel == "" {
			out["error"] = "a file path is required"
			return out
		}
		blob, err := file(gitBin, p, ref, rel)
		if err != nil {
			out["error"] = short(err.Error())
			return out
		}
		out["ok"], out["result"] = true, blob
	case "log":
		rel, err := validRel(req.Path)
		if err != nil {
			out["error"] = err.Error()
			return out
		}
		log, err := history(gitBin, p, ref, rel)
		if err != nil {
			out["error"] = short(err.Error())
			return out
		}
		out["ok"], out["result"] = true, map[string]any{"ref": ref, "commits": log}
	default:
		out["error"] = "unknown request"
	}
	return out
}

var refRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$`)

func validRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	if !refRe.MatchString(ref) || strings.Contains(ref, "..") || strings.Contains(ref, "@{") || strings.HasSuffix(ref, ".") {
		return "", errors.New("not a valid ref")
	}
	return ref, nil
}

// validRel accepts a repository-relative path only: no leading slash, no .., no control characters.
func validRel(p string) (string, error) {
	p = strings.TrimPrefix(strings.TrimSpace(p), "./")
	if p == "" {
		return "", nil
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || strings.Contains(p, "\x00") || len(p) > 4096 {
		return "", errors.New("not a path inside the repository")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, "-") {
			return "", errors.New("not a path inside the repository")
		}
	}
	return p, nil
}

// TreeEntry is one row of a machine repository's tree.
type TreeEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"`
	Size *int64 `json:"size,omitempty"`
}

func tree(gitBin, dir, ref, path string) ([]TreeEntry, error) {
	args := []string{"-C", dir, "ls-tree", "-z", "-l"}
	if ref != "" {
		args = append(args, ref)
	} else {
		args = append(args, "HEAD")
	}
	args = append(args, "--")
	if path != "" {
		args = append(args, path)
	}
	cmd := exec.Command(gitBin, args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return nil, errors.New("git could not read the tree")
	}
	var entries []TreeEntry
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		// <mode> <type> <sha> <size>\t<name>
		tab := bytes.IndexByte(rec, '\t')
		if tab < 0 {
			continue
		}
		meta := strings.Fields(string(rec[:tab]))
		if len(meta) < 4 {
			continue
		}
		name := string(rec[tab+1:])
		if name == "" {
			continue
		}
		typ := meta[1]
		if typ == "commit" {
			typ = "commit"
		}
		e := TreeEntry{Name: filepath.Base(name), Path: name, Type: typ}
		if typ == "blob" && meta[3] != "-" {
			if n, err := strconv.ParseInt(meta[3], 10, 64); err == nil {
				e.Size = &n
			}
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Type != entries[j].Type {
			return entries[i].Type == "tree"
		}
		return entries[i].Name < entries[j].Name
	})
	return entries, nil
}

// File is a machine repository's file, with the same shape Git's own blob view uses.
type File struct {
	Path      string `json:"path"`
	Binary    bool   `json:"binary"`
	Size      int64  `json:"size"`
	Text      string `json:"text,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

const maxFileBytes = 2 * 1024 * 1024

func file(gitBin, dir, ref, path string) (File, error) {
	obj := "HEAD:" + path
	if ref != "" {
		obj = ref + ":" + path
	}
	sizeOut, ok := gitOut(gitBin, dir, "cat-file", "-s", obj)
	if !ok {
		return File{}, errors.New("no such file at that ref")
	}
	size, _ := strconv.ParseInt(strings.TrimSpace(sizeOut), 10, 64)
	f := File{Path: path, Size: size}
	if size > maxFileBytes {
		f.Truncated = true
		return f, nil
	}
	b, ok := gitBytes(gitBin, dir, maxFileBytes+1, "cat-file", "blob", obj)
	if !ok {
		return File{}, errors.New("git could not read the file")
	}
	if len(b) > maxFileBytes {
		f.Truncated = true
		return f, nil
	}
	f.Text = string(b)
	if bytes.IndexByte(b, 0) >= 0 {
		f.Binary, f.Text = true, ""
	}
	return f, nil
}

// gitBytes runs git and returns at most limit bytes of stdout, so a large blob never fills memory.
func gitBytes(gitBin, dir string, limit int, args ...string) ([]byte, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, gitBin, append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, false
	}
	b, _ := io.ReadAll(io.LimitReader(stdout, int64(limit)))
	_ = cmd.Wait()
	return b, true
}

// LogEntry is one commit of a machine repository's history.
type LogEntry struct {
	SHA     string `json:"sha"`
	Short   string `json:"short"`
	Subject string `json:"subject"`
	Message string `json:"message"`
	Author  string `json:"author"`
	Date    string `json:"date"`
}

func history(gitBin, dir, ref, path string) ([]LogEntry, error) {
	args := []string{"-C", dir, "log", "-n", "60", "--format=%H%x1f%h%x1f%s%x1f%an%x1f%aI"}
	if ref != "" {
		args = append(args, ref)
	} else {
		args = append(args, "HEAD")
	}
	args = append(args, "--")
	if path != "" {
		args = append(args, path)
	}
	cmd := exec.Command(gitBin, args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return nil, errors.New("git could not read the history")
	}
	var commits []LogEntry
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\x1f")
		if len(f) < 5 {
			continue
		}
		commits = append(commits, LogEntry{SHA: f[0], Short: f[1], Subject: f[2], Author: f[3], Date: f[4]})
	}
	return commits, nil
}

// decodeResult turns a result body back into the typed shape; it is exported for tests and for a future panel.
func decodeResult[T any](raw json.RawMessage) (T, error) {
	var v T
	err := json.Unmarshal(raw, &v)
	return v, err
}
