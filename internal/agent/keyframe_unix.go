//go:build !windows

package agent

import (
	"os"
	"syscall"
)

// screenKeyframeSignal is what asks ov-desk stream for an IDR at once (Link v0.75).
func screenKeyframeSignal() os.Signal { return syscall.SIGUSR1 }
