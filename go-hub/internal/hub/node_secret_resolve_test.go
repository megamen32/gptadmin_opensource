package hub

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNodeSecretResolveRemoteOwner(t *testing.T) {
	writer, member, token, _ := bearerAdmissionNodes(t)
	request := httptest.NewRequest(http.MethodPost, "https://member.example/mcp", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	if !member.mcpAuth(httptest.NewRecorder(), request) {
		t.Fatal("member rejected caller")
	}
	secret, ingress, err := writer.secretStore.CreateRequest(secretOwnerFingerprint(request), "fixture", "TEST_SECRET", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.secretStore.ConsumeRequest(ingress, "synthetic-node-secret"); err != nil {
		t.Fatal(err)
	}
	args, values, err := member.resolveSecretEnvForRequest(request, "shell:member-a", map[string]any{"secret_env": map[string]any{"TEST_SECRET": secret.Ref}})
	if err != nil {
		t.Fatalf("remote secret resolution: %v", err)
	}
	// Ready references retain the same ingress-TTL semantics on both nodes.
	writer.secretStore.mu.Lock()
	stored := writer.secretStore.requests[secret.Ref]
	stored.ExpiresAt = time.Now().Add(-time.Minute).UnixNano()
	writer.secretStore.requests[secret.Ref] = stored
	writer.secretStore.mu.Unlock()
	if _, err := writer.secretStore.Resolve(secret.Ref, secretOwnerFingerprint(request)); err != nil {
		t.Fatal("local ready TTL changed", err)
	}
	if _, _, err := member.resolveSecretEnvForRequest(request, "shell:member-a", map[string]any{"secret_env": map[string]any{"TEST_SECRET": secret.Ref}}); err != nil {
		t.Fatal("remote ready TTL changed", err)
	}
	if _, _, err := member.resolveSecretEnvForRequest(request, "shell:other", map[string]any{"secret_env": map[string]any{"TEST_SECRET": secret.Ref}}); err == nil {
		t.Fatal("nonlocal target resolved")
	}
	if mapValue(args["env"])["TEST_SECRET"] != "synthetic-node-secret" || len(values) != 1 || values[0] != "synthetic-node-secret" {
		t.Fatal("secret or redaction state missing")
	}
	args["cmd"] = "printenv TEST_SECRET"
	result := member.callShellToolWithTraceParentAndSecrets("shell:member-a", "shell_exec", args, true, time.Second, "", "", values)
	id := firstString(result, "job_id")
	if id == "" {
		t.Fatalf("job not queued: %v", result)
	}
	records, err := member.readTaskRecords(id)
	if err != nil {
		t.Fatal(err)
	}
	record := records.Shell[id]
	encoded, _ := json.Marshal(record)
	if len(record.SealedPending) == 0 || bytes.Contains(encoded, []byte(values[0])) {
		t.Fatal("persisted input is missing or exposed")
	}
	for _, path := range []string{member.taskDatabasePath(), member.taskDatabasePath() + "-wal", member.taskDatabasePath() + "-journal"} {
		b, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte(values[0])) {
			t.Fatal("plaintext in durable task storage")
		}
	}
	member.mu.Lock()
	job := member.shellJobs[id]
	job.Status = "completed"
	job.Result = map[string]any{"stdout": values[0]}
	member.mu.Unlock()
	if strings.Contains(string(mustMarshalForSecretTest(t, shellJobResponse(job))), values[0]) {
		t.Fatal("job response leaked secret")
	}
	member.mu.Lock()
	oldURL := member.nodeIdentity.WriterURL
	member.nodeIdentity.WriterURL = "http://127.0.0.1:1"
	member.mu.Unlock()
	_, _, err = member.resolveSecretEnvForRequest(request, "shell:member-a", map[string]any{"secret_env": map[string]any{"TEST_SECRET": secret.Ref}})
	member.mu.Lock()
	member.nodeIdentity.WriterURL = oldURL
	member.mu.Unlock()
	if err == nil {
		t.Fatal("unavailable writer did not fail closed")
	}
}

func TestNodeSecretResolveHTTPRejectsUnauthorized(t *testing.T) {
	writer, member, token, nodeKey := bearerAdmissionNodes(t)
	req := httptest.NewRequest("POST", "https://member.example/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if !member.mcpAuth(httptest.NewRecorder(), req) {
		t.Fatal("caller authentication")
	}
	secret, ingress, err := writer.secretStore.CreateRequest(secretOwnerFingerprint(req), "fixture", "TEST_SECRET", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.secretStore.ConsumeRequest(ingress, "synthetic-node-secret"); err != nil {
		t.Fatal(err)
	}
	foreign, foreignIngress, err := writer.secretStore.CreateRequest(secretOwnerFingerprintValue("claim:foreign"), "foreign", "TEST_SECRET", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.secretStore.ConsumeRequest(foreignIngress, "synthetic-foreign-secret"); err != nil {
		t.Fatal(err)
	}
	call := func(input nodeSecretResolveRequest, key string) (int, string, string) {
		t.Helper()
		body, _ := json.Marshal(input)
		request, err := http.NewRequest("POST", member.nodeIdentity.WriterURL+nodeSecretResolvePath, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+key)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		b, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(b), response.Header.Get("Cache-Control")
	}
	input := nodeSecretResolveRequest{Token: token, Target: "shell:member-a", Ref: secret.Ref, Args: map[string]any{"cmd": "true"}}
	status, _, cache := call(input, nodeKey)
	if status != 200 || cache != "no-store" {
		t.Fatalf("positive HTTP status %d cache %q", status, cache)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*nodeSecretResolveRequest)
		key    string
	}{
		{"foreign owner", func(i *nodeSecretResolveRequest) { i.Ref = foreign.Ref }, nodeKey},
		{"invalid caller", func(i *nodeSecretResolveRequest) { i.Token += "invalid" }, nodeKey},
		{"member target mismatch", func(i *nodeSecretResolveRequest) { i.Target = "shell:other" }, nodeKey},
		{"invalid member", func(i *nodeSecretResolveRequest) {}, "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			altered := input
			tc.mutate(&altered)
			code, body, cache := call(altered, tc.key)
			if code < 400 || strings.Contains(body, "synthetic-") || cache != "no-store" {
				t.Fatalf("unexpected response %d", code)
			}
		})
	}
	for _, kind := range []string{"disabled", "expired", "no executor"} {
		t.Run(kind, func(t *testing.T) {
			writer.membershipMu.Lock()
			entry := writer.membership.Nodes[member.nodeIdentity.NodeID]
			original := *entry
			switch kind {
			case "disabled":
				entry.State = nodeStateDisabled
			case "expired":
				entry.LeaseExpires = time.Now().Add(-time.Minute)
			case "no executor":
				entry.Capabilities = []string{"replica"}
			}
			writer.membershipMu.Unlock()
			code, body, _ := call(input, nodeKey)
			writer.membershipMu.Lock()
			*entry = original
			writer.membershipMu.Unlock()
			if code != http.StatusForbidden || strings.Contains(body, "synthetic-") {
				t.Fatalf("unexpected response %d", code)
			}
		})
	}
	// Profiles are checked separately from the enrolled member's target match.
	for _, tc := range []struct {
		name           string
		targets, tools []string
	}{
		{"denied target", []string{"shell:other"}, []string{"shell_exec"}},
		{"denied tool", []string{"shell:member-a"}, []string{"system_inspect"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := writer.updateAccessProfiles(func(profiles map[string]AccessProfile) error {
				profiles["fixture"] = AccessProfile{ID: "fixture", AccessMode: accessModeFull, ApprovalMode: approvalModeUnrestricted, AllowedTargets: tc.targets, AllowedTools: tc.tools, Version: 1, UpdatedAt: time.Now().UTC()}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			code, body, _ := call(input, nodeKey)
			if code != http.StatusForbidden || strings.Contains(body, "synthetic-") {
				t.Fatalf("profile bypass status %d", code)
			}
		})
	}
}
