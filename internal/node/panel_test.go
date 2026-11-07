//go:build !windows

package node

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/OpenVibers/OpenVibe.Node/internal/config"
	"github.com/OpenVibers/OpenVibe.Node/internal/connect"
	"github.com/OpenVibers/OpenVibe.Node/internal/localctl"
	"github.com/OpenVibers/OpenVibe.Node/internal/protocol"
)

func panelTestNode(t *testing.T, paths config.Paths) *Node {
	t.Helper()
	n, err := New(Options{Config: &config.Config{}, Paths: paths, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPanelPauseIsTheLocalKillSwitch(t *testing.T) {
	dir, err := os.MkdirTemp("", "ovp") // short: the control socket path is limited
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	n := panelTestNode(t, config.HomePaths(dir))

	if err := n.panelPause(true); err != nil {
		t.Fatal(err)
	}
	if st := n.latch.State(); !st.Local || !st.Stopped() {
		t.Fatalf("pause did not latch the local stop: %+v", st)
	}
	// The tray's Resume and the window's resume are the same latch.
	if resp := n.control(localctl.Request{Cmd: localctl.CmdResume}); !resp.OK {
		t.Fatal(resp.Error)
	}
	if st := n.latch.State(); st.Local || st.Stopped() {
		t.Fatalf("resume did not clear the local stop: %+v", st)
	}
	// The CLI's stop and the window's pause latch the same thing.
	if resp := n.control(localctl.Request{Cmd: localctl.CmdStop}); !resp.OK {
		t.Fatal(resp.Error)
	}
	if st := n.latch.State(); !st.Local {
		t.Fatalf("openvibe-node stop did not latch: %+v", st)
	}
	if err := n.panelPause(false); err != nil {
		t.Fatal(err)
	}
	if st := n.latch.State(); st.Local {
		t.Fatalf("the window's resume did not clear the CLI's stop: %+v", st)
	}
}

func TestCapabilitySwitchIsARealRestriction(t *testing.T) {
	dir, err := os.MkdirTemp("", "ovp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	paths := config.HomePaths(dir)
	n := panelTestNode(t, paths)

	if err := n.setCapability("files", false); err != nil {
		t.Fatal(err)
	}
	if !n.capabilityOff(protocol.KindFS) {
		t.Fatal("files off does not refuse fs commands")
	}
	if n.capabilityOff(protocol.KindExec) {
		t.Fatal("turning files off also refused exec")
	}
	// It survives a restart.
	n2 := panelTestNode(t, paths)
	if !n2.capabilityOff(protocol.KindFS) {
		t.Fatal("the switch was not kept")
	}
	if err := n2.setCapability("files", true); err != nil {
		t.Fatal(err)
	}
	if n2.capabilityOff(protocol.KindFS) {
		t.Fatal("files on still refuses fs commands")
	}
	// Desktop and Terminal may be turned off but never on from Link.
	if err := n2.setCapability("terminal", false); err != nil {
		t.Fatal(err)
	}
	if err := n2.setCapability("terminal", true); err == nil || !strings.Contains(err.Error(), "gated") {
		t.Fatalf("terminal on: %v", err)
	}
	if err := n2.setCapability("desk", true); err == nil || !strings.Contains(err.Error(), "gated") {
		t.Fatalf("desk on: %v", err)
	}
	if err := n2.setCapability("nope", true); err == nil {
		t.Fatal("an unknown capability was accepted")
	}
}

func TestPanelStateComesFromTheNode(t *testing.T) {
	dir, err := os.MkdirTemp("", "ovp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	n := panelTestNode(t, config.HomePaths(dir))
	if err := n.panelPause(true); err != nil {
		t.Fatal(err)
	}
	st := n.panelState()
	if st.Machine == nil || st.Machine.Name == "" || !st.Machine.Paused {
		t.Fatalf("machine: %+v", st.Machine)
	}
	if len(st.Activity) == 0 || !strings.Contains(st.Activity[0].Text, "stopped") {
		t.Fatalf("activity: %+v", st.Activity)
	}
	// It round-trips as the contract's JSON, with no device or GPU invented.
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{`"devices"`, `"gpu"`, `"earnings_today"`} {
		if strings.Contains(string(b), absent) {
			t.Fatalf("%s was invented: %s", absent, b)
		}
	}
}

func TestPanelURLIsEmptyBeforeRun(t *testing.T) {
	dir, err := os.MkdirTemp("", "ovp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	n := panelTestNode(t, config.HomePaths(dir))
	if u := n.PanelURL(); u != "" {
		t.Fatalf("url before Run: %q", u)
	}
	resp := n.control(localctl.Request{Cmd: localctl.CmdPanel})
	if resp.OK || resp.Error == "" {
		t.Fatalf("panel before Run: %+v", resp)
	}
}

func TestShareAddWithoutAMachineIsRefused(t *testing.T) {
	dir, err := os.MkdirTemp("", "ovp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	n := panelTestNode(t, config.HomePaths(dir))
	n.shares = newShareSet(context.Background(), dir, connect.Options{})
	if _, err := n.shareAdd(3000); err == nil || !strings.Contains(err.Error(), "not paired") {
		t.Fatalf("share without pairing: %v", err)
	}
}
