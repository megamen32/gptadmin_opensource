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

func mcpV2Request(t *testing.T, s *Server, method, body string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer ctl")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", method)
	var routing struct {
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal([]byte(body), &routing); err == nil {
		if name := mcpRoutingName(method, routing.Params); name != "" {
			req.Header.Set("Mcp-Name", name)
		}
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("MCP-Protocol-Version"); got != "2026-07-28" {
		t.Fatalf("protocol header=%q", got)
	}
	var rpc struct {
		Result map[string]any `json:"result"`
		Error  any            `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rpc); err != nil {
		t.Fatal(err)
	}
	if rpc.Error != nil {
		t.Fatalf("rpc error=%v", rpc.Error)
	}
	return rpc.Result
}

func TestHubMCP20260728StatelessDiscover(t *testing.T) {
	s := New(Config{CtlToken: "ctl", DefaultTimeout: 1, PollMaxTimeout: 1})
	result := mcpV2Request(t, s, "server/discover", `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`)
	versions, ok := result["supportedVersions"].([]any)
	if !ok || len(versions) != 1 || versions[0] != "2026-07-28" {
		t.Fatalf("supportedVersions=%v", result["supportedVersions"])
	}
	if result["resultType"] != "complete" || result["ttlMs"] == nil || result["cacheScope"] != "public" {
		t.Fatalf("discover result=%v", result)
	}
	serverMeta := mapValue(mapValue(result["_meta"])["io.modelcontextprotocol/serverInfo"])
	if firstString(serverMeta, "name") != "gptadmin-go-hub" {
		t.Fatalf("serverInfo meta=%v", result["_meta"])
	}
	caps, _ := result["capabilities"].(map[string]any)
	ext, _ := caps["extensions"].(map[string]any)
	if _, ok := ext["io.modelcontextprotocol/tasks"]; !ok {
		t.Fatalf("tasks extension missing: %v", result)
	}
}

func TestHubMCP20260728TasksLifecycle(t *testing.T) {
	s := New(Config{CtlToken: "ctl", DefaultTimeout: 1, PollMaxTimeout: 1})
	s.mu.Lock()
	s.shellJobs["task-1"] = &shellJob{ID: "task-1", Server: "admin-server-100", CreatedAt: nowFloat(), Status: "queued", Cmd: "sleep 60"}
	s.shellQueues["admin-server-100"] = append(s.shellQueues["admin-server-100"], "task-1")
	s.mu.Unlock()

	get := mcpV2Request(t, s, "tasks/get", `{"jsonrpc":"2.0","id":2,"method":"tasks/get","params":{"taskId":"task-1","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}}}`)
	if get["resultType"] != "complete" || get["status"] != "working" || get["taskId"] != "task-1" {
		t.Fatalf("get=%v", get)
	}
	progress := mapValue(mapValue(get["_meta"])["io.gptadmin/taskProgress"])
	if firstString(progress, "stage") != "working" || firstString(progress, "target") != "shell:admin-server-100" {
		t.Fatalf("task progress=%v", get["_meta"])
	}

	cancel := mcpV2Request(t, s, "tasks/cancel", `{"jsonrpc":"2.0","id":3,"method":"tasks/cancel","params":{"taskId":"task-1","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}}}`)
	if cancel["resultType"] != "complete" {
		t.Fatalf("cancel=%v", cancel)
	}
	get = mcpV2Request(t, s, "tasks/get", `{"jsonrpc":"2.0","id":4,"method":"tasks/get","params":{"taskId":"task-1","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}}}`)
	if get["status"] != "cancelled" {
		t.Fatalf("cancelled get=%v", get)
	}
}

func TestMCPTaskOptInIsExplicit(t *testing.T) {
	if mcpTasksOptedIn(map[string]any{}) {
		t.Fatal("tasks must not be enabled without client opt-in")
	}
	params := map[string]any{"_meta": map[string]any{"io.modelcontextprotocol/clientCapabilities": map[string]any{"extensions": map[string]any{"io.modelcontextprotocol/tasks": map[string]any{}}}}}
	if !mcpTasksOptedIn(params) {
		t.Fatal("tasks opt-in not detected")
	}
}

func TestHubMCP20260728CancelRunningShellTaskQueuesControl(t *testing.T) {
	s := New(Config{CtlToken: "ctl", DefaultTimeout: 1, PollMaxTimeout: 1})
	s.mu.Lock()
	s.shellJobs["running-1"] = &shellJob{ID: "running-1", Server: "host", CreatedAt: nowFloat(), StartedAt: nowFloat(), Status: "running", ToolName: "shell_exec"}
	s.mu.Unlock()
	result, rpcErr := s.mcpTaskCancel("running-1", "")
	if rpcErr != nil || mapValue(result)["resultType"] != "complete" {
		t.Fatalf("cancel result=%v err=%v", result, rpcErr)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shellJobs["running-1"].Status != "cancelled" {
		t.Fatalf("status=%s", s.shellJobs["running-1"].Status)
	}
	controls := s.shellControls["host"]
	if len(controls) != 1 || controls[0].TaskID != "running-1" {
		t.Fatalf("controls=%v", controls)
	}
}

func TestPinnedShellTaskApprovalInputRequiredLifecycle(t *testing.T) {
	s := New(Config{CtlToken: "ctl", DefaultTimeout: 1, PollMaxTimeout: 1})
	profile := AccessProfile{ID: "ask-profile", AccessMode: accessModeFull, ApprovalMode: approvalModeAskBeforeWrite, AllowedTargets: []string{"shell:test"}, AllowedTools: []string{"shell_exec"}, Version: 1}
	req := httptest.NewRequest(http.MethodPost, "/server/shell-test/mcp", nil)
	req.Header.Set("Authorization", "Bearer ctl")
	req = requestWithAccessProfile(req, profile)
	agent := Agent{AgentID: "shell:test", Name: "test", Kind: "shell", Status: "online"}
	body := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name":      "shell_exec",
			"arguments": map[string]any{"cmd": "printf APPROVED"},
			"_meta": map[string]any{
				"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
				"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "test", "version": "1"},
				"io.modelcontextprotocol/clientCapabilities": map[string]any{"extensions": map[string]any{"io.modelcontextprotocol/tasks": map[string]any{}}},
			},
		},
	}
	result, rpcErr, noContent := s.agentMCPJSONRPC(req, agent, body)
	if noContent || rpcErr != nil {
		t.Fatalf("create err=%v noContent=%v", rpcErr, noContent)
	}
	task := mapValue(result)
	if task["resultType"] != "task" || task["status"] != "input_required" {
		t.Fatalf("task=%v", task)
	}
	taskID := firstString(task, "taskId")
	requests := mapValue(task["inputRequests"])
	approvalReq := mapValue(requests["approval"])
	params := mapValue(approvalReq["params"])
	message := firstString(params, "message")
	approvalID := ""
	if i := strings.LastIndex(message, "Approval handle: "); i >= 0 {
		approvalID = strings.TrimSpace(message[i+len("Approval handle: "):])
	}
	if taskID == "" || approvalID == "" {
		t.Fatalf("missing task/approval: %v", task)
	}
	s.mu.Lock()
	if len(s.shellQueues["test"]) != 0 {
		t.Fatalf("task dispatched before approval: %v", s.shellQueues["test"])
	}
	s.mu.Unlock()

	if _, err := s.mcpTaskUpdate(req, taskID, map[string]any{"approval": map[string]any{"action": "accept", "content": map[string]any{"approved": true}}}, agent.AgentID); err == nil {
		t.Fatal("tasks/update accepted pending approval")
	}
	s.mu.Lock()
	approval := s.approvals[approvalID]
	if approval == nil {
		s.mu.Unlock()
		t.Fatal("approval missing")
	}
	approval.Status = "approved"
	s.mu.Unlock()

	updated, err := s.mcpTaskUpdate(req, taskID, map[string]any{"approval": map[string]any{"action": "accept", "content": map[string]any{"approved": true}}}, agent.AgentID)
	if err != nil {
		t.Fatalf("approved update err=%v", err)
	}
	updatedMap := mapValue(updated)
	if updatedMap["resultType"] != "complete" || len(updatedMap) != 1 {
		t.Fatalf("update ack=%v", updatedMap)
	}
	gotTask, getErr := s.mcpTaskGet(taskID, agent.AgentID)
	if getErr != nil || mapValue(gotTask)["status"] != "working" {
		t.Fatalf("post-update task=%v err=%v", gotTask, getErr)
	}
	s.mu.Lock()
	if s.approvals[approvalID].Status != "consumed" {
		t.Fatalf("approval status=%s", s.approvals[approvalID].Status)
	}
	if q := s.shellQueues["test"]; len(q) != 1 || q[0] != taskID {
		t.Fatalf("queue=%v", q)
	}
	s.mu.Unlock()

	if _, err := s.mcpTaskUpdate(req, taskID, map[string]any{"approval": map[string]any{"action": "accept", "content": map[string]any{"approved": true}}}, agent.AgentID); err != nil {
		t.Fatalf("idempotent update err=%v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if q := s.shellQueues["test"]; len(q) != 1 {
		t.Fatalf("duplicate dispatch after repeated update: %v", q)
	}
}

func TestFacadeExecuteTaskUsesNestedApprovalCheckpoint(t *testing.T) {
	s := New(Config{CtlToken: "ctl", DefaultTimeout: 1, PollMaxTimeout: 1})
	profile := AccessProfile{
		ID: "ask-facade", AccessMode: accessModeFull, ApprovalMode: approvalModeAskBeforeWrite,
		AllowedTargets: []string{"shell:test"}, AllowedTools: []string{"execute", "shell_exec"}, Version: 1,
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"execute","arguments":{"target":"shell:test","tool":"shell_exec","args":{"cmd":"printf FACADE_APPROVED"}},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"facade-test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer ctl")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "execute")
	req = requestWithAccessProfile(req, profile)
	rec := httptest.NewRecorder()
	s.mcpEndpoint(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var rpc struct {
		Result map[string]any `json:"result"`
		Error  any            `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rpc); err != nil {
		t.Fatal(err)
	}
	if rpc.Error != nil {
		t.Fatalf("rpc error=%v body=%s", rpc.Error, rec.Body.String())
	}
	if rpc.Result["resultType"] != "task" || rpc.Result["status"] != "input_required" {
		t.Fatalf("result=%v", rpc.Result)
	}
	taskID := firstString(rpc.Result, "taskId")
	requests := mapValue(rpc.Result["inputRequests"])
	approvalReq := mapValue(requests["approval"])
	message := firstString(mapValue(approvalReq["params"]), "message")
	approvalID := ""
	if i := strings.LastIndex(message, "Approval handle: "); i >= 0 {
		approvalID = strings.TrimSpace(message[i+len("Approval handle: "):])
	}
	if taskID == "" || approvalID == "" {
		t.Fatalf("missing task/approval: %v", rpc.Result)
	}
	s.mu.Lock()
	approval := s.approvals[approvalID]
	if approval == nil {
		s.mu.Unlock()
		t.Fatal("approval missing")
	}
	if approval.Target != "shell:test" || approval.Tool != "shell_exec" {
		s.mu.Unlock()
		t.Fatalf("approval routed to wrapper: %#v", approval)
	}
	approval.Status = "approved"
	s.mu.Unlock()
	updated, errAny := s.mcpTaskUpdate(req, taskID, map[string]any{"approval": map[string]any{"action": "accept", "content": map[string]any{"approved": true}}}, "")
	if errAny != nil {
		t.Fatalf("update err=%v", errAny)
	}
	if mapValue(updated)["resultType"] != "complete" || len(mapValue(updated)) != 1 {
		t.Fatalf("update ack=%v", updated)
	}
	post, postErr := s.mcpTaskGet(taskID, "")
	if postErr != nil || mapValue(post)["status"] != "working" {
		t.Fatalf("post-update task=%v err=%v", post, postErr)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if q := s.shellQueues["test"]; len(q) != 1 || q[0] != taskID {
		t.Fatalf("queue=%v", q)
	}
	if s.approvals[approvalID].Status != "consumed" {
		t.Fatalf("approval=%s", s.approvals[approvalID].Status)
	}
}

func TestTaskOutputResourceURIPagination(t *testing.T) {
	uri := taskOutputResourceURI("task/one", "stdout", 1048576, 262144)
	id, stream, offset, limit, ok := parseTaskOutputResourceURI(uri)
	if !ok || id != "task/one" || stream != "stdout" || offset != 1048576 || limit != 262144 {
		t.Fatalf("roundtrip uri=%q id=%q stream=%q offset=%d limit=%d ok=%v", uri, id, stream, offset, limit, ok)
	}
	if _, _, _, _, ok := parseTaskOutputResourceURI("gptadmin://task/x/stdout?offset=-1"); ok {
		t.Fatal("negative offset accepted")
	}
	if _, _, _, _, ok := parseTaskOutputResourceURI("gptadmin://task/x/stdout?limit=1048577"); ok {
		t.Fatal("oversized limit accepted")
	}
}

func TestMCP20260728RoutingHeaderValidation(t *testing.T) {
	s := New(Config{CtlToken: "ctl", DefaultTimeout: 1, PollMaxTimeout: 1})
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"demo","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
	call := func(methodHeader, nameHeader string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer ctl")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("MCP-Protocol-Version", "2026-07-28")
		if methodHeader != "" {
			req.Header.Set("Mcp-Method", methodHeader)
		}
		if nameHeader != "" {
			req.Header.Set("Mcp-Name", nameHeader)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := call("tools/list", "demo"); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":-32020`) {
		t.Fatalf("method mismatch status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := call("tools/call", "wrong"); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":-32020`) {
		t.Fatalf("name mismatch status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := call("tools/call", "demo"); rec.Code != http.StatusOK {
		t.Fatalf("valid headers status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestShellTaskProgressIncludesOutputMetrics(t *testing.T) {
	s := New(Config{})
	now := nowFloat()
	j := &shellJob{ID: "metrics-1", Server: "host", ToolName: "shell_exec", CreatedAt: now - 2, StartedAt: now - 1, DoneAt: now, Status: "completed", Result: map[string]any{"returncode": 0, "stdout_bytes": 5000.0, "stderr_bytes": 7.0, "_spilled": true}}
	out := s.detailedShellTask(j)
	progress := mapValue(mapValue(out["_meta"])["io.gptadmin/taskProgress"])
	stdoutBytes, stdoutOK := int64FromAny(progress["stdoutBytes"])
	stderrBytes, stderrOK := int64FromAny(progress["stderrBytes"])
	if !stdoutOK || stdoutBytes != 5000 || !stderrOK || stderrBytes != 7 || !truthy(progress["spilled"]) || intFromAny(progress["returnCode"]) != 0 {
		t.Fatalf("progress=%v", progress)
	}
}

func mcpV2Raw(t *testing.T, s *Server, method, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer ctl")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", method)
	if name != "" {
		req.Header.Set("Mcp-Name", name)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestMCP20260728TaskMethodsRequireCapability(t *testing.T) {
	s := New(Config{CtlToken: "ctl", DefaultTimeout: 1, PollMaxTimeout: 1})
	body := `{"jsonrpc":"2.0","id":91,"method":"tasks/get","params":{"taskId":"missing","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
	rec := mcpV2Raw(t, s, "tasks/get", "missing", body)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"code":-32003`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMCP20260728UnknownTaskIsInvalidParams(t *testing.T) {
	s := New(Config{CtlToken: "ctl", DefaultTimeout: 1, PollMaxTimeout: 1})
	body := `{"jsonrpc":"2.0","id":92,"method":"tasks/get","params":{"taskId":"does-not-exist","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}}}`
	rec := mcpV2Raw(t, s, "tasks/get", "does-not-exist", body)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"code":-32602`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMCP20260728TaskMcpNameMustEqualTaskID(t *testing.T) {
	s := New(Config{CtlToken: "ctl", DefaultTimeout: 1, PollMaxTimeout: 1})
	body := `{"jsonrpc":"2.0","id":93,"method":"tasks/get","params":{"taskId":"task-abc","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}}}`
	rec := mcpV2Raw(t, s, "tasks/get", "wrong-task", body)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":-32020`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMCP20260728CancelAckIsEmptyComplete(t *testing.T) {
	s := New(Config{CtlToken: "ctl", DefaultTimeout: 1, PollMaxTimeout: 1})
	s.mu.Lock()
	s.shellJobs["cancel-ack"] = &shellJob{ID: "cancel-ack", Server: "host", CreatedAt: nowFloat(), Status: "queued"}
	s.mu.Unlock()
	body := `{"jsonrpc":"2.0","id":94,"method":"tasks/cancel","params":{"taskId":"cancel-ack","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}}}`
	rec := mcpV2Raw(t, s, "tasks/cancel", "cancel-ack", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var rpc struct {
		Result map[string]any `json:"result"`
		Error  any            `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rpc); err != nil {
		t.Fatal(err)
	}
	if rpc.Error != nil || rpc.Result["resultType"] != "complete" {
		t.Fatalf("response=%s", rec.Body.String())
	}
	for key := range rpc.Result {
		if key != "resultType" && key != "_meta" {
			t.Fatalf("non-empty cancel acknowledgement key=%q response=%s", key, rec.Body.String())
		}
	}
}

func TestExpireOrphanedShellJobsRespectsRequestedTimeout(t *testing.T) {
	s := New(Config{})
	now := nowFloat()
	s.mu.Lock()
	s.shellJobs["orphan"] = &shellJob{ID: "orphan", Server: "host", Status: "running", StartedAt: now - 1000, Timeout: 0}
	s.shellJobs["fresh"] = &shellJob{ID: "fresh", Server: "host", Status: "running", StartedAt: now - 100, Timeout: 0}
	s.shellJobs["long"] = &shellJob{ID: "long", Server: "host", Status: "running", StartedAt: now - 1000, Timeout: 7200}
	expired := s.expireOrphanedShellJobsLocked()
	if len(expired) != 1 || expired[0] != "orphan" {
		s.mu.Unlock()
		t.Fatalf("expired=%v", expired)
	}
	if s.shellJobs["orphan"].Status != "failed" || firstString(mapValue(s.shellJobs["orphan"].Error), "code") != "orphaned_running_task" {
		s.mu.Unlock()
		t.Fatalf("orphan=%+v", s.shellJobs["orphan"])
	}
	if s.shellJobs["fresh"].Status != "running" || s.shellJobs["long"].Status != "running" {
		s.mu.Unlock()
		t.Fatalf("fresh/long changed: fresh=%+v long=%+v", s.shellJobs["fresh"], s.shellJobs["long"])
	}
	controls := s.shellControls["host"]
	s.mu.Unlock()
	if len(controls) != 1 || controls[0].TaskID != "orphan" {
		t.Fatalf("controls=%v", controls)
	}
}

func TestTaskGroupAggregatesChildrenAndCascadeCancel(t *testing.T) {
	s := New(Config{})
	now := nowFloat()
	s.mu.Lock()
	s.shellJobs["c1"] = &shellJob{ID: "c1", Server: "one", ToolName: "shell_exec", CreatedAt: now - 2, StartedAt: now - 1, Status: "running", ParentTaskID: "group-1"}
	s.shellJobs["c2"] = &shellJob{ID: "c2", Server: "two", ToolName: "shell_exec", CreatedAt: now - 2, DoneAt: now - 1, Status: "completed", ParentTaskID: "group-1"}
	s.relayJobs["group-1"] = &relayJob{ID: "group-1", AgentID: "hub", Method: "task/group", CreatedAt: now - 3, Status: "running", Params: map[string]any{"label": "test", "children": []any{"c1", "c2"}}}
	s.mu.Unlock()

	got, rpcErr := s.mcpTaskGet("group-1", "hub")
	if rpcErr != nil {
		t.Fatalf("get err=%v", rpcErr)
	}
	group := mapValue(got)
	if group["status"] != "working" {
		t.Fatalf("group=%v", group)
	}
	children, _ := group["children"].([]map[string]any)
	if len(children) != 2 {
		t.Fatalf("children=%v", group["children"])
	}
	progress := mapValue(mapValue(group["_meta"])["io.gptadmin/taskProgress"])
	counts := mapValue(progress["counts"])
	if intFromAny(counts["working"]) != 1 || intFromAny(counts["completed"]) != 1 {
		t.Fatalf("counts=%v", counts)
	}

	if _, rpcErr = s.mcpTaskCancel("group-1", "hub"); rpcErr != nil {
		t.Fatalf("cancel err=%v", rpcErr)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shellJobs["c1"].Status != "cancelled" {
		t.Fatalf("c1 status=%s", s.shellJobs["c1"].Status)
	}
	if s.shellJobs["c2"].Status != "completed" {
		t.Fatalf("completed child changed=%s", s.shellJobs["c2"].Status)
	}
	if s.relayJobs["group-1"].Status != "cancelled" {
		t.Fatalf("group status=%s", s.relayJobs["group-1"].Status)
	}
	if controls := s.shellControls["one"]; len(controls) != 1 || controls[0].TaskID != "c1" {
		t.Fatalf("controls=%v", controls)
	}
}

func TestTaskGroupCompletesWhenAllChildrenComplete(t *testing.T) {
	s := New(Config{})
	now := nowFloat()
	s.mu.Lock()
	s.shellJobs["a"] = &shellJob{ID: "a", Server: "one", CreatedAt: now - 3, DoneAt: now - 1, Status: "completed", ParentTaskID: "g"}
	s.shellJobs["b"] = &shellJob{ID: "b", Server: "two", CreatedAt: now - 3, DoneAt: now, Status: "completed", ParentTaskID: "g"}
	s.relayJobs["g"] = &relayJob{ID: "g", AgentID: "hub", Method: "task/group", CreatedAt: now - 4, Status: "running", Params: map[string]any{"children": []any{"a", "b"}}}
	s.mu.Unlock()
	got, err := s.mcpTaskGet("g", "hub")
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if mapValue(got)["status"] != "completed" {
		t.Fatalf("group=%v", got)
	}
}

func TestBulkExecAcceptsPerServerApprovalIDsWithoutBypass(t *testing.T) {
	s := New(Config{CtlToken: "ctl"})
	s.mu.Lock()
	s.agents["shell:one"] = &Agent{AgentID: "shell:one", Name: "one", Kind: "virtual_shell", Status: "online"}
	s.agents["shell:two"] = &Agent{AgentID: "shell:two", Name: "two", Kind: "virtual_shell", Status: "online"}
	s.mu.Unlock()
	req := httptest.NewRequest(http.MethodPost, "/bulk/exec", bytes.NewBufferString(`{"servers":["one","two"],"cmd":"printf ok","approval_ids":{"one":"invalid-one","two":"invalid-two"}}`))
	req.Header.Set("Authorization", "Bearer ctl")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code < 400 {
		t.Fatalf("invalid per-target approvals bypassed policy: status=%d body=%s", rec.Code, rec.Body.String())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.shellJobs) != 0 || len(s.relayJobs) != 0 {
		t.Fatalf("invalid approvals queued work: shell=%d relay=%d", len(s.shellJobs), len(s.relayJobs))
	}
}

func TestRefreshTaskGroupsLockedUpdatesParentWithoutRead(t *testing.T) {
	s := New(Config{})
	now := nowFloat()
	s.mu.Lock()
	s.shellJobs["x"] = &shellJob{ID: "x", Server: "one", CreatedAt: now - 2, DoneAt: now - 1, Status: "completed", ParentTaskID: "g"}
	s.shellJobs["y"] = &shellJob{ID: "y", Server: "two", CreatedAt: now - 2, DoneAt: now, Status: "completed", ParentTaskID: "g"}
	s.relayJobs["g"] = &relayJob{ID: "g", AgentID: "hub", Method: "task/group", CreatedAt: now - 3, Status: "running", Params: map[string]any{"children": []any{"x", "y"}}}
	changed := s.refreshTaskGroupsLocked()
	group := s.relayJobs["g"]
	s.mu.Unlock()
	if changed != 1 || group.Status != "completed" || group.DoneAt <= 0 {
		t.Fatalf("changed=%d group=%+v", changed, group)
	}
}

func TestFleetExecToolCreatesInputRequiredParentWithoutWrapperApproval(t *testing.T) {
	s := New(Config{CtlToken: "ctl"})
	s.mu.Lock()
	s.agents["shell:one"] = &Agent{AgentID: "shell:one", Name: "one", Kind: "virtual_shell", Status: "online"}
	s.agents["shell:two"] = &Agent{AgentID: "shell:two", Name: "two", Kind: "virtual_shell", Status: "online"}
	s.mu.Unlock()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"fleetExec","arguments":{"servers":["one","two"],"cmd":"printf ok"},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer ctl")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "fleetExec")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var rpc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rpc); err != nil {
		t.Fatal(err)
	}
	if rpc["error"] != nil {
		t.Fatalf("rpc error=%v", rpc["error"])
	}
	result := mapValue(rpc["result"])
	parentID := firstString(result, "taskId")
	if result["resultType"] != "task" || result["status"] != "input_required" || parentID == "" {
		t.Fatalf("result=%v", result)
	}
	if len(mapValue(result["inputRequests"])) == 0 {
		t.Fatalf("parent missing inputRequests: %v", result)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, approval := range s.approvals {
		if approval.Target == "hub" && (approval.Tool == "fleetExec" || approval.Tool == "fleet_exec") {
			t.Fatalf("wrapper approval was created: %+v", approval)
		}
	}
	parent := s.relayJobs[parentID]
	if parent == nil || parent.Method != "task/group" || parent.Status != "input_required" {
		t.Fatalf("parent=%+v", parent)
	}
	children := taskGroupChildIDs(parent)
	if len(children) != 2 || len(s.shellJobs) != 2 {
		t.Fatalf("children=%v shellJobs=%d", children, len(s.shellJobs))
	}
	for _, childID := range children {
		child := s.shellJobs[childID]
		if child == nil || child.Status != "input_required" || child.ParentTaskID != parentID || child.ApprovalID == "" {
			t.Fatalf("child=%+v", child)
		}
		if q := s.shellQueues[child.Server]; len(q) != 0 {
			t.Fatalf("child dispatched before approval: server=%s queue=%v", child.Server, q)
		}
	}
}

func TestFleetExecParentUpdatePreflightsAllApprovalsAtomically(t *testing.T) {
	s := New(Config{CtlToken: "ctl"})
	s.mu.Lock()
	s.agents["shell:one"] = &Agent{AgentID: "shell:one", Name: "one", Kind: "virtual_shell", Status: "online"}
	s.agents["shell:two"] = &Agent{AgentID: "shell:two", Name: "two", Kind: "virtual_shell", Status: "online"}
	s.mu.Unlock()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer ctl")
	response, status := s.fleetExecForRequest(req, map[string]any{"servers": []any{"one", "two"}, "cmd": "printf ok"}, true)
	if status != http.StatusOK {
		t.Fatalf("status=%d response=%v", status, response)
	}
	parentID := firstString(response, "task_id")
	if parentID == "" || firstString(response, "status") != "input_required" {
		t.Fatalf("response=%v", response)
	}
	input := map[string]any{"approval": map[string]any{"action": "accept", "content": map[string]any{"approved": true}}}

	if _, rpcErr := s.mcpTaskUpdate(req, parentID, input, "hub"); rpcErr == nil {
		t.Fatal("parent update succeeded before all admin approvals")
	}
	s.mu.Lock()
	parent := s.relayJobs[parentID]
	children := taskGroupChildIDs(parent)
	for _, childID := range children {
		child := s.shellJobs[childID]
		if child.Status != "input_required" || len(s.shellQueues[child.Server]) != 0 {
			s.mu.Unlock()
			t.Fatalf("partial dispatch before approval: child=%+v queue=%v", child, s.shellQueues[child.Server])
		}
	}
	for _, childID := range children {
		child := s.shellJobs[childID]
		approval := s.approvals[child.ApprovalID]
		if approval == nil {
			s.mu.Unlock()
			t.Fatalf("missing approval for %s", childID)
		}
		approval.Status = "approved"
	}
	s.mu.Unlock()

	updated, rpcErr := s.mcpTaskUpdate(req, parentID, input, "hub")
	if rpcErr != nil {
		t.Fatalf("approved parent update err=%v", rpcErr)
	}
	updatedMap := mapValue(updated)
	if updatedMap["status"] != "working" {
		t.Fatalf("updated=%v", updatedMap)
	}
	s.mu.Lock()
	for _, childID := range children {
		child := s.shellJobs[childID]
		if child.Status != "queued" {
			s.mu.Unlock()
			t.Fatalf("child not queued: %+v", child)
		}
		if q := s.shellQueues[child.Server]; len(q) != 1 || q[0] != childID {
			s.mu.Unlock()
			t.Fatalf("queue=%v child=%s", q, childID)
		}
	}
	s.mu.Unlock()

	if _, rpcErr := s.mcpTaskUpdate(req, parentID, input, "hub"); rpcErr != nil {
		t.Fatalf("repeated parent update err=%v", rpcErr)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, childID := range children {
		child := s.shellJobs[childID]
		if q := s.shellQueues[child.Server]; len(q) != 1 {
			t.Fatalf("repeated update duplicated queue: %v", q)
		}
	}
}

func TestAppsSDKToolsExposeFleetExec(t *testing.T) {
	found := false
	for _, tool := range appsSDKTools() {
		if firstString(tool, "name") == "fleetExec" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("fleetExec missing from MCP Apps tool list")
	}
}

func TestFleetExecIdempotencyReturnsSameParentWithoutDuplicateApprovals(t *testing.T) {
	dir := t.TempDir()
	s := New(Config{CtlToken: "ctl", ConfigDir: dir, DefaultTimeout: time.Second})
	s.mu.Lock()
	s.agents["shell:one"] = &Agent{AgentID: "shell:one", Name: "one", Kind: "virtual_shell", Status: "online"}
	s.agents["shell:two"] = &Agent{AgentID: "shell:two", Name: "two", Kind: "virtual_shell", Status: "online"}
	s.mu.Unlock()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer ctl")
	args := map[string]any{"servers": []any{"one", "two"}, "cmd": "printf idem", "idempotency_key": "fleet-idem-1"}
	first, status := s.fleetExecForRequest(req, args, true)
	if status != http.StatusOK || firstString(first, "task_id") == "" {
		t.Fatalf("first status=%d result=%v", status, first)
	}
	second, status := s.fleetExecForRequest(req, args, true)
	if status != http.StatusOK || firstString(second, "task_id") != firstString(first, "task_id") {
		t.Fatalf("second status=%d first=%v second=%v", status, first, second)
	}
	s.mu.Lock()
	if len(s.shellJobs) != 2 || len(s.relayJobs) != 1 || len(s.approvals) != 2 {
		s.mu.Unlock()
		t.Fatalf("duplicates created: shell=%d relay=%d approvals=%d", len(s.shellJobs), len(s.relayJobs), len(s.approvals))
	}
	s.mu.Unlock()

	conflictArgs := map[string]any{"servers": []any{"one", "two"}, "cmd": "printf DIFFERENT", "idempotency_key": "fleet-idem-1"}
	if _, status := s.fleetExecForRequest(req, conflictArgs, true); status != http.StatusConflict {
		t.Fatalf("different operation reused key, status=%d", status)
	}
}

func TestFleetExecIdempotencySurvivesHubRestart(t *testing.T) {
	dir := t.TempDir()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer ctl")
	args := map[string]any{"servers": []any{"one", "two"}, "cmd": "printf durable", "idempotency_key": "fleet-durable-1"}
	s1 := New(Config{CtlToken: "ctl", ConfigDir: dir, DefaultTimeout: time.Second})
	s1.mu.Lock()
	s1.agents["shell:one"] = &Agent{AgentID: "shell:one", Status: "online"}
	s1.agents["shell:two"] = &Agent{AgentID: "shell:two", Status: "online"}
	s1.mu.Unlock()
	first, status := s1.fleetExecForRequest(req, args, true)
	if status != http.StatusOK {
		t.Fatalf("first status=%d result=%v", status, first)
	}
	parent := firstString(first, "task_id")
	if parent == "" {
		t.Fatalf("missing parent: %v", first)
	}

	s2 := New(Config{CtlToken: "ctl", ConfigDir: dir, DefaultTimeout: time.Second})
	replayed, status := s2.fleetExecForRequest(req, args, true)
	if status != http.StatusOK || firstString(replayed, "task_id") != parent {
		t.Fatalf("restart replay status=%d parent=%s result=%v", status, parent, replayed)
	}
	s2.mu.Lock()
	defer s2.mu.Unlock()
	if len(s2.shellJobs) != 2 || len(s2.relayJobs) != 1 {
		t.Fatalf("restart created duplicates: shell=%d relay=%d", len(s2.shellJobs), len(s2.relayJobs))
	}
}
