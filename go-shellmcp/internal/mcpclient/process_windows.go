//go:build windows

package mcpclient

import (
	"errors"
	"fmt"
	"os/exec"
)

// Windows has no supported in-process identity change for a named MCP user.
func applyStdioUser(_ *exec.Cmd, username string) error {
	if username != "" {
		return fmt.Errorf("configured stdio user %q requires an external OS account launcher on Windows", username)
	}
	return nil
}

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
