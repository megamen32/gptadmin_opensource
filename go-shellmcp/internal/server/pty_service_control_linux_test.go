//go:build linux

// LHC:COVERS go-shellmcp/internal/server/mcp.go go-shellmcp/internal/server/pty_service_control.go
package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPTYServiceControlBeforeChildCall(t *testing.T) {
	cgroup, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	unit := ""
	for _, line := range strings.Split(string(cgroup), "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) != 3 || (fields[1] != "" && fields[1] != "name=systemd") {
			continue
		}
		for _, part := range strings.Split(fields[2], "/") {
			// The first service ancestor is system-managed, including
			// user@UID.service. Descendants may use the user manager.
			if unit == "" && strings.HasSuffix(part, ".service") {
				unit = part
			}
		}
	}
	hasOwnUnit := unit != ""
	if !hasOwnUnit {
		unit = "fixture-self.service" // input only; never executed by the sentinel
	}
	tests := []struct {
		name, tool string
		arguments  map[string]any
		deny       bool
	}{
		{"spawn-argv", "terminal_spawn", map[string]any{"shell": "/usr/bin/systemctl", "args": []any{"stop", unit}}, true},
		{"spawn-bash-lc", "terminal_spawn", map[string]any{"shell": "/bin/bash", "args": []any{"-lc", "sudo systemctl stop " + unit + ">/dev/null"}}, true},
		{"spawn-sh-c", "terminal_spawn", map[string]any{"shell": "/bin/sh", "args": []any{"-c", "systemctl restart " + unit}}, true},
		{"write-command", "terminal_write", map[string]any{"sessionId": "fixture", "data": "sudo systemctl stop " + unit + "\r"}, false},
		{"write-cr-list", "terminal_write", map[string]any{"sessionId": "fixture", "data": "pwd\rsystemctl stop " + unit + "\r"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.deny && !hasOwnUnit {
				t.Skip("test process is not in a systemd service; pure fixtures cover this host")
			}
			dir := t.TempDir()
			marker := filepath.Join(dir, "tools-call")
			child := filepath.Join(dir, "pty-mcp")
			// A protocol sentinel, not a PTY emulator: never interpret tool args.
			body := "#!/bin/sh\nwhile IFS= read -r line; do\ncase \"$line\" in\n" +
				"*'\"method\":\"initialize\"'*) printf '%s\\n' '{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":\"2025-03-26\",\"capabilities\":{}}}' ;;\n" +
				"*'\"method\":\"tools/call\"'*) if grep -q child_invocation_intent \"$PTY_GUARD_AUDIT\"; then printf called >> \"$PTY_GUARD_MARKER\"; else printf missing-audit >> \"$PTY_GUARD_MARKER\"; fi; printf '%s\\n' '{\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[]}}' ;;\n" +
				"esac\ndone\n"
			if err := os.WriteFile(child, []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			s := New(Config{MCPConfig: filepath.Join(dir, "mcp.json"), IdentityDir: filepath.Join(dir, "identity"),
				SpillDir: filepath.Join(dir, "spill"), OutboxDir: filepath.Join(dir, "outbox"), ExecTimeout: 2})
			defer s.Close()
			if _, err := s.mcpManage(map[string]any{"action": "upsert", "config": map[string]any{
				"ref": "PtyMcp", "name": "pty-mcp", "transport": "stdio", "command": child, "enabled": true,
				"env": map[string]any{"PTY_GUARD_MARKER": marker, "PTY_GUARD_AUDIT": s.cfg.AuditLog},
			}}); err != nil {
				t.Fatal(err)
			}
			result, callErr := s.mcpChildCall(context.Background(), map[string]any{
				"ref": "PtyMcp", "name": tt.tool, "arguments": tt.arguments,
			})
			if tt.deny {
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Errorf("self-stop reached the child tools/call sentinel: marker err=%v", err)
				}
				if callErr == nil || !strings.Contains(fmt.Sprint(callErr), "собствен") || !strings.Contains(fmt.Sprint(callErr), unit) {
					t.Fatalf("self-stop must be refused before child call: result=%v err=%v", result, callErr)
				}
			} else {
				if callErr != nil {
					t.Fatalf("terminal input must reach child forwarding: %v", callErr)
				}
				observed, err := os.ReadFile(marker)
				if err != nil || string(observed) != "called" {
					t.Fatalf("terminal input did not reach child after durable audit: marker=%q err=%v", observed, err)
				}
			}
		})
	}
}
