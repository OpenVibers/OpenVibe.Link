package node

// Link's own window (internal/panel): the Node serves the embedded UI on 127.0.0.1 with a per-run token, and answers
// the small API it polls from what the Node already knows. Nothing here invents a value: a section the Node cannot
// fill truthfully is left out.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/OpenVibers/OpenVibe.Node/internal/firstrun"
	"github.com/OpenVibers/OpenVibe.Node/internal/panel"
	"github.com/OpenVibers/OpenVibe.Node/internal/plugins"
	"github.com/OpenVibers/OpenVibe.Node/internal/protocol"
)

// panelCapabilities are the switches the window shows, in its order. Every one is a switch the daemon already has:
// the computer plugin's own capabilities and the AI plugin's offer. Turning one off adds a local restriction; turning
// it on removes it. A switch never grants more than the plugin already offers.
var panelCapabilityList = []struct {
	ID, Name, Who string
	computer      bool
}{
	{"files", "Your files on this computer", "Your agents, in the folders you allowed", true},
	{"screen", "Your screen", "You and your agents, at the level you set", true},
	{"input", "Your mouse and keyboard", "Your agents, after you approve", true},
	{"browser", "A browser for your agents", "Your agents, after you approve", true},
	{"terminal", "A terminal on this computer", "Your agents, at the level you set", true},
	{"desk", "A desk for your agents", "Your agents, off your own screen", true},
	{"ai", "Run AI models here", "Only you, from your other devices", false},
}

// panelAskFirst are the switches the window confirms in plain words before turning on. They were gated until the
// 11-05 review's fixes (Node #102-#104, merged 11-06); they are never turned on by the first-run answer, only by their
// own switch.
var panelAskFirst = map[string]string{
	"terminal": "Turn on the terminal? Your agents can ask to run commands on this computer. At level ask you approve each one; at trusted they run with your account's full reach.",
	"desk":     "Turn on the desk? Your agents get their own desktop off your screen, with its own browser, and work there at the level you set.",
}

// capabilityForKind maps a command kind to the switch that can refuse it.
var capabilityForKind = map[string]string{
	protocol.KindExec:         "terminal",
	protocol.KindFS:           "files",
	protocol.KindScreen:       "screen",
	protocol.KindScreenStream: "screen",
	protocol.KindInput:        "input",
	protocol.KindBrowser:      "browser",
	protocol.KindDesk:         "desk",
	protocol.KindDeskStream:   "desk",
	protocol.KindAI:           "ai",
}

// panelFile is where the switches the person turned off and the first-run answer are kept, so a restart keeps them.
const panelFile = "panel.json"

// panelFirstRun is the answer to "What's this computer for?": answered once the person answered or skipped it, and the
// uses they picked. It is kept beside the capability switches so a restart does not ask again.
type panelFirstRun struct {
	Answered bool     `json:"answered"`
	Uses     []string `json:"uses,omitempty"`
}

type panelSaved struct {
	Off []string `json:"off"`
	// FirstRun is absent in a file written before the question existed.
	FirstRun *panelFirstRun `json:"first_run,omitempty"`
}

// loadPanel reads the switches turned off and the first-run answer. A file written before the question existed, on a
// machine that is paired, is a Node that was set up before this change (paired, its capabilities already saved): it
// is not asked. A machine that never answered and has no such file is asked once.
func loadPanel(dir string, paired bool) (map[string]bool, *panelFirstRun) {
	off := map[string]bool{}
	b, err := os.ReadFile(filepath.Join(dir, panelFile))
	if err != nil {
		return off, nil
	}
	var s panelSaved
	if json.Unmarshal(b, &s) != nil {
		return off, nil
	}
	for _, id := range s.Off {
		off[id] = true
	}
	if s.FirstRun != nil {
		return off, s.FirstRun
	}
	if paired {
		return off, &panelFirstRun{Answered: true}
	}
	return off, nil
}

func savePanel(dir string, off map[string]bool, first *panelFirstRun) error {
	s := panelSaved{Off: []string{}, FirstRun: first}
	for id := range off {
		s.Off = append(s.Off, id)
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, panelFile+".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, panelFile))
}

// startPanel serves Link's window until ctx ends. A failure leaves the Node running without it.
func (n *Node) startPanel(ctx context.Context) {
	token, err := panel.NewToken()
	if err != nil {
		n.log.Warn("Link's window off: no token", "err", err)
		return
	}
	srv, err := panel.Listen(panel.Options{
		Token:         token,
		State:         n.panelState,
		Pause:         n.panelPause,
		Stop:          n.panelStop,
		SetCapability: n.setCapability,
		Share:         n.shareAdd,
		Unshare:       n.shareRemove,
		FirstRun:      n.applyFirstRun,
		CodesStop:     n.panelStopCodesTask,
		Log:           n.log,
	})
	if err != nil {
		n.log.Warn("Link's window off: cannot listen on 127.0.0.1", "err", err)
		return
	}
	n.mu.Lock()
	n.panel = srv
	n.mu.Unlock()
	n.log.Info("Link's window is open on 127.0.0.1", "port", srv.Addr())
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	go func() {
		if err := srv.Serve(); err != nil {
			n.log.Warn("Link's window stopped", "err", err)
		}
	}()
}

// PanelURL is where the tray opens Link's window; "" when the Node did not start it.
func (n *Node) PanelURL() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.panel == nil {
		return ""
	}
	return n.panel.URL()
}

// panelState is GET /panel/api/state, from what the Node already knows.
func (n *Node) panelState() panel.State {
	st := n.Status()
	host, _ := os.Hostname()
	out := panel.State{
		Machine: &panel.Machine{
			ID:       machineID(st),
			Name:     host,
			OS:       runtime.GOOS,
			Online:   st.Link.Connected,
			Paused:   st.Latch.Stopped(),
			NeedsYou: len(n.Approvals()),
		},
		Running:      n.panelRunning(st),
		Capabilities: n.panelCapabilities(st),
		Shares:       n.panelShares(st),
		Codes:        n.panelCodes(),
	}
	n.mu.Lock()
	out.Activity = append([]panel.Activity(nil), n.activity...)
	out.FirstRun = n.panelFirst == nil || !n.panelFirst.Answered
	n.mu.Unlock()
	return out
}

// machineID is the id Codes preselects this computer by: the paired Bot device, or the Network principal before that.
func machineID(st Status) string {
	if st.DeviceID != "" {
		return st.DeviceID
	}
	return st.Principal
}

// panelRunning lists what the tray already shows as running or waiting: approvals, jobs, connected shares and desks.
func (n *Node) panelRunning(st Status) []panel.Running {
	out := []panel.Running{}
	for _, a := range n.Approvals() {
		title := strings.TrimSpace(a.Summary)
		if title == "" {
			title = "Waiting for your approval"
		}
		out = append(out, panel.Running{ID: "approval:" + a.ID, Kind: "agent", Title: title,
			Detail: "Waiting for your approval", StartedAt: time.UnixMilli(a.At).UTC()})
	}
	for _, j := range st.Jobs {
		r := panel.Running{ID: "job:" + j.ID, Kind: "agent", Title: j.Function, Detail: j.State}
		if j.StartedMS > 0 {
			r.StartedAt = time.UnixMilli(j.StartedMS).UTC()
		}
		out = append(out, r)
	}
	for _, sh := range st.Shares {
		if sh.Connected {
			out = append(out, panel.Running{ID: "share:" + strconv.Itoa(sh.Port), Kind: "share",
				Title: fmt.Sprintf("Port %d shared", sh.Port), Detail: sh.URL})
		}
	}
	n.mu.Lock()
	partyID, hosting := n.partyID, n.partyCancel != nil
	n.mu.Unlock()
	if hosting {
		out = append(out, panel.Running{ID: "party:" + partyID, Kind: "desk",
			Title: "Hosting a party desktop", Detail: partyID})
	}
	if desks := sensorInt(st.Telemetry.Sensors["computer_desks"]); desks > 0 {
		title := fmt.Sprintf("%d desks for your agents", desks)
		if desks == 1 {
			title = "A desk for your agents"
		}
		out = append(out, panel.Running{ID: "desks", Kind: "desk", Title: title, Detail: "Desktop"})
	}
	return out
}

// panelShares lists the connected shares with the address Connect gave them.
func (n *Node) panelShares(st Status) []panel.Share {
	out := []panel.Share{}
	for _, sh := range st.Shares {
		out = append(out, panel.Share{Port: sh.Port, URL: sh.URL})
	}
	return out
}

// panelCapabilities maps the daemon's permission switches to the window's list. A capability is on when a plugin
// offers it, the computer is not at level observe, and the person has not turned it off here.
func (n *Node) panelCapabilities(st Status) []panel.Capability {
	offered := map[string]bool{}
	for _, p := range st.Plugins {
		for id := range p.Capabilities {
			offered[id] = true
		}
	}
	level, _ := st.Telemetry.Sensors["computer_level"].(string)
	n.mu.Lock()
	off := make(map[string]bool, len(n.panelOff))
	for id := range n.panelOff {
		off[id] = true
	}
	n.mu.Unlock()
	out := []panel.Capability{}
	for _, c := range panelCapabilityList {
		if !offered[c.ID] {
			continue
		}
		on := !off[c.ID]
		if c.computer && (level == "" || level == "observe") {
			on = false
		}
		out = append(out, panel.Capability{ID: c.ID, Name: c.Name, Who: c.Who, On: on, Confirm: panelAskFirst[c.ID]})
	}
	return out
}

// setCapability turns one switch off (a local restriction) or back on. It persists the choice.
func (n *Node) setCapability(id string, on bool) error {
	known, computer := false, false
	for _, c := range panelCapabilityList {
		if c.ID == id {
			known, computer = true, c.computer
		}
	}
	if !known {
		return fmt.Errorf("no capability %q on this computer", id)
	}
	if on && computer && n.computerLevel() == "observe" {
		return errors.New("this computer is at level observe: set its level on the machine first")
	}
	n.mu.Lock()
	next := make(map[string]bool, len(n.panelOff)+1)
	for k, v := range n.panelOff {
		next[k] = v
	}
	if on {
		delete(next, id)
	} else {
		next[id] = true
	}
	err := savePanel(n.opt.Paths.StateDir, next, n.panelFirst)
	if err == nil {
		n.panelOff = next
	}
	n.mu.Unlock()
	if err != nil {
		return err
	}
	if on {
		n.addActivity("Turned " + id + " back on")
	} else {
		n.addActivity("Turned " + id + " off")
	}
	return nil
}

// applyFirstRun records the answer to "What's this computer for?" and turns on what each use says: a browser for the
// agents, or the AI plugin. Codes has no module switch on the Node yet and earn only records the wish (Exchange
// confirms it); Terminal and Desktop are never turned on here, only by their own switch in the window. A skip only
// stops the question. One node.first_run event carries the answer.
func (n *Node) applyFirstRun(uses []string, skip bool) error {
	if skip {
		uses = nil
	} else {
		uses = firstrun.CleanUses(uses)
	}
	for _, use := range uses {
		switch use {
		case "browser":
			if err := n.setCapability("browser", true); err != nil {
				n.log.Warn("first run: the browser stays off", "err", err)
			}
		case "models":
			if err := n.setCapability("ai", true); err != nil {
				n.log.Warn("first run: models stay off", "err", err)
			}
		case "codes":
			// Recorded only: the Node has no Codes module to switch on, and Terminal stays as it is (its own switch).
		case "earn":
			// Recorded only: Exchange confirms it, and nothing on this machine turns on here.
		}
	}

	n.mu.Lock()
	first := &panelFirstRun{Answered: true, Uses: uses}
	off := make(map[string]bool, len(n.panelOff))
	for id, v := range n.panelOff {
		off[id] = v
	}
	err := savePanel(n.opt.Paths.StateDir, off, first)
	if err == nil {
		n.panelFirst = first
	}
	n.mu.Unlock()
	if err != nil {
		return err
	}

	switch {
	case skip:
		n.addActivity("Skipped the first-run question")
	case len(uses) > 0:
		n.addActivity("Told Link what this computer is for: " + strings.Join(uses, ", "))
	default:
		n.addActivity("Answered the first-run question: nothing to turn on")
	}

	if !skip && n.reporting() {
		e := firstrun.UsesEvent(uses, n.opt.Version, runtime.GOOS, runtime.GOARCH)
		n.mu.Lock()
		n.events = append(n.events, e)
		n.mu.Unlock()
		n.flushTelemetry(true)
	}
	return nil
}

// capabilityOff reports whether a command kind is refused by a switch turned off here.
func (n *Node) capabilityOff(kind string) bool {
	id := capabilityForKind[kind]
	if id == "" {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.panelOff[id]
}

// computerLevel is the computer plugin's level, "" without it.
func (n *Node) computerLevel() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	tv := n.telemetry["computer"]
	if tv == nil {
		return ""
	}
	sensors, _ := tv["sensors"].(map[string]any)
	lv, _ := sensors["computer_level"].(string)
	return lv
}

// sensorInt reads a sensor that travelled as JSON (float64) or was set in process.
func sensorInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

// addActivity records one line for "What happened here"; the newest 30 are kept.
func (n *Node) addActivity(text string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.appendActivityLocked(text)
}

func (n *Node) appendActivityLocked(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	n.activity = append([]panel.Activity{{At: time.Now().UTC(), Text: text}}, n.activity...)
	if len(n.activity) > 30 {
		n.activity = n.activity[:30]
	}
}

// panelPause is the local kill switch, the same action as the tray's Stop everything and Resume.
func (n *Node) panelPause(paused bool) error {
	if paused {
		if err := n.stopEverything("Link panel"); err != nil {
			return err
		}
		n.addActivity("Everything stopped (Pause everything)")
		return nil
	}
	if err := n.resumeEverything(); err != nil {
		return err
	}
	n.addActivity("Resumed")
	return nil
}

// stopEverything is the local kill switch: estop every plugin at once, then persist the latch.
func (n *Node) stopEverything(reason string) error {
	n.mgr.EstopAll(plugins.EstopLocal, reason)
	_, err := n.latch.SetLocalWith(reason)
	return err
}

// resumeEverything releases the local kill switch (and a server e-stop).
func (n *Node) resumeEverything() error {
	_, err := n.latch.Resume()
	return err
}

// panelStop stops one running thing by the id panelRunning gave it.
func (n *Node) panelStop(id string) error {
	switch {
	case strings.HasPrefix(id, "approval:"):
		return n.answerApproval(strings.TrimPrefix(id, "approval:"), false)
	case strings.HasPrefix(id, "job:"):
		jid := strings.TrimPrefix(id, "job:")
		if n.worker != nil && n.worker.Cancel(jid) {
			n.addActivity("Stopped a job")
			return nil
		}
		n.jobCancel(protocol.JobCancel{ID: jid})
		return nil
	case strings.HasPrefix(id, "share:"):
		port, err := strconv.Atoi(strings.TrimPrefix(id, "share:"))
		if err != nil {
			break
		}
		return n.shareRemove(port)
	case strings.HasPrefix(id, "party:"):
		return n.stopParty(strings.TrimPrefix(id, "party:"))
	case id == "desks":
		return errors.New("a desk is stopped by the agent that started it, or by Pause everything")
	}
	return fmt.Errorf("nothing is running with the id %q", id)
}

// answerApproval lets one waiting request run or refuses it; the tray, the CLI and the window all end here.
func (n *Node) answerApproval(nonce string, approved bool) error {
	n.mu.Lock()
	var a Approval
	ok := false
	for _, candidate := range n.approvals {
		if candidate.ID == nonce {
			a, ok = candidate, true
			break
		}
	}
	if ok && approved && n.drivingLocked() {
		n.mu.Unlock()
		return errors.New("the agent is using the mouse or keyboard (or just did): answer again in a moment, so that its click cannot approve this")
	}
	if ok {
		delete(n.approvals, a.CommandID)
	}
	n.mu.Unlock()
	if !ok {
		return fmt.Errorf("nothing with the id %q waits for approval", strings.TrimSpace(nonce))
	}
	p := n.mgr.Get(a.Plugin)
	if p == nil {
		return errors.New("the plugin that asked is gone")
	}
	if err := p.Answer(a.binding, approved); err != nil {
		return err
	}
	verb := "denied"
	if approved {
		verb = "approved"
	}
	n.addActivity(fmt.Sprintf("You %s: %s", verb, strings.TrimSpace(a.Summary)))
	return nil
}
