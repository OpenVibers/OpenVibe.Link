// Package memorysync writes the person's OpenVibe Memory profile and project
// rules into the files the coding tools already read.
//
// It writes only inside one clearly marked block, and it never touches the
// person's hand-written text outside that block:
//
//	<!-- openvibe-memory:start -->
//	...
//	<!-- openvibe-memory:end -->
//
// Every file is opt-in per tool, and the first write to a file shows a diff and
// asks. Removing a block restores the file it was written into; a file this
// package created is removed again when it held only its own block.
package memorysync

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
)

// StartMarker and EndMarker delimit the block. The person's own text outside
// them is never changed, and the memory text is scrubbed of both markers before
// it is written, so a memory item cannot open or close a block.
const (
	StartMarker = "<!-- openvibe-memory:start -->"
	EndMarker   = "<!-- openvibe-memory:end -->"
)

// The block stays small enough for a tool's context: at most this many project
// rules, each this long, and this many characters in all. The profile itself is
// already capped by the store; the note at the cut says where the rest is.
const (
	MaxRules      = 40
	MaxRuleRunes  = 500
	MaxBlockRunes = 12000
)

// Root says where a target file lives.
type Root int

const (
	// RootHome is the person's home directory.
	RootHome Root = iota
	// RootProject is a project directory the person gave.
	RootProject
)

// Target is one file of one tool that receives a block.
type Target struct {
	// Tool is the stable id of the tool: agents, claude-code, gemini, cursor,
	// copilot.
	Tool string
	// Name is how the tool is called.
	Name string
	// Root is where the file lives.
	Root Root
	// Rel is the file under that root, with forward slashes.
	Rel string
	// Import, when set, makes the block a thin import of the file the tool's
	// main block is written into, instead of a copy of the memory. It is given
	// the person's home directory so a home-level import can be absolute.
	Import func(home string) string
	// Frontmatter is written above the block when this package creates the
	// file. It is never added to a file that already exists.
	Frontmatter string
	// Create says the file may not exist yet and this package may create it.
	Create bool
}

// Targets returns the files the sync writes, in a fixed order.
func Targets() []Target {
	return []Target{
		{Tool: "agents", Name: "AGENTS.md (Codex and friends)", Root: RootHome, Rel: ".codex/AGENTS.md", Create: true},
		{Tool: "agents", Name: "AGENTS.md (Codex and friends)", Root: RootProject, Rel: "AGENTS.md", Create: true},
		{Tool: "claude-code", Name: "Claude Code", Root: RootHome, Rel: ".claude/CLAUDE.md", Create: true,
			Import: func(home string) string { return "@" + filepath.Join(home, ".codex", "AGENTS.md") }},
		{Tool: "claude-code", Name: "Claude Code", Root: RootProject, Rel: "CLAUDE.md", Create: true,
			Import: func(string) string { return "@AGENTS.md" }},
		{Tool: "gemini", Name: "Gemini CLI", Root: RootHome, Rel: ".gemini/GEMINI.md", Create: true},
		{Tool: "gemini", Name: "Gemini CLI", Root: RootProject, Rel: "GEMINI.md", Create: true},
		{Tool: "cursor", Name: "Cursor", Root: RootProject, Rel: ".cursor/rules/openvibe-memory.mdc", Create: true,
			Frontmatter: "---\ndescription: OpenVibe Memory: your profile and this project's rules\nalwaysApply: true\n---\n"},
		{Tool: "copilot", Name: "GitHub Copilot", Root: RootProject, Rel: ".github/copilot-instructions.md", Create: true},
	}
}

// ToolIDs are the tools a person may turn on.
func ToolIDs() []string { return []string{"agents", "claude-code", "gemini", "cursor", "copilot"} }

// KnownTool says whether id names a tool this package writes.
func KnownTool(id string) bool {
	for _, tool := range ToolIDs() {
		if tool == id {
			return true
		}
	}
	return false
}

// Content is what a block says: the profile of one scope and the project rules
// of that scope.
type Content struct {
	// Scope is `user` or `project:<slug>`.
	Scope string
	// Profile is the cached profile of that scope, already built by the store.
	Profile string
	// Rules are the active project_rule items of that scope.
	Rules []string
}

// Body renders the text between the markers, or "" when there is nothing to
// write.
func Body(content Content) string {
	var b strings.Builder
	text := clean(content.Profile)
	rules := cleanRules(content.Rules)
	if text == "" && len(rules) == 0 {
		return ""
	}
	b.WriteString("<!-- Managed by OpenVibe Link from your OpenVibe Memory. Edit the memory in OpenVibe, not in this block. This file may be shared or committed. -->\n")
	if text != "" {
		if isProject(content.Scope) {
			b.WriteString("\n## About this project\n\n")
		} else {
			b.WriteString("\n## About you\n\n")
		}
		b.WriteString(text)
		b.WriteString("\n")
	}
	if len(rules) > 0 {
		b.WriteString("\n## Project rules\n\n")
		for _, rule := range rules {
			b.WriteString("- " + rule + "\n")
		}
	}
	body := strings.TrimRight(b.String(), "\n")
	if len([]rune(body)) > MaxBlockRunes {
		cut := string([]rune(body)[:MaxBlockRunes])
		if i := strings.LastIndex(cut, "\n"); i > 0 {
			cut = cut[:i]
		}
		body = cut + "\n\n_More notes are in your OpenVibe Memory; this block is shortened to fit._"
	}
	return strings.TrimRight(body, "\n")
}

// Block puts the markers around a body. An empty body means the block is not
// written (and an old one is removed).
func Block(body string) string {
	body = strings.TrimRight(Sanitize(body), "\n")
	if body == "" {
		return ""
	}
	return StartMarker + "\n" + body + "\n" + EndMarker + "\n"
}

// ImportBody is the thin block a tool that reads AGENTS.md gets.
func ImportBody(importLine string) string {
	importLine = strings.TrimSpace(importLine)
	if importLine == "" {
		return ""
	}
	return "<!-- Your OpenVibe Memory is kept in the AGENTS.md file this imports. Edit it in OpenVibe, not here. -->\n\n" + importLine
}

// Sanitize keeps the memory text from opening or closing a block.
func Sanitize(text string) string {
	text = strings.ReplaceAll(text, StartMarker, "openvibe-memory start")
	text = strings.ReplaceAll(text, EndMarker, "openvibe-memory end")
	return strings.ReplaceAll(text, "openvibe-memory:", "openvibe memory:")
}

func clean(text string) string {
	return strings.TrimSpace(Sanitize(text))
}

func cleanRules(rules []string) []string {
	out := make([]string, 0, len(rules))
	seen := map[string]bool{}
	for _, rule := range rules {
		rule = strings.Join(strings.Fields(Sanitize(rule)), " ")
		if len([]rune(rule)) > MaxRuleRunes {
			rule = string([]rune(rule)[:MaxRuleRunes]) + "…"
		}
		if rule == "" || seen[rule] {
			continue
		}
		seen[rule] = true
		out = append(out, rule)
		if len(out) == MaxRules {
			break
		}
	}
	return out
}

func isProject(scope string) bool { return strings.HasPrefix(scope, "project:") }

// find gives the block's bytes and where they are. It refuses a file with two
// blocks or half a block: rewriting one of them could hide the person's text.
func find(existing []byte) (start, end int, found bool, err error) {
	start = bytes.Index(existing, []byte(StartMarker))
	end = bytes.Index(existing, []byte(EndMarker))
	switch {
	case start < 0 && end < 0:
		return 0, 0, false, nil
	case start < 0 || end < 0:
		return 0, 0, false, fmt.Errorf("the file holds half an OpenVibe Memory block (a marker without its pair); leaving it alone")
	case end < start:
		return 0, 0, false, fmt.Errorf("the OpenVibe Memory end marker comes before its start; leaving the file alone")
	}
	// The block ends after the end marker and its line ending.
	after := end + len(EndMarker)
	if after < len(existing) && existing[after] == '\r' {
		after++
	}
	if after < len(existing) && existing[after] == '\n' {
		after++
	}
	if bytes.Contains(existing[after:], []byte(StartMarker)) || bytes.Contains(existing[after:], []byte(EndMarker)) {
		return 0, 0, false, fmt.Errorf("the file holds more than one OpenVibe Memory block; leaving it alone")
	}
	return start, after, true, nil
}

// Replace writes a block into a file: it replaces the one already there,
// appends a new one, or leaves an empty block out. It gives back the file, the
// separator it added before the block (for an exact undo), and whether the
// bytes changed.
func Replace(existing []byte, block string) (out []byte, sep string, changed bool, err error) {
	start, end, found, err := find(existing)
	if err != nil {
		return nil, "", false, err
	}
	if block == "" {
		return existing, "", false, fmt.Errorf("an empty block is not written; call Remove to take one out")
	}
	if found {
		out = append(append(append([]byte{}, existing[:start]...), []byte(block)...), existing[end:]...)
		return out, "", !bytes.Equal(out, existing), nil
	}
	var b bytes.Buffer
	b.Write(existing)
	switch {
	case len(existing) == 0:
	case bytes.HasSuffix(existing, []byte("\n\n")):
	case bytes.HasSuffix(existing, []byte("\n")):
		sep = "\n"
	default:
		sep = "\n\n"
	}
	b.WriteString(sep)
	b.WriteString(block)
	return b.Bytes(), sep, true, nil
}

// Remove takes the block out of a file and gives back the file, whether a block
// was there, and the old separator it also removed. The rest of the file is
// byte-identical.
func Remove(existing []byte, sep string) (out []byte, found bool, err error) {
	start, end, found, err := find(existing)
	if err != nil || !found {
		return existing, false, err
	}
	out, _ = removeAt(existing, start, end)
	if sep != "" && bytes.HasSuffix(out[:start], []byte(sep)) {
		out = append(append([]byte{}, out[:start-len(sep)]...), out[start:]...)
	}
	return out, true, nil
}

func removeAt(existing []byte, start, end int) ([]byte, string) {
	out := append(append([]byte{}, existing[:start]...), existing[end:]...)
	return out, ""
}

// OnlyOurContent says whether a file holds nothing but the frontmatter this
// package writes and whitespace. Such a file is removed on undo.
func OnlyOurContent(data []byte, frontmatter string) bool {
	text := strings.TrimSpace(string(data))
	if frontmatter != "" {
		text = strings.TrimSpace(strings.TrimPrefix(text, strings.TrimSpace(frontmatter)))
	}
	return text == ""
}

// TargetFor finds the target a file belongs to, by the end of its path. It is
// used on undo, where the state has the absolute path but the project may not
// be named again on the command line.
func TargetFor(path string) (Target, bool) {
	slash := filepath.ToSlash(path)
	for _, target := range Targets() {
		if slash == target.Rel || strings.HasSuffix(slash, "/"+target.Rel) {
			return target, true
		}
	}
	return Target{}, false
}

// Diff is a small line diff for the person to read: three lines of context
// around the part that changed. It is not a patch to apply; it is the gate
// before the first write.
func Diff(path string, before, after []byte) string {
	oldLines := lines(before)
	newLines := lines(after)
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}
	context := 3
	from := prefix - context
	if from < 0 {
		from = 0
	}
	to := prefix
	oldEnd := len(oldLines) - suffix
	newEnd := len(newLines) - suffix
	oldTail := oldEnd + context
	if oldTail > len(oldLines) {
		oldTail = len(oldLines)
	}
	newTail := newEnd + context
	if newTail > len(newLines) {
		newTail = len(newLines)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n+++ %s\n", path, path)
	fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", from+1, oldTail-from, from+1, newTail-from)
	for _, line := range oldLines[from:to] {
		b.WriteString(" " + line + "\n")
	}
	for _, line := range oldLines[to:oldEnd] {
		b.WriteString("-" + line + "\n")
	}
	for _, line := range newLines[to:newEnd] {
		b.WriteString("+" + line + "\n")
	}
	oldCtx := oldLines[oldEnd:oldTail]
	newCtx := newLines[newEnd:newTail]
	n := len(oldCtx)
	if len(newCtx) < n {
		n = len(newCtx)
	}
	for i := 0; i < n; i++ {
		b.WriteString(" " + oldCtx[i] + "\n")
	}
	return b.String()
}

// lines splits a file on newlines and keeps no phantom line after the last
// newline, so a diff is about the text and not about the ending.
func lines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	text := strings.TrimSuffix(string(data), "\n")
	return strings.Split(text, "\n")
}
