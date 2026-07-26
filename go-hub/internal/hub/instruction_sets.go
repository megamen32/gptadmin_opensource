package hub

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const defaultInstructionSetID = "default"
const instructionSetJSONMaxBytes = startupInstructionsMaxBytes*6 + 1024

// InstructionSet is the persisted, owner-managed MCP instruction document.
type InstructionSet struct {
	ID        string     `json:"id"`
	Content   string     `json:"content"`
	Version   string     `json:"version"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

func newInstructionSet(cfg Config) InstructionSet {
	content, updatedAt := loadStartupInstructionsWithUpdatedAt(cfg)
	return InstructionSet{ID: defaultInstructionSetID, Content: content, Version: instructionSetVersion(content), UpdatedAt: updatedAt}
}

func newFileInstructionSet(path string) InstructionSet {
	content, updatedAt := loadStartupInstructionsFileWithUpdatedAt(path)
	return InstructionSet{ID: defaultInstructionSetID, Content: content, Version: instructionSetVersion(content), UpdatedAt: updatedAt}
}

func instructionSetVersion(content string) string {
	digest := sha256.Sum256([]byte(content))
	return hex.EncodeToString(digest[:])
}

func instructionSetETag(version string) string {
	return `"` + version + `"`
}

func ifMatchVersion(value, currentVersion string) bool {
	for _, candidate := range strings.Split(value, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == currentVersion || candidate == instructionSetETag(currentVersion) {
			return true
		}
	}
	return false
}

func validateInstructionContent(content string) error {
	if content == "" {
		return errors.New("content must not be empty")
	}
	if !utf8.ValidString(content) {
		return errors.New("content must be valid UTF-8")
	}
	if len([]byte(content)) > startupInstructionsMaxBytes {
		return fmt.Errorf("content exceeds %d bytes", startupInstructionsMaxBytes)
	}
	return nil
}

type instructionWriteResult struct {
	UpdatedAt *time.Time
	Committed bool
}

func writeInstructionContentAtomic(path string, content string) (instructionWriteResult, error) {
	if path == "" {
		return instructionWriteResult{}, errors.New("startup instructions file path is empty")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return instructionWriteResult{}, fmt.Errorf("create startup instructions directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".startup-instructions-*")
	if err != nil {
		return instructionWriteResult{}, fmt.Errorf("create startup instructions temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return instructionWriteResult{}, fmt.Errorf("set startup instructions permissions: %w", err)
	}
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return instructionWriteResult{}, fmt.Errorf("write startup instructions: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return instructionWriteResult{}, fmt.Errorf("sync startup instructions: %w", err)
	}
	info, err := tmp.Stat()
	if err != nil {
		tmp.Close()
		return instructionWriteResult{}, fmt.Errorf("stat startup instructions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return instructionWriteResult{}, fmt.Errorf("close startup instructions: %w", err)
	}
	if err := replaceInstructionFile(tmpName, path); err != nil {
		return instructionWriteResult{}, fmt.Errorf("publish startup instructions: %w", err)
	}
	updatedAt := info.ModTime().UTC()
	result := instructionWriteResult{UpdatedAt: &updatedAt, Committed: true}
	if err := syncInstructionDirectory(dir); err != nil {
		return result, fmt.Errorf("sync startup instructions directory: %w", err)
	}
	return result, nil
}

func (s *Server) instructionSetSnapshot() InstructionSet {
	if _, inline := effectiveInlineStartupInstructions(s.cfg.StartupInstructions); !inline {
		current := newFileInstructionSet(s.cfg.StartupInstructionsFile)
		s.instructionMu.Lock()
		s.instructionSet = current
		s.instructionMu.Unlock()
		return current
	}
	s.instructionMu.RLock()
	defer s.instructionMu.RUnlock()
	return s.instructionSet
}

func (s *Server) adminDefaultInstructionSet(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet || r.Method == http.MethodPut {
		w.Header().Set("Cache-Control", "no-store")
	}
	switch r.Method {
	case http.MethodGet:
		set := s.instructionSetSnapshot()
		w.Header().Set("ETag", instructionSetETag(set.Version))
		writeJSON(w, http.StatusOK, set)
	case http.MethodPut:
		if strings.TrimSpace(r.Header.Get("If-Match")) == "" {
			writeJSON(w, http.StatusPreconditionRequired, map[string]any{"detail": "If-Match is required"})
			return
		}
		var req *struct {
			Content *string `json:"content"`
		}
		if err := readInstructionSetJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		if req == nil || req.Content == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "content must be a string"})
			return
		}
		content := strings.TrimSpace(*req.Content)
		if err := validateInstructionContent(content); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		if _, ok := effectiveInlineStartupInstructions(s.cfg.StartupInstructions); ok {
			writeJSON(w, http.StatusConflict, map[string]any{"detail": "inline startup instructions override file updates"})
			return
		}

		s.instructionWriteMu.Lock()
		defer s.instructionWriteMu.Unlock()
		lock, err := acquireInstructionFileLock(s.cfg.StartupInstructionsFile)
		if err != nil {
			if errors.Is(err, errInstructionFileLockBusy) {
				writeJSON(w, http.StatusLocked, map[string]any{"detail": "startup instructions file is being updated"})
			} else {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
			}
			return
		}
		defer func() {
			if releaseErr := releaseInstructionFileLock(lock); releaseErr != nil {
				log.Printf("startup instructions lock release failed path=%s err=%v", s.cfg.StartupInstructionsFile, releaseErr)
			}
		}()

		current := newFileInstructionSet(s.cfg.StartupInstructionsFile)
		s.instructionMu.Lock()
		s.instructionSet = current
		s.instructionMu.Unlock()
		if !ifMatchVersion(r.Header.Get("If-Match"), current.Version) {
			writeJSON(w, http.StatusPreconditionFailed, map[string]any{"detail": "instruction set version does not match"})
			return
		}
		writeResult, err := writeInstructionContentAtomic(s.cfg.StartupInstructionsFile, content)
		if err != nil && !writeResult.Committed {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
			return
		}
		if err != nil {
			log.Printf("startup instructions directory sync failed after commit path=%s err=%v", s.cfg.StartupInstructionsFile, err)
		}
		published := InstructionSet{ID: defaultInstructionSetID, Content: content, Version: instructionSetVersion(content), UpdatedAt: writeResult.UpdatedAt}
		s.instructionMu.Lock()
		s.instructionSet = published
		s.instructionMu.Unlock()
		w.Header().Set("ETag", instructionSetETag(published.Version))
		writeJSON(w, http.StatusOK, published)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
	}
}

func readInstructionSetJSON(r *http.Request, dst any) error {
	body, err := io.ReadAll(http.MaxBytesReader(nilWriter{}, r.Body, instructionSetJSONMaxBytes))
	if err != nil {
		return err
	}
	if !utf8.Valid(body) {
		return errors.New("request body must be valid UTF-8")
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return errors.New("empty JSON body")
	}
	return json.Unmarshal(body, dst)
}
