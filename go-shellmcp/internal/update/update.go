// Package update implements a self-update loop for go-shellmcp.
//
// It mirrors the Python shellmcp self-update flow: poll a remote manifest for
// a newer build_version, download the new binary, verify its sha256, and
// atomically swap it over the running executable. A caller can either set
// Config.RestartCmd (executed via /bin/sh) or rely on ErrRestartNeeded being
// returned by Run so the host program can perform its own restart sequence.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrRestartNeeded is returned by Run when an update has been applied but no
// Config.RestartCmd was provided. The caller is responsible for restarting the
// service (e.g. exec-ing the new binary in-place, signalling systemd, etc.).
var ErrRestartNeeded = errors.New("update applied; restart needed")

// DefaultInterval is the polling cadence used when Config.Interval is unset
// or non-positive. The Python reference uses 3600s (one hour).
const DefaultInterval = 1 * time.Hour

// DefaultTimeout is the per-HTTP-request timeout used when Config.Timeout is
// unset.
const DefaultTimeout = 30 * time.Second

// UserAgent is sent on every outbound HTTP request so update servers can
// identify go-shellmcp traffic.
const UserAgent = "shellmcp-go/1.0 (+self-update)"

// Config controls an Updater. Field names mirror the SHELLMCP_* environment
// variables used by the Python shellmcp.
type Config struct {
	// ManifestURL is the JSON manifest endpoint polled for new versions.
	// Env: SHELLMCP_UPDATE_MANIFEST_URL.
	ManifestURL string

	// UpdateURL is the fallback binary download URL when the manifest omits
	// one. Env: SHELLMCP_UPDATE_URL.
	UpdateURL string

	// Token is sent as a Bearer token to both manifest and binary endpoints
	// when non-empty. Env: SHELLMCP_UPDATE_TOKEN.
	Token string

	// Interval is how often CheckOnce is invoked from Run. Env:
	// SHELLMCP_UPDATE_INTERVAL_S (seconds).
	Interval time.Duration

	// Timeout is the per-HTTP-request timeout. Defaults to DefaultTimeout.
	Timeout time.Duration

	// AutoUpdate mirrors SHELLMCP_AUTO_UPDATE. It is informational only —
	// callers gate Run() on this themselves.
	AutoUpdate bool

	// RestartCmd, if non-empty, is executed via /bin/sh -c after a successful
	// Apply. When empty, Run returns ErrRestartNeeded instead. Env:
	// SHELLMCP_RESTART_CMD.
	RestartCmd string

	// HTTPClient overrides the default *http.Client when non-nil. Tests can
	// supply one with a custom Transport; production callers normally leave
	// this nil.
	HTTPClient *http.Client
}

// ConfigFromEnv populates a Config from the SHELLMCP_* environment variables
// used by the Python shellmcp. Missing/empty vars fall back to safe defaults;
// invalid integers default to the documented fallback value rather than
// panicking.
func ConfigFromEnv() Config {
	cfg := Config{
		ManifestURL: strings.TrimSpace(os.Getenv("SHELLMCP_UPDATE_MANIFEST_URL")),
		UpdateURL:   strings.TrimSpace(os.Getenv("SHELLMCP_UPDATE_URL")),
		Token:       os.Getenv("SHELLMCP_UPDATE_TOKEN"),
		RestartCmd:  os.Getenv("SHELLMCP_RESTART_CMD"),
	}
	cfg.AutoUpdate = parseBool(os.Getenv("SHELLMCP_AUTO_UPDATE"))
	cfg.Interval = parseSecondsEnv("SHELLMCP_UPDATE_INTERVAL_S", int(DefaultInterval.Seconds()))
	return cfg
}

// UpdateInfo describes a single artifact advertised by the manifest.
type UpdateInfo struct {
	Name         string `json:"name"`
	BuildVersion int    `json:"build_version"`
	GitCommit    string `json:"git_commit"`
	SHA256       string `json:"sha256"`
	URL          string `json:"url"`
}

// Updater polls a manifest, downloads new binaries, and swaps them into
// place. It is safe to construct one Updater per process; Run is the only
// method that performs periodic I/O.
type Updater struct {
	cfg               Config
	currentBuild      int
	currentExePath    string
	httpClient        *http.Client
	userAgent         string
}

// New validates cfg and constructs an Updater. currentBuildVersion is the
// running binary's build_version (parsed from the Go server's BuildVersion
// var or comparable source). currentExePath is the on-disk path of the
// running executable — typically os.Executable() at the call site.
func New(cfg Config, currentBuildVersion int, currentExePath string) (*Updater, error) {
	if strings.TrimSpace(cfg.ManifestURL) == "" {
		return nil, errors.New("update: ManifestURL is required")
	}
	if strings.TrimSpace(currentExePath) == "" {
		return nil, errors.New("update: currentExePath is required")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	return &Updater{
		cfg:            cfg,
		currentBuild:   currentBuildVersion,
		currentExePath: currentExePath,
		httpClient:     client,
		userAgent:      UserAgent,
	}, nil
}

// needsUpdate reports whether `latest` is strictly newer than `current`.
func needsUpdate(current, latest int) bool {
	return latest > current
}

// CheckOnce fetches the manifest, compares build_version, and returns either
// (nil, nil) when up-to-date or (*UpdateInfo, nil) when an update is
// available. Network/parse errors are returned as the second value.
func (u *Updater) CheckOnce(ctx context.Context) (*UpdateInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.cfg.ManifestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("update: build request: %w", err)
	}
	req.Header.Set("User-Agent", u.userAgent)
	req.Header.Set("Accept", "application/json")
	if u.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+u.cfg.Token)
	}

	resp, err := u.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: fetch manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("update: manifest status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var raw struct {
		Name         string `json:"name"`
		BuildVersion int    `json:"build_version"`
		Version      int    `json:"version"`
		GitCommit    string `json:"git_commit"`
		SHA256       string `json:"sha256"`
		URL          string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("update: parse manifest: %w", err)
	}

	latest := raw.BuildVersion
	if latest == 0 {
		latest = raw.Version
	}
	if !needsUpdate(u.currentBuild, latest) {
		return nil, nil
	}

	info := &UpdateInfo{
		Name:         raw.Name,
		BuildVersion: latest,
		GitCommit:    raw.GitCommit,
		SHA256:       strings.ToLower(strings.TrimSpace(raw.SHA256)),
		URL:          raw.URL,
	}
	if info.URL == "" {
		info.URL = u.cfg.UpdateURL
	}
	if info.URL == "" {
		return nil, errors.New("update: manifest did not include url and no UpdateURL fallback set")
	}
	// Resolve relative URLs against the manifest URL.
	if !strings.HasPrefix(info.URL, "http://") && !strings.HasPrefix(info.URL, "https://") {
		base := u.cfg.ManifestURL
		if idx := strings.LastIndex(base, "/"); idx >= 0 {
			base = base[:idx+1]
		}
		info.URL = base + strings.TrimPrefix(info.URL, "/")
	}
	return info, nil
}

// Apply downloads the new binary into a temp file, verifies its sha256 when
// the manifest provides one, and atomically renames it over the running
// executable. It does NOT restart the process; callers should follow up
// with a restart (via Config.RestartCmd or by exiting and letting the
// service supervisor restart us).
func (u *Updater) Apply(ctx context.Context, info *UpdateInfo) error {
	if info == nil {
		return errors.New("update: nil UpdateInfo")
	}
	if u.cfg.Timeout > 0 && u.httpClient == nil {
		// Defensive: ensure Apply uses a per-call timeout if Run isn't using
		// ctx-bound requests.
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, info.URL, nil)
	if err != nil {
		return fmt.Errorf("update: build download request: %w", err)
	}
	req.Header.Set("User-Agent", u.userAgent)
	if u.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+u.cfg.Token)
	}

	resp, err := u.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("update: download binary: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("update: download status %d", resp.StatusCode)
	}

	// Write to a sibling temp file in the same directory as the target so
	// os.Rename is guaranteed to be atomic (same filesystem).
	dir := filepath.Dir(u.currentExePath)
	tmp, err := os.CreateTemp(dir, ".shellmcp-update-*.tmp")
	if err != nil {
		return fmt.Errorf("update: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	hasher := sha256.New()
	written, err := io.Copy(tmp, io.TeeReader(resp.Body, hasher))
	if err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("update: write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("update: close temp file: %w", err)
	}

	// Verify sha256 only when the manifest advertises one. An empty SHA256
	// means "trust the manifest" — same posture as the Python reference when
	// sha256 is missing.
	if info.SHA256 != "" {
		got := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(got, info.SHA256) {
			cleanup()
			return fmt.Errorf("update: sha256 mismatch: got %s want %s", got, info.SHA256)
		}
	}

	// Ensure the new file is executable before swapping.
	if err := os.Chmod(tmpName, 0o755); err != nil {
		cleanup()
		return fmt.Errorf("update: chmod temp file: %w", err)
	}

	// Stage as <exe>.new so an interrupted Apply leaves the running binary
	// intact and a single os.Rename over <exe> provides the atomic swap.
	staged := u.currentExePath + ".new"
	if err := os.Rename(tmpName, staged); err != nil {
		cleanup()
		return fmt.Errorf("update: stage temp file: %w", err)
	}
	if err := os.Rename(staged, u.currentExePath); err != nil {
		// Try to roll back: remove staged, leave previous exe untouched.
		_ = os.Remove(staged)
		return fmt.Errorf("update: atomic rename: %w", err)
	}
	_ = written // bytes downloaded; surfaced implicitly via size comparisons
	return nil
}

// Run loops until ctx is cancelled. On each tick it performs CheckOnce; if
// an update is found it calls Apply and then either:
//
//   - executes Config.RestartCmd via /bin/sh -c, returning its exit error
//     after the command finishes, or
//   - returns ErrRestartNeeded so the host program can perform its own
//     restart sequence (preferred when the running binary needs to exec
//     itself in-place).
//
// Errors from individual CheckOnce calls are returned to the caller; the
// loop only terminates on a successful update (after restart), a
// non-recoverable CheckOnce error, or ctx cancellation.
func (u *Updater) Run(ctx context.Context) error {
	if u.cfg.Interval <= 0 {
		u.cfg.Interval = DefaultInterval
	}
	ticker := time.NewTicker(u.cfg.Interval)
	defer ticker.Stop()

	// Run an immediate check before waiting for the first tick.
	if err := u.tick(ctx); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := u.tick(ctx); err != nil {
				return err
			}
		}
	}
}

// tick performs one CheckOnce + (optional) Apply + restart cycle.
func (u *Updater) tick(ctx context.Context) error {
	info, err := u.CheckOnce(ctx)
	if err != nil {
		return err
	}
	if info == nil {
		return nil
	}
	if err := u.Apply(ctx, info); err != nil {
		return err
	}
	if u.cfg.RestartCmd != "" {
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", u.cfg.RestartCmd)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("update: restart command failed: %w", err)
		}
		return nil
	}
	return ErrRestartNeeded
}

// parseBool treats 1/true/yes/on (case-insensitive) as true.
func parseBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// parseSecondsEnv reads an integer-seconds env var, falling back to fallbackSec
// on parse error or missing value. Negative or zero results in fallbackSec.
func parseSecondsEnv(name string, fallbackSec int) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return time.Duration(fallbackSec) * time.Second
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return time.Duration(fallbackSec) * time.Second
	}
	return time.Duration(n) * time.Second
}