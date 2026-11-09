package config

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"testing"
)

// TestWorkerPlanLogins: worker.plan_logins is off unless the owner names the files, and each named file must be a
// path under the Link user's home: the copy a job gets is made from it.
func TestWorkerPlanLogins(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("SUDO_USER", "")
	ok := []WorkerConfig{{}, {PlanLogins: []string{filepath.Join(home, ".claude", ".credentials.json")}},
		{PlanLogins: []string{filepath.Join(home, ".codex", "auth.json"), filepath.Join(home, ".local", "share", "opencode", "auth.json")}}}
	for _, w := range ok {
		c := Default()
		c.Worker = w
		if err := c.Validate(); err != nil {
			t.Fatalf("%+v: %v", w, err)
		}
	}
	bad := []WorkerConfig{{PlanLogins: []string{"relative/path.json"}},
		{PlanLogins: []string{filepath.Join(home, "..", "elsewhere", "auth.json")}},
		{PlanLogins: []string{home}}}
	for _, w := range bad {
		c := Default()
		c.Worker = w
		if err := c.Validate(); err == nil {
			t.Fatalf("%+v was accepted", w)
		}
	}
}

// TestPlanLoginHomeUnderSudo: a `sudo openvibe-link ...` reads the plan logins under the sudo user's home (the home
// install.sh --plan-logins gives the service), not root's; without sudo it is the Link user's own home.
func TestPlanLoginHomeUnderSudo(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the worker and its plan logins are Linux-only")
	}
	who, err := user.Current()
	if err != nil || who.Username == "" || who.Username == "root" || who.HomeDir == "" {
		t.Skip("no non-root user to stand in for the sudo user")
	}
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("SUDO_USER", who.Username)
	got, err := planLoginHome()
	if err != nil || got != who.HomeDir {
		t.Fatalf("under sudo: got %q, %v; want %q", got, err, who.HomeDir)
	}
	t.Setenv("SUDO_USER", "")
	got, err = planLoginHome()
	if err != nil || got != tmp {
		t.Fatalf("without sudo: got %q, %v; want %q", got, err, tmp)
	}
}
