package networkproxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"
)

type resolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (f resolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return f(ctx, network, host)
}

type dialFunc func(context.Context, string, string) (net.Conn, error)

func (f dialFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return f(ctx, network, address)
}

func testDialer(t *testing.T, limits Limits, resolver Resolver, dial DialContextFunc) *Dialer {
	t.Helper()
	dialer, err := NewDialer(Policy{Scope: ScopeInternetEgress, AllowedPorts: []uint16{443}}, limits, resolver, dial)
	if err != nil {
		t.Fatalf("NewDialer() error = %v", err)
	}
	return dialer
}

func TestDialerPinsValidatedResolvedIPForLiveConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable: %v", err)
	}
	defer listener.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()

	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	var gotAddress string
	dialer, err := NewDialer(Policy{Scope: ScopeInternetEgress, AllowedPorts: []uint16{port}}, Limits{DialTimeoutSeconds: 1, MaxBytes: 64, ConnectionLifetimeSeconds: 2},
		resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{mustAddr(t, "8.8.8.8")}, nil
		}),
		dialFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
			gotAddress = address
			return (&net.Dialer{}).DialContext(ctx, network, listener.Addr().String())
		}),
	)
	if err != nil {
		t.Fatalf("NewDialer() error = %v", err)
	}

	conn, err := dialer.DialContext(context.Background(), "tcp", net.JoinHostPort("origin.example", strconv.Itoa(int(port))))
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer conn.Close()
	serverConn := <-accepted
	defer serverConn.Close()

	if want := "8.8.8.8:" + strconv.Itoa(int(port)); gotAddress != want {
		t.Fatalf("dialed address = %q, want pinned %q", gotAddress, want)
	}
	if _, err := conn.Write([]byte("ok")); err != nil {
		t.Fatalf("write to live listener: %v", err)
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(serverConn, buf); err != nil || string(buf) != "ok" {
		t.Fatalf("listener received %q, %v; want ok", buf, err)
	}
}

func TestDialerEnforcesByteLimitAcrossDirections(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	dialer := testDialer(t, Limits{DialTimeoutSeconds: 1, MaxBytes: 4, ConnectionLifetimeSeconds: 2},
		resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{mustAddr(t, "8.8.8.8")}, nil
		}),
		dialFunc(func(context.Context, string, string) (net.Conn, error) { return client, nil }),
	)

	conn, err := dialer.DialContext(context.Background(), "tcp", "origin.example:443")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		_, _ = io.Copy(io.Discard, server)
	}()
	if _, err := conn.Write([]byte("four")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := conn.Write([]byte("x")); !errors.Is(err, ErrByteLimitExceeded) {
		t.Fatalf("over-limit write error = %v, want ErrByteLimitExceeded", err)
	}
	_ = conn.Close()
	<-readDone
}

func TestDialerAppliesDialTimeoutAndConnectionLifetime(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable: %v", err)
	}
	defer listener.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()

	var deadlineSeen bool
	dialer := testDialer(t, Limits{DialTimeoutSeconds: 1, MaxBytes: 64, ConnectionLifetimeSeconds: 1},
		resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{mustAddr(t, "8.8.8.8")}, nil
		}),
		dialFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
			_, deadlineSeen = ctx.Deadline()
			return (&net.Dialer{}).DialContext(ctx, network, listener.Addr().String())
		}),
	)

	conn, err := dialer.DialContext(context.Background(), "tcp", "origin.example:443")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	serverConn := <-accepted
	defer serverConn.Close()
	if !deadlineSeen {
		t.Fatal("dial context had no timeout deadline")
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		_ = conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
		_, writeErr := conn.Write([]byte("x"))
		if errors.Is(writeErr, ErrConnectionExpired) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("connection remained usable after lifetime: last error %v", writeErr)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestDialerReturnsTypedResolverAndLimitErrors(t *testing.T) {
	_, err := NewDialer(Policy{Scope: ScopeInternetEgress, AllowedPorts: []uint16{443}}, Limits{}, resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) { return nil, nil }), dialFunc(func(context.Context, string, string) (net.Conn, error) { return nil, nil }))
	if !errors.Is(err, ErrInvalidLimits) {
		t.Fatalf("NewDialer(invalid limits) error = %v, want ErrInvalidLimits", err)
	}

	dialer := testDialer(t, Limits{DialTimeoutSeconds: 1, MaxBytes: 4, ConnectionLifetimeSeconds: 1}, resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return nil, errors.New("resolver unavailable")
	}), dialFunc(func(context.Context, string, string) (net.Conn, error) { return nil, nil }))
	_, err = dialer.DialContext(context.Background(), "tcp", "origin.example:443")
	if !errors.Is(err, ErrResolutionFailed) {
		t.Fatalf("DialContext(resolver error) = %v, want ErrResolutionFailed", err)
	}
	var policyErr *PolicyError
	if !errors.As(err, &policyErr) || policyErr.Kind != ErrorResolutionFailed {
		t.Fatalf("DialContext(resolver error) typed error = %#v", err)
	}
}
