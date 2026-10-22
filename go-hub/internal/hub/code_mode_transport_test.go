package hub

import (
	"strings"
	"testing"
)

func TestCodeModeNestedFileErrorIsNotSuccess(t *testing.T) {
	inner := map[string]any{"structuredContent": map[string]any{"server": "fixture", "result": map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "line id is stale; nothing was changed"}}}}}
	status, detail := codeModeCallTerminal(map[string]any{"status": "completed"}, inner, 200)
	if status != "failed" || !strings.Contains(detail, "stale") {
		t.Fatalf("status %s detail %s", status, detail)
	}
}
func TestCodeModeDoesNotInspectArbitraryBusinessResult(t *testing.T) {
	inner := map[string]any{"structuredContent": map[string]any{"result": map[string]any{"error": "ordinary business field"}}}
	if failed, _ := codeModeNestedTransportFailure(inner); failed {
		t.Fatal("arbitrary business result treated as protocol failure")
	}
}
