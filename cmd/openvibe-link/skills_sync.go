package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/OpenVibers/OpenVibe.Link/internal/config"
	"github.com/OpenVibers/OpenVibe.Link/internal/memoryupload"
	"github.com/OpenVibers/OpenVibe.Link/internal/skillsync"
)

// skillsSyncUsage is what "openvibe-link skills sync" prints.
const skillsSyncUsage = "openvibe-link skills sync [--tools a,b] [--project DIR …] [--dry-run] [--yes]\n" +
	"openvibe-link skills sync --disable a,b\n" +
	"openvibe-link skills sync --undo\n" +
	"\n" +
	"  Writes the skills and rules you chose in the OpenVibe skills hub\n" +
	"  (openvibe.codes/skills) into the folders each coding tool reads:\n" +
	"  ~/.claude/skills, ~/.codex/skills, ~/.config/opencode/skills,\n" +
	"  ~/.commandcode/skills, and each tool's CLAUDE.md/AGENTS.md/CONVENTIONS.md.\n" +
	"  It writes only inside its own marked block; your own text in those files\n" +
	"  is never changed. An existing SKILL.md keeps its frontmatter.\n" +
	"\n" +
	"  Every tool is off until you turn it on. The first write to a file shows\n" +
	"  the diff and asks. --disable takes the blocks out again and turns the tool\n" +
	"  off; --undo does that for every tool that is on.\n" +
	"\n" +
	"  --tools a,b       turn these tools on: claude-code, codex, opencode,\n" +
	"                    command-code, aider (with no --tools, the tools already on sync)\n" +
	"  --disable a,b     turn these tools off and remove their blocks\n" +
	"  --undo            remove every block this command wrote and turn the tools off\n" +
	"  --project DIR     write this project's skills and rules as well; may be given more than once\n" +
	"  --dry-run         show the diffs and write nothing\n" +
	"  --yes             do not ask on the first write\n" +
	"  --state FILE      the opt-in file (default: the Node's state directory)\n" +
	"  --endpoint URL    the skills hub (default " + skillsync.DefaultEndpoint + ")\n" +
	"  --token-env NAME  the variable the access token is read from (default " + skillsync.DefaultTokenEnv + ")\n" +
	"  --user-home DIR   use this directory as the person's home (default: yours)\n"

// cmdSkills runs the skills commands.
func cmdSkills(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, skillsSyncUsage)
		return 2
	}
	switch args[0] {
	case "sync":
		return cmdSkillsSync(args[1:], stdin, stdout, stderr)
	case "on", "off", "status":
		return cmdSkillsRunner(args, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown skills command %q\n\n", args[0])
		fmt.Fprint(stderr, skillsSyncUsage)
		return 2
	}
}

func cmdSkillsSync(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("skills sync", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, skillsSyncUsage) }
	var tools stringList
	var disable stringList
	var projectFlags stringList
	fs.Var(&tools, "tools", "turn these tools on")
	fs.Var(&disable, "disable", "turn these tools off and remove their blocks")
	fs.Var(&projectFlags, "project", "write this project as well")
	dryRun := fs.Bool("dry-run", false, "show the diffs and write nothing")
	assumeYes := fs.Bool("yes", false, "do not ask on the first write")
	undo := fs.Bool("undo", false, "remove every block and turn the tools off")
	stateFile := fs.String("state", "", "the opt-in file")
	endpoint := fs.String("endpoint", skillsync.DefaultEndpoint, "the skills hub")
	tokenEnv := fs.String("token-env", skillsync.DefaultTokenEnv, "the variable the access token is read from")
	userHome := fs.String("user-home", "", "use this directory as the person's home")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unknown argument %q\n\n", fs.Arg(0))
		fmt.Fprint(stderr, skillsSyncUsage)
		return 2
	}
	for _, id := range append(append([]string{}, tools...), disable...) {
		if !skillsync.KnownTool(id) {
			fmt.Fprintf(stderr, "error: no such tool %q. The tools are: %s\n", id, strings.Join(skillsync.ToolIDs(), ", "))
			return 2
		}
	}
	if *undo && len(tools) > 0 {
		fmt.Fprintln(stderr, "error: --undo and --tools are not used together; --undo removes every block")
		return 2
	}
	if len(tools) > 0 && len(disable) > 0 {
		fmt.Fprintln(stderr, "error: --tools and --disable are not used together; run them in two steps")
		return 2
	}
	home, code := memoryHome(*userHome, stderr)
	if code != 0 {
		return code
	}
	statePath := *stateFile
	if statePath == "" {
		statePath = filepath.Join(config.DefaultPaths("").StateDir, "skills-sync.json")
	}
	state, err := skillsync.LoadState(statePath)
	if err != nil {
		fmt.Fprintf(stderr, "error: the sync state %s cannot be read: %v\n", statePath, err)
		return 1
	}
	projects, err := absProjects(projectFlags)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}

	if *undo || len(disable) > 0 {
		turnOff := disable
		if *undo {
			turnOff = state.EnabledTools()
			if len(turnOff) == 0 {
				fmt.Fprintln(stdout, "No tool is on, so there is no block to remove.")
				return 0
			}
		}
		return skillsRemove(home, projects, state, statePath, turnOff, *dryRun, stdout, stderr)
	}

	on := state.EnabledTools()
	if len(tools) > 0 {
		on = append([]string{}, tools...)
	}
	if len(on) == 0 {
		fmt.Fprintln(stdout, "No tool is on: nothing was written.")
		fmt.Fprintf(stdout, "Turn one on with --tools (%s). The first write shows its diff and asks.\n", strings.Join(skillsync.ToolIDs(), ", "))
		return 0
	}
	sort.Strings(on)

	person, byProject, err := fetchSkills(*endpoint, *tokenEnv, projects)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return skillsWrite(home, projects, person, byProject, state, statePath, on, *dryRun, *assumeYes, stdin, stdout, stderr)
}

// fetchSkills reads the person's own picks and each project's picks. It returns
// the person's manifest and one manifest per project directory.
func fetchSkills(endpoint, tokenEnv string, projects []string) ([]skillsync.Skill, map[string][]skillsync.Skill, error) {
	token := strings.TrimSpace(os.Getenv(tokenEnv))
	if token == "" {
		return nil, nil, fmt.Errorf("%s is not set: it must hold your OpenVibe access token (sign in at openvibe.codes and copy it from the browser's cookies)", tokenEnv)
	}
	client, err := skillsync.NewClient(endpoint, token)
	if err != nil {
		return nil, nil, err
	}
	ctx := context.Background()
	person, err := client.Manifest(ctx, "")
	if err != nil {
		return nil, nil, err
	}
	byProject := map[string][]skillsync.Skill{}
	for _, project := range projects {
		slug := memoryupload.Slug(project)
		if slug == "" {
			return nil, nil, fmt.Errorf("the folder %s has no name the hub can use", project)
		}
		found, err := client.Manifest(ctx, slug)
		if err != nil {
			return nil, nil, err
		}
		byProject[project] = found
	}
	return person, byProject, nil
}

// skillFile is one file the sync is about to look at.
type skillFile struct {
	target      skillsync.Target
	path        string
	kind        string // "skill" or "rules"
	slug        string
	frontmatter string
	block       string
	tool        string
}

// planFiles gives every file of the given tools for the home and the projects.
// A file several tools share (a project's AGENTS.md is read by Codex, OpenCode
// and Command Code) is planned once, with the rules that apply to any of them.
func planFiles(home string, projects []string, person []skillsync.Skill,
	byProject map[string][]skillsync.Skill, tools []string) []skillFile {
	out := []skillFile{}
	seenRules := map[string]bool{}
	for _, id := range tools {
		for _, target := range skillsync.TargetsFor(id) {
			if target.Root != skillsync.RootHome {
				continue
			}
			out = append(out, planFor(target, home, person, id, []string{id}, seenRules)...)
		}
	}
	for _, project := range projects {
		// Which enabled tools write this project's rules file: their rule
		// choices all belong in the one file.
		ruleTools := map[string][]string{}
		for _, id := range tools {
			for _, target := range skillsync.TargetsFor(id) {
				if target.Root != skillsync.RootProject || target.RulesFile == "" {
					continue
				}
				key := filepath.Join(project, filepath.FromSlash(target.RulesFile))
				ruleTools[key] = append(ruleTools[key], id)
			}
		}
		for _, id := range tools {
			for _, target := range skillsync.TargetsFor(id) {
				if target.Root != skillsync.RootProject {
					continue
				}
				applicable := []string{id}
				if target.RulesFile != "" {
					key := filepath.Join(project, filepath.FromSlash(target.RulesFile))
					if list := ruleTools[key]; len(list) > 0 {
						applicable = list
					}
				}
				out = append(out, planFor(target, project, byProject[project], id, applicable, seenRules)...)
			}
		}
	}
	return out
}

func planFor(target skillsync.Target, root string, manifest []skillsync.Skill, tool string,
	ruleTools []string, seenRules map[string]bool) []skillFile {
	out := []skillFile{}
	rules := []string{}
	for _, skill := range manifest {
		switch skill.Kind {
		case "skill":
			if !skill.AppliesTo(tool) || target.SkillsDir == "" {
				continue
			}
			block := skillsync.Block(skill)
			if block == "" {
				continue
			}
			out = append(out, skillFile{
				target: target, tool: tool, kind: "skill", slug: skill.Slug,
				path:        filepath.Join(root, filepath.FromSlash(target.SkillsDir), skill.Slug, "SKILL.md"),
				frontmatter: skillsync.Frontmatter(skill),
				block:       block,
			})
		case "rule":
			if target.RulesFile == "" || !skill.AppliesToAny(ruleTools) {
				continue
			}
			rules = append(rules, skill.Body)
		}
	}
	if target.RulesFile != "" {
		key := filepath.Join(root, filepath.FromSlash(target.RulesFile))
		if seenRules[key] {
			return out
		}
		seenRules[key] = true
		out = append(out, skillFile{
			target: target, tool: tool, kind: "rules",
			path:  key,
			block: skillsync.RulesBlock(rules),
		})
	}
	return out
}

// skillsWrite shows the diff of every changed file and writes it inside the
// marked block, after the first-write gate.
func skillsWrite(home string, projects []string, person []skillsync.Skill,
	byProject map[string][]skillsync.Skill, state *skillsync.State, statePath string, tools []string,
	dryRun, assumeYes bool, stdin io.Reader, stdout, stderr io.Writer) int {
	fmt.Fprintf(stdout, "Skills sync of %s\n", home)
	if len(projects) == 0 {
		fmt.Fprintln(stdout, "No project given: only the user-level skill and rule folders are written.")
	} else {
		fmt.Fprintf(stdout, "Projects: %s\n", strings.Join(projects, ", "))
	}
	fmt.Fprintf(stdout, "Tools on: %s\n", strings.Join(tools, ", "))
	planned := planFiles(home, projects, person, byProject, tools)
	byTool := map[string][]skillFile{}
	for _, p := range planned {
		byTool[p.tool] = append(byTool[p.tool], p)
	}

	written, skipped, unchanged, removed := 0, 0, 0, 0
	reader := bufio.NewReader(stdin)
	paths := map[string]bool{}
	for _, p := range planned {
		paths[p.path] = true
	}
	for _, id := range tools {
		// Files this tool wrote before but the manifest no longer names are stale:
		// take their block out here, before the new files are written.
		if t := state.Tools[id]; t != nil {
			for path := range t.Files {
				if paths[path] {
					continue
				}
				n, err := removeBlockFile(home, state, id, path, dryRun, stdout)
				if err != nil {
					fmt.Fprintf(stderr, "error: %v\n", err)
					skipped++
					continue
				}
				if n {
					removed++
				}
			}
		}

		for _, p := range byTool[id] {
			existing, existed, err := readFile(p.path)
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				skipped++
				continue
			}
			fileState := state.File(id, p.path)
			var next []byte
			var sep string
			if p.block == "" {
				next, _, err = skillsync.Remove(existing, fileState.Sep)
				if err != nil {
					fmt.Fprintf(stderr, "error: %s: %v\n", displayPath(home, p.path), err)
					skipped++
					continue
				}
				if !existed {
					// Nothing to remove: do not create an empty rules file.
					if fileState.Sep != "" || fileState.Created {
						delete(state.Tool(id).Files, p.path)
					}
					continue
				}
			} else {
				if !existed {
					existing = []byte(p.frontmatter)
				}
				next, sep, _, err = skillsync.Replace(existing, p.block)
				if err != nil {
					fmt.Fprintf(stderr, "error: %s: %v\n", displayPath(home, p.path), err)
					skipped++
					continue
				}
			}
			if string(next) == string(existing) {
				unchanged++
				continue
			}
			fmt.Fprintf(stdout, "\n%s\n", displayPath(home, p.path))
			if p.target.Root == skillsync.RootProject {
				fmt.Fprintln(stdout, "  (this is a project file: it may be shared or committed)")
			}
			fmt.Fprint(stdout, skillsync.Diff(displayPath(home, p.path), existing, next))
			if dryRun {
				skipped++
				continue
			}
			if !fileState.Confirmed {
				if !assumeYes && !ask(reader, stdout, "Write this file for the first time? [y/N] ") {
					fmt.Fprintln(stdout, "Not written: no confirmation.")
					skipped++
					continue
				}
				fileState.Confirmed = true
			}
			if err := writeFile(p.path, next); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				return 1
			}
			fileState.Kind = p.kind
			fileState.Slug = p.slug
			fileState.Frontmatter = p.frontmatter
			if !existed {
				fileState.Created = true
			}
			if sep != "" {
				fileState.Sep = sep
			}
			if err := state.Save(statePath); err != nil {
				fmt.Fprintf(stderr, "error: the sync state %s cannot be saved: %v\n", statePath, err)
				return 1
			}
			fmt.Fprintln(stdout, "Written, inside the OpenVibe skills block.")
			written++
		}
	}

	if !dryRun {
		for _, id := range tools {
			state.Tool(id).Enabled = true
		}
		if err := state.Save(statePath); err != nil {
			fmt.Fprintf(stderr, "error: the sync state %s cannot be saved: %v\n", statePath, err)
			return 1
		}
	}
	fmt.Fprintf(stdout, "\n%d file(s) written, %d unchanged, %d not written, %d stale block(s) removed.\n", written, unchanged, skipped, removed)
	if dryRun {
		fmt.Fprintln(stdout, "Nothing was written: this was a dry run.")
	}
	return 0
}

// removeBlockFile takes one file's block out, removes the file when the Node
// created it and nothing else is left, and forgets it in the state.
func removeBlockFile(home string, state *skillsync.State, tool, path string, dryRun bool, stdout io.Writer) (bool, error) {
	existing, existed, err := readFile(path)
	if err != nil || !existed {
		return false, err
	}
	fileState := state.FileStateOf(tool, path)
	sep, created, frontmatter := "", false, ""
	if fileState != nil {
		sep, created, frontmatter = fileState.Sep, fileState.Created, fileState.Frontmatter
	}
	next, found, err := skillsync.Remove(existing, sep)
	if err != nil || !found {
		return false, err
	}
	fmt.Fprintf(stdout, "\n%s: removed the OpenVibe skills block\n", displayPath(home, path))
	if dryRun {
		return true, nil
	}
	if created && skillsync.OnlyOurContent(next, frontmatter) {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		// A skill folder the Node made is removed too when it is empty.
		_ = os.Remove(filepath.Dir(path))
		fmt.Fprintf(stdout, "%s: removed (the Node created it)\n", displayPath(home, path))
	} else if err := writeFile(path, next); err != nil {
		return false, err
	}
	delete(state.Tool(tool).Files, path)
	return true, nil
}

// skillsRemove takes the block out of every file of the given tools and turns
// them off.
func skillsRemove(home string, projects []string, state *skillsync.State, statePath string, tools []string,
	dryRun bool, stdout, stderr io.Writer) int {
	fmt.Fprintf(stdout, "Skills sync undo of %s\n", home)
	removed, missing, failed := 0, 0, 0
	for _, id := range tools {
		paths := map[string]bool{}
		if t := state.Tools[id]; t != nil {
			for path := range t.Files {
				paths[path] = true
			}
		}
		// Files that match a target but are not in the state are still tried:
		// the state is a convenience, not the permission.
		for _, target := range skillsync.TargetsFor(id) {
			if target.Root == skillsync.RootHome {
				if target.RulesFile != "" {
					paths[filepath.Join(home, filepath.FromSlash(target.RulesFile))] = true
				}
				continue
			}
			for _, project := range projects {
				if target.RulesFile != "" {
					paths[filepath.Join(project, filepath.FromSlash(target.RulesFile))] = true
				}
			}
		}
		for path := range paths {
			n, err := removeBlockFile(home, state, id, path, dryRun, stdout)
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				failed++
				continue
			}
			if n {
				removed++
			} else {
				missing++
			}
		}
		if !dryRun {
			state.Tool(id).Enabled = false
		}
	}
	if !dryRun {
		if err := state.Save(statePath); err != nil {
			fmt.Fprintf(stderr, "error: the sync state %s cannot be saved: %v\n", statePath, err)
			return 1
		}
	}
	fmt.Fprintf(stdout, "\n%d block(s) removed, %d file(s) had none or were not there, %d failed.\n", removed, missing, failed)
	if dryRun {
		fmt.Fprintln(stdout, "Nothing was changed: this was a dry run.")
	}
	if failed > 0 {
		return 1
	}
	return 0
}
