package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const DefaultGitHubReleaseRepo = "megamen32/gptadmin_opensource"

type GitHubReleaseConfig struct {
	Repo           string
	DesiredBuild   int
	CurrentBuild   int
	CurrentExe     string
	HTTPClient     *http.Client
	ReleaseBaseURL string
}

type GitHubReleaseResult struct {
	Updated     bool
	Build       int
	Asset       string
	ArchiveSHA  string
	StagedPath  string
	NeedsHelper bool
}

func GitHubAssetName(goos, goarch string) (string, error) {
	arch := ""
	switch goarch {
	case "amd64":
		arch = "x64"
	case "arm64":
		arch = "arm64"
	default:
		return "", fmt.Errorf("self-repair: unsupported arch %q", goarch)
	}
	switch goos {
	case "linux":
		return "gptadmin-ubuntu-" + arch + "-client.tar.gz", nil
	case "darwin":
		return "gptadmin-macos-" + arch + "-client.tar.gz", nil
	case "windows":
		return "gptadmin-windows-" + arch + "-client.zip", nil
	default:
		return "", fmt.Errorf("self-repair: unsupported os %q", goos)
	}
}

func ApplyGitHubRelease(ctx context.Context, cfg GitHubReleaseConfig) (GitHubReleaseResult, error) {
	var out GitHubReleaseResult
	if cfg.DesiredBuild <= 0 || cfg.DesiredBuild <= cfg.CurrentBuild {
		return out, nil
	}
	if strings.TrimSpace(cfg.CurrentExe) == "" {
		return out, fmt.Errorf("self-repair: current executable is required")
	}
	repo := strings.TrimSpace(cfg.Repo)
	if repo == "" {
		repo = DefaultGitHubReleaseRepo
	}
	asset, err := GitHubAssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return out, err
	}
	out.Build, out.Asset = cfg.DesiredBuild, asset
	base := strings.TrimSpace(cfg.ReleaseBaseURL)
	if base == "" {
		base = fmt.Sprintf("https://github.com/%s/releases/download", repo)
	}
	base = strings.TrimRight(base, "/") + "/v" + strconv.Itoa(cfg.DesiredBuild) + "/"
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	checksums, err := fetchBytes(ctx, client, base+"gptadmin-checksums.txt")
	if err != nil {
		return out, err
	}
	expected, err := checksumForAsset(string(checksums), asset)
	if err != nil {
		return out, err
	}
	archivePath, got, err := downloadTemp(ctx, client, base+asset, os.TempDir(), ".gptadmin-release-*")
	if err != nil {
		return out, err
	}
	defer os.Remove(archivePath)
	if !strings.EqualFold(got, expected) {
		return out, fmt.Errorf("self-repair: archive sha256 mismatch: got %s want %s", got, expected)
	}
	out.ArchiveSHA = got
	stageFile, err := os.CreateTemp(os.TempDir(), ".shellmcp-self-repair-*.new")
	if err != nil {
		return out, fmt.Errorf("self-repair: create staged file: %w", err)
	}
	stage := stageFile.Name()
	_ = stageFile.Close()
	_ = os.Remove(stage)
	if err := extractShellMCP(archivePath, asset, stage); err != nil {
		return out, err
	}
	if err := os.Chmod(stage, 0o755); err != nil {
		_ = os.Remove(stage)
		return out, fmt.Errorf("self-repair: chmod staged binary: %w", err)
	}
	if runtime.GOOS == "windows" {
		out.Updated, out.StagedPath, out.NeedsHelper = true, stage, true
		return out, nil
	}
	if err := replaceUnixExecutable(ctx, stage, cfg.CurrentExe); err != nil {
		_ = os.Remove(stage)
		return out, err
	}
	out.Updated = true
	return out, nil
}

func fetchBytes(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("self-repair: GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("self-repair: GET %s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return b, nil
}

func checksumForAsset(text, asset string) (string, error) {
	for _, line := range strings.Split(text, "\n") {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		name := strings.TrimPrefix(parts[len(parts)-1], "*")
		if filepath.Base(name) == asset {
			if len(parts[0]) != 64 {
				break
			}
			if _, err := hex.DecodeString(parts[0]); err != nil {
				break
			}
			return strings.ToLower(parts[0]), nil
		}
	}
	return "", fmt.Errorf("self-repair: checksum for %s not found", asset)
}

func downloadTemp(ctx context.Context, client *http.Client, url, dir, pattern string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("self-repair: download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("self-repair: download HTTP %d", resp.StatusCode)
	}
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", "", err
	}
	name := f.Name()
	h := sha256.New()
	_, cpErr := io.Copy(f, io.TeeReader(resp.Body, h))
	closeErr := f.Close()
	if cpErr != nil {
		os.Remove(name)
		return "", "", cpErr
	}
	if closeErr != nil {
		os.Remove(name)
		return "", "", closeErr
	}
	return name, hex.EncodeToString(h.Sum(nil)), nil
}

func extractShellMCP(archivePath, asset, dst string) error {
	if strings.HasSuffix(asset, ".zip") {
		return extractZipShellMCP(archivePath, dst)
	}
	return extractTarShellMCP(archivePath, dst)
}

func extractTarShellMCP(path, dst string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("self-repair: gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.ToSlash(h.Name)
		if name == "bin/shellmcp" || strings.HasSuffix(name, "/bin/shellmcp") {
			return writeExtracted(dst, tr)
		}
	}
	return fmt.Errorf("self-repair: bin/shellmcp not found in archive")
}

func extractZipShellMCP(path, dst string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, zf := range zr.File {
		name := filepath.ToSlash(zf.Name)
		if name == "bin/shellmcp.exe" || strings.HasSuffix(name, "/bin/shellmcp.exe") {
			r, err := zf.Open()
			if err != nil {
				return err
			}
			defer r.Close()
			return writeExtracted(dst, r)
		}
	}
	return fmt.Errorf("self-repair: bin/shellmcp.exe not found in archive")
}

func writeExtracted(dst string, r io.Reader) error {
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	_, cpErr := io.Copy(f, r)
	closeErr := f.Close()
	if cpErr != nil {
		os.Remove(dst)
		return cpErr
	}
	if closeErr != nil {
		os.Remove(dst)
		return closeErr
	}
	return nil
}

func replaceUnixExecutable(ctx context.Context, staged, current string) error {
	// First try a same-directory temp + rename. This works for normal user
	// installs and avoids cross-filesystem rename problems from /tmp.
	dir := filepath.Dir(current)
	local, err := os.CreateTemp(dir, ".shellmcp-replace-*.new")
	if err == nil {
		localName := local.Name()
		src, openErr := os.Open(staged)
		if openErr == nil {
			_, copyErr := io.Copy(local, src)
			_ = src.Close()
			closeErr := local.Close()
			if copyErr == nil && closeErr == nil && os.Chmod(localName, 0755) == nil {
				if renameErr := os.Rename(localName, current); renameErr == nil {
					_ = os.Remove(staged)
					return nil
				}
			}
		}
		_ = local.Close()
		_ = os.Remove(localName)
	}
	// Root-owned system install: never prompt. A preconfigured sudo policy may
	// allow install; otherwise fail closed and retry on the next repair cycle.
	cmd := exec.CommandContext(ctx, "sudo", "-n", "install", "-m", "0755", staged, current)
	if sudoErr := cmd.Run(); sudoErr != nil {
		return fmt.Errorf("self-repair: replace %s failed (direct=%v, sudo=%w)", current, err, sudoErr)
	}
	_ = os.Remove(staged)
	return nil
}

const WindowsSelfRepairExitCode = 42

func windowsReplaceScript(pid int, canonicalTask, helperTask, currentExe, stagedPath, scriptPath string, args []string) string {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	argList := make([]string, 0, len(args))
	for _, a := range args {
		argList = append(argList, q(a))
	}
	return "$ErrorActionPreference='Stop'; " +
		"Wait-Process -Id " + strconv.Itoa(pid) + " -ErrorAction SilentlyContinue; " +
		"for($i=0;$i -lt 80;$i++){try{$t=Get-ScheduledTask -TaskName " + q(canonicalTask) + " -ErrorAction SilentlyContinue;if($null -eq $t -or $t.State -ne 'Running'){break}}catch{break};Start-Sleep -Milliseconds 250}; " +
		"$ok=$false; for($i=0;$i -lt 80;$i++){try{Move-Item -Force " + q(stagedPath) + " " + q(currentExe) + ";$ok=$true;break}catch{Start-Sleep -Milliseconds 250}}; " +
		"if(-not $ok){exit 31}; " +
		"$started=$false; try{$task=Get-ScheduledTask -TaskName " + q(canonicalTask) + " -ErrorAction Stop; Start-ScheduledTask -TaskName " + q(canonicalTask) + "; " +
		"for($i=0;$i -lt 80;$i++){Start-Sleep -Milliseconds 250; $p=Get-CimInstance Win32_Process -Filter \"Name='shellmcp.exe'\" -ErrorAction SilentlyContinue | Where-Object {$_.ExecutablePath -and $_.ExecutablePath.Equals(" + q(currentExe) + ",[StringComparison]::OrdinalIgnoreCase)}; if($p){$started=$true;break}}}catch{}; " +
		"if(-not $started){Start-Process -FilePath " + q(currentExe) + " -ArgumentList @(" + strings.Join(argList, ",") + ") -WindowStyle Hidden}; " +
		"try{Unregister-ScheduledTask -TaskName " + q(helperTask) + " -Confirm:$false -ErrorAction SilentlyContinue}catch{}; " +
		"try{Remove-Item -LiteralPath " + q(scriptPath) + " -Force -ErrorAction SilentlyContinue}catch{}"
}

func windowsRegisterHelperScript(helperTask, scriptPath string) string {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	arg := "-NoProfile -NonInteractive -ExecutionPolicy Bypass -File `\"" + scriptPath + "`\""
	return "$ErrorActionPreference='Stop'; " +
		"$action=New-ScheduledTaskAction -Execute 'powershell.exe' -Argument " + q(arg) + "; " +
		"$trigger=New-ScheduledTaskTrigger -Once -At (Get-Date).AddSeconds(2); " +
		"$principal=New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest; " +
		"$settings=New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit ([TimeSpan]::FromMinutes(5)) -StartWhenAvailable; " +
		"Register-ScheduledTask -TaskName " + q(helperTask) + " -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null; " +
		"Start-ScheduledTask -TaskName " + q(helperTask)
}

func ScheduleWindowsReplace(currentExe, stagedPath string, args []string) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("self-repair: windows replacement requested on %s", runtime.GOOS)
	}
	const canonicalTask = "gptadmin-shellmcp"
	const helperTask = "gptadmin-shellmcp-self-repair"
	scriptPath := filepath.Join(os.TempDir(), helperTask+".ps1")
	script := windowsReplaceScript(os.Getpid(), canonicalTask, helperTask, currentExe, stagedPath, scriptPath, args)
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		return fmt.Errorf("self-repair: write Windows helper script: %w", err)
	}
	register := windowsRegisterHelperScript(helperTask, scriptPath)
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", register)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(scriptPath)
		return fmt.Errorf("self-repair: register Windows helper task: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func GitHubDesiredTag(build int) string { return "v" + strconv.Itoa(build) }
func SelfRepairTimeout() time.Duration  { return 3 * time.Minute }

func LatestGitHubBuild(ctx context.Context, repo string, client *http.Client) (int, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		repo = DefaultGitHubReleaseRepo
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	url := "https://github.com/" + repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("self-repair: latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("self-repair: latest release HTTP %d", resp.StatusCode)
	}
	parts := strings.Split(strings.Trim(resp.Request.URL.Path, "/"), "/")
	if len(parts) == 0 {
		return 0, fmt.Errorf("self-repair: latest release redirect missing tag")
	}
	tag := parts[len(parts)-1]
	if !strings.HasPrefix(tag, "v") {
		return 0, fmt.Errorf("self-repair: latest release tag %q", tag)
	}
	build, err := strconv.Atoi(strings.TrimPrefix(tag, "v"))
	if err != nil || build <= 0 {
		return 0, fmt.Errorf("self-repair: invalid latest release tag %q", tag)
	}
	return build, nil
}
