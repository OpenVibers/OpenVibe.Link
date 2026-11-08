package panel

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

const testToken = "test-token-for-the-panel"

func testServer(t *testing.T, opt Options) *Server {
	t.Helper()
	if opt.Token == "" {
		opt.Token = testToken
	}
	srv, err := Listen(opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func do(t *testing.T, srv *Server, method, path, token, origin, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		r.Header.Set("X-Link-Panel", token)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	return w
}

func TestListenIsLoopbackOnly(t *testing.T) {
	srv := testServer(t, Options{State: func() State { return State{} }})
	host, port, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	if host != "127.0.0.1" || port == "0" || port == "" {
		t.Fatalf("addr %q is not a free loopback port", srv.Addr())
	}
	if srv.Origin() != "http://"+srv.Addr() {
		t.Fatalf("origin %q", srv.Origin())
	}
	if !strings.HasPrefix(srv.URL(), srv.Origin()+"/panel/#t="+testToken) {
		t.Fatalf("url %q", srv.URL())
	}
}

func TestAPIRequiresTheToken(t *testing.T) {
	srv := testServer(t, Options{State: func() State { return State{Machine: &Machine{Name: "here"}} }})
	if w := do(t, srv, http.MethodGet, "/panel/api/state", "", "", ""); w.Code != http.StatusForbidden {
		t.Fatalf("no token: %d", w.Code)
	}
	if w := do(t, srv, http.MethodGet, "/panel/api/state", "wrong-token-wrong-token", "", ""); w.Code != http.StatusForbidden {
		t.Fatalf("wrong token: %d", w.Code)
	}
	w := do(t, srv, http.MethodGet, "/panel/api/state", testToken, "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("token: %d %s", w.Code, w.Body)
	}
	var st State
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || st.Machine == nil || st.Machine.Name != "here" {
		t.Fatalf("state: %v %+v", err, st)
	}
}

func TestAPIRequiresItsOwnOrigin(t *testing.T) {
	srv := testServer(t, Options{State: func() State { return State{} }})
	if w := do(t, srv, http.MethodGet, "/panel/api/state", testToken, "https://evil.example", ""); w.Code != http.StatusForbidden {
		t.Fatalf("wrong origin: %d", w.Code)
	}
	w := do(t, srv, http.MethodGet, "/panel/api/state", testToken, srv.Origin(), "")
	if w.Code != http.StatusOK {
		t.Fatalf("own origin: %d", w.Code)
	}
	for k := range w.Header() {
		if strings.HasPrefix(k, "Access-Control-") {
			t.Fatalf("a CORS header was written: %s", k)
		}
	}
}

func TestStaticFilesNeedNoToken(t *testing.T) {
	srv := testServer(t, Options{})
	w := do(t, srv, http.MethodGet, "/panel/", "", "", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "OpenVibe Link") {
		t.Fatalf("index: %d %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("index content type %q", ct)
	}
	for _, p := range []string{"/panel/panel.css", "/panel/panel.js"} {
		if w := do(t, srv, http.MethodGet, p, "", "", ""); w.Code != http.StatusOK {
			t.Fatalf("%s: %d", p, w.Code)
		}
	}
}

func TestTestdataAndReadmeAreNotServedNorEmbedded(t *testing.T) {
	srv := testServer(t, Options{})
	for _, p := range []string{"/panel/testdata/state.json", "/panel/README.md", "/panel/panel.go"} {
		if w := do(t, srv, http.MethodGet, p, "", "", ""); w.Code != http.StatusNotFound {
			t.Fatalf("%s answered %d", p, w.Code)
		}
	}
	for _, p := range []string{"ui/testdata/state.json", "ui/README.md"} {
		if _, err := fs.Stat(Assets(), p); err == nil {
			t.Fatalf("%s is embedded; it must not be", p)
		}
	}
	if _, err := fs.Stat(Assets(), "ui/index.html"); err != nil {
		t.Fatalf("index.html is not embedded: %v", err)
	}
}

// The window's JSON contract: a fully filled State has exactly the keys testdata/state.json has.
func TestStateShapeMatchesTheDesign(t *testing.T) {
	raw, err := os.ReadFile("ui/testdata/state.json")
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]json.RawMessage
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 7, 10, 2, 0, 0, time.UTC)
	full := State{
		Machine:       &Machine{ID: "dev_1", Name: "Workshop desktop", OS: "linux", Online: true, Paused: false, NeedsYou: 1},
		Running:       []Running{{ID: "r_1", Kind: "agent", Title: "t", Detail: "d", StartedAt: now}},
		Capabilities:  []Capability{{ID: "files", Name: "n", Who: "w", On: true}},
		Shares:        []Share{{Port: 3000, URL: "https://x", ExpiresAt: now}},
		Devices:       []Device{{Name: "d", Kind: "desktop", Online: true, Here: true}},
		GPU:           &GPU{Name: "RTX", VRAMGB: 24, Models: []Model{{Name: "m", TPS: 41, Loaded: true}}},
		EarningsToday: &Earnings{Coins: 120},
		Activity:      []Activity{{At: now, Text: "x"}},
		FirstRun:      true,
		Codes:         &Codes{Agents: []CodesAgent{{ID: "claude-code", Name: "Claude Code", State: "ready"}}, Tasks: []CodesTask{{ID: "t", Title: "t", State: "running", StartedAt: now}}},
	}
	b, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	gotKeys, wantKeys := keysOf(got), keysOf(want)
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("state keys:\n got %v\nwant %v", gotKeys, wantKeys)
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestEmptySectionsAreOmitted(t *testing.T) {
	srv := testServer(t, Options{State: func() State { return State{Machine: &Machine{Name: "x"}} }})
	w := do(t, srv, http.MethodGet, "/panel/api/state", testToken, "", "")
	for _, k := range []string{"running", "capabilities", "shares", "devices", "gpu", "earnings_today", "activity"} {
		if strings.Contains(w.Body.String(), `"`+k+`"`) {
			t.Fatalf("an empty section (%s) was written: %s", k, w.Body)
		}
	}
}

func TestPauseCallsTheKillSwitch(t *testing.T) {
	var calls []bool
	srv := testServer(t, Options{Pause: func(p bool) error { calls = append(calls, p); return nil }})
	if w := do(t, srv, http.MethodPost, "/panel/api/pause", testToken, srv.Origin(), `{"paused":true}`); w.Code != http.StatusOK {
		t.Fatalf("pause: %d %s", w.Code, w.Body)
	}
	if w := do(t, srv, http.MethodPost, "/panel/api/pause", testToken, "", `{"paused":false}`); w.Code != http.StatusOK {
		t.Fatalf("resume: %d %s", w.Code, w.Body)
	}
	if w := do(t, srv, http.MethodPost, "/panel/api/pause", testToken, "", `{}`); w.Code != http.StatusBadRequest {
		t.Fatalf("no paused: %d", w.Code)
	}
	if !reflect.DeepEqual(calls, []bool{true, false}) {
		t.Fatalf("calls %v", calls)
	}
}

func TestStopCapabilityAndSharesReachTheDaemon(t *testing.T) {
	stopped, caps, shared, unshared := "", "", 0, 0
	srv := testServer(t, Options{
		Stop:          func(id string) error { stopped = id; return nil },
		SetCapability: func(id string, on bool) error { caps = id; return nil },
		Share:         func(port int) (Share, error) { shared = port; return Share{Port: port}, nil },
		Unshare:       func(port int) error { unshared = port; return nil },
	})
	if w := do(t, srv, http.MethodPost, "/panel/api/running/approval:a1/stop", testToken, "", ""); w.Code != http.StatusOK {
		t.Fatalf("stop: %d %s", w.Code, w.Body)
	}
	if stopped != "approval:a1" {
		t.Fatalf("stopped %q", stopped)
	}
	if w := do(t, srv, http.MethodPost, "/panel/api/capabilities/files", testToken, "", `{"on":false}`); w.Code != http.StatusOK {
		t.Fatalf("capability: %d %s", w.Code, w.Body)
	}
	if caps != "files" {
		t.Fatalf("capability %q", caps)
	}
	if w := do(t, srv, http.MethodPost, "/panel/api/shares", testToken, "", `{"port":3000}`); w.Code != http.StatusOK {
		t.Fatalf("share: %d %s", w.Code, w.Body)
	}
	if shared != 3000 {
		t.Fatalf("shared %d", shared)
	}
	if w := do(t, srv, http.MethodDelete, "/panel/api/shares/3000", testToken, "", ""); w.Code != http.StatusNoContent {
		t.Fatalf("unshare: %d %s", w.Code, w.Body)
	}
	if unshared != 3000 {
		t.Fatalf("unshared %d", unshared)
	}
}

func TestMachineCarriesItsOwnID(t *testing.T) {
	srv := testServer(t, Options{State: func() State { return State{Machine: &Machine{ID: "dev_1", Name: "here"}} }})
	w := do(t, srv, http.MethodGet, "/panel/api/state", testToken, "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("state: %d %s", w.Code, w.Body)
	}
	var st State
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || st.Machine == nil || st.Machine.ID != "dev_1" {
		t.Fatalf("machine: %v %s", err, w.Body)
	}
}

func TestCodesTaskStopNeedsTheTokenAndARunningTask(t *testing.T) {
	var stopped string
	srv := testServer(t, Options{CodesStop: func(id string) error {
		if id != "tsk_running" {
			return errors.New("that task is not running on this computer")
		}
		stopped = id
		return nil
	}})
	path := "/panel/api/codes/tasks/tsk_running/stop"
	if w := do(t, srv, http.MethodPost, path, "", "", ""); w.Code != http.StatusForbidden {
		t.Fatalf("no token: %d", w.Code)
	}
	if w := do(t, srv, http.MethodPost, path, testToken, "https://evil.example", ""); w.Code != http.StatusForbidden {
		t.Fatalf("wrong origin: %d", w.Code)
	}
	if w := do(t, srv, http.MethodPost, "/panel/api/codes/tasks/tsk_unknown/stop", testToken, srv.Origin(), ""); w.Code != http.StatusConflict {
		t.Fatalf("unknown task: %d %s", w.Code, w.Body)
	}
	if w := do(t, srv, http.MethodPost, path, testToken, srv.Origin(), ""); w.Code != http.StatusOK {
		t.Fatalf("running task: %d %s", w.Code, w.Body)
	}
	if stopped != "tsk_running" {
		t.Fatalf("stopped %q", stopped)
	}
}

func TestFirstRunIsRecordedAfterTokenAndUseChecks(t *testing.T) {
	var calls int
	var gotUses []string
	var gotSkip bool
	srv := testServer(t, Options{FirstRun: func(uses []string, skip bool) error {
		calls++
		gotUses, gotSkip = uses, skip
		return nil
	}})

	// Every write has the same rules: the token, and this server's own origin when one is sent.
	if w := do(t, srv, http.MethodPost, "/panel/api/first-run", "", srv.Origin(), `{"uses":["browser"]}`); w.Code != http.StatusForbidden {
		t.Fatalf("no token: %d", w.Code)
	}
	if w := do(t, srv, http.MethodPost, "/panel/api/first-run", testToken, "https://evil.example", `{"uses":["browser"]}`); w.Code != http.StatusForbidden {
		t.Fatalf("wrong origin: %d", w.Code)
	}
	// An unknown use is a typo, not something to record.
	if w := do(t, srv, http.MethodPost, "/panel/api/first-run", testToken, srv.Origin(), `{"uses":["nope"]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown use: %d %s", w.Code, w.Body)
	}
	if calls != 0 {
		t.Fatalf("the daemon was called %d times before a valid answer", calls)
	}

	w := do(t, srv, http.MethodPost, "/panel/api/first-run", testToken, srv.Origin(), `{"uses":["codes","browser"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("answer: %d %s", w.Code, w.Body)
	}
	var body map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["first_run"] != false {
		t.Fatalf("answer body: %s", w.Body)
	}
	if !reflect.DeepEqual(gotUses, []string{"codes", "browser"}) || gotSkip {
		t.Fatalf("daemon got uses %v skip %v", gotUses, gotSkip)
	}

	if w := do(t, srv, http.MethodPost, "/panel/api/first-run", testToken, srv.Origin(), `{"uses":[],"skip":true}`); w.Code != http.StatusOK || !gotSkip {
		t.Fatalf("skip: %d %s (skip %v)", w.Code, w.Body, gotSkip)
	}
}

func TestFirstRunWithoutAWayToRecordItSaysSo(t *testing.T) {
	bare := testServer(t, Options{})
	if w := do(t, bare, http.MethodPost, "/panel/api/first-run", testToken, "", `{"uses":["vibe"]}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no callback: %d", w.Code)
	}
	refuse := testServer(t, Options{FirstRun: func([]string, bool) error { return errors.New("the answer could not be saved") }})
	w := do(t, refuse, http.MethodPost, "/panel/api/first-run", testToken, "", `{"uses":["vibe"]}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "could not be saved") {
		t.Fatalf("refused: %d %s", w.Code, w.Body)
	}
}

func TestFilesSendEncryptedEnvelope(t *testing.T) {
	var gotTarget, gotEnvelope string
	srv := testServer(t, Options{SendFile: func(target, envelope string) error { gotTarget, gotEnvelope = target, envelope; return nil }})
	w := do(t, srv, http.MethodPost, "/panel/api/files", testToken, "", `{"target_node":"nod_example","envelope":"Y2lwaGVydGV4dA=="}`)
	if w.Code != http.StatusCreated || gotTarget != "nod_example" || gotEnvelope != "Y2lwaGVydGV4dA==" {
		t.Fatalf("send: status %d target %q envelope %q body %s", w.Code, gotTarget, gotEnvelope, w.Body)
	}
}

func TestIncomingItemsNeedAnExplicitAcceptOrDecline(t *testing.T) {
	accepted, declined, completed := false, false, false
	srv := testServer(t, Options{
		SendInbox: func() ([]IncomingSend, error) {
			return []IncomingSend{{ID: "snd_0123456789abcdef0123456789abcdef", Envelope: "ciphertext"}}, nil
		},
		SendAccept:   func(id string) (string, error) { accepted = id != ""; return "ciphertext", nil },
		SendDecline:  func(id string) error { declined = id != ""; return nil },
		SendComplete: func(id string) error { completed = id != ""; return nil },
	})
	if w := do(t, srv, http.MethodGet, "/panel/api/send/inbox", testToken, "", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ciphertext") {
		t.Fatalf("inbox: %d %s", w.Code, w.Body)
	}
	if w := do(t, srv, http.MethodPost, "/panel/api/files/snd_0123456789abcdef0123456789abcdef/accept", testToken, "", "{}"); w.Code != http.StatusOK || !accepted {
		t.Fatalf("accept: %d %s", w.Code, w.Body)
	}
	if w := do(t, srv, http.MethodPost, "/panel/api/files/snd_0123456789abcdef0123456789abcdef/complete", testToken, "", "{}"); w.Code != http.StatusNoContent || !completed {
		t.Fatalf("complete: %d %s", w.Code, w.Body)
	}
	if w := do(t, srv, http.MethodPost, "/panel/api/files/snd_0123456789abcdef0123456789abcdef/decline", testToken, "", "{}"); w.Code != http.StatusNoContent || !declined {
		t.Fatalf("decline: %d %s", w.Code, w.Body)
	}
}

func TestAPIUnknownPathIs404(t *testing.T) {
	srv := testServer(t, Options{})
	if w := do(t, srv, http.MethodGet, "/panel/api/nope", testToken, "", ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown: %d", w.Code)
	}
}
