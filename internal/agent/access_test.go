package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/OpenVibers/OpenVibe.Link/internal/access"
	"github.com/OpenVibers/OpenVibe.Link/internal/config"
	"github.com/OpenVibers/OpenVibe.Link/internal/credentials"
	"github.com/OpenVibers/OpenVibe.Link/internal/protocol"
)

func TestAgentTaskQueueRequiresOwnerSwitch(t *testing.T) {
	inbox, err := access.OpenInbox(filepath.Join(t.TempDir(), "inbox.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	n := &Agent{opt: Options{Config: cfg, Creds: &credentials.Credentials{Principal: "lnk_target", Network: "https://openvibe.network"}}, accessInbox: inbox, localAgents: map[string]localAgent{"codex": {ID: "agt_target", Owner: "usr_owner"}}}
	task := access.Task{ID: "tsk_12345678", To: "agt_target", From: "agt_sender", OnBehalfOf: "usr_owner", Thread: "thr_12345678"}
	task.Message.Parts = append(task.Message.Parts, struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{Type: "text", Text: "hello"})
	raw, _ := json.Marshal(task)
	frame := protocol.AgentTaskFrame{Task: raw}
	if err := n.receiveAgentTask(frame); err == nil {
		t.Fatal("off switch accepted task")
	}
	cfg.AgentsTalk = true
	if err := n.receiveAgentTask(frame); err != nil {
		t.Fatal(err)
	}
	if len(inbox.List("agt_target")) != 1 {
		t.Fatal("task did not enter inbox")
	}
	frame.Task = json.RawMessage(`{"id":"tsk_12345678","to":"agt_elsewhere","from":"agt_sender","on_behalf_of":"usr_owner","thread":"thr_12345678","message":{"parts":[{"type":"text","text":"hello"}]}}`)
	if err := n.receiveAgentTask(frame); err == nil {
		t.Fatal("task for an unregistered agent accepted")
	}
}

func TestMachineAccessAnswersKeepGrantTokenPrivate(t *testing.T) {
	raw := json.RawMessage(`{"grant":{"id":"gra_12345678","grant_token":"fake-private-value"},"requests":[{"credential":"fake-private-value","status":"pending"}]}`)
	public := string(publicData(raw))
	if strings.Contains(public, "fake-private-value") || !strings.Contains(public, "gra_12345678") {
		t.Fatalf("unsafe access answer: %s", public)
	}
}

func TestMachineRelayIDIsSeparateFromBotCommandIDs(t *testing.T) {
	if !validOperationID(access.NewRelayID()) || !validRelayID("rly_01J8Z4M2Q0R7T9YV3K6N8P1W2Z") ||
		validOperationID("cmd_12345678") || validRelayID("mrl_12345678901234567890123456789012") ||
		validRelayID("rly_cmd.12345678") || validOperationID("mrl_zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz") {
		t.Fatal("relay id namespace accepted a command or malformed id")
	}
}

func TestLocalAgentRegistrationIDsSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	items := map[string]localAgent{"codex": {ID: "agt_12345678", Owner: "usr_12345678"}}
	if err := saveLocalAgents(dir, items); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "agent-directory.json"))
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		t.Fatalf("directory file permissions: %v %v", info, err)
	}
	if got := loadLocalAgents(dir)["codex"]; got != items["codex"] {
		t.Fatalf("registration id changed: %+v", got)
	}
}

func TestIncomingAccessRequestReadsBothContractAndFirstNetworkTimes(t *testing.T) {
	first := incomingAccessRequest{FromMachineName: "laptop", Scopes: []string{"screen", "desk"}, DurationSeconds: 3600}
	if err := json.Unmarshal([]byte(`{"asked_at":"2026-11-11T20:00:00Z","pending_until":"2026-11-12T20:00:00Z"}`), &first); err != nil {
		t.Fatal(err)
	}
	if first.askedAt() != "2026-11-11T20:00:00Z" || first.expiresAt() != "2026-11-12T20:00:00Z" {
		t.Fatalf("first Network fields: %+v", first)
	}
	contract := incomingAccessRequest{FromMachineName: "laptop", Scopes: []string{"screen", "desk"}, DurationSeconds: 3600}
	if err := json.Unmarshal([]byte(`{"requested_at":"2026-11-11T20:00:00Z","expires_at":"2026-11-12T20:00:00Z"}`), &contract); err != nil {
		t.Fatal(err)
	}
	if contract.askedAt() != "2026-11-11T20:00:00Z" || contract.expiresAt() != "2026-11-12T20:00:00Z" {
		t.Fatalf("contract fields: %+v", contract)
	}
	title, body := accessRequestWords(contract)
	if title != "laptop asks for access" || body != "Watch the screen and use a private desk, for 1 hour." {
		t.Fatalf("tray words: %q / %q", title, body)
	}
}

func TestEveryRelayScopeMapsToItsLocalFeature(t *testing.T) {
	valid := map[string][]string{
		"agent":    {"task"},
		"send":     {"offer"},
		"screen":   {protocol.KindScreen, protocol.KindScreenStream},
		"desk":     {protocol.KindDesk, protocol.KindDeskStream},
		"computer": {protocol.KindExec, protocol.KindFS, protocol.KindInput, protocol.KindBrowser, protocol.KindProbe},
		"worker":   {"run", "status", "cancel"},
	}
	for scope, actions := range valid {
		for _, action := range actions {
			if !validRelayAction(scope, action) {
				t.Errorf("%s/%s refused", scope, action)
			}
		}
	}
	for _, bad := range [][2]string{{"agent", "run"}, {"send", "task"}, {"screen", "exec"}, {"worker", "browse"}, {"admin", "run"}} {
		if validRelayAction(bad[0], bad[1]) {
			t.Errorf("%s/%s accepted", bad[0], bad[1])
		}
	}
	if relayCapability("screen", protocol.KindScreen) != protocol.KindScreen ||
		relayCapability("desk", protocol.KindDesk) != protocol.KindDesk ||
		relayCapability("computer", protocol.KindExec) != protocol.KindExec ||
		relayCapability("worker", "run") != "" {
		t.Fatal("capability mapping")
	}
}
