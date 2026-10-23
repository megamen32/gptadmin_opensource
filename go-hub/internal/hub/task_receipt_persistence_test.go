package hub

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func taskReceiptTestProfile(version int64) AccessProfile {
	return AccessProfile{
		ID:             "receipt-profile",
		Version:        version,
		AccessMode:     accessModeFull,
		ApprovalMode:   approvalModeUnrestricted,
		AllowedTargets: []string{"hub", "file:node-44"},
		AllowedTools:   []string{"system_inspect"},
	}
}

func taskReceiptTestRequest(method, path, clientID, tokenID string, profile AccessProfile) *http.Request {
	r := requestWithAuthClaims(httptest.NewRequest(method, path, nil), map[string]any{
		"client_id": clientID,
		"jti":       tokenID,
		"sub":       clientID,
	})
	return requestWithAccessProfile(r, profile)
}

func saveCompletedReceiptFixture(t *testing.T, dir string) (*Server, string, string, AccessProfile) {
	t.Helper()
	s := New(Config{ConfigDir: dir})
	profile := taskReceiptTestProfile(1)
	if err := saveAccessProfilesState(s.accessProfilesStatePath(), map[string]AccessProfile{profile.ID: profile}); err != nil {
		_ = s.Close()
		t.Fatal(err)
	}
	caller := &taskCaller{
		ClientID: "receipt-owner", TokenID: "receipt-token", Subject: "receipt-owner",
		ProfileID: profile.ID, ProfileVersion: profile.Version,
	}
	now := nowFloat()
	done := now - taskCacheSeconds - 5
	s.mu.Lock()
	ownerID, err := s.ensureTaskOwnerLocked()
	if err != nil {
		s.mu.Unlock()
		_ = s.Close()
		t.Fatal(err)
	}
	shellID := "shell-receipt-after-cache"
	relayID := "relay-receipt-after-cache"
	s.shellJobs[shellID] = &shellJob{
		ID: shellID, OwnerID: ownerID, Server: "node-44", ToolName: "system_inspect",
		Caller: caller, CreatedAt: done - 1, StartedAt: done - 0.1, DoneAt: done,
		Status: "completed", CachedAt: done, Result: map[string]any{"sentinel": "shell-safe-result"},
		FilesystemACL: &filesystemACLPolicy{
			Version: 1, ProfileID: profile.ID, ProfileVersion: profile.Version,
			RootsDigest: "receipt-only-fixture", JobID: shellID, Target: "file:node-44",
			Tool: "system_inspect", ArgumentsSHA256: "receipt-only-fixture",
		},
	}
	s.relayJobs[relayID] = &relayJob{
		ID: relayID, OwnerID: ownerID, AgentID: "file:node-44", Method: "tools/call",
		Caller: caller, CreatedAt: done - 1, StartedAt: done - 0.1, DoneAt: done,
		Status: "completed", CachedAt: done, Result: map[string]any{"sentinel": "relay-safe-result"},
	}
	if err := s.saveTaskStateLocked(shellID, relayID); err != nil {
		s.mu.Unlock()
		_ = s.Close()
		t.Fatal(err)
	}
	s.mu.Unlock()
	return s, shellID, relayID, profile
}

func rehydrateCompletedReceiptFixture(t *testing.T, dir string, profile AccessProfile, ids ...string) *Server {
	t.Helper()
	s := New(Config{ConfigDir: dir})
	if got, ok := s.accessProfiles[profile.ID]; !ok || got.Version != profile.Version {
		_ = s.Close()
		t.Fatalf("test profile not restored from the fixture store: %#v", got)
	}
	s.mu.Lock()
	if err := s.refreshTaskRecordsLocked(ids...); err != nil {
		s.mu.Unlock()
		_ = s.Close()
		t.Fatal(err)
	}
	s.mu.Unlock()
	return s
}

func TestPersistedShellReceiptSurvivesTerminalCacheAndKeepsACLCallerBinding(t *testing.T) {
	dir := t.TempDir()
	writer, shellID, _, profile := saveCompletedReceiptFixture(t, dir)
	defer writer.Close()
	reader := rehydrateCompletedReceiptFixture(t, dir, profile, shellID)
	defer reader.Close()

	job := reader.shellJobs[shellID]
	if job == nil || job.Caller == nil || job.Caller.TokenID != "receipt-token" || job.Caller.ProfileID != profile.ID || job.Caller.ProfileVersion != profile.Version {
		t.Fatalf("durable caller identity not restored: %#v", job)
	}
	if job.FilesystemACL != nil {
		t.Fatal("restoring a receipt must not restore the executable filesystem ACL policy")
	}
	ownerReq := taskReceiptTestRequest(http.MethodGet, "/mcp", "receipt-owner", "receipt-token", profile)
	if got := taskCallerFromRequest(ownerReq); !sameTaskCaller(got, job.Caller) {
		t.Fatalf("restored owner identity does not match current request: got=%#v want=%#v", got, job.Caller)
	}
	if err := reader.authorizeTaskReceipt(ownerReq, job.Caller, nil); err != nil {
		t.Fatalf("current profile could not authorize restored caller: %v", err)
	}
	result, rpcErr := reader.mcpTaskGetForRequest(ownerReq, shellID, "")
	if rpcErr != nil || firstString(mapValue(result), "resultType") != "complete" {
		t.Fatalf("own completed task receipt unavailable after cache eviction: result=%v err=%v", result, rpcErr)
	}

	foreignReq := taskReceiptTestRequest(http.MethodGet, "/mcp-relay/job/"+shellID+"?ack=true", "receipt-foreign", "foreign-token", profile)
	foreign := httptest.NewRecorder()
	reader.mcpRelayJob(foreign, foreignReq)
	if foreign.Code != http.StatusNotFound || reader.shellJobs[shellID] == nil {
		t.Fatalf("foreign acknowledgement must be masked and preserve the receipt: code=%d present=%v body=%s", foreign.Code, reader.shellJobs[shellID] != nil, foreign.Body.String())
	}

	staleProfile := taskReceiptTestProfile(2)
	if err := saveAccessProfilesState(reader.accessProfilesStatePath(), map[string]AccessProfile{profile.ID: staleProfile}); err != nil {
		t.Fatal(err)
	}
	staleReq := taskReceiptTestRequest(http.MethodGet, "/mcp", "receipt-owner", "receipt-token", profile)
	_, staleErr := reader.mcpTaskGetForRequest(staleReq, shellID, "")
	if staleErr == nil || reader.shellJobs[shellID] == nil {
		t.Fatalf("stale profile receipt must fail closed and remain present: err=%v present=%v", staleErr, reader.shellJobs[shellID] != nil)
	}

	if err := saveAccessProfilesState(reader.accessProfilesStatePath(), map[string]AccessProfile{profile.ID: profile}); err != nil {
		t.Fatal(err)
	}
	ackReq := taskReceiptTestRequest(http.MethodGet, "/mcp-relay/job/"+shellID+"?ack=true", "receipt-owner", "receipt-token", profile)
	ack := httptest.NewRecorder()
	reader.mcpRelayJob(ack, ackReq)
	if ack.Code != http.StatusOK || reader.shellJobs[shellID] != nil {
		t.Fatalf("current owner could not acknowledge its restored receipt: code=%d present=%v body=%s", ack.Code, reader.shellJobs[shellID] != nil, ack.Body.String())
	}
}

func TestPersistedRelayReceiptSurvivesTerminalCache(t *testing.T) {
	dir := t.TempDir()
	writer, _, relayID, profile := saveCompletedReceiptFixture(t, dir)
	defer writer.Close()
	reader := rehydrateCompletedReceiptFixture(t, dir, profile, relayID)
	defer reader.Close()

	job := reader.relayJobs[relayID]
	if job == nil || job.Caller == nil || job.Caller.TokenID != "receipt-token" || job.Caller.ProfileID != profile.ID || job.Caller.ProfileVersion != profile.Version {
		t.Fatalf("durable relay caller identity not restored: %#v", job)
	}
	ownerReq := taskReceiptTestRequest(http.MethodGet, "/mcp", "receipt-owner", "receipt-token", profile)
	if got := taskCallerFromRequest(ownerReq); !sameTaskCaller(got, job.Caller) {
		t.Fatalf("restored relay owner identity does not match current request: got=%#v want=%#v", got, job.Caller)
	}
	if err := reader.authorizeTaskReceipt(ownerReq, job.Caller, nil); err != nil {
		t.Fatalf("current profile could not authorize restored relay caller: %v", err)
	}
	result, rpcErr := reader.mcpTaskGetForRequest(ownerReq, relayID, "")
	if rpcErr != nil || firstString(mapValue(result), "resultType") != "complete" {
		t.Fatalf("own relay task receipt unavailable after cache eviction: result=%v err=%v", result, rpcErr)
	}
	foreignReq := taskReceiptTestRequest(http.MethodGet, "/mcp", "receipt-foreign", "foreign-token", profile)
	if _, err := reader.mcpTaskGetForRequest(foreignReq, relayID, ""); err == nil {
		t.Fatal("foreign relay caller received a persisted task receipt")
	}
	if reader.relayJobs[relayID] == nil {
		t.Fatal("foreign relay read removed the persisted receipt")
	}
	foreignAckReq := taskReceiptTestRequest(http.MethodGet, "/mcp-relay/job/"+relayID+"?ack=true", "receipt-foreign", "foreign-token", profile)
	foreignAck := httptest.NewRecorder()
	reader.mcpRelayJob(foreignAck, foreignAckReq)
	if foreignAck.Code != http.StatusNotFound || reader.relayJobs[relayID] == nil {
		t.Fatalf("foreign relay acknowledgement must be masked and preserve the receipt: code=%d present=%v body=%s", foreignAck.Code, reader.relayJobs[relayID] != nil, foreignAck.Body.String())
	}

	staleProfile := taskReceiptTestProfile(2)
	if err := saveAccessProfilesState(reader.accessProfilesStatePath(), map[string]AccessProfile{profile.ID: staleProfile}); err != nil {
		t.Fatal(err)
	}
	staleReq := taskReceiptTestRequest(http.MethodGet, "/mcp", "receipt-owner", "receipt-token", profile)
	if _, err := reader.mcpTaskGetForRequest(staleReq, relayID, ""); err == nil || reader.relayJobs[relayID] == nil {
		t.Fatalf("stale relay profile must fail closed and preserve its receipt: err=%v present=%v", err, reader.relayJobs[relayID] != nil)
	}
	if err := saveAccessProfilesState(reader.accessProfilesStatePath(), map[string]AccessProfile{profile.ID: profile}); err != nil {
		t.Fatal(err)
	}
	ownerAliasReq := taskReceiptTestRequest(http.MethodGet, "/mcp-relay/job/"+relayID, "receipt-owner", "receipt-token", profile)
	ownerAlias := httptest.NewRecorder()
	reader.mcpRelayJob(ownerAlias, ownerAliasReq)
	if ownerAlias.Code != http.StatusOK || reader.relayJobs[relayID] == nil {
		t.Fatalf("current relay owner could not read its restored alias: code=%d present=%v body=%s", ownerAlias.Code, reader.relayJobs[relayID] != nil, ownerAlias.Body.String())
	}
}

func TestLegacyPersistedTaskWithoutCallerRemainsFailClosed(t *testing.T) {
	dir := t.TempDir()
	writer := New(Config{ConfigDir: dir})
	defer writer.Close()
	profile := taskReceiptTestProfile(1)
	if err := saveAccessProfilesState(writer.accessProfilesStatePath(), map[string]AccessProfile{profile.ID: profile}); err != nil {
		t.Fatal(err)
	}
	id := "legacy-receipt-without-caller"
	now := nowFloat()
	writer.mu.Lock()
	writer.shellJobs[id] = &shellJob{ID: id, Server: "node-44", Status: "completed", CreatedAt: now - taskCacheSeconds - 10, DoneAt: now - taskCacheSeconds - 5, CachedAt: now - taskCacheSeconds - 5, Result: map[string]any{"sentinel": "legacy-result"}}
	if err := writer.saveTaskStateLocked(id); err != nil {
		writer.mu.Unlock()
		t.Fatal(err)
	}
	writer.mu.Unlock()

	reader := rehydrateCompletedReceiptFixture(t, dir, profile, id)
	defer reader.Close()
	ownerReq := taskReceiptTestRequest(http.MethodGet, "/mcp", "receipt-owner", "receipt-token", profile)
	_, rpcErr := reader.mcpTaskGetForRequest(ownerReq, id, "")
	if rpcErr == nil || reader.shellJobs[id] == nil || reader.shellJobs[id].Caller != nil {
		t.Fatalf("legacy receipt without caller must remain present and fail closed: err=%v job=%#v", rpcErr, reader.shellJobs[id])
	}
}
