package hub

// exec is the public sandbox entry. Scripts search the caller-visible
// catalog and call tools through the ordinary authorized dispatch path.

import (
	"net/http"
)

// codeModeInputSchema is the published contract. Extra properties are refused
// so a typo fails loudly instead of silently disabling a budget.
func codeModeInputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"code": map[string]any{
				"type":        "string",
				"minLength":   1,
				"maxLength":   codeModeMaxCodeBytes,
				"description": "Short async JavaScript body. Top-level await, call/search, mcp.call/search/schema/discover/job/expand and files.* are available. Explicitly return only the chosen result. File helpers call authorized file:<host> MCP tools. No host filesystem, environment, process, network or timers.",
			},
			"context": map[string]any{"type": "string", "description": "Instruction context returned by search when required."},
			"timeout_ms": map[string]any{
				"type": "integer", "minimum": 100,
				"maximum":     int(codeModeMaxTimeout / 1e6),
				"default":     int(codeModeDefaultTimeout / 1e6),
				"description": "Wall-clock budget for the whole run.",
			},
			"max_calls": map[string]any{
				"type": "integer", "minimum": 1, "maximum": codeModeMaxCallsCeiling,
				"default":     codeModeDefaultMaxCalls,
				"description": "Maximum mcp.search + mcp.call invocations.",
			},
			"max_output_bytes": map[string]any{
				"type": "integer", "minimum": 256, "maximum": codeModeMaxOutBytes,
				"default":     codeModeDefaultOutBytes,
				"description": "Maximum combined serialized size of the returned value and console.log entries.",
			},
		},
		"required":             []string{"code"},
		"additionalProperties": false,
	}
}

func codeModeOutputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"status":     map[string]any{"type": "string", "enum": []string{"completed", "failed"}},
			"result":     map[string]any{"description": "Exactly the value the script returned."},
			"logs":       map[string]any{"type": "array", "items": map[string]any{"type": "array", "items": map[string]any{}}, "description": "Structured console.log arguments, included in the combined output budget."},
			"calls":      map[string]any{"type": "integer", "minimum": 0},
			"elapsed_ms": map[string]any{"type": "integer", "minimum": 0},
			"limits": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"timeout_ms":       map[string]any{"type": "integer"},
					"max_calls":        map[string]any{"type": "integer"},
					"heap_bytes":       map[string]any{"type": "integer"},
					"max_output_bytes": map[string]any{"type": "integer"},
				},
				"additionalProperties": true,
			},
			"error": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"code":            map[string]any{"type": "string"},
					"message":         map[string]any{"type": "string"},
					"calls_completed": map[string]any{"type": "integer", "minimum": 0},
				},
				"required": []string{"code", "message"},
			},
		},
		"required":             []string{"status", "calls"},
		"additionalProperties": true,
	}
}

// callCodeModeTool runs one script for an authenticated caller.
//
// code_mode is a hub tool, so the caller has already passed authorizeToolCall,
// approvalGate, boundedAutonomousGate, admission drain, idempotency and audit
// inside executeMCPTool before this runs. Each nested mcp.call repeats that
// same path with the same request, so no call can exceed the caller's rights.
func (s *Server) callCodeModeTool(r *http.Request, args map[string]any) (map[string]any, int) {
	// code_mode is reachable from more than one caller (the execute facade, the
	// Hub bridge, a direct hub tool dispatch). Each nested call re-checks the
	// caller's policy, so the tool's own entry must also clear the caller's
	// target/tool authorization and read-only boundary before any script runs.
	if r != nil {
		if err := authorizeToolCall(r, "hub", codeModeTool); err != nil {
			return codeModeFailed("forbidden", err.Error(), 0), http.StatusForbidden
		}
	}
	code := firstString(args, "code")
	if code == "" {
		return codeModeFailed("invalid_arguments", "code is required", 0), http.StatusBadRequest
	}
	limits, err := resolveCodeModeLimits(args)
	if err != nil {
		return codeModeFailed("invalid_arguments", err.Error(), 0), http.StatusBadRequest
	}
	if r == nil {
		// No request means no caller identity. Fail closed instead of running a
		// script with a synthesized privileged context.
		return codeModeFailed("unauthenticated", "code_mode requires an authenticated caller", 0), http.StatusUnauthorized
	}
	run, runErr := s.runCodeMode(r.Context(), r, code, limits)
	if runErr != nil {
		return codeModeFailedWith(runErr.code, runErr.Error(), runErr.call, runErr.receipt), codeModeStatusFor(runErr.code)
	}
	return map[string]any{
		"status":     "completed",
		"result":     run.result,
		"logs":       run.logs,
		"calls":      run.calls,
		"elapsed_ms": run.elapsed.Milliseconds(),
		"limits": map[string]any{
			"timeout_ms":       run.limits.timeout.Milliseconds(),
			"max_calls":        run.limits.maxCalls,
			"heap_bytes":       run.limits.heap,
			"max_output_bytes": run.limits.maxOut,
		},
	}, http.StatusOK
}

func codeModeFailed(code, message string, calls int) map[string]any {
	return codeModeFailedWith(code, message, calls, nil)
}

// codeModeFailedWith keeps the original bounded receipt of the first
// non-completing call, so a pending approval id or a job id survives the failure
// and the caller can act on it instead of guessing.
func codeModeFailedWith(code, message string, calls int, receipt map[string]any) map[string]any {
	failure := map[string]any{
		"code":            code,
		"message":         message,
		"calls_completed": calls,
	}
	if receipt != nil {
		failure["receipt"] = receipt
	}
	return map[string]any{
		"status": "failed",
		"calls":  calls,
		"error":  failure,
	}
}

// codeModeStatusFor keeps failures non-2xx so no caller can mistake a failed
// script for a completed one. A limit or policy stop is a client-visible budget
// condition (429), a bad request is 400, and a runtime fault is 500.
func codeModeStatusFor(code string) int {
	switch code {
	case "invalid_arguments", "invalid_return", "unknown_tool":
		return http.StatusBadRequest
	case "recursive_call_denied", "capability_denied", "forbidden":
		return http.StatusForbidden
	case "timeout", "memory_limit", "stack_limit", "call_limit_exceeded", "busy", "output_too_large":
		return http.StatusTooManyRequests
	case "unauthenticated", "reference_unavailable", "approval_required", "target_unavailable", "discover_unavailable", "schema_unavailable", "cancelled":
		return http.StatusForbidden
	case "run_stopped", "nonterminal":
		return http.StatusConflict
	case "script_error":
		return http.StatusBadRequest
	case "unavailable", "runtime_unavailable":
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}
