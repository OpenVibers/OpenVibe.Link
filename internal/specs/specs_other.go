//go:build !linux && !darwin && !windows

package specs

import "runtime"

// readOS on a system with no reader of its own (FreeBSD, Android's own build): its name alone.
func readOS(m *Machine) {
	m.OSName = runtime.GOOS
	m.Display = "none"
}
