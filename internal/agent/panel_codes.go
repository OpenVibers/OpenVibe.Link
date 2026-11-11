package agent

// The Codes half of Link's window: the coding-agent CLIs on this machine, and the Codes tasks its worker runs. Both
// are local facts. The agents are read the way OpenVibe.Codes reads them (on PATH, signed in by the CLI's own login
// file, the version the binary reports); the tasks are the jobs the Codes worker accepted here, kept under the Agent's
// state directory so a task that ended today is still shown after a restart. Nothing here reads a token or a key: a
// login file is only checked for a usable session, never reported.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/OpenVibers/OpenVibe.Link/internal/panel"
	"github.com/OpenVibers/OpenVibe.Link/internal/protocol"
)

const (
	// codesArtifact is the OpenVibe.Codes job runner's artifact name: the jobs that are coding tasks.
	codesArtifact = "openvibe-codes"
	// codesTasksName is the file that keeps the Codes tasks this worker ran, under the Agent's state directory.
	codesTasksName = "codes-tasks.json"
	// codesTaskMax bounds what that file holds; the window lists what happened today, newest first.
	codesTaskMax = 50
	// codesTitleMax is the design's title: the first line of the prompt, up to 80 characters.
	codesTitleMax = 80
	// codesRefresh is how often the agent chips are probed. A login does not change in seconds, and each probe asks
	// the installed binaries for their version.
	codesRefresh = time.Minute
	// codesURL is the only origin a task link may have.
	codesURL = "https://openvibe.codes/tasks/"
)

// codesAgentSpec is one coding-agent CLI on the machine, and how OpenVibe.Codes finds it: the binary on PATH, the
// login file its CLI writes, and the command that signs it in. A CLI with no login file (dsh reads its provider
// settings from the machine) is ready once its binary is there. Install is the command that installs the CLI, shown
// when it is missing (InstallWindows on Windows); empty when Link knows no installer for it.
type codesAgentSpec struct {
	ID             string
	Name           string
	Bin            string
	Login          string
	Hint           string
	Install        string
	InstallWindows string
}

// codesAgentSpecs are the CLIs the desktop's Codes module drives (domains plan §13.1).
var codesAgentSpecs = []codesAgentSpec{
	{ID: "claude-code", Name: "Claude Code", Bin: "claude", Login: ".claude/.credentials.json", Hint: "claude auth login"},
	{ID: "codex", Name: "Codex", Bin: "codex", Login: ".codex/auth.json", Hint: "codex login"},
	{ID: "opencode", Name: "OpenCode", Bin: "opencode", Login: ".local/share/opencode/auth.json", Hint: "opencode auth login"},
	// Gemini CLI signs in from inside the CLI with /auth; `gemini` is the command that starts it.
	{ID: "gemini", Name: "Gemini CLI", Bin: "gemini", Login: ".gemini/oauth_creds.json", Hint: "gemini"},
	// dsh keeps its model settings in its profile, not in a plan login: installed is as much as is known here.
	{ID: "dsh", Name: "DeepSeek Harness", Bin: "dsh"},
	// Muse Code keeps the person's own plan session in auth.json under providers.meta (Meta's installer and login;
	// Meta's quickstart, checked 2026-11-10). A Codes or Actor job on this machine runs it with contributor mode off.
	{ID: "muse", Name: "Muse Code", Bin: "muse", Login: ".config/muse/auth.json", Hint: "muse login",
		Install: "curl -fsSL https://dev.meta.ai/install.sh | sh", InstallWindows: "irm https://dev.meta.ai/install.ps1 | iex"},
}

// codesProbe is what detection needs from the machine, so a test can fake it.
type codesProbe struct {
	Home     string
	LookPath func(string) (string, error)
	ReadFile func(string) ([]byte, error)
	Version  func(string) string
	// OpenCodeAuth asks OpenCode itself whether it holds a credential. OpenCode moved its logins from auth.json into
	// its own store, so the file is not the truth on every version. known is false when the CLI cannot answer.
	OpenCodeAuth func(bin string) (signedIn, known bool)
}

var codesVersionNumber = regexp.MustCompile(`\d+\.\d+(?:[.\-+][0-9A-Za-z.\-]+)*`)

// detectCodesAgents reads the CLIs: missing when its binary is not on PATH, signin when it is there but its login
// file holds no usable session, ready otherwise. A version is the first number the binary's own --version reports
// (never read from a file of ours).
func detectCodesAgents(p codesProbe) []panel.CodesAgent {
	if p.Home == "" {
		p.Home, _ = os.UserHomeDir()
	}
	if p.LookPath == nil {
		p.LookPath = exec.LookPath
	}
	if p.ReadFile == nil {
		p.ReadFile = os.ReadFile
	}
	if p.Version == nil {
		p.Version = func(bin string) string { return codesVersion(bin) }
	}
	out := make([]panel.CodesAgent, 0, len(codesAgentSpecs))
	for _, spec := range codesAgentSpecs {
		a := panel.CodesAgent{ID: spec.ID, Name: spec.Name, State: "missing"}
		bin, err := p.LookPath(spec.Bin)
		if err != nil || bin == "" {
			// A per-user install in ~/.local/bin is not on a service's PATH; that is still the CLI on this machine.
			candidate := filepath.Join(p.Home, ".local", "bin", spec.Bin)
			if fi, statErr := os.Stat(candidate); statErr == nil && !fi.IsDir() {
				bin = candidate
			}
		}
		if bin == "" {
			a.Install = codesInstall(spec)
			out = append(out, a)
			continue
		}
		a.State = "ready"
		if spec.Login != "" {
			raw, readErr := p.ReadFile(filepath.Join(p.Home, filepath.FromSlash(spec.Login)))
			signed := readErr == nil && codesSignedIn(raw)
			if spec.ID == "opencode" && p.OpenCodeAuth != nil {
				if byCLI, known := p.OpenCodeAuth(bin); known {
					signed = byCLI
				}
			}
			if !signed {
				a.State, a.Hint = "signin", spec.Hint
			}
		}
		if p.Version != nil {
			a.Version = p.Version(bin)
		}
		out = append(out, a)
	}
	return out
}

// codesSignedIn says whether a CLI's login file holds a usable session. It reads the same keys OpenVibe.Codes' logins
// probe reads (and Claude Code's own nested claudeAiOauth object) and never reports the file's content.
func codesSignedIn(raw []byte) bool {
	var login map[string]any
	if json.Unmarshal(raw, &login) != nil || login == nil {
		return false
	}
	if usableSession(login) {
		return true
	}
	// Claude Code 2.x keeps the plan session nested: { "claudeAiOauth": { "accessToken": …, "refreshToken": … } }.
	if nested, ok := login["claudeAiOauth"].(map[string]any); ok && usableSession(nested) {
		return true
	}
	// Muse Code nests one session per provider: { "providers": { "meta": { "access_token": … } } }. The plan that
	// runs a job is Meta's, so a third-party provider's key does not count as signed in.
	if providers, ok := login["providers"].(map[string]any); ok {
		if meta, ok := providers["meta"].(map[string]any); ok && usableSession(meta) {
			return true
		}
	}
	return false
}

// codesInstall is the command that installs a missing CLI on this OS, or empty when Link knows none.
func codesInstall(spec codesAgentSpec) string {
	if runtime.GOOS == "windows" {
		return spec.InstallWindows
	}
	return spec.Install
}

func usableSession(login map[string]any) bool {
	for _, key := range []string{"access_token", "accessToken", "api_key", "apiKey", "refresh_token", "refreshToken", "token", "tokens", "account"} {
		switch v := login[key].(type) {
		case nil:
			continue
		case string:
			if strings.TrimSpace(v) != "" {
				return true
			}
		case map[string]any:
			if len(v) > 0 {
				return true
			}
		case []any:
			if len(v) > 0 {
				return true
			}
		default:
			return true
		}
	}
	return false
}

// codesVersion asks the binary about itself, with a deadline: a CLI that does not answer must not hold the window.
func codesVersion(bin string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil && len(out) == 0 {
		return ""
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if m := codesVersionNumber.FindString(line); m != "" {
		line = m
	}
	if len(line) > 40 {
		line = line[:40]
	}
	return line
}

// openCodeAuth asks OpenCode's own store, because a signed-in OpenCode keeps its credential in opencode.db, not in
// auth.json. Only whether a credential is stored is read; the answer itself never leaves this function.
func openCodeAuth(bin string) (signedIn, known bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "auth", "list").CombinedOutput()
	if err != nil && len(out) == 0 {
		return false, false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(strings.ToLower(line), "stored") {
			return true, true
		}
	}
	return false, true
}

// codesAgentsCache keeps the last probe, so a window polled every three seconds does not run every binary each time.
type codesAgentsCache struct {
	mu     sync.Mutex
	at     time.Time
	agents []panel.CodesAgent
	fn     func() []panel.CodesAgent // a test's fake, or nil for the machine
}

func (c *codesAgentsCache) latest(real func() []panel.CodesAgent) []panel.CodesAgent {
	c.mu.Lock()
	defer c.mu.Unlock()
	fn := c.fn
	if fn == nil {
		fn = real
	}
	if c.at.IsZero() || time.Since(c.at) >= codesRefresh {
		c.agents, c.at = fn(), time.Now()
	}
	return append([]panel.CodesAgent(nil), c.agents...)
}

// codesTaskRecord is one task the Codes worker ran here, as it is kept between runs.
type codesTaskRecord struct {
	ID        string    `json:"id"`
	Job       string    `json:"job,omitempty"`
	Title     string    `json:"title,omitempty"`
	Folder    string    `json:"folder,omitempty"`
	Agent     string    `json:"agent,omitempty"`
	State     string    `json:"state"`
	StartedAt time.Time `json:"started_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type codesTasksFile struct {
	Tasks []*codesTaskRecord `json:"tasks"`
}

// codesTaskState is the Codes worker's local record: the tasks it runs or ran today.
type codesTaskState struct {
	mu    sync.Mutex
	dir   string
	home  string
	log   *slog.Logger
	tasks []*codesTaskRecord
}

// loadCodesTasks reads today's tasks. A task left running or waiting by a previous process is stopped: the job that
// held it did not survive the restart.
func loadCodesTasks(dir, home string, log *slog.Logger) *codesTaskState {
	s := &codesTaskState{dir: dir, home: home, log: log}
	if dir == "" {
		return s
	}
	raw, err := os.ReadFile(filepath.Join(dir, codesTasksName))
	if err != nil {
		return s
	}
	var f codesTasksFile
	if json.Unmarshal(raw, &f) != nil {
		return s
	}
	now := time.Now().UTC()
	for _, t := range f.Tasks {
		if t == nil || !validCodesTaskID(t.ID) || !codesTaskStates[t.State] {
			continue
		}
		if t.State == "running" || t.State == "waiting" {
			t.State, t.UpdatedAt = "stopped", now
		}
		s.tasks = append(s.tasks, t)
	}
	s.pruneLocked(now)
	return s
}

func (s *codesTaskState) accepted(j protocol.Job) {
	if !codesJob(j) {
		return
	}
	var args struct {
		TaskID string `json:"task_id"`
		Prompt string `json:"prompt"`
		Folder string `json:"folder"`
		Agent  string `json:"agent"`
	}
	_ = json.Unmarshal(j.Args, &args)
	id := j.ID
	if codesContractTaskID.MatchString(args.TaskID) {
		id = args.TaskID
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.findLocked(id)
	if t == nil {
		t = &codesTaskRecord{ID: id, StartedAt: now}
		s.tasks = append(s.tasks, t)
	}
	t.Job = j.ID
	t.State = "waiting"
	t.UpdatedAt = now
	if title := codesTitle(args.Prompt); title != "" {
		t.Title = title
	}
	if folder := cleanCodesFolder(args.Folder); folder != "" {
		t.Folder = folder
	}
	if agent := cleanCodesAgent(args.Agent); agent != "" {
		t.Agent = agent
	}
	s.saveLocked(now)
}

func (s *codesTaskState) started(jobID string, ms int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.byJobLocked(jobID)
	if t == nil {
		return
	}
	t.State = "running"
	if ms > 0 {
		t.StartedAt = time.UnixMilli(ms).UTC()
	}
	t.UpdatedAt = time.Now().UTC()
	s.saveLocked(t.UpdatedAt)
}

func (s *codesTaskState) ended(e protocol.JobExit) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.byJobLocked(e.ID)
	if t == nil {
		return
	}
	t.State = codesEndState(e)
	t.UpdatedAt = time.Now().UTC()
	s.saveLocked(t.UpdatedAt)
}

// runningJob is the live worker job for a task, by the task's id or by its job id. Only running or waiting tasks are
// still this machine's to stop.
func (s *codesTaskState) runningJob(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tasks {
		if (t.ID == id || t.Job == id) && (t.State == "running" || t.State == "waiting") && t.Job != "" {
			return t.Job, true
		}
	}
	return "", false
}

// list is what the window shows: today's tasks, newest first, the folder with the home directory as ~.
func (s *codesTaskState) list() []panel.CodesTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	sort.SliceStable(s.tasks, func(i, j int) bool { return codesTaskTime(s.tasks[i]).After(codesTaskTime(s.tasks[j])) })
	out := make([]panel.CodesTask, 0, len(s.tasks))
	for _, t := range s.tasks {
		ct := panel.CodesTask{ID: t.ID, Title: t.Title, Folder: shortHome(t.Folder, s.home), Agent: t.Agent,
			State: t.State, StartedAt: t.StartedAt}
		if strings.HasPrefix(t.ID, "tsk_") {
			ct.URL = codesURL + url.PathEscape(t.ID)
		}
		out = append(out, ct)
	}
	return out
}

func (s *codesTaskState) findLocked(id string) *codesTaskRecord {
	for _, t := range s.tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func (s *codesTaskState) byJobLocked(jobID string) *codesTaskRecord {
	for _, t := range s.tasks {
		if t.Job == jobID {
			return t
		}
	}
	return nil
}

// pruneLocked keeps what runs now and what ran today (by when it started or last changed), newest first, at most
// codesTaskMax. It does not save: its callers do.
func (s *codesTaskState) pruneLocked(now time.Time) {
	kept := s.tasks[:0]
	for _, t := range s.tasks {
		if t.State == "running" || t.State == "waiting" || sameCodesDay(t.StartedAt, now) || sameCodesDay(t.UpdatedAt, now) {
			kept = append(kept, t)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return codesTaskTime(kept[i]).After(codesTaskTime(kept[j])) })
	if len(kept) > codesTaskMax {
		kept = kept[:codesTaskMax]
	}
	s.tasks = kept
}

func sameCodesDay(a, b time.Time) bool {
	a, b = a.Local(), b.Local()
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

func (s *codesTaskState) saveLocked(now time.Time) {
	s.pruneLocked(now)
	if s.dir == "" {
		return
	}
	f := filepath.Join(s.dir, codesTasksName)
	b, err := json.MarshalIndent(codesTasksFile{Tasks: s.tasks}, "", "  ")
	if err != nil {
		s.warn("Codes tasks not saved", err)
		return
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		s.warn("Codes tasks not saved", err)
		return
	}
	tmp := f + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		s.warn("Codes tasks not saved", err)
		return
	}
	if err := os.Rename(tmp, f); err != nil {
		s.warn("Codes tasks not saved", err)
	}
}

func (s *codesTaskState) warn(msg string, err error) {
	if s.log != nil {
		s.log.Warn(msg, "err", err)
	}
}

var codesTaskStates = map[string]bool{"running": true, "waiting": true, "done": true, "failed": true, "stopped": true}
var codesTaskID = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,120}$`)
var codesContractTaskID = regexp.MustCompile(`^tsk_[0-9A-HJKMNP-TV-Z]{26}$`)

func validCodesTaskID(id string) bool { return codesTaskID.MatchString(id) }

func codesJob(j protocol.Job) bool {
	return j.Artifact != nil && j.Artifact.Name == codesArtifact
}

func codesEndState(e protocol.JobExit) string {
	switch e.Reason {
	case protocol.ExitStopped, protocol.ExitCancelled:
		return "stopped"
	case protocol.ExitExited:
		if e.Code != nil && *e.Code == 0 {
			return "done"
		}
	}
	return "failed"
}

// codesTitle is the first line of the prompt, at most 80 characters. The prompt itself stays in the job.
func codesTitle(prompt string) string {
	line := strings.TrimSpace(strings.SplitN(strings.ReplaceAll(prompt, "\r\n", "\n"), "\n", 2)[0])
	line = strings.Join(strings.Fields(line), " ")
	if utf8.RuneCountInString(line) > codesTitleMax {
		r := []rune(line)
		line = string(r[:codesTitleMax])
	}
	return line
}

func cleanCodesFolder(folder string) string {
	folder = strings.TrimSpace(folder)
	if folder == "" || len(folder) > 4096 || strings.ContainsAny(folder, "\x00\r\n") {
		return ""
	}
	return folder
}

func cleanCodesAgent(agent string) string {
	agent = strings.TrimSpace(agent)
	if len(agent) > 40 {
		agent = agent[:40]
	}
	return agent
}

// shortHome shows a folder under the person's home as ~/…; another folder is left as it is.
func shortHome(folder, home string) string {
	if folder == "" || home == "" || home == string(filepath.Separator) {
		return folder
	}
	home = filepath.Clean(home)
	if home == string(filepath.Separator) {
		return folder
	}
	clean := filepath.Clean(folder)
	if clean == home {
		return "~"
	}
	if strings.HasPrefix(clean, home+string(filepath.Separator)) {
		return "~" + clean[len(home):]
	}
	return folder
}

func codesTaskTime(t *codesTaskRecord) time.Time {
	if !t.StartedAt.IsZero() {
		return t.StartedAt
	}
	return t.UpdatedAt
}

// panelCodes is the window's Codes section: the CLIs here and the tasks this worker ran today.
func (n *Agent) panelCodes() *panel.Codes {
	c := &panel.Codes{Agents: n.codesAgents.latest(n.probeCodesAgents)}
	if n.codes != nil {
		c.Tasks = n.codes.list()
	}
	return c
}

// probeCodesAgents is the real detection: this machine's home and PATH, the binaries' own --version.
func (n *Agent) probeCodesAgents() []panel.CodesAgent {
	home, _ := os.UserHomeDir()
	return detectCodesAgents(codesProbe{Home: home, LookPath: exec.LookPath, ReadFile: os.ReadFile,
		Version: codesVersion, OpenCodeAuth: openCodeAuth})
}

// panelStopCodesTask stops the local run the way Codes' own stop does: cancel its Run job here, which kills the
// worker's process and makes its job_exit say cancelled. A task that is not running on this computer is refused.
func (n *Agent) panelStopCodesTask(id string) error {
	if n.codes == nil {
		return errors.New("that task is not running on this computer")
	}
	jobID, ok := n.codes.runningJob(id)
	if !ok || n.worker == nil || !n.worker.Cancel(jobID) {
		return errors.New("that task is not running on this computer")
	}
	return nil
}
