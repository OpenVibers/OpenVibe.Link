//go:build linux

package firstrun

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// memory reads MemTotal and MemAvailable (or MemFree) from /proc/meminfo, in bytes.
func memory() (total, free uint64) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "kB")), 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			total = n * 1024
		case "MemAvailable":
			free = n * 1024
		}
	}
	return total, free
}

// disk measures the filesystem that holds dir, in bytes.
func disk(dir string) (total, free uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0
	}
	return uint64(st.Blocks) * uint64(st.Bsize), uint64(st.Bavail) * uint64(st.Bsize)
}
