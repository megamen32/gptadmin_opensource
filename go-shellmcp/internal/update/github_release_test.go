package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGitHubAssetNameMatrix(t *testing.T) {
	cases := []struct{ os, arch, want string }{{"linux", "amd64", "gptadmin-ubuntu-x64-client.tar.gz"}, {"linux", "arm64", "gptadmin-ubuntu-arm64-client.tar.gz"}, {"darwin", "amd64", "gptadmin-macos-x64-client.tar.gz"}, {"darwin", "arm64", "gptadmin-macos-arm64-client.tar.gz"}, {"windows", "amd64", "gptadmin-windows-x64-client.zip"}, {"windows", "arm64", "gptadmin-windows-arm64-client.zip"}}
	for _, tc := range cases {
		got, err := GitHubAssetName(tc.os, tc.arch)
		if err != nil || got != tc.want {
			t.Fatalf("%s/%s got=%q err=%v want=%q", tc.os, tc.arch, got, err, tc.want)
		}
	}
}

func TestArchiveExtractors(t *testing.T) {
	payload := []byte("shellmcp-binary")
	tarPath := filepath.Join(t.TempDir(), "a.tar.gz")
	var tb bytes.Buffer
	gz := gzip.NewWriter(&tb)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "bin/shellmcp", Mode: 0755, Size: int64(len(payload))})
	_, _ = tw.Write(payload)
	_ = tw.Close()
	_ = gz.Close()
	if err := os.WriteFile(tarPath, tb.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	tarOut := tarPath + ".out"
	if err := extractTarShellMCP(tarPath, tarOut); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(tarOut); !bytes.Equal(got, payload) {
		t.Fatalf("tar payload=%q", got)
	}
	zipPath := filepath.Join(t.TempDir(), "a.zip")
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	zf, _ := zw.Create("bin/shellmcp.exe")
	_, _ = zf.Write(payload)
	_ = zw.Close()
	if err := os.WriteFile(zipPath, zb.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	zipOut := zipPath + ".out"
	if err := extractZipShellMCP(zipPath, zipOut); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(zipOut); !bytes.Equal(got, payload) {
		t.Fatalf("zip payload=%q", got)
	}
}

func TestApplyGitHubReleaseFromFixture(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("fixture uses linux asset selected by runtime")
	}
	payload := []byte("new-linux-shellmcp")
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "bin/shellmcp", Mode: 0755, Size: int64(len(payload))})
	_, _ = tw.Write(payload)
	_ = tw.Close()
	_ = gz.Close()
	sum := sha256.Sum256(archive.Bytes())
	asset := "gptadmin-ubuntu-x64-client.tar.gz"
	if runtime.GOARCH == "arm64" {
		asset = "gptadmin-ubuntu-arm64-client.tar.gz"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v9/gptadmin-checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
	})
	mux.HandleFunc("/v9/"+asset, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archive.Bytes()) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	exe := filepath.Join(t.TempDir(), "shellmcp")
	if err := os.WriteFile(exe, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	res, err := ApplyGitHubRelease(context.Background(), GitHubReleaseConfig{DesiredBuild: 9, CurrentBuild: 8, CurrentExe: exe, ReleaseBaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Updated || res.Build != 9 {
		t.Fatalf("result=%+v", res)
	}
	got, _ := os.ReadFile(exe)
	if !bytes.Equal(got, payload) {
		t.Fatalf("got=%q", got)
	}
}

func TestChecksumForAssetRejectsMissingOrMalformed(t *testing.T) {
	if _, err := checksumForAsset("deadbeef  x.tar.gz\n", "x.tar.gz"); err == nil {
		t.Fatal("expected malformed checksum error")
	}
	if _, err := checksumForAsset(strings.Repeat("a", 64)+"  y.tar.gz\n", "x.tar.gz"); err == nil {
		t.Fatal("expected missing checksum error")
	}
}

func TestWindowsReplaceScriptRestartsCanonicalTaskBeforeDirectFallback(t *testing.T) {
	script := windowsReplaceScript(123, "gptadmin-shellmcp", "gptadmin-shellmcp-self-repair", `C:\ProgramData\gptadmin\bin\shellmcp.exe`, `C:\Temp\shellmcp.new`, `C:\Windows\Temp\gptadmin-shellmcp-self-repair.ps1`, []string{"--flag", "value"})
	for _, want := range []string{"Get-ScheduledTask -TaskName 'gptadmin-shellmcp'", "Start-ScheduledTask -TaskName 'gptadmin-shellmcp'", "Get-CimInstance Win32_Process", "if(-not $started){Start-Process"} {
		if !strings.Contains(script, want) {
			t.Fatalf("generated Windows replacement script missing %q: %s", want, script)
		}
	}
	if strings.Index(script, "Start-ScheduledTask") > strings.Index(script, "if(-not $started){Start-Process") {
		t.Fatalf("direct fallback appears before canonical task start: %s", script)
	}
}

func TestWindowsRepairUsesIndependentSystemTaskAndCanonicalRestart(t *testing.T) {
	scriptPath := `C:\Windows\Temp\gptadmin-shellmcp-self-repair.ps1`
	script := windowsReplaceScript(123, "gptadmin-shellmcp", "gptadmin-shellmcp-self-repair", `C:\ProgramData\gptadmin\bin\shellmcp.exe`, `C:\Windows\Temp\shellmcp.new`, scriptPath, []string{"--flag", "value"})
	for _, want := range []string{"Wait-Process -Id 123", "Get-ScheduledTask -TaskName 'gptadmin-shellmcp'", "Start-ScheduledTask -TaskName 'gptadmin-shellmcp'", "Unregister-ScheduledTask -TaskName 'gptadmin-shellmcp-self-repair'", "if(-not $started){Start-Process"} {
		if !strings.Contains(script, want) {
			t.Fatalf("helper script missing %q: %s", want, script)
		}
	}
	register := windowsRegisterHelperScript("gptadmin-shellmcp-self-repair", scriptPath)
	for _, want := range []string{"Register-ScheduledTask", "New-ScheduledTaskPrincipal -UserId 'SYSTEM'", "Start-ScheduledTask -TaskName 'gptadmin-shellmcp-self-repair'"} {
		if !strings.Contains(register, want) {
			t.Fatalf("registration script missing %q: %s", want, register)
		}
	}
	if WindowsSelfRepairExitCode == 0 {
		t.Fatal("Windows self-repair exit code must be non-zero so Task Scheduler restart policy is a fallback")
	}
}
