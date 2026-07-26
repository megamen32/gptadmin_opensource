// Package networkproxy enforces destination policy for the edge proxy data plane.
package networkproxy

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// Scope identifies the destination class authorized by an offer or ticket.
type Scope string

const (
	// ScopeLAN allows only explicitly configured private network destinations.
	ScopeLAN Scope = "lan"
	// ScopeInternetEgress allows only public global Internet destinations.
	ScopeInternetEgress Scope = "internet_egress"
)

// ErrorKind categorizes a policy or dial failure for callers that need stable handling.
type ErrorKind string

const (
	ErrorInvalidTarget     ErrorKind = "invalid_target"
	ErrorInvalidPolicy     ErrorKind = "invalid_policy"
	ErrorPortNotAllowed    ErrorKind = "port_not_allowed"
	ErrorBlockedAddress    ErrorKind = "blocked_address"
	ErrorResolutionFailed  ErrorKind = "resolution_failed"
	ErrorInvalidLimits     ErrorKind = "invalid_limits"
	ErrorNetworkNotAllowed ErrorKind = "network_not_allowed"
	ErrorDialFailed        ErrorKind = "dial_failed"
	ErrorByteLimitExceeded ErrorKind = "byte_limit_exceeded"
	ErrorConnectionExpired ErrorKind = "connection_expired"
)

// PolicyError is a typed, non-secret error returned by this package.
type PolicyError struct {
	Kind    ErrorKind
	Target  Target
	Address netip.Addr
	Reason  string
	Err     error
}

func (e *PolicyError) Error() string {
	parts := []string{string(e.Kind)}
	if e.Reason != "" {
		parts = append(parts, e.Reason)
	}
	if e.Target.Host != "" {
		parts = append(parts, e.Target.String())
	}
	if e.Address.IsValid() {
		parts = append(parts, e.Address.String())
	}
	return strings.Join(parts, ": ")
}

// Unwrap exposes the underlying resolver or socket error, when there is one.
func (e *PolicyError) Unwrap() error { return e.Err }

// Is compares errors by stable kind so callers can use errors.Is.
func (e *PolicyError) Is(target error) bool {
	other, ok := target.(*PolicyError)
	return ok && e.Kind == other.Kind
}

var (
	ErrInvalidTarget     = &PolicyError{Kind: ErrorInvalidTarget}
	ErrInvalidPolicy     = &PolicyError{Kind: ErrorInvalidPolicy}
	ErrPortNotAllowed    = &PolicyError{Kind: ErrorPortNotAllowed}
	ErrBlockedAddress    = &PolicyError{Kind: ErrorBlockedAddress}
	ErrResolutionFailed  = &PolicyError{Kind: ErrorResolutionFailed}
	ErrInvalidLimits     = &PolicyError{Kind: ErrorInvalidLimits}
	ErrNetworkNotAllowed = &PolicyError{Kind: ErrorNetworkNotAllowed}
	ErrDialFailed        = &PolicyError{Kind: ErrorDialFailed}
	ErrByteLimitExceeded = &PolicyError{Kind: ErrorByteLimitExceeded}
	ErrConnectionExpired = &PolicyError{Kind: ErrorConnectionExpired}
)

func policyError(kind ErrorKind, target Target, address netip.Addr, reason string, err error) *PolicyError {
	return &PolicyError{Kind: kind, Target: target, Address: address, Reason: reason, Err: err}
}

// Target is a parsed host and TCP port before resolution.
type Target struct {
	Host string
	Port uint16
}

// String returns the canonical host:port presentation of the target.
func (t Target) String() string {
	return net.JoinHostPort(t.Host, strconv.Itoa(int(t.Port)))
}

// ParseTarget parses a non-empty TCP host:port pair without resolving it.
func ParseTarget(raw string) (Target, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return Target{}, policyError(ErrorInvalidTarget, Target{}, netip.Addr{}, "empty_or_whitespace", nil)
	}
	host, portText, err := net.SplitHostPort(raw)
	if err != nil || host == "" {
		return Target{}, policyError(ErrorInvalidTarget, Target{}, netip.Addr{}, "host_port_required", err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return Target{}, policyError(ErrorInvalidTarget, Target{}, netip.Addr{}, "valid_port_required", err)
	}
	return Target{Host: strings.ToLower(host), Port: uint16(port)}, nil
}

// PinnedTarget binds a validated resolved IP to the original target port.
type PinnedTarget struct {
	Target  Target
	Address netip.Addr
}

// DialAddress returns the numeric address that must be used for the socket dial.
func (t PinnedTarget) DialAddress() string {
	return net.JoinHostPort(t.Address.String(), strconv.Itoa(int(t.Target.Port)))
}

// Policy is the local enforcement policy derived from a signed offer or ticket.
// ApprovedLANCIDRs applies only to ScopeLAN; AllowedPorts applies to both scopes.
type Policy struct {
	Scope            Scope
	ApprovedLANCIDRs []netip.Prefix
	AllowedPorts     []uint16
}

// Validate checks that the policy itself cannot widen either destination scope.
func (p Policy) Validate() error {
	if p.Scope != ScopeLAN && p.Scope != ScopeInternetEgress {
		return policyError(ErrorInvalidPolicy, Target{}, netip.Addr{}, "unknown_scope", nil)
	}
	if len(p.AllowedPorts) == 0 {
		return policyError(ErrorInvalidPolicy, Target{}, netip.Addr{}, "ports_required", nil)
	}
	for _, port := range p.AllowedPorts {
		if port == 0 {
			return policyError(ErrorInvalidPolicy, Target{}, netip.Addr{}, "invalid_port", nil)
		}
	}
	if p.Scope == ScopeInternetEgress {
		if len(p.ApprovedLANCIDRs) != 0 {
			return policyError(ErrorInvalidPolicy, Target{}, netip.Addr{}, "lan_cidrs_not_allowed_for_internet_egress", nil)
		}
		return nil
	}
	if len(p.ApprovedLANCIDRs) == 0 {
		return policyError(ErrorInvalidPolicy, Target{}, netip.Addr{}, "lan_cidrs_required", nil)
	}
	for _, prefix := range p.ApprovedLANCIDRs {
		if !isPrivatePrefix(prefix) {
			return policyError(ErrorInvalidPolicy, Target{}, prefix.Addr(), "lan_cidr_must_be_private", nil)
		}
	}
	return nil
}

// SelectTarget validates every locally resolved address and pins the first safe one.
func (p Policy) SelectTarget(target Target, addresses []netip.Addr) (PinnedTarget, error) {
	if err := p.Validate(); err != nil {
		return PinnedTarget{}, err
	}
	if !p.allowsPort(target.Port) {
		return PinnedTarget{}, policyError(ErrorPortNotAllowed, target, netip.Addr{}, "port_not_approved", nil)
	}
	if len(addresses) == 0 {
		return PinnedTarget{}, policyError(ErrorResolutionFailed, target, netip.Addr{}, "no_addresses", nil)
	}

	normalized := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if reason := p.addressReason(address); reason != "" {
			return PinnedTarget{}, policyError(ErrorBlockedAddress, target, address, reason, nil)
		}
		normalized = append(normalized, address)
	}
	return PinnedTarget{Target: target, Address: normalized[0]}, nil
}

func (p Policy) allowsPort(port uint16) bool {
	for _, allowed := range p.AllowedPorts {
		if allowed == port {
			return true
		}
	}
	return false
}

func (p Policy) addressReason(address netip.Addr) string {
	if !address.IsValid() {
		return "invalid"
	}
	if isMetadataAddress(address) {
		return "metadata"
	}
	if address.IsLoopback() {
		return "loopback"
	}
	if address.IsUnspecified() {
		return "unspecified"
	}
	if address.IsMulticast() {
		return "multicast"
	}
	if address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() {
		return "link-local"
	}
	if address.Is4() && address == netip.MustParseAddr("255.255.255.255") {
		return "broadcast"
	}
	if inPrefixes(address, documentationPrefixes) {
		return "documentation"
	}
	if inPrefixes(address, benchmarkPrefixes) {
		return "benchmark"
	}
	if inPrefixes(address, carrierGradeNATPrefixes) {
		return "cgnat"
	}
	if inPrefixes(address, reservedPrefixes) || !address.IsGlobalUnicast() {
		return "reserved"
	}

	if p.Scope == ScopeInternetEgress {
		if address.IsPrivate() {
			return "private"
		}
		return ""
	}
	if !address.IsPrivate() {
		return "not-private"
	}
	for _, prefix := range p.ApprovedLANCIDRs {
		if prefix.Contains(address) {
			return ""
		}
	}
	return "private"
}

var (
	privatePrefixes         = mustPrefixes("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7")
	documentationPrefixes   = mustPrefixes("192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24", "2001:db8::/32")
	benchmarkPrefixes       = mustPrefixes("198.18.0.0/15", "2001:2::/48")
	carrierGradeNATPrefixes = mustPrefixes("100.64.0.0/10")
	reservedPrefixes        = mustPrefixes(
		"0.0.0.0/8", "192.0.0.0/24", "192.88.99.0/24", "240.0.0.0/4",
		"100::/64", "2001::/23", "2002::/16",
	)
	metadataAddresses = []netip.Addr{netip.MustParseAddr("169.254.169.254"), netip.MustParseAddr("fd00:ec2::254")}
)

func mustPrefixes(raw ...string) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(raw))
	for _, value := range raw {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			panic(fmt.Sprintf("invalid static network prefix %q: %v", value, err))
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes
}

func inPrefixes(address netip.Addr, prefixes []netip.Prefix) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func isMetadataAddress(address netip.Addr) bool {
	for _, metadata := range metadataAddresses {
		if address == metadata {
			return true
		}
	}
	return false
}

func isPrivatePrefix(prefix netip.Prefix) bool {
	if !prefix.IsValid() {
		return false
	}
	for _, privatePrefix := range privatePrefixes {
		if privatePrefix.Addr().BitLen() == prefix.Addr().BitLen() && privatePrefix.Contains(prefix.Addr()) && privatePrefix.Bits() <= prefix.Bits() {
			return true
		}
	}
	return false
}

// IsTyped reports whether err is a networkproxy error with the requested kind.
func IsTyped(err error, kind ErrorKind) bool {
	return errors.Is(err, &PolicyError{Kind: kind})
}
