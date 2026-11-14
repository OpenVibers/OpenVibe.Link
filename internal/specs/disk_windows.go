//go:build windows

package specs

import "golang.org/x/sys/windows"

// diskGB is the size and the free space (to this user) of the volume holding path, in GB.
func diskGB(path string) (total, free float64) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0
	}
	var avail, all, allFree uint64
	if windows.GetDiskFreeSpaceEx(p, &avail, &all, &allFree) != nil {
		return 0, 0
	}
	return round1(float64(all) / 1e9), round1(float64(avail) / 1e9)
}
