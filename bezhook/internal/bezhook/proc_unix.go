//go:build !windows

package bezhook

import (
	"os"
	"os/exec"
	"syscall"
)

// setPgid puts the provider process into its own process group so the whole
// tree can be signalled on shutdown.
func setPgid(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree sends SIGTERM to the process group of a provider process.
func killTree(p *os.Process) {
	if p == nil {
		return
	}
	_ = syscall.Kill(-p.Pid, syscall.SIGTERM)
}
