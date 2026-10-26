//go:build windows

package utilities

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// FreeBytes is the space the calling user can still use on the volume holding path.
func FreeBytes(path string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var available, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &available, &total, &free); err != nil {
		return 0, err
	}
	return available, nil
}

// SameFilesystem reports whether two paths are on one volume, where a rename moves a
// file without copying it.
func SameFilesystem(a, b string) bool {
	va, vb := filepath.VolumeName(a), filepath.VolumeName(b)
	return va != "" && strings.EqualFold(va, vb)
}
