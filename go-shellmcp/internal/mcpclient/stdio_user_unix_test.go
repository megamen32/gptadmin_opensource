//go:build !windows

package mcpclient

import (
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"testing"
)

func TestStdioConfiguredUserSetsTrueAuthority(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("true")
	cmd.Env = []string{"HOME=/root", "USER=root", "LOGNAME=root", "TEST=unchanged"}
	if err := applyStdioUser(cmd, me.Username); err != nil {
		t.Fatal(err)
	}
	for _, pair := range []string{
		"HOME=" + me.HomeDir, "USER=" + me.Username, "LOGNAME=" + me.Username,
	} {
		if got := cmd.Env[len(cmd.Env)-3:]; !containsString(got, pair) {
			t.Fatalf("%s not installed in trailing environment: %v", pair, got)
		}
	}
	if !containsString(cmd.Env, "TEST=unchanged") {
		t.Fatal("configured user unexpectedly removed unrelated environment")
	}
	if os.Geteuid() != 0 && cmd.SysProcAttr != nil && cmd.SysProcAttr.Credential != nil {
		t.Fatal("unprivileged matching user must not require setgroups or root")
	}
}

// Exercise the real OS process boundary; this helper never connects to a model.
func TestStdioConfiguredUserExecHasExpectedUID(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("id", "-u")
	cmd.Env = os.Environ()
	if err := applyStdioUser(cmd, me.Username); err != nil {
		t.Fatal(err)
	}
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(output)) != strconv.Itoa(os.Geteuid()) {
		t.Fatalf("configured user subprocess ran as UID %q, expected %d", strings.TrimSpace(string(output)), os.Geteuid())
	}
}

// On privileged CI hosts, verify actual root-to-unprivileged credential drop.
func TestStdioRootDropsPrivilegesWhenRequested(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("credential drop requires root; other user guards run without it")
	}
	unprivileged, err := user.Lookup("nobody")
	if err != nil {
		t.Skip("no nobody account")
	}
	cmd := exec.Command("id", "-u")
	cmd.Env = os.Environ()
	if err := applyStdioUser(cmd, unprivileged.Username); err != nil {
		t.Fatal(err)
	}
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(output)) != unprivileged.Uid {
		t.Fatalf("configured user subprocess ran as UID %q, expected %s", strings.TrimSpace(string(output)), unprivileged.Uid)
	}
}

func TestStdioUnknownConfiguredUserFailsClosed(t *testing.T) {
	cmd := exec.Command("true")
	if err := applyStdioUser(cmd, "gptadmin_nonexistent_stdio_user_913753"); err == nil {
		t.Fatal("unknown configured user was silently ignored")
	}
	if cmd.Process != nil {
		t.Fatal("user validation launched a process")
	}
}

func TestStdioConfiguredRootNeverRunsAsNonroot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("unprivileged authority guard is exercised by non-root test runner")
	}
	cmd := exec.Command("true")
	if err := applyStdioUser(cmd, "root"); err == nil ||
		!strings.Contains(err.Error(), "cannot start") {
		t.Fatalf("expected fail-closed UID switch, got %v", err)
	}
}

func containsString(items []string, want string) bool {
	for _, s := range items {
		if s == want {
			return true
		}
	}
	return false
}
