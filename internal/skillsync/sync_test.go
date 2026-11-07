package skillsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBodyAndFrontmatter(t *testing.T) {
	s := Skill{Slug: "ship-a-change", Title: "Ship a change", Summary: "Open a PR, wait for CI.\nBe careful.", Body: "---\nname: old\ndescription: old\n---\n\n# Ship\n\nDo it.\n"}
	body := BodyOf(s)
	if strings.Contains(body, "name: old") {
		t.Fatalf("BodyOf kept the old frontmatter: %q", body)
	}
	if !strings.HasPrefix(body, "# Ship") {
		t.Fatalf("BodyOf lost the body: %q", body)
	}
	fm := Frontmatter(s)
	if !strings.HasPrefix(fm, "---\nname: \"ship-a-change\"\n") {
		t.Fatalf("frontmatter = %q", fm)
	}
	if strings.Contains(fm, "\nBe careful.") || !strings.Contains(fm, "Be careful.") {
		t.Fatalf("frontmatter is not one line: %q", fm)
	}
}

func TestReplaceAndRemovePreserveThePerson(t *testing.T) {
	user := []byte("# My own notes\n\nKeep me.\n")
	block := Block(Skill{Slug: "x", Title: "X", Body: "The skill."})
	next, sep, changed, err := Replace(user, block)
	if err != nil || !changed {
		t.Fatalf("Replace: changed=%v err=%v", changed, err)
	}
	if !strings.Contains(string(next), "Keep me.") || !strings.Contains(string(next), StartMarker) {
		t.Fatalf("next = %q", next)
	}
	back, found, err := Remove(next, sep)
	if err != nil || !found {
		t.Fatalf("Remove: found=%v err=%v", found, err)
	}
	if string(back) != string(user) {
		t.Fatalf("undo = %q, want %q", back, user)
	}
}

func TestReplaceRejectsHalfABlock(t *testing.T) {
	half := []byte("before\n" + StartMarker + "\ntext\n")
	if _, _, _, err := Replace(half, Block(Skill{Slug: "x", Body: "y"})); err == nil {
		t.Fatal("Replace accepted half a block")
	}
}

func TestOnlyOurContent(t *testing.T) {
	fm := Frontmatter(Skill{Slug: "x", Title: "X"})
	block := Block(Skill{Slug: "x", Body: "body"})
	if !OnlyOurContent([]byte(fm), fm) {
		t.Fatal("a created skill file with only our content was not recognised")
	}
	if OnlyOurContent([]byte(fm+"\n"+block+"my notes\n"), fm) {
		t.Fatal("a file with the person's text was treated as ours")
	}
}

func TestRulesBlockSanitizesAndDeduplicates(t *testing.T) {
	got := RulesBlock([]string{"never force-push", "never  force-push", "a " + StartMarker + " b", "", "another"})
	if strings.Count(got, StartMarker) != 1 || !strings.Contains(got, "never force-push") || !strings.Contains(got, "another") {
		t.Fatalf("rules = %q", got)
	}
	if strings.Contains(got, "\n- never  force-push") {
		t.Fatalf("duplicate rules were not removed: %q", got)
	}
}

func TestTargetsAndToolIDs(t *testing.T) {
	found := map[string]bool{}
	for _, target := range Targets() {
		found[target.Tool+"/"+target.Root.String()] = true
	}
	for _, want := range []string{"claude-code/home", "claude-code/project", "codex/home", "opencode/home", "command-code/home", "aider/project"} {
		if !found[want] {
			t.Fatalf("missing target %s in %v", want, found)
		}
	}
	for _, id := range ToolIDs() {
		if !KnownTool(id) || len(TargetsFor(id)) == 0 {
			t.Fatalf("tool %s is listed but has no target", id)
		}
	}
	if _, ok := TargetForPath("/home/me/.claude/skills/demo/SKILL.md"); !ok {
		t.Fatal("TargetForPath did not recognise a skill file")
	}
	if _, ok := TargetForPath("/home/me/project/CLAUDE.md"); !ok {
		t.Fatal("TargetForPath did not recognise a project rules file")
	}
}

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "skills-sync.json")
	st := NewState()
	st.Tool("claude-code").Enabled = true
	st.File("claude-code", "/home/me/.claude/CLAUDE.md").Sep = "\n"
	if err := st.Save(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %v err = %v", info.Mode().Perm(), err)
	}
	back, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if !back.Tool("claude-code").Enabled || back.FileStateOf("claude-code", "/home/me/.claude/CLAUDE.md").Sep != "\n" {
		t.Fatalf("state did not round-trip: %+v", back)
	}
}

func TestClientManifest(t *testing.T) {
	var gotAuth, gotProject string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotProject = r.URL.Query().Get("project")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"skills": []map[string]any{
				{"id": "skl_1", "slug": "demo", "kind": "skill", "title": "Demo", "body": "text", "version": 2, "tools": []string{"claude-code"}},
			},
		})
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	skills, err := c.Manifest(context.Background(), "demo-project")
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok" || gotProject != "demo-project" || len(skills) != 1 || skills[0].Version != 2 {
		t.Fatalf("auth=%q project=%q skills=%+v", gotAuth, gotProject, skills)
	}
}

func TestClientRefusesAnUnsafeSlug(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"skills": []map[string]any{{"slug": "../../etc", "kind": "skill", "body": "x"}}})
	}))
	defer srv.Close()
	c, _ := NewClient(srv.URL, "tok")
	if _, err := c.Manifest(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "unsafe folder name") {
		t.Fatalf("an unsafe slug was accepted: %v", err)
	}
}

func TestClientUnauthorizedIsExplained(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"auth.required"}`))
	}))
	defer srv.Close()
	c, _ := NewClient(srv.URL, "tok")
	if _, err := c.Manifest(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "refused the access token") {
		t.Fatalf("401 was not explained: %v", err)
	}
}
