//go:build windows

package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OpenVibers/OpenVibe.Link/internal/config"
	"github.com/OpenVibers/OpenVibe.Link/internal/protocol"
	"github.com/OpenVibers/OpenVibe.Link/internal/skills"
)

// TestMain lets this test binary stand in for openvibe-link as the job sandbox: the skill runner starts a tool through
// `<this binary> job-sandbox -- node …` (OPENVIBE_CONFINE names os.Executable()).
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == SandboxArg {
		os.Exit(SandboxMain(os.Args[2:], os.Stderr))
	}
	os.Exit(m.Run())
}

// TestSkillRunnerJobOnWindows: on Windows the worker runs the skill runner (openvibe-skill-runner@1.1.0) in a Job Object
// and its tool in an AppContainer: a made tool's inline code runs and answers its input (the result back through
// OPENVIBE_RESULT_FILE), while the sandbox keeps it off the network (the internet and loopback) and from writing or
// reading outside its folder, on top of Node's permission model. The probe checks the AppContainer alone. Any other
// function, and a runner too old to confine its tool, are refused. (Plan §AD.13 (2): tools on Windows too.) Skips
// where there is no Node.js 22.13+ or the sandbox does not hold, unless OPENVIBE_WORKER_TESTS=require (CI).
func TestSkillRunnerJobOnWindows(t *testing.T) {
	require := os.Getenv("OPENVIBE_WORKER_TESTS") == "require"
	node, err := skills.FindNode("")
	if err != nil {
		if require {
			t.Fatalf("no Node.js for the skill runner: %v", err)
		}
		t.Skipf("no Node.js for the skill runner: %v", err)
	}
	cfg := config.Default()
	dir := t.TempDir()
	if _, _, err := skills.Enable(cfg, dir, node); err != nil {
		t.Fatal(err)
	}
	wc := cfg.Worker
	wc.Enabled = true
	wc.AllowSameUser = true
	wc.Caps = config.WorkerCaps{MaxMemBytes: 8 << 30, MaxDiskBytes: 256 << 20, MaxVMBytes: 1 << 46}
	s := &sink{}
	w := New(wc, s.send, quiet())
	if err := w.Probe(); err != nil {
		if require {
			t.Fatalf("the Windows sandbox does not hold: %v", err)
		}
		t.Skipf("the Windows sandbox is unavailable here: %v", err)
	}
	t.Cleanup(w.Close)

	escape := filepath.Join(os.TempDir(), "ov-escape-test")
	os.Remove(escape)
	t.Cleanup(func() { os.Remove(escape) })
	secret := filepath.Join(os.TempDir(), "ov-secret-test")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(secret) })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	source := `const fs = require('fs'); const net = require('net');
let input = ''; let left = 2;
const out = {};
function done() { if (--left > 0) return; process.stdout.write(JSON.stringify(out)); process.exit(0); }
function connect(key, port, host) {
    let ended = false; const end = (v) => { if (ended) return; ended = true; out[key] = v; done(); };
    const s = net.connect(port, host);
    s.on('connect', () => end('ALLOWED'));
    s.on('error', (e) => end(e.code || 'ERR'));
    setTimeout(() => end('TIMEOUT'), 4000);
}
process.stdin.on('data', (d) => input += d).on('end', () => {
    out.input = JSON.parse(input);
    try { fs.writeFileSync(out.input.escape, 'x'); out.write = 'ALLOWED'; } catch (e) { out.write = e.code || 'ERR'; }
    try { fs.readFileSync(out.input.secret); out.read = 'ALLOWED'; } catch (e) { out.read = e.code || 'ERR'; }
    connect('net', 53, '1.1.1.1');
    connect('loopback', out.input.port, '127.0.0.1');
});`
	sum := sha256.Sum256([]byte(source))
	port := ln.Addr().(*net.TCPAddr).Port
	args, _ := json.Marshal(map[string]any{"tool": "win_check", "source": source, "sha256": hex.EncodeToString(sum[:]),
		"input": map[string]any{"n": 7, "escape": escape, "secret": secret, "port": port}})
	j := testJob(1, skills.Name)
	j.Artifact = &protocol.Artifact{Name: skills.Name, Version: skills.Version}
	j.Args = args
	j.Limits.WallMS = 30000
	if f, r := w.Admit(j); f != "" {
		t.Fatalf("refused: %s %s", f, r)
	}
	w.Launch(j.ID)
	ex := s.exit(t, j.ID)
	if ex.Reason != protocol.ExitExited || ex.Code == nil || *ex.Code != 0 {
		t.Fatalf("%+v result %s", ex, ex.Result)
	}
	var got struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Stderr string `json:"stderr"`
		Output struct {
			Input    map[string]any `json:"input"`
			Write    string         `json:"write"`
			Read     string         `json:"read"`
			Net      string         `json:"net"`
			Loopback string         `json:"loopback"`
		} `json:"output"`
	}
	if err := json.Unmarshal(ex.Result, &got); err != nil || !got.OK {
		t.Fatalf("result %s: %v", ex.Result, err)
	}
	if got.Output.Input["n"] != float64(7) {
		t.Fatalf("the tool got %v", got.Output.Input)
	}
	for what, v := range map[string]string{"wrote outside its folder": got.Output.Write, "read outside its folder": got.Output.Read,
		"reached the internet": got.Output.Net, "reached loopback": got.Output.Loopback} {
		if v == "ALLOWED" {
			t.Fatalf("the tool %s", what)
		}
	}
	if _, err := os.Stat(escape); err == nil {
		t.Fatal("a file appeared outside the job's folder")
	}
	t.Logf("confined: write %s, read %s, internet %s, loopback %s", got.Output.Write, got.Output.Read, got.Output.Net, got.Output.Loopback)

	// Any other function is refused at isolate: the Windows worker runs the skill runner only.
	if _, err := isolate(nil, wc, plan{root: dir, fn: config.FunctionConfig{Name: "compute", Class: config.ClassFunction}}); err == nil {
		t.Fatal("another function was not refused")
	}
	// A runner from before the Windows sandbox would start its tool outside the AppContainer: refused.
	old := filepath.Join(t.TempDir(), "old-runner.js")
	if err := os.WriteFile(old, []byte("// openvibe-skill-runner 1.1.0, no sandbox\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fn := config.FunctionConfig{Name: skillRunnerName, Class: config.ClassFunction, Command: []string{node.Path, old, "job"}}
	if _, err := isolate(nil, wc, plan{root: t.TempDir(), fn: fn}); err == nil || !strings.Contains(err.Error(), "skills on") {
		t.Fatalf("an old runner: %v", err)
	}
}

// TestJobSandboxCommandRuns: `openvibe-link job-sandbox -- PROGRAM` runs PROGRAM in the AppContainer and passes its
// exit code and output back; a bad command line is a usage error.
func TestJobSandboxCommandRuns(t *testing.T) {
	if code := SandboxMain(nil, &strings.Builder{}); code != 2 {
		t.Fatalf("no program: %d", code)
	}
	if _, err := appContainerSID(); err != nil {
		if os.Getenv("OPENVIBE_WORKER_TESTS") == "require" {
			t.Fatalf("no AppContainer: %v", err)
		}
		t.Skipf("no AppContainer here: %v", err)
	}
	code, err := runConfined([]string{filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"), "/c", "exit", "7"})
	if err != nil || code != 7 {
		t.Fatalf("exit code %d, err %v", code, err)
	}
}
