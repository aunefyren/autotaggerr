//go:build !windows

package storage

import (
	"fmt"
	"syscall"
)

// diskUsage reports the size and free space of the volume holding path, and an
// identity for that volume so two paths on one disk are shown as one. Free is what an
// unprivileged process can use (Bavail), which is what a growing library will meet.
func diskUsage(path string) (disk, error) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return disk{}, err
	}
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return disk{}, err
	}
	block := uint64(fs.Bsize)
	return disk{
		id:    fmt.Sprintf("dev:%d", uint64(st.Dev)),
		total: fs.Blocks * block,
		free:  fs.Bavail * block,
	}, nil
}
