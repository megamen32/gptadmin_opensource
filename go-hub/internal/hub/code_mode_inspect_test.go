package hub

import (
	"net/http"
	"testing"
)

func TestCodeModeInspectOneToolAndReuseWithinRun(t *testing.T) {
	s, r := codeModeFixture(t)
	got, status := codeModeRun(t, s, r, map[string]any{"code": `
const first=await mcp.inspect('hub','demo');
first.description='caller mutation';
const second=await mcp.inspect('hub','demo');
const value=await call('hub','demo',{});
return {tool:second.tool, schema:typeof second.inputSchema, catalog:('tools' in second), mutated:second.description==='caller mutation', ok:value.status};`})
	if status != http.StatusOK {
		t.Fatalf("inspect failed: %d %#v", status, got)
	}
	value := mapValue(got["result"])
	if value["tool"] != "demo" || value["schema"] != "object" || value["catalog"] != false || value["mutated"] != false || value["ok"] != "ok" {
		t.Fatalf("inspect returned unrelated catalog or mutable schema: %#v", got)
	}
	reads := 0
	for _, item := range outputRows(got["tool_calls"]) {
		if firstString(mapValue(item), "name") == "schema" {
			reads++
		}
	}
	if reads != 1 {
		t.Fatalf("repeated inspect fetched %d catalogs, want one: %#v", reads, got)
	}
}

func TestCodeModeInspectRequiresExactVisibleTool(t *testing.T) {
	for _, code := range []string{`return mcp.inspect('hub');`, `return mcp.inspect('hub','does_not_exist');`} {
		s, r := codeModeFixture(t)
		got, status := codeModeRun(t, s, r, map[string]any{"code": code})
		if status == http.StatusOK {
			t.Fatalf("invalid inspect returned a catalog: %#v", got)
		}
	}
}

func TestCodeModeInspectPreservesTargetPolicy(t *testing.T) {
	s, r := codeModeFixture(t)
	r = requestWithAccessProfile(r, AccessProfile{ID: "inspect-denied", AccessMode: "full", AllowedTargets: []string{"shell:elsewhere"}})
	got, status := codeModeRun(t, s, r, map[string]any{"code": `return mcp.inspect('hub','demo');`})
	if status == http.StatusOK {
		t.Fatalf("inspect bypassed target policy: %#v", got)
	}
}

func TestCodeModeInspectPreservesToolPolicy(t *testing.T) {
	s, r := codeModeFixture(t)
	r = requestWithAccessProfile(r, AccessProfile{ID: "inspect-tool-denied", AccessMode: "full", AllowedTargets: []string{"hub"}, AllowedTools: []string{"exec", "schema", "handover_status"}})
	got, status := codeModeRun(t, s, r, map[string]any{"code": `return mcp.inspect('hub','demo');`})
	if status == http.StatusOK {
		t.Fatalf("inspect bypassed tool policy: %#v", got)
	}
}

func TestCodeModeInspectRetainsOptionalContract(t *testing.T) {
	s, r := codeModeFixture(t)
	s.cfg.SchemaContractValidation = true
	got, status := codeModeRun(t, s, r, map[string]any{"code": `const s=mcp.inspect('hub','demo');return {version:s.schema_version,digest:s.schema_digest_sha256,tool:s.tool};`})
	value := mapValue(got["result"])
	if status != http.StatusOK || firstString(value, "version") != schemaContractVersion || len(firstString(value, "digest")) != 64 || value["tool"] != "demo" {
		t.Fatalf("inspect lost contract identity: %d %#v", status, got)
	}
}

func TestCodeModeInspectRechecksRevokedGrantWithinRun(t *testing.T) {
	var host *codeModeShellHost
	host = startCodeModeShellHost(t, func(_ string, _ map[string]any) (any, error) {
		_, err := host.server.updateAccessProfiles(func(profiles map[string]AccessProfile) error {
			p := profiles["inspect-revoked"]
			p.AllowedTools = []string{"exec", "schema", "shell_exec"}
			p.Version++
			profiles[p.ID] = p
			return nil
		})
		return codeModeShellResult(map[string]any{"stdout": "revoked", "returncode": 0}), err
	})
	s := host.server
	_, err := s.updateAccessProfiles(func(profiles map[string]AccessProfile) error {
		profiles["inspect-revoked"] = AccessProfile{ID: "inspect-revoked", Version: 1, AccessMode: "full", AllowedTargets: []string{"hub", "shell:fixture"}, AllowedTools: []string{"exec", "schema", "demo", "shell_exec"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	claims := map[string]any{"sub": "inspect-client", "client_id": "inspect-client", "profile_id": "inspect-revoked", "scope": "gptadmin.read gptadmin.exec", "access_mode": "full"}
	r := requestWithAuthClaims(codeModeHostRequest(t, host), claims)
	r = s.applyAccessProfileContext(r, claims)
	got, status := codeModeRun(t, s, r, map[string]any{"code": `mcp.inspect('hub','demo');await call('shell:fixture','shell_exec',{cmd:'no-side-effect'});return mcp.inspect('hub','demo');`})
	if status == http.StatusOK || firstString(mapValue(got["error"]), "code") != "forbidden" {
		t.Fatalf("cached inspect ignored revoked grant: %d %#v", status, got)
	}
}
