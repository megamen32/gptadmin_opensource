package hub

import (
	"fmt"
	"strings"
)

// Output detail is presentation-only: never include it in execution arguments,
// idempotency fingerprints, persisted results, or audit records.
func validateToolOutputDetail(detail any) error {
	if detail == nil {
		return nil
	}
	if value, ok := detail.(string); ok && (value == "" || value == "compact" || value == "full") {
		return nil
	}
	return fmt.Errorf("detail must be compact or full")
}

func toolOutputDetailSchema() map[string]any {
	return map[string]any{"type": "string", "enum": []string{"compact", "full"}}
}

func (s *Server) formatToolOutput(raw map[string]any, tool string, detail any) map[string]any {
	full := detail == "full"
	if detail == nil || detail == "" {
		s.mu.Lock()
		full = s.hubSettingBoolLocked("tool_output_verbose")
		s.mu.Unlock()
	}
	if full {
		return raw
	}
	return compactToolOutput(raw, tool)
}

// Only reshape envelopes owned by GPTAdmin. Never recursively strip keys from
// an upstream tool's business data, errors, content blocks, or resource links.
func compactToolOutput(raw map[string]any, tool string) map[string]any {
	id, target := firstString(raw, "job_id"), firstString(raw, "server_id")
	if id == "" || target == "" || firstString(raw, "status") == "" {
		return raw
	}
	out := cloneMap(raw)
	for _, key := range []string{"server_id", "trace_id", "traceparent"} {
		delete(out, key)
	}
	if out["task_id"] == id {
		delete(out, "task_id")
	}
	if status := firstString(out, "status"); status == "running" || status == "queued" {
		delete(out, "background")
	}
	switch firstString(out, "message") {
	case "shell job queued", "shell job is still running", "MCP relay job is still running":
		delete(out, "message")
	}
	if !strings.HasPrefix(target, "shell:") {
		return out
	}
	response := mapValue(raw["response"])
	structured := mapValue(response["structuredContent"])
	// This is precisely the wrapper produced by shellJobResponse, not a generic
	// MCP result. The upstream result beneath it remains opaque except shell_exec.
	if firstString(structured, "server") != strings.TrimPrefix(target, "shell:") {
		return out
	}
	value, ok := structured["result"]
	if !ok {
		return out
	}
	delete(out, "response")
	out["result"] = value
	if resources, exists := structured["resources"]; exists {
		out["resources"] = resources
	}
	if tool != "shell_exec" {
		return out
	}
	result, ok := value.(map[string]any)
	if !ok {
		return out
	}
	result = cloneMap(result)
	delete(result, "cwd_effective")
	delete(result, "duration_ms")
	_, external := structured["resources"]
	for _, stream := range []string{"stdout", "stderr"} {
		if result[stream] == "" {
			delete(result, stream)
		}
		if !external && !truthyAny(result[stream+"_truncated"]) {
			delete(result, stream+"_bytes")
		}
		if result[stream+"_truncated"] == false {
			delete(result, stream+"_truncated")
		}
	}
	switch result["returncode"] {
	case 0, int64(0), float64(0):
		delete(result, "returncode")
	}
	// An unexpected payload key must never overwrite Hub status or recovery IDs.
	for key := range result {
		if _, conflict := out[key]; conflict {
			out["result"] = result
			return out
		}
	}
	delete(out, "result")
	for key, value := range result {
		out[key] = value
	}
	return out
}
