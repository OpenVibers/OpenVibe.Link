//go:build !linux && !darwin

package firstrun

// memory and disk are only read on Linux and macOS; elsewhere the event carries the OS, architecture and CPU count.
func memory() (total, free uint64)     { return 0, 0 }
func disk(string) (total, free uint64) { return 0, 0 }
