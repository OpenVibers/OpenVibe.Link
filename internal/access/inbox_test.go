package access

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleTask() Task {
	t := Task{ID: "tsk_12345678", To: "agt_target", From: "agt_sender", OnBehalfOf: "usr_owner", Thread: "thr_12345678"}
	t.Message.Parts = append(t.Message.Parts, struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{Type: "text", Text: "hello"})
	return t
}

func TestInboxPersistsAndExpires(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-inbox.json")
	i, err := OpenInbox(path)
	if err != nil {
		t.Fatal(err)
	}
	task := sampleTask()
	if err := i.Add(task); err != nil {
		t.Fatal(err)
	}
	if err := i.Add(task); err != nil {
		t.Fatal(err)
	}
	if len(i.List("agt_target")) != 1 || len(i.List("agt_other")) != 0 {
		t.Fatal("wrong inbox filter or duplicate")
	}
	i, err = OpenInbox(path)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := i.Reply(task.ID, "answer")
	if err != nil || entry.Status != "completed" {
		t.Fatalf("reply: %+v %v", entry, err)
	}
	if _, err := i.Reply(task.ID, "again"); err == nil {
		t.Fatal("duplicate reply accepted")
	}
	i.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	if len(i.List("")) != 0 {
		t.Fatal("stale thread remains")
	}
}

func TestInboxRefusesInvalidTask(t *testing.T) {
	i, err := OpenInbox(filepath.Join(t.TempDir(), "inbox.json"))
	if err != nil {
		t.Fatal(err)
	}
	task := sampleTask()
	task.Message.Parts[0].Text = strings.Repeat("x", 8001)
	if err := i.Add(task); err == nil {
		t.Fatal("oversized text accepted")
	}
	task = sampleTask()
	task.OnBehalfOf = ""
	if err := i.Add(task); err == nil {
		t.Fatal("missing person accepted")
	}
}
