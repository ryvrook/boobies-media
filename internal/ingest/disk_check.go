package ingest

import (
	"errors"
	"fmt"
)

var ErrNotEnoughDisk = errors.New("ingest: not enough free disk space")

const DefaultDiskHeadroom = 1 << 30

type FreeSpaceFunc func(path string) (uint64, error)

func CheckFreeSpace(free FreeSpaceFunc, path string, need, headroom uint64) error {
	if free == nil {
		return nil
	}
	available, err := free(path)
	if err != nil {
		return nil
	}
	if need > ^uint64(0)-headroom || available < need+headroom {
		return fmt.Errorf("%w: %d bytes free on %s, need %d plus %d headroom",
			ErrNotEnoughDisk, available, path, need, headroom)
	}
	return nil
}
