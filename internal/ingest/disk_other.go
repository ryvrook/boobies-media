//go:build !unix

package ingest

import "errors"

func FreeSpace(string) (uint64, error) {
	return 0, errors.New("ingest: free space reporting is not supported on this platform")
}
