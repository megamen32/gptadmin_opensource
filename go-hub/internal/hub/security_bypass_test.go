package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func securityBypassServer(t *testing.T) *Server {
	t.Helper()
	s := New(Config{CtlToken: "ctl", RelayAgentToken: "relay", DefaultTimeout: time.Second, PollMaxTimeout: time.Second})
	s.mu.Lock()
	s.agents["shell:allowed"] = &Agent{AgentID: "shell:allowed", Status: "online"}
	s.agents["shell:forbidden"] = &Agent{AgentID: "shell:forbidden", Status: "online"}
	s.agents["mcp:allowed"] = &Agent{AgentID: "mcp:allowed", Status: "online"}
	s.agents["mcp:forbidden"] = &Agent{AgentID: "mcp:forbidden", Status: "online"}
	s.mu.Unlock()
	return s
}

func TestAppsSDKInspectCannotBypassProfileTargetPolicy(t *testing.T) {
	s := securityBypassServer(t)
	request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"inspect_system","arguments":{"target":"shell:forbidden","action":"list_directory","path":"/tmp"}}}`))
	request.Header.Set("Authorization", "Bearer ctl")
	request.Header.Set("Content-Type", "application/json")
	request = requestWithAccessProfile(request, AccessProfile{ID: "restricted", AccessMode: accessModeFull, AllowedTargets: []string{"shell:allowed"}, AllowedTools: []string{"inspect_system"}, Version: 1})
	record := httptest.NewRecorder()
	s.Handler().ServeHTTP(record, request)
	if record.Code != http.StatusOK || !strings.Contains(record.Body.String(), "access profile denies") {
		t.Fatalf("forbidden inspect target bypassed profile: status=%d body=%s", record.Code, record.Body.String())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.shellJobs) != 0 {
		t.Fatalf("forbidden inspect queued shell work: %#v", s.shellJobs)
	}
}

func TestMCPRelaySchemaCannotBypassProfileTargetPolicy(t *testing.T) {
	s := securityBypassServer(t)
	request := httptest.NewRequest(http.MethodPost, "/mcp-relay/tools", bytes.NewBufferString(`{"target":"mcp:forbidden","background":true}`))
	request.Header.Set("Authorization", "Bearer ctl")
	request.Header.Set("Content-Type", "application/json")
	request = requestWithAccessProfile(request, AccessProfile{ID: "restricted", AccessMode: accessModeFull, AllowedTargets: []string{"mcp:allowed"}, AllowedTools: []string{"schema"}, Version: 1})
	record := httptest.NewRecorder()
	s.Handler().ServeHTTP(record, request)
	if record.Code != http.StatusForbidden {
		t.Fatalf("forbidden schema target bypassed profile: status=%d body=%s", record.Code, record.Body.String())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.relayJobs) != 0 {
		t.Fatalf("forbidden schema queued relay work: %#v", s.relayJobs)
	}
}

func TestReadonlyAppsSDKSchemaCannotReachRemoteTargets(t *testing.T) {
	s := securityBypassServer(t)
	request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_mcp_tools","arguments":{"target":"mcp:allowed"}}}`))
	request.Header.Set("Authorization", "Bearer ctl")
	request.Header.Set("Content-Type", "application/json")
	request = requestWithAuthClaims(request, map[string]any{"access_mode": accessModeReadonly, "scope": "gptadmin.read"})
	request = requestWithAccessProfile(request, AccessProfile{ID: "readonly", AccessMode: accessModeReadonly, AllowedTargets: []string{"mcp:allowed"}, AllowedTools: []string{"schema", "list_mcp_tools"}, Version: 1})
	record := httptest.NewRecorder()
	s.Handler().ServeHTTP(record, request)
	if record.Code != http.StatusOK || !strings.Contains(record.Body.String(), `"tools":[]`) {
		t.Fatalf("readonly schema reached remote target: status=%d body=%s", record.Code, record.Body.String())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.relayJobs) != 0 {
		t.Fatalf("readonly schema queued relay work: %#v", s.relayJobs)
	}
}

func TestMCPRelayResultRequiresJobOwnerAndIsSingleAssignment(t *testing.T) {
	s := securityBypassServer(t)
	jobID := s.enqueueRelay("mcp:allowed", "tools/list", nil)

	wrongAgent := httptest.NewRequest(http.MethodPost, "/mcp-relay/result/mcp:forbidden", bytes.NewBufferString(`{"id":"`+jobID+`","result":{"tools":[]}}`))
	wrongAgent.Header.Set("Authorization", "Bearer relay")
	wrongRecord := httptest.NewRecorder()
	s.Handler().ServeHTTP(wrongRecord, wrongAgent)
	if wrongRecord.Code != http.StatusForbidden {
		t.Fatalf("wrong relay agent accepted result: status=%d body=%s", wrongRecord.Code, wrongRecord.Body.String())
	}

	owner := httptest.NewRequest(http.MethodPost, "/mcp-relay/result/mcp:allowed", bytes.NewBufferString(`{"id":"`+jobID+`","result":{"value":"first"}}`))
	owner.Header.Set("Authorization", "Bearer relay")
	ownerRecord := httptest.NewRecorder()
	s.Handler().ServeHTTP(ownerRecord, owner)
	if ownerRecord.Code != http.StatusOK {
		t.Fatalf("owner result failed: status=%d body=%s", ownerRecord.Code, ownerRecord.Body.String())
	}

	duplicate := httptest.NewRequest(http.MethodPost, "/mcp-relay/result/mcp:allowed", bytes.NewBufferString(`{"id":"`+jobID+`","result":{"value":"second"}}`))
	duplicate.Header.Set("Authorization", "Bearer relay")
	duplicateRecord := httptest.NewRecorder()
	s.Handler().ServeHTTP(duplicateRecord, duplicate)
	if duplicateRecord.Code != http.StatusConflict {
		t.Fatalf("duplicate relay result was accepted: status=%d body=%s", duplicateRecord.Code, duplicateRecord.Body.String())
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	encoded, err := json.Marshal(s.relayJobs[jobID].Result)
	if err != nil || !strings.Contains(string(encoded), "first") {
		t.Fatalf("duplicate result overwrote first result: %s err=%v", encoded, err)
	}
}
