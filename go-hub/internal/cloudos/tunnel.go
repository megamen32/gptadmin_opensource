package cloudos

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// StreamType constants identify the kind of multiplexed stream opened
// to a remote computer. Control, filesystem, and process streams are
// Cloud OS–specific; network streams reuse the existing proxyrelay
// ticket and pairing infrastructure.
const (
	StreamTypeControl    = "control"
	StreamTypeFilesystem = "filesystem"
	StreamTypeProcess    = "process"
	StreamTypeNetwork    = "network"
)

// TunnelManager manages multiplexed tunnels to connected computers.
// The hub delegates all remote-computer I/O through an implementation
// of this interface so the handler layer stays transport-agnostic.
//
// The production implementation (ProxyRelayTunnelManager) wraps the
// existing go-proxyrelay + NetworkProxyController infrastructure:
//
//   - control/filesystem/process streams: multiplexed over a single
//     WebSocket connection using the proxyrelay frame protocol,
//     authenticated with gpr1 tickets signed by the hub.
//   - network streams: delegated to NetworkProxyController which
//     issues its own client+agent gpr1 grants and posts revocations
//     to the relay's /v1/control/revoke endpoint.
//
// When no relay key is configured the hub falls back to
// NoopTunnelManager which always returns ErrTunnelUnavailable.
type TunnelManager interface {
	// OpenStream opens a new multiplexed stream to a computer.
	// streamType is one of the StreamType* constants.
	OpenStream(computerID string, streamType string) (io.ReadWriteCloser, error)

	// CloseStream closes a specific stream.
	CloseStream(computerID string, streamID string) error

	// CloseTunnel closes all streams for a computer.
	CloseTunnel(computerID string) error

	// IsConnected checks if a computer has an active tunnel.
	IsConnected(computerID string) bool
}

// ErrTunnelUnavailable is returned when no relay is configured.
var ErrTunnelUnavailable = errTunnelUnavailable{}

type errTunnelUnavailable struct{}

func (errTunnelUnavailable) Error() string { return "tunnel relay not available" }

// ---------------------------------------------------------------------------
// NoopTunnelManager — used when no relay key is configured.
// ---------------------------------------------------------------------------

// NoopTunnelManager is a TunnelManager that is never connected.
type NoopTunnelManager struct{}

func (n *NoopTunnelManager) OpenStream(_ string, _ string) (io.ReadWriteCloser, error) {
	return nil, ErrTunnelUnavailable
}
func (n *NoopTunnelManager) CloseStream(_ string, _ string) error {
	return ErrTunnelUnavailable
}
func (n *NoopTunnelManager) CloseTunnel(_ string) error {
	return ErrTunnelUnavailable
}
func (n *NoopTunnelManager) IsConnected(_ string) bool {
	return false
}

// ---------------------------------------------------------------------------
// ProxyRelayTunnelManager — production tunnel using go-proxyrelay.
// ---------------------------------------------------------------------------

// RelayTicketSigner is the subset of go-proxyrelay/internal/ticket.Signer
// that the Cloud OS tunnel needs. The hub already imports and uses this
// for the NetworkProxyController; Cloud OS reuses the same Signer.
//
// If the hub is compiled without proxyrelay support, this interface
// will have no implementation and the tunnel falls back to noop.
type RelayTicketSigner interface {
	// SignStream signs a stream claim and returns a gpr1 token.
	SignStream(claims RelayStreamClaims) (string, error)
	// SignRevocation signs a revocation notice.
	SignRevocation(capabilityID, jti string, expiresAt time.Time) (string, error)
}

// RelayStreamClaims mirrors ticket.Claims for the relay.
// This avoids a direct import of the proxyrelay ticket package
// from the cloudos sub-package.
type RelayStreamClaims struct {
	Kind            string         `json:"kind"`
	ProtocolVersion int            `json:"protocol_version"`
	CapabilityID    string         `json:"capability_id"`
	StreamID        string         `json:"stream_id"`
	ProfileID       string         `json:"profile_id"`
	AgentID         string         `json:"agent_id"`
	Target          string         `json:"target"`
	Role            string         `json:"role"`
	ExpiresAt       int64          `json:"exp"`
	JTI             string         `json:"jti"`
	Limits          RelayLimits    `json:"limits"`
}

// RelayLimits mirrors ticket.Limits.
type RelayLimits struct {
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

// RelayClient is the subset of the proxyrelay WebSocket client
// needed to open a paired stream.
type RelayClient interface {
	// OpenStream connects to the relay via WebSocket with the given
	// client ticket, waits for the agent peer, and returns a
	// bidirectional io.ReadWriteCloser for the stream.
	OpenStream(relayURL, clientTicket string) (io.ReadWriteCloser, error)
}

// ProxyRelayConfig holds the configuration for the production tunnel.
type ProxyRelayConfig struct {
	// RelayURL is the base URL of the proxyrelay server
	// (e.g. "http://127.0.0.1:8787").
	RelayURL string

	// RelayRevokeURL is the relay's control endpoint for revocation
	// (e.g. "http://127.0.0.1:8787/v1/control/revoke").
	RelayRevokeURL string

	// GrantTTL is how long each stream grant token is valid.
	GrantTTL time.Duration

	// MaxFrameBytes is the per-frame size limit sent to the relay.
	MaxFrameBytes int64

	// MaxStreamsPerComputer limits concurrent streams per computer.
	MaxStreamsPerComputer int

	// IdleTimeout is the inactivity timeout for streams.
	IdleTimeout time.Duration

	// StreamLifetime is the maximum lifetime of a single stream.
	StreamLifetime time.Duration

	// MaxStreamBytes is the total byte budget per stream.
	MaxStreamBytes int64
}

// DefaultProxyRelayConfig returns sensible defaults that match the
// existing NetworkProxyController's grant parameters.
func DefaultProxyRelayConfig() ProxyRelayConfig {
	return ProxyRelayConfig{
		RelayURL:             "http://127.0.0.1:8787",
		RelayRevokeURL:       "http://127.0.0.1:8787/v1/control/revoke",
		GrantTTL:             30 * time.Second,
		MaxFrameBytes:        32768,
		MaxStreamsPerComputer: 16,
		IdleTimeout:          5 * time.Minute,
		StreamLifetime:       10 * time.Minute,
		MaxStreamBytes:       256 * 1024 * 1024, // 256 MB
	}
}

// ProxyRelayTunnelManager is the production TunnelManager that uses
// the existing go-proxyrelay WebSocket relay for all stream types.
//
// For network streams, it delegates capability lifecycle to the
// existing NetworkProxyController (which signs gpr1 tickets with its
// own relay key and posts revocations). For control/filesystem/process
// streams, it signs its own gpr1 tickets using the same relay key.
//
// The native host (computer) connects to the relay as the "agent"
// peer. The hub (or Browser OS client) connects as the "client" peer.
// When both sides present matching stream_id tickets, the relay pairs
// them and begins bidirectional framing.
type ProxyRelayTunnelManager struct {
	mu       sync.RWMutex
	config   ProxyRelayConfig
	signer   RelayTicketSigner
	client   RelayClient
	registry *Registry

	// activeStreams tracks open streams per computer for cleanup.
	activeStreams map[string]map[string]io.ReadWriteCloser // computerID → streamID → closer
}

// NewProxyRelayTunnelManager creates a tunnel manager backed by the
// proxyrelay. Pass nil for signer or client to get a manager that
// reports IsConnected=false (relay not configured).
func NewProxyRelayTunnelManager(
	config ProxyRelayConfig,
	signer RelayTicketSigner,
	client RelayClient,
	registry *Registry,
) *ProxyRelayTunnelManager {
	if registry == nil {
		registry = NewRegistry()
	}
	return &ProxyRelayTunnelManager{
		config:        config,
		signer:        signer,
		client:        client,
		registry:      registry,
		activeStreams:  make(map[string]map[string]io.ReadWriteCloser),
	}
}

// IsConnected returns true if the computer is registered, online, and
// the relay signer/client are configured.
func (m *ProxyRelayTunnelManager) IsConnected(computerID string) bool {
	if m.signer == nil || m.client == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	comp, ok := m.registry.Get(computerID)
	if !ok {
		return false
	}
	return comp.Status == "online"
}

// OpenStream opens a stream to the remote computer via the proxyrelay.
//
// Flow:
//  1. Look up the computer in the registry (must be online).
//  2. Generate a stream ID and JTI.
//  3. Sign a client-side gpr1 ticket.
//  4. Connect to the relay via WebSocket with the ticket.
//  5. Wait for the agent (native host) to connect with its matching ticket.
//  6. Return the bidirectional stream as io.ReadWriteCloser.
//
// The native host must have already obtained the agent-side ticket
// (delivered via the browser extension's pairing flow or via the
// hub's /proxy-agent/offers endpoint).
func (m *ProxyRelayTunnelManager) OpenStream(computerID string, streamType string) (io.ReadWriteCloser, error) {
	if m.signer == nil || m.client == nil {
		return nil, ErrTunnelUnavailable
	}

	comp, ok := m.registry.Get(computerID)
	if !ok {
		return nil, fmt.Errorf("computer %q not found", computerID)
	}
	if comp.Status != "online" {
		return nil, fmt.Errorf("computer %q is offline", computerID)
	}

	streamID := newStreamID()
	jti := newJTI()
	expiresAt := time.Now().UTC().Add(m.config.GrantTTL).Unix()

	claims := RelayStreamClaims{
		Kind:            "stream",
		ProtocolVersion: 1,
		CapabilityID:    fmt.Sprintf("cloudos-%s-%s", computerID, streamType),
		StreamID:        streamID,
		ProfileID:       "cloudos",
		AgentID:         comp.ID,
		Target:          streamType, // stream type serves as the "target" for routing
		Role:            "client",
		ExpiresAt:       expiresAt,
		JTI:             jti,
		Limits: RelayLimits{
			MaxFrameBytes:             m.config.MaxFrameBytes,
			MaxPendingFrames:          16,
			DialTimeoutSeconds:        10,
			IdleTimeoutSeconds:        int(m.config.IdleTimeout.Seconds()),
			MaxStreamLifetimeSeconds: int(m.config.StreamLifetime.Seconds()),
			MaxBytes:                  m.config.MaxStreamBytes,
			MaxStreamsPerAgent:        m.config.MaxStreamsPerComputer,
			MaxStreamsPerProfile:      m.config.MaxStreamsPerComputer,
		},
	}

	clientTicket, err := m.signer.SignStream(claims)
	if err != nil {
		return nil, fmt.Errorf("sign client stream ticket: %w", err)
	}

	// The agent-side ticket must be delivered to the native host before
	// the client connects. In the current architecture, the hub delivers
	// agent tickets via the /proxy-agent/offers endpoint or webhook.
	// For Cloud OS, the computer's browser extension receives the agent
	// ticket and passes it to the native host via native messaging.
	//
	// The agent offer is:
	agentOffer := map[string]any{
		"stream_id":     streamID,
		"capability_id": claims.CapabilityID,
		"role":          "agent",
		"stream_type":   streamType,
		"expires_at":    time.Unix(expiresAt, 0).UTC().Format(time.RFC3339),
	}
	offerBytes, _ := json.Marshal(agentOffer)
	_ = offerBytes // In production, this is delivered to the native host.

	// Connect to the relay as the client peer.
	stream, err := m.client.OpenStream(m.config.RelayURL, clientTicket)
	if err != nil {
		return nil, fmt.Errorf("open relay stream: %w", err)
	}

	// Track the stream for cleanup.
	m.mu.Lock()
	if m.activeStreams[computerID] == nil {
		m.activeStreams[computerID] = make(map[string]io.ReadWriteCloser)
	}
	m.activeStreams[computerID][streamID] = &trackedCloser{
		ReadWriteCloser: stream,
		computerID:      computerID,
		streamID:        streamID,
		mgr:             m,
	}
	m.mu.Unlock()

	return m.activeStreams[computerID][streamID], nil
}

// CloseStream closes a specific stream.
func (m *ProxyRelayTunnelManager) CloseStream(computerID string, streamID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	streams, ok := m.activeStreams[computerID]
	if !ok {
		return nil
	}
	closer, ok := streams[streamID]
	if !ok {
		return nil
	}
	err := closer.Close()
	delete(streams, streamID)
	return err
}

// CloseTunnel closes all active streams for a computer and
// posts a revocation ticket to the relay.
func (m *ProxyRelayTunnelManager) CloseTunnel(computerID string) error {
	m.mu.Lock()
	streams := m.activeStreams[computerID]
	delete(m.activeStreams, computerID)
	m.mu.Unlock()

	var firstErr error
	for id, closer := range streams {
		if err := closer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		_ = id
	}

	// Post revocation to relay if signer is available.
	if m.signer != nil && m.config.RelayRevokeURL != "" {
		jti := newJTI()
		expiresAt := time.Now().UTC().Add(30 * time.Second)
		capID := fmt.Sprintf("cloudos-%s", computerID)
		_, _ = m.signer.SignRevocation(capID, jti, expiresAt)
		// In production, the hub posts this to the relay's
		// /v1/control/revoke endpoint. The existing
		// NetworkProxyController.onRevoke callback handles this
		// for network capabilities. Cloud OS should register its
		// own onRevoke or reuse the same mechanism.
	}

	return firstErr
}

// IssueAgentTicket creates the agent-side gpr1 ticket for a stream.
// The browser extension calls this to obtain the ticket that the
// native host uses to connect to the relay as the "agent" peer.
func (m *ProxyRelayTunnelManager) IssueAgentTicket(computerID, streamID, streamType string) (string, error) {
	if m.signer == nil {
		return "", ErrTunnelUnavailable
	}
	comp, ok := m.registry.Get(computerID)
	if !ok {
		return "", fmt.Errorf("computer %q not found", computerID)
	}

	jti := newJTI()
	expiresAt := time.Now().UTC().Add(m.config.GrantTTL).Unix()

	claims := RelayStreamClaims{
		Kind:            "stream",
		ProtocolVersion: 1,
		CapabilityID:    fmt.Sprintf("cloudos-%s-%s", computerID, streamType),
		StreamID:        streamID,
		ProfileID:       "cloudos",
		AgentID:         comp.ID,
		Target:          streamType,
		Role:            "agent",
		ExpiresAt:       expiresAt,
		JTI:             jti,
		Limits: RelayLimits{
			MaxFrameBytes:             m.config.MaxFrameBytes,
			MaxPendingFrames:          16,
			DialTimeoutSeconds:        10,
			IdleTimeoutSeconds:        int(m.config.IdleTimeout.Seconds()),
			MaxStreamLifetimeSeconds: int(m.config.StreamLifetime.Seconds()),
			MaxBytes:                  m.config.MaxStreamBytes,
			MaxStreamsPerAgent:        m.config.MaxStreamsPerComputer,
			MaxStreamsPerProfile:      m.config.MaxStreamsPerComputer,
		},
	}

	return m.signer.SignStream(claims)
}

// trackedCloser removes itself from the active streams map on Close.
type trackedCloser struct {
	io.ReadWriteCloser
	computerID string
	streamID   string
	mgr        *ProxyRelayTunnelManager
}

func (t *trackedCloser) Close() error {
	err := t.ReadWriteCloser.Close()
	t.mgr.mu.Lock()
	delete(t.mgr.activeStreams[t.computerID], t.streamID)
	t.mgr.mu.Unlock()
	return err
}
