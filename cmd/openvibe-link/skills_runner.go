package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/OpenVibers/OpenVibe.Link/internal/config"
	"github.com/OpenVibers/OpenVibe.Link/internal/skills"
)

// skillsRunnerUsage is what "openvibe-link skills on|off|status" prints.
var skillsRunnerUsage = "openvibe-link skills on [--dir DIR] [--node PATH]\n" +
	"openvibe-link skills off\n" +
	"openvibe-link skills status [--json]\n" +
	"\n" +
	"  Runs one tool of a declared skill as a sandboxed function job on this\n" +
	"  machine (artifact openvibe-skill-runner@1.0.0, docs/worker.md). The job's\n" +
	"  code is the job's own checked input; it runs with this machine's Node.js,\n" +
	"  no environment, no child processes or workers, no addons, no eval, and\n" +
	"  reads nothing outside the job directory. The network and every kernel\n" +
	"  limit are the job worker's.\n" +
	"\n" +
	"  The runner is OFF until `skills on`: that writes the runner and declares\n" +
	"  it in worker.functions. It does not turn the job worker on by itself (the\n" +
	"  installer's --worker does), and the Node must be restarted to load it.\n" +
	"\n" +
	"  --dir DIR    where the runner lives (default " + skills.DefaultDir() + ")\n" +
	"  --node PATH  the Node.js 22.13+ binary the tool runs with (default: /usr/bin/node,\n" +
	"               /usr/local/bin/node, then node on PATH)\n" +
	"  --json       print status as JSON\n"

// cmdSkillsRunner runs the skill runner commands; args[0] is on, off or status.
func cmdSkillsRunner(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, skillsRunnerUsage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "on", "off", "status":
	default:
		fmt.Fprintf(stderr, "unknown skills command %q\n\n", cmd)
		fmt.Fprint(stderr, skillsSyncUsage)
		fmt.Fprint(stderr, skillsRunnerUsage)
		return 2
	}
	fs := flag.NewFlagSet("skills "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, skillsRunnerUsage) }
	home := fs.String("home", "", "config and state directory (also OPENVIBE_LINK_HOME)")
	dir := fs.String("dir", skills.DefaultDir(), "where the skill runner lives")
	node := fs.String("node", "", "the Node.js binary the skill's code runs with")
	asJSON := fs.Bool("json", false, "print JSON (status)")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unknown argument %q\n\n", fs.Arg(0))
		fmt.Fprint(stderr, skillsRunnerUsage)
		return 2
	}
	paths := config.DefaultPaths(*home)
	cfg, err := config.Load(paths.ConfigFile())
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	switch cmd {
	case "on":
		entry, n, _, err := skills.On(cfg, *dir, *node)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		if err := config.Save(paths.ConfigFile(), cfg); err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		fmt.Fprintf(stdout, "Skill runner on: %s@%s (%s) in %s\n", entry.Name, entry.Version, entry.Class, paths.ConfigFile())
		fmt.Fprintf(stdout, "  command: %s\n", skills.CommandString(entry.Command))
		fmt.Fprintf(stdout, "  node:    %s (%s)\n", n.Path, n.Version)
		if !cfg.Worker.Enabled {
			fmt.Fprintln(stdout, "The job worker is off: jobs run only once worker.enabled is true (install.sh --worker sets it).")
		}
		fmt.Fprintln(stdout, "Restart the Node to load it.")
	case "off":
		if !skills.Off(cfg) {
			fmt.Fprintf(stdout, "Skill runner is already off (%s).\n", paths.ConfigFile())
			break
		}
		if err := config.Save(paths.ConfigFile(), cfg); err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		fmt.Fprintf(stdout, "Skill runner off: removed %s from worker.functions in %s\n", skills.Name, paths.ConfigFile())
		fmt.Fprintln(stdout, "Restart the Node to unload it.")
	case "status":
		s := skills.StatusOf(cfg)
		if *asJSON {
			b, _ := json.MarshalIndent(s, "", "  ")
			fmt.Fprintln(stdout, string(b))
			break
		}
		if !s.On {
			fmt.Fprintln(stdout, "skill runner: off")
			fmt.Fprintf(stdout, "  The job worker is %s.\n", onOff(s.WorkerEnabled))
			fmt.Fprintln(stdout, "  Turn it on: openvibe-link skills on   (then restart the Node)")
			break
		}
		fmt.Fprintf(stdout, "skill runner: on (%s@%s, %s)\n", s.Name, s.Version, s.Class)
		fmt.Fprintf(stdout, "  artifact: %s (%s)\n", s.Artifact, present(s.ArtifactPresent))
		fmt.Fprintf(stdout, "  command: %s\n", skills.CommandString(s.Command))
		if s.NodeVersion != "" {
			fmt.Fprintf(stdout, "  node: %s (%s)\n", s.Node, s.NodeVersion)
		} else {
			fmt.Fprintf(stdout, "  node: %s (unusable: %s)\n", s.Node, s.NodeError)
		}
		fmt.Fprintf(stdout, "  job worker: %s\n", onOff(s.WorkerEnabled))
	}
	return 0
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func present(v bool) string {
	if v {
		return "present"
	}
	return "missing"
}
