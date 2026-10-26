//go:build !windows

package utilities

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// FreeBytes is the space an unprivileged writer can still use on the filesystem
// holding path: available blocks, not free ones, since root's reserve is not ours.
func FreeBytes(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

// SameFilesystem reports whether two existing paths are on one filesystem, where a
// rename moves a file without copying it.
func SameFilesystem(a, b string) bool {
	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)
	if errA != nil || errB != nil {
		return false
	}
	sa, okA := ia.Sys().(*syscall.Stat_t)
	sb, okB := ib.Sys().(*syscall.Stat_t)
	return okA && okB && uint64(sa.Dev) == uint64(sb.Dev)
}
