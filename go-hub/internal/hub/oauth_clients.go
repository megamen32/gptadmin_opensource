package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	oauthClientsStateFilename = "oauth_clients_state.json"
	oauthClientsStateMaxBytes = 128 << 10
	oauthClientsMaxItems      = 256
	oauthRedirectMaxItems     = 16
	oauthRedirectMaxBytes     = 2048
)

// oauthClientMetadata contains only registration metadata. Client secrets and
// issued bearer tokens deliberately have no field in this durable state.
type oauthClientMetadata struct {
	RedirectURIs []string `json:"redirect_uris,omitempty"`
	ProfileID    string   `json:"profile_id,omitempty"`
	CreatedAt    int64    `json:"created_at"`
}

type oauthClientsState struct {
	Clients map[string]oauthClientMetadata `json:"clients"`
}

func (s *Server) oauthClientsStatePath() string {
	if s.cfg.ConfigDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.ConfigDir, oauthClientsStateFilename)
}

func (s *Server) loadOAuthClientsState() error {
	path := s.oauthClientsStatePath()
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(b) > oauthClientsStateMaxBytes {
		return fmt.Errorf("OAuth client state exceeds %d bytes", oauthClientsStateMaxBytes)
	}
	var state oauthClientsState
	if err := json.Unmarshal(b, &state); err != nil {
		return err
	}
	if len(state.Clients) > oauthClientsMaxItems {
		return fmt.Errorf("OAuth client state contains too many clients")
	}
	for clientID, metadata := range state.Clients {
		if err := validateOAuthClientMetadata(clientID, metadata); err != nil {
			return err
		}
		s.oauthClients[clientID] = cloneOAuthClientMetadata(metadata)
	}
	return nil
}

func (s *Server) saveOAuthClientsStateLocked() error {
	path := s.oauthClientsStatePath()
	if path == "" {
		return nil
	}
	state := oauthClientsState{Clients: make(map[string]oauthClientMetadata, len(s.oauthClients))}
	for clientID, metadata := range s.oauthClients {
		state.Clients[clientID] = cloneOAuthClientMetadata(metadata)
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if len(b) > oauthClientsStateMaxBytes {
		return fmt.Errorf("OAuth client state exceeds %d bytes", oauthClientsStateMaxBytes)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".oauth-clients-*")
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

func cloneOAuthClientMetadata(metadata oauthClientMetadata) oauthClientMetadata {
	metadata.RedirectURIs = append([]string(nil), metadata.RedirectURIs...)
	return metadata
}

func validateOAuthClientMetadata(clientID string, metadata oauthClientMetadata) error {
	if strings.TrimSpace(clientID) == "" || len([]byte(clientID)) > accessProfileMaxStringBytes {
		return errors.New("invalid OAuth client_id in state")
	}
	if metadata.CreatedAt <= 0 {
		return fmt.Errorf("invalid OAuth client %q created_at", clientID)
	}
	if _, err := validateOAuthRedirectURIs(metadata.RedirectURIs); err != nil {
		return fmt.Errorf("invalid OAuth client %q: %w", clientID, err)
	}
	if len([]byte(metadata.ProfileID)) > accessProfileMaxStringBytes {
		return fmt.Errorf("invalid OAuth client %q profile_id", clientID)
	}
	return nil
}

func validateOAuthRedirectURIs(values []string) ([]string, error) {
	if len(values) > oauthRedirectMaxItems {
		return nil, errors.New("too many redirect_uris")
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len([]byte(value)) > oauthRedirectMaxBytes {
			return nil, errors.New("redirect_uri is empty or too long")
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out, nil
}

func oauthRedirectURIsFromRequest(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, errors.New("redirect_uris must be an array")
	}
	redirects := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, errors.New("redirect_uris must contain strings")
		}
		redirects = append(redirects, text)
	}
	return validateOAuthRedirectURIs(redirects)
}

func (s *Server) oauthClientAllowsRedirect(clientID, redirectURI string) bool {
	s.mu.Lock()
	metadata, registered := s.oauthClients[clientID]
	s.mu.Unlock()
	if !registered {
		return true
	}
	for _, allowed := range metadata.RedirectURIs {
		if allowed == redirectURI {
			return true
		}
	}
	return false
}

func (s *Server) oauthClientProfileID(clientID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.oauthClients[clientID].ProfileID
}

func oauthClientInventory(metadata oauthClientMetadata, clientID string) managedMCPToken {
	return managedMCPToken{
		ID:           clientID,
		ClientID:     clientID,
		TokenKind:    "oauth",
		Status:       "registered",
		RedirectURIs: append([]string(nil), metadata.RedirectURIs...),
		ProfileID:    metadata.ProfileID,
		IssuedAt:     metadata.CreatedAt,
		CreatedAt:    metadata.CreatedAt,
	}
}

func readOAuthRegistrationJSON(r *http.Request, dst any) error {
	body, err := io.ReadAll(http.MaxBytesReader(nilWriter{}, r.Body, oauthClientsStateMaxBytes))
	if err != nil {
		return err
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return errors.New("empty JSON body")
	}
	return json.Unmarshal(body, dst)
}
