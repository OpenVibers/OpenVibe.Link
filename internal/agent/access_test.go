package agent

import (
	"encoding/json"
	"path/filepath"
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
	frame.FromMachine = "lnk_other"
	if err := n.receiveAgentTask(frame); err == nil {
		t.Fatal("other machine without grant accepted")
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
	if !validRelayID(access.NewRelayID()) || validRelayID("cmd_12345678") || validRelayID("job_12345678") || validRelayID("mrl_zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz") {
		t.Fatal("relay id namespace accepted a command or malformed id")
	}
}
