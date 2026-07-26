package networkproxy

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

// Resolver resolves hostnames locally before the policy pins a numeric address.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// DialContextFunc is the minimal socket dependency needed by Dialer.
type DialContextFunc interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// Limits is the local representation of signed offer or ticket constraints.
type Limits struct {
	DialTimeoutSeconds        int   `json:"dial_timeout_seconds"`
	MaxBytes                  int64 `json:"max_bytes"`
	ConnectionLifetimeSeconds int   `json:"connection_lifetime_seconds"`
}

// Dialer resolves, validates and pins destinations before creating limited TCP connections.
type Dialer struct {
	policy   Policy
	limits   Limits
	resolver Resolver
	dialer   DialContextFunc
}

// NewDialer creates an independent data-plane dialer with no Hub or ShellMCP credentials.
func NewDialer(policy Policy, limits Limits, resolver Resolver, dialer DialContextFunc) (*Dialer, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if limits.DialTimeoutSeconds <= 0 || limits.MaxBytes <= 0 || limits.ConnectionLifetimeSeconds <= 0 {
		return nil, policyError(ErrorInvalidLimits, Target{}, netip.Addr{}, "positive_limits_required", nil)
	}
	if resolver == nil || dialer == nil {
		return nil, policyError(ErrorInvalidLimits, Target{}, netip.Addr{}, "resolver_and_dialer_required", nil)
	}
	return &Dialer{policy: policy, limits: limits, resolver: resolver, dialer: dialer}, nil
}

// DialContext resolves target locally, validates every answer and dials only the pinned IP.
func (d *Dialer) DialContext(ctx context.Context, network, targetText string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, policyError(ErrorNetworkNotAllowed, Target{}, netip.Addr{}, "tcp_required", nil)
	}
	target, err := ParseTarget(targetText)
	if err != nil {
		return nil, err
	}
	addresses, err := d.resolve(ctx, target)
	if err != nil {
		return nil, err
	}
	pinned, err := d.policy.SelectTarget(target, addresses)
	if err != nil {
		return nil, err
	}

	dialCtx, cancel := context.WithTimeout(ctx, time.Duration(d.limits.DialTimeoutSeconds)*time.Second)
	defer cancel()
	conn, err := d.dialer.DialContext(dialCtx, network, pinned.DialAddress())
	if err != nil {
		return nil, policyError(ErrorDialFailed, target, pinned.Address, "socket_dial", err)
	}
	return newLimitedConn(conn, d.limits.MaxBytes, time.Duration(d.limits.ConnectionLifetimeSeconds)*time.Second), nil
}

func (d *Dialer) resolve(ctx context.Context, target Target) ([]netip.Addr, error) {
	if literal, err := netip.ParseAddr(target.Host); err == nil {
		return []netip.Addr{literal}, nil
	}
	addresses, err := d.resolver.LookupNetIP(ctx, "ip", target.Host)
	if err != nil {
		return nil, policyError(ErrorResolutionFailed, target, netip.Addr{}, "local_lookup", err)
	}
	if len(addresses) == 0 {
		return nil, policyError(ErrorResolutionFailed, target, netip.Addr{}, "no_addresses", nil)
	}
	return addresses, nil
}

type limitedConn struct {
	net.Conn
	maxBytes int64

	mu        sync.Mutex
	used      int64
	expired   atomic.Bool
	closeOnce sync.Once
	timer     *time.Timer
}

func newLimitedConn(conn net.Conn, maxBytes int64, lifetime time.Duration) *limitedConn {
	limited := &limitedConn{Conn: conn, maxBytes: maxBytes}
	limited.timer = time.AfterFunc(lifetime, func() { limited.close(true) })
	return limited
}

func (c *limitedConn) Read(p []byte) (int, error) {
	return c.transfer(p, c.Conn.Read)
}

func (c *limitedConn) Write(p []byte) (int, error) {
	return c.transfer(p, c.Conn.Write)
}

func (c *limitedConn) transfer(p []byte, operation func([]byte) (int, error)) (int, error) {
	if c.expired.Load() {
		return 0, ErrConnectionExpired
	}
	reserved, err := c.reserve(int64(len(p)))
	if err != nil {
		return 0, err
	}
	n, operationErr := operation(p[:reserved])
	c.release(int64(reserved - n))
	return n, operationErr
}

func (c *limitedConn) reserve(requested int64) (int, error) {
	if requested == 0 {
		return 0, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.expired.Load() {
		return 0, ErrConnectionExpired
	}
	remaining := c.maxBytes - c.used
	if remaining <= 0 {
		return 0, ErrByteLimitExceeded
	}
	if requested > remaining {
		requested = remaining
	}
	c.used += requested
	return int(requested), nil
}

func (c *limitedConn) release(amount int64) {
	if amount <= 0 {
		return
	}
	c.mu.Lock()
	c.used -= amount
	c.mu.Unlock()
}

func (c *limitedConn) close(expired bool) {
	if expired {
		c.expired.Store(true)
		c.closeOnce.Do(func() {
			_ = c.Conn.Close()
		})
		return
	}
	c.closeOnce.Do(func() {
		if c.timer != nil {
			c.timer.Stop()
		}
		_ = c.Conn.Close()
	})
}

func (c *limitedConn) Close() error {
	c.close(false)
	return nil
}
