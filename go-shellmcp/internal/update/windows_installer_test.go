package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func windowsInstallerPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "..", "deploy", "install_win.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWindowsIsolatedInstallerRejectsUnsafeTargetsBeforeMutation(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native PowerShell installer validation")
	}
	target := filepath.Join(t.TempDir(), "green")
	name := "gptadmin-shellmcp-ci-green"
	base := []string{"-Isolated", "-System", "-NoStart", "-InstallDir", target,
		"-TaskName", name, "-ShellmcpName", "CI-isolated-green", "-ShellmcpPort", "55031",
		"-HubUrl", "https://example.test", "-PackageUrl", filepath.Join(t.TempDir(), "missing.zip")}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"missing explicit settings", []string{"-Isolated", "-System"}, "requires an explicit -InstallDir"},
		{"missing explicit package", base[:len(base)-2], "requires an explicit -PackageUrl"},
		{"user mode", []string{"-Isolated", "-User"}, "requires -System"},
		{"inherited credential rejected", append(append([]string{}, base...), "-ShellmcpToken", "must-not-copy-blue-credential"), "generates a new local credential"},
		{"negative build pin", append(append([]string{}, base...), "-LatestBuild", "-1"), "LatestBuild"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", windowsInstallerPath(t)}, tc.args...)
			out, err := exec.Command("powershell.exe", args...).CombinedOutput()
			if err == nil || !strings.Contains(string(out), tc.want) {
				t.Fatalf("expected %q rejection before download, got %v %s", tc.want, err, out)
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("rejected isolated invocation created target %s: %v", target, err)
			}
		})
	}
	if err := os.MkdirAll(filepath.Join(target, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(target, "bin", "shellmcp.exe")
	if err := os.WriteFile(sentinel, []byte("existing-install-must-remain-unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", windowsInstallerPath(t)}, base...)
	out, err := exec.Command("powershell.exe", args...).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "refuses an existing bin directory") {
		t.Fatalf("existing bin must be rejected before download: %v %s", err, out)
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "existing-install-must-remain-unchanged" {
		t.Fatalf("existing binary was touched: %v %q", err, data)
	}
}

func TestWindowsInstallerParsesAndGeneratesFreshIsolatedCredential(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native PowerShell installer parser and setup unit test")
	}
	path := windowsInstallerPath(t)
	check := "$tokens=$null; $errors=$null; [System.Management.Automation.Language.Parser]::ParseFile(" + powershellQuote(path) + ",[ref]$tokens,[ref]$errors) | Out-Null; if ($errors.Count) { $errors | Out-String | Write-Output; exit 1 }"
	if out, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", check).CombinedOutput(); err != nil {
		t.Fatalf("installer PowerShell syntax: %v %s", err, out)
	}
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the real argument validation and token generation prefix without
	// downloading a package or registering a task. Full install is a release gate.
	prefix, _, ok := strings.Cut(string(source), "function Assert-IsolatedTarget")
	if !ok {
		t.Fatal("isolated target guard not found")
	}
	prefix += "\nif ($TokenProvided -or $ShellmcpToken -eq $env:SHELLMCP_TOKEN -or $ShellmcpToken.Length -lt 32) { throw 'isolated credential was inherited' }\n"
	prefix += "if (-not $DisableSelfRepair) { throw 'isolated self-repair must default to disabled' }\n"
	prefix += "if ($ShellmcpBind -ne '127.0.0.1' -or $ShellmcpTransport -ne 'polling') { throw 'isolated transport defaults were inherited' }\n"
	probe := filepath.Join(t.TempDir(), "installer-setup.ps1")
	if err := os.WriteFile(probe, []byte(prefix), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELLMCP_TOKEN", "blue-credential-that-must-not-be-copied")
	t.Setenv("SHELLMCP_BIND", "0.0.0.0")
	t.Setenv("SHELLMCP_TRANSPORT", "stdio")
	args := []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", probe,
		"-Isolated", "-System", "-NoStart", "-InstallDir", filepath.Join(t.TempDir(), "green"),
		"-TaskName", "gptadmin-shellmcp-ci-green", "-ShellmcpName", "CI-isolated-green",
		"-ShellmcpPort", "55031", "-HubUrl", "https://example.test",
		"-LatestBuild", "1156", "-PackageUrl", filepath.Join(t.TempDir(), "explicit-candidate.zip")}
	if out, err := exec.Command("powershell.exe", args...).CombinedOutput(); err != nil {
		t.Fatalf("isolated setup inherited Blue settings: %v %s", err, out)
	}
}
