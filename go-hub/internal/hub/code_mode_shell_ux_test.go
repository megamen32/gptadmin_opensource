package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func codeModeQuotedShellHost(t *testing.T) *codeModeShellHost {
	t.Helper()
	// Queue through the real Hub-to-ShellMCP receipt. A synthetic local agent
	// runs argv directly, while explicit cmd retains the POSIX shell path.
	return startCodeModeShellHost(t, func(tool string, args map[string]any) (any, error) {
		if tool != "shell_exec" {
			return nil, fmt.Errorf("unexpected tool %q", tool)
		}
		var run *exec.Cmd
		if _, native := args["argv"]; native {
			items := outputRows(args["argv"])
			argv := make([]string, 0, len(items))
			for _, item := range items {
				value, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("native argv has nonstring entry")
				}
				argv = append(argv, value)
			}
			if len(argv) == 0 {
				return nil, fmt.Errorf("native argv cannot be empty")
			}
			run = exec.Command(argv[0], argv[1:]...)
		} else {
			run = exec.Command("sh", "-c", firstString(args, "cmd"))
		}
		if source, ok := args["stdin"].(string); ok {
			run.Stdin = strings.NewReader(source)
		}
		output, err := run.Output()
		code := 0
		stderr := ""
		if err != nil {
			exit, ok := err.(*exec.ExitError)
			if !ok {
				return nil, err
			}
			code, stderr = exit.ExitCode(), string(exit.Stderr)
		}
		return codeModeShellResult(map[string]any{
			"stdout": string(output), "stderr": stderr, "returncode": code,
			"cwd_effective": "/tmp", "duration_ms": 4,
		}), nil
	})
}

func TestCodeModeShellArgvPreservesQuotesAndAvoidsExpansion(t *testing.T) {
	host := codeModeQuotedShellHost(t)
	r := codeModeHostRequest(t, host)
	marker := filepath.Join(t.TempDir(), "injection-should-not-run")
	tick := string(rune(96))
	original := "one ' quote; two \" quotes; backslash \\\\; JSON {\"a\":1}; Привет 🌍\n" +
		"dollars $HOME $(touch " + marker + "); " + tick + "uname" + tick + " && echo BAD"
	argv, _ := json.Marshal([]string{"printf", "%s", original})
	script := "return await mcp.shell('shell:fixture'," + string(argv) + ");"
	result, status := codeModeRun(t, host.server, r, map[string]any{"code": script, "timeout_ms": 10000})
	if status != http.StatusOK {
		t.Fatalf("shell helper failed: status=%d result=%v", status, result)
	}
	got := mapValue(result["result"])
	if got["stdout"] != original || got["ok"] != true || intFromAny(got["exit_code"]) != 0 {
		t.Fatalf("original arguments were not preserved: %#v", got)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("command substitution ran instead of remaining literal: %v", err)
	}
	calls := host.agent.callsFor("shell_exec")
	if len(calls) != 1 {
		t.Fatalf("expected one native argv call, got %#v", calls)
	}
	params := mapValue(calls[0]["args"])
	if _, exists := params["cmd"]; exists {
		t.Fatalf("native argv was rewritten into a Bash command: %#v", params)
	}
	if argv := outputRows(params["argv"]); len(argv) != 3 || argv[2] != original {
		t.Fatalf("arguments not forwarded exactly: %#v", params)
	}
	for _, key := range []string{"cwd_effective", "duration_ms", "run_as_user"} {
		if _, present := got[key]; present {
			t.Fatalf("compact shell leaked transport metadata %s: %#v", key, got)
		}
	}
	if _, present := result["limits"]; present {
		t.Fatalf("compact Code Mode still returned limits: %#v", result)
	}
	if _, present := result["logs"]; present {
		t.Fatalf("compact Code Mode still returned empty logs: %#v", result)
	}
}

func TestCodeModeShellRawScriptFullAndVerbose(t *testing.T) {
	host := codeModeQuotedShellHost(t)
	r := codeModeHostRequest(t, host)
	tick := string(rune(96))
	script := "const cmd=String.raw" + tick + "printf '%s\\n' \"hello 'world'\"" + tick + ";return await mcp.shell('shell:fixture',cmd,{full:true});"
	response, status := codeModeRun(t, host.server, r, map[string]any{
		"code": script, "timeout_ms": 10000, "verbose": true,
	})
	if status != http.StatusOK {
		t.Fatalf("raw Bash source failed: %d %v", status, response)
	}
	value := mapValue(response["result"])
	if value["stdout"] != "hello 'world'\n" || intFromAny(value["returncode"]) != 0 {
		t.Fatalf("raw string incorrectly interpreted: %#v", value)
	}
	if _, present := value["cwd_effective"]; !present {
		t.Fatalf("full:true lost metadata: %#v", value)
	}
	if _, present := response["limits"]; !present {
		t.Fatal("verbose:true must retain limits")
	}
	if _, present := response["logs"]; !present {
		t.Fatal("verbose:true must retain the logs field")
	}
}

func TestCodeModeShellExitCodeAndOptions(t *testing.T) {
	host := codeModeQuotedShellHost(t)
	r := codeModeHostRequest(t, host)
	good, status := codeModeRun(t, host.server, r, map[string]any{
		"code":       "return await mcp.shell('shell:fixture',{argv:['printf','%s','x \"quoted\"'],cwd:'/tmp',timeout:12});",
		"timeout_ms": 10000,
	})
	if status != http.StatusOK || firstString(mapValue(good["result"]), "stdout") != "x \"quoted\"" {
		t.Fatalf("object argv failed: %d %#v", status, good)
	}
	calls := host.agent.callsFor("shell_exec")
	if len(calls) != 1 || firstString(mapValue(calls[0]["args"]), "cwd") != "/tmp" || intFromAny(mapValue(calls[0]["args"])["timeout"]) != 12 {
		t.Fatalf("shell options missing: %#v", calls)
	}
	failed, status := codeModeRun(t, host.server, r, map[string]any{
		"code":       "return await mcp.shell('shell:fixture',['sh','-c','echo failed >&2;exit 7']);",
		"timeout_ms": 10000,
	})
	value := mapValue(failed["result"])
	if status != http.StatusOK || value["ok"] != false || intFromAny(value["exit_code"]) != 7 || !strings.Contains(firstString(value, "stderr"), "failed") {
		t.Fatalf("nonzero shell exit concealed: %d %#v", status, failed)
	}
}

func TestCodeModeShellRejectsInvalidWithoutDispatch(t *testing.T) {
	host := codeModeQuotedShellHost(t)
	r := codeModeHostRequest(t, host)
	attempts := []string{
		"await mcp.shell('hub',['printf','hello'])",
		"await mcp.shell('shell:fixture',[])",
		"await mcp.shell('shell:fixture',['printf',12])",
		"await mcp.shell('shell:fixture',['printf','x\\u0000y'])",
		"await mcp.shell('shell:fixture',{argv:['printf','hi'],cmd:'pwd'})",
		"await mcp.shell('shell:fixture','pwd',{unknown:'x'})",
		"await mcp.shell('shell:fixture','echo a',{argv:['echo','b']})",
	}
	for _, attempt := range attempts {
		script := "try{" + attempt + ";return 'bad'}catch(e){return e.message;}"
		result, status := codeModeRun(t, host.server, r, map[string]any{"code": script})
		if status != http.StatusOK || firstString(result, "result") == "bad" || firstString(result, "result") == "" {
			t.Fatalf("invalid argument was accepted: %q => %#v", attempt, result)
		}
	}
	if calls := host.agent.callCount(); calls != 0 {
		t.Fatalf("invalid input dispatched calls: %d", calls)
	}
}

func TestCodeModeShellCannotBypassProfileOrReadonlyCeiling(t *testing.T) {
	host := codeModeQuotedShellHost(t)
	r := codeModeHostRequest(t, host)
	restricted := requestWithAccessProfile(r, AccessProfile{
		ID: "exec-only", Version: 1, AccessMode: accessModeFull,
		ApprovalMode:   approvalModeUnrestricted,
		AllowedTargets: []string{"hub"}, AllowedTools: []string{"exec"},
	})
	result, status := codeModeRun(t, host.server, restricted, map[string]any{
		"code": "return await mcp.shell('shell:fixture',['printf','denied']);", "timeout_ms": 5000,
	})
	if status == http.StatusOK || firstString(mapValue(result["error"]), "code") != "forbidden" {
		t.Fatalf("profile bypassed: %d %#v", status, result)
	}
	readonly, status := host.server.callCodeModeToolMode(r, map[string]any{
		"code": "return await mcp.shell('shell:fixture',['printf','denied']);", "timeout_ms": 5000,
	}, true)
	if status == http.StatusOK || firstString(mapValue(readonly["error"]), "code") != "forbidden" {
		t.Fatalf("read-only bypassed: %d %#v", status, readonly)
	}
	if calls := host.agent.callCount(); calls != 0 {
		t.Fatalf("forbidden calls reached executor: %d", calls)
	}
}

func TestCodeModeVerboseValidation(t *testing.T) {
	s, r := codeModeFixture(t)
	defer s.Close()
	result, status := codeModeRun(t, s, r, map[string]any{"code": "return 1;", "verbose": "yes"})
	if status != http.StatusBadRequest || firstString(mapValue(result["error"]), "code") != "invalid_arguments" {
		t.Fatalf("nonboolean verbose accepted: %d %#v", status, result)
	}
}
