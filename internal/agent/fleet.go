package agent

import (
	"os"

	"github.com/OpenVibers/OpenVibe.Link/internal/codesrunner"
	"github.com/OpenVibers/OpenVibe.Link/internal/config"
)

// codesProbeCommand builds the fleet probe (Codes C9c) from the worker's declared OpenVibe.Codes entry: the same
// interpreter and script the jobs run, asked for `logins --json` instead of `job`. Nil when the machine has no such
// entry — a Agent that never installed the Codes runner has no coding agents to report.
func codesProbeCommand(fns []config.FunctionConfig) []string {
	for _, f := range fns {
		if f.Name != "openvibe-codes" || f.EffectiveClass() != config.ClassCode || len(f.Command) == 0 {
			continue
		}
		argv := append([]string(nil), f.Command...)
		// The installer declares the entry as `node …/openvibe-codes.js job`: the same command with another verb.
		if len(argv) > 1 && argv[len(argv)-1] == "job" {
			argv = argv[:len(argv)-1]
		}
		return append(argv, "logins", "--json")
	}
	return nil
}

// newCodesRunner makes the updater for the worker's openvibe-codes entry (v0.76.0), or nil: no such entry, a runner
// not in the installer's shape, a folder this Link cannot write beside, or OPENVIBE_CODES_AUTO_UPDATE=0.
func (n *Agent) newCodesRunner(fns []config.FunctionConfig) *codesrunner.Updater {
	if os.Getenv("OPENVIBE_CODES_AUTO_UPDATE") == "0" {
		return nil
	}
	for _, f := range fns {
		if f.Name != "openvibe-codes" || f.EffectiveClass() != config.ClassCode {
			continue
		}
		prefix, node, ok := codesrunner.PrefixOf(f.Command)
		if !ok {
			return nil
		}
		if !codesrunner.Writable(prefix) {
			n.log.Info("codes runner: kept as installed (this Link cannot write beside it)", "dir", prefix)
			return nil
		}
		u, err := codesrunner.New(codesrunner.Options{Prefix: prefix, Node: node, Log: n.log, Busy: func() bool {
			n.mu.Lock()
			w := n.worker
			n.mu.Unlock()
			return w != nil && len(w.Jobs()) > 0
		}})
		if err != nil {
			n.log.Warn("codes runner: no updater", "err", err)
			return nil
		}
		return u
	}
	return nil
}
