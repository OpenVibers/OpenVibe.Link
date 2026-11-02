//go:build !windows

package plugins

import "os/exec"

// processTree is the plugin's own process group on Unix, set up by setProcAttr before Start. Killing the group is
// killProcess, so there is nothing more to hold.
type processTree struct{ cmd *exec.Cmd }

func startProcessTree(cmd *exec.Cmd) (*processTree, error) { return &processTree{cmd: cmd}, nil }

func (t *processTree) kill() { killProcess(t.cmd) }

// close is empty: the process group goes away with the processes in it.
func (t *processTree) close() {}
