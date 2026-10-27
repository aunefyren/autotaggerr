//go:build windows

package storage

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// diskUsage reports the size and free space of the volume holding path, keyed by its
// volume name (C:, \\server\share) so two paths on one disk are shown as one.
func diskUsage(path string) (disk, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return disk{}, err
	}
	p, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return disk{}, err
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return disk{}, err
	}
	return disk{id: strings.ToUpper(filepath.VolumeName(abs)), total: total, free: free}, nil
}
