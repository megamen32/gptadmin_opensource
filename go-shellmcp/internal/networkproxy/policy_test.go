package networkproxy

import (
	"errors"
	"net/netip"
	"testing"
)

func mustPrefix(t *testing.T, raw string) netip.Prefix {
	t.Helper()
	prefix, err := netip.ParsePrefix(raw)
	if err != nil {
		t.Fatalf("parse prefix %q: %v", raw, err)
	}
	return prefix
}

func mustAddr(t *testing.T, raw string) netip.Addr {
	t.Helper()
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		t.Fatalf("parse address %q: %v", raw, err)
	}
	return addr
}

func TestParseTargetRejectsMalformedHostPort(t *testing.T) {
	for _, raw := range []string{"example.com", "example.com:0", "example.com:65536", ":443"} {
		t.Run(raw, func(t *testing.T) {
			_, err := ParseTarget(raw)
			if !errors.Is(err, ErrInvalidTarget) {
				t.Fatalf("ParseTarget(%q) error = %v, want ErrInvalidTarget", raw, err)
			}
		})
	}
}

func TestPolicyRejectsBlockedAddressClassesForInternetEgress(t *testing.T) {
	policy := Policy{Scope: ScopeInternetEgress, AllowedPorts: []uint16{443}}
	target, err := ParseTarget("origin.example:443")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		addr string
		want string
	}{
		{name: "loopback", addr: "127.0.0.1", want: "loopback"},
		{name: "private", addr: "10.1.2.3", want: "private"},
		{name: "carrier-grade NAT", addr: "100.64.0.1", want: "cgnat"},
		{name: "link local", addr: "169.254.1.2", want: "link-local"},
		{name: "multicast", addr: "224.0.0.1", want: "multicast"},
		{name: "broadcast", addr: "255.255.255.255", want: "broadcast"},
		{name: "metadata", addr: "169.254.169.254", want: "metadata"},
		{name: "documentation", addr: "192.0.2.1", want: "documentation"},
		{name: "benchmark", addr: "198.18.0.1", want: "benchmark"},
		{name: "reserved", addr: "240.0.0.1", want: "reserved"},
		{name: "IPv6 loopback", addr: "::1", want: "loopback"},
		{name: "IPv6 private", addr: "fd00::1", want: "private"},
		{name: "IPv6 documentation", addr: "2001:db8::1", want: "documentation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := policy.SelectTarget(target, []netip.Addr{mustAddr(t, tc.addr)})
			if !errors.Is(err, ErrBlockedAddress) {
				t.Fatalf("SelectTarget(%s) error = %v, want ErrBlockedAddress", tc.addr, err)
			}
			var policyErr *PolicyError
			if !errors.As(err, &policyErr) || policyErr.Reason != tc.want {
				t.Fatalf("SelectTarget(%s) error = %#v, want reason %q", tc.addr, err, tc.want)
			}
		})
	}
}

func TestPolicyLANRequiresApprovedPrivateCIDRAndPort(t *testing.T) {
	policy := Policy{
		Scope:            ScopeLAN,
		ApprovedLANCIDRs: []netip.Prefix{mustPrefix(t, "10.20.0.0/16"), mustPrefix(t, "fd12:3456::/48")},
		AllowedPorts:     []uint16{443, 8443},
	}

	target, err := ParseTarget("printer.lan:8443")
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := policy.SelectTarget(target, []netip.Addr{mustAddr(t, "10.20.1.4")})
	if err != nil {
		t.Fatalf("SelectTarget() error = %v", err)
	}
	if got, want := pinned.DialAddress(), "10.20.1.4:8443"; got != want {
		t.Fatalf("DialAddress() = %q, want %q", got, want)
	}

	for _, addr := range []string{"10.21.1.4", "127.0.0.1", "169.254.169.254", "8.8.8.8"} {
		t.Run("reject_"+addr, func(t *testing.T) {
			_, err := policy.SelectTarget(target, []netip.Addr{mustAddr(t, addr)})
			if !errors.Is(err, ErrBlockedAddress) {
				t.Fatalf("SelectTarget(%s) error = %v, want ErrBlockedAddress", addr, err)
			}
		})
	}

	wrongPort, err := ParseTarget("printer.lan:22")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := policy.SelectTarget(wrongPort, []netip.Addr{mustAddr(t, "10.20.1.4")}); !errors.Is(err, ErrPortNotAllowed) {
		t.Fatalf("SelectTarget(wrong port) error = %v, want ErrPortNotAllowed", err)
	}
}

func TestPolicyInternetEgressRequiresPublicAddressAndApprovedPort(t *testing.T) {
	policy := Policy{Scope: ScopeInternetEgress, AllowedPorts: []uint16{443}}
	target, err := ParseTarget("origin.example:443")
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := policy.SelectTarget(target, []netip.Addr{mustAddr(t, "8.8.8.8")})
	if err != nil {
		t.Fatalf("SelectTarget() error = %v", err)
	}
	if got, want := pinned.Address.String(), "8.8.8.8"; got != want {
		t.Fatalf("pinned address = %q, want %q", got, want)
	}

	wrongPort, err := ParseTarget("origin.example:80")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := policy.SelectTarget(wrongPort, []netip.Addr{mustAddr(t, "8.8.8.8")}); !errors.Is(err, ErrPortNotAllowed) {
		t.Fatalf("SelectTarget(wrong port) error = %v, want ErrPortNotAllowed", err)
	}
}

func TestPolicyNormalizesMappedIPv6AndRejectsAnyUnsafeResolution(t *testing.T) {
	policy := Policy{Scope: ScopeInternetEgress, AllowedPorts: []uint16{443}}
	target, err := ParseTarget("origin.example:443")
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := policy.SelectTarget(target, []netip.Addr{mustAddr(t, "::ffff:8.8.8.8")})
	if err != nil {
		t.Fatalf("SelectTarget(mapped IPv6) error = %v", err)
	}
	if got, want := pinned.Address.String(), "8.8.8.8"; got != want {
		t.Fatalf("pinned address = %q, want %q", got, want)
	}

	_, err = policy.SelectTarget(target, []netip.Addr{mustAddr(t, "8.8.8.8"), mustAddr(t, "127.0.0.1")})
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("mixed resolution error = %v, want ErrBlockedAddress", err)
	}
}
