//go:build windows

package mcpclient

import (
	"errors"
	"os/exec"
)

func ScopedProcessGroupsSupported() bool { return false }

func prepareStdioProcess(cmd *exec.Cmd, grouped bool) error {
	if grouped {
		return errors.New("scoped stdio process-tree cleanup is unsupported on Windows")
	}
	return nil
}

func terminateStdioProcess(cmd *exec.Cmd, grouped bool) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func confirmStdioProcessDisposition(cmd *exec.Cmd, grouped bool) error {
	if grouped {
		return errors.New("scoped stdio process-tree disposition unsupported on Windows")
	}
	return nil
}
