// Package codesrunner keeps this machine's OpenVibe.Codes job runner current (Link v0.76.0).
//
// The installer puts the runner (`openvibe-codes job`) in /opt/openvibe-codes at the release it pins, and nothing moved
// it afterwards: a machine paired in October still ran October's runner, and a job Codes started later (an agent home,
// mode 'home', needs 0.12.0) failed there. The owner, 2026-11-17: "make it so link can do all this … rather than
// sshing in". So the Link asks openvibe.codes which runner is current (GET /api/v1/runner → {"version":"0.12.0"}),
// and when it is newer than the one installed:
//
//  1. it installs that release beside the old one (<prefix>.next), from OpenVibe.Codes' own tag on GitHub, the same
//     way the installer does (npm install --prefix … https://codeload.github.com/OpenVibers/OpenVibe.Codes/…). The
//     address is built here from the version: openvibe.codes names a version, never what to download;
//  2. it checks the staged runner: its package says that version, and `node … --version` prints it;
//  3. it carries over what the installer put beside the runner (act, for check jobs);
//  4. it swaps the folders when no job is admitted or running (each job takes its own read-only copy of the folder
//     as it starts, so a running job never sees the swap), and removes the old one.
//
// A failure leaves the installed runner as it was and is tried again later. OPENVIBE_CODES_AUTO_UPDATE=0 turns it off.
package codesrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Defaults: a look every six hours (a runner release is a day's event, not a minute's), sooner again after a
// failure, a short wait for idle when an update is staged, and a first look a little after start.
const (
	DefaultEndpoint = "https://openvibe.codes"
	DefaultInterval = 6 * time.Hour
	RetryInterval   = 30 * time.Minute
	IdleRetry       = 2 * time.Minute
	FirstLook       = 3 * time.Minute
	InstallTimeout  = 10 * time.Minute
	scriptSuffix    = "node_modules/openvibe-codes/bin/openvibe-codes.js"
	tarballBase     = "https://codeload.github.com/OpenVibers/OpenVibe.Codes/tar.gz/refs/tags/v"
)

var semver = regexp.MustCompile(`^(\d{1,4})\.(\d{1,4})\.(\d{1,6})$`)

// Options configure an Updater. Prefix and Node come from the worker's openvibe-codes entry (PrefixOf).
type Options struct {
	Prefix   string // the npm prefix the runner is installed under: /opt/openvibe-codes
	Node     string // the interpreter the entry runs: its npm is beside it
	Endpoint string // openvibe.codes (an https origin; http only on loopback, for tests)
	Busy     func() bool
	Log      *slog.Logger
	HTTP     *http.Client
	// Install runs npm; tests replace it. It gets the staging prefix and the tarball address.
	Install func(ctx context.Context, npm, stage, tarball string) error
	// Version asks the staged runner its version; tests replace it.
	Version  func(ctx context.Context, node, script string) (string, error)
	Interval time.Duration
}

// Updater checks and updates one runner install.
type Updater struct {
	o Options
}

// PrefixOf finds the npm prefix and interpreter of an `openvibe-codes` entry's command (`node <prefix>/node_modules/
// openvibe-codes/bin/openvibe-codes.js job`). ok is false for any other shape: a runner installed some other way
// is not this updater's to move.
func PrefixOf(command []string) (prefix, node string, ok bool) {
	if len(command) < 2 || !filepath.IsAbs(command[0]) || !filepath.IsAbs(command[1]) {
		return "", "", false
	}
	script := filepath.ToSlash(filepath.Clean(command[1]))
	if !strings.HasSuffix(script, "/"+scriptSuffix) {
		return "", "", false
	}
	prefix = filepath.FromSlash(strings.TrimSuffix(script, "/"+scriptSuffix))
	if prefix == "" || prefix == string(filepath.Separator) {
		return "", "", false
	}
	return prefix, command[0], true
}

// Writable says whether this process can stage and swap a runner under prefix: it makes and removes a folder beside it.
// A Link that cannot (an install under root, a Link running as a person) leaves the runner to the installer.
func Writable(prefix string) bool {
	d, err := os.MkdirTemp(filepath.Dir(prefix), ".openvibe-codes-probe-")
	if err != nil {
		return false
	}
	_ = os.Remove(d)
	return true
}

// New makes an Updater. It refuses an endpoint that is not https (loopback http is allowed for tests).
func New(o Options) (*Updater, error) {
	if o.Endpoint == "" {
		o.Endpoint = DefaultEndpoint
	}
	u, err := url.Parse(o.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"))) {
		return nil, fmt.Errorf("codesrunner: the endpoint must be an https origin")
	}
	o.Endpoint = strings.TrimRight(o.Endpoint, "/")
	if o.Prefix == "" || !filepath.IsAbs(o.Prefix) || o.Node == "" {
		return nil, fmt.Errorf("codesrunner: a prefix and an interpreter are needed")
	}
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	if o.Install == nil {
		o.Install = npmInstall
	}
	if o.Version == nil {
		o.Version = runnerVersion
	}
	if o.Busy == nil {
		o.Busy = func() bool { return false }
	}
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
	}
	return &Updater{o: o}, nil
}

// Run looks now and then until ctx ends.
func (u *Updater) Run(ctx context.Context) {
	wait := FirstLook
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		res, err := u.Check(ctx)
		switch {
		case err != nil:
			u.o.Log.Warn("codes runner: no update this time", "err", err)
			wait = RetryInterval
		case res.Updated:
			u.o.Log.Info("codes runner updated", "from", res.From, "to", res.To)
			wait = u.o.Interval
		case res.Waiting:
			wait = IdleRetry
		default:
			wait = u.o.Interval
		}
	}
}

// Result says what one look did.
type Result struct {
	From, To string
	Updated  bool // the new runner is in place
	Waiting  bool // staged, waiting for no job to be running
	Current  bool // already the current release
}

// Check looks once: it asks for the current release and installs it when it is newer.
func (u *Updater) Check(ctx context.Context) (Result, error) {
	have := u.installed()
	want, err := u.current(ctx)
	if err != nil {
		return Result{From: have}, err
	}
	res := Result{From: have, To: want}
	if have != "" && !newer(want, have) {
		res.Current = true
		return res, nil
	}
	stage := u.o.Prefix + ".next"
	if v, _ := readVersion(stage); v != want {
		if err := u.stage(ctx, stage, want); err != nil {
			_ = os.RemoveAll(stage)
			return res, err
		}
	}
	if u.o.Busy() {
		res.Waiting = true
		return res, nil
	}
	if err := u.swap(stage); err != nil {
		return res, err
	}
	res.Updated = true
	return res, nil
}

// current asks openvibe.codes which runner release is current. Only a version is taken from the answer.
func (u *Updater) current(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.o.Endpoint+"/api/v1/runner", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := u.o.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("openvibe.codes answered %d", resp.StatusCode)
	}
	var doc struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&doc); err != nil {
		return "", fmt.Errorf("the runner answer is not JSON: %w", err)
	}
	v := strings.TrimPrefix(strings.TrimSpace(doc.Version), "v")
	if !semver.MatchString(v) {
		return "", fmt.Errorf("the runner answer names no version")
	}
	return v, nil
}

func (u *Updater) installed() string {
	v, _ := readVersion(u.o.Prefix)
	return v
}

func readVersion(prefix string) (string, error) {
	b, err := os.ReadFile(filepath.Join(prefix, "node_modules", "openvibe-codes", "package.json"))
	if err != nil {
		return "", err
	}
	var p struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return "", err
	}
	return p.Version, nil
}

// stage installs the release into a fresh folder beside the runner and checks it.
func (u *Updater) stage(ctx context.Context, stage, want string) error {
	if err := os.RemoveAll(stage); err != nil {
		return err
	}
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return fmt.Errorf("cannot stage the runner beside %s: %w", u.o.Prefix, err)
	}
	ictx, cancel := context.WithTimeout(ctx, InstallTimeout)
	defer cancel()
	npm := filepath.Join(filepath.Dir(u.o.Node), "npm")
	if err := u.o.Install(ictx, npm, stage, tarballBase+want); err != nil {
		return fmt.Errorf("npm could not install OpenVibe.Codes v%s: %w", want, err)
	}
	if got, err := readVersion(stage); err != nil || got != want {
		return fmt.Errorf("the staged runner is %q, not %s", got, want)
	}
	got, err := u.o.Version(ictx, u.o.Node, filepath.Join(stage, filepath.FromSlash(scriptSuffix)))
	if err != nil || strings.TrimPrefix(strings.TrimSpace(got), "v") != want {
		return fmt.Errorf("the staged runner does not start (%q): %v", got, err)
	}
	// What the installer put beside the runner (act, for check jobs) comes along.
	entries, _ := os.ReadDir(u.o.Prefix)
	for _, e := range entries {
		if e.Name() == "node_modules" || e.Name() == "package.json" || e.Name() == "package-lock.json" || e.IsDir() {
			continue
		}
		if err := copyFile(filepath.Join(u.o.Prefix, e.Name()), filepath.Join(stage, e.Name())); err != nil {
			return fmt.Errorf("could not carry %s over: %w", e.Name(), err)
		}
	}
	return nil
}

// swap puts the staged runner in place: the old folder moves aside, the new one takes its name, the old one goes.
func (u *Updater) swap(stage string) error {
	old := u.o.Prefix + ".old"
	_ = os.RemoveAll(old)
	if err := os.Rename(u.o.Prefix, old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("could not move the old runner aside: %w", err)
	}
	if err := os.Rename(stage, u.o.Prefix); err != nil {
		_ = os.Rename(old, u.o.Prefix)
		return fmt.Errorf("could not put the new runner in place: %w", err)
	}
	return os.RemoveAll(old)
}

func copyFile(src, dst string) error {
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// newer says whether a is a later release than b (both x.y.z; anything else is not newer).
func newer(a, b string) bool {
	pa, pb := semver.FindStringSubmatch(a), semver.FindStringSubmatch(b)
	if pa == nil {
		return false
	}
	if pb == nil {
		return true
	}
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x > y
		}
	}
	return false
}

func npmInstall(ctx context.Context, npm, stage, tarball string) error {
	cmd := exec.CommandContext(ctx, npm, "install", "--prefix", stage, "--omit=dev", "--no-audit", "--no-fund", "--no-save", "--loglevel=error", tarball)
	cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(npm)+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		tail := strings.TrimSpace(string(out))
		if len(tail) > 300 {
			tail = tail[len(tail)-300:]
		}
		return fmt.Errorf("%w: %s", err, tail)
	}
	return nil
}

func runnerVersion(ctx context.Context, node, script string) (string, error) {
	out, err := exec.CommandContext(ctx, node, script, "--version").Output()
	return strings.TrimSpace(string(out)), err
}
