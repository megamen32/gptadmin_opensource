//go:build !windows

package mcpclient

// Pure classification of a group probe result, no process/network execution.
import (
	"os/exec"
	"syscall"
	"testing"
)

func TestProcessGroupDispositionRequiresConfirmedAbsence(t *testing.T) {
	if err := processGroupDisposition(syscall.ESRCH); err != nil {
		t.Fatal(err)
	}
	for _, probe := range []error{nil, syscall.EPERM, syscall.EINVAL} {
		if processGroupDisposition(probe) == nil {
			t.Fatal("existing or uninspectable group reported disposed")
		}
	}
}

func TestScopedUnixStdioCapabilityFailsClosedBeforeLaunch(t *testing.T) {
	if ScopedProcessGroupsSupported() {
		t.Fatal("unsafe scoped Unix process lifecycle advertised as supported")
	}
	cmd := &exec.Cmd{}
	if err := prepareStdioProcess(cmd, true); err == nil {
		t.Fatal("scoped Unix process launch was admitted")
	}
	if err := prepareStdioProcess(cmd, false); err != nil {
		t.Fatal("legacy unscoped process preparation changed")
	}
}
