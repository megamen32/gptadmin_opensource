package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCodeModeBlocksWASIHostTimers(t *testing.T) {
	s, r := codeModeFixture(t)
	if result, status := codeModeRun(t, s, r, map[string]any{"code": "return true;"}); status != http.StatusOK {
		t.Fatalf("warm-up failed: %v", result)
	}
	for name, code := range map[string]string{
		"sleep":       "const os=await import('qjs:os'); os.sleep(1500); return true;",
		"sleep_async": "const os=await import('qjs:os'); await os.sleepAsync(1500); return true;",
		"timer":       "const os=await import('qjs:os'); await new Promise(resolve=>os.setTimeout(resolve,1500)); return true;",
	} {
		t.Run(name, func(t *testing.T) {
			started := time.Now()
			result, status := codeModeRun(t, s, r, map[string]any{"code": code, "timeout_ms": 300})
			elapsed := time.Since(started)
			if status != http.StatusForbidden || firstString(mapValue(result["error"]), "code") != "capability_denied" {
				t.Fatalf("host timer not denied: status=%d result=%v", status, result)
			}
			if elapsed > time.Second {
				t.Fatalf("host timer blocked the caller for %s", elapsed)
			}
			t.Logf("host timer refused in %s", elapsed)
		})
	}
	result, status := codeModeRun(t, s, r, map[string]any{"code": "return await Promise.resolve({healthy:true});"})
	if status != http.StatusOK || mapValue(result["result"])["healthy"] != true {
		t.Fatalf("fresh runtime broken after denial: %v", result)
	}
}

func TestCodeModeBuiltinModulesCannotReadHostFilesOrEnvironment(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("CODE_MODE_PRIVATE_TEST_MARKER", "nonsecret-own-canary")
	path := filepath.Join(dir, "marker.txt")
	if err := os.WriteFile(path, []byte("nonsecret-own-file-canary"), 0600); err != nil {
		t.Fatal(err)
	}
	quoted, _ := json.Marshal(path)
	s, r := codeModeFixture(t)
	code := "const std=await import('qjs:std'); return {relative:std.loadFile('marker.txt'),absolute:std.loadFile(" + string(quoted) + "),env:std.getenv('CODE_MODE_PRIVATE_TEST_MARKER')??null};"
	result, status := codeModeRun(t, s, r, map[string]any{"code": code})
	if status != http.StatusOK {
		t.Fatalf("filesystem denial probe failed: %v", result)
	}
	values := mapValue(result["result"])
	for _, key := range []string{"relative", "absolute", "env"} {
		if v, exists := values[key]; !exists || v != nil {
			t.Fatalf("host %s reachable: %v", key, values)
		}
	}
}

func TestCodeModeReadHelpersHonorProfile(t *testing.T) {
	s, r := codeModeFixture(t)
	r = requestWithAccessProfile(r, AccessProfile{ID: "script-only", Version: 1, AccessMode: accessModeFull, ApprovalMode: approvalModeUnrestricted, AllowedTargets: []string{"hub"}, AllowedTools: []string{"exec"}})
	for _, code := range []string{"return mcp.discover();", "return mcp.schema('hub','status');"} {
		result, status := codeModeRun(t, s, r, map[string]any{"code": code})
		if status == http.StatusOK || firstString(mapValue(result["error"]), "code") != "forbidden" {
			t.Fatalf("read helper bypassed tool policy: %v", result)
		}
	}
}

func TestCodeModeSchemaListContainsToolNames(t *testing.T) {
	s, r := codeModeFixture(t)
	result, status := codeModeRun(t, s, r, map[string]any{"code": "const s=await mcp.search('exec'); return {hasCodeExec:s.results.some(x=>x.name==='exec'),hasEmpty:s.results.some(x=>x.name==='')};"})
	value := mapValue(result["result"])
	if status != http.StatusOK || value["hasCodeExec"] != true || value["hasEmpty"] != false {
		t.Fatalf("invalid schema names: %v", result)
	}
}

func TestCodeModeRelaySchemaHonorsRequestDeadline(t *testing.T) {
	s, r := codeModeFixture(t)
	s.cfg.DefaultTimeout = 3 * time.Second
	s.agents["code-mode-relay-canary"] = &Agent{AgentID: "code-mode-relay-canary", Name: "canary", Status: "online", Meta: map[string]any{"approved": true}}
	ctx, cancel := context.WithTimeout(r.Context(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	result := mapValue(s.appsSDKSchemaForRequest(r.WithContext(ctx), map[string]any{"target": "code-mode-relay-canary"}))
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("schema exceeded caller deadline: %s", elapsed)
	}
	if firstString(result, "status") != "running" || firstString(result, "job_id") == "" {
		t.Fatalf("schema did not preserve pending receipt: %v", result)
	}
}

func TestCodeModeFailurePreservesStringError(t *testing.T) {
	result := codeModeCallPayload(map[string]any{"status": "failed", "error": "target is not allowed by this profile"}, http.StatusForbidden, "shell:denied", "shell_exec")
	if result["ok"] != false || firstString(result, "detail") != "target is not allowed by this profile" {
		t.Fatalf("denial detail lost: %v", result)
	}
}

func TestCodeModeRoundTripsJSONReturnTypes(t *testing.T) {
	s, r := codeModeFixture(t)
	for _, literal := range []string{`[1,2,3]`, `{"text":"Привет 🌍","nested":[true,null,2]}`, `""`, `"hello"`, `false`, `null`, `42`, `0`} {
		t.Run(literal, func(t *testing.T) {
			result, status := codeModeRun(t, s, r, map[string]any{"code": "return " + literal + ";"})
			if status != http.StatusOK {
				t.Fatalf("return failed: %v", result)
			}
			encoded, err := json.Marshal(result["result"])
			if err != nil {
				t.Fatal(err)
			}
			var expected any
			if err := json.Unmarshal([]byte(literal), &expected); err != nil {
				t.Fatal(err)
			}
			canonical, _ := json.Marshal(expected)
			if string(encoded) != string(canonical) {
				t.Fatalf("return changed: got %s, want %s", encoded, canonical)
			}
		})
	}
}
