package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
)

const authSnapshotDefaultReserve int64 = 1 << 20

var errAuthSnapshotSpace = errors.New("auth snapshot storage unavailable")

type authFilesystemSpace struct {
	Available, Block, Inodes uint64
	HasInodes                bool // Windows filesystems do not expose a Unix-style inode allowance.
}
type authDiskWriteBudget struct{ Bytes, Inodes uint64 }

func authSnapshotReserveFromEnv() int64 {
	raw := os.Getenv("GPTADMIN_AUTH_SNAPSHOT_RESERVE_BYTES")
	if raw == "" {
		return authSnapshotDefaultReserve
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return -1
	} // Explicit invalid configuration fails closed on reader apply.
	return n
}

// Count actual pretty JSON plus newline and round each atomic file allocation.
// Do not credit old slot/manifest blocks or inodes released by replacement.
func authWriteBudget(values []any, block uint64, newDirs, newLocks uint64) (authDiskWriteBudget, error) {
	need := authDiskWriteBudget{Inodes: uint64(len(values)) + newDirs + newLocks}
	if block == 0 || block > 1<<30 {
		return need, fmt.Errorf("%w: invalid allocation block", errAuthSnapshotSpace)
	}
	for _, value := range values {
		raw, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return need, err
		}
		need.Bytes += ((uint64(len(raw)) + 1 + block - 1) / block) * block
	}
	// Conservative directory/lock metadata allowance, in addition to operation reserve.
	need.Bytes += (newDirs + newLocks) * block
	return need, nil
}
func (need authDiskWriteBudget) admit(fs authFilesystemSpace, reserve int64) error {
	if reserve <= 0 || uint64(reserve) > math.MaxUint64-need.Bytes {
		return fmt.Errorf("%w: invalid operation reserve", errAuthSnapshotSpace)
	}
	if fs.Available < need.Bytes+uint64(reserve) {
		return fmt.Errorf("%w: insufficient available bytes", errAuthSnapshotSpace)
	}
	if fs.HasInodes && fs.Inodes < need.Inodes {
		return fmt.Errorf("%w: insufficient available inodes", errAuthSnapshotSpace)
	}
	return nil
}

func (s *Server) preflightReaderAuthSnapshot(bundle authSnapshot) error {
	slot := 1 - s.authState.Slot
	dir := s.authSlotDir(slot)
	files := map[string]any{"mcp_tokens_state.json": bundle.Tokens, oauthClientsStateFilename: bundle.Clients}
	if bundle.Profiles != nil {
		files[accessProfilesStateFilename] = bundle.Profiles
	}
	pending := s.authState
	pending.Pending = true
	next := authSnapshotState{WriterID: bundle.WriterID, Generation: bundle.Generation, Slot: slot, Digest: authSnapshotDigest(bundle), ReadOnly: true, ProfilesPresent: bundle.Profiles != nil}
	values := []any{pending, next}
	var dirs, locks uint64
	destinations := []string{s.authBaseDir()}
	if info, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		dirs = 1
	} else if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: invalid slot directory", errAuthSnapshotSpace)
	} else {
		destinations = append(destinations, dir)
	}
	for name, value := range files {
		values = append(values, value)
		path := filepath.Join(dir, name+".lock")
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			locks++
		} else if err != nil {
			return fmt.Errorf("%w: cannot inspect slot lock", errAuthSnapshotSpace)
		}
	}
	// Inspect both manifest and slot filesystems if the slot already exists;
	// this also handles an operator's separate mount conservatively.
	for _, path := range destinations {
		fs, err := authFilesystemAvailable(path)
		if err != nil {
			return fmt.Errorf("%w: cannot determine filesystem availability", errAuthSnapshotSpace)
		}
		need, err := authWriteBudget(values, fs.Block, dirs, locks)
		if err != nil {
			return err
		}
		if err := need.admit(fs, s.cfg.AuthSnapshotReserveBytes); err != nil {
			return err
		}
	}
	return nil
}
