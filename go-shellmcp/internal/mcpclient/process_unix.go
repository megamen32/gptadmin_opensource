//go:build !windows

package mcpclient

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
)

// applyStdioUser enforces the registered definition's real OS authority.
// Setting USER/HOME alone would only impersonate the name while keeping root UID.
func applyStdioUser(cmd *exec.Cmd, username string) error {
	if username == "" {
		return nil
	}
	account, err := user.Lookup(username)
	if err != nil {
		return fmt.Errorf("lookup %q: %w", username, err)
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil {
		return fmt.Errorf("invalid UID for %q: %w", username, err)
	}
	gid, err := strconv.ParseUint(account.Gid, 10, 32)
	if err != nil {
		return fmt.Errorf("invalid GID for %q: %w", username, err)
	}
	currentUID := os.Geteuid()
	if currentUID != 0 && uint64(currentUID) != uid {
		return fmt.Errorf("cannot start %q (UID %d) from unprivileged UID %d", username, uid, currentUID)
	}
	if uint64(currentUID) != uid {
		memberships, err := account.GroupIds()
		if err != nil {
			return fmt.Errorf("groups for %q: %w", username, err)
		}
		groups := make([]uint32, 0, len(memberships))
		for _, id := range memberships {
			n, err := strconv.ParseUint(id, 10, 32)
			if err != nil {
				return fmt.Errorf("invalid supplementary GID for %q: %w", username, err)
			}
			groups = append(groups, uint32(n))
		}
		if cmd.SysProcAttr == nil {
			cmd.SysProcAttr = &syscall.SysProcAttr{}
		}
		cmd.SysProcAttr.Credential = &syscall.Credential{
			Uid: uint32(uid), Gid: uint32(gid), Groups: groups,
		}
	}
	// Last env assignment wins in os/exec. Resolve user config against its
	// actual home, not the privileged parent process's home.
	cmd.Env = append(cmd.Env,
		"HOME="+account.HomeDir,
		"USER="+account.Username,
		"LOGNAME="+account.Username,
	)
	return nil
}

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
