//go:build windows

package hub

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

func requireTaskQuorumSnapshotCapacity(path string, maxBytes, freeReserve int64, copies int64) error {
	maxInt64 := int64(^uint64(0) >> 1)
	if maxBytes <= 0 || freeReserve <= 0 || copies <= 0 || maxBytes > (maxInt64-freeReserve)/copies {
		return errors.New("invalid task quorum snapshot disk budget")
	}
	directory, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	var available uint64
	if err := windows.GetDiskFreeSpaceEx(directory, &available, nil, nil); err != nil {
		return err
	}
	required := uint64(copies*maxBytes + freeReserve)
	if available < required {
		return fmt.Errorf("task quorum snapshot needs %d bytes plus reserve; filesystem has %d bytes available", required, available)
	}
	return nil
}
