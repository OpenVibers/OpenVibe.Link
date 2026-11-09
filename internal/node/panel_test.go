//go:build !windows

package node

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/OpenVibers/OpenVibe.Link/internal/config"
	"github.com/OpenVibers/OpenVibe.Link/internal/connect"
	"github.com/OpenVibers/OpenVibe.Link/internal/localctl"
	"github.com/OpenVibers/OpenVibe.Link/internal/panel"
	"github.com/OpenVibers/OpenVibe.Link/internal/protocol"
)

func panelTestNode(t *testing.T, paths config.Paths) *Node {
	t.Helper()
	n, err := New(Options{Config: &config.Config{}, Paths: paths, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		CodesAgents: func() []panel.CodesAgent { return nil }})
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
		t.Fatalf("openvibe-link stop did not latch: %+v", st)
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
	// Desktop and Terminal turn off and back on from Link (the 11-05 fixes are in); the window asks first.
	for _, id := range []string{"terminal", "desk"} {
		if err := n2.setCapability(id, false); err != nil {
			t.Fatal(err)
		}
		if err := n2.setCapability(id, true); err != nil {
			t.Fatalf("%s on: %v", id, err)
		}
	}
	if n2.capabilityOff(protocol.KindExec) || n2.capabilityOff(protocol.KindDesk) {
		t.Fatal("terminal or desk still refused after turning them on")
	}
	offer := Status{Plugins: []PluginStatus{{Capabilities: map[string]any{"terminal": true, "desk": true, "files": true}}}}
	for _, c := range n2.panelCapabilities(offer) {
		if (c.ID == "terminal" || c.ID == "desk") != (c.Confirm != "") {
			t.Fatalf("%s: confirm %q", c.ID, c.Confirm)
		}
	}
	if err := n2.setCapability("nope", true); err == nil {
		t.Fatal("an unknown capability was accepted")
	}
}

func TestMachineIDIsThePairedDevice(t *testing.T) {
	if got := machineID(Status{DeviceID: "dev_1", Principal: "lnk_1"}); got != "dev_1" {
		t.Fatalf("device id: %q", got)
	}
	if got := machineID(Status{Principal: "lnk_1"}); got != "lnk_1" {
		t.Fatalf("principal: %q", got)
	}
}

func TestPanelStateComesFromTheNode(t *testing.T) {
	dir, err := os.MkdirTemp("", "ovp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	n := panelTestNode(t, config.HomePaths(dir))
	n.codesAgents.fn = func() []panel.CodesAgent {
		return []panel.CodesAgent{{ID: "codex", Name: "Codex", State: "signin", Hint: "codex login"}}
	}
	if err := n.panelPause(true); err != nil {
		t.Fatal(err)
	}
	st := n.panelState()
	if st.Machine == nil || st.Machine.Name == "" || !st.Machine.Paused {
		t.Fatalf("machine: %+v", st.Machine)
	}
	if st.Codes == nil || len(st.Codes.Agents) != 1 || st.Codes.Agents[0].Hint != "codex login" {
		t.Fatalf("codes: %+v", st.Codes)
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

func TestFirstRunIsShownThenCleared(t *testing.T) {
	dir, err := os.MkdirTemp("", "ovp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	paths := config.HomePaths(dir)
	n := panelTestNode(t, paths)
	if !n.panelState().FirstRun {
		t.Fatal("a new Node does not ask what the computer is for")
	}
	if err := n.applyFirstRun([]string{"vibe"}, false); err != nil {
		t.Fatal(err)
	}
	if n.panelState().FirstRun {
		t.Fatal("the question is still shown after the answer")
	}
	// The answer is kept, so a restart does not ask again.
	if n2 := panelTestNode(t, paths); n2.panelState().FirstRun {
		t.Fatal("the question is shown again after a restart")
	}
}

func TestFirstRunTurnsOnOnlyWhatEachUseSays(t *testing.T) {
	dir, err := os.MkdirTemp("", "ovp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	paths := config.HomePaths(dir)
	n := panelTestNode(t, paths)
	// The uses must turn browser and models on, and must never turn Terminal or Desktop on.
	for _, id := range []string{"browser", "ai", "terminal", "desk"} {
		if err := n.setCapability(id, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := n.applyFirstRun([]string{"codes", "browser", "models", "earn", "vibe"}, false); err != nil {
		t.Fatal(err)
	}
	if n.capabilityOff(protocol.KindBrowser) {
		t.Fatal("browser was not turned on")
	}
	if n.capabilityOff(protocol.KindAI) {
		t.Fatal("models were not turned on")
	}
	// Codes and earn record the wish only; an answer never turns Terminal or Desktop on (only their own switch).
	if !n.capabilityOff(protocol.KindExec) {
		t.Fatal("the answer turned Terminal on")
	}
	if !n.capabilityOff(protocol.KindDesk) {
		t.Fatal("the answer turned Desktop on")
	}
	n.mu.Lock()
	uses := append([]string(nil), n.panelFirst.Uses...)
	off := map[string]bool{}
	for id, v := range n.panelOff {
		off[id] = v
	}
	n.mu.Unlock()
	if !reflect.DeepEqual(uses, []string{"codes", "browser", "models", "earn", "vibe"}) {
		t.Fatalf("recorded uses %v", uses)
	}
	for _, id := range []string{"files", "screen", "input"} {
		if off[id] {
			t.Fatalf("the answer turned %s off", id)
		}
	}
	// Everything above survives a restart.
	n2 := panelTestNode(t, paths)
	if n2.panelState().FirstRun {
		t.Fatal("the question came back after a restart")
	}
	if !n2.capabilityOff(protocol.KindExec) || !n2.capabilityOff(protocol.KindDesk) {
		t.Fatal("Terminal or Desktop was turned on after a restart")
	}
	if n2.capabilityOff(protocol.KindBrowser) || n2.capabilityOff(protocol.KindAI) {
		t.Fatal("the browser or models setting was lost after a restart")
	}
}

func TestFirstRunSkipOnlyStopsAsking(t *testing.T) {
	dir, err := os.MkdirTemp("", "ovp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	n := panelTestNode(t, config.HomePaths(dir))
	if err := n.setCapability("browser", false); err != nil {
		t.Fatal(err)
	}
	// A skip wins over any uses in the same answer.
	if err := n.applyFirstRun([]string{"browser"}, true); err != nil {
		t.Fatal(err)
	}
	if n.panelState().FirstRun {
		t.Fatal("a skip did not stop the question")
	}
	if !n.capabilityOff(protocol.KindBrowser) {
		t.Fatal("a skip turned the browser on")
	}
	n.mu.Lock()
	uses := n.panelFirst.Uses
	n.mu.Unlock()
	if len(uses) != 0 {
		t.Fatalf("a skip recorded uses: %v", uses)
	}
}

func TestFirstRunIsNotAskedOnAMachineSetUpBeforeIt(t *testing.T) {
	dir := t.TempDir()
	// A panel.json written before the question existed: the switches saved, no first_run. With a pairing, that is a
	// machine already in use, so it is not asked.
	if err := savePanel(dir, map[string]bool{"terminal": true}, nil); err != nil {
		t.Fatal(err)
	}
	off, first := loadPanel(dir, true)
	if first == nil || !first.Answered {
		t.Fatal("a paired machine with its switches already saved is asked")
	}
	if !off["terminal"] {
		t.Fatal("the saved switches were lost")
	}
	// Without a pairing it is a new machine: it is asked.
	if _, first := loadPanel(dir, false); first != nil {
		t.Fatal("an unpaired machine is not asked")
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
