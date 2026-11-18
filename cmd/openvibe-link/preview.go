package main

// `openvibe-link preview <dir> [--for 2h]` (plan §AD.25, "preview links"): a folder this computer built, served
// read-only for a while behind an owner-only OpenVibe Connect share, so a check or a reviewer has a page to open. The
// folder is served from loopback only: plain files (GET and HEAD), no listing, no dotfiles, nothing outside it (a link
// that points out is refused), with noindex and a sandboxing Content-Security-Policy. Connect opens the share once
// Network confirms whose computer this is (POST /api/v1/link/previews, Connect #194), and closes it when its time is up.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/connect"
	"github.com/OpenVibers/OpenVibe.Link/internal/credentials"
	"github.com/OpenVibers/OpenVibe.Link/internal/egress"
	"github.com/OpenVibers/OpenVibe.Link/internal/link"
)

const previewUsage = `usage:
  openvibe-link preview <dir> [--for 2h] [--detach]
      serve a built folder (static, read-only) at a short-lived address only you can open, signed in, until the time is
      up or Ctrl-C (5m to 24h; 2h by default). --detach keeps it up in the background and returns once the address is
      printed (for scripts and agents).`

// previewReadyEnv names the file a detached preview writes its address (or why it failed) to, for the command that
// started it (`--detach`): "ok\t<url>\t<until RFC 3339>\n" or "error\t<why>\n".
const previewReadyEnv = "OPENVIBE_LINK_PREVIEW_READY"

const previewDefault = 2 * time.Hour

// previewFor reads --for: empty is two hours; otherwise a Go duration from five minutes to a day.
func previewFor(s string) (time.Duration, error) {
	if strings.TrimSpace(s) == "" {
		return previewDefault, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil || d < 5*time.Minute || d > 24*time.Hour {
		return 0, fmt.Errorf("--for is how long, from 5m to 24h (like 30m or 2h): %q", s)
	}
	return d, nil
}

// previewRoot checks the folder: an existing directory, never the filesystem's root or the home folder itself.
func previewRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("no such folder: %s", dir)
	}
	fi, err := os.Stat(real)
	if err != nil || !fi.IsDir() {
		return "", fmt.Errorf("not a folder: %s", dir)
	}
	home, _ := os.UserHomeDir()
	if real == string(filepath.Separator) || (home != "" && filepath.Clean(real) == filepath.Clean(home)) || filepath.VolumeName(real)+string(filepath.Separator) == real {
		return "", errors.New("serve a built folder (like ./dist), not your whole home or disk")
	}
	return real, nil
}

// previewHandler serves root read-only: GET and HEAD, no listing, no dotfiles, nothing outside root.
func previewHandler(root string) http.Handler {
	files := http.FileServer(http.Dir(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Robots-Tag", "noindex, nofollow")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		// allow-same-origin keeps the preview's own origin: without it the page is an opaque origin, its CSS, scripts and
		// fetches go cross-site, and Connect's sign-in cookie (__Host-ovc, SameSite=Lax) stays home, so they all bounce to
		// sign-in. The cookie is HttpOnly and host-only, and each preview has its own host, so nothing else is reachable.
		h.Set("Content-Security-Policy", "sandbox allow-scripts allow-same-origin allow-forms allow-popups allow-modals allow-downloads")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "a preview is read-only", http.StatusMethodNotAllowed)
			return
		}
		clean := path.Clean("/" + r.URL.Path)
		for _, part := range strings.Split(clean, "/") {
			if strings.HasPrefix(part, ".") {
				http.NotFound(w, r)
				return
			}
		}
		full := filepath.Join(root, filepath.FromSlash(clean))
		real, err := filepath.EvalSymlinks(full)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if real != root && !strings.HasPrefix(real, root+string(filepath.Separator)) {
			http.NotFound(w, r) // a link that points outside the folder
			return
		}
		if fi, err := os.Stat(real); err == nil && fi.IsDir() {
			if _, err := os.Stat(filepath.Join(real, "index.html")); err != nil {
				http.NotFound(w, r) // no listing: a folder serves its index.html or nothing
				return
			}
		}
		files.ServeHTTP(w, r)
	})
}

// previewAPI is Connect's https base: from --connect-server (its wss://…/tunnel URL) when given, else production.
func previewAPI(server string) string {
	if s := strings.TrimSpace(server); s != "" {
		s = strings.TrimSuffix(strings.TrimSuffix(s, "/"), "/tunnel")
		s = strings.Replace(strings.Replace(s, "wss://", "https://", 1), "ws://", "http://", 1)
		return s
	}
	return "https://connect.openvibe.link"
}

type previewShare struct {
	Share     connect.LinkShare `json:"share"`
	ExpiresAt string            `json:"expires_at"`
}

// openPreview asks Connect for the preview share: the computer's node token, and its Network token for Connect to
// confirm whose computer this is.
func openPreview(ctx context.Context, api, connectToken, networkToken string, port int, d time.Duration) (previewShare, error) {
	body, _ := json.Marshal(map[string]any{"port": port, "network_token": networkToken, "expires_in": int(d.Seconds())})
	req, err := http.NewRequestWithContext(ctx, "POST", api+"/api/v1/link/previews", bytes.NewReader(body))
	if err != nil {
		return previewShare{}, err
	}
	req.Header.Set("Authorization", "Bearer "+connectToken)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return previewShare{}, errors.New("OpenVibe Connect could not be reached")
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if resp.StatusCode != http.StatusCreated {
		var p struct {
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(data, &p)
		if p.Detail != "" {
			return previewShare{}, fmt.Errorf("OpenVibe Connect said no: %s", p.Detail)
		}
		return previewShare{}, fmt.Errorf("OpenVibe Connect said no (%d)", resp.StatusCode)
	}
	var out previewShare
	if err := json.Unmarshal(data, &out); err != nil || !connect.ShareIDRe.MatchString(out.Share.ID) {
		return previewShare{}, errors.New("OpenVibe Connect answered something unexpected")
	}
	return out, nil
}

func cmdPreview(g *globals, positional []string, forText, server string, detach bool, stdout, stderr io.Writer) error {
	if len(positional) != 1 {
		fmt.Fprintln(stderr, previewUsage)
		return errUsage
	}
	ready := os.Getenv(previewReadyEnv)
	if ready != "" {
		// The background copy: whatever stops it before the address is up goes to the one that started it.
		err := servePreview(g, positional[0], forText, server, stdout, func(url string, until time.Time) {
			_ = os.WriteFile(ready, []byte("ok\t"+url+"\t"+until.UTC().Format(time.RFC3339)+"\n"), 0o600)
		})
		if err != nil {
			_ = os.WriteFile(ready, []byte("error\t"+strings.ReplaceAll(err.Error(), "\n", " ")+"\n"), 0o600)
		}
		return err
	}
	if detach {
		return detachPreview(g, positional[0], forText, stdout)
	}
	return servePreview(g, positional[0], forText, server, stdout, func(url string, until time.Time) {
		root, _ := previewRoot(positional[0])
		fmt.Fprintf(stdout, "Preview of %s until %s (only you, signed in to OpenVibe, can open it):\n%s\n", root, until.Local().Format("15:04"), url)
	})
}

// previewArgsWithoutDetach is this command line without --detach, for the background copy.
func previewArgsWithoutDetach(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		switch a {
		case "--detach", "-detach", "--detach=true", "-detach=true":
			continue
		}
		out = append(out, a)
	}
	return out
}

// detachPreview starts this same command in the background (its own session, no terminal) and returns once it says
// the address is up, or why not; it waits a minute at most.
func detachPreview(g *globals, dir, forText string, stdout io.Writer) error {
	if _, err := previewFor(forText); err != nil {
		return err
	}
	root, err := previewRoot(dir)
	if err != nil {
		return err
	}
	if creds, err := credentials.Load(g.paths.CredentialFile(), nil); err != nil || !creds.NetworkPaired() {
		return errors.New("this computer is not added to OpenVibe yet: add it on openvibe.bot/machines first")
	}
	f, err := os.CreateTemp("", "openvibe-link-preview-*")
	if err != nil {
		return err
	}
	readyPath := f.Name()
	f.Close()
	defer os.Remove(readyPath)
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, previewArgsWithoutDetach(os.Args[1:])...)
	cmd.Env = append(os.Environ(), previewReadyEnv+"="+readyPath)
	cmd.SysProcAttr = previewDetachAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("the background preview did not start: %w", err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	line, err := waitPreviewReady(readyPath, exited, time.Minute)
	if err != nil {
		return err
	}
	parts := strings.SplitN(strings.TrimSpace(line), "\t", 3)
	if parts[0] != "ok" || len(parts) != 3 {
		if len(parts) >= 2 {
			return errors.New(parts[1])
		}
		return errors.New("the background preview did not start")
	}
	until, _ := time.Parse(time.RFC3339, parts[2])
	fmt.Fprintf(stdout, "Preview of %s until %s (only you, signed in to OpenVibe, can open it):\n%s\n", root, until.Local().Format("15:04"), parts[1])
	fmt.Fprintf(stdout, "It runs in the background (process %d) and closes by itself then.\n", cmd.Process.Pid)
	return nil
}

// waitPreviewReady waits for the background copy's one line, its end, or the time limit.
func waitPreviewReady(path string, exited <-chan struct{}, limit time.Duration) (string, error) {
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if b, err := os.ReadFile(path); err == nil && strings.HasSuffix(string(b), "\n") {
			return string(b), nil
		}
		select {
		case <-exited:
			if b, err := os.ReadFile(path); err == nil && strings.HasSuffix(string(b), "\n") {
				return string(b), nil
			}
			return "", errors.New("the background preview stopped before its address was up")
		case <-deadline.C:
			return "", errors.New("the background preview did not say its address within a minute")
		case <-tick.C:
		}
	}
}

// servePreview serves dir until --for runs out or the process is stopped; up is told the address once it is open.
func servePreview(g *globals, dir, forText, server string, stdout io.Writer, up func(url string, until time.Time)) error {
	d, err := previewFor(forText)
	if err != nil {
		return err
	}
	root, err := previewRoot(dir)
	if err != nil {
		return err
	}
	creds, err := credentials.Load(g.paths.CredentialFile(), nil)
	if err != nil || !creds.NetworkPaired() {
		return errors.New("this computer is not added to OpenVibe yet: add it on openvibe.bot/machines first")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	srv := &http.Server{Handler: previewHandler(root), ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10}
	go srv.Serve(listener)
	defer srv.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	networkToken, err := egress.NewClient(creds).Tokens.Token(ctx)
	if err != nil {
		return errors.New("this computer's OpenVibe sign-in is unavailable")
	}
	connectTokens := link.NewTokenSourceFor(creds, nil, "openvibe-link/"+version, link.ConnectAudience)
	connectToken, err := connectTokens.Token(ctx)
	if err != nil {
		return errors.New("this computer's OpenVibe sign-in is unavailable")
	}
	share, err := openPreview(ctx, previewAPI(server), connectToken.Reveal(), networkToken.Reveal(), port, d)
	if err != nil {
		return err
	}
	until := time.Now().Add(d)
	if t, err := time.Parse(time.RFC3339, share.ExpiresAt); err == nil {
		until = t
	}
	up(share.Share.URL, until)
	ctx, cancel := context.WithDeadline(ctx, until)
	defer cancel()
	err = connect.Run(ctx, connect.Share{ID: share.Share.ID, Port: port}, connect.Options{
		LocalHost: "127.0.0.1", Version: version,
		LinkToken: func(ctx context.Context) (string, error) { t, e := connectTokens.Token(ctx); return t.Reveal(), e },
	})
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || ctx.Err() != nil {
		fmt.Fprintln(stdout, "The preview is closed.")
		return nil
	}
	return err
}
