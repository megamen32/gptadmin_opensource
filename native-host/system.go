// System information handler.
package main

import (
	"context"
	"encoding/json"
	"os"
	"os/user"
	"runtime"
)

// SystemInfoResult is returned by system.info.
type SystemInfoResult struct {
	Hostname     string   `json:"hostname"`
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	Username     string   `json:"username"`
	HomeDir      string   `json:"home_dir"`
	Capabilities []string `json:"capabilities"`
}

// handleSystemInfo returns basic information about the host machine.
func handleSystemInfo(ctx context.Context, params json.RawMessage) (interface{}, error) {
	hostname, err := os.Hostname()
	if err != nil {
		// Non-fatal — use "unknown".
		hostname = "unknown"
	}

	username := "unknown"
	if u, err := user.Current(); err == nil {
		username = u.Username
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = ""
	}

	return SystemInfoResult{
		Hostname:     hostname,
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		Username:     username,
		HomeDir:      homeDir,
		Capabilities: []string{"files", "processes", "network"},
	}, nil
}

// handleNetworkDNS is a placeholder that returns a not-implemented message.
func handleNetworkDNS(ctx context.Context, params json.RawMessage) (interface{}, error) {
	return map[string]string{
		"message": "network.dns is not yet implemented",
	}, nil
}
