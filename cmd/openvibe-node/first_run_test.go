package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/OpenVibers/OpenVibe.Node/internal/fakebot"
	"github.com/OpenVibers/OpenVibe.Node/internal/fakenetwork"
	"github.com/OpenVibers/OpenVibe.Node/internal/firstrun"
	"github.com/OpenVibers/OpenVibe.Node/internal/protocol"
	"github.com/OpenVibers/OpenVibe.Node/internal/service"
)

// quietService keeps the guide from asking a service manager on the test host.
func quietService(t *testing.T) {
	t.Helper()
	old := serviceStatus
	serviceStatus = func(service.Options) string { return "not installed" }
	t.Cleanup(func() { serviceStatus = old })
}

func guide(t *testing.T, home string, stdin string, args ...string) (string, string, int) {
	t.Helper()
	t.Setenv("PYTHONPATH", pythonPath(t))
	var out, errb bytes.Buffer
	code := runWithStdin(append([]string{"--home", home}, args...), strings.NewReader(stdin), &out, &errb)
	return out.String(), errb.String(), code
}

func telemetryEvents(t *testing.T, srv *fakebot.Server) []protocol.Event {
	t.Helper()
	c, err := srv.NextConn(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var events []protocol.Event
	for _, f := range c.Frames() {
		if tel, ok := f.Msg.(protocol.Telemetry); ok {
			events = append(events, tel.Events...)
		}
	}
	return events
}

func TestFirstRunWrongCode(t *testing.T) {
	quietService(t)
	srv := fakebot.New()
	defer srv.Close()
	srv.AddCode("ABCD2345")
	home := shortHome(t)
	out, errOut, code := guide(t, home, "ZZZZ-9999\n", "--server", srv.URL())
	if code != 1 {
		t.Fatalf("exit %d, out %s err %s", code, out, errOut)
	}
	if !strings.Contains(out, "Pairing code: ") || !strings.Contains(out, "That did not pair") || !strings.Contains(errOut, "not the pairing code") {
		t.Fatalf("out %q err %q", out, errOut)
	}
	if _, err := os.Stat(filepath.Join(home, "credential.json")); !os.IsNotExist(err) {
		t.Fatal("a credential was written for a wrong code")
	}
	if n := len(srv.PairRequests()); n != 1 {
		t.Fatalf("%d pair requests", n)
	}
}

func TestFirstRunSuccessReportsOneEventPerStep(t *testing.T) {
	quietService(t)
	srv := fakebot.New()
	defer srv.Close()
	srv.AddCode("ABCD2345")
	home := shortHome(t)
	out, errOut, code := guide(t, home, "abcd-2345\n", "--server", srv.URL())
	if code != 0 {
		t.Fatalf("exit %d, out %s err %s", code, out, errOut)
	}
	for _, want := range []string{"OpenVibe Node first run", "Paired as", "Self-check: OpenVibe.Bot answers.",
		"First job: this machine", "system   " + runtime.GOOS, "Reported this first run to OpenVibe.Bot.", "Next:", "docs/install.md"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in output:\n%s", want, out)
		}
	}
	// The record is on disk, mode 0600, and every step is there.
	r, err := firstrun.Load(filepath.Join(home, firstrun.FileName))
	if err != nil || len(r.Steps) != 4 {
		t.Fatalf("%v %+v", err, r)
	}
	events := telemetryEvents(t, srv)
	if len(events) != 4 {
		t.Fatalf("%d events: %+v", len(events), events)
	}
	for i, step := range []string{firstrun.StepInstalled, firstrun.StepPaired, firstrun.StepCheck, firstrun.StepFirstJob} {
		e := events[i]
		if e.Name != firstrun.EventName || e.Fields["step"] != step || e.Fields["version"] != version || e.Fields["os"] != runtime.GOOS {
			t.Fatalf("event %d %+v", i, e)
		}
	}
	if events[2].Fields["ok"] != true {
		t.Fatalf("self-check %+v", events[2])
	}
	if events[3].Fields["cpus"] == nil || (runtime.GOOS == "linux" && events[3].Fields["disk_total_bytes"] == nil) {
		t.Fatalf("first job has no machine figures: %+v", events[3])
	}
	// No personal data, path, username or IP in any event.
	b, _ := json.Marshal(events)
	host, _ := os.Hostname()
	for _, secret := range []string{host, os.Getenv("USER"), os.Getenv("HOME"), home, "127.0.0.1", "/tmp/"} {
		if secret != "" && strings.Contains(string(b), secret) {
			t.Fatalf("%q leaked in %s", secret, b)
		}
	}
}

func TestFirstRunSelfCheckFailureIsReportedWithTheReason(t *testing.T) {
	quietService(t)
	srv := fakebot.New()
	defer srv.Close()
	srv.AddCode("ABCD2345")
	srv.SetHealth(http.StatusServiceUnavailable)
	home := shortHome(t)
	out, errOut, code := guide(t, home, "ABCD-2345\n", "--server", srv.URL())
	if code != 0 {
		t.Fatalf("exit %d, out %s err %s", code, out, errOut)
	}
	if !strings.Contains(out, "Self-check: FAILED: OpenVibe.Bot answered HTTP 503.") {
		t.Fatalf("output:\n%s", out)
	}
	events := telemetryEvents(t, srv)
	if len(events) != 4 || events[2].Fields["step"] != firstrun.StepCheck || events[2].Fields["ok"] != false {
		t.Fatalf("%+v", events)
	}
	if reason, _ := events[2].Fields["reason"].(string); !strings.Contains(reason, "HTTP 503") {
		t.Fatalf("reason %q", reason)
	}
}

// The machines page's install command pasted at the one prompt: Network pairing, then the same four events.
func TestFirstRunNetworkPairingFromTheInstallCommand(t *testing.T) {
	quietService(t)
	bot, net := fakes(t)
	home := shortHome(t)
	command := fmt.Sprintf("curl -fsSL https://openvibe.bot/install | sh -s -- --network %s --pairing %s --code abcd-2345 --worker",
		net.URL(), fakenetwork.PairingID)
	out, errOut, code := guide(t, home, command+"\n", "--server", bot.URL())
	if code != 0 {
		t.Fatalf("exit %d, out %s err %s", code, out, errOut)
	}
	if !strings.Contains(out, "Paired as "+fakenetwork.DeviceID) || !strings.Contains(out, "Self-check: OpenVibe.Bot and OpenVibe.Network answer.") {
		t.Fatalf("output:\n%s", out)
	}
	if p := net.Pairings(); len(p) != 1 || p[0].Code != "ABCD2345" || p[0].Pairing != fakenetwork.PairingID {
		t.Fatalf("%+v", p)
	}
	if bot.Binds() == 0 {
		t.Fatal("the machine was not bound on Bot")
	}
	events := telemetryEvents(t, bot)
	if len(events) != 4 || events[3].Fields["step"] != firstrun.StepFirstJob {
		t.Fatalf("%+v", events)
	}
}

func TestParsePairingInput(t *testing.T) {
	cases := []struct {
		in                            string
		code, network, pairing, robot string
		ok                            bool
	}{
		{"ABCD-1234", "ABCD-1234", "", "", "", true},
		{"openvibe-node pair ABCD-1234", "ABCD-1234", "", "", "", true},
		{"curl -fsSL https://openvibe.bot/install | sh -s -- --network https://openvibe.network --pairing pair_01J8Z4M2Q0R7T9YV3K6N8P1W2Y --code ABCD-1234 --worker",
			"ABCD-1234", "https://openvibe.network", "pair_01J8Z4M2Q0R7T9YV3K6N8P1W2Y", "", true},
		{"openvibe-node pair --code=ABCD-1234 --robot rob_01J8Z4M2Q0R7T9YV3K6N8P1W2X", "ABCD-1234", "", "", "rob_01J8Z4M2Q0R7T9YV3K6N8P1W2X", true},
		{"", "", "", "", "", false},
		{"curl https://openvibe.bot/install | sh", "", "", "", "", false},
	}
	for _, c := range cases {
		a, err := parsePairingInput(c.in)
		if (err == nil) != c.ok {
			t.Fatalf("%q: %v", c.in, err)
		}
		if err != nil {
			continue
		}
		if a.code != c.code || a.network != c.network || a.pairing != c.pairing || a.robot != c.robot {
			t.Fatalf("%q → %+v", c.in, a)
		}
	}
}
