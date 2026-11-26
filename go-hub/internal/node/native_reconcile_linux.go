//go:build linux

package node

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const nativeUpdater = "/usr/local/lib/gptadmin/gptadmin_node_autoupdate.py"
const nativeUpdaterConfig = "/etc/gptadmin-node/autoupdate.json"

var nativeInstanceUnit = regexp.MustCompile(`^gptadmin-node@[A-Za-z0-9_-]{1,128}\.service$`)

// Only the actual systemd Node, not a shell task, dev process, canary,
// or user service, may reconcile this host's native update scheduler.
func isNativeNodeService(cgroup string) bool {
	for _, line := range strings.Split(cgroup, "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		path := strings.TrimPrefix(parts[2], "/system.slice/")
		if path == parts[2] {
			continue
		}
		// systemd creates a dedicated slice for templated Node instances.
		path = strings.TrimPrefix(path, `system-gptadmin\x2dnode.slice/`)
		if path == "gptadmin-hub.service" || nativeInstanceUnit.MatchString(path) {
			return true
		}
	}
	return false
}

func protectedRootFile(path string, limit int64) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > limit {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return false
	}
	// Do not run a root-owned leaf through an untrusted or symlink parent.
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		p, err := os.Lstat(parent)
		if err != nil || !p.IsDir() || p.Mode().Perm()&0022 != 0 {
			return false
		}
		st, ok := p.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 {
			return false
		}
		if parent == "/" {
			break
		}
	}
	return true
}

func nativeUpdateOptedIn() bool {
	if !protectedRootFile(nativeUpdaterConfig, 65536) || !protectedRootFile(nativeUpdater, 2<<20) {
		return false
	}
	raw, err := os.ReadFile(nativeUpdaterConfig)
	var config struct {
		Enabled bool `json:"enabled"`
	}
	return err == nil && json.Unmarshal(raw, &config) == nil && config.Enabled
}

func startNativeLifecycleReconcile(ctx context.Context) func() {
	if os.Geteuid() != 0 {
		return func() {}
	}
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil || !isNativeNodeService(string(data)) {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		last := ""
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if nativeUpdateOptedIn() {
					childCtx, stop := context.WithTimeout(ctx, 40*time.Second)
					cmd := exec.CommandContext(childCtx, "/usr/bin/python3", "-I", "-B", nativeUpdater, "--reconcile-only")
					var out nativeReconcileOutput
					cmd.Stdout = &out
					// No raw command output (which may contain private deployment data).
					err := cmd.Run()
					stop()
					code := "native_reconcile_failed"
					if err == nil {
						for _, line := range strings.Split(out.String(), "\n") {
							var result struct {
								Code string `json:"code"`
							}
							if json.Unmarshal([]byte(line), &result) == nil {
								switch result.Code {
								case "native_recovery_hold", "native_enrollment_mismatch", "native_timer_not_enabled", "native_timer_healthy", "native_timer_state_unknown", "native_timer_started", "native_timer_start_pending":
									code = result.Code
								}
							}
						}
					}
					if code != last {
						log.Printf("native_lifecycle_reconcile code=%s", code)
						last = code
					}
				}
				timer.Reset(2 * time.Minute)
			}
		}
	}()
	return func() { cancel(); <-done }
}

// A native helper may emit diagnostics, but the running Node never stores
// unbounded output or forwards raw private deployment data to its logs.
type nativeReconcileOutput struct{ bytes.Buffer }

func (b *nativeReconcileOutput) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := 8192 - b.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return n, nil
}
