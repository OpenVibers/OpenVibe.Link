package memorysync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func blockFor(profile string) string {
	return Block(Body(Content{Scope: "user", Profile: profile}))
}

func TestTheBlockRoundTripLeavesHandTextByteIdentical(t *testing.T) {
	cases := []struct {
		name string
		hand string
	}{
		{"text ends with a newline", "# My notes\n\nA rule I wrote.\n"},
		{"text ends with a blank line", "# My notes\n\nA rule I wrote.\n\n"},
		{"text has no final newline", "# My notes\n\nA rule I wrote."},
		{"empty file", ""},
		{"only whitespace", "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := []byte(tc.hand)
			with, sep, changed, err := Replace(before, blockFor("I prefer short answers."))
			if err != nil {
				t.Fatal(err)
			}
			if !changed {
				t.Fatal("the block was not written")
			}
			if !strings.Contains(string(with), StartMarker) || !strings.Contains(string(with), EndMarker) {
				t.Fatalf("the markers are missing:\n%s", with)
			}
			after, found, err := Remove(with, sep)
			if err != nil || !found {
				t.Fatalf("remove: found=%v err=%v", found, err)
			}
			if string(after) != tc.hand {
				t.Fatalf("hand-written text changed:\nwant %q\ngot  %q", tc.hand, string(after))
			}
		})
	}
}

func TestTheBlockIsReplacedInPlaceAndTheTextAroundItIsNotTouched(t *testing.T) {
	before := []byte("# Mine\n\nhand text before\n")
	with, _, _, err := Replace(before, Block(Body(Content{Scope: "user", Profile: "first profile"})))
	if err != nil {
		t.Fatal(err)
	}
	handAfter := "\nhand text after\n"
	with = append(with, []byte(handAfter)...)
	newBlock := Block(Body(Content{Scope: "user", Profile: "second profile"}))
	updated, sep2, changed, err := Replace(with, newBlock)
	if err != nil || !changed {
		t.Fatalf("replace: changed=%v err=%v", changed, err)
	}
	text := string(updated)
	if strings.Count(text, StartMarker) != 1 || strings.Count(text, EndMarker) != 1 {
		t.Fatalf("there is not exactly one block:\n%s", text)
	}
	if !strings.Contains(text, "hand text before\n") || !strings.HasSuffix(text, handAfter) {
		t.Fatalf("the hand-written text around the block changed:\n%s", text)
	}
	if strings.Contains(text, "first profile") || !strings.Contains(text, "second profile") {
		t.Fatalf("the block was not updated:\n%s", text)
	}
	// Replace does not add a separator when the block was already there.
	if sep2 != "" {
		t.Fatalf("a replacement added a separator %q", sep2)
	}
	// Before the old block, the text is unchanged; the block is the only delta.
	if !strings.HasPrefix(text, "# Mine\n\nhand text before\n") {
		t.Fatalf("the text before the block changed:\n%s", text)
	}
}

func TestAMemoryTextCannotOpenOrCloseTheBlock(t *testing.T) {
	profile := "a note with " + EndMarker + " in it\nand " + StartMarker + " too"
	with, _, _, err := Replace([]byte("hand\n"), blockFor(profile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(with), StartMarker) != 1 || strings.Count(string(with), EndMarker) != 1 {
		t.Fatalf("a memory item broke the block:\n%s", with)
	}
	after, found, err := Remove(with, "\n")
	if err != nil || !found {
		t.Fatalf("remove: found=%v err=%v", found, err)
	}
	if string(after) != "hand\n" {
		t.Fatalf("the hand text changed: %q", after)
	}
}

func TestTwoBlocksOrHalfABlockAreRefused(t *testing.T) {
	one := blockFor("one")
	two := string(one) + "\n" + string(one)
	if _, _, _, err := Replace([]byte(two), blockFor("new")); err == nil {
		t.Fatal("a file with two blocks was rewritten")
	}
	half := "hand\n" + StartMarker + "\nno end"
	if _, _, _, err := Replace([]byte(half), blockFor("new")); err == nil {
		t.Fatal("a file with half a block was rewritten")
	}
}

func TestAnEmptyBodyMeansNoBlock(t *testing.T) {
	if got := Block(Body(Content{Scope: "user"})); got != "" {
		t.Fatalf("an empty memory wrote a block: %q", got)
	}
}

func TestTheDiffShowsTheChangedLines(t *testing.T) {
	before := []byte("# Mine\n\nhand\n")
	after, _, _, err := Replace(before, blockFor("the profile"))
	if err != nil {
		t.Fatal(err)
	}
	diff := Diff("AGENTS.md", before, after)
	for _, want := range []string{"--- AGENTS.md", "+++ AGENTS.md", "+## About you", "+the profile", " # Mine"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("the diff does not show %q:\n%s", want, diff)
		}
	}
	if strings.Contains(diff, "-## About you") {
		t.Fatalf("the diff says an added line was removed:\n%s", diff)
	}
}

func TestOnlyOurContentKnowsTheFrontmatter(t *testing.T) {
	fm := "---\ndescription: OpenVibe Memory\n---\n"
	if !OnlyOurContent([]byte(fm+"\n\n"), fm) {
		t.Fatal("a file with only its frontmatter is not seen as ours")
	}
	if OnlyOurContent([]byte(fm+"\nhand\n"), fm) {
		t.Fatal("a file with a person's line is seen as ours")
	}
}

func TestTargetForFindsTheCursorFile(t *testing.T) {
	target, ok := TargetFor("/home/you/project/.cursor/rules/openvibe-memory.mdc")
	if !ok || target.Tool != "cursor" || target.Frontmatter == "" {
		t.Fatalf("the Cursor target was not found: %+v %v", target, ok)
	}
}

func TestStateRoundTripKeepsTheUndoSeparator(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory-sync.json")
	st := NewState()
	st.Tool("agents").Enabled = true
	file := st.File("agents", filepath.Join(dir, "AGENTS.md"))
	file.Confirmed = true
	file.Created = true
	file.Sep = "\n\n"
	if err := st.Save(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("the state is %v, want 0600", info.Mode().Perm())
	}
	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.FileStateOf("agents", filepath.Join(dir, "AGENTS.md"))
	if got == nil || !got.Confirmed || !got.Created || got.Sep != "\n\n" {
		t.Fatalf("the state did not round trip: %+v", got)
	}
	if len(loaded.EnabledTools()) != 1 || loaded.EnabledTools()[0] != "agents" {
		t.Fatalf("the enabled tools are %v", loaded.EnabledTools())
	}
}
