package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/megamen32/gptadmin/go-hub/internal/hub"
	"github.com/megamen32/gptadmin/go-hub/internal/node"
	shell "github.com/megamen32/gptadmin/go-shellmcp/runtime"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg := hub.FromEnv()
	executor := shell.FromEnv()
	// Unified-node defaults keep all state under the configured node root.
	// Explicit legacy paths remain usable during migration.
	if os.Getenv("SHELL_IDENTITY_DIR") == "" && os.Getenv("SHELLMCP_IDENTITY_DIR") == "" {
		executor.IdentityDir = cfg.ConfigDir
	}
	if os.Getenv("SHELL_SPOOL_DIR") == "" && os.Getenv("SHELLMCP_SPOOL_DIR") == "" && os.Getenv("SHELL_SPILL_DIR") == "" && os.Getenv("SHELLMCP_SPILL_DIR") == "" {
		executor.SpillDir = filepath.Join(cfg.ConfigDir, "spool")
		if os.Getenv("SHELL_OUTBOX_DIR") == "" && os.Getenv("SHELLMCP_OUTBOX_DIR") == "" {
			executor.OutboxDir = filepath.Join(executor.SpillDir, "outbox")
		}
	}
	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("gptadmin-node listening addr=%s", listener.Addr())
	if err := node.Run(ctx, listener, cfg, executor); err != nil {
		log.Fatal(err)
	}
}
