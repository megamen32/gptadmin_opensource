package server

import (
	"context"
	"errors"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

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
	// The legacy Hub-manifest updater is retained only as an explicit opt-in.
	// Normal installs self-repair from public GitHub Releases; Hub runtime policy
	// overrides the autonomous latest-release fallback when available.
	if legacyManifestUpdateEnabled() {
		cfg := update.ConfigFromEnv()
		if !cfg.AutoUpdate || cfg.ManifestURL == "" {
			return
		}
		exe, err := os.Executable()
		if err != nil {
			log.Printf("update: executable: %v", err)
			return
		}
		upd, err := update.New(cfg, parseBuildVersion(BuildVersion), exe)
		if err != nil {
			log.Printf("update: init failed: %v", err)
			return
		}
		go func() {
			if err := upd.Run(ctx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				log.Printf("legacy update: %v", err)
			}
		}()
		return
	}
	s.startGitHubLatestLoop(ctx)
}
func (s *Server) startGitHubLatestLoop(ctx context.Context) {
	if selfRepairDisabled() {
		return
	}
	interval := time.Hour
	if raw := strings.TrimSpace(os.Getenv("SHELLMCP_UPDATE_INTERVAL_S")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 60 {
			interval = time.Duration(n) * time.Second
		}
	}
	go func() {
		grace := time.NewTimer(30 * time.Second)
		defer grace.Stop()
		select {
		case <-ctx.Done():
			return
		case <-grace.C:
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if !s.selfRepairPolicySeen.Load() {
				cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				latest, err := update.LatestGitHubBuild(cctx, update.DefaultGitHubReleaseRepo, nil)
				cancel()
				if err != nil {
					log.Printf("self-repair: GitHub latest check failed: %v", err)
				} else {
					s.triggerGitHubSelfRepair(latest, update.DefaultGitHubReleaseRepo)
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func legacyManifestUpdateEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("SHELLMCP_LEGACY_MANIFEST_UPDATE")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func (s *Server) triggerGitHubSelfRepair(desired int, repo string) {
	if desired <= 0 || selfRepairDisabled() {
		return
	}
	current := parseBuildVersion(BuildVersion)
	previousDesired := s.selfRepairDesired.Swap(int64(desired))
	if desired <= current {
		s.selfRepairState.Store("up_to_date")
		return
	}
	now := time.Now().Unix()
	last := s.selfRepairLastAttempt.Load()
	if int(previousDesired) == desired && last > 0 && now-last < 600 {
		return
	}
	if !s.selfRepairBusy.CompareAndSwap(false, true) {
		return
	}
	s.selfRepairLastAttempt.Store(now)
	s.selfRepairState.Store("pending")
	go func() {
		defer s.selfRepairBusy.Store(false)
		s.selfRepairState.Store("updating")
		exe, err := os.Executable()
		if err != nil {
			log.Printf("self-repair: executable: %v", err)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), update.SelfRepairTimeout())
		defer cancel()
		result, err := update.ApplyGitHubRelease(ctx, update.GitHubReleaseConfig{Repo: repo, DesiredBuild: desired, CurrentBuild: parseBuildVersion(BuildVersion), CurrentExe: exe})
		if err != nil {
			s.selfRepairState.Store("failed")
			log.Printf("self-repair: GitHub v%d failed: %v", desired, err)
			return
		}
		if !result.Updated {
			return
		}
		if result.NeedsHelper {
			s.selfRepairState.Store("staged")
			if err := update.ScheduleWindowsReplace(exe, result.StagedPath, os.Args[1:]); err != nil {
				s.selfRepairState.Store("failed")
				log.Printf("self-repair: windows helper failed: %v", err)
				return
			}
			s.selfRepairState.Store("restart_scheduled")
			log.Printf("self-repair: staged GitHub release v%d asset=%s sha256=%s; Windows restart task scheduled", desired, result.Asset, result.ArchiveSHA)
			time.Sleep(250 * time.Millisecond)
			os.Exit(update.WindowsSelfRepairExitCode)
		}
		s.selfRepairState.Store("applied")
		log.Printf("self-repair: applied GitHub release v%d asset=%s sha256=%s", desired, result.Asset, result.ArchiveSHA)
		time.Sleep(250 * time.Millisecond)
		os.Exit(0)
	}()
}

func selfRepairDisabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("SHELLMCP_SELF_REPAIR_DISABLE")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}
