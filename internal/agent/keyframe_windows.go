//go:build windows

package agent

import "os"

// screenKeyframeSignal: Windows has no SIGUSR1, and ov-desk stream is not offered there.
func screenKeyframeSignal() os.Signal { return nil }
