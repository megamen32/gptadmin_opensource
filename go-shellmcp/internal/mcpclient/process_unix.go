//go:build !windows

package mcpclient

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
)

// Scoped stdio stays disabled until its ownership fence cannot signal a reused PGID.
func ScopedProcessGroupsSupported() bool { return false }

func prepareStdioProcess(cmd *exec.Cmd, grouped bool) error {
	if grouped {
		return errors.New("scoped stdio ownership-safe lifecycle unavailable; managed stdio is disabled")
	}
	return nil
}

func terminateStdioProcess(cmd *exec.Cmd, grouped bool) {
	if cmd.Process == nil {
		return
	}
	if grouped {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return
	}
	_ = cmd.Process.Kill()
}

// Querying a group never signals it. After leader Wait, an existing PGID might
// be reused, so uncertainty retains ownership/capacity instead of killing it.
func confirmStdioProcessDisposition(cmd *exec.Cmd, grouped bool) error {
	if !grouped {
		return nil
	}
	if cmd.Process == nil {
		return errors.New("scoped stdio process group ownership unavailable")
	}
	return processGroupDisposition(syscall.Kill(-cmd.Process.Pid, 0))
}

func processGroupDisposition(probeErr error) error {
	if errors.Is(probeErr, syscall.ESRCH) {
		return nil
	}
	if probeErr == nil {
		return errors.New("scoped stdio process group still exists; disposal unconfirmed and late group kill refused")
	}
	return fmt.Errorf("scoped stdio process group disposal unconfirmed: %w", probeErr)
}
