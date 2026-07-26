package hub

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
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
)

const (
	securityPresetWorkingDefault = "working_default"
	securityPresetPrivateAccess  = "private_access"
	securityPresetLockedDown     = "locked_down"
	securityStateFilename        = "security_state.json"
	securityStateMaxBytes        = 16 << 10
	adminReauthCookieName        = "gptadmin_admin_reauth"
	adminReauthTTL               = 10 * time.Minute
)

type securitySettings struct {
	Preset             string    `json:"preset"`
	TOTPSecret         string    `json:"totp_secret,omitempty"`
	MFAEnrolledAt      time.Time `json:"mfa_enrolled_at,omitempty"`
	RecoveryCodeHashes []string  `json:"recovery_code_hashes,omitempty"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type persistedSecuritySettings struct {
	Preset             string    `json:"preset"`
	EncryptedTOTP      string    `json:"totp_secret_ciphertext,omitempty"`
	LegacyTOTP         string    `json:"totp_secret,omitempty"`
	MFAEnrolledAt      time.Time `json:"mfa_enrolled_at,omitempty"`
	RecoveryCodeHashes []string  `json:"recovery_code_hashes,omitempty"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func defaultSecuritySettings() securitySettings {
	return securitySettings{Preset: securityPresetWorkingDefault}
}

func validateSecurityPreset(preset string) error {
	switch preset {
	case securityPresetWorkingDefault, securityPresetPrivateAccess, securityPresetLockedDown:
		return nil
	default:
		return errors.New("preset must be working_default, private_access or locked_down")
	}
}

func validatePublicOriginForPreset(origin string) error {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return nil
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return errors.New("PUBLIC_ORIGIN must be an absolute URL without userinfo")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	host := strings.ToLower(parsed.Hostname())
	if parsed.Scheme == "http" && (host == "localhost" || host == "127.0.0.1" || host == "::1") {
		return nil
	}
	return errors.New("external PUBLIC_ORIGIN must use HTTPS")
}

func loadSecuritySettings(path, key string) (securitySettings, error) {
	state := defaultSecuritySettings()
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
	if len(data) > securityStateMaxBytes {
		return state, errors.New("security settings file is too large")
	}
	var persisted persistedSecuritySettings
	if err := json.Unmarshal(data, &persisted); err != nil {
		return defaultSecuritySettings(), fmt.Errorf("decode security settings: %w", err)
	}
	state = securitySettings{Preset: persisted.Preset, MFAEnrolledAt: persisted.MFAEnrolledAt, RecoveryCodeHashes: persisted.RecoveryCodeHashes, UpdatedAt: persisted.UpdatedAt}
	if persisted.EncryptedTOTP != "" {
		secret, err := decryptSecuritySecret(persisted.EncryptedTOTP, key)
		if err != nil {
			return defaultSecuritySettings(), fmt.Errorf("decrypt TOTP secret: %w", err)
		}
		state.TOTPSecret = secret
	} else {
		state.TOTPSecret = persisted.LegacyTOTP
	}
	if state.Preset == "" {
		state.Preset = securityPresetWorkingDefault
	}
	if err := validateSecurityPreset(state.Preset); err != nil {
		return defaultSecuritySettings(), err
	}
	if state.TOTPSecret != "" && !validTOTPSecret(state.TOTPSecret) {
		return defaultSecuritySettings(), errors.New("security settings contains invalid TOTP secret")
	}
	if len(state.RecoveryCodeHashes) > 16 {
		return defaultSecuritySettings(), errors.New("security settings contains too many recovery codes")
	}
	return state, nil
}

func saveSecuritySettings(path string, state securitySettings, key string) error {
	if path == "" {
		return nil
	}
	if err := validateSecurityPreset(state.Preset); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	encrypted, err := encryptSecuritySecret(state.TOTPSecret, key)
	if err != nil {
		return err
	}
	persisted := persistedSecuritySettings{
		Preset: state.Preset, EncryptedTOTP: encrypted, MFAEnrolledAt: state.MFAEnrolledAt,
		RecoveryCodeHashes: state.RecoveryCodeHashes, UpdatedAt: state.UpdatedAt,
	}
	data, err := json.Marshal(persisted)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".security-state-*")
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
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func securityCipherKey(key string) []byte {
	digest := sha256.Sum256([]byte("gptadmin-security-state:" + key))
	return digest[:]
}

func encryptSecuritySecret(secret, key string) (string, error) {
	if secret == "" {
		return "", nil
	}
	block, err := aes.NewCipher(securityCipherKey(key))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(secret), nil)
	return base64.RawStdEncoding.EncodeToString(ciphertext), nil
}

func decryptSecuritySecret(encoded, key string) (string, error) {
	data, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(securityCipherKey(key))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() {
		return "", errors.New("encrypted TOTP secret is truncated")
	}
	nonce, ciphertext := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", errors.New("encrypted TOTP secret authentication failed")
	}
	return string(plaintext), nil
}

func generateTOTPSecret() (string, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw), nil
}

func generateRecoveryCodes(count int) ([]string, []string, error) {
	codes := make([]string, 0, count)
	hashes := make([]string, 0, count)
	for i := 0; i < count; i++ {
		raw := make([]byte, 8)
		if _, err := rand.Read(raw); err != nil {
			return nil, nil, err
		}
		code := strings.ToUpper(hex.EncodeToString(raw))
		codes = append(codes, code)
		hashes = append(hashes, recoveryCodeHash(code))
	}
	return codes, hashes, nil
}

func recoveryCodeHash(code string) string {
	digest := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(code))))
	return hex.EncodeToString(digest[:])
}

func validTOTPSecret(secret string) bool {
	secret = strings.ToUpper(strings.TrimSpace(secret))
	if secret == "" {
		return false
	}
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	return err == nil && len(decoded) >= 16
}

func totpCode(secret string, now time.Time) string {
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil || len(decoded) == 0 {
		return ""
	}
	counter := uint64(now.Unix() / 30)
	var message [8]byte
	binary.BigEndian.PutUint64(message[:], counter)
	mac := hmac.New(sha1.New, decoded) // RFC 6238's interoperable default.
	_, _ = mac.Write(message[:])
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 0x0f
	value := (uint32(digest[offset])&0x7f)<<24 | uint32(digest[offset+1])<<16 | uint32(digest[offset+2])<<8 | uint32(digest[offset+3])
	return fmt.Sprintf("%06d", value%1000000)
}

func validTOTPCode(secret, code string, now time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return false
	}
	for _, offset := range []int64{-30, 0, 30} {
		want := totpCode(secret, now.Add(time.Duration(offset)*time.Second))
		if want != "" && hmac.Equal([]byte(want), []byte(code)) {
			return true
		}
	}
	return false
}

func (s *Server) securitySnapshot() securitySettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.security
}

func (s *Server) securityPublicSnapshot() map[string]any {
	state := s.securitySnapshot()
	webauthnEnrolled := s.webAuthnEnrolled()
	mfaEnrolled := !state.MFAEnrolledAt.IsZero()
	mfaMethod := "none"
	if webauthnEnrolled {
		mfaEnrolled = true
		mfaMethod = "webauthn"
	} else if mfaEnrolled {
		mfaMethod = "totp"
	}
	return map[string]any{
		"preset":                   state.Preset,
		"mfa_enrolled":             mfaEnrolled,
		"updated_at":               state.UpdatedAt,
		"mfa_method":               mfaMethod,
		"recovery_codes_remaining": len(state.RecoveryCodeHashes),
		"restart_bound":            true,
	}
}

func (s *Server) persistSecurity(state securitySettings) error {
	return saveSecuritySettings(s.securityPath, state, s.securityKey())
}

func (s *Server) signAdminReauth(expires time.Time) string {
	payload := strconv.FormatInt(expires.Unix(), 10)
	mac := s.adminSessionMAC("reauth:" + payload)
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac)
}

func (s *Server) adminReauthValid(r *http.Request) bool {
	if s.cfg.AdminPassword == "" {
		return true
	}
	if !s.adminSessionValid(r) {
		return false
	}
	cookie, err := r.Cookie(adminReauthCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 {
		return false
	}
	expires, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || expires < time.Now().Unix() {
		return false
	}
	mac, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	return hmac.Equal(mac, s.adminSessionMAC("reauth:"+parts[0]))
}

func (s *Server) requireSensitiveSecurityReauth(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.AdminPassword == "" || s.adminReauthValid(r) {
		return true
	}
	writeJSON(w, http.StatusPreconditionRequired, map[string]any{"detail": "fresh admin reauthentication required"})
	return false
}

func (s *Server) adminSecurityReauth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	if s.cfg.AdminPassword != "" && !s.adminSessionValid(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "admin session required"})
		return
	}
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if s.cfg.AdminPassword != "" && !hmac.Equal([]byte(req.Password), []byte(s.cfg.AdminPassword)) {
		s.addSecurityAudit("security_reauth_denied", map[string]any{"reason": "bad_password"})
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "invalid reauthentication"})
		return
	}
	state := s.securitySnapshot()
	if (!state.MFAEnrolledAt.IsZero() || s.webAuthnEnrolled()) && !s.verifyAdminMFARequest(r, req.Code) {
		s.addSecurityAudit("security_reauth_denied", map[string]any{"reason": "invalid_mfa"})
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "invalid reauthentication"})
		return
	}
	expires := time.Now().Add(adminReauthTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     adminReauthCookieName,
		Value:    s.signAdminReauth(expires),
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(adminReauthTTL.Seconds()),
		HttpOnly: true,
		Secure:   isSecureRequest(r) || strings.HasPrefix(s.origin(r), "https://"),
		SameSite: http.SameSiteLaxMode,
	})
	s.addSecurityAudit("security_reauth_ok", map[string]any{"mfa": !state.MFAEnrolledAt.IsZero()})
	writeJSON(w, http.StatusOK, map[string]any{"reauthenticated": true, "expires_at": expires})
}

func (s *Server) securityKey() string {
	return firstNonEmpty(s.cfg.AdminPassword, s.cfg.OAuthClientSecret, s.cfg.CtlToken, "gptadmin-security-state")
}

func (s *Server) adminSecurityPreset(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.securityPublicSnapshot())
	case http.MethodPut:
		if !s.requireSensitiveSecurityReauth(w, r) {
			return
		}
		var req struct {
			Preset string `json:"preset"`
		}
		if err := readJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		preset := strings.ToLower(strings.TrimSpace(req.Preset))
		if err := validateSecurityPreset(preset); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		if err := validatePublicOriginForPreset(s.cfg.PublicOrigin); err != nil {
			writeJSON(w, http.StatusPreconditionFailed, map[string]any{"detail": err.Error()})
			return
		}
		s.mu.Lock()
		state := s.security
		if preset == securityPresetLockedDown && !s.mfaEnrolledLockedState(state) {
			s.mu.Unlock()
			writeJSON(w, http.StatusPreconditionFailed, map[string]any{"detail": "Locked down requires enrolled MFA"})
			return
		}
		state.Preset = preset
		state.UpdatedAt = s.now()
		s.security = state
		s.mu.Unlock()
		if err := s.persistSecurity(state); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "failed to persist security preset"})
			return
		}
		s.addSecurityAudit("security_preset_changed", map[string]any{"preset": preset})
		writeJSON(w, http.StatusOK, s.securityPublicSnapshot())
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
	}
}

func (s *Server) adminTOTPEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	s.mu.Lock()
	if s.security.TOTPSecret != "" {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{"detail": "TOTP is already enrolled; reset requires an explicit recovery flow"})
		return
	}
	secret, err := generateTOTPSecret()
	if err != nil {
		s.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "failed to generate TOTP enrollment"})
		return
	}
	recoveryCodes, recoveryHashes, err := generateRecoveryCodes(8)
	if err != nil {
		s.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "failed to generate recovery codes"})
		return
	}
	state := s.security
	state.TOTPSecret = secret
	state.RecoveryCodeHashes = recoveryHashes
	state.UpdatedAt = s.now()
	s.security = state
	s.mu.Unlock()
	if err := s.persistSecurity(state); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "failed to persist TOTP enrollment"})
		return
	}
	s.addSecurityAudit("mfa_totp_enrollment_started", map[string]any{})
	issuer := url.QueryEscape("GPTAdmin")
	account := url.QueryEscape("admin")
	uri := "otpauth://totp/" + issuer + ":" + account + "?secret=" + secret + "&issuer=" + issuer
	writeJSON(w, http.StatusOK, map[string]any{"mfa_enrolled": false, "method": "totp", "secret": secret, "otpauth_uri": uri, "recovery_codes": recoveryCodes, "message": "Store the setup secret and recovery codes securely, then verify one code."})
}

func (s *Server) adminTOTPVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	s.mu.Lock()
	state := s.security
	now := s.now()
	valid := validTOTPCode(state.TOTPSecret, req.Code, now)
	if !valid {
		hash := recoveryCodeHash(req.Code)
		for i, candidate := range state.RecoveryCodeHashes {
			if hmac.Equal([]byte(candidate), []byte(hash)) {
				state.RecoveryCodeHashes = append(state.RecoveryCodeHashes[:i], state.RecoveryCodeHashes[i+1:]...)
				valid = true
				break
			}
		}
	}
	if valid {
		state.MFAEnrolledAt = now
		state.UpdatedAt = now
		s.security = state
	}
	s.mu.Unlock()
	if !valid {
		s.addSecurityAudit("mfa_totp_verification_denied", map[string]any{"reason": "invalid_code"})
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "invalid MFA code"})
		return
	}
	if err := s.persistSecurity(state); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "failed to persist MFA enrollment"})
		return
	}
	s.addSecurityAudit("mfa_totp_enrolled", map[string]any{"method": "totp"})
	writeJSON(w, http.StatusOK, map[string]any{"mfa_enrolled": true, "method": "totp"})
}

func (s *Server) addSecurityAudit(name string, fields map[string]any) {
	s.mu.Lock()
	s.addAuditLocked(name, fields)
	s.mu.Unlock()
}

func (s *Server) securityRequiresMFA() bool {
	state := s.securitySnapshot()
	return state.Preset == securityPresetLockedDown
}

func (s *Server) verifyAdminMFA(code string) bool {
	s.mu.Lock()
	previous := s.security
	state := previous
	if !state.MFAEnrolledAt.IsZero() && validTOTPCode(state.TOTPSecret, code, s.now()) {
		s.mu.Unlock()
		return true
	}
	hash := recoveryCodeHash(code)
	for i, candidate := range state.RecoveryCodeHashes {
		if hmac.Equal([]byte(candidate), []byte(hash)) {
			state.RecoveryCodeHashes = append(state.RecoveryCodeHashes[:i], state.RecoveryCodeHashes[i+1:]...)
			state.UpdatedAt = s.now()
			s.security = state
			s.mu.Unlock()
			if err := s.persistSecurity(state); err != nil {
				s.mu.Lock()
				s.security = previous
				s.mu.Unlock()
				return false
			}
			return true
		}
	}
	s.mu.Unlock()
	return false
}

func (s *Server) verifyAdminMFARequest(r *http.Request, code string) bool {
	if s.webAuthnMFACookieValid(r) {
		return true
	}
	return s.verifyAdminMFA(code)
}

func (s *Server) webAuthnEnrolled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.webAuthnEnrolledLocked()
}

func (s *Server) mfaEnrolledLockedState(state securitySettings) bool {
	return (!state.MFAEnrolledAt.IsZero() && state.TOTPSecret != "") || s.webAuthnEnrolledLocked()
}

func (s *Server) webAuthnEnrolledLocked() bool {
	return len(s.webauthnState.Credentials) > 0 && !s.webauthnState.EnrolledAt.IsZero()
}
