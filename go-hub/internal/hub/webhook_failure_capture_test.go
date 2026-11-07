package hub

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWebhookFailureCaptureRedactsUntrustedOutput(t *testing.T) {
	result := map[string]any{"status": "completed", "response": map[string]any{"structuredContent": map[string]any{"result": map[string]any{"returncode": 1, "stderr": "RuntimeError: agent job profile is not allowlisted\n", "stdout": "{\"token\":\"secret-value\"}", "env": "secret-value", "cmd": "secret-value"}}}}
	got := webhookSafeShellFailure(result)
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "secret-value") {
		t.Fatalf("secret exposed: %s", b)
	}
	if !strings.Contains(string(b), "agent job profile is not allowlisted") || !strings.Contains(string(b), "returncode") {
		t.Fatalf("missing safe diagnosis: %s", b)
	}
	result["response"].(map[string]any)["structuredContent"].(map[string]any)["result"].(map[string]any)["stderr"] = "RuntimeError: agent job profile is not allowlisted secret-value"
	b, _ = json.Marshal(webhookSafeShellFailure(result))
	if strings.Contains(string(b), "secret-value") || strings.Contains(string(b), "allowlisted") {
		t.Fatalf("untrusted output exposed: %s", b)
	}
}
