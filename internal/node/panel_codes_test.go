package node

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenVibers/OpenVibe.Node/internal/panel"
	"github.com/OpenVibers/OpenVibe.Node/internal/protocol"
)

// The three states the window shows, from a fake machine: ready with a version, sign in with the exact command, and
// not installed.
func TestDetectCodesAgentsReportsReadySignInAndMissing(t *testing.T) {
	home := t.TempDir()
	login := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(login), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(login, []byte(`{"claudeAiOauth":{"accessToken":"not-a-real-token","refreshToken":"also-not-real"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	probe := codesProbe{
		Home: home,
		LookPath: func(bin string) (string, error) {
			switch bin {
			case "claude", "codex", "dsh":
				return "/usr/bin/" + bin, nil
			}
			return "", errors.New("not on PATH")
		},
		ReadFile: os.ReadFile,
		Version:  func(string) string { return "1.2.3" },
	}
	got := map[string]panel.CodesAgent{}
	for _, a := range detectCodesAgents(probe) {
		got[a.ID] = a
	}
	if a := got["claude-code"]; a.State != "ready" || a.Version != "1.2.3" || a.Hint != "" {
		t.Fatalf("claude-code: %+v", a)
	}
	if a := got["codex"]; a.State != "signin" || a.Hint != "codex login" {
		t.Fatalf("codex: %+v", a)
	}
	if a := got["opencode"]; a.State != "missing" {
		t.Fatalf("opencode: %+v", a)
	}
	if a := got["gemini"]; a.State != "missing" {
		t.Fatalf("gemini: %+v", a)
	}
	// dsh has no plan login of its own: installed is the fact.
	if a := got["dsh"]; a.State != "ready" || a.Hint != "" {
		t.Fatalf("dsh: %+v", a)
	}
}

// A login file that is there but has no usable session is sign-in, never ready.
func TestCodesLoginFileWithoutASessionIsSignIn(t *testing.T) {
	if codesSignedIn([]byte(`{}`)) || codesSignedIn([]byte(`not json`)) || codesSignedIn([]byte(`{"access_token":""}`)) {
		t.Fatal("an empty login was taken as signed in")
	}
	if !codesSignedIn([]byte(`{"refresh_token":"x"}`)) || !codesSignedIn([]byte(`{"tokens":{"a":"b"}}`)) ||
		!codesSignedIn([]byte(`{"claudeAiOauth":{"accessToken":"x"}}`)) {
		t.Fatal("a usable login was not recognised")
	}
}

func TestCodesFolderUsesTildeAndTheTitleIsTheFirstLine(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	s := loadCodesTasks(dir, home, nil)
	args, err := json.Marshal(map[string]any{
		"task_id": "tsk_01M6Q1Z8V4T7R2N5K3B9C6D0EF",
		"prompt":  strings.Repeat("A very long first line ", 8) + "\nsecond line",
		"folder":  filepath.Join(home, "code", "site"),
		"agent":   "claude-code",
	})
	if err != nil {
		t.Fatal(err)
	}
	s.accepted(protocol.Job{ID: "job_1", Artifact: &protocol.Artifact{Name: codesArtifact}, Args: args})
	tasks := s.list()
	if len(tasks) != 1 {
		t.Fatalf("tasks: %+v", tasks)
	}
	got := tasks[0]
	if got.Folder != filepath.Join("~", "code", "site") {
		t.Fatalf("folder %q", got.Folder)
	}
	if len([]rune(got.Title)) != codesTitleMax || strings.Contains(got.Title, "second line") {
		t.Fatalf("title %q", got.Title)
	}
	if got.Agent != "claude-code" || got.State != "waiting" || got.StartedAt.IsZero() {
		t.Fatalf("task %+v", got)
	}
	if got.URL != "https://openvibe.codes/tasks/tsk_01M6Q1Z8V4T7R2N5K3B9C6D0EF" {
		t.Fatalf("url %q", got.URL)
	}
	if shortHome(home, home) != "~" || shortHome("/elsewhere/x", home) != "/elsewhere/x" {
		t.Fatal("shortHome changed a folder outside the home")
	}
}

func TestCodesTaskGoesFromWaitingToRunningToDoneAndStays(t *testing.T) {
	dir := t.TempDir()
	s := loadCodesTasks(dir, "/home/alex", nil)
	args, _ := json.Marshal(map[string]any{"task_id": "tsk_01M6Q1Z8V4T7R2N5K3B9C6D0EF", "prompt": "Fix the test"})
	s.accepted(protocol.Job{ID: "job_1", Artifact: &protocol.Artifact{Name: codesArtifact}, Args: args})
	if job, ok := s.runningJob("tsk_01M6Q1Z8V4T7R2N5K3B9C6D0EF"); !ok || job != "job_1" {
		t.Fatalf("waiting task is not stoppable: %q %v", job, ok)
	}
	started := time.Date(2026, 11, 7, 10, 2, 0, 0, time.UTC)
	s.started("job_1", started.UnixMilli())
	if got := s.list()[0]; got.State != "running" || !got.StartedAt.Equal(started) {
		t.Fatalf("running: %+v", got)
	}
	zero := 0
	s.ended(protocol.JobExit{ID: "job_1", Reason: protocol.ExitExited, Code: &zero})
	if got := s.list()[0]; got.State != "done" {
		t.Fatalf("done: %+v", got)
	}
	if _, ok := s.runningJob("tsk_01M6Q1Z8V4T7R2N5K3B9C6D0EF"); ok {
		t.Fatal("a done task is still stoppable")
	}
	// The record survives a restart, and a task left running by the old process is stopped, not left working.
	again := loadCodesTasks(dir, "/home/alex", nil)
	if got := again.list(); len(got) != 1 || got[0].State != "done" {
		t.Fatalf("reloaded: %+v", got)
	}
	again.accepted(protocol.Job{ID: "job_2", Artifact: &protocol.Artifact{Name: codesArtifact}, Args: args})
	if got := again.list()[0]; got.State != "waiting" {
		t.Fatalf("a second job for the task did not restart it: %+v", got)
	}
	restarted := loadCodesTasks(dir, "/home/alex", nil)
	if got := restarted.list()[0]; got.State != "stopped" {
		t.Fatalf("left-running task: %+v", got)
	}
}

func TestCodesTaskEndStatesAndJobFilter(t *testing.T) {
	dir := t.TempDir()
	s := loadCodesTasks(dir, "/home/alex", nil)
	args, _ := json.Marshal(map[string]any{"prompt": "x"})
	s.accepted(protocol.Job{ID: "job_other", Artifact: &protocol.Artifact{Name: "thumbnail"}})
	s.accepted(protocol.Job{ID: "job_stop", Artifact: &protocol.Artifact{Name: codesArtifact}, Args: args})
	if len(s.list()) != 1 || s.list()[0].ID != "job_stop" {
		t.Fatalf("a job that is not a Codes task was recorded: %+v", s.list())
	}
	s.ended(protocol.JobExit{ID: "job_stop", Reason: protocol.ExitStopped})
	if got := s.list()[0].State; got != "stopped" {
		t.Fatalf("stopped: %q", got)
	}
	s.accepted(protocol.Job{ID: "job_fail", Artifact: &protocol.Artifact{Name: codesArtifact}, Args: args})
	one := 1
	s.ended(protocol.JobExit{ID: "job_fail", Reason: protocol.ExitExited, Code: &one})
	if got := s.list()[0].State; got != "failed" {
		t.Fatalf("failed: %q", got)
	}
}

func TestStopCodesTaskRefusesWhatIsNotRunningHere(t *testing.T) {
	if err := (&Node{}).panelStopCodesTask("tsk_01M6Q1Z8V4T7R2N5K3B9C6D0EF"); err == nil {
		t.Fatal("a task was stopped with no worker")
	}
	s := loadCodesTasks(t.TempDir(), "/home/alex", nil)
	args, _ := json.Marshal(map[string]any{"task_id": "tsk_01M6Q1Z8V4T7R2N5K3B9C6D0EF", "prompt": "x"})
	s.accepted(protocol.Job{ID: "job_1", Artifact: &protocol.Artifact{Name: codesArtifact}, Args: args})
	n := &Node{codes: s}
	if err := n.panelStopCodesTask("tsk_01M6Q1Z8V4T7R2N5K3B9C6D0EF"); err == nil {
		t.Fatal("a task was stopped with no worker")
	}
	if err := n.panelStopCodesTask("tsk_unknown"); err == nil {
		t.Fatal("an unknown task was stopped")
	}
}
