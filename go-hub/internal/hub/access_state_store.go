package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/gofrs/flock"
)

var errAccessStateConflict = errors.New("access record changed in another Hub; reload and retry")

// Existing configuration files remain the storage contract. Every writer merges
// only its changed records against the snapshot it read, under an OS lock.
// Independent issuers cannot erase each other's clients; same-record stale
// mutations conflict instead of silently undoing a revocation or role change.
func mergeAccessRecords[T any](base, wanted, latest map[string]T) (map[string]T, error) {
	out := cloneAccessRecords(latest)
	for id, next := range wanted {
		before, existed := base[id]
		if existed && reflect.DeepEqual(before, next) {
			continue
		}
		live, present := latest[id]
		if present && reflect.DeepEqual(live, next) {
			continue
		}
		if present != existed || (present && !reflect.DeepEqual(live, before)) {
			return nil, fmt.Errorf("%w: %s", errAccessStateConflict, id)
		}
		out[id] = next
	}
	for id, before := range base {
		if _, exists := wanted[id]; exists {
			continue
		}
		if live, present := latest[id]; present && !reflect.DeepEqual(live, before) {
			return nil, fmt.Errorf("%w: %s", errAccessStateConflict, id)
		}
		delete(out, id)
	}
	return out, nil
}
func cloneAccessRecords[T any](values map[string]T) map[string]T {
	out := make(map[string]T, len(values))
	for id, v := range values {
		out[id] = v
	}
	return out
}
func withAccessStateLock(path string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	lock := flock.New(path + ".lock")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	held, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return err
	}
	if !held {
		return errors.New("access state busy; retry")
	}
	defer lock.Close()
	return fn()
}
func readAccessStateJSON(path string, limit int64, dst any) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return errors.New("access state exceeds size limit")
	}
	return json.Unmarshal(data, dst)
}
func writeAccessStateJSON(path string, value any, limit int) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > limit {
		return errors.New("access state exceeds size limit")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".access-state-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = replaceInstructionFile(name, path); err != nil {
		return err
	}
	return syncInstructionDirectory(filepath.Dir(path))
}

// Only this on-disk envelope includes a bearer value. Normal inventory,
// diagnostic JSON and audit encoding of managedMCPToken cannot emit it.
type storedManagedMCPToken struct {
	managedMCPToken
	Value string `json:"token_value,omitempty"`
}

func (s managedMCPTokenState) MarshalJSON() ([]byte, error) {
	tokens := make(map[string]storedManagedMCPToken, len(s.Tokens))
	for id, record := range s.Tokens {
		tokens[id] = storedManagedMCPToken{record, record.TokenValue}
	}
	return json.Marshal(struct {
		Tokens map[string]storedManagedMCPToken `json:"tokens"`
	}{tokens})
}
func (s *managedMCPTokenState) UnmarshalJSON(data []byte) error {
	var stored struct {
		Tokens map[string]storedManagedMCPToken `json:"tokens"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		return err
	}
	s.Tokens = make(map[string]managedMCPToken, len(stored.Tokens))
	for id, record := range stored.Tokens {
		record.managedMCPToken.TokenValue = record.Value
		s.Tokens[id] = record.managedMCPToken
	}
	return nil
}
func (s *Server) loadManagedMCPState() error { return s.refreshManagedMCPStateLocked() }
func (s *Server) refreshManagedMCPStateLocked() error {
	path := s.managedMCPStatePath()
	if path == "" {
		return nil
	}
	var state managedMCPTokenState
	if err := readAccessStateJSON(path, 8<<20, &state); err != nil {
		return err
	}
	s.managedMCP = cloneAccessRecords(state.Tokens)
	s.managedMCPPersisted = cloneAccessRecords(state.Tokens)
	return nil
}
func (s *Server) saveManagedMCPStateLocked() error {
	path := s.managedMCPStatePath()
	if path == "" {
		return nil
	}
	before := cloneAccessRecords(s.managedMCPPersisted)
	wanted := s.managedMCP
	latest := before
	err := withAccessStateLock(path, func() error {
		var state managedMCPTokenState
		if err := readAccessStateJSON(path, 8<<20, &state); err != nil {
			return err
		}
		latest = cloneAccessRecords(state.Tokens)
		merged, err := mergeAccessRecords(before, wanted, latest)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(latest, merged) {
			if err := writeAccessStateJSON(path, managedMCPTokenState{Tokens: merged}, 8<<20); err != nil {
				return err
			}
		}
		latest = merged
		return nil
	})
	s.managedMCP = cloneAccessRecords(latest)
	s.managedMCPPersisted = cloneAccessRecords(latest)
	return err
}
func (s *Server) loadOAuthClientsState() error { return s.refreshOAuthClientsStateLocked() }
func (s *Server) refreshOAuthClientsStateLocked() error {
	path := s.oauthClientsStatePath()
	if path == "" {
		return nil
	}
	var state oauthClientsState
	if err := readAccessStateJSON(path, oauthClientsStateMaxBytes, &state); err != nil {
		return err
	}
	if len(state.Clients) > oauthClientsMaxItems {
		return errors.New("too many OAuth clients")
	}
	for id, metadata := range state.Clients {
		if err := validateOAuthClientMetadata(id, metadata); err != nil {
			return err
		}
	}
	s.oauthClients = cloneAccessRecords(state.Clients)
	s.oauthClientsPersisted = cloneAccessRecords(state.Clients)
	return nil
}
func (s *Server) saveOAuthClientsStateLocked() error {
	path := s.oauthClientsStatePath()
	if path == "" {
		return nil
	}
	before := cloneAccessRecords(s.oauthClientsPersisted)
	wanted := s.oauthClients
	latest := before
	err := withAccessStateLock(path, func() error {
		var state oauthClientsState
		if err := readAccessStateJSON(path, oauthClientsStateMaxBytes, &state); err != nil {
			return err
		}
		latest = cloneAccessRecords(state.Clients)
		merged, err := mergeAccessRecords(before, wanted, latest)
		if err != nil {
			return err
		}
		if len(merged) > oauthClientsMaxItems {
			return errors.New("too many OAuth clients")
		}
		if !reflect.DeepEqual(latest, merged) {
			if err := writeAccessStateJSON(path, oauthClientsState{Clients: merged}, oauthClientsStateMaxBytes); err != nil {
				return err
			}
		}
		latest = merged
		return nil
	})
	s.oauthClients = cloneAccessRecords(latest)
	s.oauthClientsPersisted = cloneAccessRecords(latest)
	return err
}

// Called without s.mu before accepting a request, and by profile resolution.
// Read failure is a failure, never permission to reuse an obsolete allow rule.
func (s *Server) refreshAccessState() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshManagedMCPStateLocked(); err != nil {
		return err
	}
	if err := s.refreshOAuthClientsStateLocked(); err != nil {
		return err
	}
	if path := s.accessProfilesStatePath(); path != "" {
		profiles, err := readAccessProfilesState(path)
		if err != nil {
			return err
		}
		s.accessProfiles = profiles
	}
	return nil
}
