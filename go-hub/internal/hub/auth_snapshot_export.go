package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

// ExportLegacyAuthSnapshot reads a live legacy authority without constructing a
// Server or changing source files. Generation 1 is only for an unseeded reader.
// The existing CTL value is needed to exclude control aliases as well as kinds.
func ExportLegacyAuthSnapshot(configDir, writerID, output string, budget int, ctlToken string) error {
	if writerID == "" || strings.TrimSpace(writerID) != writerID || ctlToken == "" {
		return errors.New("explicit existing writer identity and CTL_TOKEN are required")
	}
	if budget <= 0 || budget > authSnapshotMaxBudget {
		return errors.New("snapshot budget must be between 1 and 10 MiB")
	}
	info, err := os.Lstat(configDir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("source must be a real legacy config directory")
	}
	source, err := filepath.EvalSymlinks(configDir)
	if err != nil {
		return err
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(source, "auth-continuity")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("source already has auth continuity state; use the writer export API")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(output))
	if err != nil {
		return err
	}
	parent, err = filepath.Abs(parent)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(source, parent)
	if err != nil {
		return err
	}
	if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))) {
		return errors.New("output must be outside the source config directory")
	}
	output = filepath.Join(parent, filepath.Base(output))
	bundle := authSnapshot{Version: 1, WriterID: writerID, Generation: 1, Tokens: &managedMCPTokenState{}, Clients: &oauthClientsState{}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	locks := []*flock.Flock{}
	defer func() {
		for i := len(locks) - 1; i >= 0; i-- {
			_ = locks[i].Close()
		}
	}()
	// Hold all existing native locks together until all stores have been read.
	// No O_CREATE: even missing lock files are never created on the authority.
	for _, name := range []string{"mcp_tokens_state.json", oauthClientsStateFilename, accessProfilesStateFilename} {
		path := filepath.Join(source, name)
		absent := false
		if name == accessProfilesStateFilename {
			_, err := os.Lstat(path)
			absent = errors.Is(err, os.ErrNotExist)
		}
		if absent {
			if _, err := os.Lstat(path + ".lock"); errors.Is(err, os.ErrNotExist) {
				continue
			}
		}
		if err := regularAuthPath(path+".lock", false); err != nil {
			return fmt.Errorf("native lock unavailable for %s: %w", name, err)
		}
		lock := flock.New(path+".lock", flock.SetFlag(os.O_RDONLY))
		held, err := lock.TryRLockContext(ctx, 10*time.Millisecond)
		if err != nil || !held {
			_ = lock.Close()
			return fmt.Errorf("native auth store lock busy or unavailable: %s", name)
		}
		locks = append(locks, lock)
	}
	for name, dest := range map[string]any{"mcp_tokens_state.json": bundle.Tokens, oauthClientsStateFilename: bundle.Clients} {
		path := filepath.Join(source, name)
		if err := regularAuthPath(path, false); err != nil {
			return err
		}
		if err := readAccessStateJSON(path, authSnapshotMaxBudget, dest); err != nil {
			return err
		}
	}
	profilePath := filepath.Join(source, accessProfilesStateFilename)
	if _, err := os.Lstat(profilePath); err == nil {
		// An optional store created after the lock pass requires a fresh attempt.
		if len(locks) != 3 {
			return errors.New("optional profile store appeared during export; retry")
		}
		if err := regularAuthPath(profilePath, false); err != nil {
			return err
		}
		bundle.Profiles = &accessProfilesState{}
		if err := readAccessStateJSON(profilePath, accessProfileStateMaxBytes, bundle.Profiles); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	excludeSnapshotControlCredentials(&bundle, ctlToken)
	// Instruction-set contents are not replicated; the receiving node validates
	// referenced sets against its separately provisioned local configuration.
	if err := validateAuthSnapshotValues(bundle, budget, ctlToken, func(string) bool { return true }); err != nil {
		return err
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		_ = f.Close()
		if !complete {
			_ = os.Remove(output)
		}
	}()
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	complete = true
	return nil
}
