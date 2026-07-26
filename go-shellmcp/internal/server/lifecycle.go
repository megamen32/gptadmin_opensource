package server

import (
	"context"
	"errors"
	"log"
	"os"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/update"
)

// startUpdateLoop, when configured, builds an updater from environment
// variables and runs it in a goroutine. ctx cancellation triggers a clean
// shutdown of the loop (the updater returns on ctx.Done()). The loop is
// no-op when auto-update is disabled or no manifest URL is configured.
//
// Mirrors the Python shellmcp behavior: a successful update either
// executes cfg.RestartCmd (when set) or returns ErrRestartNeeded so an
// external supervisor can perform the swap.
func (s *Server) startUpdateLoop(ctx context.Context) {
	cfg := update.ConfigFromEnv()
	if !cfg.AutoUpdate || cfg.ManifestURL == "" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		log.Printf("update: cannot determine executable path: %v", err)
		return
	}
	currentBuild := parseBuildVersion(BuildVersion)
	upd, err := update.New(cfg, currentBuild, exe)
	if err != nil {
		log.Printf("update: init failed: %v", err)
		return
	}
	go func() {
		if err := upd.Run(ctx); err != nil {
			if errors.Is(err, update.ErrRestartNeeded) {
				log.Printf("update applied; restart needed")
				return
			}
			// Context cancellation is a normal shutdown — don't log it as
			// an error.
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			log.Printf("update: %v", err)
		}
	}()
}
