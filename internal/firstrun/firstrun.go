// Package firstrun is the guided first run's local record: which steps this machine reached, what it found, and the
// one telemetry event per step the Link reports to Bot. The record never holds a path, a username, an IP or a secret:
// it is the material for `link.first_run` events and for the terminal, nothing else.
package firstrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/protocol"
)

// EventName is the telemetry event the Link sends for every step. Bot stores these as the machine's first run.
const EventName = "link.first_run"

// The steps, in order. A self-check that failed is still the self_check step (with ok false and a reason), so a
// funnel never counts a failure as a step it did not reach.
const (
	StepInstalled = "installed"
	StepPaired    = "paired"
	StepCheck     = "self_check"
	StepFirstJob  = "first_job"
)

// Steps is the order the guide runs them in.
var Steps = []string{StepInstalled, StepPaired, StepCheck, StepFirstJob}

// UsesStep is the link.first_run event that carries the window's answer to "What's this computer for?". It is not in
// Steps: the funnel counts the guide's four steps, and this one carries what the person said.
const UsesStep = "uses"

// Uses are the answers the window offers, in its order.
var Uses = []string{"codes", "browser", "models", "earn", "vibe"}

// ValidUse reports whether u is one of the answers the window offers.
func ValidUse(u string) bool {
	for _, use := range Uses {
		if use == u {
			return true
		}
	}
	return false
}

// CleanUses keeps the known answers, without repeats, in the window's order, so an event has a fixed shape.
func CleanUses(in []string) []string {
	have := make(map[string]bool, len(in))
	for _, u := range in {
		if ValidUse(u) {
			have[u] = true
		}
	}
	out := make([]string, 0, len(have))
	for _, u := range Uses {
		if have[u] {
			out = append(out, u)
		}
	}
	return out
}

// UsesEvent is the one link.first_run event that carries the person's answer: the step, the uses, and the version, OS
// and architecture every event carries. It never carries a path, a username, an address or a secret.
func UsesEvent(uses []string, version, goos, goarch string) protocol.Event {
	f := map[string]any{"step": UsesStep, "uses": CleanUses(uses), "version": version, "os": goos}
	if goarch != "" {
		f["arch"] = goarch
	}
	return protocol.Event{Name: EventName, TS: time.Now().UnixMilli(), Fields: f}
}

// Step is one reached step: when, whether the self-check passed, and what the first job found.
type Step struct {
	Name   string         `json:"name"`
	At     time.Time      `json:"at"`
	OK     bool           `json:"ok,omitempty"`
	Reason string         `json:"reason,omitempty"`
	Fields map[string]any `json:"fields,omitempty"`
}

// Report is the on-disk first-run record (firstrun.json in the state directory, mode 0600).
type Report struct {
	Version int    `json:"version"`
	Steps   []Step `json:"steps"`
}

// FileName is the record's name in the Link's state directory.
const FileName = "firstrun.json"

// Load reads the record; a missing one is an empty report, not an error.
func Load(path string) (*Report, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Report{Version: 1}, nil
	}
	if err != nil {
		return nil, err
	}
	var r Report
	if err := json.Unmarshal(b, &r); err != nil {
		// A torn or old record must not stop the guide: start over.
		return &Report{Version: 1}, nil
	}
	r.Version = 1
	return &r, nil
}

// Save writes the record atomically with mode 0600.
func (r *Report) Save(path string) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".firstrun-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Upsert records a step, keeping the first time it was reached: a second run of the guide does not make an install
// look newer, and re-reporting a step is harmless.
func (r *Report) Upsert(s Step) {
	for i := range r.Steps {
		if r.Steps[i].Name == s.Name {
			r.Steps[i] = s
			return
		}
	}
	r.Steps = append(r.Steps, s)
}

// Has reports whether the step is already recorded.
func (r *Report) Has(name string) bool {
	for _, s := range r.Steps {
		if s.Name == name {
			return true
		}
	}
	return false
}

// ValidStep reports whether name is one of the steps Bot stores.
func ValidStep(name string) bool {
	for _, s := range Steps {
		if s == name {
			return true
		}
	}
	return false
}

// ValidEvent is what the running Link accepts from `openvibe-link`: a first-run event for a known step, with the
// version and OS every one carries. Anything else from the local socket is refused rather than forwarded.
func ValidEvent(e protocol.Event) bool {
	if e.Name != EventName || e.Fields == nil {
		return false
	}
	step, _ := e.Fields["step"].(string)
	if !ValidStep(step) {
		return false
	}
	if _, ok := e.Fields["version"].(string); !ok {
		return false
	}
	if _, ok := e.Fields["os"].(string); !ok {
		return false
	}
	return true
}

// Info is what the first job found about the machine. It is all counts and names: no path, no user, no address.
type Info struct {
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	CPUs        int    `json:"cpus,omitempty"`
	MemoryTotal uint64 `json:"memory_total_bytes,omitempty"`
	MemoryFree  uint64 `json:"memory_free_bytes,omitempty"`
	DiskTotal   uint64 `json:"disk_total_bytes,omitempty"`
	DiskFree    uint64 `json:"disk_free_bytes,omitempty"`
}

// Gather collects the machine report. dir is only used to choose the filesystem whose free space is measured; it is
// never stored.
func Gather(dir string) Info {
	i := Info{OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU()}
	i.MemoryTotal, i.MemoryFree = memory()
	i.DiskTotal, i.DiskFree = disk(dir)
	return i
}

// Fields turns the info into the first_job event's fields, with the version and OS every event carries.
func (i Info) Fields(version string) map[string]any {
	f := map[string]any{"version": version, "os": i.OS}
	if i.Arch != "" {
		f["arch"] = i.Arch
	}
	if i.CPUs > 0 {
		f["cpus"] = i.CPUs
	}
	if i.MemoryTotal > 0 {
		f["memory_total_bytes"] = i.MemoryTotal
		f["memory_free_bytes"] = i.MemoryFree
	}
	if i.DiskTotal > 0 {
		f["disk_total_bytes"] = i.DiskTotal
		f["disk_free_bytes"] = i.DiskFree
	}
	return f
}

// Events turns the recorded steps into the telemetry events, in order. Every event names the step, the version and
// the OS; a failed self-check carries the reason, and the first job carries the machine's counts.
func (r *Report) Events(version, goos, goarch string) []protocol.Event {
	var out []protocol.Event
	for _, name := range Steps {
		for _, s := range r.Steps {
			if s.Name != name {
				continue
			}
			f := map[string]any{"step": s.Name, "version": version, "os": goos}
			if goarch != "" {
				f["arch"] = goarch
			}
			if s.Name == StepCheck {
				f["ok"] = s.OK
				if !s.OK && s.Reason != "" {
					f["reason"] = SafeReason(s.Reason)
				}
			}
			if s.Name == StepFirstJob {
				for k, v := range s.Fields {
					f[k] = v
				}
				// The step, version and OS are ours, never a field's.
				f["step"], f["version"], f["os"], f["arch"] = s.Name, version, goos, goarch
			}
			at := s.At
			if at.IsZero() {
				at = time.Now()
			}
			out = append(out, protocol.Event{Name: EventName, TS: at.UnixMilli(), Fields: f})
			break
		}
	}
	return out
}

// SafeReason bounds and cleans a self-check reason for an event and a page: one line, no control characters, no
// path, no address. The checks themselves produce bounded reasons; this is the last gate.
func SafeReason(reason string) string {
	reason = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, reason)
	reason = strings.Join(strings.Fields(reason), " ")
	reason = scrubAddresses(reason)
	if len(reason) > 240 {
		reason = reason[:240]
	}
	return strings.TrimSpace(reason)
}

// scrubAddresses drops anything that looks like an IPv4/IPv6 address or a URL path, so a transport error can never
// put a machine's address in an event.
func scrubAddresses(s string) string {
	fields := strings.Fields(s)
	out := fields[:0]
	for _, f := range fields {
		host := strings.TrimSuffix(strings.Trim(f, "[](),;:"), ".")
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = strings.Trim(h, "[]")
		}
		if ip := net.ParseIP(host); ip != nil {
			continue
		}
		if strings.Contains(f, "/") && (strings.Contains(f, "://") || strings.HasPrefix(f, "/")) {
			continue
		}
		out = append(out, f)
	}
	return strings.Join(out, " ")
}

// Check answers whether this machine reaches Bot and, for a Network-paired machine, Network: GET <origin>/api/health,
// a 2xx being up. The reason is safe to store (no address, no path) and short.
func Check(ctx context.Context, client *http.Client, botURL, networkURL string) (bool, string) {
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	checked := 0
	for _, target := range []struct{ name, url string }{{"OpenVibe.Bot", botURL}, {"OpenVibe.Network", networkURL}} {
		if strings.TrimSpace(target.url) == "" {
			continue
		}
		checked++
		if ok, reason := probe(ctx, client, target.name, target.url); !ok {
			return false, reason
		}
	}
	if checked == 0 {
		return false, "no OpenVibe address is configured"
	}
	return true, ""
}

func probe(ctx context.Context, client *http.Client, name, origin string) (bool, string) {
	u := strings.TrimSuffix(strings.TrimSpace(origin), "/") + "/api/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, fmt.Sprintf("%s: the configured address is not usable", name)
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, fmt.Sprintf("%s did not answer in time", name)
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return false, fmt.Sprintf("%s did not answer in time", name)
		}
		return false, fmt.Sprintf("%s is unreachable from this machine", name)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return false, fmt.Sprintf("%s answered HTTP %d", name, resp.StatusCode)
	}
	return true, ""
}
