//go:build linux

package worker

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/OpenVibers/OpenVibe.Node/internal/config"
	"github.com/OpenVibers/OpenVibe.Node/internal/protocol"
	"github.com/OpenVibers/OpenVibe.Node/internal/skills"
)

// skillRunnerWorker declares the real openvibe-skill-runner@1.0.0 (skills.Enable writes the script it embedded) and a
// fake Media API the job's code input comes from. It skips where the sandbox cannot stand: the skill runner's own tests
// cover its flags, this covers the job it runs in.
func skillRunnerWorker(t *testing.T, m *mediaFake) (*Worker, *sink) {
	t.Helper()
	node, err := skills.FindNode("")
	if err != nil {
		if os.Getenv("OPENVIBE_WORKER_TESTS") == "require" {
			t.Fatalf("the skill runner needs a Node.js a job can see: %v", err)
		}
		t.Skipf("no Node.js a job can see: %v", err)
	}
	cfg := config.Default()
	// Directly under /tmp and open to every user: as root the job runs as nobody, which must search every directory
	// above the artifact (the worker copies it as the job's user), and Go's t.TempDir parents are 0700.
	dir, err := os.MkdirTemp("", "openvibe-skill-runner-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := skills.Enable(cfg, dir, node); err != nil {
		t.Fatal(err)
	}
	wc := cfg.Worker
	wc.Enabled = true
	wc.Caps = config.WorkerCaps{MaxMemBytes: 8 << 30, MaxDiskBytes: 256 << 20, MaxVMBytes: 1 << 46}
	if os.Geteuid() == 0 {
		wc.AllowSameUser, wc.RunAs = false, &config.RunAs{UID: 65534, GID: 65534}
	} else {
		wc.AllowSameUser = true
	}
	m.config(&wc)
	w, s := newRunWorkerConfig(t, wc)
	w.mediaRT = m.srv.Client().Transport
	return w, s
}

// skillJob is one job of the declared runner: code.js is the tool's code, pinned by digest, and args names it.
func skillJob(n int, tool []byte, digest string, args string) protocol.Job {
	j := testJob(n, skills.Name)
	j.Artifact = &protocol.Artifact{Name: skills.Name, Version: skills.Version}
	j.Args = json.RawMessage(args)
	j.Inputs = []protocol.JobInput{{Name: "code.js", MediaID: inputID, SHA256: digest, SizeBytes: int64(len(tool))}}
	return j
}

// TestSkillRunnerJob: the real runner runs the job's code input as a real function job: the tool gets the args' input
// as JSON, no environment, and the permission model's denials (no spawn, no write); the job's result is { ok, output }.
func TestSkillRunnerJob(t *testing.T) {
	tool := []byte(`
const fs = require('fs');
let input = '';
process.stdin.on('data', (d) => input += d).on('end', () => {
    const out = { input: JSON.parse(input), env: Object.keys(process.env), cwd: process.cwd() };
    try { require('child_process').spawnSync('id'); out.spawn = 'ALLOWED'; } catch (e) { out.spawn = e.code; }
    try { fs.writeFileSync('written.txt', 'x'); out.write = 'ALLOWED'; } catch (e) { out.write = e.code; }
    process.stdout.write(JSON.stringify(out));
});
`)
	m := newMediaFake(t)
	m.objects[inputID] = tool
	t.Setenv(testMediaEnv, testMediaToken)
	w, s := skillRunnerWorker(t, m)
	j := skillJob(1, tool, digestOf(tool), `{"tool":"report","entry":"code.js","input":{"n":7}}`)
	run(t, w, j)
	ex := s.exit(t, j.ID)
	if ex.Reason != protocol.ExitExited || ex.Code == nil || *ex.Code != 0 {
		t.Fatalf("%+v result %s", ex, ex.Result)
	}
	var got struct {
		OK     bool `json:"ok"`
		Exit   *int `json:"exit"`
		Output struct {
			Input map[string]any `json:"input"`
			Env   []string       `json:"env"`
			Cwd   string         `json:"cwd"`
			Spawn string         `json:"spawn"`
			Write string         `json:"write"`
		} `json:"output"`
	}
	if err := json.Unmarshal(ex.Result, &got); err != nil {
		t.Fatalf("result %s: %v", ex.Result, err)
	}
	if !got.OK || got.Exit == nil || *got.Exit != 0 {
		t.Fatalf("result %s", ex.Result)
	}
	if got.Output.Input["n"] != float64(7) {
		t.Fatalf("the tool got %v for its input", got.Output.Input)
	}
	if len(got.Output.Env) != 0 {
		t.Fatalf("the tool's environment is not empty: %q", got.Output.Env)
	}
	if got.Output.Spawn != "ERR_ACCESS_DENIED" || got.Output.Write != "ERR_ACCESS_DENIED" {
		t.Fatalf("spawn %q write %q", got.Output.Spawn, got.Output.Write)
	}
	if !strings.HasPrefix(got.Output.Cwd, "/tmp/openvibe-job-") {
		t.Fatalf("the tool's cwd is %q, not the job directory", got.Output.Cwd)
	}
}

// TestSkillRunnerJobNetDeny: the job names no net, so the worker's network namespace holds no network: the tool's socket
// to the internet fails, not because Node's permission model stopped it but because the sandbox has nowhere to send it.
func TestSkillRunnerJobNetDeny(t *testing.T) {
	tool := []byte(`
const net = require('net');
const s = net.connect({ host: '1.1.1.1', port: 80 });
const done = (o) => { process.stdout.write(JSON.stringify(o)); process.exit(0); };
s.on('connect', () => done({ reached: true }));
s.on('error', (e) => done({ reached: false, code: e.code }));
setTimeout(() => done({ reached: true, timeout: true }), 8000);
`)
	m := newMediaFake(t)
	m.objects[inputID] = tool
	t.Setenv(testMediaEnv, testMediaToken)
	w, s := skillRunnerWorker(t, m)
	j := skillJob(2, tool, digestOf(tool), `{"tool":"net","entry":"code.js","input":{}}`)
	run(t, w, j)
	ex := s.exit(t, j.ID)
	var got struct {
		OK     bool `json:"ok"`
		Output struct {
			Reached bool   `json:"reached"`
			Code    string `json:"code"`
		} `json:"output"`
	}
	if err := json.Unmarshal(ex.Result, &got); err != nil || !got.OK {
		t.Fatalf("%+v result %s: %v", ex, ex.Result, err)
	}
	if got.Output.Reached {
		t.Fatalf("the tool reached the internet under net deny: %s", ex.Result)
	}
	if got.Output.Code != "ENETUNREACH" && got.Output.Code != "EHOSTUNREACH" {
		t.Fatalf("the socket failed with %q, not a missing route: %s", got.Output.Code, ex.Result)
	}
}

// TestSkillRunnerDigestMismatch: a code input whose bytes do not match the pinned sha256 ends the job failed before
// anything runs: no sandbox, no job_started, no result.
func TestSkillRunnerDigestMismatch(t *testing.T) {
	tool := []byte(`process.stdout.write('{"ok":true}');`)
	m := newMediaFake(t)
	m.objects[inputID] = tool
	t.Setenv(testMediaEnv, testMediaToken)
	w, s := skillRunnerWorker(t, m)
	j := skillJob(3, tool, strings.Repeat("0", 64), `{"tool":"x","entry":"code.js","input":{}}`)
	run(t, w, j)
	ex := s.exit(t, j.ID)
	if ex.Reason != protocol.ExitFailed || ex.Code != nil || string(ex.Result) != "null" {
		t.Fatalf("%+v result %s", ex, ex.Result)
	}
	if n := s.count(protocol.TypeJobStarted, j.ID); n != 0 {
		t.Fatalf("%d job_started: the runner was started", n)
	}
}
