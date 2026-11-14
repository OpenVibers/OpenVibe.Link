//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !windows

package specs

func diskGB(string) (float64, float64) { return 0, 0 }
