package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	Role         string   `json:"role,omitempty"`
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
	if err := s.refreshOAuthClientsStateLocked(); err != nil {
		s.mu.Unlock()
		return false
	}
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
		Role:         metadata.Role,
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
