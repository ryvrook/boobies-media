//go:build unix

package ingest

import (
	"fmt"
	"syscall"
)

func FreeSpace(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, fmt.Errorf("ingest: statfs %s: %w", path, err)
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}
