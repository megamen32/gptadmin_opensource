//go:build unix

package hub

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func requireTaskQuorumSnapshotCapacity(path string, maxBytes, freeReserve int64, copies int64) error {
	maxInt64 := int64(^uint64(0) >> 1)
	if maxBytes <= 0 || freeReserve <= 0 || copies <= 0 || maxBytes > (maxInt64-freeReserve)/copies {
		return errors.New("invalid task quorum snapshot disk budget")
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return err
	}
	available := uint64(stat.Bavail) * uint64(stat.Bsize)
	required := uint64(copies*maxBytes + freeReserve)
	if available < required {
		return fmt.Errorf("task quorum snapshot needs %d bytes plus reserve; filesystem has %d bytes available", required, available)
	}
	return nil
}
