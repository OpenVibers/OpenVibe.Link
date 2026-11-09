package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/user"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/agent"
	"github.com/OpenVibers/OpenVibe.Link/internal/config"
	"github.com/OpenVibers/OpenVibe.Link/internal/localctl"
)

func reviewSocket(t *testing.T, h localctl.Handler) *globals {
	t.Helper()
	dir := shortHome(t)
	g := &globals{paths: config.HomePaths(dir)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go localctl.Serve(ctx, g.paths.SocketPath(), h)
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		if _, err := os.Stat(g.paths.SocketPath()); err == nil {
			return g
		}
		time.Sleep(time.Millisecond * 10)
	}
	t.Fatal("socket did not start")
	return nil
}

func TestStopReportsPersistenceFailure(t *testing.T) {
	g := reviewSocket(t, func(localctl.Request) localctl.Response {
		return localctl.Response{Error: "stopped, but the latch could not be saved: fixture"}
	})
	var out bytes.Buffer
	err := cmdStop(g, &out)
	if err == nil || !strings.Contains(err.Error(), "could not be saved") || out.Len() != 0 {
		t.Fatalf("failure hidden: %v %q", err, out.String())
	}
	if _, err := os.Stat(g.paths.LatchFile()); !os.IsNotExist(err) {
		t.Fatal("server failure treated as offline fallback")
	}
}

func TestApprovalDisplaysEscapeControls(t *testing.T) {
	hostile := "x\n\r\x1b[2J\u202ey\u2066z\u0085"
	a := agent.Approval{ID: hostile, Kind: hostile, Summary: hostile}
	g := reviewSocket(t, func(localctl.Request) localctl.Response {
		b, _ := json.Marshal([]agent.Approval{a})
		return localctl.Response{OK: true, Data: b}
	})
	var out bytes.Buffer
	if err := cmdApprovals(g, false, &out); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{out.String(), slotLabel(a)} {
		if strings.ContainsAny(s, "\x1b\r\u202e\u2066\u0085") || strings.Contains(s, "x\n") {
			t.Fatalf("unescaped approval: %q", s)
		}
		if !strings.Contains(s, `\u202e`) {
			t.Fatalf("bidi not visibly escaped: %q", s)
		}
	}
}

func TestInstallRefusesPrivilegedComputer(t *testing.T) {
	dir := shortHome(t)
	paths := config.HomePaths(dir)
	cfg := config.Default()
	cfg.Plugins = []config.PluginConfig{{Name: "computer", Config: map[string]any{"level": "trusted"}}}
	if err := config.Save(paths.ConfigFile(), cfg); err != nil {
		t.Fatal(err)
	}
	// Refusal must precede service manager calls and even directory ownership changes.
	g := &globals{paths: paths}
	var out bytes.Buffer
	if err := cmdInstall(g, "", &out); err == nil || !strings.Contains(err.Error(), "computer") {
		t.Fatalf("unsafe install not refused: %v", err)
	}
}

func TestComputerServiceAccountsAndSharedHost(t *testing.T) {
	cfg := config.Default()
	computer := config.PluginConfig{Name: "computer", Config: map[string]any{"level": "trusted"}}
	cfg.Plugins = []config.PluginConfig{computer}
	if err := validateComputerService(cfg, ""); err == nil {
		t.Fatal("default privileged account allowed")
	}
	if runtime.GOOS != "windows" {
		if err := validateComputerService(cfg, "root"); err == nil {
			t.Fatal("UID 0 allowed")
		}
	}
	if current, err := user.Current(); err == nil && current.Uid != "0" {
		if err := validateComputerService(cfg, current.Username); err != nil {
			t.Fatal("ordinary account refused", err)
		}
	}
	computer.Config["desk_isolation"] = "sandbox"
	if err := validateComputerService(cfg, ""); err == nil {
		t.Fatal("shared host without worker allowed")
	}
	cfg.Worker.Enabled = true
	if err := validateComputerService(cfg, ""); (err == nil) != (runtime.GOOS == "linux") {
		t.Fatalf("shared host: %v", err)
	}
	// The installer gives a shared desk host level trusted or observe; nothing else is that host.
	computer.Config["level"] = "observe"
	if err := validateComputerService(cfg, ""); (err == nil) != (runtime.GOOS == "linux") {
		t.Fatalf("shared host at observe: %v", err)
	}
	computer.Config["level"] = "full"
	if err := validateComputerService(cfg, ""); err == nil {
		t.Fatal("a shared desk host at full allowed a privileged service")
	}
	computer.Config["level"] = "trusted"
	cfg.Plugins[0].Disabled = true
	if err := validateComputerService(cfg, ""); err != nil {
		t.Fatal("disabled computer blocks robot install", err)
	}
}
