package hub

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodeModeShellSendsMultilinePythonViaStdin(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	host := codeModeQuotedShellHost(t)
	request := codeModeHostRequest(t, host)
	marker := filepath.Join(t.TempDir(), "should-not-run")
	value := "Hello \"quoted\" and it's fine; Привет 🌍; $HOME; $(touch " + marker + ")"
	valueAsJSON, _ := json.Marshal(value)
	script := "print(" + string(valueAsJSON) + ")\n"
	sourceAsJSON, _ := json.Marshal(script)
	code := "return await mcp.shell('shell:fixture', ['python3','-'], {stdin:" + string(sourceAsJSON) + "});"
	body, status := codeModeRun(t, host.server, request, map[string]any{"code": code, "timeout_ms": 10000})
	if status != http.StatusOK {
		t.Fatalf("Code Mode script failed: %d %#v", status, body)
	}
	result := mapValue(body["result"])
	if result["stdout"] != value+"\n" || result["ok"] != true || intFromAny(result["exit_code"]) != 0 {
		t.Fatalf("stdin source was corrupted: %#v", result)
	}
	calls := host.agent.callsFor("shell_exec")
	if len(calls) != 1 {
		t.Fatalf("unexpected call count: %#v", calls)
	}
	args := mapValue(calls[0]["args"])
	if _, usedShell := args["cmd"]; usedShell {
		t.Fatalf("script was placed into Bash command: %#v", args)
	}
	if args["stdin"] != script || len(outputRows(args["argv"])) != 2 {
		t.Fatalf("stdin/argv lost across Hub: %#v", args)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("script interpolated by Bash: %v", err)
	}
}

func TestCodeModeShellValidatesStdinBeforeCallingTool(t *testing.T) {
	host := codeModeQuotedShellHost(t)
	request := codeModeHostRequest(t, host)
	cases := []string{
		"await mcp.shell('shell:fixture',['cat'],{stdin:5});",
		"await mcp.shell('shell:fixture',['cat'],{stdin:'bad\\u0000source'});",
	}
	for _, attempt := range cases {
		body, status := codeModeRun(t, host.server, request, map[string]any{"code": "try {" + attempt + "return \"bad\";} catch(e) {return e.message;}"})
		if status != http.StatusOK || strings.TrimSpace(firstString(body, "result")) == "" || firstString(body, "result") == "bad" {
			t.Fatalf("invalid stdin accepted: %#v", body)
		}
	}
	if host.agent.callCount() != 0 {
		t.Fatalf("invalid input reached agent: %d", host.agent.callCount())
	}
}
