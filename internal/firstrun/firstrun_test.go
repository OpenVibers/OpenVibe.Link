package firstrun

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestReportRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	r, err := Load(path)
	if err != nil || len(r.Steps) != 0 {
		t.Fatalf("%v %+v", err, r)
	}
	at := time.Now().UTC().Truncate(time.Second)
	r.Upsert(Step{Name: StepInstalled, At: at})
	r.Upsert(Step{Name: StepPaired, At: at.Add(time.Second)})
	r.Upsert(Step{Name: StepCheck, At: at.Add(2 * time.Second), OK: true})
	r.Upsert(Step{Name: StepFirstJob, At: at.Add(3 * time.Second), Fields: Info{OS: "linux", Arch: "amd64", CPUs: 4}.Fields("v1")})
	// A second upsert replaces the step rather than appending a duplicate.
	r.Upsert(Step{Name: StepCheck, At: at.Add(4 * time.Second), OK: false, Reason: "OpenVibe.Bot answered HTTP 503"})
	if err := r.Save(path); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
	got, err := Load(path)
	if err != nil || len(got.Steps) != 4 {
		t.Fatalf("%v %+v", err, got)
	}
	last := got.Steps[len(got.Steps)-1]
	if last.Name != StepFirstJob || last.Fields["cpus"] != float64(4) {
		t.Fatalf("last step %+v (JSON numbers decode as float64)", last)
	}
}

func TestEventsCarryOnlyTheStepAndMachineCounts(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	r := &Report{Version: 1, Steps: []Step{
		{Name: StepInstalled, At: at},
		{Name: StepPaired, At: at.Add(time.Second)},
		{Name: StepCheck, At: at.Add(2 * time.Second), OK: false, Reason: "OpenVibe.Bot answered HTTP 503"},
		{Name: StepFirstJob, At: at.Add(3 * time.Second), Fields: Info{OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: 8,
			MemoryTotal: 16 << 30, MemoryFree: 8 << 30, DiskTotal: 100 << 30, DiskFree: 40 << 30}.Fields("v9")},
	}}
	events := r.Events("v9", runtime.GOOS, runtime.GOARCH)
	if len(events) != 4 {
		t.Fatalf("events %d", len(events))
	}
	want := []string{"installed", "paired", "self_check", "first_job"}
	for i, e := range events {
		if e.Name != EventName || e.Fields["step"] != want[i] || e.Fields["version"] != "v9" || e.Fields["os"] != runtime.GOOS {
			t.Fatalf("event %d: %+v", i, e)
		}
	}
	if events[2].Fields["ok"] != false || !strings.Contains(events[2].Fields["reason"].(string), "HTTP 503") {
		t.Fatalf("self-check %+v", events[2])
	}
	if events[3].Fields["cpus"] != 8 || events[3].Fields["disk_free_bytes"] != uint64(40<<30) {
		t.Fatalf("first job %+v", events[3])
	}
	// Nothing a person owns goes into an event: no hostname, no account name, no home path, no IPv4 address.
	b, _ := json.Marshal(events)
	host, _ := os.Hostname()
	for _, secret := range []string{host, os.Getenv("USER"), os.Getenv("HOME"), "127.0.0.1", "/home/", "/tmp/"} {
		if secret != "" && strings.Contains(string(b), secret) {
			t.Fatalf("%q in events: %s", secret, b)
		}
	}
}

func TestSafeReasonDropsAddressesAndPaths(t *testing.T) {
	got := SafeReason("dial tcp 10.1.2.3:443: connect: refused; /home/alice/.config\nnext line")
	if strings.Contains(got, "10.1.2.3") || strings.Contains(got, "/home/") || strings.Contains(got, "\n") {
		t.Fatalf("unsafe reason %q", got)
	}
	long := strings.Repeat("x", 400)
	if len(SafeReason(long)) != 240 {
		t.Fatalf("not bounded: %d", len(SafeReason(long)))
	}
}

func TestCheck(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusServiceUnavailable)
	}))
	defer down.Close()

	if good, reason := Check(context.Background(), ok.Client(), ok.URL, ""); !good || reason != "" {
		t.Fatalf("%v %q", good, reason)
	}
	if good, reason := Check(context.Background(), down.Client(), ok.URL, down.URL); good || !strings.Contains(reason, "OpenVibe.Network answered HTTP 503") {
		t.Fatalf("%v %q", good, reason)
	}
	if good, reason := Check(context.Background(), ok.Client(), "http://127.0.0.1:1", ""); good || strings.Contains(reason, "127.0.0.1") {
		t.Fatalf("%v %q", good, reason)
	}
	if good, reason := Check(context.Background(), ok.Client(), "", ""); good || !strings.Contains(reason, "no OpenVibe address") {
		t.Fatalf("%v %q", good, reason)
	}
}

func TestGatherNeverStoresTheDirectory(t *testing.T) {
	dir := t.TempDir()
	i := Gather(dir)
	if i.OS != runtime.GOOS || i.Arch != runtime.GOARCH || i.CPUs < 1 {
		t.Fatalf("%+v", i)
	}
	if runtime.GOOS == "linux" && (i.MemoryTotal == 0 || i.DiskTotal == 0) {
		t.Fatalf("linux figures missing: %+v", i)
	}
	b, _ := json.Marshal(i)
	if strings.Contains(string(b), dir) {
		t.Fatalf("the directory leaked: %s", b)
	}
}
