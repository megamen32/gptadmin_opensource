package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	accessProfilesStateFilename = "access_profiles_state.json"
	accessProfileJSONMaxBytes   = 64 << 10
	accessProfileMaxListItems   = 256
	accessProfileMaxStringBytes = 256
	accessProfileStateMaxBytes  = 128 << 10
)

var (
	errAccessProfilesLockBusy = errors.New("access profiles file lock busy")
	accessProfilesProcessMu   sync.Mutex
)

// WorkspaceRef identifies an external workspace without copying its contents
// into Hub state.
type WorkspaceRef struct {
	MachineID       string `json:"machine_id"`
	WorkspacePath   string `json:"workspace_path"`
	StartupDocument string `json:"startup_document"`
	ShellTarget     string `json:"shell_target"`
}

// AccessProfile is the durable authorization profile associated with a
// managed MCP client. Policy evaluation is intentionally implemented by the
// later policy worker; this type only carries its stable inputs.
type AccessProfile struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	InstructionSetID string         `json:"instruction_set_id"`
	AccessMode       string         `json:"access_mode"`
	ApprovalMode     string         `json:"approval_mode"`
	AllowedTargets   []string       `json:"allowed_targets"`
	AllowedTools     []string       `json:"allowed_tools"`
	WorkspaceRefs    []WorkspaceRef `json:"workspace_refs"`
	Version          int64          `json:"version"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

type accessProfilesState struct {
	Profiles map[string]AccessProfile `json:"profiles"`
}

type accessProfileRequest struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	InstructionSetID string         `json:"instruction_set_id"`
	AccessMode       string         `json:"access_mode"`
	ApprovalMode     string         `json:"approval_mode"`
	AllowedTargets   []string       `json:"allowed_targets"`
	AllowedTools     []string       `json:"allowed_tools"`
	WorkspaceRefs    []WorkspaceRef `json:"workspace_refs"`
}

type clientBindingRequest struct {
	ProfileID string `json:"profile_id"`
}

type accessProfileContextKey struct{}

// AccessProfileFromRequest returns the profile snapshot attached during JWT
// authentication. The copy prevents a policy worker from mutating request
// state shared with another consumer.
func AccessProfileFromRequest(r *http.Request) (AccessProfile, bool) {
	if r == nil {
		return AccessProfile{}, false
	}
	profile, ok := r.Context().Value(accessProfileContextKey{}).(AccessProfile)
	if !ok {
		return AccessProfile{}, false
	}
	return cloneAccessProfile(profile), true
}

// AccessProfileIDFromRequest returns the profile ID selected for this request,
// or an empty string when the managed token is not bound to a profile.
func AccessProfileIDFromRequest(r *http.Request) string {
	profile, ok := AccessProfileFromRequest(r)
	if !ok {
		return ""
	}
	return profile.ID
}

func cloneAccessProfile(profile AccessProfile) AccessProfile {
	profile.AllowedTargets = append([]string(nil), profile.AllowedTargets...)
	profile.AllowedTools = append([]string(nil), profile.AllowedTools...)
	profile.WorkspaceRefs = append([]WorkspaceRef(nil), profile.WorkspaceRefs...)
	return profile
}

func requestWithAccessProfile(r *http.Request, profile AccessProfile) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), accessProfileContextKey{}, cloneAccessProfile(profile)))
}

func (s *Server) accessProfilesStatePath() string {
	if s.cfg.ConfigDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.ConfigDir, accessProfilesStateFilename)
}

func readAccessProfilesState(statePath string) (map[string]AccessProfile, error) {
	profiles := map[string]AccessProfile{}
	if statePath == "" {
		return profiles, nil
	}
	b, err := os.ReadFile(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return profiles, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) > accessProfileStateMaxBytes {
		return nil, fmt.Errorf("access profile state exceeds %d bytes", accessProfileStateMaxBytes)
	}
	var state accessProfilesState
	if err := json.Unmarshal(b, &state); err != nil {
		return nil, err
	}
	for id, profile := range state.Profiles {
		if id == "" || profile.ID != id || profile.Version < 1 {
			return nil, fmt.Errorf("invalid access profile %q in state", id)
		}
		normalized, err := normalizeAccessProfile(profile)
		if err != nil {
			return nil, fmt.Errorf("invalid access profile %q in state: %w", id, err)
		}
		profiles[id] = cloneAccessProfile(normalized)
	}
	return profiles, nil
}

func cloneAccessProfiles(profiles map[string]AccessProfile) map[string]AccessProfile {
	cloned := make(map[string]AccessProfile, len(profiles))
	for id, profile := range profiles {
		cloned[id] = cloneAccessProfile(profile)
	}
	return cloned
}

func (s *Server) loadAccessProfilesState() error {
	profiles, err := readAccessProfilesState(s.accessProfilesStatePath())
	if err != nil {
		return err
	}
	for id, profile := range profiles {
		if !s.instructionSetExists(profile.InstructionSetID) {
			return fmt.Errorf("access profile %q references missing instruction set %q", id, profile.InstructionSetID)
		}
	}
	s.mu.Lock()
	s.accessProfiles = profiles
	s.mu.Unlock()
	return nil
}

func acquireAccessProfilesFileLock(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	lock, err := AcquireUpdateLock(path + ".lock")
	if err != nil {
		if isInstructionLockBusy(err) {
			return nil, fmt.Errorf("%w: %v", errAccessProfilesLockBusy, err)
		}
		return nil, err
	}
	return lock, nil
}

func writeAccessProfilesStateAtomic(filename string, data []byte) error {
	if len(data) > accessProfileStateMaxBytes {
		return fmt.Errorf("access profile state exceeds %d bytes", accessProfileStateMaxBytes)
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(filename), ".access-profiles-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
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
	if err := replaceInstructionFile(tmpName, filename); err != nil {
		return err
	}
	return syncInstructionDirectory(filepath.Dir(filename))
}

func saveAccessProfilesState(path string, profiles map[string]AccessProfile) error {
	state := accessProfilesState{Profiles: cloneAccessProfiles(profiles)}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeAccessProfilesStateAtomic(path, append(b, '\n'))
}

func (s *Server) accessProfilesSnapshot() (map[string]AccessProfile, error) {
	accessProfilesProcessMu.Lock()
	defer accessProfilesProcessMu.Unlock()
	path := s.accessProfilesStatePath()
	if path == "" {
		s.mu.Lock()
		profiles := cloneAccessProfiles(s.accessProfiles)
		s.mu.Unlock()
		return profiles, nil
	}
	lock, err := acquireAccessProfilesFileLock(path)
	if err != nil {
		return nil, err
	}
	profiles, readErr := readAccessProfilesState(path)
	releaseErr := ReleaseUpdateLock(lock)
	if readErr != nil {
		return nil, readErr
	}
	if releaseErr != nil {
		return nil, releaseErr
	}
	s.mu.Lock()
	s.accessProfiles = profiles
	s.mu.Unlock()
	return cloneAccessProfiles(profiles), nil
}

func (s *Server) updateAccessProfiles(mutator func(map[string]AccessProfile) error) (map[string]AccessProfile, error) {
	accessProfilesProcessMu.Lock()
	defer accessProfilesProcessMu.Unlock()
	path := s.accessProfilesStatePath()
	if path == "" {
		s.mu.Lock()
		profiles := cloneAccessProfiles(s.accessProfiles)
		s.mu.Unlock()
		if err := mutator(profiles); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.accessProfiles = cloneAccessProfiles(profiles)
		s.mu.Unlock()
		return profiles, nil
	}
	lock, err := acquireAccessProfilesFileLock(path)
	if err != nil {
		return nil, err
	}
	profiles, err := readAccessProfilesState(path)
	if err != nil {
		_ = ReleaseUpdateLock(lock)
		return nil, err
	}
	if err := mutator(profiles); err != nil {
		_ = ReleaseUpdateLock(lock)
		return nil, err
	}
	err = saveAccessProfilesState(path, profiles)
	if err != nil {
		_ = ReleaseUpdateLock(lock)
		return nil, err
	}
	if err := ReleaseUpdateLock(lock); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.accessProfiles = cloneAccessProfiles(profiles)
	s.mu.Unlock()
	return profiles, nil
}

func accessProfileETag(version int64) string {
	return fmt.Sprintf(`"%d"`, version)
}

func readBoundedJSON(r *http.Request, dst any) error {
	body, err := io.ReadAll(http.MaxBytesReader(nilWriter{}, r.Body, accessProfileJSONMaxBytes))
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

func normalizeAccessProfileList(values []string, field string) ([]string, error) {
	if len(values) > accessProfileMaxListItems {
		return nil, fmt.Errorf("%s has too many entries", field)
	}
	result := make([]string, len(values))
	for i, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("%s must not contain empty entries", field)
		}
		if len([]byte(value)) > accessProfileMaxStringBytes {
			return nil, fmt.Errorf("%s entry exceeds %d bytes", field, accessProfileMaxStringBytes)
		}
		result[i] = value
	}
	return result, nil
}

func normalizeAccessProfileString(value, field string, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if required {
			return "", fmt.Errorf("%s must not be empty", field)
		}
		return "", nil
	}
	if !utf8.ValidString(value) || len([]byte(value)) > accessProfileMaxStringBytes {
		return "", fmt.Errorf("%s must be valid and bounded", field)
	}
	return value, nil
}

func normalizeWorkspaceRefs(refs []WorkspaceRef) ([]WorkspaceRef, error) {
	if len(refs) > accessProfileMaxListItems {
		return nil, errors.New("workspace_refs has too many entries")
	}
	normalized := make([]WorkspaceRef, len(refs))
	for i, ref := range refs {
		machineID, err := normalizeAccessProfileString(ref.MachineID, "workspace_refs.machine_id", true)
		if err != nil {
			return nil, err
		}
		workspacePath, err := normalizeAccessProfileString(ref.WorkspacePath, "workspace_refs.workspace_path", true)
		if err != nil {
			return nil, err
		}
		startupDocument, err := normalizeAccessProfileString(ref.StartupDocument, "workspace_refs.startup_document", false)
		if err != nil {
			return nil, err
		}
		if startupDocument == "" {
			startupDocument = "AGENTS.md"
		}
		shellTarget, err := normalizeAccessProfileString(ref.ShellTarget, "workspace_refs.shell_target", true)
		if err != nil {
			return nil, err
		}
		normalized[i] = WorkspaceRef{MachineID: machineID, WorkspacePath: workspacePath, StartupDocument: startupDocument, ShellTarget: shellTarget}
	}
	return normalized, nil
}

func normalizeAccessProfile(profile AccessProfile) (AccessProfile, error) {
	id, err := normalizeAccessProfileString(profile.ID, "id", true)
	if err != nil {
		return AccessProfile{}, err
	}
	name, err := normalizeAccessProfileString(profile.Name, "name", false)
	if err != nil {
		return AccessProfile{}, err
	}
	if name == "" {
		name = id
	}
	instructionSetID, err := normalizeAccessProfileString(profile.InstructionSetID, "instruction_set_id", false)
	if err != nil {
		return AccessProfile{}, err
	}
	if instructionSetID == "" {
		instructionSetID = defaultInstructionSetID
	}
	if profile.AccessMode != accessModeFull && profile.AccessMode != accessModeReadonly {
		return AccessProfile{}, errors.New("access_mode must be full or readonly")
	}
	approvalMode := strings.ToLower(strings.TrimSpace(profile.ApprovalMode))
	if approvalMode == "" {
		approvalMode = approvalModeBoundedAutonomous
	}
	if profile.AccessMode == accessModeReadonly {
		approvalMode = approvalModeReadOnly
	}
	if approvalMode != approvalModeReadOnly && approvalMode != approvalModeAskBeforeWrite && approvalMode != approvalModeBoundedAutonomous {
		return AccessProfile{}, errors.New("approval_mode must be read_only, ask_before_write or bounded_autonomous")
	}
	if profile.Version < 1 {
		return AccessProfile{}, errors.New("version must be positive")
	}
	allowedTargets, err := normalizeAccessProfileList(profile.AllowedTargets, "allowed_targets")
	if err != nil {
		return AccessProfile{}, err
	}
	allowedTools, err := normalizeAccessProfileList(profile.AllowedTools, "allowed_tools")
	if err != nil {
		return AccessProfile{}, err
	}
	workspaceRefs, err := normalizeWorkspaceRefs(profile.WorkspaceRefs)
	if err != nil {
		return AccessProfile{}, err
	}
	profile.ID = id
	profile.Name = name
	profile.InstructionSetID = instructionSetID
	profile.ApprovalMode = approvalMode
	profile.AllowedTargets = allowedTargets
	profile.AllowedTools = allowedTools
	profile.WorkspaceRefs = workspaceRefs
	return profile, nil
}

func validateAccessProfile(profile AccessProfile) error {
	_, err := normalizeAccessProfile(profile)
	return err
}

func accessProfileIDFromPath(r *http.Request) (string, error) {
	raw := strings.TrimPrefix(r.URL.Path, "/admin/api/access-profiles/")
	if raw == "" || strings.Contains(raw, "/") {
		return "", errors.New("invalid access profile id")
	}
	id, err := url.PathUnescape(raw)
	if err != nil || strings.TrimSpace(id) == "" {
		return "", errors.New("invalid access profile id")
	}
	return strings.TrimSpace(id), nil
}

type accessProfileHTTPError struct {
	status int
	detail string
}

func (e *accessProfileHTTPError) Error() string { return e.detail }

func writeAccessProfileError(w http.ResponseWriter, err error) {
	var httpErr *accessProfileHTTPError
	if errors.As(err, &httpErr) {
		writeJSON(w, httpErr.status, map[string]any{"detail": httpErr.detail})
		return
	}
	if errors.Is(err, errAccessProfilesLockBusy) {
		writeJSON(w, http.StatusLocked, map[string]any{"detail": "access profiles are being updated"})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
}

func (s *Server) adminAccessProfiles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	profiles, err := s.accessProfilesSnapshot()
	if err != nil {
		writeAccessProfileError(w, err)
		return
	}
	items := make([]AccessProfile, 0, len(profiles))
	for _, profile := range profiles {
		items = append(items, cloneAccessProfile(profile))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	writeJSON(w, http.StatusOK, map[string]any{"profiles": items})
}

func (s *Server) adminAccessProfile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, err := accessProfileIDFromPath(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	switch r.Method {
	case http.MethodGet:
		profiles, err := s.accessProfilesSnapshot()
		if err != nil {
			writeAccessProfileError(w, err)
			return
		}
		profile, ok := profiles[id]
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "access profile not found"})
			return
		}
		w.Header().Set("ETag", accessProfileETag(profile.Version))
		writeJSON(w, http.StatusOK, cloneAccessProfile(profile))
	case http.MethodPut:
		if strings.TrimSpace(r.Header.Get("If-Match")) == "" {
			writeJSON(w, http.StatusPreconditionRequired, map[string]any{"detail": "If-Match is required"})
			return
		}
		var req accessProfileRequest
		if err := readBoundedJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		if req.ID != "" && strings.TrimSpace(req.ID) != id {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "profile id does not match path"})
			return
		}
		candidate, err := normalizeAccessProfile(AccessProfile{
			ID:               id,
			Name:             req.Name,
			InstructionSetID: req.InstructionSetID,
			AccessMode:       strings.ToLower(strings.TrimSpace(req.AccessMode)),
			ApprovalMode:     strings.ToLower(strings.TrimSpace(req.ApprovalMode)),
			AllowedTargets:   req.AllowedTargets,
			AllowedTools:     req.AllowedTools,
			WorkspaceRefs:    req.WorkspaceRefs,
			Version:          1,
			UpdatedAt:        time.Now().UTC(),
		})
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		if !s.instructionSetExists(candidate.InstructionSetID) {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "instruction set not found"})
			return
		}
		var saved AccessProfile
		_, err = s.updateAccessProfiles(func(profiles map[string]AccessProfile) error {
			current, exists := profiles[id]
			match := strings.TrimSpace(r.Header.Get("If-Match"))
			if match == "*" {
				if exists {
					return &accessProfileHTTPError{status: http.StatusPreconditionFailed, detail: "access profile already exists"}
				}
			} else if !exists {
				return &accessProfileHTTPError{status: http.StatusNotFound, detail: "access profile not found"}
			} else if match != accessProfileETag(current.Version) {
				return &accessProfileHTTPError{status: http.StatusPreconditionFailed, detail: "access profile version does not match"}
			}
			candidate.Version = 1
			if exists {
				candidate.Version = current.Version + 1
			}
			profiles[id] = candidate
			saved = cloneAccessProfile(candidate)
			return nil
		})
		if err != nil {
			if _, ok := err.(*accessProfileHTTPError); ok {
				writeAccessProfileError(w, err)
				return
			}
			writeAccessProfileError(w, err)
			return
		}
		w.Header().Set("ETag", accessProfileETag(saved.Version))
		writeJSON(w, http.StatusOK, saved)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
	}
}

func clientBindingIDFromPath(r *http.Request) (string, error) {
	raw := strings.TrimPrefix(r.URL.Path, "/admin/api/client-bindings/")
	if raw == "" || strings.Contains(raw, "/") {
		return "", errors.New("invalid managed token id")
	}
	id, err := url.PathUnescape(raw)
	if err != nil || strings.TrimSpace(id) == "" {
		return "", errors.New("invalid managed token id")
	}
	return strings.TrimSpace(id), nil
}

func (s *Server) adminClientBinding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPut && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "PUT, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	id, err := clientBindingIDFromPath(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if r.Method == http.MethodDelete {
		s.mu.Lock()
		record, managed := s.managedMCP[id]
		oauthClient, oauth := s.oauthClients[id]
		if managed && record.ProfileID == "" {
			managed = false
		}
		if oauth && oauthClient.ProfileID == "" {
			oauth = false
		}
		if !managed && !oauth {
			s.mu.Unlock()
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "client binding not found"})
			return
		}
		var saveErr error
		if managed {
			previous := record.ProfileID
			record.ProfileID = ""
			s.managedMCP[id] = record
			saveErr = s.saveManagedMCPStateLocked()
			if saveErr != nil {
				record.ProfileID = previous
				s.managedMCP[id] = record
			}
		} else {
			previous := oauthClient.ProfileID
			oauthClient.ProfileID = ""
			s.oauthClients[id] = oauthClient
			saveErr = s.saveOAuthClientsStateLocked()
			if saveErr != nil {
				oauthClient.ProfileID = previous
				s.oauthClients[id] = oauthClient
			}
		}
		s.mu.Unlock()
		if saveErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": saveErr.Error()})
			return
		}
		response := map[string]any{"ok": true}
		if managed {
			response["token_id"] = id
		} else {
			response["client_id"] = id
		}
		writeJSON(w, http.StatusOK, response)
		return
	}
	var req clientBindingRequest
	if err := readBoundedJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	profileID := strings.TrimSpace(req.ProfileID)
	if profileID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "profile_id is required"})
		return
	}

	s.mu.Lock()
	if _, ok := s.accessProfiles[profileID]; !ok {
		s.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "access profile not found"})
		return
	}
	record, managed := s.managedMCP[id]
	oauthClient, oauth := s.oauthClients[id]
	if !managed && !oauth {
		s.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "MCP token or OAuth client not found"})
		return
	}
	var saveErr error
	if managed {
		previous := record.ProfileID
		record.ProfileID = profileID
		s.managedMCP[id] = record
		saveErr = s.saveManagedMCPStateLocked()
		if saveErr != nil {
			record.ProfileID = previous
			s.managedMCP[id] = record
		}
	} else {
		previous := oauthClient.ProfileID
		oauthClient.ProfileID = profileID
		s.oauthClients[id] = oauthClient
		saveErr = s.saveOAuthClientsStateLocked()
		if saveErr != nil {
			oauthClient.ProfileID = previous
			s.oauthClients[id] = oauthClient
		}
	}
	if saveErr != nil {
		s.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": saveErr.Error()})
		return
	}
	s.mu.Unlock()
	response := map[string]any{"profile_id": profileID}
	if managed {
		response["token_id"] = id
	} else {
		response["client_id"] = id
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) applyAccessProfileContext(r *http.Request, claims map[string]any) *http.Request {
	jti := firstString(claims, "jti")
	clientID := firstString(claims, "client_id")
	s.mu.Lock()
	record, managed := s.managedMCP[jti]
	profileID := ""
	if managed {
		profileID = record.ProfileID
	}
	if profileID == "" && clientID != "" {
		profileID = s.oauthClients[clientID].ProfileID
	}
	if profileID == "" {
		profileID = firstString(claims, "profile_id")
	}
	profile, bound := s.accessProfiles[profileID]
	s.mu.Unlock()
	if !bound {
		return r
	}
	claims["profile_id"] = profileID
	return requestWithAccessProfile(r, profile)
}
