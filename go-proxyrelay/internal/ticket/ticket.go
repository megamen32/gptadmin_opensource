// Package ticket implements the relay-specific signed ticket contract.
// It deliberately has no Hub, OAuth, or ShellMCP credential dependency.
package ticket

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	// KindStream identifies a one-time data-stream ticket.
	KindStream = "stream"
	// KindRevoke identifies a one-time capability revocation notice.
	KindRevoke = "revoke"
	// RoleClient is the local connector side of a stream pair.
	RoleClient = "client"
	// RoleAgent is the edge-agent side of a stream pair.
	RoleAgent = "agent"

	tokenPrefix       = "gpr1"
	minimumKeyBytes   = 32
	maximumTokenBytes = 16 * 1024
	maximumFrameBytes = 64 * 1024
	maximumQueueDepth = 256
	maximumStreams    = 4096
	maximumSeconds    = 24 * 60 * 60
	maximumBytes      = int64(1 << 40)
	maximumBandwidth  = int64(1 << 30)
)

var (
	ErrMalformed       = errors.New("relay ticket is malformed")
	ErrSignature       = errors.New("relay ticket signature is invalid")
	ErrClaims          = errors.New("relay ticket claims are invalid")
	ErrExpired         = errors.New("relay ticket is expired")
	ErrRole            = errors.New("relay ticket role is denied")
	ErrReplay          = errors.New("relay ticket was already consumed")
	ErrReplayCacheFull = errors.New("relay replay cache is full")
)

// Limits contains every finite resource boundary needed by one stream.
type Limits struct {
	MaxFrameBytes             int64 `json:"max_frame_bytes"`
	MaxPendingFrames          int   `json:"max_pending_frames"`
	DialTimeoutSeconds        int   `json:"dial_timeout_seconds"`
	IdleTimeoutSeconds        int   `json:"idle_timeout_seconds"`
	MaxStreamLifetimeSeconds int   `json:"max_stream_lifetime_seconds"`
	MaxBytes                  int64 `json:"max_bytes"`
	BandwidthBytesPerSecond   int64 `json:"bandwidth_bytes_per_second,omitempty"`
	MaxStreamsPerAgent        int   `json:"max_streams_per_agent"`
	MaxStreamsPerProfile      int   `json:"max_streams_per_profile"`
}

// Claims is the immutable signed authorization for one stream side.
type Claims struct {
	Kind            string `json:"kind"`
	ProtocolVersion int    `json:"protocol_version"`
	CapabilityID    string `json:"capability_id"`
	StreamID        string `json:"stream_id"`
	ProfileID       string `json:"profile_id"`
	AgentID         string `json:"agent_id"`
	Target          string `json:"target"`
	Role            string `json:"role"`
	ExpiresAt       int64  `json:"exp"`
	JTI             string `json:"jti"`
	Limits          Limits `json:"limits"`
}

// Revocation identifies one signed, one-time capability revoke request.
type Revocation struct {
	Kind         string `json:"kind"`
	CapabilityID string `json:"capability_id"`
	ExpiresAt    int64  `json:"exp"`
	JTI          string `json:"jti"`
}

// Signer creates compact HMAC-SHA256 tickets for the isolated relay.
type Signer struct {
	key []byte
}

// NewSigner copies and validates a relay-specific signing key.
func NewSigner(key []byte) (*Signer, error) {
	if len(key) < minimumKeyBytes {
		return nil, fmt.Errorf("%w: signing key must be at least %d bytes", ErrClaims, minimumKeyBytes)
	}
	return &Signer{key: append([]byte(nil), key...)}, nil
}

// SignStream validates and signs one role-bound stream claim.
func (s *Signer) SignStream(claims Claims) (string, error) {
	if err := validateStreamClaims(claims); err != nil {
		return "", err
	}
	return s.sign(claims)
}

// SignRevocation signs a short-lived capability revocation notice.
func (s *Signer) SignRevocation(capabilityID, jti string, expiresAt time.Time) (string, error) {
	revoke := Revocation{
		Kind:         KindRevoke,
		CapabilityID: strings.TrimSpace(capabilityID),
		ExpiresAt:    expiresAt.UTC().Unix(),
		JTI:          strings.TrimSpace(jti),
	}
	if revoke.CapabilityID == "" || revoke.JTI == "" || revoke.ExpiresAt <= 0 {
		return "", ErrClaims
	}
	return s.sign(revoke)
}

func (s *Signer) sign(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("%w: encode claims", ErrClaims)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signed := tokenPrefix + "." + encoded
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte(signed))
	token := signed + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if len(token) > maximumTokenBytes {
		return "", ErrClaims
	}
	return token, nil
}

// Verifier validates signatures and atomically consumes ticket JTIs.
type Verifier struct {
	key      []byte
	now      func() time.Time
	capacity int
	mu       sync.Mutex
	used     map[string]int64
}

// NewVerifier creates a bounded fail-closed verifier.
func NewVerifier(key []byte, now func() time.Time, replayCapacity int) (*Verifier, error) {
	if len(key) < minimumKeyBytes || now == nil || replayCapacity <= 0 {
		return nil, ErrClaims
	}
	return &Verifier{
		key:      append([]byte(nil), key...),
		now:      now,
		capacity: replayCapacity,
		used:     make(map[string]int64, replayCapacity),
	}, nil
}

// VerifyAndConsumeStream validates one stream ticket for the requested side.
func (v *Verifier) VerifyAndConsumeStream(ctx context.Context, raw, expectedRole string) (Claims, error) {
	if err := ctx.Err(); err != nil {
		return Claims{}, err
	}
	var claims Claims
	if err := v.verify(raw, &claims); err != nil {
		return Claims{}, err
	}
	if err := validateStreamClaims(claims); err != nil {
		return Claims{}, err
	}
	if claims.Role != expectedRole || (expectedRole != RoleClient && expectedRole != RoleAgent) {
		return Claims{}, ErrRole
	}
	if !v.now().UTC().Before(time.Unix(claims.ExpiresAt, 0)) {
		return Claims{}, ErrExpired
	}
	if err := v.consume(claims.JTI, claims.ExpiresAt); err != nil {
		return Claims{}, err
	}
	return claims, nil
}

// VerifyAndConsumeRevocation validates one relay-specific revocation ticket.
func (v *Verifier) VerifyAndConsumeRevocation(ctx context.Context, raw string) (Revocation, error) {
	if err := ctx.Err(); err != nil {
		return Revocation{}, err
	}
	var revoke Revocation
	if err := v.verify(raw, &revoke); err != nil {
		return Revocation{}, err
	}
	if revoke.Kind != KindRevoke || strings.TrimSpace(revoke.CapabilityID) == "" || strings.TrimSpace(revoke.JTI) == "" {
		return Revocation{}, ErrClaims
	}
	if !v.now().UTC().Before(time.Unix(revoke.ExpiresAt, 0)) {
		return Revocation{}, ErrExpired
	}
	if err := v.consume(revoke.JTI, revoke.ExpiresAt); err != nil {
		return Revocation{}, err
	}
	return revoke, nil
}

// ReplayEntries reports bounded replay-cache occupancy for observability.
func (v *Verifier) ReplayEntries() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.used)
}

func (v *Verifier) verify(raw string, dst any) error {
	if len(raw) == 0 || len(raw) > maximumTokenBytes {
		return ErrMalformed
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] != tokenPrefix || parts[1] == "" || parts[2] == "" {
		return ErrMalformed
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != sha256.Size {
		return ErrMalformed
	}
	mac := hmac.New(sha256.New, v.key)
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return ErrSignature
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ErrMalformed
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return ErrMalformed
	}
	return nil
}

func (v *Verifier) consume(jti string, expiresAt int64) error {
	now := v.now().UTC().Unix()
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, exists := v.used[jti]; exists {
		return ErrReplay
	}
	for usedJTI, expiry := range v.used {
		if expiry <= now {
			delete(v.used, usedJTI)
		}
	}
	if len(v.used) >= v.capacity {
		return ErrReplayCacheFull
	}
	v.used[jti] = expiresAt
	return nil
}

func validateStreamClaims(claims Claims) error {
	if claims.Kind != KindStream || claims.ProtocolVersion != 1 {
		return ErrClaims
	}
	if strings.TrimSpace(claims.CapabilityID) == "" || strings.TrimSpace(claims.StreamID) == "" ||
		strings.TrimSpace(claims.ProfileID) == "" || strings.TrimSpace(claims.AgentID) == "" ||
		strings.TrimSpace(claims.Target) == "" || strings.TrimSpace(claims.JTI) == "" || claims.ExpiresAt <= 0 {
		return ErrClaims
	}
	if claims.Role != RoleClient && claims.Role != RoleAgent {
		return ErrClaims
	}
	limits := claims.Limits
	if limits.MaxFrameBytes <= 0 || limits.MaxFrameBytes > maximumFrameBytes ||
		limits.MaxPendingFrames <= 0 || limits.MaxPendingFrames > maximumQueueDepth ||
		limits.DialTimeoutSeconds <= 0 || limits.DialTimeoutSeconds > maximumSeconds ||
		limits.IdleTimeoutSeconds <= 0 || limits.IdleTimeoutSeconds > maximumSeconds ||
		limits.MaxStreamLifetimeSeconds <= 0 || limits.MaxStreamLifetimeSeconds > maximumSeconds ||
		limits.MaxBytes <= 0 || limits.MaxBytes > maximumBytes ||
		limits.BandwidthBytesPerSecond < 0 || limits.BandwidthBytesPerSecond > maximumBandwidth ||
		limits.MaxStreamsPerAgent <= 0 || limits.MaxStreamsPerAgent > maximumStreams ||
		limits.MaxStreamsPerProfile <= 0 || limits.MaxStreamsPerProfile > maximumStreams {
		return ErrClaims
	}
	return nil
}
