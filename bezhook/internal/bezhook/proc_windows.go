//go:build windows

package bezhook

import (
	"os"
	"os/exec"
)

// setPgid is a no-op on Windows; exec.CommandContext handles teardown.
func setPgid(cmd *exec.Cmd) {}

// killTree terminates the provider process directly on Windows.
func killTree(p *os.Process) {
	if p == nil {
		return
	}
	_ = p.Kill()
}
