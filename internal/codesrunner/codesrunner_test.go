package codesrunner

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRunner(t *testing.T, prefix, version string) {
	t.Helper()
	dir := filepath.Join(prefix, "node_modules", "openvibe-codes", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prefix, "node_modules", "openvibe-codes", "package.json"), []byte(`{"name":"openvibe-codes","version":"`+version+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "openvibe-codes.js"), []byte("// "+version), 0o644); err != nil {
		t.Fatal(err)
	}
}

type fake struct {
	installs []string
	fail     bool
}

func (f *fake) install(_ context.Context, npm, stage, tarball string) error {
	f.installs = append(f.installs, tarball)
	if f.fail {
		return fmt.Errorf("npm ERR! 404")
	}
	v := tarball[strings.LastIndex(tarball, "/v")+2:]
	writeRunner(&testing.T{}, stage, v)
	return nil
}

func (f *fake) version(_ context.Context, _ string, script string) (string, error) {
	b, err := os.ReadFile(script)
	return strings.TrimPrefix(string(b), "// "), err
}

func codes(version string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runner" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"version":%q,"tarball":"https://evil.example/x.tgz"}`, version)
	}))
}

// The owner (11-17): Link keeps the Codes runner current, nobody logs in. A newer release is installed beside the old
// one from OpenVibe.Codes' own tag (never an address the answer names), checked, and swapped in; act comes along.
func TestTheRunnerMovesToTheCurrentRelease(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "openvibe-codes")
	writeRunner(t, prefix, "0.10.0")
	if err := os.WriteFile(filepath.Join(prefix, "act"), []byte("act-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := codes("0.12.0")
	defer srv.Close()
	f := &fake{}
	u, err := New(Options{Prefix: prefix, Node: "/usr/bin/node", Endpoint: srv.URL, Install: f.install, Version: f.version})
	if err != nil {
		t.Fatal(err)
	}
	res, err := u.Check(context.Background())
	if err != nil || !res.Updated || res.From != "0.10.0" || res.To != "0.12.0" {
		t.Fatalf("check = %+v, %v", res, err)
	}
	if want := "https://codeload.github.com/OpenVibers/OpenVibe.Codes/tar.gz/refs/tags/v0.12.0"; len(f.installs) != 1 || f.installs[0] != want {
		t.Fatalf("installed from %v, want only %s", f.installs, want)
	}
	if v, _ := readVersion(prefix); v != "0.12.0" {
		t.Fatalf("the runner is %q after the swap", v)
	}
	if b, err := os.ReadFile(filepath.Join(prefix, "act")); err != nil || string(b) != "act-binary" {
		t.Fatalf("act did not come along: %v", err)
	}
	for _, gone := range []string{prefix + ".next", prefix + ".old"} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("%s is left behind", gone)
		}
	}
	// Again: nothing to do.
	res, err = u.Check(context.Background())
	if err != nil || !res.Current || len(f.installs) != 1 {
		t.Fatalf("second check = %+v, %v (installs %d)", res, err, len(f.installs))
	}
}

// A job running holds the swap: the release waits staged, and goes in once nothing runs.
func TestTheSwapWaitsForNoJob(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "openvibe-codes")
	writeRunner(t, prefix, "0.11.0")
	srv := codes("0.12.0")
	defer srv.Close()
	f := &fake{}
	busy := true
	u, _ := New(Options{Prefix: prefix, Node: "/usr/bin/node", Endpoint: srv.URL, Install: f.install, Version: f.version, Busy: func() bool { return busy }})
	res, err := u.Check(context.Background())
	if err != nil || !res.Waiting || res.Updated {
		t.Fatalf("busy check = %+v, %v", res, err)
	}
	if v, _ := readVersion(prefix); v != "0.11.0" {
		t.Fatalf("swapped under a running job (%s)", v)
	}
	busy = false
	res, err = u.Check(context.Background())
	if err != nil || !res.Updated {
		t.Fatalf("idle check = %+v, %v", res, err)
	}
	if len(f.installs) != 1 {
		t.Fatalf("the staged release was installed again (%d installs)", len(f.installs))
	}
}

// A failed install or a runner that does not answer its version leaves the old runner as it was.
func TestAFailureLeavesTheRunnerAsItWas(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "openvibe-codes")
	writeRunner(t, prefix, "0.11.0")
	srv := codes("0.12.0")
	defer srv.Close()
	f := &fake{fail: true}
	u, _ := New(Options{Prefix: prefix, Node: "/usr/bin/node", Endpoint: srv.URL, Install: f.install, Version: f.version})
	if _, err := u.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "npm could not install") {
		t.Fatalf("err = %v", err)
	}
	if v, _ := readVersion(prefix); v != "0.11.0" {
		t.Fatalf("the runner moved (%s)", v)
	}
	if _, err := os.Stat(prefix + ".next"); !os.IsNotExist(err) {
		t.Fatal("the failed stage is left behind")
	}
	bad := &fake{}
	u2, _ := New(Options{Prefix: prefix, Node: "/usr/bin/node", Endpoint: srv.URL, Install: bad.install,
		Version: func(context.Context, string, string) (string, error) { return "", fmt.Errorf("SyntaxError") }})
	if _, err := u2.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "does not start") {
		t.Fatalf("err = %v", err)
	}
	if v, _ := readVersion(prefix); v != "0.11.0" {
		t.Fatalf("a runner that does not start was swapped in (%s)", v)
	}
}

// An older or equal release, or an answer without a version, moves nothing.
func TestOnlyANewerReleaseMoves(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "openvibe-codes")
	writeRunner(t, prefix, "0.12.0")
	for _, v := range []string{"0.12.0", "0.9.9", "0.11.10"} {
		srv := codes(v)
		f := &fake{}
		u, _ := New(Options{Prefix: prefix, Node: "/usr/bin/node", Endpoint: srv.URL, Install: f.install, Version: f.version})
		res, err := u.Check(context.Background())
		srv.Close()
		if err != nil || !res.Current || len(f.installs) != 0 {
			t.Fatalf("%s: %+v, %v, installs %v", v, res, err, f.installs)
		}
	}
	srv := codes("latest; rm -rf /")
	defer srv.Close()
	u, _ := New(Options{Prefix: prefix, Node: "/usr/bin/node", Endpoint: srv.URL, Install: (&fake{}).install, Version: (&fake{}).version})
	if _, err := u.Check(context.Background()); err == nil {
		t.Fatal("a version that is not x.y.z was taken")
	}
	if newer("0.12.0", "0.9.0") != true || newer("0.9.0", "0.12.0") != false || newer("1.0.0", "") != true {
		t.Fatal("newer() compares numbers, not strings")
	}
}

// Only the installer's shape is this updater's: node <prefix>/node_modules/openvibe-codes/bin/openvibe-codes.js job.
func TestPrefixOfTheInstallersEntry(t *testing.T) {
	p, node, ok := PrefixOf([]string{"/usr/local/bin/node", "/opt/openvibe-codes/node_modules/openvibe-codes/bin/openvibe-codes.js", "job"})
	if !ok || p != "/opt/openvibe-codes" || node != "/usr/local/bin/node" {
		t.Fatalf("PrefixOf = %q %q %v", p, node, ok)
	}
	for _, c := range [][]string{
		{"/usr/bin/node", "/home/me/OpenVibe.Codes/bin/openvibe-codes.js", "job"},
		{"node", "/opt/openvibe-codes/node_modules/openvibe-codes/bin/openvibe-codes.js"},
		{"/usr/bin/node", "/node_modules/openvibe-codes/bin/openvibe-codes.js"},
		{"/usr/bin/node"},
	} {
		if _, _, ok := PrefixOf(c); ok {
			t.Fatalf("PrefixOf(%v) took a shape that is not the installer's", c)
		}
	}
	if _, err := New(Options{Prefix: "/opt/x", Node: "/usr/bin/node", Endpoint: "http://openvibe.codes"}); err == nil {
		t.Fatal("a plain-http endpoint was taken")
	}
}
