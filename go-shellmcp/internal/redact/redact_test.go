package redact

import (
	"strings"
	"testing"
)

func TestSecretsRemovesRecognizableCredentials(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhZG1pbiJ9.c2lnbmF0dXJl"
	input := strings.Join([]string{
		"Authorization: Bearer " + jwt,
		"API_KEY=sk-live-1234567890",
		"password: correct-horse-battery-staple",
		"Cookie: session=abc123; csrf=def456",
		"-----BEGIN PRIVATE KEY-----",
		"private-material",
		"-----END PRIVATE KEY-----",
	}, "\n")

	got := Secrets(input)
	for _, secret := range []string{jwt, "sk-live-1234567890", "correct-horse-battery-staple", "abc123", "def456", "private-material"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted output leaked %q: %s", secret, got)
		}
	}
	for _, marker := range []string{"<redacted:bearer>", "<redacted:api-key>", "<redacted:password>", "<redacted:cookie>", "<redacted:private-key>"} {
		if !strings.Contains(got, marker) {
			t.Fatalf("redacted output missing marker %q: %s", marker, got)
		}
	}
}

func TestSecretsLeavesOrdinaryDiagnosticTextUnchanged(t *testing.T) {
	input := "service=nginx status=active requests=42"
	if got := Secrets(input); got != input {
		t.Fatalf("ordinary output changed: %q", got)
	}
}
