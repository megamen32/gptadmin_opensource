//go:build linux

package shell

import (
	"maps"
	"os"
)

func checkSelfServiceControl(req Request) error {
	cgroup, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		// Absence of procfs is not evidence of ownership of any target unit.
		return nil
	}
	if _, explicit := req.Env["XDG_RUNTIME_DIR"]; !explicit {
		if runtimeDir, exists := os.LookupEnv("XDG_RUNTIME_DIR"); exists {
			req.Env = maps.Clone(req.Env)
			if req.Env == nil {
				req.Env = make(map[string]string)
			}
			req.Env["XDG_RUNTIME_DIR"] = runtimeDir
		}
	}
	return selfServiceControlError(string(cgroup), req)
}
