package hub

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	webauthnlib "github.com/go-webauthn/webauthn/webauthn"
)

const (
	webAuthnStateFilename  = "webauthn_state.json"
	webAuthnSessionCookie  = "gptadmin_webauthn_session"
	webAuthnMFACookie      = "gptadmin_webauthn_mfa"
	webAuthnSessionTTL     = 5 * time.Minute
	webAuthnMaxCredentials = 16
)

type webAuthnState struct {
	Credentials []webauthnlib.Credential `json:"credentials,omitempty"`
	EnrolledAt  time.Time                `json:"enrolled_at,omitempty"`
}

type webAuthnSession struct {
	Kind    string
	Session webauthnlib.SessionData
}

type adminWebAuthnUser struct {
	credentials []webauthnlib.Credential
}

func (u adminWebAuthnUser) WebAuthnID() []byte {
	digest := sha256.Sum256([]byte("gptadmin-admin-webauthn-user"))
	return digest[:]
}

func (u adminWebAuthnUser) WebAuthnName() string        { return "admin" }
func (u adminWebAuthnUser) WebAuthnDisplayName() string { return "GPTAdmin administrator" }
func (u adminWebAuthnUser) WebAuthnCredentials() []webauthnlib.Credential {
	return append([]webauthnlib.Credential(nil), u.credentials...)
}

func defaultWebAuthnState() webAuthnState { return webAuthnState{} }

func loadWebAuthnState(path string) (webAuthnState, error) {
	state := defaultWebAuthnState()
	if path == "" {
		return state, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return state, errors.New("webauthn state is not a regular file")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return state, err
	}
	if len(data) > securityStateMaxBytes {
		return state, errors.New("webauthn state file is too large")
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return defaultWebAuthnState(), fmt.Errorf("decode WebAuthn state: %w", err)
	}
	if len(state.Credentials) > webAuthnMaxCredentials {
		return defaultWebAuthnState(), errors.New("too many WebAuthn credentials")
	}
	for _, credential := range state.Credentials {
		if len(credential.ID) == 0 || len(credential.PublicKey) == 0 {
			return defaultWebAuthnState(), errors.New("invalid WebAuthn credential")
		}
	}
	return state, nil
}

func saveWebAuthnState(path string, state webAuthnState) error {
	if path == "" {
		return nil
	}
	if len(state.Credentials) > webAuthnMaxCredentials {
		return errors.New("too many WebAuthn credentials")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".webauthn-state-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
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
	return os.Rename(tmpName, path)
}

func (s *Server) webAuthnUser() adminWebAuthnUser {
	s.mu.Lock()
	defer s.mu.Unlock()
	return adminWebAuthnUser{credentials: append([]webauthnlib.Credential(nil), s.webauthnState.Credentials...)}
}

func (s *Server) webAuthnConfigured(r *http.Request) (*webauthnlib.WebAuthn, error) {
	origin := strings.TrimRight(s.origin(r), "/")
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Hostname() == "" {
		return nil, errors.New("WebAuthn requires a configured Hub origin")
	}
	host := parsed.Hostname()
	if parsed.Scheme != "https" && host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return nil, errors.New("WebAuthn requires HTTPS outside loopback")
	}
	return webauthnlib.New(&webauthnlib.Config{
		RPDisplayName:        "GPTAdmin",
		RPID:                 host,
		RPOrigins:            []string{origin},
		EncodeUserIDAsString: false,
	})
}

func (s *Server) setWebAuthnSession(w http.ResponseWriter, r *http.Request, session webAuthnSession) error {
	token, err := randomSecretString(24)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.webauthnSessions[token] = session
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: webAuthnSessionCookie, Value: token, Path: "/admin", MaxAge: int(webAuthnSessionTTL.Seconds()), HttpOnly: true, Secure: isSecureRequest(r) || strings.HasPrefix(s.origin(r), "https://"), SameSite: http.SameSiteStrictMode})
	return nil
}

func (s *Server) takeWebAuthnSession(r *http.Request, kind string) (webauthnlib.SessionData, bool) {
	cookie, err := r.Cookie(webAuthnSessionCookie)
	if err != nil || cookie.Value == "" {
		return webauthnlib.SessionData{}, false
	}
	s.mu.Lock()
	session, ok := s.webauthnSessions[cookie.Value]
	delete(s.webauthnSessions, cookie.Value)
	s.mu.Unlock()
	if !ok || session.Kind != kind || (!session.Session.Expires.IsZero() && time.Now().After(session.Session.Expires)) {
		return webauthnlib.SessionData{}, false
	}
	return session.Session, true
}

func (s *Server) adminWebAuthnRegisterBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	webauthn, err := s.webAuthnConfigured(r)
	if err != nil {
		writeJSON(w, http.StatusPreconditionFailed, map[string]any{"detail": err.Error()})
		return
	}
	user := s.webAuthnUser()
	creation, session, err := webauthn.BeginRegistration(user, webauthnlib.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired), webauthnlib.WithExclusions(webauthnlib.Credentials(user.WebAuthnCredentials()).CredentialDescriptors()))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "failed to begin WebAuthn registration"})
		return
	}
	if err := s.setWebAuthnSession(w, r, webAuthnSession{Kind: "register", Session: *session}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "failed to persist WebAuthn ceremony"})
		return
	}
	writeJSON(w, http.StatusOK, creation)
}

func (s *Server) adminWebAuthnRegisterFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	session, ok := s.takeWebAuthnSession(r, "register")
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "WebAuthn registration ceremony is missing or expired"})
		return
	}
	webauthn, err := s.webAuthnConfigured(r)
	if err != nil {
		writeJSON(w, http.StatusPreconditionFailed, map[string]any{"detail": err.Error()})
		return
	}
	user := s.webAuthnUser()
	credential, err := webauthn.FinishRegistration(user, session, r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "invalid WebAuthn registration"})
		return
	}
	s.mu.Lock()
	state := s.webauthnState
	state.Credentials = append(state.Credentials, *credential)
	state.EnrolledAt = s.now()
	s.webauthnState = state
	security := s.security
	security.MFAEnrolledAt = state.EnrolledAt
	security.UpdatedAt = state.EnrolledAt
	s.security = security
	s.mu.Unlock()
	if err := saveWebAuthnState(s.webauthnPath, state); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "failed to persist WebAuthn credential"})
		return
	}
	if err := s.persistSecurity(security); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "failed to persist WebAuthn MFA state"})
		return
	}
	s.addSecurityAudit("mfa_webauthn_enrolled", map[string]any{"method": "webauthn"})
	writeJSON(w, http.StatusOK, map[string]any{"mfa_enrolled": true, "method": "webauthn"})
}

func (s *Server) adminWebAuthnLoginBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	user := s.webAuthnUser()
	if len(user.credentials) == 0 {
		writeJSON(w, http.StatusPreconditionFailed, map[string]any{"detail": "no WebAuthn credential is enrolled"})
		return
	}
	webauthn, err := s.webAuthnConfigured(r)
	if err != nil {
		writeJSON(w, http.StatusPreconditionFailed, map[string]any{"detail": err.Error()})
		return
	}
	assertion, session, err := webauthn.BeginLogin(user, webauthnlib.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "failed to begin WebAuthn login"})
		return
	}
	if err := s.setWebAuthnSession(w, r, webAuthnSession{Kind: "login", Session: *session}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "failed to persist WebAuthn ceremony"})
		return
	}
	writeJSON(w, http.StatusOK, assertion)
}

func (s *Server) adminWebAuthnLoginFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	session, ok := s.takeWebAuthnSession(r, "login")
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "WebAuthn login ceremony is missing or expired"})
		return
	}
	webauthn, err := s.webAuthnConfigured(r)
	if err != nil {
		writeJSON(w, http.StatusPreconditionFailed, map[string]any{"detail": err.Error()})
		return
	}
	user := s.webAuthnUser()
	credential, err := webauthn.FinishLogin(user, session, r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "invalid WebAuthn assertion"})
		return
	}
	s.mu.Lock()
	state := s.webauthnState
	for i := range state.Credentials {
		if hmac.Equal(state.Credentials[i].ID, credential.ID) {
			state.Credentials[i] = *credential
		}
	}
	s.webauthnState = state
	s.mu.Unlock()
	if err := saveWebAuthnState(s.webauthnPath, state); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "failed to persist WebAuthn counter"})
		return
	}
	expires := time.Now().Add(adminReauthTTL)
	http.SetCookie(w, &http.Cookie{Name: webAuthnMFACookie, Value: s.signAdminProof("webauthn-mfa", expires), Path: "/", MaxAge: int(adminReauthTTL.Seconds()), HttpOnly: true, Secure: isSecureRequest(r) || strings.HasPrefix(s.origin(r), "https://"), SameSite: http.SameSiteStrictMode})
	s.addSecurityAudit("mfa_webauthn_verified", map[string]any{"method": "webauthn"})
	writeJSON(w, http.StatusOK, map[string]any{"mfa_verified": true, "method": "webauthn", "expires_at": expires})
}

func (s *Server) signAdminProof(prefix string, expires time.Time) string {
	payload := fmt.Sprintf("%s:%d", prefix, expires.Unix())
	mac := s.adminSessionMAC(payload)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac)
}

func (s *Server) webAuthnMFACookieValid(r *http.Request) bool {
	cookie, err := r.Cookie(webAuthnMFACookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	values := strings.Split(string(payload), ":")
	if len(values) != 2 || values[0] != "webauthn-mfa" {
		return false
	}
	seconds, err := strconv.ParseInt(values[1], 10, 64)
	if err != nil || time.Now().Unix() > seconds {
		return false
	}
	mac, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	return hmac.Equal(mac, s.adminSessionMAC(string(payload)))
}
