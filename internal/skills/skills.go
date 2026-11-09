// Package skills declares OpenVibe.Link's skill runner: the function that runs one tool of a declared skill as a Run
// job, with this Node's Node.js in the function sandbox (docs/worker.md, "Skills that run code").
package skills

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/OpenVibers/OpenVibe.Link/internal/config"
)

const (
	// Name and Version are the artifact the job names (platform.job@1 artifact.name/version).
	Name    = "openvibe-skill-runner"
	Version = "1.0.0"
	// File is the runner script inside the artifact directory.
	File = "openvibe-skill-runner.js"
	// MinNode is the first Node.js whose permission model is stable behind --permission (v22.13.0).
	MinNode = "22.13.0"
)

//go:embed skill_runner.js
var runnerJS []byte

// DefaultDir is where `skills on` puts the runner when no --dir is given. It is outside the Node's config and state
// directories and outside /tmp, /home and /var, which the worker refuses as an artifact directory.
func DefaultDir() string {
	switch runtime.GOOS {
	case "linux":
		return "/opt/openvibe-skill-runner"
	case "darwin":
		return "/usr/local/lib/openvibe-skill-runner"
	default:
		base := os.Getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		return filepath.Join(base, "OpenVibeSkillRunner")
	}
}

// RunnerPath is the runner script's path in dir.
func RunnerPath(dir string) string { return filepath.Join(dir, File) }

// Node is the Node.js binary a job runs the skill's code with.
type Node struct {
	Path    string // absolute
	Version string // the version node reports, without the leading v
}

// FindNode finds the Node.js the runner runs tools with: --node when given, else /usr/bin/node and /usr/local/bin/node
// (the two the installer uses), else node on PATH. It must report a version and accept the exact permission flags the
// runner passes, so a Node too old for them is refused here rather than at the first job.
func FindNode(explicit string) (Node, error) {
	var candidates []string
	if explicit != "" {
		candidates = []string{explicit}
	} else {
		if runtime.GOOS == "linux" {
			candidates = append(candidates, "/usr/bin/node", "/usr/local/bin/node")
		}
		if p, err := exec.LookPath("node"); err == nil {
			candidates = append(candidates, p)
		}
	}
	var tried []string
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		st, err := os.Stat(abs)
		if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0o111 == 0 {
			tried = append(tried, fmt.Sprintf("%s (not an executable file)", abs))
			continue
		}
		v, err := nodeVersion(abs)
		if err != nil {
			tried = append(tried, fmt.Sprintf("%s (%v)", abs, err))
			continue
		}
		if err := probePermission(abs); err != nil {
			tried = append(tried, fmt.Sprintf("%s %s (%v)", abs, v, err))
			continue
		}
		if err := nodeVisible(abs); err != nil {
			tried = append(tried, fmt.Sprintf("%s %s (%v)", abs, v, err))
			continue
		}
		return Node{Path: abs, Version: v}, nil
	}
	if len(tried) == 0 {
		tried = append(tried, "no node found; --node PATH or install Node.js "+MinNode+" or newer system-wide")
	}
	return Node{}, fmt.Errorf("no usable Node.js (%s or newer, with --permission): %s", MinNode, strings.Join(tried, "; "))
}

// nodeVersion is the version node reports (its --version, without the leading v).
func nodeVersion(path string) (string, error) {
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("--version failed: %w", err)
	}
	v := strings.TrimSpace(string(out))
	v = strings.TrimPrefix(v, "v")
	if v == "" || strings.ContainsAny(v, " \t\n") {
		return "", fmt.Errorf("--version printed %q", strings.TrimSpace(string(out)))
	}
	return v, nil
}

// probePermission runs node with the flags the runner gives a tool, so an old Node that does not know --permission is
// refused when the owner turns the runner on, not silently at the first job.
func probePermission(path string) error {
	dir, err := os.MkdirTemp("", "openvibe-skill-node-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	cmd := exec.Command(path, "--permission", "--allow-fs-read="+dir, "--disallow-code-generation-from-strings", "-e", "")
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("does not support the skill runner's permission flags: %v %s", err, msg)
	}
	return nil
}

// nodeVisible refuses a node under a directory a job's private root never holds (/home, a version manager's store): the
// job would fail to start its tool. Linux only, where the worker runs.
func nodeVisible(path string) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	p := filepath.Clean(path)
	for _, root := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64"} {
		if p == root || strings.HasPrefix(p, root+"/") {
			return nil
		}
	}
	return fmt.Errorf("is outside the system paths a job sees (/usr, /bin, /sbin, /lib*); install Node.js %s system-wide", MinNode)
}

// Entry is the worker.functions entry for node and dir.
func Entry(node Node, dir string) config.FunctionConfig {
	return config.FunctionConfig{
		Name:        Name,
		Version:     Version,
		Class:       config.ClassFunction,
		Command:     []string{node.Path, RunnerPath(dir), "job"},
		ArtifactDir: dir,
	}
}

// On finds Node.js and enables the runner.
func On(cfg *config.Config, dir string, nodeArg string) (config.FunctionConfig, Node, bool, error) {
	node, err := FindNode(nodeArg)
	if err != nil {
		return config.FunctionConfig{}, Node{}, false, err
	}
	entry, changed, err := Enable(cfg, dir, node)
	return entry, node, changed, err
}

// Enable writes the runner script into dir and adds (or replaces) the worker.functions entry for it. It leaves
// worker.enabled alone: the entry alone does not make the worker run jobs.
func Enable(cfg *config.Config, dir string, node Node) (config.FunctionConfig, bool, error) {
	var err error
	dir, err = filepath.Abs(dir)
	if err != nil {
		return config.FunctionConfig{}, false, err
	}
	dir = filepath.Clean(dir)
	if filepath.Dir(dir) == dir { // a filesystem root: no artifact_dir, and a job root must never be /
		return config.FunctionConfig{}, false, errors.New("the skill runner directory must not be a filesystem root")
	}
	entry := Entry(node, dir)
	functions := make([]config.FunctionConfig, 0, len(cfg.Worker.Functions)+1)
	for _, f := range cfg.Worker.Functions {
		if f.Name != Name {
			functions = append(functions, f)
		}
	}
	functions = append(functions, entry)
	cfg.Worker.Functions = functions
	if err := cfg.Validate(); err != nil {
		return config.FunctionConfig{}, false, err
	}
	if err := writeRunner(dir); err != nil {
		return config.FunctionConfig{}, false, err
	}
	return entry, true, nil
}

// Off removes every worker.functions entry named openvibe-skill-runner (of any version or class) and reports whether
// the config changed. It keeps the script on disk: turning the runner back on rewrites it.
func Off(cfg *config.Config) bool {
	functions := make([]config.FunctionConfig, 0, len(cfg.Worker.Functions))
	for _, f := range cfg.Worker.Functions {
		if f.Name != Name {
			functions = append(functions, f)
		}
	}
	if len(functions) == len(cfg.Worker.Functions) {
		return false
	}
	cfg.Worker.Functions = functions
	return true
}

// Status is what `skills status` reports.
type Status struct {
	On              bool     `json:"on"`
	Name            string   `json:"name"`
	Version         string   `json:"version"`
	Class           string   `json:"class"`
	Artifact        string   `json:"artifact_dir,omitempty"`
	ArtifactPresent bool     `json:"artifact_present"`
	Command         []string `json:"command,omitempty"`
	Node            string   `json:"node,omitempty"`
	NodeVersion     string   `json:"node_version,omitempty"`
	NodeError       string   `json:"node_error,omitempty"`
	WorkerEnabled   bool     `json:"worker_enabled"`
}

// StatusOf reports whether the runner is declared and whether its files and Node.js are still there. It does not run
// the Node.js permission probe (status stays cheap); `skills on` does.
func StatusOf(cfg *config.Config) Status {
	s := Status{On: false, Name: Name, Version: Version, Class: config.ClassFunction, WorkerEnabled: cfg.Worker.Enabled}
	for _, f := range cfg.Worker.Functions {
		if f.Name != Name {
			continue
		}
		s.On = true
		s.Version = f.Version
		s.Class = f.EffectiveClass()
		s.Artifact = f.Artifact()
		s.Command = append([]string(nil), f.Command...)
		if st, err := os.Stat(RunnerPath(s.Artifact)); err == nil && st.Mode().IsRegular() {
			s.ArtifactPresent = true
		}
		if len(f.Command) > 0 {
			s.Node = f.Command[0]
			if v, err := nodeVersion(s.Node); err == nil {
				s.NodeVersion = v
			} else {
				s.NodeError = err.Error()
			}
		}
		break
	}
	return s
}

// writeRunner writes the embedded script atomically, readable by the unprivileged job user, into dir.
func writeRunner(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cannot create %s (run with sudo, or give --dir): %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".openvibe-skill-runner-")
	if err != nil {
		return fmt.Errorf("cannot write %s (run with sudo, or give --dir): %w", dir, err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(runnerJS); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, RunnerPath(dir))
}

// CommandString is the declared command as a person reads it.
func CommandString(cmd []string) string {
	quoted := make([]string, len(cmd))
	for i, c := range cmd {
		if c == "" || strings.ContainsAny(c, " \t\"'") {
			quoted[i] = fmt.Sprintf("%q", c)
		} else {
			quoted[i] = c
		}
	}
	return strings.Join(quoted, " ")
}
