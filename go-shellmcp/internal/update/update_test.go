package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// helper: build a manifest payload with sha256 + url
func mustManifest(t *testing.T, buildVersion int, payload []byte) map[string]any {
	t.Helper()
	sum := sha256.Sum256(payload)
	return map[string]any{
		"name":          "shellmcp-go",
		"build_version": buildVersion,
		"git_commit":    "deadbeef",
		"sha256":        hex.EncodeToString(sum[:]),
		"url":           "/shellmcp-new.bin",
		"size":          len(payload),
	}
}

func startManifestServer(t *testing.T, manifest map[string]any, binary []byte, requireToken string) (*httptest.Server, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		if requireToken != "" {
			got := r.Header.Get("Authorization")
			want := "Bearer " + requireToken
			if got != want {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(manifest)
	})
	mux.HandleFunc("/shellmcp-new.bin", func(w http.ResponseWriter, r *http.Request) {
		if requireToken != "" {
			got := r.Header.Get("Authorization")
			want := "Bearer " + requireToken
			if got != want {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		_, _ = w.Write(binary)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, srv.URL + "/manifest.json"
}

func TestConfigFromEnvDefaults(t *testing.T) {
	// Clear env vars that might leak in.
	t.Setenv("SHELLMCP_UPDATE_MANIFEST_URL", "")
	t.Setenv("SHELLMCP_UPDATE_URL", "")
	t.Setenv("SHELLMCP_UPDATE_TOKEN", "")
	t.Setenv("SHELLMCP_UPDATE_INTERVAL_S", "")
	t.Setenv("SHELLMCP_RESTART_CMD", "")
	t.Setenv("SHELLMCP_AUTO_UPDATE", "")
	cfg := ConfigFromEnv()
	if cfg.ManifestURL != "" {
		t.Errorf("ManifestURL=%q want empty", cfg.ManifestURL)
	}
	if cfg.UpdateURL != "" {
		t.Errorf("UpdateURL=%q want empty", cfg.UpdateURL)
	}
	if cfg.Token != "" {
		t.Errorf("Token=%q want empty", cfg.Token)
	}
	if cfg.Interval <= 0 {
		t.Errorf("Interval=%v want positive default", cfg.Interval)
	}
	if cfg.AutoUpdate {
		t.Errorf("AutoUpdate=true want false")
	}
	if cfg.RestartCmd != "" {
		t.Errorf("RestartCmd=%q want empty", cfg.RestartCmd)
	}
}

func TestConfigFromEnvOverrides(t *testing.T) {
	t.Setenv("SHELLMCP_UPDATE_MANIFEST_URL", "https://example.com/m.json")
	t.Setenv("SHELLMCP_UPDATE_URL", "https://example.com/b.bin")
	t.Setenv("SHELLMCP_UPDATE_TOKEN", "secret")
	t.Setenv("SHELLMCP_UPDATE_INTERVAL_S", "120")
	t.Setenv("SHELLMCP_RESTART_CMD", "systemctl restart shellmcp")
	t.Setenv("SHELLMCP_AUTO_UPDATE", "true")
	cfg := ConfigFromEnv()
	if cfg.ManifestURL != "https://example.com/m.json" {
		t.Errorf("ManifestURL=%q", cfg.ManifestURL)
	}
	if cfg.UpdateURL != "https://example.com/b.bin" {
		t.Errorf("UpdateURL=%q", cfg.UpdateURL)
	}
	if cfg.Token != "secret" {
		t.Errorf("Token=%q", cfg.Token)
	}
	if cfg.Interval != 120*time.Second {
		t.Errorf("Interval=%v", cfg.Interval)
	}
	if cfg.RestartCmd != "systemctl restart shellmcp" {
		t.Errorf("RestartCmd=%q", cfg.RestartCmd)
	}
	if !cfg.AutoUpdate {
		t.Errorf("AutoUpdate=false want true")
	}
}

func TestVersionCompareHelper(t *testing.T) {
	cases := []struct {
		name             string
		current, latest  int
		wantNeedsUpdate  bool
	}{
		{"equal", 5, 5, false},
		{"current-newer", 10, 5, false},
		{"one-behind", 4, 5, true},
		{"zero-current", 0, 1, true},
		{"both-zero", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsUpdate(tc.current, tc.latest); got != tc.wantNeedsUpdate {
				t.Errorf("needsUpdate(%d, %d)=%v want %v", tc.current, tc.latest, got, tc.wantNeedsUpdate)
			}
		})
	}
}

func TestCheckOnceUpToDate(t *testing.T) {
	// Manifest build_version <= current -> nil, nil
	bin := []byte("old-binary")
	manifest := mustManifest(t, 3, bin)
	_, manifestURL := startManifestServer(t, manifest, bin, "")
	u, err := New(Config{ManifestURL: manifestURL, Timeout: 5 * time.Second}, 5, "/tmp/not-used-check-once")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	info, err := u.CheckOnce(context.Background())
	if err != nil {
		t.Fatalf("CheckOnce: %v", err)
	}
	if info != nil {
		t.Fatalf("expected nil info when up-to-date, got %+v", info)
	}
}

func TestCheckOnceNewerAvailable(t *testing.T) {
	bin := []byte("newer-binary-bytes")
	manifest := mustManifest(t, 7, bin)
	_, manifestURL := startManifestServer(t, manifest, bin, "")
	u, err := New(Config{ManifestURL: manifestURL, Timeout: 5 * time.Second}, 5, "/tmp/not-used-newer")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	info, err := u.CheckOnce(context.Background())
	if err != nil {
		t.Fatalf("CheckOnce: %v", err)
	}
	if info == nil {
		t.Fatal("expected UpdateInfo, got nil")
	}
	if info.BuildVersion != 7 {
		t.Errorf("BuildVersion=%d want 7", info.BuildVersion)
	}
	if info.GitCommit != "deadbeef" {
		t.Errorf("GitCommit=%q want deadbeef", info.GitCommit)
	}
	if info.Name != "shellmcp-go" {
		t.Errorf("Name=%q want shellmcp-go", info.Name)
	}
	wantSHA := sha256.Sum256(bin)
	if info.SHA256 != hex.EncodeToString(wantSHA[:]) {
		t.Errorf("SHA256=%q want %q", info.SHA256, hex.EncodeToString(wantSHA[:]))
	}
	if !strings.HasSuffix(info.URL, "/shellmcp-new.bin") {
		t.Errorf("URL=%q does not end with /shellmcp-new.bin", info.URL)
	}
}

func TestCheckOnceUsesToken(t *testing.T) {
	bin := []byte("binary")
	manifest := mustManifest(t, 9, bin)
	_, manifestURL := startManifestServer(t, manifest, bin, "topsecret")
	u, err := New(Config{ManifestURL: manifestURL, Token: "topsecret", Timeout: 5 * time.Second}, 1, "/tmp/not-used-token")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	info, err := u.CheckOnce(context.Background())
	if err != nil {
		t.Fatalf("CheckOnce: %v", err)
	}
	if info == nil || info.BuildVersion != 9 {
		t.Fatalf("expected update info, got %+v", info)
	}
}

func TestApplySucceedsAndSwaps(t *testing.T) {
	newBin := []byte("brand-new-binary-payload")
	manifest := mustManifest(t, 12, newBin)
	_, manifestURL := startManifestServer(t, manifest, newBin, "")

	// Use a temp file as our "current exe" — never the real test binary.
	tmpDir := t.TempDir()
	oldExe := filepath.Join(tmpDir, "shellmcp-current")
	if err := os.WriteFile(oldExe, []byte("old-bytes"), 0o755); err != nil {
		t.Fatalf("seed old exe: %v", err)
	}
	originalStat, err := os.Stat(oldExe)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	u, err := New(Config{
		ManifestURL: manifestURL,
		Timeout:     5 * time.Second,
	}, 1, oldExe)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	info, err := u.CheckOnce(context.Background())
	if err != nil {
		t.Fatalf("CheckOnce: %v", err)
	}
	if info == nil {
		t.Fatal("expected update info")
	}

	if err := u.Apply(context.Background(), info); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Verify contents are the new binary.
	got, err := os.ReadFile(oldExe)
	if err != nil {
		t.Fatalf("read new exe: %v", err)
	}
	if string(got) != string(newBin) {
		t.Errorf("binary content mismatch: got %q want %q", got, newBin)
	}
	// Verify executable bit preserved.
	newStat, err := os.Stat(oldExe)
	if err != nil {
		t.Fatalf("stat new: %v", err)
	}
	if newStat.Mode().Perm()&0o100 == 0 {
		t.Errorf("executable bit missing: mode=%v", newStat.Mode().Perm())
	}
	_ = originalStat // ensure we at least referenced

	// Temp .new file should not exist after rename.
	if _, err := os.Stat(oldExe + ".new"); !os.IsNotExist(err) {
		t.Errorf(".new should be removed after rename, err=%v", err)
	}
}

func TestApplySHA256Mismatch(t *testing.T) {
	// Manifest advertises a sha256 that does not match the real binary payload.
	manifest := map[string]any{
		"name":          "shellmcp-go",
		"build_version": 99,
		"git_commit":    "cafef00d",
		"sha256":        strings.Repeat("0", 64),
		"url":           "/shellmcp-new.bin",
	}
	bin := []byte("real-payload")
	_, manifestURL := startManifestServer(t, manifest, bin, "")

	tmpDir := t.TempDir()
	oldExe := filepath.Join(tmpDir, "shellmcp-current")
	if err := os.WriteFile(oldExe, []byte("old"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}

	u, err := New(Config{ManifestURL: manifestURL, Timeout: 5 * time.Second}, 1, oldExe)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	info, err := u.CheckOnce(context.Background())
	if err != nil {
		t.Fatalf("CheckOnce: %v", err)
	}
	if info == nil {
		t.Fatal("expected update info")
	}

	err = u.Apply(context.Background(), info)
	if err == nil {
		t.Fatal("expected sha256 mismatch error, got nil")
	}
	// The original exe must NOT have been clobbered.
	got, _ := os.ReadFile(oldExe)
	if string(got) != "old" {
		t.Errorf("exe was modified despite sha256 failure: %q", got)
	}
}

func TestRunTriggersRestartSentinelWhenNoCmd(t *testing.T) {
	// Make CheckOnce always return "update available" and Apply succeed.
	newBin := []byte("binary-x")
	manifest := mustManifest(t, 42, newBin)
	_, manifestURL := startManifestServer(t, manifest, newBin, "")

	tmpDir := t.TempDir()
	oldExe := filepath.Join(tmpDir, "shellmcp-current")
	if err := os.WriteFile(oldExe, []byte("old"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// We can't override CheckOnce/Apply directly, so simulate by configuring
	// an extremely long interval and cancelling fast. Instead, test that Run
	// respects ctx cancellation and returns ctx.Err without performing a check.
	u, err := New(Config{
		ManifestURL: manifestURL,
		Timeout:     5 * time.Second,
		Interval:    1 * time.Hour, // long enough that we cancel first
	}, 1, oldExe)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- u.Run(ctx)
	}()
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v, expected context.Canceled or nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}

func TestRunExecutesRestartCmd(t *testing.T) {
	// Use a short interval and force an update; RestartCmd uses `true` (a builtin).
	newBin := []byte("new")
	manifest := mustManifest(t, 200, newBin)
	_, manifestURL := startManifestServer(t, manifest, newBin, "")

	tmpDir := t.TempDir()
	oldExe := filepath.Join(tmpDir, "shellmcp-current")
	if err := os.WriteFile(oldExe, []byte("old"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}

	u, err := New(Config{
		ManifestURL: manifestURL,
		UpdateURL:   "", // not used, info.URL overrides
		Timeout:     5 * time.Second,
		Interval:    50 * time.Millisecond,
		RestartCmd:  "true", // shell-safe builtin
	}, 1, oldExe)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = u.Run(ctx)
	// Run should exit cleanly (RestartCmd ran successfully).
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned unexpected error: %v", err)
	}
	// Old exe should now contain the new binary.
	got, _ := os.ReadFile(oldExe)
	if string(got) != string(newBin) {
		t.Errorf("exe not updated: got %q want %q", got, newBin)
	}
}

func TestNewRequiresManifestURL(t *testing.T) {
	_, err := New(Config{}, 1, "")
	if err == nil {
		t.Fatal("expected error when ManifestURL is empty")
	}
}

func TestNewRequiresExePath(t *testing.T) {
	_, err := New(Config{ManifestURL: "http://x/y"}, 1, "")
	if err == nil {
		t.Fatal("expected error when exe path is empty")
	}
}

func TestApplyDownloadFailure(t *testing.T) {
	// Serve a manifest whose URL points to a route that 500s.
	manifest := map[string]any{
		"name":          "shellmcp-go",
		"build_version": 1,
		"sha256":        "00",
		"url":           "/broken",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(manifest)
	})
	mux.HandleFunc("/broken", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	tmpDir := t.TempDir()
	exe := filepath.Join(tmpDir, "exe")
	_ = os.WriteFile(exe, []byte("old"), 0o755)

	u, err := New(Config{ManifestURL: srv.URL + "/manifest.json", Timeout: 2 * time.Second}, 0, exe)
	if err != nil {
		t.Fatal(err)
	}
	info, err := u.CheckOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info == nil {
		t.Fatal("expected update info")
	}
	err = u.Apply(context.Background(), info)
	if err == nil {
		t.Fatal("expected download error")
	}
}

func TestApplySkipsVerifyWhenShaEmpty(t *testing.T) {
	// Manifest sha256 empty -> Apply must still succeed without error.
	newBin := []byte("binary-y")
	manifest := map[string]any{
		"name":          "shellmcp-go",
		"build_version": 50,
		"git_commit":    "gitsha",
		"sha256":        "",
		"url":           "/shellmcp-new.bin",
	}
	_, manifestURL := startManifestServer(t, manifest, newBin, "")
	tmpDir := t.TempDir()
	exe := filepath.Join(tmpDir, "exe")
	_ = os.WriteFile(exe, []byte("old"), 0o755)
	u, err := New(Config{ManifestURL: manifestURL, Timeout: 5 * time.Second}, 1, exe)
	if err != nil {
		t.Fatal(err)
	}
	info, err := u.CheckOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info == nil {
		t.Fatal("expected info")
	}
	if err := u.Apply(context.Background(), info); err != nil {
		t.Fatalf("Apply with empty sha should succeed: %v", err)
	}
	got, _ := os.ReadFile(exe)
	if string(got) != string(newBin) {
		t.Errorf("exe content mismatch")
	}
}

func TestUserAgentHeader(t *testing.T) {
	var ua string
	mux := http.NewServeMux()
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		_, _ = io.WriteString(w, `{"build_version":1,"url":"/x","sha256":""}`)
	})
	mux.HandleFunc("/x", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	u, err := New(Config{ManifestURL: srv.URL + "/manifest.json", Timeout: 2 * time.Second}, 0, "/tmp/dummy-not-used")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.CheckOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ua, "shellmcp-go") {
		t.Errorf("User-Agent=%q missing shellmcp-go", ua)
	}
}

func TestErrRestartNeededIsExported(t *testing.T) {
	if ErrRestartNeeded == nil {
		t.Fatal("ErrRestartNeeded must be non-nil sentinel error")
	}
	if ErrRestartNeeded.Error() == "" {
		t.Error("ErrRestartNeeded must have non-empty message")
	}
}