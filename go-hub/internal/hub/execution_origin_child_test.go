// LHC:COVERS go-hub/internal/hub/server.go go-hub/internal/hub/access_task_receipts.go go-hub/internal/hub/execution_origin.go
package hub

import (
	"net/http"
	"testing"
	"time"
)

func TestExecutionOriginChildMCPCallPreservesKnownCaller(t *testing.T) {
	for _, route := range []string{"execute-child", "legacy-child-proxy", "native-shell-control", "pty-terminal-spawn"} {
		t.Run(route, func(t *testing.T) {
			s, child := childSchemaFixture(t, t.TempDir())
			childTool := "hello"
			if route == "pty-terminal-spawn" {
				s.mu.Lock()
				parent := s.agents["shell:fixture"]
				parent.Meta["mcp_agents"] = []any{map[string]any{"ref": "PtyMcp", "enabled": true, "transport": "streamable-http"}}
				child = childMCPAgents(*parent)[0]
				s.mu.Unlock()
				childTool = "terminal_spawn"
			}
			r := originTestRequest()
			r = requestWithAuthClaims(r, map[string]any{"client_id": "origin-client", "jti": "origin-token",
				"sub": "origin-subject", "scope": "gptadmin.read gptadmin.exec"})
			profile := AccessProfile{ID: "origin-child-profile", Version: 3, AccessMode: accessModeFull,
				ApprovalMode: approvalModeUnrestricted, AllowedTargets: []string{child.AgentID, "shell:fixture"},
				AllowedTools: []string{"hello", "terminal_spawn", "mcp_call"}}
			r = requestWithAccessProfile(r, profile)
			args := map[string]any{"execution_origin": map[string]any{"client_id": "forged-client"}}
			if route == "pty-terminal-spawn" {
				// Only enqueue the original tool/argument shape; no PTY or command
				// is executed by this caller propagation regression.
				args = map[string]any{"args": []any{"-lc", "printf origin-fixture"}, "cols": 80, "rows": 24, "shell": "/bin/sh", "waitMs": 1}
			}
			if route == "legacy-child-proxy" {
				s.agentToolCall(r, child, childTool, args)
			} else {
				target, tool := child.AgentID, childTool
				if route == "native-shell-control" {
					target, tool = "shell:fixture", "mcp_call"
					args = map[string]any{"ref": "fixture-child", "name": "hello", "arguments": args}
				}
				response, status := s.executeMCPTool(r, target, tool, args, true, time.Millisecond, "")
				if status != http.StatusOK || firstString(response, "job_id") == "" {
					t.Fatalf("child call did not create the real queued job: %d %v", status, response)
				}
			}
			s.mu.Lock()
			var job *shellJob
			if len(s.shellJobs) == 1 {
				for _, candidate := range s.shellJobs {
					job = candidate
				}
			}
			s.mu.Unlock()
			if job == nil || job.ToolName != "mcp_call" {
				t.Fatal("child proxy did not produce its single parent mcp_call job")
			}
			if route == "pty-terminal-spawn" && (job.Arguments["ref"] != "PtyMcp" || job.Arguments["name"] != "terminal_spawn" || len(mapValue(job.Arguments["arguments"])) != 5) {
				t.Fatal("PTY child tool/argument shape was changed before queueing")
			}
			if job.Caller == nil || !sameTaskCaller(job.Caller, taskCallerFromRequest(r)) {
				t.Fatal("known authenticated caller was dropped on the child mcp_call path")
			}
			origin := originTestCallerJSON(t, job.Caller)
			if origin["client_id"] != "origin-client" || origin["profile_version"] != float64(3) || origin["native_session_hint"] != originTestNativeSession {
				t.Fatalf("child proxy did not preserve validated origin: %v", origin)
			}
		})
	}
}
