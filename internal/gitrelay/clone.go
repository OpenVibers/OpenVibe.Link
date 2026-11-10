package gitrelay

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// CloneDecision is the person's answer in Link: no, or yes with the folder to use ("" is <root>/<repo name>).
// Folder is a path inside the root; a folder outside it is refused, not clamped.
type CloneDecision struct {
	Approve bool
	Folder  string
}

// CloneOptions tune one clone. The zero value still works: the root is ~/OpenVibe and a nil Approve refuses.
type CloneOptions struct {
	Root             string // the root the person chose in Link
	Client           *http.Client
	Approve          func(ctx context.Context, req CloneRequest) (CloneDecision, error)
	ApproveOverwrite func(ctx context.Context, req CloneRequest, folder string) (bool, error)
	Log              *slog.Logger
	Now              func() time.Time
	GitBin           string
	// AllowLocal lets a test clone a file:// repository. Production links are https only.
	AllowLocal bool
}

// CloneResult is what Link did with one request.
type CloneResult struct {
	Folder string
	Reason string
}

// ErrBusy is another clone running on this machine. Only one runs at a time, whichever process asked.
var ErrBusy = errors.New("another clone is already running on this computer")

var cloneBusy atomic.Bool

var (
	progressPercentRe = regexp.MustCompile(`(\d{1,3})%`)
	lockName          = ".openvibe-link-clone.lock"
)

// Clone serves one clone request: fetch it, ask the person, check the folder, clone with the person's own git and
// report each step back. It refuses before running anything when the request has expired.
func Clone(ctx context.Context, raw string, o CloneOptions) (CloneResult, error) {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.GitBin == "" {
		o.GitBin = "git"
	}
	ep, err := ParseEndpoint(raw)
	if err != nil {
		return CloneResult{}, err
	}
	if ep.Kind != "clone" {
		return CloneResult{}, errors.New("that link is for a published machine, not a clone")
	}
	var req CloneRequest
	if err := call(ctx, o.Client, "GET", linkURL(ep, ""), nil, &req); err != nil {
		return CloneResult{}, err
	}
	if req.State != "pending" {
		return CloneResult{Reason: req.Reason}, fmt.Errorf("the request is already %s", req.State)
	}
	if req.ExpiresAt.Before(o.Now()) {
		_ = postState(ctx, o, ep, map[string]any{"state": "refused", "reason": "the request expired before your Link answered it"})
		return CloneResult{}, errors.New("the request expired after two minutes; ask the page again")
	}
	if !cloneBusy.CompareAndSwap(false, true) {
		return CloneResult{}, ErrBusy
	}
	defer cloneBusy.Store(false)

	root, err := prepareRoot(o.Root)
	if err != nil {
		return CloneResult{}, err
	}
	unlock, err := lockMachine(root)
	if err != nil {
		return CloneResult{}, err
	}
	defer unlock()

	_, repoName, err := RepoName(req.Repo)
	if err != nil {
		_ = postState(ctx, o, ep, map[string]any{"state": "refused", "reason": "the repository name is not valid"})
		return CloneResult{}, err
	}
	folder := filepath.Join(root, repoName)
	if o.Approve == nil {
		_ = postState(ctx, o, ep, map[string]any{"state": "refused", "reason": "nothing on this computer asked for approval"})
		return CloneResult{}, errors.New("no approval on this computer")
	}
	decision, err := o.Approve(ctx, req)
	if err != nil {
		_ = postState(ctx, o, ep, map[string]any{"state": "refused", "reason": "the approval could not be asked: " + short(err.Error())})
		return CloneResult{}, err
	}
	if !decision.Approve {
		_ = postState(ctx, o, ep, map[string]any{"state": "refused", "reason": "refused in Link"})
		return CloneResult{Reason: "refused in Link"}, nil
	}
	if decision.Folder != "" {
		folder = decision.Folder
		if !filepath.IsAbs(folder) {
			folder = filepath.Join(root, folder)
		}
	}
	folder, err = insideRoot(root, folder)
	if err != nil {
		_ = postState(ctx, o, ep, map[string]any{"state": "refused", "reason": "the folder is outside the root you chose in Link"})
		return CloneResult{}, err
	}
	if o.Now().After(req.ExpiresAt) {
		_ = postState(ctx, o, ep, map[string]any{"state": "refused", "reason": "the request expired before the clone started"})
		return CloneResult{}, errors.New("the request expired after two minutes; ask the page again")
	}
	nonEmpty, err := folderNonEmpty(folder)
	if err != nil {
		_ = postState(ctx, o, ep, map[string]any{"state": "failed", "reason": "the folder could not be read"})
		return CloneResult{}, err
	}
	if nonEmpty {
		overwrite := false
		if o.ApproveOverwrite != nil {
			overwrite, err = o.ApproveOverwrite(ctx, req, folder)
			if err != nil {
				_ = postState(ctx, o, ep, map[string]any{"state": "refused", "reason": "the overwrite could not be asked: " + short(err.Error())})
				return CloneResult{}, err
			}
		}
		if !overwrite {
			_ = postState(ctx, o, ep, map[string]any{"state": "refused", "reason": "the folder already has files in it"})
			return CloneResult{Reason: "the folder already has files in it"}, nil
		}
	}
	if !o.AllowLocal && !strings.HasPrefix(req.CloneURL, "https://") {
		_ = postState(ctx, o, ep, map[string]any{"state": "failed", "reason": "the clone URL is not https"})
		return CloneResult{}, errors.New("the clone URL is not https")
	}
	_ = postState(ctx, o, ep, map[string]any{"state": "approved"})
	if err := postState(ctx, o, ep, map[string]any{"state": "cloning", "progress": Progress{Percent: 0, Line: "Starting"}}); err != nil {
		return CloneResult{}, err
	}
	if err := runClone(ctx, o, ep, req.CloneURL, folder, repoName); err != nil {
		return CloneResult{Folder: folder, Reason: err.Error()}, err
	}
	_ = postState(ctx, o, ep, map[string]any{
		"state": "done", "path": folder,
		"progress": Progress{Percent: 100, Line: "Cloned into " + repoName},
	})
	return CloneResult{Folder: folder}, nil
}

func postState(ctx context.Context, o CloneOptions, ep Endpoint, body map[string]any) error {
	// The final answer should not be lost because the context ended with the clone.
	postCtx := ctx
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		postCtx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
	}
	return call(postCtx, o.Client, "POST", linkURL(ep, ""), body, nil)
}

func prepareRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, "OpenVibe")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", fmt.Errorf("the root %s could not be made: %w", abs, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

// insideRoot proves folder is the root or below it after symlinks, so a name with .. or a symlinked parent can
// never put the clone outside the root the person chose.
func insideRoot(root, folder string) (string, error) {
	abs, err := filepath.Abs(folder)
	if err != nil {
		return "", err
	}
	// The folder itself may not exist yet: resolve its nearest existing parent.
	parent, base := filepath.Dir(abs), filepath.Base(abs)
	for {
		if _, err := os.Lstat(parent); err == nil {
			break
		}
		next := filepath.Dir(parent)
		if next == parent {
			break
		}
		base = filepath.Join(filepath.Base(parent), base)
		parent = next
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(resolvedParent, base)
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside %s", folder, root)
	}
	// If it exists, it must not be a symlink: that could point anywhere.
	if fi, err := os.Lstat(candidate); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%s is a symlink", folder)
	}
	return candidate, nil
}

func folderNonEmpty(folder string) (bool, error) {
	entries, err := os.ReadDir(folder)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return len(entries) > 0, nil
}

// lockMachine keeps one clone at a time per root, across processes: O_EXCL lock file with the PID. A lock left by
// a process that no longer runs is removed.
func lockMachine(root string) (func(), error) {
	path := filepath.Join(root, lockName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if pid := readPID(path); pid > 0 {
			if p, e := os.FindProcess(pid); e == nil {
				if e = p.Signal(syscall.Signal(0)); e == nil {
					return nil, ErrBusy
				}
			}
		}
		_ = os.Remove(path)
		f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, ErrBusy
		}
	}
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Close()
	return func() { _ = os.Remove(path) }, nil
}

func readPID(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid
}

func runClone(ctx context.Context, o CloneOptions, ep Endpoint, cloneURL, folder, repoName string) error {
	cmd := exec.CommandContext(ctx, o.GitBin, "clone", "--progress", "--", cloneURL, folder)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=", "GCM_INTERACTIVE=never")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		_ = postState(ctx, o, ep, map[string]any{"state": "failed", "reason": "git could not start"})
		return errors.New("git could not start")
	}
	var lastLine atomic.Value
	lastLine.Store("")
	var lastPost atomic.Int64
	lastPost.Store(time.Now().Add(-time.Hour).UnixNano())
	var wg sync.WaitGroup
	read := func(r io.Reader) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 256*1024)
		sc.Split(splitProgress)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			line = short(line)
			lastLine.Store(line)
			now := time.Now()
			if now.UnixNano()-lastPost.Load() < int64(400*time.Millisecond) && !strings.Contains(line, "Cloning into") {
				continue
			}
			lastPost.Store(now.UnixNano())
			percent := 0
			if m := progressPercentRe.FindStringSubmatch(line); m != nil {
				percent, _ = strconv.Atoi(m[1])
				if percent > 100 {
					percent = 100
				}
			}
			_ = postState(ctx, o, ep, map[string]any{"state": "cloning", "progress": Progress{Percent: percent, Line: line}})
		}
	}
	wg.Add(2)
	go read(stderr)
	go read(stdout)
	err = cmd.Wait()
	wg.Wait()
	if err != nil {
		reason := lastLine.Load().(string)
		if reason == "" {
			reason = "git clone failed"
		}
		_ = postState(ctx, o, ep, map[string]any{"state": "failed", "reason": reason})
		return fmt.Errorf("git clone failed: %s", reason)
	}
	_ = repoName
	return nil
}

// splitProgress splits git's progress on either CR (its progress meter) or LF.
func splitProgress(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i, b := range data {
		if b == '\r' || b == '\n' {
			return i + 1, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// short makes a line safe for the page: one line, no control characters, at most 200 runes.
func short(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > 200 {
		s = string([]rune(s)[:199]) + "…"
	}
	return s
}
