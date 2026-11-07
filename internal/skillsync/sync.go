package skillsync

import (
	"bytes"
	"fmt"
	"strings"
)

// Skill is one record the Node receives from the hub. Only Body, Summary,
// Slug and Title are written; the rest tells the person what was chosen.
type Skill struct {
	ID        string   `json:"id"`
	Slug      string   `json:"slug"`
	Kind      string   `json:"kind"`
	Title     string   `json:"title"`
	Summary   string   `json:"summary"`
	Audience  string   `json:"audience"`
	Tools     []string `json:"tools"`
	Version   int      `json:"version"`
	Body      string   `json:"body"`
	UpdatedAt string   `json:"updated_at"`
}

// AppliesTo says whether a record is written for this tool: an empty tools
// list means every tool the Node knows.
func (s Skill) AppliesTo(tool string) bool { return s.AppliesToAny([]string{tool}) }

// AppliesToAny is AppliesTo for a file several tools share (a project's
// AGENTS.md is read by Codex, OpenCode and Command Code): an empty tools list
// or any one of them means the record belongs in that file.
func (s Skill) AppliesToAny(tools []string) bool {
	if len(s.Tools) == 0 {
		return true
	}
	for _, t := range s.Tools {
		for _, want := range tools {
			if t == want {
				return true
			}
		}
	}
	return false
}

// Frontmatter is the header a SKILL.md needs for the tool to find the skill.
// It is written only when the Node creates the file; an existing file keeps
// its own frontmatter untouched.
func Frontmatter(s Skill) string {
	description := strings.TrimSpace(s.Summary)
	if description == "" {
		description = strings.TrimSpace(s.Title)
	}
	if description == "" {
		description = s.Slug
	}
	return "---\nname: " + yamlValue(s.Slug) + "\ndescription: " + yamlValue(description) + "\n---\n"
}

// yamlValue makes one YAML double-quoted scalar. A body is never put here and
// a newline cannot escape a quoted scalar.
func yamlValue(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return "\"" + s + "\""
}

// BodyOf strips a frontmatter block the body already carried and the markers,
// leaving exactly what the block may hold.
func BodyOf(s Skill) string {
	body := strings.TrimSpace(s.Body)
	if strings.HasPrefix(body, "---\n") {
		rest := body[4:]
		if i := strings.Index(rest, "\n---"); i >= 0 {
			after := rest[i+4:]
			body = strings.TrimPrefix(after, "\n")
		}
	}
	body = Sanitize(strings.TrimSpace(body))
	if len([]rune(body)) > MaxBodyRunes {
		cut := string([]rune(body)[:MaxBodyRunes])
		if i := strings.LastIndex(cut, "\n"); i > 0 {
			cut = cut[:i]
		}
		body = cut + "\n\n_This skill is longer in OpenVibe; the block is shortened to fit._"
	}
	return body
}

// Block puts the markers around one skill's body. An empty body means the
// block is not written (and an old one is removed).
func Block(s Skill) string {
	body := BodyOf(s)
	if body == "" {
		return ""
	}
	return StartMarker + "\n" + body + "\n" + EndMarker + "\n"
}

// RulesBlock is the one block a tool's rules file receives: every chosen rule,
// one bullet each, in a stable order.
func RulesBlock(rules []string) string {
	clean := cleanRules(rules)
	if len(clean) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<!-- Managed by OpenVibe Node from your OpenVibe skills hub. Edit the rules in OpenVibe, not in this block. -->\n")
	for _, rule := range clean {
		b.WriteString("- " + rule + "\n")
	}
	return StartMarker + "\n" + strings.TrimRight(b.String(), "\n") + "\n" + EndMarker + "\n"
}

// Sanitize keeps content from opening or closing a block.
func Sanitize(text string) string {
	text = strings.ReplaceAll(text, StartMarker, "openvibe-skills start")
	text = strings.ReplaceAll(text, EndMarker, "openvibe-skills end")
	return strings.ReplaceAll(text, "openvibe-skills:", "openvibe skills")
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

// find gives the block's bytes and where they are. It refuses a file with two
// blocks or half a block: rewriting one of them could hide the person's text.
func find(existing []byte) (start, end int, found bool, err error) {
	start = bytes.Index(existing, []byte(StartMarker))
	end = bytes.Index(existing, []byte(EndMarker))
	switch {
	case start < 0 && end < 0:
		return 0, 0, false, nil
	case start < 0 || end < 0:
		return 0, 0, false, fmt.Errorf("the file holds half an OpenVibe skills block (a marker without its pair); leaving it alone")
	case end < start:
		return 0, 0, false, fmt.Errorf("the OpenVibe skills end marker comes before its start; leaving the file alone")
	}
	after := end + len(EndMarker)
	if after < len(existing) && existing[after] == '\r' {
		after++
	}
	if after < len(existing) && existing[after] == '\n' {
		after++
	}
	if bytes.Contains(existing[after:], []byte(StartMarker)) || bytes.Contains(existing[after:], []byte(EndMarker)) {
		return 0, 0, false, fmt.Errorf("the file holds more than one OpenVibe skills block; leaving it alone")
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
	out = append(append([]byte{}, existing[:start]...), existing[end:]...)
	if sep != "" && bytes.HasSuffix(out[:start], []byte(sep)) {
		out = append(append([]byte{}, out[:start-len(sep)]...), out[start:]...)
	}
	return out, true, nil
}

// OnlyOurContent says whether a file holds nothing but the frontmatter this
// package wrote and whitespace. Such a file is removed on undo.
func OnlyOurContent(data []byte, frontmatter string) bool {
	text := strings.TrimSpace(string(data))
	if frontmatter != "" {
		text = strings.TrimSpace(strings.TrimPrefix(text, strings.TrimSpace(frontmatter)))
	}
	return text == ""
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

func lines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	text := strings.TrimSuffix(string(data), "\n")
	return strings.Split(text, "\n")
}
