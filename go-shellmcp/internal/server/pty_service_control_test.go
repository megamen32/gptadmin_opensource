// LHC:COVERS go-shellmcp/internal/server/pty_service_control.go go-shellmcp/internal/shell/self_service_control_export.go
package server

import (
	"testing"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/shell"
	"github.com/megamen32/gptadmin/go-shellmcp/internal/supervisor"
)

func TestPTYServiceControlContract(t *testing.T) {
	local := supervisor.Agent{Ref: "PtyMcp", Name: "pty-mcp", Transport: "stdio", Command: "/home/admin/.npm-global/bin/pty-mcp"}
	remote := local
	remote.Transport, remote.URL = "streamable-http", "https://remote.invalid/mcp"
	fileChild := local
	fileChild.Ref, fileChild.Name, fileChild.Command = "Files", "file-mcp", "/bin/file-mcp"
	otherName := local
	otherName.Name = "unrelated"
	otherCommand := local
	otherCommand.Command = "/bin/file-mcp"
	otherRef := local
	otherRef.Ref = "unrelated"
	const own = "repair-agent.service"
	spawn := func(executable string, args ...string) map[string]any {
		return map[string]any{"shell": executable, "args": args}
	}
	write := func(data string) map[string]any {
		return map[string]any{"sessionId": "fixture", "data": data}
	}
	tests := []struct {
		name      string
		agent     supervisor.Agent
		tool      string
		arguments map[string]any
		deny      bool
		checked   bool
	}{
		{"direct-argv", local, "terminal_spawn", spawn("/usr/bin/systemctl", "stop", own), true, true},
		{"sudo-argv", local, "terminal_spawn", spawn("/usr/bin/sudo", "-n", "/usr/bin/systemctl", "kill", own), true, true},
		{"bash-c", local, "terminal_spawn", spawn("/bin/bash", "-c", "systemctl stop "+own), true, true},
		{"bash-lc", local, "terminal_spawn", spawn("bash", "-lc", "sudo systemctl stop "+own+">/dev/null"), true, true},
		{"sh-c", local, "terminal_spawn", spawn("/bin/sh", "-c", "systemctl try-restart "+own), true, true},
		{"json-array", local, "terminal_spawn", map[string]any{"shell": "sh", "args": []any{"-c", "systemctl restart " + own}}, true, true},
		{"write-full", local, "terminal_write", write("sudo systemctl stop " + own + "\n"), false, false},
		{"write-crlf", local, "terminal_write", write("pwd\r\nsystemctl stop " + own + "\r\n"), false, false},
		{"write-cr", local, "terminal_write", write("pwd\rsystemctl stop " + own + "\r"), false, false},
		{"foreign-unit", local, "terminal_spawn", spawn("sh", "-c", "systemctl stop other.service"), false, true},
		{"write-foreign-unit", local, "terminal_write", write("systemctl stop other.service\n"), false, false},
		{"readonly", local, "terminal_spawn", spawn("systemctl", "status", own), false, true},
		{"detached", local, "terminal_spawn", spawn("bash", "-lc", "sudo systemd-run --collect --unit=repair /bin/bash -c 'systemctl stop "+own+"'"), false, true},
		{"remote-http", remote, "terminal_spawn", spawn("systemctl", "stop", own), false, false},
		{"other-child", fileChild, "terminal_write", write("systemctl stop " + own + "\n"), false, false},
		{"other-name", otherName, "terminal_write", write("systemctl stop " + own + "\n"), false, false},
		{"other-command", otherCommand, "terminal_write", write("systemctl stop " + own + "\n"), false, false},
		{"other-ref", otherRef, "terminal_write", write("systemctl stop " + own + "\n"), false, false},
		{"file-tool-data", local, "file_write", write("systemctl stop " + own + "\n"), false, false},
		{"read-tool-data", local, "terminal_read", write("systemctl stop " + own + "\n"), false, false},
		{"nested-script", local, "terminal_spawn", spawn("bash", "/absolute/maintenance.sh"), false, true},
		{"partial-write-prefix", local, "terminal_write", write("systemctl sto"), false, false},
		{"partial-write-suffix", local, "terminal_write", write("p " + own + "\n"), false, false},
		{"missing-shell", local, "terminal_spawn", map[string]any{"args": []any{"-c", "systemctl stop " + own}}, false, false},
		{"malformed-args", local, "terminal_spawn", map[string]any{"shell": "bash", "args": []any{123}}, false, false},
		{"non-string-data", local, "terminal_write", map[string]any{"data": 123}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checked := false
			err := validatePTYServiceControl(tt.agent, tt.tool, tt.arguments, func(req shell.Request) error {
				checked = true
				return shell.ValidateSelfServiceControl("0::/system.slice/"+own+"/mcp/pty\n", req)
			})
			if (err != nil) != tt.deny || checked != tt.checked {
				t.Fatalf("deny=%v want=%v checked=%v want=%v error=%v", err != nil, tt.deny, checked, tt.checked, err)
			}
		})
	}
}

func TestPTYServiceControlWriteIsInputOnly(t *testing.T) {
	agent := supervisor.Agent{Ref: "PtyMcp", Name: "pty-mcp", Transport: "stdio", Command: "/usr/local/bin/pty-mcp"}
	const data = "sudo systemctl stop repair-agent.service\r"
	arguments := map[string]any{"sessionId": "fixture-remote-or-editor", "data": data}
	checked := false
	err := validatePTYServiceControl(agent, "terminal_write", arguments, func(req shell.Request) error {
		checked = true
		return shell.ValidateSelfServiceControl("0::/system.slice/repair-agent.service\n", req)
	})
	if err != nil || checked {
		t.Fatalf("terminal input must bypass the local command guard: checked=%v err=%v", checked, err)
	}
	if arguments["data"] != data {
		t.Fatal("terminal input was changed")
	}
}
