package worker

import (
	"errors"
	"os/exec"
	"testing"

	"github.com/OpenVibers/OpenVibe.Link/internal/config"
	"github.com/OpenVibers/OpenVibe.Link/internal/protocol"
)

func TestVersionServes(t *testing.T) {
	for _, c := range []struct {
		declared, asked string
		want            bool
	}{
		{"1.1.0", "1.1.0", true},
		{"1.2.0", "1.0.0", true},
		{"1.2.0", "1.1.3", true},
		{"1.2.3", "1.2.1", true},
		{"1.2.0", "1.2.1", false},
		{"1.0.0", "1.0.1", false},
		{"2.0.0", "1.9.9", false},
		{"1.9.9", "2.0.0", false},
		{"0.3.0", "0.2.0", false},
		{"1.2.0", "1.0", false},
		{"1.2.0", "1.0.0-beta", false},
		{"1.02.0", "1.1.0", false},
		{"weird", "weird", true},
	} {
		if got := versionServes(c.declared, c.asked); got != c.want {
			t.Errorf("versionServes(%q, %q) = %v, want %v", c.declared, c.asked, got, c.want)
		}
	}
}

// A runner brought up to a later minor (skills.Refresh) keeps admitting the jobs that name the earlier one.
func TestAdmitEarlierMinor(t *testing.T) {
	s := &sink{}
	cfg := helperConfig(t, config.WorkerCaps{MaxJobs: 2}, "sleep")
	cfg.Functions[0].Version = "1.2.0"
	w := New(cfg, s.send, quiet())
	w.isolate = func(*exec.Cmd, config.WorkerConfig, plan) (*sandbox, error) {
		return nil, errors.New("not in this test")
	}
	older := testJob(1, "sleep") // asks 1.0.0
	if f, r := w.Admit(older); f != "" {
		t.Fatalf("1.2.0 serves a job asking 1.0.0: %q %q", f, r)
	}
	newer := testJob(2, "sleep")
	newer.Artifact.Version = "1.3.0"
	if f, r := w.Admit(newer); f != protocol.FaultUnsupported || r != protocol.JobUnknownArtifact {
		t.Fatalf("never a later version than declared: %q %q", f, r)
	}
}
