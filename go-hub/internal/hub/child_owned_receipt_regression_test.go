// LHC:COVERS go-hub/internal/hub/server.go go-hub/internal/hub/access_task_receipts.go
package hub

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestChildOnlyProfileCanReadItsOwnReceipt(t *testing.T) {
	s, child := childSchemaFixture(t, t.TempDir())
	profile := AccessProfile{ID: "child-only", Version: 3, AccessMode: accessModeFull, ApprovalMode: approvalModeUnrestricted, AllowedTargets: []string{child.AgentID}, AllowedTools: []string{"hello"}}
	if err := saveAccessProfilesState(s.accessProfilesStatePath(), map[string]AccessProfile{profile.ID: profile}); err != nil {
		t.Fatal(err)
	}
	r := requestWithAuthClaims(httptest.NewRequest(http.MethodPost, "/mcp", nil), map[string]any{"client_id": "client-a", "jti": "token-a", "sub": "client-a", "scope": "gptadmin.read gptadmin.exec"})
	r = requestWithAccessProfile(r, profile)
	raw, status := s.executeMCPTool(r, child.AgentID, "hello", map[string]any{}, true, time.Second, "")
	id := firstString(raw, "job_id")
	if status != http.StatusOK || id == "" {
		t.Fatalf("dispatch %d %v", status, raw)
	}
	s.mu.Lock()
	j := s.shellJobs[id]
	if j == nil || !sameTaskCaller(j.Caller, taskCallerFromRequest(r)) {
		s.mu.Unlock()
		t.Fatal("caller lost")
	}
	s.mu.Unlock()
	if _, ok := s.taskReceiptForRequest(r, id, nil); !ok {
		t.Fatalf("same client cannot read own child receipt: target=%s", j.ReceiptTarget)
	}
	foreign := requestWithAuthClaims(httptest.NewRequest(http.MethodPost, "/mcp", nil), map[string]any{"client_id": "client-a", "jti": "other", "sub": "client-a"})
	foreign = requestWithAccessProfile(foreign, profile)
	if _, ok := s.taskReceiptForRequest(foreign, id, nil); ok {
		t.Fatal("foreign token got receipt")
	}
	foreignClient := requestWithAuthClaims(httptest.NewRequest(http.MethodPost, "/mcp", nil), map[string]any{"client_id": "client-b", "jti": "token-a", "sub": "client-a"})
	foreignClient = requestWithAccessProfile(foreignClient, profile)
	if _, rpcErr := s.mcpTaskGetForRequest(foreignClient, id, ""); rpcErr == nil {
		t.Fatal("foreign client got task")
	}
	if _, ok := s.taskReceiptForRequest(foreignClient, id, nil); ok {
		t.Fatal("foreign client got receipt")
	}
	stale := profile
	stale.Version++
	if err := saveAccessProfilesState(s.accessProfilesStatePath(), map[string]AccessProfile{profile.ID: stale}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.taskReceiptForRequest(r, id, nil); ok {
		t.Fatal("stale profile got receipt")
	}
	if _, rpcErr := s.mcpTaskGetForRequest(r, id, ""); rpcErr == nil {
		t.Fatal("stale profile got task")
	}
	if err := saveAccessProfilesState(s.accessProfilesStatePath(), map[string]AccessProfile{profile.ID: profile}); err != nil {
		t.Fatal(err)
	}
	if _, rpcErr := s.mcpTaskGetForRequest(r, id, ""); rpcErr != nil {
		t.Fatalf("tasks/get: %v", rpcErr)
	}
	if _, rpcErr := s.mcpTaskGetForRequest(foreign, id, ""); rpcErr == nil {
		t.Fatal("foreign token got task")
	}
	w := httptest.NewRecorder()
	if s.routeNodePeerJob(w, r, map[string]any{"id": id, "owner_target": "shell:fixture"}, []byte(fmt.Sprintf("{\"id\":%q}", id))) {
		t.Fatalf("owner hint refused: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	if !s.routeNodePeerJob(w, foreign, map[string]any{"id": id, "owner_target": "shell:fixture"}, []byte("{}")) || w.Code != http.StatusForbidden {
		t.Fatalf("foreign owner hint allowed: %d", w.Code)
	}
}

func TestChildOnlyProfileCannotRouteUnknownReceiptToPeer(t *testing.T) {
	var calls atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(http.StatusOK) }))
	defer peer.Close()
	s := peerTestHub(t, "fixture", map[string]string{"shell:peer": peer.URL})
	profile := AccessProfile{ID: "child-only", Version: 1, AccessMode: accessModeFull, AllowedTargets: []string{"mcp:shell:peer:child"}, AllowedTools: []string{"job"}}
	r := taskReceiptTestRequest("POST", "/mcp", "caller", "token", profile)
	w := httptest.NewRecorder()
	if !s.routeNodePeerJob(w, r, map[string]any{"id": "unknown", "owner_target": "shell:peer"}, []byte("{}")) || w.Code != http.StatusForbidden || calls.Load() != 0 {
		t.Fatalf("unknown child-only receipt reached peer: status=%d calls=%d", w.Code, calls.Load())
	}
}

func TestChildOnlyProfileCodeModeStart(t *testing.T) {
	s, child := childSchemaFixture(t, t.TempDir())
	profile := AccessProfile{ID: "child-only", Version: 3, AccessMode: accessModeFull, ApprovalMode: approvalModeUnrestricted, AllowedTargets: []string{"hub", child.AgentID}, AllowedTools: []string{"exec", "hello"}}
	if err := saveAccessProfilesState(s.accessProfilesStatePath(), map[string]AccessProfile{profile.ID: profile}); err != nil {
		t.Fatal(err)
	}
	r := requestWithAuthClaims(httptest.NewRequest(http.MethodPost, "/mcp", nil), map[string]any{"client_id": "client-a", "jti": "token-a", "sub": "client-a", "scope": "gptadmin.read gptadmin.exec"})
	r = requestWithAccessProfile(r, profile)
	out, status := codeModeRun(t, s, r, map[string]any{"code": fmt.Sprintf("const t=await mcp.tasks.start(%q,'hello',{}); return mcp.tasks.get(t.taskId);", child.AgentID)})
	if status != http.StatusOK || firstString(out, "status") != "completed" {
		t.Fatalf("start: %d %v", status, out)
	}
	id := firstString(mapValue(out["result"]), "taskId")
	if id == "" {
		t.Fatalf("missing task: %v", out)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.shellJobs) != 1 || s.shellJobs[id] == nil || s.shellJobs[id].ReceiptTarget != child.AgentID || !sameTaskCaller(s.shellJobs[id].Caller, taskCallerFromRequest(r)) {
		t.Fatal("start did not persist one child-owned task")
	}
}
