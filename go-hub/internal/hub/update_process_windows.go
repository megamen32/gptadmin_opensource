//go:build windows

package hub

import "os/exec"

func setDetachedProcess(*exec.Cmd) {
	// Windows never executes the systemd or launchd branches.
}
