package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// skillManifest is the hub's answer; phase 1 has the demo skill, phase 2 does not.
func skillManifest(project string, phase int32) map[string]any {
	skills := []map[string]any{
		{"id": "skl_1", "slug": "demo", "kind": "skill", "title": "Demo", "summary": "A demo skill", "version": 2,
			"tools": []string{"claude-code"}, "body": "# Demo\n\nDo the thing.\n"},
		{"id": "skl_2", "slug": "no-force-push", "kind": "rule", "title": "No force push", "version": 1,
			"tools": []string{}, "body": "Never force-push a shared branch."},
		{"id": "skl_3", "slug": "an-mcp", "kind": "mcp", "title": "An MCP server", "version": 1,
			"tools": []string{}, "body": `{"command":"x"}`},
	}
	if project != "" {
		skills = append(skills,
			map[string]any{"id": "skl_4", "slug": "project-only", "kind": "skill", "title": "Project only", "version": 1,
				"tools": []string{"claude-code"}, "body": "# Project only\n"},
			map[string]any{"id": "skl_5", "slug": "project-rule", "kind": "rule", "title": "Project rule", "version": 1,
				"tools": []string{}, "body": "Use tabs in this project."},
			map[string]any{"id": "skl_6", "slug": "opencode-only", "kind": "rule", "title": "OpenCode only", "version": 1,
				"tools": []string{"opencode"}, "body": "Only OpenCode needs this."},
		)
	}
	if phase >= 2 {
		skills = skills[1:]
	}
	return map[string]any{"generated_at": "2026-11-07T00:00:00Z", "skills": skills}
}

func TestSkillsSyncDryRunWriteAndUndo(t *testing.T) {
	var phase atomic.Int32
	var seenProject atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		project := r.URL.Query().Get("project")
		seenProject.Store(project)
		_ = json.NewEncoder(w).Encode(skillManifest(project, phase.Load()))
	}))
	defer srv.Close()

	home := t.TempDir()
	project := t.TempDir()
	state := filepath.Join(t.TempDir(), "skills-sync.json")
	t.Setenv("OV_TEST_SKILLS_TOKEN", "test-token")
	args := []string{"--tools", "claude-code,codex,opencode", "--project", project, "--yes",
		"--user-home", home, "--state", state, "--endpoint", srv.URL, "--token-env", "OV_TEST_SKILLS_TOKEN"}

	// The person's own text in a rules file must survive every run and the undo.
	notes := filepath.Join(home, ".claude", "CLAUDE.md")
	if err := os.MkdirAll(filepath.Dir(notes), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notes, []byte("# My own notes\n\nKeep me.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A dry run reads the hub and writes nothing at all.
	var out, errOut bytes.Buffer
	if code := cmdSkillsSync(append(args, "--dry-run"), strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("dry run exited %d: %s%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "dry run") {
		t.Fatalf("dry run did not say so: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills")); !os.IsNotExist(err) {
		t.Fatalf("dry run created a skill folder: %v", err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote the state: %v", err)
	}
	if seenProject.Load() == "" {
		// The temp project's slug is its directory name; it only has to have been asked for.
		t.Fatal("the project manifest was not asked for")
	}

	out.Reset()
	errOut.Reset()
	if code := cmdSkillsSync(args, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("sync exited %d: %s%s", code, out.String(), errOut.String())
	}
	skill := filepath.Join(home, ".claude", "skills", "demo", "SKILL.md")
	raw, err := os.ReadFile(skill)
	if err != nil {
		t.Fatalf("the skill file was not written: %v", err)
	}
	for _, want := range []string{"---\nname: \"demo\"", "A demo skill", skillsyncMarker(), "# Demo", "Do the thing."} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("skill file misses %q:\n%s", want, raw)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "skills", "demo", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("a claude-code-only skill was written for Codex: %v", err)
	}
	codexRules := filepath.Join(home, ".codex", "AGENTS.md")
	codexRaw, err := os.ReadFile(codexRules)
	if err != nil || !strings.Contains(string(codexRaw), "Never force-push") {
		t.Fatalf("the rule was not written for Codex: %v\n%s", err, codexRaw)
	}
	projectSkill := filepath.Join(project, ".claude", "skills", "project-only", "SKILL.md")
	if _, err := os.Stat(projectSkill); err != nil {
		t.Fatalf("the project skill was not written: %v", err)
	}
	projectRules := filepath.Join(project, "CLAUDE.md")
	if raw, err := os.ReadFile(projectRules); err != nil || !strings.Contains(string(raw), "Use tabs in this project.") {
		t.Fatalf("the project rule was not written: %v\n%s", err, raw)
	}
	// AGENTS.md is shared by Codex, OpenCode and Command Code: one block, and the rules for all of them.
	shared, err := os.ReadFile(filepath.Join(project, "AGENTS.md"))
	if err != nil || strings.Count(string(shared), skillsyncMarker()) != 1 {
		t.Fatalf("the shared project AGENTS.md is not one block: %v\n%s", err, shared)
	}
	if !strings.Contains(string(shared), "Use tabs in this project.") || !strings.Contains(string(shared), "Only OpenCode needs this.") {
		t.Fatalf("the shared project rules lost a tool's rule:\n%s", shared)
	}
	if strings.Count(string(shared), "openvibe-skills:start") != 1 {
		t.Fatalf("the shared project AGENTS.md holds more than one block:\n%s", shared)
	}
	// The MCP record is kept in the hub but this sync does not write MCP configs.
	if strings.Contains(out.String(), "an-mcp") {
		t.Fatalf("the MCP record was reported as written: %s", out.String())
	}
	if raw, _ := os.ReadFile(notes); !strings.Contains(string(raw), "Keep me.") {
		t.Fatalf("the person's notes were changed: %s", raw)
	}

	// A stale skill is removed when the manifest no longer names it.
	phase.Store(2)
	out.Reset()
	errOut.Reset()
	if code := cmdSkillsSync(args, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("second sync exited %d: %s%s", code, out.String(), errOut.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "demo")); !os.IsNotExist(err) {
		t.Fatalf("the stale skill folder is still there: %v\nout: %s\nerr: %s", err, out.String(), errOut.String())
	}

	// Undo puts the person's file back exactly and removes what the Link made.
	out.Reset()
	errOut.Reset()
	if code := cmdSkillsSync([]string{"--undo", "--user-home", home, "--project", project, "--state", state}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("undo exited %d: %s%s", code, out.String(), errOut.String())
	}
	if raw, _ := os.ReadFile(notes); string(raw) != "# My own notes\n\nKeep me.\n" {
		t.Fatalf("undo did not restore the rules file: %q", raw)
	}
	for _, path := range []string{
		filepath.Join(home, ".claude", "skills", "demo", "SKILL.md"),
		filepath.Join(home, ".codex", "AGENTS.md"),
		filepath.Join(project, ".claude", "skills", "project-only", "SKILL.md"),
		filepath.Join(project, "CLAUDE.md"),
		filepath.Join(project, "AGENTS.md"),
		filepath.Join(home, ".config", "opencode", "AGENTS.md"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("undo left %s: %v", path, err)
		}
	}
}

func skillsyncMarker() string { return "<!-- openvibe-skills:start -->" }

func TestSkillsSyncRefusesAnUnknownToolAndAMissingToken(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := cmdSkillsSync([]string{"--tools", "nope"}, strings.NewReader(""), &out, &errOut); code != 2 {
		t.Fatalf("unknown tool exited %d, want 2", code)
	}
	t.Setenv("OV_TEST_SKILLS_TOKEN", "")
	out.Reset()
	errOut.Reset()
	if code := cmdSkillsSync([]string{"--tools", "claude-code", "--token-env", "OV_TEST_SKILLS_TOKEN", "--yes", "--user-home", t.TempDir(), "--state", filepath.Join(t.TempDir(), "s.json")}, strings.NewReader(""), &out, &errOut); code != 1 {
		t.Fatalf("missing token exited %d, want 1\nout: %s\nerr: %s", code, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "OV_TEST_SKILLS_TOKEN") {
		t.Fatalf("missing token was not named: %s", errOut.String())
	}
}
