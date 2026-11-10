// LHC:COVERS go-shellmcp/internal/shell/self_service_control.go
package shell

import (
	"strings"
	"testing"
)

var selfServiceControlRedirectionCases = []struct {
	name, command string
	deny          bool
}{
	{"attached-output", "systemctl stop OWN>/dev/null", true},
	{"stderr-output", "systemctl stop OWN 2>/dev/null", true},
	{"before-executable", ">/dev/null systemctl stop OWN", true},
	{"before-action", "systemctl >/dev/null stop OWN", true},
	{"between-action-and-unit", "systemctl stop >/dev/null OWN", true},
	{"fd-before-action", "systemctl 2>/dev/null stop OWN", true},
	{"quoted-target", "systemctl stop 'OWN'>/dev/null", true},
	{"append-output", "systemctl stop OWN>>/dev/null", true},
	{"duplicate-fd", "systemctl 2>&1 stop OWN>/dev/null", true},
	{"combined-output", "systemctl stop OWN&>/dev/null", true},
	{"list", "systemctl stop OWN>/dev/null; systemctl start OWN", true},
	{"foreign-unit-own-output-file", "systemctl stop neighbor.service >OWN", false},
	{"foreign-unit-own-input-file", "systemctl stop neighbor.service <OWN", false},
	{"quoted-greater-in-target", "systemctl stop 'OWN>/dev/null'", false},
	{"quoted-greater-argument", "systemctl stop neighbor.service '>' OWN", true},
	{"escaped-greater-in-target", "systemctl stop OWN\\>/dev/null", false},
	{"readonly-own-output-file", "systemctl status neighbor.service >OWN", false},
}

func TestSelfServiceControlRedirections(t *testing.T) {
	const cgroup = "0::/system.slice/repair-agent.service/subgroup\n"
	for _, tt := range selfServiceControlRedirectionCases {
		t.Run(tt.name, func(t *testing.T) {
			command := strings.ReplaceAll(tt.command, "OWN", "repair-agent.service")
			err := selfServiceControlError(cgroup, Request{Cmd: command})
			if (err != nil) != tt.deny {
				t.Fatalf("command=%q deny=%v want=%v error=%v", command, err != nil, tt.deny, err)
			}
		})
	}
	// Native argv has no shell redirections.
	if err := selfServiceControlError(cgroup, Request{Argv: []string{"systemctl", "stop", "repair-agent.service>/dev/null"}}); err != nil {
		t.Fatalf("literal argv argument was treated as a redirection: %v", err)
	}
}

func TestSelfServiceControlLiteralCommands(t *testing.T) {
	const cgroup = "0::/system.slice/repair-agent.service/executors/job-1\n"
	tests := []struct {
		name string
		req  Request
		deny bool
	}{
		{"stop", Request{Cmd: "systemctl stop repair-agent.service"}, true},
		{"restart", Request{Cmd: "systemctl restart repair-agent.service"}, true},
		{"try-restart", Request{Cmd: "systemctl try-restart repair-agent.service"}, true},
		{"kill", Request{Cmd: "systemctl kill --signal SIGKILL repair-agent.service"}, true},
		{"absolute-paths", Request{Cmd: "/usr/bin/sudo -n /usr/bin/systemctl stop repair-agent.service"}, true},
		{"absolute-path-with-equals", Request{Cmd: "/tmp/release=v1/systemctl stop repair-agent.service"}, true},
		{"sudo-options", Request{Cmd: "sudo -u root -- /bin/systemctl --no-block restart repair-agent.service"}, true},
		{"quoted-unit", Request{Cmd: "systemctl 'stop' \"repair-agent.service\""}, true},
		{"short-unit", Request{Cmd: "systemctl stop repair-agent"}, true},
		{"multiple-units", Request{Cmd: "systemctl stop neighbor.service repair-agent.service"}, true},
		{"semicolon", Request{Cmd: "systemctl stop repair-agent.service; systemctl start repair-agent.service"}, true},
		{"multiline", Request{Cmd: "true\nsudo systemctl stop repair-agent.service\nsystemctl start repair-agent.service"}, true},
		{"conditional-list", Request{Cmd: "true && systemctl stop repair-agent.service || true"}, true},
		{"variable-prefix", Request{Cmd: "printf %s $HOME; systemctl stop repair-agent.service"}, true},
		{"braced-variable-prefix", Request{Cmd: "printf %s ${HOME}; systemctl stop repair-agent.service"}, true},
		{"substitution-prefix", Request{Cmd: "printf %s $(printf 'a;b'); systemctl stop repair-agent.service"}, true},
		{"backtick-prefix", Request{Cmd: "printf %s " + string(rune(96)) + "true" + string(rune(96)) + "; systemctl stop repair-agent.service"}, true},
		{"continued-line", Request{Cmd: "sudo systemctl \\\nstop repair-agent.service"}, true},
		{"comment-and-assignment", Request{Cmd: "# preparation\nLANG=C sudo systemctl stop repair-agent.service # maintenance"}, true},
		{"argv", Request{Argv: []string{"systemctl", "stop", "repair-agent.service"}}, true},
		{"argv-path-with-equals", Request{Argv: []string{"/tmp/release=v1/systemctl", "stop", "repair-agent.service"}}, true},
		{"sudo-argv", Request{Argv: []string{"/usr/bin/sudo", "-n", "/bin/systemctl", "kill", "-s", "KILL", "repair-agent.service"}}, true},
		{"foreign-unit", Request{Cmd: "systemctl stop neighbor.service"}, false},
		{"unit-prefix", Request{Cmd: "systemctl stop repair-agent.service-backup.service"}, false},
		{"status", Request{Cmd: "sudo systemctl status repair-agent.service"}, false},
		{"show", Request{Cmd: "systemctl show repair-agent.service -p stop"}, false},
		{"start", Request{Cmd: "systemctl start repair-agent.service"}, false},
		{"printed-command", Request{Cmd: "printf '%s\\n' 'systemctl stop repair-agent.service; systemctl start repair-agent.service'"}, false},
		{"comment", Request{Cmd: "true # systemctl stop repair-agent.service"}, false},
		{"detached", Request{Cmd: "sudo systemd-run --collect --unit=maintenance-123 /bin/bash /absolute/maintenance.sh stop repair-agent.service"}, false},
		{"detached-inline-script", Request{Cmd: "sudo systemd-run --collect --unit=maintenance-123 /bin/bash -c 'systemctl stop repair-agent.service; systemctl start repair-agent.service'"}, false},
		{"detached-argv", Request{Argv: []string{"sudo", "systemd-run", "--collect", "--unit=maintenance-123", "systemctl", "stop", "repair-agent.service"}}, false},
		{"remote", Request{Cmd: "systemctl --host=other-server stop repair-agent.service"}, false},
		{"machine", Request{Cmd: "systemctl -M container stop repair-agent.service"}, false},
		// Nested scripts and expansion-dependent commands are outside the claim.
		{"nested-script", Request{Cmd: "bash -c 'systemctl stop repair-agent.service'"}, false},
		{"heredoc-data", Request{Cmd: "cat <<'EOF'\nsystemctl stop repair-agent.service\nEOF\n"}, false},
		{"expanded-unit", Request{Cmd: "systemctl stop \"$UNIT\""}, false},
		{"expanded-target-suffix", Request{Cmd: "systemctl stop repair-agent$SUFFIX"}, false},
		{"nested-substitution", Request{Cmd: "printf %s $(printf 'systemctl stop repair-agent.service; true')"}, false},
		{"argv-is-not-shell-source", Request{Argv: []string{"printf", "%s", "systemctl stop repair-agent.service"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := selfServiceControlError(cgroup, tt.req)
			if (err != nil) != tt.deny {
				t.Fatalf("deny=%v want=%v error=%v", err != nil, tt.deny, err)
			}
			if err != nil && !strings.Contains(err.Error(), "repair-agent.service") {
				t.Fatalf("refusal lost the owning service: %v", err)
			}
		})
	}
}

func TestSelfServiceControlCgroupMembership(t *testing.T) {
	tests := []struct {
		name, cgroup, target string
		deny                 bool
	}{
		{"unified", "0::/system.slice/unrelated-name.service\n", "unrelated-name.service", true},
		{"descendant", "0::/system.slice/other.service/job/subgroup\n", "other.service", true},
		{"v1-systemd", "5:cpu,cpuacct:/other\n1:name=systemd:/system.slice/old.service/subgroup\n", "old.service", true},
		{"v1-other-controller", "5:cpu:/system.slice/old.service\n", "old.service", false},
		{"user-service-default-systemctl", "0::/user.slice/user-1000.slice/user@1000.service/app.slice/editor.service/work\n", "editor.service", false},
		{"ancestor-service", "0::/system.slice/parent.service/nested/child.service\n", "parent.service", true},
		{"template", "0::/system.slice/worker@one.service/job\n", "worker@one.service", true},
		{"dotted-short-name", "0::/system.slice/worker.one.service\n", "worker.one", true},
		{"empty", "", "repair-agent.service", false},
		{"no-service", "0::/user.slice/session-1.scope\n", "repair-agent.service", false},
		{"neighbor", "0::/system.slice/repair-agent.service-other/work\n", "repair-agent.service", false},
		{"malformed", "not a cgroup entry", "repair-agent.service", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := selfServiceControlError(tt.cgroup, Request{Argv: []string{"systemctl", "stop", tt.target}})
			if (err != nil) != tt.deny {
				t.Fatalf("deny=%v want=%v error=%v", err != nil, tt.deny, err)
			}
		})
	}
}

func TestSelfServiceControlManagerNamespace(t *testing.T) {
	const systemGroup = "0::/system.slice/gptadmin-hub.service\n"
	const userGroup = "0::/user.slice/user-1000.slice/user@1000.service/app.slice/gptadmin-hub.service/jobs\n"
	tests := []struct {
		name, cgroup string
		req          Request
		deny         bool
	}{
		{"system-default", systemGroup, Request{Cmd: "systemctl stop gptadmin-hub.service"}, true},
		{"system-explicit", systemGroup, Request{Cmd: "systemctl --system stop gptadmin-hub.service"}, true},
		{"system-foreign-user", systemGroup, Request{Cmd: "systemctl --user stop gptadmin-hub.service"}, false},
		{"system-foreign-user-argv", systemGroup, Request{Argv: []string{"systemctl", "--user", "stop", "gptadmin-hub.service"}}, false},
		{"system-xdg-does-not-select-user", systemGroup, Request{Cmd: "systemctl stop gptadmin-hub.service", Env: map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}}, true},
		{"user-default-is-system", userGroup, Request{Cmd: "systemctl stop gptadmin-hub.service"}, false},
		{"user-foreign-system", userGroup, Request{Cmd: "systemctl --system stop gptadmin-hub.service"}, false},
		{"user-local-default", userGroup, Request{Cmd: "systemctl --user stop gptadmin-hub.service"}, true},
		{"user-local-xdg", userGroup, Request{Cmd: "systemctl --user stop gptadmin-hub.service", Env: map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}}, true},
		{"user-foreign-xdg", userGroup, Request{Cmd: "systemctl --user stop gptadmin-hub.service", Env: map[string]string{"XDG_RUNTIME_DIR": "/run/user/2000"}}, false},
		{"user-parent-system-unit", userGroup, Request{Cmd: "systemctl stop user@1000.service"}, true},
		{"user-parent-in-other-manager", userGroup, Request{Cmd: "systemctl --user stop user@1000.service"}, false},
		{"last-manager-option-system", systemGroup, Request{Cmd: "systemctl --user --system stop gptadmin-hub.service"}, true},
		{"last-manager-option-user", systemGroup, Request{Cmd: "systemctl --system --user stop gptadmin-hub.service"}, false},
		{"different-managers-in-list", systemGroup, Request{Cmd: "systemctl --user stop gptadmin-hub.service; systemctl --system stop gptadmin-hub.service"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := selfServiceControlError(tt.cgroup, tt.req)
			if (err != nil) != tt.deny {
				t.Fatalf("deny=%v want=%v error=%v", err != nil, tt.deny, err)
			}
		})
	}
}
