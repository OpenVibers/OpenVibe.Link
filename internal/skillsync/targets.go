// Package skillsync writes the skills and rules a person chose in OpenVibe's
// skills hub into the folders each coding tool already reads: Claude Code,
// Codex, OpenCode and Command Code, with Aider's project conventions file.
//
// It writes only inside one clearly marked block, and it never touches the
// person's hand-written text outside that block:
//
//	<!-- openvibe-skills:start -->
//	...
//	<!-- openvibe-skills:end -->
//
// A skill's SKILL.md keeps its frontmatter above the block, because the tools
// read it there; the frontmatter is only written when the Node creates the
// file, and never replaced in a file the person already had. Every file is
// opt-in per tool, the first write shows a diff and asks, and undo puts the
// file back the way it was (or removes it when it held only the Node's block).
package skillsync

import (
	"path/filepath"
	"strings"
)

// StartMarker and EndMarker delimit the block. Both are scrubbed from
// everything written inside it, so content cannot open or close a block.
const (
	StartMarker = "<!-- openvibe-skills:start -->"
	EndMarker   = "<!-- openvibe-skills:end -->"
)

// MaxBodyRunes bounds one skill's body and one rules file, so a huge record
// cannot freeze a tool reading its own instructions.
const (
	MaxBodyRunes  = 131072
	MaxRules      = 200
	MaxRuleRunes  = 2000
	MaxBlockRunes = 200000
)

// Root says where a target lives.
type Root int

const (
	// RootHome is the person's home directory.
	RootHome Root = iota
	// RootProject is a project directory the person gave.
	RootProject
)

// String is "home" or "project", for messages and tests.
func (r Root) String() string {
	if r == RootProject {
		return "project"
	}
	return "home"
}

// Target is one tool's folders: where its skills go and which file its rules
// are written into.
type Target struct {
	// Tool is the stable id of the tool: claude-code, codex, opencode,
	// command-code, aider.
	Tool string
	// Name is how the tool is called.
	Name string
	// Root is where the folders live.
	Root Root
	// SkillsDir is the folder under that root holding one directory per skill,
	// with forward slashes. Empty means the tool has no skills folder.
	SkillsDir string
	// RulesFile is the file under that root that receives one rules block,
	// with forward slashes. Empty means the tool has no rules file.
	RulesFile string
}

// Targets returns the files the sync may write, in a fixed order.
func Targets() []Target {
	return []Target{
		{Tool: "claude-code", Name: "Claude Code", Root: RootHome, SkillsDir: ".claude/skills", RulesFile: ".claude/CLAUDE.md"},
		{Tool: "claude-code", Name: "Claude Code", Root: RootProject, SkillsDir: ".claude/skills", RulesFile: "CLAUDE.md"},
		{Tool: "codex", Name: "Codex", Root: RootHome, SkillsDir: ".codex/skills", RulesFile: ".codex/AGENTS.md"},
		{Tool: "codex", Name: "Codex", Root: RootProject, SkillsDir: ".codex/skills", RulesFile: "AGENTS.md"},
		{Tool: "opencode", Name: "OpenCode", Root: RootHome, SkillsDir: ".config/opencode/skills", RulesFile: ".config/opencode/AGENTS.md"},
		{Tool: "opencode", Name: "OpenCode", Root: RootProject, SkillsDir: ".opencode/skills", RulesFile: "AGENTS.md"},
		{Tool: "command-code", Name: "Command Code", Root: RootHome, SkillsDir: ".commandcode/skills", RulesFile: ".commandcode/AGENTS.md"},
		{Tool: "command-code", Name: "Command Code", Root: RootProject, SkillsDir: ".commandcode/skills", RulesFile: "AGENTS.md"},
		// Aider has no skills folder; its project instructions are CONVENTIONS.md.
		{Tool: "aider", Name: "Aider", Root: RootProject, RulesFile: "CONVENTIONS.md"},
	}
}

// ToolIDs are the tools a person may turn on.
func ToolIDs() []string { return []string{"claude-code", "codex", "opencode", "command-code", "aider"} }

// KnownTool says whether id names a tool this package writes.
func KnownTool(id string) bool {
	for _, tool := range ToolIDs() {
		if tool == id {
			return true
		}
	}
	return false
}

// TargetsFor gives one tool's targets, home first.
func TargetsFor(id string) []Target {
	out := []Target{}
	for _, t := range Targets() {
		if t.Tool == id {
			out = append(out, t)
		}
	}
	return out
}

// TargetForPath finds the target a file belongs to, by the end of its path. It
// is used on undo, where the state has the absolute path but the project may
// not be named again on the command line.
func TargetForPath(path string) (Target, bool) {
	slash := filepath.ToSlash(path)
	for _, t := range Targets() {
		if t.RulesFile != "" && (slash == t.RulesFile || strings.HasSuffix(slash, "/"+t.RulesFile)) {
			return t, true
		}
		if t.SkillsDir != "" && (strings.HasPrefix(slash, t.SkillsDir+"/") || strings.Contains(slash, "/"+t.SkillsDir+"/")) {
			return t, true
		}
	}
	return Target{}, false
}
