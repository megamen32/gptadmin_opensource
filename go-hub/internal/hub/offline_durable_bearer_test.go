package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOfflineDurableBearerSnapshotContract(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	const origin = "https://offline.test"
	const resource = origin + "/mcp"
	profile := AccessProfile{
		ID: "durable-local", AccessMode: accessModeFull, ApprovalMode: approvalModeUnrestricted,
		AllowedTargets: []string{"hub", "shell:fixture"}, AllowedTools: []string{"shell_exec"},
	}
	for _, tc := range []struct {
		name     string
		alter    func(*managedMCPToken)
		mangle   bool
		optOut   bool
		readonly bool
		want     int
	}{
		{name: "durable policy works with no signer", want: http.StatusOK},
		{name: "cannot opt in through the JWT signer flag", optOut: true, want: http.StatusUnauthorized},
		{name: "digest mismatch", mangle: true, want: http.StatusUnauthorized},
		{name: "configured alias", alter: func(r *managedMCPToken) { r.TokenKind = configuredMCPBearerTokenKind }, want: http.StatusUnauthorized},
		{name: "revoked", alter: func(r *managedMCPToken) { r.RevokedAt = now.Unix() }, want: http.StatusUnauthorized},
		{name: "expired", alter: func(r *managedMCPToken) { r.ExpiresAt = now.Unix() }, want: http.StatusUnauthorized},
		{name: "wrong issuer", alter: func(r *managedMCPToken) { r.Issuer = "https://other.test" }, want: http.StatusUnauthorized},
		{name: "wrong audience", alter: func(r *managedMCPToken) { r.Audience = "https://other.test/mcp" }, want: http.StatusUnauthorized},
		{name: "future issuance", alter: func(r *managedMCPToken) { r.IssuedAt = now.Unix() + 600 }, want: http.StatusUnauthorized},
		{name: "unknown scope", alter: func(r *managedMCPToken) { r.Scope = "gptadmin.unrecognized" }, want: http.StatusUnauthorized},
		{name: "unbound managed client", alter: func(r *managedMCPToken) { r.ProfileID = "" }, want: http.StatusForbidden},
		{name: "managed profile uses normal fallback to client binding", alter: func(r *managedMCPToken) {
			r.ProfileID, r.ClientID = "", "bound-client"
		}, want: http.StatusOK},
		{name: "read-only token stays read-only under a full profile", readonly: true, alter: func(r *managedMCPToken) {
			r.AccessMode, r.Scope = accessModeReadonly, "gptadmin.read gptadmin.inspect"
		}, want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token, record, err := newManagedMCPToken("unregistered-client", 1, origin, resource, accessModeFull, profile.ID, "client", now.Unix())
			if err != nil {
				t.Fatal(err)
			}
			if tc.alter != nil {
				tc.alter(&record)
			}
			if tc.mangle {
				token += "x"
			}
			record.TokenValue = "" // only the digest is needed in an applied record
			cfg := Config{PublicOrigin: origin, MCPResource: resource, Now: func() time.Time { return now },
				OfflineDurableBearer: !tc.optOut, ReaderOfflineSharedSigner: tc.optOut}
			s := New(cfg)
			defer s.Close()
			bundle := authSnapshot{
				Tokens: &managedMCPTokenState{Tokens: map[string]managedMCPToken{record.ID: record}},
				Clients: &oauthClientsState{Clients: map[string]oauthClientMetadata{
					"bound-client": {ProfileID: profile.ID},
				}},
				Profiles: &accessProfilesState{Profiles: map[string]AccessProfile{profile.ID: profile}},
			}
			view := newOfflineShellAuthView(cfg, securitySettings{}, bundle)
			r := httptest.NewRequest(http.MethodPost, resource, nil)
			r.Header.Set("Authorization", "Bearer "+token)
			r = r.WithContext(context.WithValue(r.Context(), offlineShellMCPKey{}, true))
			w := httptest.NewRecorder()
			claims, bound, ok := s.authenticateOfflineShellBearer(w, r, view)
			if w.Code != tc.want || ok != (tc.want == http.StatusOK) {
				t.Fatalf("HTTP %d, verified=%t, wanted %d", w.Code, ok, tc.want)
			}
			if ok {
				authorized := requestWithAccessProfile(requestWithAuthClaims(r, claims), bound)
				if bound.ID != profile.ID || firstString(claims, "jti") != record.ID ||
					(requestAccessMode(authorized) == accessModeReadonly) != tc.readonly {
					t.Fatal("unexpected credential identity, profile, or effective access mode")
				}
			}
		})
	}
}
