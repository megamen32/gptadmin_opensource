package hub

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestChildScopeLegacyStdioPreservesExistingBridge(t *testing.T) {
	r := connectionTestRequest("one", "owner-a", 1)
	agent := Agent{AgentID: "child-a", Transport: "stdio", Meta: map[string]any{"connection_instance": strings.Repeat("a", 64)}}
	input := map[string]any{"ref": "child-a", "_connection_scope": "caller-supplied", "_connection_instance": "caller-instance", "_connection_expires_at": int64(1)}
	legacy := childConnectionArguments(r, agent, input)
	for _, field := range []string{"_connection_scope", "_connection_instance", "_connection_expires_at"} {
		if _, exists := legacy[field]; exists {
			t.Fatalf("legacy stdio entered disabled managed ownership path: %s", field)
		}
	}
	if input["_connection_scope"] != "caller-supplied" {
		t.Fatal("caller arguments were mutated")
	}
	for _, transport := range []string{"streamable-http", "sse"} {
		agent.Transport = transport
		for _, request := range []*http.Request{r, connectionTestRequest("refreshed-bearer", "owner-a", 1)} {
			for _, field := range []string{"_connection_scope", "_connection_instance", "_connection_expires_at"} {
				if _, exists := childConnectionArguments(request, agent, input)[field]; exists {
					t.Fatalf("legacy remote allocated a retained partition: %s %s", transport, field)
				}
			}
		}
	}
	// Explicit managed context remains authoritative; only the legacy fallback
	// changes. The HTTP endpoint independently gates unsupported stdio ownership.
	agent.Transport = "stdio"
	managed := r.WithContext(context.WithValue(r.Context(), connectionContextKey{}, connectionScope{Target: agent.AgentID, Key: strings.Repeat("b", 64), Instance: strings.Repeat("a", 64)}))
	if childConnectionArguments(managed, agent, input)["_connection_scope"] != strings.Repeat("b", 64) {
		t.Fatal("explicit managed context lost its scope")
	}
}

// These tests cover local partition and authorization binding logic only.
// Native Codex/ZCode execution remains the migration acceptance gate.
func connectionTestRequest(token, profileID string, version int64) *http.Request {
	r, _ := http.NewRequest(http.MethodPost, "http://localhost/connection/test/mcp", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	r = requestWithAuthClaims(r, map[string]any{"sub": "admin", "client_id": "codex", "iss": "hub", "aud": "hub", "scope": "gptadmin.read gptadmin.exec"})
	return requestWithAccessProfile(r, AccessProfile{ID: profileID, Version: version})
}

func TestConnectionPrincipalSeparatesCredentialsAndProfileRevisions(t *testing.T) {
	base := connectionPrincipal(connectionTestRequest("one", "owner-a", 1))
	for _, r := range []*http.Request{
		connectionTestRequest("two", "owner-a", 1),
		connectionTestRequest("one", "owner-b", 1),
		connectionTestRequest("one", "owner-a", 2),
	} {
		if connectionPrincipal(r) == base {
			t.Fatal("distinct authenticated scopes share a principal")
		}
	}
	if connectionPrincipal(connectionTestRequest("one", "owner-a", 1)) != base {
		t.Fatal("same verified scope cannot reuse its connection")
	}
	r, _ := http.NewRequest(http.MethodPost, "http://localhost/", nil)
	r.Header.Set("Authorization", "Bearer one")
	if connectionPrincipal(r) != "" {
		t.Fatal("unverified header establishes a principal")
	}
}

func TestConnectionMutableSessionsAreSeparatedAndBound(t *testing.T) {
	s := &Server{}
	agent := Agent{AgentID: "child-a"}
	id, first, err := s.newConnection(agent, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := s.newConnection(agent, "owner-a")
	if err != nil || second.Scope == first.Scope {
		t.Fatal("mutable consumer sessions share a partition", err)
	}
	if _, ok := s.lookupConnection(id, "child-a", "owner-a", "session", ""); !ok {
		t.Fatal("valid session rejected")
	}
	for _, pair := range [][2]string{{"child-b", "owner-a"}, {"child-a", "owner-b"}} {
		if _, ok := s.lookupConnection(id, pair[0], pair[1], "session", ""); ok {
			t.Fatal("session crossed its authenticated owner/target binding")
		}
	}
	first.Expires = time.Now().Add(-time.Second)
	s.connectionSessions[id] = first
	if _, ok := s.lookupConnection(id, "child-a", "owner-a", "session", ""); ok {
		t.Fatal("expired session accepted")
	}
}

func TestConnectionStatelessReuseIsExplicitAndOwnerBound(t *testing.T) {
	s := &Server{}
	agent := Agent{AgentID: "child-a", Meta: map[string]any{"connection_mode": "stateless"}}
	_, first, _ := s.newConnection(agent, "owner-a")
	_, second, _ := s.newConnection(agent, "owner-a")
	_, other, _ := s.newConnection(agent, "owner-b")
	if first.Scope != second.Scope || first.Scope == other.Scope {
		t.Fatal("stateless reuse failed to preserve owner isolation")
	}
}

func TestConnectionSessionCapacityFailsClosedAndReclaimsExpired(t *testing.T) {
	s := &Server{}
	agent := Agent{AgentID: "child-a"}
	for i := 0; i < connectionSessionLimit; i++ {
		if _, _, err := s.newConnection(agent, "owner-a"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.newConnection(agent, "owner-a"); err == nil {
		t.Fatal("unbounded downstream session registry")
	}
	for id, sess := range s.connectionSessions {
		sess.Expires = time.Now().Add(-time.Second)
		s.connectionSessions[id] = sess
		break
	}
	if _, _, err := s.newConnection(agent, "owner-a"); err != nil {
		t.Fatal("expired registry entry was not reclaimed", err)
	}
}

func TestChildScopeCannotBeSuppliedByToolArguments(t *testing.T) {
	agent := Agent{AgentID: "child-a", Meta: map[string]any{"connection_instance": "trusted-instance"}}
	input := map[string]any{"ref": "child-a", "_connection_scope": "caller-supplied"}
	result := childConnectionArguments(connectionTestRequest("one", "owner-a", 1), agent, input)
	if result["_connection_scope"] == input["_connection_scope"] || input["_connection_scope"] != "caller-supplied" {
		t.Fatal("caller controls scope or input arguments were mutated")
	}
	if _, ok := childConnectionArguments(nil, agent, input)["_connection_scope"]; ok {
		t.Fatal("unauthenticated internal call retains supplied scope")
	}
}

func TestConnectionConnectorRestartRejectsOldSessionBeforeRenewal(t *testing.T) {
	s := &Server{}
	agent := Agent{AgentID: "child-a", Meta: map[string]any{"connection_instance": "first-instance"}}
	id, session, err := s.newConnection(agent, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.lookupConnection(id, agent.AgentID, "owner-a", "session", "replacement-instance"); ok {
		t.Fatal("old downstream session survived connector restart")
	}
	if s.connectionSessions[id].Expires != session.Expires {
		t.Fatal("rejected session renewed its lifetime")
	}
}

func TestConnectionClosingSessionRetainsCapacityAndCannotBeReused(t *testing.T) {
	s := &Server{}
	agent := Agent{AgentID: "child-a", Meta: map[string]any{"connection_mode": "stateless"}}
	id, session, err := s.newConnection(agent, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	session.Closing = true
	session.Expires = time.Now().Add(-time.Second)
	s.connectionSessions[id] = session
	if _, ok := s.lookupConnection(id, agent.AgentID, "owner-a", "stateless", ""); ok {
		t.Fatal("closing session accepted another request")
	}
	_, replacement, err := s.newConnection(agent, "owner-a")
	if err != nil || replacement.Scope == session.Scope {
		t.Fatal("initialize reused a closing upstream group", err)
	}
	if !s.connectionSessions[id].Closing {
		t.Fatal("expiration released an unconfirmed teardown reservation")
	}
	for len(s.connectionSessions) < connectionSessionLimit {
		if _, _, err := s.newConnection(agent, "owner-b"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.newConnection(agent, "owner-c"); err == nil {
		t.Fatal("closing reservation did not count toward capacity")
	}
}
