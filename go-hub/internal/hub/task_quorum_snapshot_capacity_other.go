//go:build !unix && !windows

package hub

import "errors"

func requireTaskQuorumSnapshotCapacity(string, int64, int64, int64) error {
	return errors.New("task quorum external snapshots require a supported filesystem capacity probe")
}
