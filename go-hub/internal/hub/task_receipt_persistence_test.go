package hub

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
	relayID := "legacy-relay-receipt-without-caller"
	now := nowFloat()
	writer.mu.Lock()
	writer.shellJobs[id] = &shellJob{ID: id, Server: "node-44", Status: "completed", CreatedAt: now - taskCacheSeconds - 10, DoneAt: now - taskCacheSeconds - 5, CachedAt: now - taskCacheSeconds - 5, Result: map[string]any{"sentinel": "legacy-result"}}
	writer.relayJobs[relayID] = &relayJob{ID: relayID, AgentID: "file:node-44", Method: "tools/call", Status: "completed", CreatedAt: now - taskCacheSeconds - 10, DoneAt: now - taskCacheSeconds - 5, CachedAt: now - taskCacheSeconds - 5, Result: map[string]any{"sentinel": "legacy-relay-result"}}
	if err := writer.saveTaskStateLocked(id, relayID); err != nil {
		writer.mu.Unlock()
		t.Fatal(err)
	}
	writer.mu.Unlock()

	reader := rehydrateCompletedReceiptFixture(t, dir, profile, id, relayID)
	defer reader.Close()
	ownerReq := taskReceiptTestRequest(http.MethodGet, "/mcp", "receipt-owner", "receipt-token", profile)
	_, rpcErr := reader.mcpTaskGetForRequest(ownerReq, id, "")
	if rpcErr == nil || reader.shellJobs[id] == nil || reader.shellJobs[id].Caller != nil {
		t.Fatalf("legacy receipt without caller must remain present and fail closed: err=%v job=%#v", rpcErr, reader.shellJobs[id])
	}
	_, rpcErr = reader.mcpTaskGetForRequest(ownerReq, relayID, "")
	if rpcErr == nil || reader.relayJobs[relayID] == nil || reader.relayJobs[relayID].Caller != nil {
		t.Fatalf("legacy relay receipt without caller must remain present and fail closed: err=%v job=%#v", rpcErr, reader.relayJobs[relayID])
	}
}

func TestMCPRelayJobAckDurablyConsumesRehydratedReceipt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		idFor    func(shellID, relayID string) string
		kind     string
		ackPath  func(id string) string
		readPath func(id string) string
	}{
		{
			name:    "shell job alias",
			idFor:   func(shellID, _ string) string { return shellID },
			kind:    "shell",
			ackPath: func(id string) string { return "/mcp-relay/job/" + id + "?ack=true" },
			readPath: func(id string) string {
				return "/mcp-relay/get_mcp_job/" + id
			},
		},
		{
			name:    "relay job alias",
			idFor:   func(_, relayID string) string { return relayID },
			kind:    "relay",
			ackPath: func(id string) string { return "/mcp-relay/get_mcp_job/" + id + "?ack=1" },
			readPath: func(id string) string {
				return "/mcp-relay/job/" + id
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writer, shellID, relayID, profile := saveCompletedReceiptFixture(t, dir)
			defer writer.Close()
			id := tc.idFor(shellID, relayID)
			newHTTPServer := func() *Server {
				s := New(Config{
					ConfigDir: dir, OAuthClientSecret: "receipt-test-secret",
					PublicOrigin: "https://hub.example", MCPResource: "https://hub.example",
				})
				if got, ok := s.accessProfiles[profile.ID]; !ok || got.Version != profile.Version {
					_ = s.Close()
					t.Fatalf("test profile was not loaded: %#v", got)
				}
				return s
			}
			server := newHTTPServer()
			defer server.Close()
			sibling := newHTTPServer()
			defer sibling.Close()
			ownerToken, err := server.signJWT(map[string]any{
				"sub": "receipt-owner", "client_id": "receipt-owner", "jti": "receipt-token",
				"profile_id": profile.ID, "scope": "gptadmin.read gptadmin.exec",
				"iss": "https://hub.example", "aud": "https://hub.example", "resource": "https://hub.example",
				"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "kid": defaultJWTKeyID,
			})
			if err != nil {
				t.Fatal(err)
			}
			foreignToken, err := server.signJWT(map[string]any{
				"sub": "receipt-foreign", "client_id": "receipt-foreign", "jti": "foreign-token",
				"profile_id": profile.ID, "scope": "gptadmin.read gptadmin.exec",
				"iss": "https://hub.example", "aud": "https://hub.example", "resource": "https://hub.example",
				"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "kid": defaultJWTKeyID,
			})
			if err != nil {
				t.Fatal(err)
			}
			call := func(s *Server, token, path string) *httptest.ResponseRecorder {
				t.Helper()
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.Header.Set("Authorization", "Bearer "+token)
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, req)
				return w
			}

			if got := call(server, ownerToken, tc.readPath(id)); got.Code != http.StatusOK {
				t.Fatalf("rehydrated owner receipt unavailable before ack: status=%d body=%s", got.Code, got.Body.String())
			}
			if got := call(sibling, ownerToken, tc.readPath(id)); got.Code != http.StatusOK {
				t.Fatalf("sibling Hub could not warm its independent task cache: status=%d body=%s", got.Code, got.Body.String())
			}
			foreignAck := call(server, foreignToken, tc.ackPath(id))
			cacheHasReceipt := server.shellJobs[id] != nil
			if tc.kind == "relay" {
				cacheHasReceipt = server.relayJobs[id] != nil
			}
			if foreignAck.Code != http.StatusNotFound || !cacheHasReceipt {
				t.Fatalf("foreign acknowledgement was not denied without removing the cache receipt: status=%d body=%s present=%v", foreignAck.Code, foreignAck.Body.String(), cacheHasReceipt)
			}
			beforeOwnerAck, err := server.readTaskRecords(id)
			if err != nil {
				t.Fatal(err)
			}
			if (tc.kind == "shell" && beforeOwnerAck.Shell[id].ID == "") || (tc.kind == "relay" && beforeOwnerAck.Relay[id].ID == "") {
				t.Fatal("foreign acknowledgement removed the durable task record")
			}
			staleProfile := profile
			staleProfile.Version++
			if err := saveAccessProfilesState(server.accessProfilesStatePath(), map[string]AccessProfile{profile.ID: staleProfile}); err != nil {
				t.Fatal(err)
			}
			staleAck := call(server, ownerToken, tc.ackPath(id))
			if staleAck.Code != http.StatusNotFound {
				t.Fatalf("stale-profile acknowledgement was not denied: status=%d body=%s", staleAck.Code, staleAck.Body.String())
			}
			stillPresent, err := server.readTaskRecords(id)
			if err != nil {
				t.Fatal(err)
			}
			if (tc.kind == "shell" && stillPresent.Shell[id].ID == "") || (tc.kind == "relay" && stillPresent.Relay[id].ID == "") {
				t.Fatal("stale-profile acknowledgement removed the durable task record")
			}
			if err := saveAccessProfilesState(server.accessProfilesStatePath(), map[string]AccessProfile{profile.ID: profile}); err != nil {
				t.Fatal(err)
			}

			ownerAck := call(server, ownerToken, tc.ackPath(id))
			if ownerAck.Code != http.StatusOK {
				t.Fatalf("owner acknowledgement failed: status=%d body=%s", ownerAck.Code, ownerAck.Body.String())
			}
			if siblingRead := call(sibling, ownerToken, tc.readPath(id)); siblingRead.Code != http.StatusNotFound {
				t.Fatalf("sibling Hub served a receipt after its durable acknowledgement: status=%d body=%s", siblingRead.Code, siblingRead.Body.String())
			}
			siblingCacheHasReceipt := sibling.shellJobs[id] != nil
			if tc.kind == "relay" {
				siblingCacheHasReceipt = sibling.relayJobs[id] != nil
			}
			if siblingCacheHasReceipt {
				t.Fatal("sibling Hub retained an acknowledged terminal receipt in memory")
			}
			if err := server.Close(); err != nil {
				t.Fatal(err)
			}
			reloaded := newHTTPServer()
			defer reloaded.Close()
			if afterAck := call(reloaded, ownerToken, tc.readPath(id)); afterAck.Code != http.StatusNotFound {
				t.Fatalf("acknowledged receipt reappeared after SQLite reload: status=%d body=%s", afterAck.Code, afterAck.Body.String())
			}
			afterOwnerAck, err := reloaded.readTaskRecords(id)
			if err != nil {
				t.Fatal(err)
			}
			if (tc.kind == "shell" && afterOwnerAck.Shell[id].ID != "") || (tc.kind == "relay" && afterOwnerAck.Relay[id].ID != "") {
				t.Fatal("owner acknowledgement left the durable task record present")
			}
		})
	}
}
