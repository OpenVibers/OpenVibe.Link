package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OpenVibers/OpenVibe.Node/internal/config"
	"github.com/OpenVibers/OpenVibe.Node/internal/skills"
)

// TestSkillsRunnerOnStatusOff drives the CLI the way an owner does: on declares the runner in worker.functions and
// writes the script, status reports it, off removes the declaration and keeps the script.
func TestSkillsRunnerOnStatusOff(t *testing.T) {
	n, err := skills.FindNode("")
	if err != nil {
		t.Skipf("no usable node on this machine: %v", err)
	}
	home := t.TempDir()
	dir := filepath.Join(home, "runner")
	var stdout, stderr bytes.Buffer
	run := func(args ...string) int {
		stdout.Reset()
		stderr.Reset()
		return runWithStdin(append([]string{"skills"}, args...), strings.NewReader(""), &stdout, &stderr)
	}

	if code := run("on", "--home", home, "--dir", dir, "--node", n.Path); code != 0 {
		t.Fatalf("skills on: code %d, stderr %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Skill runner on") {
		t.Fatalf("on printed %q", stdout.String())
	}
	cfg, err := config.Load(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var found *config.FunctionConfig
	for i := range cfg.Worker.Functions {
		if cfg.Worker.Functions[i].Name == skills.Name {
			found = &cfg.Worker.Functions[i]
		}
	}
	if found == nil {
		t.Fatalf("no %s in worker.functions: %+v", skills.Name, cfg.Worker.Functions)
	}
	if found.Version != skills.Version || found.EffectiveClass() != config.ClassFunction {
		t.Fatalf("entry %+v", *found)
	}
	if found.ArtifactDir != dir {
		t.Fatalf("artifact_dir %q, want %q", found.ArtifactDir, dir)
	}

	stdout.Reset()
	if code := run("status", "--home", home, "--json"); code != 0 {
		t.Fatalf("skills status: code %d, stderr %s", code, stderr.String())
	}
	var st skills.Status
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &st); err != nil {
		t.Fatalf("status is not JSON: %v: %s", err, stdout.String())
	}
	if !st.On || !st.ArtifactPresent || st.Version != skills.Version || st.Artifact != dir {
		t.Fatalf("status %+v", st)
	}

	if code := run("off", "--home", home); code != 0 {
		t.Fatalf("skills off: code %d, stderr %s", code, stderr.String())
	}
	cfg, err = config.Load(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range cfg.Worker.Functions {
		if f.Name == skills.Name {
			t.Fatalf("off left %+v", f)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, skills.File)); err != nil {
		t.Fatalf("off removed the script: %v", err)
	}
	if st := skills.StatusOf(cfg); st.On {
		t.Fatalf("still on: %+v", st)
	}
}
