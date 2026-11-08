package skills

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/OpenVibers/OpenVibe.Node/internal/config"
)

func testNode() Node { return Node{Path: "/usr/bin/node", Version: "24.14.1"} }

func TestEnableOffStatus(t *testing.T) {
	cfg := config.Default()
	dir := t.TempDir()
	entry, changed, err := Enable(cfg, dir, testNode())
	if err != nil || !changed {
		t.Fatalf("Enable: %v (changed %v)", err, changed)
	}
	if entry.Name != Name || entry.Version != Version || entry.Class != config.ClassFunction {
		t.Fatalf("entry %+v", entry)
	}
	if entry.ArtifactDir != dir {
		t.Fatalf("artifact_dir %q, want %q", entry.ArtifactDir, dir)
	}
	want := []string{"/usr/bin/node", RunnerPath(dir), "job"}
	if strings.Join(entry.Command, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("command %q, want %q", entry.Command, want)
	}
	got, err := os.ReadFile(RunnerPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, runnerJS) {
		t.Fatalf("the runner on disk is %d bytes, want the embedded %d", len(got), len(runnerJS))
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm()&0o005 == 0 {
		t.Fatalf("the runner directory is not readable by the job user: %v %v", st, err)
	}

	st := StatusOf(cfg)
	if !st.On || st.Name != Name || st.Version != Version || st.Class != config.ClassFunction {
		t.Fatalf("status %+v", st)
	}
	if st.Artifact != dir || !st.ArtifactPresent {
		t.Fatalf("status artifact %q present %v", st.Artifact, st.ArtifactPresent)
	}
	if len(st.Command) != 3 || st.Command[0] != "/usr/bin/node" {
		t.Fatalf("status command %q", st.Command)
	}

	if !Off(cfg) {
		t.Fatal("Off did not report the removal")
	}
	if Off(cfg) {
		t.Fatal("Off removed a runner that was not declared")
	}
	if StatusOf(cfg).On {
		t.Fatal("status still says on")
	}
}

func TestEnableKeepsOtherFunctionsAndReplacesItself(t *testing.T) {
	other := config.FunctionConfig{Name: "compute", Version: "2.0.0", Command: []string{"/usr/bin/true"}}
	cfg := config.Default()
	cfg.Worker.Functions = []config.FunctionConfig{other}
	dir := t.TempDir()
	if _, _, err := Enable(cfg, dir, testNode()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Enable(cfg, dir, testNode()); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Worker.Functions) != 2 {
		t.Fatalf("%d functions, want the other one and the runner: %+v", len(cfg.Worker.Functions), cfg.Worker.Functions)
	}
	n := 0
	for _, f := range cfg.Worker.Functions {
		if f.Name == Name {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d skill runner entries", n)
	}
}

func TestEnableRefusesRoot(t *testing.T) {
	cfg := config.Default()
	if _, _, err := Enable(cfg, "/", testNode()); err == nil {
		t.Fatal("Enable wrote the runner at /")
	}
	if len(cfg.Worker.Functions) != 0 {
		t.Fatal("a refused Enable changed the config")
	}
}

func TestFindNode(t *testing.T) {
	n, err := FindNode("")
	if err != nil {
		t.Skipf("no usable node on this machine: %v", err)
	}
	if n.Path == "" || n.Version == "" {
		t.Fatalf("%+v", n)
	}
	if runtime.GOOS == "linux" {
		if err := nodeVisible(n.Path); err != nil {
			t.Fatalf("FindNode returned a node a job cannot see: %v", err)
		}
	}
}

func TestNodeVisibleRefusesHome(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the worker runs on Linux only")
	}
	if err := nodeVisible("/home/user/.local/share/fnm/node"); err == nil {
		t.Fatal("a node under /home was accepted: a job's root never holds /home")
	}
	if err := nodeVisible("/usr/local/bin/node"); err != nil {
		t.Fatalf("a system node was refused: %v", err)
	}
}

func TestCommandString(t *testing.T) {
	if got := CommandString([]string{"/usr/bin/node", "/opt/x/my runner.js", "job"}); got != "/usr/bin/node \"/opt/x/my runner.js\" job" {
		t.Fatalf("%q", got)
	}
}

// TestDefaultDirIsNotAWorkerRefusedPath: the default never lies in /tmp, /home or the Node's own directories, which the
// worker's artifact probe refuses (docs/worker.md).
func TestDefaultDirIsNotAWorkerRefusedPath(t *testing.T) {
	d := filepath.Clean(DefaultDir())
	if d == "/" || !filepath.IsAbs(d) {
		t.Fatalf("default dir %q", d)
	}
	if runtime.GOOS != "linux" {
		return
	}
	for _, bad := range []string{"/tmp", "/home", "/var/tmp", "/etc", "/root", "/proc", "/sys", "/dev", "/run"} {
		if d == bad || strings.HasPrefix(d, bad+"/") {
			t.Fatalf("default dir %q is under %s", d, bad)
		}
	}
}
