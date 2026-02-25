//go:build windows

package utilities

import (
	"syscall"

	"golang.org/x/sys/windows"
)

func NormalizePathForExternalTool(p string) (string, error) {
	return toShortPath(p)
}

func toShortPath(path string) (string, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}

	buf := make([]uint16, syscall.MAX_PATH)
	n, err := windows.GetShortPathName(p, &buf[0], uint32(len(buf)))
	if err != nil {
		return "", err
	}

	return syscall.UTF16ToString(buf[:n]), nil
}
