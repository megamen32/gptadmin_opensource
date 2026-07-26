package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const (
	instructionSetsStateFilename = "instruction_sets_state.json"
	instructionSetsStateMaxBytes = 128 << 10
)

var instructionSetsProcessMu sync.Mutex

type instructionSetsState struct {
	Sets map[string]InstructionSet `json:"sets"`
}

func (s *Server) instructionSetsStatePath() string {
	if s.cfg.InstructionSetsStateFile != "" {
		return s.cfg.InstructionSetsStateFile
	}
	if s.cfg.ConfigDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.ConfigDir, instructionSetsStateFilename)
}

func validateNamedInstructionSetID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" || id == defaultInstructionSetID || len([]byte(id)) > accessProfileMaxStringBytes {
		return errors.New("invalid named instruction set id")
	}
	if strings.ContainsAny(id, "/\\") {
		return errors.New("instruction set id must not contain path separators")
	}
	return nil
}

func cloneInstructionSet(set InstructionSet) InstructionSet {
	if set.UpdatedAt != nil {
		updated := set.UpdatedAt.UTC()
		set.UpdatedAt = &updated
	}
	return set
}

func cloneInstructionSets(sets map[string]InstructionSet) map[string]InstructionSet {
	cloned := make(map[string]InstructionSet, len(sets))
	for id, set := range sets {
		cloned[id] = cloneInstructionSet(set)
	}
	return cloned
}

func readInstructionSetsState(path string) (map[string]InstructionSet, error) {
	sets := map[string]InstructionSet{}
	if path == "" {
		return sets, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return sets, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) > instructionSetsStateMaxBytes {
		return nil, fmt.Errorf("instruction sets state exceeds %d bytes", instructionSetsStateMaxBytes)
	}
	var state instructionSetsState
	if err := json.Unmarshal(b, &state); err != nil {
		return nil, err
	}
	for id, set := range state.Sets {
		if err := validateNamedInstructionSetID(id); err != nil || set.ID != id {
			return nil, fmt.Errorf("invalid instruction set %q", id)
		}
		if err := validateInstructionContent(set.Content); err != nil {
			return nil, fmt.Errorf("invalid instruction set %q: %w", id, err)
		}
		if set.Version != instructionSetVersion(set.Content) {
			return nil, fmt.Errorf("instruction set %q version does not match content", id)
		}
		sets[id] = cloneInstructionSet(set)
	}
	return sets, nil
}

func writeInstructionSetsStateAtomic(path string, sets map[string]InstructionSet) error {
	if path == "" {
		return nil
	}
	state := instructionSetsState{Sets: cloneInstructionSets(sets)}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if len(b) > instructionSetsStateMaxBytes {
		return fmt.Errorf("instruction sets state exceeds %d bytes", instructionSetsStateMaxBytes)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".instruction-sets-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *Server) loadInstructionSetsState() error {
	sets, err := readInstructionSetsState(s.instructionSetsStatePath())
	if err != nil {
		return err
	}
	s.instructionSetsMu.Lock()
	s.instructionSets = sets
	s.instructionSetsMu.Unlock()
	return nil
}

func (s *Server) instructionSetExists(id string) bool {
	if id == defaultInstructionSetID || id == "" {
		return true
	}
	s.instructionSetsMu.RLock()
	_, ok := s.instructionSets[id]
	s.instructionSetsMu.RUnlock()
	return ok
}

func (s *Server) namedInstructionSetSnapshot(id string) (InstructionSet, bool) {
	s.instructionSetsMu.RLock()
	set, ok := s.instructionSets[id]
	s.instructionSetsMu.RUnlock()
	if !ok {
		return InstructionSet{}, false
	}
	return cloneInstructionSet(set), true
}

func (s *Server) instructionSetForRequest(r *http.Request) InstructionSet {
	profile, ok := AccessProfileFromRequest(r)
	if !ok || profile.InstructionSetID == "" || profile.InstructionSetID == defaultInstructionSetID {
		return s.instructionSetSnapshot()
	}
	if set, found := s.namedInstructionSetSnapshot(profile.InstructionSetID); found {
		return set
	}
	// A missing named set is a corrupt or stale profile reference. Instructions
	// are advisory, so retain the safe default rather than serving arbitrary data.
	return s.instructionSetSnapshot()
}

func (s *Server) startupInstructionsTextForRequest(r *http.Request) string {
	content := s.instructionSetForRequest(r).Content
	if strings.TrimSpace(content) == "" {
		return defaultStartupInstructions
	}
	return content
}

func instructionSetIDFromPath(r *http.Request) (string, error) {
	raw := strings.TrimPrefix(r.URL.Path, "/admin/api/instruction-sets/")
	if raw == "" || strings.Contains(raw, "/") {
		return "", errors.New("invalid instruction set id")
	}
	id, err := url.PathUnescape(raw)
	if err != nil {
		return "", errors.New("invalid instruction set id")
	}
	if err := validateNamedInstructionSetID(id); err != nil {
		return "", err
	}
	return strings.TrimSpace(id), nil
}

func (s *Server) updateInstructionSets(update func(map[string]InstructionSet) error) error {
	instructionSetsProcessMu.Lock()
	defer instructionSetsProcessMu.Unlock()
	path := s.instructionSetsStatePath()
	var lock *os.File
	var err error
	if path != "" {
		lock, err = AcquireUpdateLock(path + ".lock")
		if err != nil {
			return err
		}
		defer ReleaseUpdateLock(lock)
	}
	var sets map[string]InstructionSet
	if path == "" {
		s.instructionSetsMu.RLock()
		sets = cloneInstructionSets(s.instructionSets)
		s.instructionSetsMu.RUnlock()
	} else {
		sets, err = readInstructionSetsState(path)
		if err != nil {
			return err
		}
	}
	if err := update(sets); err != nil {
		return err
	}
	if path != "" {
		if err := writeInstructionSetsStateAtomic(path, sets); err != nil {
			return err
		}
	}
	s.instructionSetsMu.Lock()
	s.instructionSets = cloneInstructionSets(sets)
	s.instructionSetsMu.Unlock()
	return nil
}

func (s *Server) adminInstructionSets(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	items := []InstructionSet{s.instructionSetSnapshot()}
	s.instructionSetsMu.RLock()
	for _, set := range s.instructionSets {
		items = append(items, cloneInstructionSet(set))
	}
	s.instructionSetsMu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	writeJSON(w, http.StatusOK, map[string]any{"instruction_sets": items})
}

func (s *Server) adminInstructionSet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, err := instructionSetIDFromPath(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	switch r.Method {
	case http.MethodGet:
		set, ok := s.namedInstructionSetSnapshot(id)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "instruction set not found"})
			return
		}
		w.Header().Set("ETag", instructionSetETag(set.Version))
		writeJSON(w, http.StatusOK, set)
	case http.MethodPut:
		if strings.TrimSpace(r.Header.Get("If-Match")) == "" {
			writeJSON(w, http.StatusPreconditionRequired, map[string]any{"detail": "If-Match is required"})
			return
		}
		var req struct {
			Content *string `json:"content"`
		}
		if err := readInstructionSetJSON(r, &req); err != nil || req.Content == nil {
			if err == nil {
				err = errors.New("content must be a string")
			}
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		content := strings.TrimSpace(*req.Content)
		if err := validateInstructionContent(content); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		now := s.now().UTC()
		saved := InstructionSet{ID: id, Content: content, Version: instructionSetVersion(content), UpdatedAt: &now}
		match := strings.TrimSpace(r.Header.Get("If-Match"))
		err = s.updateInstructionSets(func(sets map[string]InstructionSet) error {
			current, exists := sets[id]
			if match == "*" {
				if exists {
					return &accessProfileHTTPError{status: http.StatusPreconditionFailed, detail: "instruction set already exists"}
				}
			} else if !exists {
				return &accessProfileHTTPError{status: http.StatusNotFound, detail: "instruction set not found"}
			} else if !ifMatchVersion(match, current.Version) {
				return &accessProfileHTTPError{status: http.StatusPreconditionFailed, detail: "instruction set version does not match"}
			}
			sets[id] = saved
			return nil
		})
		if err != nil {
			if httpErr, ok := err.(*accessProfileHTTPError); ok {
				writeJSON(w, httpErr.status, map[string]any{"detail": httpErr.detail})
			} else {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
			}
			return
		}
		w.Header().Set("ETag", instructionSetETag(saved.Version))
		writeJSON(w, http.StatusOK, saved)
	case http.MethodDelete:
		var inUse bool
		s.mu.Lock()
		for _, profile := range s.accessProfiles {
			if profile.InstructionSetID == id {
				inUse = true
				break
			}
		}
		s.mu.Unlock()
		if inUse {
			writeJSON(w, http.StatusConflict, map[string]any{"detail": "instruction set is referenced by an access profile"})
			return
		}
		err = s.updateInstructionSets(func(sets map[string]InstructionSet) error {
			if _, ok := sets[id]; !ok {
				return &accessProfileHTTPError{status: http.StatusNotFound, detail: "instruction set not found"}
			}
			delete(sets, id)
			return nil
		})
		if err != nil {
			if httpErr, ok := err.(*accessProfileHTTPError); ok {
				writeJSON(w, httpErr.status, map[string]any{"detail": httpErr.detail})
			} else {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
	}
}
