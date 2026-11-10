//go:build linux

// LHC:COVERS go-shellmcp/internal/shell/run.go go-shellmcp/internal/shell/self_service_control_linux.go
package shell

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The real runner is exercised against harmless temporary executables. No
// service-manager command is executed, and no system cgroup is changed.
func selfServiceControlFixture(t *testing.T) (string, string) {
	t.Helper()
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
			if unit == "" && strings.HasSuffix(part, ".service") {
				// Pick the system-manager ancestor (including user@UID.service)
				// so these default-systemctl fixtures target the owning manager.
				unit = part
			}
		}
	}
	if unit == "" {
		t.Skip("test process is not in a systemd service; pure cgroup fixtures cover this host")
	}
	bin := t.TempDir()
	control := filepath.Join(bin, "systemctl")
	sudo := filepath.Join(bin, "sudo")
	if err := os.WriteFile(control, []byte("#!/bin/sh\nprintf called >> \"$SELF_STOP_MARKER\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sudo, []byte("#!/bin/sh\n[ \"$1\" != -n ] || shift\nexec \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return bin, unit
}

func TestSelfServiceControlRunInternalRejectsBeforeStart(t *testing.T) {
	bin, unit := selfServiceControlFixture(t)
	control, sudo := filepath.Join(bin, "systemctl"), filepath.Join(bin, "sudo")
	for _, action := range []string{"stop", "restart", "try-restart", "kill"} {
		for _, mode := range []string{"cmd", "argv", "sudo-cmd", "sudo-argv", "multiline-list"} {
			t.Run(action+"/"+mode, func(t *testing.T) {
				marker := filepath.Join(t.TempDir(), "started")
				req := Request{RunAsUser: "root", SpillDir: t.TempDir(),
					Env: map[string]string{"SELF_STOP_MARKER": marker}}
				switch mode {
				case "cmd":
					req.Cmd = "systemctl " + action + " " + unit
				case "argv":
					req.Argv = []string{control, action, unit}
				case "sudo-cmd":
					req.Cmd = fmt.Sprintf("%q -n %q %s %s", sudo, control, action, unit)
				case "sudo-argv":
					req.Argv = []string{sudo, "-n", control, action, unit}
				case "multiline-list":
					req.Cmd = fmt.Sprintf("printf prefix >> %q;\nsudo -n systemctl %s %s; systemctl start %s", marker, action, unit, unit)
				}
				res, runErr := runInternal(context.Background(), req, 1024, nil)
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Errorf("request reached process execution before refusal: marker err=%v", err)
				}
				if runErr == nil || res.ReturnCode != -1 {
					t.Fatalf("own-service %s must be refused before Start: result=%+v err=%v", action, res, runErr)
				}
				for _, hint := range []string{"собствен", unit, "sudo systemd-run --collect --unit=<unique> /bin/bash /absolute/maintenance.sh", "journalctl"} {
					if !strings.Contains(res.Error, hint) {
						t.Errorf("missing actionable Russian refusal %q: %s", hint, res.Error)
					}
				}
			})
		}
	}
}

func TestSelfServiceControlRunInternalAllowsMaintenance(t *testing.T) {
	bin, unit := selfServiceControlFixture(t)
	if err := os.WriteFile(filepath.Join(bin, "systemd-run"), []byte("#!/bin/sh\nprintf called >> \"$SELF_STOP_MARKER\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	requests := []Request{
		{Cmd: "systemctl show " + unit},
		{Cmd: "sudo systemctl status " + unit},
		{Cmd: "systemctl stop different-unit.service"},
		{Cmd: "sudo systemd-run --collect --unit=maintenance-test /bin/bash -c 'systemctl stop " + unit + "; systemctl start " + unit + "'"},
		{Argv: []string{filepath.Join(bin, "sudo"), "systemd-run", "--collect", "--unit=maintenance-test", "systemctl", "stop", unit}},
	}
	for i, req := range requests {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "started")
			req.RunAsUser, req.SpillDir = "root", t.TempDir()
			req.Env = map[string]string{"SELF_STOP_MARKER": marker}
			res, runErr := runInternal(context.Background(), req, 1024, nil)
			if runErr != nil || res.ReturnCode != 0 {
				t.Fatalf("safe request was rejected: result=%+v err=%v", res, runErr)
			}
			if data, err := os.ReadFile(marker); err != nil || string(data) != "called" {
				t.Fatalf("safe request did not execute exactly once: data=%q err=%v", data, err)
			}
		})
	}
}

func TestSelfServiceControlRedirectionsRunInternal(t *testing.T) {
	_, unit := selfServiceControlFixture(t)
	for _, tt := range selfServiceControlRedirectionCases {
		if !tt.deny {
			continue // allowed redirection operands are covered by the pure cases
		}
		t.Run(tt.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "started")
			req := Request{
				Cmd: strings.ReplaceAll(tt.command, "OWN", unit), RunAsUser: "root",
				Cwd: t.TempDir(), SpillDir: t.TempDir(),
				Env: map[string]string{"SELF_STOP_MARKER": marker},
			}
			res, runErr := runInternal(context.Background(), req, 1024, nil)
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Errorf("redirected self-stop reached Start: marker err=%v", err)
			}
			if runErr == nil || res.ReturnCode != -1 || !strings.Contains(res.Error, unit) {
				t.Fatalf("redirected self-stop must be refused before Start: result=%+v err=%v", res, runErr)
			}
		})
	}
}
