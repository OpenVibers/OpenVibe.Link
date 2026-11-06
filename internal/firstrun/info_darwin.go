//go:build darwin

package firstrun

import "syscall"

// memory is not read on macOS without another API; the event simply carries no memory figures there.
func memory() (total, free uint64) { return 0, 0 }

// disk measures the filesystem that holds dir, in bytes.
func disk(dir string) (total, free uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0
	}
	return uint64(st.Blocks) * uint64(st.Bsize), uint64(st.Bavail) * uint64(st.Bsize)
}
