package hub

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	secretKeySize = 32
	minSecretTTL  = time.Minute
	maxSecretTTL  = time.Hour
)

var validSecretEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// SecretStoreError is a stable, non-sensitive error category for secret-store callers.
type SecretStoreError string

func (e SecretStoreError) Error() string { return string(e) }

const (
	ErrSecretNotFound           SecretStoreError = "secret not found"
	ErrSecretRequestConsumed    SecretStoreError = "secret request already consumed"
	ErrSecretRequestExpired     SecretStoreError = "secret request expired"
	ErrSecretRequestNotReady    SecretStoreError = "secret request is not ready"
	ErrSecretInvalidLabel       SecretStoreError = "secret label is invalid"
	ErrSecretInvalidEnvironment SecretStoreError = "secret environment name is invalid"
	ErrSecretInvalidTTL         SecretStoreError = "secret request TTL is invalid"
	ErrSecretInvalidOwner       SecretStoreError = "secret owner is invalid"
	ErrSecretInvalidToken       SecretStoreError = "secret token is invalid"
	ErrSecretStoreCorrupt       SecretStoreError = "secret store is corrupt"
)

// ErrSecretInvalidEnvName is kept as a descriptive alias for HTTP/MCP callers.
var ErrSecretInvalidEnvName = ErrSecretInvalidEnvironment

// SecretIngressRequest is the opaque metadata returned when a browser request is created.
type SecretIngressRequest struct {
	Ref       string    `json:"ref"`
	Label     string    `json:"label"`
	EnvName   string    `json:"env_name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// SecretReference identifies a secret without containing its value.
type SecretReference struct {
	Ref       string    `json:"ref"`
	Label     string    `json:"label"`
	EnvName   string    `json:"env_name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type persistedSecretRequest struct {
	Ref              string `json:"ref"`
	TokenHash        string `json:"token_hash"`
	OwnerFingerprint string `json:"owner_fingerprint"`
	Label            string `json:"label"`
	EnvName          string `json:"env_name"`
	Status           string `json:"status"`
	CreatedAt        int64  `json:"created_at"`
	ExpiresAt        int64  `json:"expires_at"`
}

type persistedSecretState struct {
	Requests map[string]persistedSecretRequest `json:"requests"`
}

type persistedSecretRecord struct {
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

// SecretStore owns encrypted one-time browser secret requests and their references.
type SecretStore struct {
	configDir string
	storeDir  string
	keyFile   string
	stateFile string
	now       func() time.Time

	mu       sync.Mutex
	key      []byte
	cipher   cipher.AEAD
	requests map[string]persistedSecretRequest
}

// NewSecretStore loads or creates the AES-256-GCM key and persistent request state.
func NewSecretStore(configDir, storeDir, keyFile string, now func() time.Time) (*SecretStore, error) {
	return newSecretStore(configDir, storeDir, keyFile, filepath.Join(storeDir, "requests.json"), now)
}

// NewSecretStoreWithStateFile is the configurable constructor used by Hub deployments.
func NewSecretStoreWithStateFile(configDir, storeDir, keyFile, stateFile string, now func() time.Time) (*SecretStore, error) {
	return newSecretStore(configDir, storeDir, keyFile, stateFile, now)
}

func newSecretStore(configDir, storeDir, keyFile, stateFile string, now func() time.Time) (*SecretStore, error) {
	if strings.TrimSpace(configDir) == "" || strings.TrimSpace(storeDir) == "" || strings.TrimSpace(keyFile) == "" {
		return nil, fmt.Errorf("create secret store: %w", ErrSecretStoreCorrupt)
	}
	if strings.TrimSpace(stateFile) == "" {
		return nil, fmt.Errorf("create secret store: %w", ErrSecretStoreCorrupt)
	}
	if now == nil {
		now = time.Now
	}
	if err := ensurePrivateDir(configDir); err != nil {
		return nil, fmt.Errorf("create secret config directory: %w", err)
	}
	if err := ensurePrivateDir(storeDir); err != nil {
		return nil, fmt.Errorf("create secret store directory: %w", err)
	}
	if err := ensurePrivateDir(filepath.Dir(keyFile)); err != nil {
		return nil, fmt.Errorf("create secret key directory: %w", err)
	}
	key, err := loadOrCreateSecretKey(keyFile)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		zeroBytes(key)
		return nil, fmt.Errorf("create secret cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		zeroBytes(key)
		return nil, fmt.Errorf("create secret AEAD: %w", err)
	}
	requests, err := loadSecretState(stateFile)
	if err != nil {
		zeroBytes(key)
		return nil, err
	}
	if err := validateSecretRecords(storeDir, requests); err != nil {
		zeroBytes(key)
		return nil, err
	}
	return &SecretStore{
		configDir: configDir,
		storeDir:  storeDir,
		keyFile:   keyFile,
		stateFile: stateFile,
		now:       now,
		key:       key,
		cipher:    aead,
		requests:  requests,
	}, nil
}

// CreateRequest creates a caller-owned, single-use browser request.
func (s *SecretStore) CreateRequest(ownerFingerprint, label, envName string, ttl time.Duration) (SecretIngressRequest, string, error) {
	if strings.TrimSpace(ownerFingerprint) == "" {
		return SecretIngressRequest{}, "", ErrSecretInvalidOwner
	}
	label = strings.TrimSpace(label)
	if label == "" {
		return SecretIngressRequest{}, "", ErrSecretInvalidLabel
	}
	if !validSecretEnvName.MatchString(envName) {
		return SecretIngressRequest{}, "", ErrSecretInvalidEnvironment
	}
	if ttl < minSecretTTL || ttl > maxSecretTTL {
		return SecretIngressRequest{}, "", ErrSecretInvalidTTL
	}

	ref, err := randomSecretString(18)
	if err != nil {
		return SecretIngressRequest{}, "", fmt.Errorf("create secret reference: %w", err)
	}
	rawToken, err := randomSecretString(32)
	if err != nil {
		return SecretIngressRequest{}, "", fmt.Errorf("create secret token: %w", err)
	}
	now := s.now()
	persisted := persistedSecretRequest{
		Ref:              ref,
		TokenHash:        hashSecretToken(rawToken),
		OwnerFingerprint: ownerFingerprint,
		Label:            label,
		EnvName:          envName,
		Status:           "pending",
		CreatedAt:        now.UnixNano(),
		ExpiresAt:        now.Add(ttl).UnixNano(),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests[ref] = persisted
	if err := s.saveStateLocked(); err != nil {
		delete(s.requests, ref)
		return SecretIngressRequest{}, "", err
	}
	return secretIngressRequestFromPersisted(persisted), rawToken, nil
}

// ConsumeRequest validates and atomically consumes rawToken, storing only encrypted content.
func (s *SecretStore) ConsumeRequest(rawToken, value string) (SecretReference, error) {
	if rawToken == "" {
		return SecretReference{}, ErrSecretInvalidToken
	}
	tokenHash := hashSecretToken(rawToken)
	s.mu.Lock()
	defer s.mu.Unlock()
	ref, request, ok := s.findRequestByTokenHashLocked(tokenHash)
	if !ok {
		return SecretReference{}, ErrSecretNotFound
	}
	if request.Status == "ready" || request.Status == "consumed" {
		return SecretReference{}, ErrSecretRequestConsumed
	}
	if request.Status != "pending" {
		return SecretReference{}, ErrSecretNotFound
	}
	if s.now().UnixNano() >= request.ExpiresAt {
		return SecretReference{}, ErrSecretRequestExpired
	}

	if err := s.writeEncryptedRecord(ref, []byte(value)); err != nil {
		return SecretReference{}, err
	}
	request.Status = "ready"
	s.requests[ref] = request
	if err := s.saveStateLocked(); err != nil {
		deleteErr := os.Remove(s.recordPath(ref))
		if deleteErr != nil && !errors.Is(deleteErr, os.ErrNotExist) {
			return SecretReference{}, fmt.Errorf("persist consumed secret request: %w (cleanup: %v)", err, deleteErr)
		}
		request.Status = "pending"
		s.requests[ref] = request
		return SecretReference{}, err
	}
	return secretReferenceFromPersisted(request), nil
}

// Status returns reference metadata and readiness without returning the secret value.
func (s *SecretStore) Status(ref, ownerFingerprint string) (SecretReference, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, ok := s.requests[ref]
	if !ok || request.OwnerFingerprint != ownerFingerprint {
		return SecretReference{}, ErrSecretNotFound
	}
	if request.Status == "pending" && s.now().UnixNano() >= request.ExpiresAt {
		return SecretReference{}, ErrSecretRequestExpired
	}
	if request.Status == "ready" {
		if _, err := os.Stat(s.recordPath(ref)); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return SecretReference{}, ErrSecretNotFound
			}
			return SecretReference{}, fmt.Errorf("check encrypted secret record: %w", err)
		}
	}
	return secretReferenceFromPersisted(request), nil
}

// Resolve decrypts a ready secret for immediate use by an authorized internal job.
func (s *SecretStore) Resolve(ref, ownerFingerprint string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, ok := s.requests[ref]
	if !ok || request.OwnerFingerprint != ownerFingerprint {
		return "", ErrSecretNotFound
	}
	if request.Status == "pending" {
		if s.now().UnixNano() >= request.ExpiresAt {
			return "", ErrSecretRequestExpired
		}
		return "", ErrSecretRequestNotReady
	}
	if request.Status != "ready" {
		return "", ErrSecretNotFound
	}
	return s.decryptRecord(ref)
}

func (s *SecretStore) findRequestByTokenHashLocked(tokenHash string) (string, persistedSecretRequest, bool) {
	for ref, request := range s.requests {
		if subtle.ConstantTimeCompare([]byte(request.TokenHash), []byte(tokenHash)) == 1 {
			return ref, request, true
		}
	}
	return "", persistedSecretRequest{}, false
}

func (s *SecretStore) saveStateLocked() error {
	data, err := json.MarshalIndent(persistedSecretState{Requests: s.requests}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal secret state: %w", err)
	}
	return writeSecretFileAtomic(s.stateFile, append(data, '\n'))
}

func (s *SecretStore) writeEncryptedRecord(ref string, value []byte) error {
	nonce := make([]byte, s.cipher.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		zeroBytes(nonce)
		return fmt.Errorf("create secret nonce: %w", err)
	}
	ciphertext := s.cipher.Seal(nil, nonce, value, nil)
	zeroBytes(value)
	record := persistedSecretRecord{
		Nonce:      base64.RawURLEncoding.EncodeToString(nonce),
		Ciphertext: base64.RawURLEncoding.EncodeToString(ciphertext),
	}
	zeroBytes(nonce)
	zeroBytes(ciphertext)
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal encrypted secret record: %w", err)
	}
	return writeSecretFileAtomic(s.recordPath(ref), append(data, '\n'))
}

func (s *SecretStore) decryptRecord(ref string) (string, error) {
	data, err := os.ReadFile(s.recordPath(ref))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrSecretNotFound
		}
		return "", fmt.Errorf("read encrypted secret record: %w", err)
	}
	var record persistedSecretRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return "", fmt.Errorf("parse encrypted secret record: %w", ErrSecretStoreCorrupt)
	}
	nonce, err := base64.RawURLEncoding.DecodeString(record.Nonce)
	if err != nil {
		return "", fmt.Errorf("decode encrypted secret nonce: %w", ErrSecretStoreCorrupt)
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(record.Ciphertext)
	if err != nil {
		zeroBytes(nonce)
		return "", fmt.Errorf("decode encrypted secret content: %w", ErrSecretStoreCorrupt)
	}
	plaintext, err := s.cipher.Open(nil, nonce, ciphertext, nil)
	zeroBytes(nonce)
	zeroBytes(ciphertext)
	if err != nil {
		zeroBytes(plaintext)
		return "", fmt.Errorf("decrypt encrypted secret record: %w", ErrSecretStoreCorrupt)
	}
	value := string(plaintext)
	zeroBytes(plaintext)
	return value, nil
}

func (s *SecretStore) recordPath(ref string) string {
	return filepath.Join(s.storeDir, ref+".json")
}

func secretIngressRequestFromPersisted(request persistedSecretRequest) SecretIngressRequest {
	return SecretIngressRequest{
		Ref:       request.Ref,
		Label:     request.Label,
		EnvName:   request.EnvName,
		Status:    request.Status,
		CreatedAt: time.Unix(0, request.CreatedAt).UTC(),
		ExpiresAt: time.Unix(0, request.ExpiresAt).UTC(),
	}
}

func secretReferenceFromPersisted(request persistedSecretRequest) SecretReference {
	return SecretReference{
		Ref:       request.Ref,
		Label:     request.Label,
		EnvName:   request.EnvName,
		Status:    request.Status,
		CreatedAt: time.Unix(0, request.CreatedAt).UTC(),
		ExpiresAt: time.Unix(0, request.ExpiresAt).UTC(),
	}
}

func loadSecretState(path string) (map[string]persistedSecretRequest, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]persistedSecretRequest), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read secret state: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("validate secret state: %w", ErrSecretStoreCorrupt)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("restrict secret state permissions: %w", err)
	}
	var state persistedSecretState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse secret state: %w", ErrSecretStoreCorrupt)
	}
	if state.Requests == nil {
		state.Requests = make(map[string]persistedSecretRequest)
	}
	for ref, request := range state.Requests {
		if ref == "" || request.Ref != ref || request.TokenHash == "" || request.OwnerFingerprint == "" || request.Label == "" || !validSecretEnvName.MatchString(request.EnvName) {
			return nil, fmt.Errorf("validate secret state: %w", ErrSecretStoreCorrupt)
		}
	}
	return state.Requests, nil
}

func validateSecretRecords(storeDir string, requests map[string]persistedSecretRequest) error {
	for ref, request := range requests {
		if request.Status != "pending" && request.Status != "ready" {
			return fmt.Errorf("validate secret state status %q: %w", request.Status, ErrSecretStoreCorrupt)
		}
		if request.Status != "ready" {
			continue
		}
		recordPath := filepath.Join(storeDir, ref+".json")
		info, err := os.Lstat(recordPath)
		if err != nil {
			return fmt.Errorf("validate encrypted secret record %q: %w", ref, ErrSecretStoreCorrupt)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("validate encrypted secret record %q: %w", ref, ErrSecretStoreCorrupt)
		}
		if err := os.Chmod(recordPath, 0o600); err != nil {
			return fmt.Errorf("restrict encrypted secret record %q: %w", ref, err)
		}
	}
	return nil
}

func loadOrCreateSecretKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err == nil {
		if len(key) != secretKeySize {
			zeroBytes(key)
			return nil, fmt.Errorf("load secret key: %w", ErrSecretStoreCorrupt)
		}
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.Mode().IsRegular() {
			zeroBytes(key)
			return nil, fmt.Errorf("load secret key: %w", ErrSecretStoreCorrupt)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			zeroBytes(key)
			return nil, fmt.Errorf("restrict secret key permissions: %w", err)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read secret key: %w", err)
	}
	key = make([]byte, secretKeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		zeroBytes(key)
		return nil, fmt.Errorf("create secret key: %w", err)
	}
	if err := writeSecretFileAtomic(path, key); err != nil {
		zeroBytes(key)
		return nil, fmt.Errorf("persist secret key: %w", err)
	}
	return key, nil
}

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func writeSecretFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := ensurePrivateDir(dir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".secret-store-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
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
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return nil
}

func randomSecretString(size int) (string, error) {
	b := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		zeroBytes(b)
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(b)
	zeroBytes(b)
	return encoded, nil
}

func hashSecretToken(rawToken string) string {
	digest := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(digest[:])
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
