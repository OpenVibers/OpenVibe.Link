//go:build darwin

package worker

// The job worker on macOS (plan 2026-11-13 §AD.13 (2); the owner, 11-15: tools on "windows and mac" too). macOS has no
// namespaces, so it runs one kind of job only: the skill runner (openvibe-skill-runner, class function), whose tool
// already runs under Node's permission model (no files beyond its own folder, no processes, no workers, no eval). The
// worker adds macOS's own sandbox around the whole job (sandbox-exec, the profile below): no network at all, and no
// file written anywhere but the job's own folder. A job is limited by its wall time (the watchdog); CPU and memory are
// not sampled here. Every other class, a job naming a network, and a host egress other than none are refused.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/OpenVibers/OpenVibe.Link/internal/config"
)

const sandboxExec = "/usr/bin/sandbox-exec"

// errNotLinux: what only Linux has (desks: desk_other.go).
var errNotLinux = errors.New("this runs on Linux only")

// skillRunnerName is the one function this worker runs (internal/skills Name; not imported, to keep the worker free of it).
const skillRunnerName = "openvibe-skill-runner"

// profile is the job's sandbox: everything the default allows (reading the system, running Node), but no network and no
// file written outside the job's folder (and /dev/null). The folder is its real path: /var is a link to /private/var.
func profile(root string) string {
	q := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) }
	return "(version 1)\n(allow default)\n(deny network*)\n(deny file-write*)\n" +
		fmt.Sprintf("(allow file-write* (subpath \"%s\") (literal \"/dev/null\") (literal \"/dev/dtracehelper\"))\n", q(root))
}

func workDir(dir string) string { return dir }

func isolate(cmd *exec.Cmd, cfg config.WorkerConfig, p plan) (*sandbox, error) {
	if p.fn.Name != "" && (p.fn.Class != config.ClassFunction || p.fn.Name != skillRunnerName) {
		return nil, errors.New("on macOS the job worker runs the skill runner only")
	}
	if p.egress != "" {
		return nil, errors.New("on macOS a job runs with no network")
	}
	root, err := filepath.EvalSymlinks(p.root)
	if err != nil {
		return nil, err
	}
	args := append([]string{"sandbox-exec", "-p", profile(root), cmd.Path}, cmd.Args[1:]...)
	cmd.Path, cmd.Args = sandboxExec, args
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &sandbox{}, nil
}

// probe: sandbox-exec is here, the host's egress is none, and a confined process runs and cannot write outside its folder.
func probe(cfg config.WorkerConfig, isolate isolateFunc) error {
	if cfg.Egress != "" && cfg.Egress != config.EgressNone {
		return errors.New("on macOS the job worker runs with worker.egress none only")
	}
	if _, err := os.Stat(sandboxExec); err != nil {
		return fmt.Errorf("the job worker needs %s: %w", sandboxExec, err)
	}
	root, err := os.MkdirTemp("", "openvibe-probe-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	outside := filepath.Join(os.TempDir(), fmt.Sprintf("openvibe-probe-outside-%d", os.Getpid()))
	defer os.Remove(outside)
	cmd := exec.Command("/bin/sh", "-c", `echo ok > "$1/inside" && ! (echo x > "$2") 2>/dev/null`, "probe", root, outside)
	cmd.Env, cmd.Dir = []string{"PATH=/usr/bin:/bin"}, root
	if _, err := isolate(cmd, cfg, plan{root: root}); err != nil {
		return err
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("a confined process did not run as it should: %v %s", err, strings.TrimSpace(string(out)))
	}
	if _, err := os.Stat(outside); err == nil {
		return errors.New("a confined process wrote outside its folder")
	}
	return nil
}

// sandbox holds nothing on macOS: sandbox-exec confines the process from its first instruction.
type sandbox struct{}

func (*sandbox) started()        {}
func (*sandbox) ready(int) error { return nil }
func (*sandbox) run() error      { return nil }
func (*sandbox) oomKilled() bool { return false }
func (*sandbox) close()          {}

// resultFile: the result comes on fd 3 here.
func (*sandbox) resultFile() string { return "" }

func killGroup(p *os.Process) {
	if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err != nil {
		_ = p.Kill()
	}
}

// limitCPU: macOS cannot set another process's RLIMIT_CPU; the watchdog's wall limit bounds the job.
func limitCPU(int, int64) error { return nil }

func pidNS(int) (string, error) { return "", nil }

// sampleProcs: nothing sampled on macOS (the wall limit bounds the job; usage seconds are still sent).
func sampleProcs(int, string) (int64, int64, error) { return 0, 0, nil }

func exitStatus(ps *os.ProcessState) (*int, bool) {
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return nil, ws.Signal() == syscall.SIGXCPU
	}
	if c := ps.ExitCode(); c >= 0 {
		return &c, false
	}
	return nil, false
}

// maxRSS is the peak resident memory macOS reports for the finished process (bytes on macOS).
func maxRSS(ps *os.ProcessState) int64 {
	if ru, ok := ps.SysUsage().(*syscall.Rusage); ok {
		return ru.Maxrss
	}
	return 0
}

// sandboxSupported: not yet; a job here keeps nothing between runs.
const sandboxSupported = false
