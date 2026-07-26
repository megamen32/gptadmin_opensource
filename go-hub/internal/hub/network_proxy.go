package hub

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	networkProxyStateVersion = 1
	networkProxyStateMaxSize = 1 << 20
	networkProxyGrantTTL     = 30 * time.Second
)

var (
	ErrNetworkProxyUnauthorized = errors.New("network proxy request is not authorized")
	ErrNetworkProxyInvalid      = errors.New("invalid network proxy request")
	ErrNetworkProxyNotFound     = errors.New("network proxy capability not found")
	ErrNetworkProxyNotActive    = errors.New("network proxy capability is not active")
	ErrNetworkProxyExpired      = errors.New("network proxy capability expired")
	ErrNetworkProxyRevoked      = errors.New("network proxy capability revoked")
	ErrNetworkProxyTargetDenied = errors.New("network proxy target denied")
	ErrNetworkProxyRoleDenied   = errors.New("network proxy grant role denied")
	ErrNetworkProxyGrantInvalid = errors.New("network proxy grant is invalid")
	ErrNetworkProxyGrantUsed    = errors.New("network proxy grant was already consumed")
	ErrNetworkProxyLimitReached = errors.New("network proxy stream limit reached")
	ErrNetworkProxyUnavailable  = errors.New("network proxy controller unavailable")
)

// NetworkProxyPolicy is the immutable authorization boundary for one
// capability. TargetCIDRs and TargetPorts are both allowlists.
type NetworkProxyPolicy struct {
	Scope       string        `json:"scope"`
	AgentID     string        `json:"agent_id"`
	Mode        string        `json:"mode"`
	TargetCIDRs []string      `json:"target_cidrs"`
	TargetPorts []int         `json:"target_ports"`
	MaxStreams  int           `json:"max_streams"`
	MaxBytes    int64         `json:"max_bytes"`
	Lease       time.Duration `json:"lease"`
}

// NetworkProxyCapability records approved policy, but is never itself a
// credential. Active records are expired during controller restart.
type NetworkProxyCapability struct {
	CapabilityID  string             `json:"capability_id"`
	ProfileID     string             `json:"profile_id"`
	Policy        NetworkProxyPolicy `json:"policy"`
	State         string             `json:"state"`
	RequestedAt   time.Time          `json:"requested_at"`
	ApprovedAt    time.Time          `json:"approved_at,omitempty"`
	ExpiresAt     time.Time          `json:"expires_at,omitempty"`
	StreamsIssued int                `json:"streams_issued"`
}

// ProxyStreamGrant is one role-bound, target-bound, short-lived claim. Token
// values are returned only at issuance and are never persisted.
type ProxyStreamGrant struct {
	CapabilityID string    `json:"capability_id"`
	StreamID     string    `json:"stream_id"`
	AgentID      string    `json:"agent_id"`
	Target       string    `json:"target"`
	Role         string    `json:"role"`
	Token        string    `json:"token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type networkProxyGrantRecord struct {
	Grant    ProxyStreamGrant
	Consumed bool
}

type networkProxyPersistentState struct {
	Version      int                               `json:"version"`
	Capabilities map[string]NetworkProxyCapability `json:"capabilities"`
}

// NetworkProxyController owns capability policy and ephemeral grant claims.
// It deliberately has no dependency on command queues, heartbeat state, or
// relay sockets.
type NetworkProxyController struct {
	mu           sync.Mutex
	statePath    string
	now          func() time.Time
	onRevoke     func(capabilityID string)
	capabilities map[string]NetworkProxyCapability
	grants       map[string]*networkProxyGrantRecord
	relayKey     []byte
	unavailable  error
}

// NewNetworkProxyController loads persisted policy. Any active capability is
// expired before the controller is returned so restart cannot restore access.
func NewNetworkProxyController(statePath string, now func() time.Time, onRevoke func(capabilityID string)) (*NetworkProxyController, error) {
	if now == nil {
		now = time.Now
	}
	c := &NetworkProxyController{
		statePath:    statePath,
		now:          now,
		onRevoke:     onRevoke,
		capabilities: map[string]NetworkProxyCapability{},
		grants:       map[string]*networkProxyGrantRecord{},
	}
	changed, err := c.load()
	if err != nil {
		return nil, err
	}
	if changed {
		if err := c.persistLocked(); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func newUnavailableNetworkProxyController(now func() time.Time, cause error) *NetworkProxyController {
	if now == nil {
		now = time.Now
	}
	return &NetworkProxyController{
		now:          now,
		capabilities: map[string]NetworkProxyCapability{},
		grants:       map[string]*networkProxyGrantRecord{},
		unavailable:  cause,
	}
}

// SetRelayKey enables issuance of relay-compatible, role-bound stream tickets.
// The key is separate from every Hub, ShellMCP, OAuth, and admin credential.
func (c *NetworkProxyController) SetRelayKey(key []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.relayKey = append([]byte(nil), key...)
}

// SetOnRevoke installs the isolated relay control callback before capabilities
// are served. It never carries stream bytes or ShellMCP commands.
func (c *NetworkProxyController) SetOnRevoke(onRevoke func(string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onRevoke = onRevoke
}

func (c *NetworkProxyController) load() (bool, error) {
	if c.statePath == "" {
		return false, nil
	}
	b, err := os.ReadFile(c.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if len(b) > networkProxyStateMaxSize {
		return false, fmt.Errorf("network proxy state exceeds %d bytes", networkProxyStateMaxSize)
	}
	var state networkProxyPersistentState
	if err := json.Unmarshal(b, &state); err != nil {
		return false, fmt.Errorf("decode network proxy state: %w", err)
	}
	if state.Version != networkProxyStateVersion {
		return false, fmt.Errorf("unsupported network proxy state version %d", state.Version)
	}
	changed := false
	for id, capability := range state.Capabilities {
		if id == "" || capability.CapabilityID != id {
			return false, fmt.Errorf("invalid network proxy capability %q", id)
		}
		if err := validateNetworkProxyPolicy(capability.Policy); err != nil {
			return false, fmt.Errorf("invalid network proxy capability %q: %w", id, err)
		}
		if capability.State == "active" {
			capability.State = "expired"
			changed = true
		}
		c.capabilities[id] = cloneNetworkProxyCapability(capability)
	}
	return changed, nil
}

// Request creates a pending capability after its caller has completed profile
// and agent authorization.
func (c *NetworkProxyController) Request(profileID string, policy NetworkProxyPolicy) (NetworkProxyCapability, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.availableLocked(); err != nil {
		return NetworkProxyCapability{}, err
	}
	if strings.TrimSpace(profileID) == "" {
		return NetworkProxyCapability{}, ErrNetworkProxyInvalid
	}
	if err := validateNetworkProxyPolicy(policy); err != nil {
		return NetworkProxyCapability{}, err
	}
	policy = cloneNetworkProxyPolicy(policy)
	capability := NetworkProxyCapability{
		CapabilityID: newID(),
		ProfileID:    strings.TrimSpace(profileID),
		Policy:       policy,
		State:        "pending",
		RequestedAt:  c.now().UTC(),
	}
	c.capabilities[capability.CapabilityID] = capability
	if err := c.persistLocked(); err != nil {
		delete(c.capabilities, capability.CapabilityID)
		return NetworkProxyCapability{}, err
	}
	return cloneNetworkProxyCapability(capability), nil
}

// Approve activates a pending capability and starts its finite lease.
func (c *NetworkProxyController) Approve(capabilityID string) (NetworkProxyCapability, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.availableLocked(); err != nil {
		return NetworkProxyCapability{}, err
	}
	capability, ok := c.capabilities[strings.TrimSpace(capabilityID)]
	if !ok {
		return NetworkProxyCapability{}, ErrNetworkProxyNotFound
	}
	if capability.State == "active" {
		return cloneNetworkProxyCapability(capability), nil
	}
	if capability.State != "pending" {
		return NetworkProxyCapability{}, stateError(capability.State)
	}
	previous := capability
	now := c.now().UTC()
	capability.State = "active"
	capability.ApprovedAt = now
	capability.ExpiresAt = now.Add(capability.Policy.Lease)
	c.capabilities[capability.CapabilityID] = capability
	if err := c.persistLocked(); err != nil {
		c.capabilities[capability.CapabilityID] = previous
		return NetworkProxyCapability{}, err
	}
	return cloneNetworkProxyCapability(capability), nil
}

// Status returns the current capability state and applies lease expiry before
// responding.
func (c *NetworkProxyController) Status(capabilityID string) (NetworkProxyCapability, error) {
	c.mu.Lock()
	if err := c.availableLocked(); err != nil {
		c.mu.Unlock()
		return NetworkProxyCapability{}, err
	}
	capability, ok := c.capabilities[strings.TrimSpace(capabilityID)]
	if !ok {
		c.mu.Unlock()
		return NetworkProxyCapability{}, ErrNetworkProxyNotFound
	}
	expired := c.expireLocked(&capability)
	if expired {
		c.capabilities[capability.CapabilityID] = capability
		if err := c.persistLocked(); err != nil {
			c.mu.Unlock()
			c.signalRevoke(capability.CapabilityID)
			return NetworkProxyCapability{}, err
		}
	}
	result := cloneNetworkProxyCapability(capability)
	c.mu.Unlock()
	if expired {
		c.signalRevoke(capability.CapabilityID)
	}
	return result, nil
}

// IssueStreamGrants creates separate client and agent claims for one approved
// target. The raw tokens and grant records remain memory-only.
func (c *NetworkProxyController) IssueStreamGrants(capabilityID, target string) (ProxyStreamGrant, ProxyStreamGrant, error) {
	c.mu.Lock()
	if err := c.availableLocked(); err != nil {
		c.mu.Unlock()
		return ProxyStreamGrant{}, ProxyStreamGrant{}, err
	}
	capability, ok := c.capabilities[strings.TrimSpace(capabilityID)]
	if !ok {
		c.mu.Unlock()
		return ProxyStreamGrant{}, ProxyStreamGrant{}, ErrNetworkProxyNotFound
	}
	if c.expireLocked(&capability) {
		c.capabilities[capability.CapabilityID] = capability
		_ = c.persistLocked()
		c.mu.Unlock()
		c.signalRevoke(capability.CapabilityID)
		return ProxyStreamGrant{}, ProxyStreamGrant{}, ErrNetworkProxyExpired
	}
	if capability.State != "active" {
		c.mu.Unlock()
		return ProxyStreamGrant{}, ProxyStreamGrant{}, stateError(capability.State)
	}
	canonicalTarget, err := allowedNetworkProxyTarget(capability.Policy, target)
	if err != nil {
		c.mu.Unlock()
		return ProxyStreamGrant{}, ProxyStreamGrant{}, err
	}
	if capability.StreamsIssued >= capability.Policy.MaxStreams {
		c.mu.Unlock()
		return ProxyStreamGrant{}, ProxyStreamGrant{}, ErrNetworkProxyLimitReached
	}
	streamID := newID()
	expiresAt := c.now().UTC().Add(networkProxyGrantTTL)
	if capability.ExpiresAt.Before(expiresAt) {
		expiresAt = capability.ExpiresAt
	}
	clientGrant, clientRecord, err := c.newProxyStreamGrant(capability, streamID, canonicalTarget, "client", expiresAt)
	if err != nil {
		c.mu.Unlock()
		return ProxyStreamGrant{}, ProxyStreamGrant{}, err
	}
	agentGrant, agentRecord, err := c.newProxyStreamGrant(capability, streamID, canonicalTarget, "agent", expiresAt)
	if err != nil {
		c.mu.Unlock()
		return ProxyStreamGrant{}, ProxyStreamGrant{}, err
	}
	previous := capability
	capability.StreamsIssued++
	c.capabilities[capability.CapabilityID] = capability
	if err := c.persistLocked(); err != nil {
		c.capabilities[capability.CapabilityID] = previous
		c.mu.Unlock()
		return ProxyStreamGrant{}, ProxyStreamGrant{}, err
	}
	c.grants[proxyTokenDigest(clientGrant.Token)] = clientRecord
	c.grants[proxyTokenDigest(agentGrant.Token)] = agentRecord
	c.mu.Unlock()
	return clientGrant, agentGrant, nil
}

// Open atomically validates and consumes one grant for the requested role.
func (c *NetworkProxyController) Open(token, role string) (ProxyStreamGrant, error) {
	digest := proxyTokenDigest(strings.TrimSpace(token))
	c.mu.Lock()
	if err := c.availableLocked(); err != nil {
		c.mu.Unlock()
		return ProxyStreamGrant{}, err
	}
	record := c.grants[digest]
	if record == nil || token == "" {
		c.mu.Unlock()
		return ProxyStreamGrant{}, ErrNetworkProxyGrantInvalid
	}
	if record.Consumed {
		c.mu.Unlock()
		return ProxyStreamGrant{}, ErrNetworkProxyGrantUsed
	}
	if role != record.Grant.Role || (role != "client" && role != "agent") {
		c.mu.Unlock()
		return ProxyStreamGrant{}, ErrNetworkProxyRoleDenied
	}
	capability, ok := c.capabilities[record.Grant.CapabilityID]
	if !ok || capability.Policy.AgentID != record.Grant.AgentID {
		c.mu.Unlock()
		return ProxyStreamGrant{}, ErrNetworkProxyGrantInvalid
	}
	if c.expireLocked(&capability) {
		c.capabilities[capability.CapabilityID] = capability
		_ = c.persistLocked()
		c.mu.Unlock()
		c.signalRevoke(capability.CapabilityID)
		return ProxyStreamGrant{}, ErrNetworkProxyExpired
	}
	if capability.State != "active" {
		c.mu.Unlock()
		return ProxyStreamGrant{}, stateError(capability.State)
	}
	if !c.now().UTC().Before(record.Grant.ExpiresAt) {
		c.mu.Unlock()
		return ProxyStreamGrant{}, ErrNetworkProxyExpired
	}
	if _, err := allowedNetworkProxyTarget(capability.Policy, record.Grant.Target); err != nil {
		c.mu.Unlock()
		return ProxyStreamGrant{}, ErrNetworkProxyGrantInvalid
	}
	record.Consumed = true
	result := record.Grant
	result.Token = ""
	c.mu.Unlock()
	return result, nil
}

// Revoke transitions a capability to draining and signals its consumer once.
func (c *NetworkProxyController) Revoke(capabilityID string) (NetworkProxyCapability, error) {
	c.mu.Lock()
	if err := c.availableLocked(); err != nil {
		c.mu.Unlock()
		return NetworkProxyCapability{}, err
	}
	capability, ok := c.capabilities[strings.TrimSpace(capabilityID)]
	if !ok {
		c.mu.Unlock()
		return NetworkProxyCapability{}, ErrNetworkProxyNotFound
	}
	if capability.State == "draining" {
		result := cloneNetworkProxyCapability(capability)
		c.mu.Unlock()
		return result, nil
	}
	previous := capability
	capability.State = "draining"
	c.capabilities[capability.CapabilityID] = capability
	if err := c.persistLocked(); err != nil {
		c.capabilities[capability.CapabilityID] = previous
		c.mu.Unlock()
		return NetworkProxyCapability{}, err
	}
	result := cloneNetworkProxyCapability(capability)
	c.mu.Unlock()
	c.signalRevoke(capability.CapabilityID)
	return result, nil
}

func (c *NetworkProxyController) availableLocked() error {
	if c.unavailable != nil {
		return fmt.Errorf("%w: %v", ErrNetworkProxyUnavailable, c.unavailable)
	}
	return nil
}

func (c *NetworkProxyController) expireLocked(capability *NetworkProxyCapability) bool {
	if capability.State != "active" || capability.ExpiresAt.IsZero() || c.now().UTC().Before(capability.ExpiresAt) {
		return false
	}
	capability.State = "expired"
	return true
}

func (c *NetworkProxyController) signalRevoke(capabilityID string) {
	if c.onRevoke != nil {
		c.onRevoke(capabilityID)
	}
}

func (c *NetworkProxyController) persistLocked() error {
	if c.statePath == "" {
		return nil
	}
	state := networkProxyPersistentState{
		Version:      networkProxyStateVersion,
		Capabilities: make(map[string]NetworkProxyCapability, len(c.capabilities)),
	}
	for id, capability := range c.capabilities {
		state.Capabilities[id] = cloneNetworkProxyCapability(capability)
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if len(b) > networkProxyStateMaxSize {
		return fmt.Errorf("network proxy state exceeds %d bytes", networkProxyStateMaxSize)
	}
	if err := os.MkdirAll(filepath.Dir(c.statePath), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.statePath), ".network-proxy-*")
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
	if err := replaceInstructionFile(tmpName, c.statePath); err != nil {
		return err
	}
	return syncInstructionDirectory(filepath.Dir(c.statePath))
}

func validateNetworkProxyPolicy(policy NetworkProxyPolicy) error {
	if strings.TrimSpace(policy.AgentID) == "" || policy.MaxStreams <= 0 || policy.MaxBytes <= 0 || policy.Lease <= 0 {
		return ErrNetworkProxyInvalid
	}
	switch policy.Mode {
	case "webhook", "pull", "auto":
	default:
		return ErrNetworkProxyInvalid
	}
	switch policy.Scope {
	case "lan", "internet_egress":
	default:
		return ErrNetworkProxyInvalid
	}
	if len(policy.TargetCIDRs) == 0 || len(policy.TargetPorts) == 0 {
		return ErrNetworkProxyInvalid
	}
	for _, raw := range policy.TargetCIDRs {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			return ErrNetworkProxyInvalid
		}
		if policy.Scope == "lan" && !privateNetworkProxyPrefix(prefix) {
			return ErrNetworkProxyInvalid
		}
	}
	seenPorts := map[int]struct{}{}
	for _, port := range policy.TargetPorts {
		if port < 1 || port > 65535 {
			return ErrNetworkProxyInvalid
		}
		if _, exists := seenPorts[port]; exists {
			return ErrNetworkProxyInvalid
		}
		seenPorts[port] = struct{}{}
	}
	return nil
}

func allowedNetworkProxyTarget(policy NetworkProxyPolicy, target string) (string, error) {
	host, rawPort, err := net.SplitHostPort(strings.TrimSpace(target))
	if err != nil {
		return "", ErrNetworkProxyTargetDenied
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		return "", ErrNetworkProxyTargetDenied
	}
	// Treat IPv4-mapped IPv6 literals as their IPv4 address before applying
	// CIDR and reserved-range policy; otherwise ::ffff:100.64.0.1 can bypass
	// an IPv4 egress deny-list under an IPv6 catch-all prefix.
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || !containsInt(policy.TargetPorts, port) {
		return "", ErrNetworkProxyTargetDenied
	}
	allowed := false
	for _, raw := range policy.TargetCIDRs {
		prefix, parseErr := netip.ParsePrefix(strings.TrimSpace(raw))
		if parseErr == nil && prefix.Contains(addr) {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", ErrNetworkProxyTargetDenied
	}
	if policy.Scope == "lan" {
		if !addr.IsPrivate() || isNetworkProxyBroadcast(addr, policy.TargetCIDRs) {
			return "", ErrNetworkProxyTargetDenied
		}
	} else if policy.Scope == "internet_egress" && internetEgressAddressDenied(addr) {
		return "", ErrNetworkProxyTargetDenied
	}
	return net.JoinHostPort(addr.String(), strconv.Itoa(port)), nil
}

func privateNetworkProxyPrefix(prefix netip.Prefix) bool {
	prefix = prefix.Masked()
	for _, raw := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"} {
		privatePrefix := netip.MustParsePrefix(raw)
		if privatePrefix.Bits() <= prefix.Bits() && privatePrefix.Contains(prefix.Addr()) {
			return true
		}
	}
	return false
}

func internetEgressAddressDenied(addr netip.Addr) bool {
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return true
	}
	for _, raw := range []string{
		"0.0.0.0/8",          // "this" network
		"100.64.0.0/10",      // carrier-grade NAT
		"100.100.100.200/32", // cloud metadata
		"192.0.0.0/24",       // IETF protocol assignments
		"192.0.0.192/32",     // cloud metadata
		"192.0.2.0/24",       // TEST-NET-1
		"198.18.0.0/15",      // benchmarking
		"198.51.100.0/24",    // TEST-NET-2
		"203.0.113.0/24",     // TEST-NET-3
		"240.0.0.0/4",        // reserved/future use
		"fd00:ec2::254/128",  // cloud metadata
		"2001:db8::/32",      // IPv6 documentation
	} {
		if netip.MustParsePrefix(raw).Contains(addr) {
			return true
		}
	}
	return addr == netip.MustParseAddr("255.255.255.255")
}

func isNetworkProxyBroadcast(addr netip.Addr, rawPrefixes []string) bool {
	if !addr.Is4() {
		return false
	}
	if addr == netip.MustParseAddr("255.255.255.255") {
		return true
	}
	value := addr.As4()
	addrValue := uint32(value[0])<<24 | uint32(value[1])<<16 | uint32(value[2])<<8 | uint32(value[3])
	for _, raw := range rawPrefixes {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil || !prefix.Addr().Is4() || prefix.Bits() >= 31 || !prefix.Contains(addr) {
			continue
		}
		mask := ^uint32(0) << (32 - prefix.Bits())
		if addrValue|mask == ^uint32(0) {
			return true
		}
	}
	return false
}

func (c *NetworkProxyController) newProxyStreamGrant(capability NetworkProxyCapability, streamID, target, role string, expiresAt time.Time) (ProxyStreamGrant, *networkProxyGrantRecord, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return ProxyStreamGrant{}, nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	if len(c.relayKey) >= 32 {
		jtiBytes := make([]byte, 16)
		if _, err := rand.Read(jtiBytes); err != nil {
			return ProxyStreamGrant{}, nil, err
		}
		jti := base64.RawURLEncoding.EncodeToString(jtiBytes)
		lifetime := int(expiresAt.Sub(c.now().UTC()).Seconds())
		if lifetime < 1 {
			lifetime = 1
		}
		if lifetime > 24*60*60 {
			lifetime = 24 * 60 * 60
		}
		claims := networkProxyRelayClaims{
			Kind: "stream", ProtocolVersion: 1, CapabilityID: capability.CapabilityID,
			StreamID: streamID, ProfileID: capability.ProfileID, AgentID: capability.Policy.AgentID,
			Target: target, Role: role, ExpiresAt: expiresAt.UTC().Unix(), JTI: jti,
			Limits: networkProxyRelayLimits{
				MaxFrameBytes: 32 * 1024, MaxPendingFrames: 16, DialTimeoutSeconds: 10,
				IdleTimeoutSeconds: lifetime, MaxStreamLifetimeSeconds: lifetime,
				MaxBytes: capability.Policy.MaxBytes, MaxStreamsPerAgent: capability.Policy.MaxStreams,
				MaxStreamsPerProfile: capability.Policy.MaxStreams,
			},
		}
		payload, err := json.Marshal(claims)
		if err != nil {
			return ProxyStreamGrant{}, nil, err
		}
		encoded := base64.RawURLEncoding.EncodeToString(payload)
		signed := "gpr1." + encoded
		mac := hmac.New(sha256.New, c.relayKey)
		_, _ = mac.Write([]byte(signed))
		token = signed + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	}
	grant := ProxyStreamGrant{
		CapabilityID: capability.CapabilityID,
		StreamID:     streamID,
		AgentID:      capability.Policy.AgentID,
		Target:       target,
		Role:         role,
		Token:        token,
		ExpiresAt:    expiresAt,
	}
	// The digest of the opaque token is the replay identifier. Keeping a
	// separate JTI would create a second, unused replay contract.
	return grant, &networkProxyGrantRecord{Grant: grant}, nil
}

type networkProxyRelayLimits struct {
	MaxFrameBytes            int64 `json:"max_frame_bytes"`
	MaxPendingFrames         int   `json:"max_pending_frames"`
	DialTimeoutSeconds       int   `json:"dial_timeout_seconds"`
	IdleTimeoutSeconds       int   `json:"idle_timeout_seconds"`
	MaxStreamLifetimeSeconds int   `json:"max_stream_lifetime_seconds"`
	MaxBytes                 int64 `json:"max_bytes"`
	BandwidthBytesPerSecond  int64 `json:"bandwidth_bytes_per_second,omitempty"`
	MaxStreamsPerAgent       int   `json:"max_streams_per_agent"`
	MaxStreamsPerProfile     int   `json:"max_streams_per_profile"`
}

type networkProxyRelayClaims struct {
	Kind            string                  `json:"kind"`
	ProtocolVersion int                     `json:"protocol_version"`
	CapabilityID    string                  `json:"capability_id"`
	StreamID        string                  `json:"stream_id"`
	ProfileID       string                  `json:"profile_id"`
	AgentID         string                  `json:"agent_id"`
	Target          string                  `json:"target"`
	Role            string                  `json:"role"`
	ExpiresAt       int64                   `json:"exp"`
	JTI             string                  `json:"jti"`
	Limits          networkProxyRelayLimits `json:"limits"`
}

func signNetworkProxyRevocation(key []byte, capabilityID string, expiresAt time.Time) (string, error) {
	if len(key) < 32 || strings.TrimSpace(capabilityID) == "" {
		return "", ErrNetworkProxyInvalid
	}
	jtiBytes := make([]byte, 16)
	if _, err := rand.Read(jtiBytes); err != nil {
		return "", err
	}
	claims := struct {
		Kind         string `json:"kind"`
		CapabilityID string `json:"capability_id"`
		ExpiresAt    int64  `json:"exp"`
		JTI          string `json:"jti"`
	}{"revoke", strings.TrimSpace(capabilityID), expiresAt.UTC().Unix(), base64.RawURLEncoding.EncodeToString(jtiBytes)}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signed := "gpr1." + encoded
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(signed))
	return signed + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func proxyTokenDigest(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func stateError(state string) error {
	switch state {
	case "expired":
		return ErrNetworkProxyExpired
	case "draining", "revoked":
		return ErrNetworkProxyRevoked
	default:
		return ErrNetworkProxyNotActive
	}
}

func containsInt(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func cloneNetworkProxyPolicy(policy NetworkProxyPolicy) NetworkProxyPolicy {
	policy.TargetCIDRs = append([]string(nil), policy.TargetCIDRs...)
	policy.TargetPorts = append([]int(nil), policy.TargetPorts...)
	return policy
}

func cloneNetworkProxyCapability(capability NetworkProxyCapability) NetworkProxyCapability {
	capability.Policy = cloneNetworkProxyPolicy(capability.Policy)
	return capability
}

func (s *Server) requestNetworkProxyCapability(profileID string, policy NetworkProxyPolicy) (NetworkProxyCapability, error) {
	if !s.networkProxyPolicyAuthorized(profileID, "network_proxy_request", policy) {
		return NetworkProxyCapability{}, ErrNetworkProxyUnauthorized
	}
	capability, err := s.networkProxy.Request(profileID, policy)
	if err != nil {
		return NetworkProxyCapability{}, err
	}
	s.addNetworkProxyAudit("network_proxy_request", map[string]any{
		"capability_id": capability.CapabilityID,
		"profile_id":    capability.ProfileID,
		"agent_id":      capability.Policy.AgentID,
		"mode":          capability.Policy.Mode,
		"state":         capability.State,
	})
	return capability, nil
}

func (s *Server) networkProxyPolicyAuthorized(profileID, toolName string, policy NetworkProxyPolicy) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	profile, profileFound := s.accessProfiles[strings.TrimSpace(profileID)]
	agent := s.agents[policy.AgentID]
	return profileFound && profile.AccessMode == accessModeFull &&
		containsString(profile.AllowedTargets, policy.AgentID) &&
		containsString(profile.AllowedTools, "network_proxy_request") &&
		containsString(profile.AllowedTools, toolName) &&
		agent != nil && agent.Meta != nil && truthyAny(agent.Meta["approved"])
}

func isNetworkProxyHubTool(name string) bool {
	return strings.HasPrefix(name, "network_proxy_") || strings.HasPrefix(name, "network_access_")
}

func networkAccessCanonicalTool(name string) (string, bool) {
	switch name {
	case "network_access_plan":
		return "network_proxy_request", true
	case "network_access_enable":
		return "network_proxy_approve", true
	case "network_access_status":
		return "network_proxy_status", true
	case "network_access_disable":
		return "network_proxy_revoke", true
	default:
		return "", false
	}
}

func (s *Server) networkAccessAliasAuthorized(profileID, alias, canonical string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	profile, found := s.accessProfiles[strings.TrimSpace(profileID)]
	return found && profile.AccessMode == accessModeFull &&
		containsString(profile.AllowedTools, alias) && containsString(profile.AllowedTools, canonical)
}

func requireNetworkAccessConfirmation(args map[string]any) error {
	confirmed, ok := args["explicit_confirm"].(bool)
	if !ok || !confirmed {
		return fmt.Errorf("%w: explicit_confirm must be true", ErrNetworkProxyInvalid)
	}
	return nil
}

func (s *Server) approveNetworkProxyCapability(capabilityID string) (NetworkProxyCapability, error) {
	return s.approveNetworkProxyCapabilityForProfile("", capabilityID)
}

func (s *Server) approveNetworkProxyCapabilityForProfile(callerProfileID, capabilityID string) (NetworkProxyCapability, error) {
	capability, err := s.networkProxy.Status(capabilityID)
	if err != nil {
		return NetworkProxyCapability{}, err
	}
	if callerProfileID != "" && capability.ProfileID != callerProfileID {
		return NetworkProxyCapability{}, ErrNetworkProxyUnauthorized
	}
	if !s.networkProxyPolicyAuthorized(capability.ProfileID, "network_proxy_approve", capability.Policy) {
		return NetworkProxyCapability{}, ErrNetworkProxyUnauthorized
	}
	capability, err = s.networkProxy.Approve(capabilityID)
	if err != nil {
		return NetworkProxyCapability{}, err
	}
	s.addNetworkProxyAudit("network_proxy_approve", map[string]any{
		"capability_id": capability.CapabilityID,
		"agent_id":      capability.Policy.AgentID,
		"state":         capability.State,
	})
	return capability, nil
}

func (s *Server) statusNetworkProxyCapability(capabilityID string) (NetworkProxyCapability, error) {
	return s.statusNetworkProxyCapabilityForProfile("", capabilityID)
}

func (s *Server) statusNetworkProxyCapabilityForProfile(callerProfileID, capabilityID string) (NetworkProxyCapability, error) {
	capability, err := s.networkProxy.Status(capabilityID)
	if err != nil {
		return NetworkProxyCapability{}, err
	}
	if callerProfileID != "" && capability.ProfileID != callerProfileID {
		return NetworkProxyCapability{}, ErrNetworkProxyUnauthorized
	}
	s.addNetworkProxyAudit("network_proxy_status", map[string]any{
		"capability_id": capability.CapabilityID,
		"state":         capability.State,
	})
	return capability, nil
}

func (s *Server) revokeNetworkProxyCapability(capabilityID string) (NetworkProxyCapability, error) {
	return s.revokeNetworkProxyCapabilityForProfile("", capabilityID)
}

func (s *Server) revokeNetworkProxyCapabilityForProfile(callerProfileID, capabilityID string) (NetworkProxyCapability, error) {
	current, err := s.networkProxy.Status(capabilityID)
	if err != nil {
		return NetworkProxyCapability{}, err
	}
	if callerProfileID != "" && current.ProfileID != callerProfileID {
		return NetworkProxyCapability{}, ErrNetworkProxyUnauthorized
	}
	capability, err := s.networkProxy.Revoke(capabilityID)
	if err != nil {
		return NetworkProxyCapability{}, err
	}
	s.addNetworkProxyAudit("network_proxy_revoke", map[string]any{
		"capability_id": capability.CapabilityID,
		"agent_id":      capability.Policy.AgentID,
		"state":         capability.State,
	})
	return capability, nil
}

func (s *Server) issueNetworkProxyGrants(callerProfileID, capabilityID, target string) (ProxyStreamGrant, ProxyStreamGrant, error) {
	capability, err := s.networkProxy.Status(capabilityID)
	if err != nil {
		return ProxyStreamGrant{}, ProxyStreamGrant{}, err
	}
	if callerProfileID != "" && capability.ProfileID != callerProfileID {
		return ProxyStreamGrant{}, ProxyStreamGrant{}, ErrNetworkProxyUnauthorized
	}
	if !s.networkProxyPolicyAuthorized(capability.ProfileID, "network_proxy_issue", capability.Policy) {
		return ProxyStreamGrant{}, ProxyStreamGrant{}, ErrNetworkProxyUnauthorized
	}
	clientGrant, agentGrant, err := s.networkProxy.IssueStreamGrants(capabilityID, target)
	if err != nil {
		return ProxyStreamGrant{}, ProxyStreamGrant{}, err
	}
	s.addNetworkProxyAudit("network_proxy_issue", map[string]any{
		"capability_id": capability.CapabilityID,
		"stream_id":     clientGrant.StreamID,
		"agent_id":      capability.Policy.AgentID,
	})
	return clientGrant, agentGrant, nil
}

func (s *Server) openNetworkProxyGrant(token, role string) (map[string]any, error) {
	grant, err := s.networkProxy.Open(token, role)
	if err != nil {
		s.addNetworkProxyAudit("network_proxy_open_denied", map[string]any{"role": role, "reason": err.Error()})
		return nil, err
	}
	// Target and token are deliberately omitted from both the response and the
	// audit event. The future relay receives them through its isolated channel.
	result := map[string]any{
		"capability_id": grant.CapabilityID,
		"stream_id":     grant.StreamID,
		"agent_id":      grant.AgentID,
		"role":          grant.Role,
		"expires_at":    grant.ExpiresAt,
	}
	s.addNetworkProxyAudit("network_proxy_open", map[string]any{
		"capability_id": grant.CapabilityID,
		"stream_id":     grant.StreamID,
		"agent_id":      grant.AgentID,
		"role":          grant.Role,
	})
	return result, nil
}

func (s *Server) addNetworkProxyAudit(name string, fields map[string]any) {
	s.mu.Lock()
	s.addAuditLocked(name, fields)
	s.mu.Unlock()
}

func (s *Server) networkProxyRequestHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	var req struct {
		ProfileID string             `json:"profile_id"`
		Policy    NetworkProxyPolicy `json:"policy"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	capability, err := s.requestNetworkProxyCapability(req.ProfileID, req.Policy)
	if err != nil {
		writeNetworkProxyError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"capability": capability})
}

func (s *Server) networkProxyApproveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	capabilityID, ok := readNetworkProxyCapabilityID(w, r)
	if !ok {
		return
	}
	capability, err := s.approveNetworkProxyCapability(capabilityID)
	if err != nil {
		writeNetworkProxyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"capability": capability})
}

func (s *Server) networkProxyIssueHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	var req struct {
		CapabilityID string `json:"capability_id"`
		Target       string `json:"target"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	clientGrant, agentGrant, err := s.issueNetworkProxyGrants("", req.CapabilityID, req.Target)
	if err != nil {
		writeNetworkProxyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"client_grant": clientGrant, "agent_grant": agentGrant})
}

func (s *Server) networkProxyOpenHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	var req struct {
		Token string `json:"token"`
		Role  string `json:"role"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	grant, err := s.openNetworkProxyGrant(req.Token, req.Role)
	if err != nil {
		writeNetworkProxyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"grant": grant})
}

func (s *Server) networkProxyStatusHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	capability, err := s.statusNetworkProxyCapability(r.URL.Query().Get("capability_id"))
	if err != nil {
		writeNetworkProxyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"capability": capability})
}

func (s *Server) networkProxyRevokeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	capabilityID, ok := readNetworkProxyCapabilityID(w, r)
	if !ok {
		return
	}
	capability, err := s.revokeNetworkProxyCapability(capabilityID)
	if err != nil {
		writeNetworkProxyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"capability": capability})
}

func readNetworkProxyCapabilityID(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req struct {
		CapabilityID string `json:"capability_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return "", false
	}
	if strings.TrimSpace(req.CapabilityID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "capability_id is required"})
		return "", false
	}
	return req.CapabilityID, true
}

func writeNetworkProxyError(w http.ResponseWriter, err error) {
	writeJSON(w, networkProxyErrorStatus(err), map[string]any{"detail": err.Error()})
}

func networkProxyErrorStatus(err error) int {
	switch {
	case errors.Is(err, ErrNetworkProxyUnauthorized), errors.Is(err, ErrNetworkProxyRoleDenied), errors.Is(err, ErrNetworkProxyTargetDenied), errors.Is(err, ErrNetworkProxyGrantInvalid):
		return http.StatusForbidden
	case errors.Is(err, ErrNetworkProxyGrantUsed):
		return http.StatusConflict
	case errors.Is(err, ErrNetworkProxyNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrNetworkProxyExpired), errors.Is(err, ErrNetworkProxyRevoked):
		return http.StatusGone
	case errors.Is(err, ErrNetworkProxyNotActive), errors.Is(err, ErrNetworkProxyLimitReached):
		return http.StatusConflict
	case errors.Is(err, ErrNetworkProxyInvalid):
		return http.StatusBadRequest
	case errors.Is(err, ErrNetworkProxyUnavailable):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func (s *Server) callNetworkProxyTool(callerProfileID, name string, args map[string]any) (map[string]any, int) {
	s.addNetworkProxyAudit("hub_tool", map[string]any{"tool": name, "profile_id": callerProfileID, "decision": "evaluate"})
	denied := func(err error) (map[string]any, int) {
		if errors.Is(err, ErrNetworkProxyUnauthorized) {
			s.addNetworkProxyAudit("hub_tool_denied", map[string]any{"tool": name, "profile_id": callerProfileID, "decision": "deny", "reason": err.Error()})
		}
		return map[string]any{"error": err.Error()}, networkProxyErrorStatus(err)
	}
	if callerProfileID == "" {
		return denied(ErrNetworkProxyUnauthorized)
	}
	canonical, alias := networkAccessCanonicalTool(name)
	if alias && !s.networkAccessAliasAuthorized(callerProfileID, name, canonical) {
		return denied(ErrNetworkProxyUnauthorized)
	}
	switch name {
	case "network_proxy_request":
		policy, err := networkProxyPolicyFromArgs(args)
		if err != nil {
			return map[string]any{"error": err.Error()}, networkProxyErrorStatus(err)
		}
		if supplied := firstString(args, "profile_id"); supplied != "" && supplied != callerProfileID {
			return map[string]any{"error": ErrNetworkProxyUnauthorized.Error()}, http.StatusForbidden
		}
		capability, err := s.requestNetworkProxyCapability(callerProfileID, policy)
		if err != nil {
			return denied(err)
		}
		return map[string]any{"capability": capability}, http.StatusCreated
	case "network_access_plan":
		policy, err := networkProxyPolicyFromArgs(args)
		if err != nil {
			return map[string]any{"error": err.Error()}, networkProxyErrorStatus(err)
		}
		capability, err := s.requestNetworkProxyCapability(callerProfileID, policy)
		if err != nil {
			return denied(err)
		}
		return map[string]any{"capability": capability}, http.StatusCreated
	case "network_proxy_approve":
		capability, err := s.approveNetworkProxyCapabilityForProfile(callerProfileID, firstString(args, "capability_id"))
		if err != nil {
			return denied(err)
		}
		return map[string]any{"capability": capability}, http.StatusOK
	case "network_access_enable":
		if err := requireNetworkAccessConfirmation(args); err != nil {
			return map[string]any{"error": err.Error()}, networkProxyErrorStatus(err)
		}
		capability, err := s.approveNetworkProxyCapabilityForProfile(callerProfileID, firstString(args, "capability_id"))
		if err != nil {
			return denied(err)
		}
		return map[string]any{"capability": capability}, http.StatusOK
	case "network_proxy_issue":
		clientGrant, agentGrant, err := s.issueNetworkProxyGrants(callerProfileID, firstString(args, "capability_id"), firstString(args, "target"))
		if err != nil {
			return denied(err)
		}
		return map[string]any{"client_grant": clientGrant, "agent_grant": agentGrant}, http.StatusOK
	case "network_proxy_open":
		grant, err := s.openNetworkProxyGrant(firstString(args, "token"), firstString(args, "role"))
		if err != nil {
			return map[string]any{"error": err.Error()}, networkProxyErrorStatus(err)
		}
		return map[string]any{"grant": grant}, http.StatusOK
	case "network_proxy_status":
		capability, err := s.statusNetworkProxyCapabilityForProfile(callerProfileID, firstString(args, "capability_id"))
		if err != nil {
			return denied(err)
		}
		return map[string]any{"capability": capability}, http.StatusOK
	case "network_access_status":
		capability, err := s.statusNetworkProxyCapabilityForProfile(callerProfileID, firstString(args, "capability_id"))
		if err != nil {
			return denied(err)
		}
		return map[string]any{"capability": capability}, http.StatusOK
	case "network_proxy_revoke":
		capability, err := s.revokeNetworkProxyCapabilityForProfile(callerProfileID, firstString(args, "capability_id"))
		if err != nil {
			return denied(err)
		}
		return map[string]any{"capability": capability}, http.StatusOK
	case "network_access_disable":
		if err := requireNetworkAccessConfirmation(args); err != nil {
			return map[string]any{"error": err.Error()}, networkProxyErrorStatus(err)
		}
		capability, err := s.revokeNetworkProxyCapabilityForProfile(callerProfileID, firstString(args, "capability_id"))
		if err != nil {
			return denied(err)
		}
		return map[string]any{"capability": capability}, http.StatusOK
	default:
		return map[string]any{"error": "unsupported hub tool", "tool": name}, http.StatusBadRequest
	}
}

func networkProxyPolicyFromArgs(args map[string]any) (NetworkProxyPolicy, error) {
	raw := args["policy"]
	if raw == nil {
		raw = args
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return NetworkProxyPolicy{}, ErrNetworkProxyInvalid
	}
	var policy NetworkProxyPolicy
	if err := json.Unmarshal(b, &policy); err != nil {
		return NetworkProxyPolicy{}, ErrNetworkProxyInvalid
	}
	return policy, nil
}

func networkProxyHubTools() []map[string]any {
	capabilityID := map[string]any{"type": "string", "description": "Opaque capability identifier"}
	policy := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"scope":        map[string]any{"type": "string", "enum": []string{"lan", "internet_egress"}},
			"agent_id":     map[string]any{"type": "string"},
			"mode":         map[string]any{"type": "string", "enum": []string{"webhook", "pull", "auto"}},
			"target_cidrs": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"target_ports": map[string]any{"type": "array", "items": map[string]any{"type": "integer", "minimum": 1, "maximum": 65535}},
			"max_streams":  map[string]any{"type": "integer", "minimum": 1},
			"max_bytes":    map[string]any{"type": "integer", "minimum": 1},
			"lease":        map[string]any{"type": "integer", "minimum": 1, "description": "Lease duration in nanoseconds"},
		},
		"required":             []string{"scope", "agent_id", "mode", "target_cidrs", "target_ports", "max_streams", "max_bytes", "lease"},
		"additionalProperties": false,
	}
	return []map[string]any{
		{"name": "network_proxy_request", "description": "Request a bounded Network Tunnel capability for the authenticated profile", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"policy": policy}, "required": []string{"policy"}, "additionalProperties": false}},
		{"name": "network_proxy_approve", "description": "Approve a pending Network Tunnel capability", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"capability_id": capabilityID}, "required": []string{"capability_id"}, "additionalProperties": false}},
		{"name": "network_proxy_issue", "description": "Issue one client grant and one agent grant for an approved target", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"capability_id": capabilityID, "target": map[string]any{"type": "string"}}, "required": []string{"capability_id", "target"}, "additionalProperties": false}},
		{"name": "network_proxy_open", "description": "Atomically consume one role-bound stream grant", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"token": map[string]any{"type": "string"}, "role": map[string]any{"type": "string", "enum": []string{"client", "agent"}}}, "required": []string{"token", "role"}, "additionalProperties": false}},
		{"name": "network_proxy_status", "description": "Read capability state without touching agent liveness", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"capability_id": capabilityID}, "required": []string{"capability_id"}, "additionalProperties": false}},
		{"name": "network_proxy_revoke", "description": "Stop new grants and drain a capability", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"capability_id": capabilityID}, "required": []string{"capability_id"}, "additionalProperties": false}},
		{"name": "network_access_plan", "description": "Plan a Network Tunnel capability for lan or internet_egress. The policy permits bounded TCP only; no UDP. Planning does not enable access.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"policy": policy}, "required": []string{"policy"}, "additionalProperties": false}},
		{"name": "network_access_enable", "description": "Enable a planned Network Tunnel for lan or internet_egress after explicit confirmation. Set explicit_confirm=true. Streams are bounded TCP only; no UDP.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"capability_id": capabilityID, "explicit_confirm": map[string]any{"type": "boolean", "description": "Must be true to enable access"}}, "required": []string{"capability_id", "explicit_confirm"}, "additionalProperties": false}},
		{"name": "network_access_status", "description": "Read the status of a Network Tunnel for lan or internet_egress. It reports bounded TCP-only access; no UDP.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"capability_id": capabilityID}, "required": []string{"capability_id"}, "additionalProperties": false}},
		{"name": "network_access_disable", "description": "Disable a Network Tunnel for lan or internet_egress after explicit confirmation. Set explicit_confirm=true. This controls bounded TCP only; no UDP.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"capability_id": capabilityID, "explicit_confirm": map[string]any{"type": "boolean", "description": "Must be true to disable access"}}, "required": []string{"capability_id", "explicit_confirm"}, "additionalProperties": false}},
	}
}
