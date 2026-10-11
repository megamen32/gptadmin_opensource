// Package node runs hub routing and its local executor in one process.
package node

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"path/filepath"

	"github.com/megamen32/gptadmin/go-hub/internal/hub"
	shell "github.com/megamen32/gptadmin/go-shellmcp/runtime"
)

// Run owns listener. The existing durable queue remains the sole execution
// path, reached over this same listener; no standalone shell service is needed.
func Run(ctx context.Context, listener net.Listener, cfg hub.Config, executorConfig shell.Config) error {
	defer listener.Close()
	if cfg.ShellToken == "" {
		return fmt.Errorf("SHELL_TOKEN is required for node startup")
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return err
	}
	loopback := "127.0.0.1"
	if address, ok := listener.Addr().(*net.TCPAddr); ok {
		if !address.IP.IsUnspecified() {
			loopback = address.IP.String()
		} else if address.IP.To4() == nil {
			loopback = "::1"
		}
	}
	executorConfig.HubURL = "http://" + net.JoinHostPort(loopback, port)
	executorConfig.HubDNSServer = ""
	executorConfig.HubResolveTo = ""
	var localCredential [32]byte
	if _, err := rand.Read(localCredential[:]); err != nil {
		return err
	}
	executorConfig.Token = hex.EncodeToString(localCredential[:])
	executorConfig.QueueEnabled = true
	executorConfig.Mode = "long_poll"
	executorConfig.HeartbeatEnabled = true
	executorConfig.DisableSelfUpdate = true
	executorConfig.SSHHost = ""
	executorConfig.SSHUser = ""
	executorConfig.SSHPassword = ""
	executorConfig.SSHKeyPath = ""
	if executorConfig.IdentityDir == "" {
		executorConfig.IdentityDir = cfg.ConfigDir
	}
	if executorConfig.SpillDir == "" {
		executorConfig.SpillDir = filepath.Join(cfg.ConfigDir, "spool")
	}
	if executorConfig.OutboxDir == "" {
		executorConfig.OutboxDir = filepath.Join(executorConfig.SpillDir, "outbox")
	}
	executor := shell.New(executorConfig)
	defer executor.Close()
	name, identity, err := executor.LocalIdentity()
	if err != nil {
		return err
	}
	router := hub.New(cfg)
	defer router.Close()
	if err := router.RegisterLocalExecutor(name, identity, executorConfig.Token); err != nil {
		return err
	}
	// Drain readiness reads the executor's callbacks and outbox depth.
	router.SetNodeRuntimeCounters(executor)
	hubCtx, cancelHub := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelHub()
	shellCtx, cancelShell := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelShell()
	errors := make(chan error, 2)
	go func() { errors <- router.ServeContext(hubCtx, listener) }()
	go func() { errors <- executor.ListenAndServeContext(shellCtx) }()
	var first error
	remaining := 2
	select {
	case first = <-errors:
		remaining--
	case <-ctx.Done():
	}
	cancelShell()
	executor.StopCallbacks()
	cancelHub()
	for ; remaining > 0; remaining-- {
		if err := <-errors; first == nil {
			first = err
		}
	}
	return first
}
