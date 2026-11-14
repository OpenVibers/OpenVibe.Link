//go:build linux || darwin || freebsd || netbsd || openbsd

package specs

import "golang.org/x/sys/unix"

// diskGB is the size and the free space (to an unprivileged user) of the volume holding path, in GB.
func diskGB(path string) (total, free float64) {
	var st unix.Statfs_t
	if unix.Statfs(path, &st) != nil {
		return 0, 0
	}
	bs := float64(st.Bsize)
	return round1(float64(st.Blocks) * bs / 1e9), round1(float64(st.Bavail) * bs / 1e9)
}
