//go:build darwin

package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/OpenVibers/OpenVibe.Link/internal/config"
	"github.com/OpenVibers/OpenVibe.Link/internal/protocol"
	"github.com/OpenVibers/OpenVibe.Link/internal/skills"
)

// TestSkillRunnerJobOnMacOS: on macOS the worker runs the skill runner (openvibe-skill-runner@1.1.0) under sandbox-exec:
// a made tool's inline code runs and answers its input, while the sandbox keeps it off the network and from writing
// outside the job's folder, on top of Node's permission model. Any other function is refused. (Plan §AD.13 (2): tools on
// Mac too.) Skips where there is no Node.js 22.13+ or no sandbox-exec, unless OPENVIBE_WORKER_TESTS=require.
func TestSkillRunnerJobOnMacOS(t *testing.T) {
	node, err := skills.FindNode("")
	if err != nil {
		if os.Getenv("OPENVIBE_WORKER_TESTS") == "require" {
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
		if os.Getenv("OPENVIBE_WORKER_TESTS") == "require" {
			t.Fatalf("the macOS sandbox is unavailable: %v", err)
		}
		t.Skipf("the macOS sandbox is unavailable here: %v", err)
	}
	t.Cleanup(w.Close)

	escape := filepath.Join(os.TempDir(), "ov-escape-test")
	os.Remove(escape)
	t.Cleanup(func() { os.Remove(escape) })
	source := `const fs = require('fs'); const net = require('net');
let input = ''; let sent = false;
const out = {};
function done() { if (sent) return; sent = true; process.stdout.write(JSON.stringify(out)); process.exit(0); }
process.stdin.on('data', (d) => input += d).on('end', () => {
    out.input = JSON.parse(input);
    try { fs.writeFileSync(` + "`${out.input.escape}`" + `, 'x'); out.write = 'ALLOWED'; } catch (e) { out.write = e.code || 'ERR'; }
    const s = net.connect(53, '1.1.1.1');
    s.on('connect', () => { out.net = 'ALLOWED'; done(); });
    s.on('error', (e) => { out.net = e.code || 'ERR'; done(); });
    setTimeout(() => { out.net = out.net || 'TIMEOUT'; done(); }, 4000);
});`
	sum := sha256.Sum256([]byte(source))
	args, _ := json.Marshal(map[string]any{"tool": "mac_check", "source": source, "sha256": hex.EncodeToString(sum[:]), "input": map[string]any{"n": 7, "escape": escape}})
	j := testJob(1, skills.Name)
	j.Artifact = &protocol.Artifact{Name: skills.Name, Version: skills.Version}
	j.Args = args
	j.Limits.WallMS = 20000
	if f, r := w.Admit(j); f != "" {
		t.Fatalf("refused: %s %s", f, r)
	}
	w.Launch(j.ID)
	ex := s.exit(t, j.ID)
	if ex.Reason != protocol.ExitExited || ex.Code == nil || *ex.Code != 0 {
		t.Fatalf("%+v result %s", ex, ex.Result)
	}
	var got struct {
		OK     bool `json:"ok"`
		Output struct {
			Input map[string]any `json:"input"`
			Write string         `json:"write"`
			Net   string         `json:"net"`
		} `json:"output"`
	}
	if err := json.Unmarshal(ex.Result, &got); err != nil || !got.OK {
		t.Fatalf("result %s: %v", ex.Result, err)
	}
	if got.Output.Input["n"] != float64(7) {
		t.Fatalf("the tool got %v", got.Output.Input)
	}
	if got.Output.Write == "ALLOWED" {
		t.Fatal("the tool wrote outside its folder")
	}
	if got.Output.Net == "ALLOWED" {
		t.Fatal("the tool reached the network")
	}
	if _, err := os.Stat(escape); err == nil {
		t.Fatal("a file appeared outside the job's folder")
	}

	// Any other function is refused at isolate: the macOS worker runs the skill runner only.
	if _, err := isolate(nil, wc, plan{root: dir, fn: config.FunctionConfig{Name: "compute", Class: config.ClassFunction}}); err == nil {
		t.Fatal("another function was not refused")
	}
}
