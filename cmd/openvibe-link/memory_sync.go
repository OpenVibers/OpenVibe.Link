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
	"github.com/OpenVibers/OpenVibe.Link/internal/memorystore"
	"github.com/OpenVibers/OpenVibe.Link/internal/memorysync"
	"github.com/OpenVibers/OpenVibe.Link/internal/memoryupload"
)

// memorySyncUsage is what "openvibe-link memory sync" prints.
const memorySyncUsage = "openvibe-link memory sync [--tools a,b] [--project DIR …] [--dry-run] [--yes]\n" +
	"openvibe-link memory sync --disable a,b\n" +
	"openvibe-link memory sync --undo\n" +
	"\n" +
	"  Writes your OpenVibe Memory profile and this project's rules into the\n" +
	"  places the coding tools read them: AGENTS.md (Codex and friends), a thin\n" +
	"  CLAUDE.md that imports it, GEMINI.md, Cursor rules and Copilot\n" +
	"  instructions. It writes only inside its own marked block; your own text\n" +
	"  in those files is never changed.\n" +
	"\n" +
	"  Every tool is off until you turn it on. The first write to a file shows\n" +
	"  the diff and asks. --disable takes the block out again and turns the tool\n" +
	"  off; --undo does that for every tool that is on.\n" +
	"\n" +
	"  --tools a,b       turn these tools on: agents, claude-code, gemini, cursor,\n" +
	"                    copilot (with no --tools, the tools already on are synced)\n" +
	"  --disable a,b     turn these tools off and remove their blocks\n" +
	"  --undo            remove every block this command wrote and turn the tools off\n" +
	"  --project DIR     write this project's files as well; may be given more than once\n" +
	"  --dry-run         show the diffs and write nothing\n" +
	"  --yes             do not ask on the first write\n" +
	"  --state FILE      the opt-in file (default: the Link's state directory)\n" +
	"  --endpoint URL    the memory store (default " + memorystore.DefaultBaseURL + ")\n" +
	"  --token-env NAME  the variable the access token is read from (default " + memorystore.DefaultTokenEnv + ")\n" +
	"  --user-home DIR   use this directory as the person's home (default: yours)\n"

// cmdMemorySync writes the person's memory into the tools' own files, one
// marked block at a time.
func cmdMemorySync(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory sync", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, memorySyncUsage) }
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
	endpoint := fs.String("endpoint", memorystore.DefaultBaseURL, "the memory store")
	tokenEnv := fs.String("token-env", memorystore.DefaultTokenEnv, "the variable the access token is read from")
	userHome := fs.String("user-home", "", "use this directory as the person's home")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unknown argument %q\n\n", fs.Arg(0))
		fmt.Fprint(stderr, memorySyncUsage)
		return 2
	}
	for _, id := range append(append([]string{}, tools...), disable...) {
		if !memorysync.KnownTool(id) {
			fmt.Fprintf(stderr, "error: no such tool %q. The tools are: %s\n", id, strings.Join(memorysync.ToolIDs(), ", "))
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
		statePath = filepath.Join(config.DefaultPaths("").StateDir, "memory-sync.json")
	}
	state, err := memorysync.LoadState(statePath)
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
		return syncRemove(home, projects, state, statePath, turnOff, *dryRun, stdout, stderr)
	}

	// With no --tools, sync what is already on. Nothing is on by default.
	on := state.EnabledTools()
	if len(tools) > 0 {
		on = append([]string{}, tools...)
	}
	if len(on) == 0 {
		fmt.Fprintln(stdout, "No tool is on: nothing was written.")
		fmt.Fprintf(stdout, "Turn one on with --tools (%s). The first write shows its diff and asks.\n", strings.Join(memorysync.ToolIDs(), ", "))
		return 0
	}
	sort.Strings(on)

	content, err := fetchContent(projects, on, *endpoint, *tokenEnv)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return syncWrite(home, projects, state, statePath, on, content, *dryRun, *assumeYes, stdin, stdout, stderr)
}

// absProjects makes the project paths absolute and checks they are directories.
func absProjects(projects []string) ([]string, error) {
	out := make([]string, 0, len(projects))
	seen := map[string]bool{}
	for _, p := range projects {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(abs)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("%s is not a directory", p)
		}
		if !seen[abs] {
			seen[abs] = true
			out = append(out, abs)
		}
	}
	return out, nil
}

// fetchContent reads the profile of the person and of each project, and the
// project rules of each project, from the person's own store.
func fetchContent(projects []string, tools []string, endpoint, tokenEnv string) (map[string]memorysync.Content, error) {
	token := strings.TrimSpace(os.Getenv(tokenEnv))
	if token == "" {
		return nil, fmt.Errorf("%s is not set: it must hold your OpenVibe access token", tokenEnv)
	}
	client, err := memorystore.New(memorystore.Options{BaseURL: endpoint, Token: token})
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	out := map[string]memorysync.Content{}
	user, err := client.Profile(ctx, "user")
	if err != nil {
		return nil, err
	}
	out["user"] = memorysync.Content{Scope: "user", Profile: user.Text}
	for _, project := range projects {
		slug := memoryupload.Slug(project)
		if slug == "" {
			return nil, fmt.Errorf("the folder %s has no name the store can use", project)
		}
		scope := "project:" + slug
		profile, err := client.Profile(ctx, scope)
		if err != nil {
			return nil, err
		}
		items, err := client.List(ctx, scope, "")
		if err != nil {
			return nil, err
		}
		rules := []string{}
		for _, item := range items.Items {
			if item.Status != "" && item.Status != "active" {
				continue
			}
			if item.Kind == "project_rule" {
				rules = append(rules, item.Text)
			}
		}
		out[scope] = memorysync.Content{Scope: scope, Profile: profile.Text, Rules: rules}
	}
	// A tool that only has project files needs a project to write into.
	for _, id := range tools {
		if id != "cursor" && id != "copilot" {
			continue
		}
		if len(projects) == 0 {
			return nil, fmt.Errorf("%s writes only project files: give at least one --project", id)
		}
	}
	return out, nil
}

// plannedFile is one file the sync is about to look at.
type plannedFile struct {
	target memorysync.Target
	path   string
	scope  string
}

// filesFor gives every file of the given tools, for the home and the projects.
func filesFor(home string, projects []string, tools []string) []plannedFile {
	on := map[string]bool{}
	for _, id := range tools {
		on[id] = true
	}
	out := []plannedFile{}
	for _, target := range memorysync.Targets() {
		if !on[target.Tool] {
			continue
		}
		if target.Root == memorysync.RootHome {
			out = append(out, plannedFile{target: target, path: filepath.Join(home, filepath.FromSlash(target.Rel)), scope: "user"})
			continue
		}
		for _, project := range projects {
			out = append(out, plannedFile{
				target: target,
				path:   filepath.Join(project, filepath.FromSlash(target.Rel)),
				scope:  "project:" + memoryupload.Slug(project),
			})
		}
	}
	return out
}

// syncWrite shows the diff of every changed file and writes it inside the
// marked block, after the first-write gate.
func syncWrite(home string, projects []string, state *memorysync.State, statePath string, tools []string,
	content map[string]memorysync.Content, dryRun, assumeYes bool, stdin io.Reader, stdout, stderr io.Writer) int {
	fmt.Fprintf(stdout, "Memory sync of %s\n", home)
	if len(projects) == 0 {
		fmt.Fprintln(stdout, "No project given: only the user-level files are written.")
	} else {
		fmt.Fprintf(stdout, "Projects: %s\n", strings.Join(projects, ", "))
	}
	fmt.Fprintf(stdout, "Tools on: %s\n", strings.Join(tools, ", "))

	written, skipped, unchanged := 0, 0, 0
	reader := bufio.NewReader(stdin)
	for _, planned := range filesFor(home, projects, tools) {
		c, ok := content[planned.scope]
		if !ok {
			fmt.Fprintf(stderr, "error: no memory was read for %s\n", planned.scope)
			return 1
		}
		body := memorysync.Body(c)
		if planned.target.Import != nil {
			body = memorysync.ImportBody(planned.target.Import(home))
		}
		block := memorysync.Block(body)
		existing, existed, err := readFile(planned.path)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		var next []byte
		var sep string
		if block == "" {
			fileState := state.FileStateOf(planned.target.Tool, planned.path)
			oldSep := ""
			if fileState != nil {
				oldSep = fileState.Sep
			}
			next, _, err = memorysync.Remove(existing, oldSep)
			if err != nil {
				fmt.Fprintf(stderr, "error: %s: %v\n", displayPath(home, planned.path), err)
				skipped++
				continue
			}
		} else {
			if !existed && !planned.target.Create && planned.target.Frontmatter == "" {
				// Files that are not ours to create stay absent.
				fmt.Fprintf(stdout, "\n%s: not there; not creating it (only its own block is ever written)\n", displayPath(home, planned.path))
				skipped++
				continue
			}
			if !existed && planned.target.Frontmatter != "" {
				existing = []byte(planned.target.Frontmatter)
			}
			next, sep, _, err = memorysync.Replace(existing, block)
			if err != nil {
				fmt.Fprintf(stderr, "error: %s: %v\n", displayPath(home, planned.path), err)
				skipped++
				continue
			}
		}
		if string(next) == string(existing) {
			unchanged++
			continue
		}
		fmt.Fprintf(stdout, "\n%s\n", displayPath(home, planned.path))
		if planned.target.Root == memorysync.RootProject {
			fmt.Fprintln(stdout, "  (this is a project file: it may be shared or committed)")
		}
		fmt.Fprint(stdout, memorysync.Diff(displayPath(home, planned.path), existing, next))
		if dryRun {
			skipped++
			continue
		}
		fileState := state.File(planned.target.Tool, planned.path)
		if !fileState.Confirmed {
			if !assumeYes && !ask(reader, stdout, "Write this file for the first time? [y/N] ") {
				fmt.Fprintln(stdout, "Not written: no confirmation.")
				skipped++
				continue
			}
			fileState.Confirmed = true
		}
		if err := writeFile(planned.path, next); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
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
		fmt.Fprintln(stdout, "Written, inside the OpenVibe Memory block.")
		written++
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
	fmt.Fprintf(stdout, "\n%d file(s) written, %d unchanged, %d not written.\n", written, unchanged, skipped)
	if dryRun {
		fmt.Fprintln(stdout, "Nothing was written: this was a dry run.")
	}
	return 0
}

// syncRemove takes the block out of every file of the given tools and turns
// them off.
func syncRemove(home string, projects []string, state *memorysync.State, statePath string, tools []string,
	dryRun bool, stdout, stderr io.Writer) int {
	fmt.Fprintf(stdout, "Memory sync undo of %s\n", home)
	removed, missing, failed := 0, 0, 0
	for _, id := range tools {
		// The state knows every file that was written; the targets catch a
		// file that was edited by hand after the state was lost.
		paths := map[string]memorysync.Target{}
		for _, planned := range filesFor(home, projects, []string{id}) {
			paths[planned.path] = planned.target
		}
		if t := state.Tools[id]; t != nil {
			for path := range t.Files {
				if _, ok := paths[path]; !ok {
					if target, ok := memorysync.TargetFor(path); ok {
						paths[path] = target
					} else {
						paths[path] = memorysync.Target{Tool: id, Name: id}
					}
				}
			}
		}
		for path, target := range paths {
			existing, existed, err := readFile(path)
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				failed++
				continue
			}
			if !existed {
				missing++
				continue
			}
			fileState := state.FileStateOf(id, path)
			sep := ""
			created := false
			if fileState != nil {
				sep = fileState.Sep
				created = fileState.Created
			}
			next, found, err := memorysync.Remove(existing, sep)
			if err != nil {
				fmt.Fprintf(stderr, "error: %s: %v\n", path, err)
				failed++
				continue
			}
			if !found {
				missing++
				if fileState != nil && !dryRun {
					delete(state.Tool(id).Files, path)
				}
				continue
			}
			fmt.Fprintf(stdout, "\n%s: removed the OpenVibe Memory block\n", displayPath(home, path))
			if dryRun {
				removed++
				continue
			}
			if created && memorysync.OnlyOurContent(next, target.Frontmatter) {
				if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
					fmt.Fprintf(stderr, "error: %v\n", err)
					failed++
					continue
				}
				fmt.Fprintf(stdout, "%s: removed (the Link created it)\n", displayPath(home, path))
			} else if err := writeFile(path, next); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				failed++
				continue
			}
			delete(state.Tool(id).Files, path)
			removed++
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
	fmt.Fprintf(stdout, "\n%d block(s) removed, %d file(s) had none, %d failed.\n", removed, missing, failed)
	if dryRun {
		fmt.Fprintln(stdout, "Nothing was changed: this was a dry run.")
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// readFile reads a file, treating a missing file as empty. It never follows a
// path that is not a regular file.
func readFile(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s is not a regular file; leaving it alone", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	return raw, true, nil
}

// writeFile writes the file through a temporary file in its own directory, so a
// crash never leaves half a block.
func writeFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, ".openvibe-memory-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// ask shows the question and takes yes for an answer. Anything else is no.
func ask(reader *bufio.Reader, stdout io.Writer, question string) bool {
	fmt.Fprint(stdout, "\n"+question)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(stdout)
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

// displayPath shows a file without the absolute project path in the output: a
// home file is `~/.codex/AGENTS.md`, a project file is relative to its project.
func displayPath(home, path string) string {
	if rel, err := filepath.Rel(home, path); err == nil && !strings.HasPrefix(rel, "..") {
		return "~/" + filepath.ToSlash(rel)
	}
	return path
}
