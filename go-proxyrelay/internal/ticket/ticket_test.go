package ticket_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/megamen32/gptadmin/go-proxyrelay/internal/ticket"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

func validClaims(role, jti string, now time.Time) ticket.Claims {
	return ticket.Claims{
		Kind:            ticket.KindStream,
		ProtocolVersion: 1,
		CapabilityID:    "cap-1",
		StreamID:        "stream-1",
		ProfileID:       "profile-1",
		AgentID:         "agent-1",
		Target:          "192.0.2.10:443",
		Role:            role,
		ExpiresAt:       now.Add(time.Minute).Unix(),
		JTI:             jti,
		Limits: ticket.Limits{
			MaxFrameBytes:             32 * 1024,
			MaxPendingFrames:          4,
			DialTimeoutSeconds:        2,
			IdleTimeoutSeconds:        5,
			MaxStreamLifetimeSeconds: 30,
			MaxBytes:                  1 << 20,
			BandwidthBytesPerSecond:   64 * 1024,
			MaxStreamsPerAgent:        2,
			MaxStreamsPerProfile:      3,
		},
	}
}

func TestStreamTicketIsRoleBoundAndConsumedOnce(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	signer, err := ticket.NewSigner(testKey)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := ticket.NewVerifier(testKey, func() time.Time { return now }, 16)
	if err != nil {
		t.Fatal(err)
	}
	token, err := signer.SignStream(validClaims(ticket.RoleClient, "jti-client", now))
	if err != nil {
		t.Fatal(err)
	}

	got, err := verifier.VerifyAndConsumeStream(context.Background(), token, ticket.RoleClient)
	if err != nil {
		t.Fatalf("verify valid stream ticket: %v", err)
	}
	if got.CapabilityID != "cap-1" || got.StreamID != "stream-1" || got.AgentID != "agent-1" {
		t.Fatalf("unexpected claims: %#v", got)
	}
	if _, err := verifier.VerifyAndConsumeStream(context.Background(), token, ticket.RoleClient); !errors.Is(err, ticket.ErrReplay) {
		t.Fatalf("replay error = %v, want ErrReplay", err)
	}
}

func TestStreamTicketRejectsWrongRoleExpiredAndOpaqueControlTokens(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	signer, _ := ticket.NewSigner(testKey)

	t.Run("wrong role", func(t *testing.T) {
		verifier, _ := ticket.NewVerifier(testKey, func() time.Time { return now }, 16)
		token, err := signer.SignStream(validClaims(ticket.RoleAgent, "jti-agent", now))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := verifier.VerifyAndConsumeStream(context.Background(), token, ticket.RoleClient); !errors.Is(err, ticket.ErrRole) {
			t.Fatalf("error = %v, want ErrRole", err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		verifier, _ := ticket.NewVerifier(testKey, func() time.Time { return now }, 16)
		claims := validClaims(ticket.RoleClient, "jti-expired", now)
		claims.ExpiresAt = now.Add(-time.Second).Unix()
		token, err := signer.SignStream(claims)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := verifier.VerifyAndConsumeStream(context.Background(), token, ticket.RoleClient); !errors.Is(err, ticket.ErrExpired) {
			t.Fatalf("error = %v, want ErrExpired", err)
		}
	})

	for _, raw := range []string{"shellmcp-secret", "oauth-admin-token", "queue-job-id"} {
		verifier, _ := ticket.NewVerifier(testKey, func() time.Time { return now }, 16)
		if _, err := verifier.VerifyAndConsumeStream(context.Background(), raw, ticket.RoleClient); !errors.Is(err, ticket.ErrMalformed) {
			t.Fatalf("opaque control token %q error = %v, want ErrMalformed", raw, err)
		}
	}
}

func TestStreamTicketRequiresFiniteV1Limits(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	signer, _ := ticket.NewSigner(testKey)

	tests := []struct {
		name   string
		mutate func(*ticket.Claims)
	}{
		{"protocol", func(c *ticket.Claims) { c.ProtocolVersion = 2 }},
		{"capability", func(c *ticket.Claims) { c.CapabilityID = "" }},
		{"profile", func(c *ticket.Claims) { c.ProfileID = "" }},
		{"frame", func(c *ticket.Claims) { c.Limits.MaxFrameBytes = 0 }},
		{"queue", func(c *ticket.Claims) { c.Limits.MaxPendingFrames = 0 }},
		{"stream lifetime", func(c *ticket.Claims) { c.Limits.MaxStreamLifetimeSeconds = 0 }},
		{"idle timeout", func(c *ticket.Claims) { c.Limits.IdleTimeoutSeconds = 0 }},
		{"bytes", func(c *ticket.Claims) { c.Limits.MaxBytes = 0 }},
		{"agent streams", func(c *ticket.Claims) { c.Limits.MaxStreamsPerAgent = 0 }},
		{"profile streams", func(c *ticket.Claims) { c.Limits.MaxStreamsPerProfile = 0 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			claims := validClaims(ticket.RoleClient, "jti-"+tc.name, now)
			tc.mutate(&claims)
			if _, err := signer.SignStream(claims); !errors.Is(err, ticket.ErrClaims) {
				t.Fatalf("error = %v, want ErrClaims", err)
			}
		})
	}
}

func TestRevocationTicketIsScopedAndReplayProtected(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	signer, _ := ticket.NewSigner(testKey)
	verifier, _ := ticket.NewVerifier(testKey, func() time.Time { return now }, 16)
	raw, err := signer.SignRevocation("cap-1", "revoke-1", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := verifier.VerifyAndConsumeRevocation(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if claim.CapabilityID != "cap-1" {
		t.Fatalf("capability = %q", claim.CapabilityID)
	}
	if _, err := verifier.VerifyAndConsumeRevocation(context.Background(), raw); !errors.Is(err, ticket.ErrReplay) {
		t.Fatalf("replay error = %v, want ErrReplay", err)
	}
}

func TestReplayCacheFailsClosedAtBound(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	signer, _ := ticket.NewSigner(testKey)
	verifier, _ := ticket.NewVerifier(testKey, func() time.Time { return now }, 1)
	first, _ := signer.SignStream(validClaims(ticket.RoleClient, "jti-1", now))
	secondClaims := validClaims(ticket.RoleClient, "jti-2", now)
	secondClaims.StreamID = "stream-2"
	second, _ := signer.SignStream(secondClaims)
	if _, err := verifier.VerifyAndConsumeStream(context.Background(), first, ticket.RoleClient); err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.VerifyAndConsumeStream(context.Background(), second, ticket.RoleClient); !errors.Is(err, ticket.ErrReplayCacheFull) {
		t.Fatalf("error = %v, want ErrReplayCacheFull", err)
	}
	if got := verifier.ReplayEntries(); got != 1 {
		t.Fatalf("replay entries = %d, want 1", got)
	}
}
