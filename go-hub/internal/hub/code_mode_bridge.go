package hub

// codeModeBridge is the entire host surface available to a Code Mode script.
//
// Every function here re-enters the ordinary Hub dispatch path with the
// caller's own *http.Request, in the same order execute uses:
//	authorizeToolCall -> selectMCPRelayTarget -> validateSchemaContract
//	-> executeMCPTool (connection owner, admission drain, approval,
//	   bounded autonomy, idempotency keys, audit)
// so a script can never reach a tool, target or effect the caller could not
// reach directly.
//
// No Go object, file handle, environment value or network handle is ever handed
// to the sandbox; arguments and results cross as plain JSON only.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fastschema/qjs"
)

// codeModeAPISource exposes the host bridge under one frozen `mcp` namespace.
// qjs only supports global binding, so the functions are bound to hidden
// globals first and then removed again.
const codeModeAPISource = `globalThis.mcp = Object.freeze({
  discover: globalThis.__codeModeDiscover,
  schema: globalThis.__codeModeSchema,
  call: globalThis.__codeModeCall,
});
delete globalThis.__codeModeDiscover;
delete globalThis.__codeModeSchema;
delete globalThis.__codeModeCall;
`

// codeModeBridge installs mcp.discover, mcp.schema and mcp.call into a sandbox.
type codeModeBridge struct {
	server   *Server
	request  *http.Request
	ctx      context.Context
	deadline time.Time
	limits   codeModeLimits

	mu        sync.Mutex
	callCount int
	// fatal is set once, by the first nested call that did not complete. Later
	// bridge calls are refused even if the script catches the exception, and the
	// whole run ends as failed. A script cannot acknowledge a failed or pending
	// call and keep producing side effects.
	fatal *codeModeFatal
}

// codeModeFatal is the bounded receipt of the first non-completing call.
type codeModeFatal struct {
	code    string
	message string
	receipt map[string]any
}

func newCodeModeBridge(s *Server, r *http.Request, ctx context.Context, deadline time.Time, limits codeModeLimits) *codeModeBridge {
	return &codeModeBridge{server: s, request: r, ctx: ctx, deadline: deadline, limits: limits}
}

func (b *codeModeBridge) calls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.callCount
}

func (b *codeModeBridge) fatalReceipt() *codeModeFatal {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.fatal
}

// fail records the first non-completing outcome. Later failures never overwrite
// the original receipt, so the caller always learns why the run stopped first.
func (b *codeModeBridge) fail(code, message string, receipt map[string]any) *codeModeError {
	b.mu.Lock()
	if b.fatal == nil {
		b.fatal = &codeModeFatal{code: code, message: message, receipt: receipt}
	}
	b.mu.Unlock()
	return codeModeFailure(code, message)
}

// install binds the bridge functions onto the sandbox global object.
func (b *codeModeBridge) install(ctx *qjs.Context) error {
	ctx.SetFunc("__codeModeDiscover", b.discover)
	ctx.SetFunc("__codeModeSchema", b.schema)
	ctx.SetFunc("__codeModeCall", b.call)
	if _, err := ctx.Eval("code_mode_api.js", qjs.Code(codeModeAPISource)); err != nil {
		return fmt.Errorf("cannot expose the code_mode API: %w", err)
	}
	return nil
}

// entry performs the checks every bridge function must pass before it may touch
// the Hub: the run is still alive, nothing has already failed, and budget
// remains.
func (b *codeModeBridge) entry() *codeModeError {
	b.mu.Lock()
	fatal := b.fatal
	count := b.callCount
	b.mu.Unlock()
	if fatal != nil {
		return codeModeFailure("run_stopped", fatal.message)
	}
	if err := b.ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return codeModeFailure("timeout", fmt.Sprintf("code_mode exceeded its %s budget", b.limits.timeout))
		}
		return codeModeFailure("cancelled", "code_mode was cancelled before it finished")
	}
	if count >= b.limits.maxCalls {
		// Stop-on-first-failure: the budget stop is sticky, so later calls stay
		// refused even if the script catches this exception.
		b.mu.Lock()
		if b.fatal == nil {
			b.fatal = &codeModeFatal{
				code:    "call_limit_exceeded",
				message: fmt.Sprintf("script exceeded the %d call limit", b.limits.maxCalls),
				receipt: map[string]any{"max_calls": b.limits.maxCalls},
			}
		}
		b.mu.Unlock()
		return &codeModeError{
			code:   "call_limit_exceeded",
			detail: fmt.Sprintf("script exceeded the %d call limit", b.limits.maxCalls),
			call:   count,
		}
	}
	b.mu.Lock()
	b.callCount++
	b.mu.Unlock()
	return nil
}

// remaining bounds one hop by what is left of the run, never by the full budget
// again, so a chain of calls cannot run for N x the run timeout.
func (b *codeModeBridge) remaining() (time.Duration, *codeModeError) {
	left := time.Until(b.deadline)
	if left <= 0 {
		return 0, codeModeFailure("timeout", fmt.Sprintf("code_mode exhausted its %s budget", b.limits.timeout))
	}
	if defaultTimeout := b.server.cfg.DefaultTimeout; defaultTimeout > 0 && left > defaultTimeout {
		left = defaultTimeout
	}
	return left, nil
}

// discover lists targets, optionally filtered by a substring query.
func (b *codeModeBridge) discover(this *qjs.This) (*qjs.Value, error) {
	if err := b.entry(); err != nil {
		return nil, err
	}
	if _, err := b.remaining(); err != nil {
		return nil, err
	}
	query := codeModeStringArg(this, 0)
	args := map[string]any{}
	if query != "" {
		args["query"] = query
	}
	request := b.request.WithContext(b.ctx)
	if err := authorizeFacadeCall(request, "discover", args); err != nil {
		return nil, b.fail("forbidden", err.Error(), nil)
	}
	result := mapValue(b.server.appsSDKCallRawForRequest(request, "discover", args))
	if firstString(result, "status") == "failed" {
		return nil, b.fail("discover_unavailable", firstString(result, "error", "detail"), nil)
	}
	servers := make([]any, 0, len(outputRows(result["servers"])))
	for _, item := range outputRows(result["servers"]) {
		row := mapValue(item)
		// The sandbox never receives hidden connection material.
		delete(row, "meta")
		if query != "" && !strings.Contains(strings.ToLower(firstString(row, "server_id", "name", "status")), strings.ToLower(query)) {
			continue
		}
		servers = append(servers, row)
	}
	return b.jsonValue(this, map[string]any{"servers": servers}), nil
}

// schema returns one tool schema plus the call reference execute understands,
// or the tool names of one target.
func (b *codeModeBridge) schema(this *qjs.This) (*qjs.Value, error) {
	if err := b.entry(); err != nil {
		return nil, err
	}
	// A read-only schema hop must not start once the run budget is spent.
	if _, err := b.remaining(); err != nil {
		return nil, err
	}
	target := codeModeStringArg(this, 0)
	tool := codeModeStringArg(this, 1)
	if target == "" {
		return nil, b.fail("invalid_arguments", "mcp.schema requires a target", nil)
	}
	// Use the ordinary facade result so the call reference and the optional
	// schema contract metadata are exactly what a direct execute would receive.
	// The run context is applied so an aborted caller stops the schema path.
	schemaRequest := b.request.WithContext(b.ctx)
	schemaArgs := map[string]any{"target": target, "tool": tool, "detail": "compact"}
	if err := authorizeFacadeCall(schemaRequest, "schema", schemaArgs); err != nil {
		return nil, b.fail("forbidden", err.Error(), nil)
	}
	result := mapValue(b.server.appsSDKCallForRequest(schemaRequest, "schema", schemaArgs))
	if status := firstString(result, "status"); status != "completed" {
		receipt := map[string]any{"status": status, "owner_target": target}
		if id := firstString(result, "job_id", "task_id"); id != "" {
			receipt["job_id"] = id
		}
		if status == "running" || status == "queued" {
			return nil, b.fail("nonterminal", "schema is still running; read its job receipt without resubmitting", receipt)
		}
		detail := firstString(mapValue(result["error"]), "message", "error", "detail")
		if detail == "" {
			detail = firstString(result, "error", "message", "detail")
		}
		return nil, b.fail("schema_unavailable", detail, receipt)
	}
	response := mapValue(result["response"])
	rows := outputRows(response["tools"])
	payload := map[string]any{"tools": []any{}}
	// Forward the optional schema-contract metadata so a strict caller can pass
	// it straight back on mcp.call.
	for _, key := range []string{"schema_version", "schema_digest_sha256"} {
		if value := firstString(response, key); value != "" {
			payload[key] = value
		}
	}
	if tool == "" {
		// List mode: names only, so a script can choose without pulling schemas.
		names := make([]any, 0, len(rows))
		for _, item := range rows {
			if name, ok := item.(string); ok {
				names = append(names, name)
			} else {
				names = append(names, firstString(mapValue(item), "name"))
			}
		}
		payload["tools"] = names
		return b.jsonValue(this, payload), nil
	}
	for _, item := range rows {
		definition := mapValue(item)
		if firstString(definition, "name") != tool {
			continue
		}
		payload["tool"] = tool
		payload["inputSchema"] = definition["inputSchema"]
		payload["description"] = definition["description"]
		// A schema reference replaces repeated target/tool strings in the next
		// call; mcp.call({ref, args}) accepts it exactly like execute does.
		if ref := firstString(result, "call_ref", "operation_id"); ref != "" {
			payload["ref"] = ref
		}
		return b.jsonValue(this, payload), nil
	}
	// An unknown tool is an explicit miss, not a silent empty result.
	return nil, b.fail("unknown_tool", fmt.Sprintf("target %q has no tool %q", target, tool), nil)
}

// call runs exactly one tool through the normal authorized execute path.
//
// It never retries. A failed, pending or non-terminal receipt stops the run: a
// later call is refused and the whole script ends as failed with the original
// receipt, so a script cannot swallow an error and keep producing effects.
func (b *codeModeBridge) call(this *qjs.This) (*qjs.Value, error) {
	if err := b.entry(); err != nil {
		return nil, err
	}
	remaining, err := b.remaining()
	if err != nil {
		return nil, err
	}
	args := codeModeObjectArg(this, 0)
	ref := firstString(args, "ref")
	target := firstString(args, "target", "server_id", "agent_id")
	tool := firstString(args, "tool", "tool_name", "name")

	// A reference alone is enough: it carries the resolved target and tool.
	resolvedArgs := map[string]any{}
	if ref != "" {
		resolved, err := b.server.resolveExecutionReference(b.request, map[string]any{"ref": ref})
		if err != nil {
			return nil, b.fail("reference_unavailable", err.Error(), nil)
		}
		target = firstString(resolved, "target")
		tool = firstString(resolved, "tool")
	}
	if target == "" || tool == "" {
		return nil, b.fail("invalid_arguments", "mcp.call requires ref, or target and tool", nil)
	}
	// Re-check recursion after resolution: the canonical tool is what runs.
	if target == "hub" && tool == codeModeTool {
		return nil, b.fail("recursive_call_denied", "a code_mode script cannot call code_mode", nil)
	}
	if callArgs := mapValue(args["args"]); callArgs != nil {
		resolvedArgs = callArgs
	} else if callArgs := mapValue(args["arguments"]); callArgs != nil {
		resolvedArgs = callArgs
	}
	// Forward the optional schema-contract metadata so a strict caller can pass
	// exactly what mcp.schema advertised.
	contract := map[string]any{}
	for _, key := range []string{"schema_version", "schema_digest_sha256"} {
		if value := firstString(args, key); value != "" {
			contract[key] = value
		}
	}

	callCtx, cancel := context.WithTimeout(b.ctx, remaining)
	defer cancel()
	callRequest := b.request.WithContext(callCtx)

	response, status := b.dispatch(callRequest, target, tool, resolvedArgs, contract, remaining, firstString(args, "idempotency_key"))
	payload := codeModeCallPayload(response, status, target, tool)
	if payload["ok"] != true {
		// Stop the run on the first non-completing call. The script still sees
		// the exception, but no further call can succeed.
		b.fail(firstString(payload, "status"), firstString(payload, "detail"), payload)
	}
	return b.jsonValue(this, payload), nil
}

// dispatch mirrors the ordinary execute path for one nested call, in the same
// order, so authorization can never be weaker than a direct call.
func (b *codeModeBridge) dispatch(callRequest *http.Request, target, tool string, callArgs, contract map[string]any, timeout time.Duration, idempotencyKey string) (map[string]any, int) {
	if err := authorizeToolCall(callRequest, target, tool); err != nil {
		return map[string]any{"status": "failed", "error": err.Error()}, http.StatusForbidden
	}
	selected, status, detail := b.server.selectMCPRelayTarget(target)
	if status != http.StatusOK {
		return map[string]any{"status": "failed", "error": detail}, status
	}
	contractArgs := map[string]any{"target": selected, "tool": tool, "arguments": callArgs}
	for key, value := range contract {
		contractArgs[key] = value
	}
	if response, blocked := b.server.validateSchemaContract(callRequest, selected, contractArgs); blocked {
		return map[string]any{"server_id": selected, "status": "failed", "response": response}, http.StatusConflict
	}
	return b.server.executeMCPTool(callRequest, selected, tool, callArgs, false, timeout, idempotencyKey)
}

// codeModeInnerResult returns the nested response as plain JSON data. Some hub
// tools return a Go struct rather than a map, so anything that is not already a
// JSON-shaped value is normalized here instead of silently arriving empty.
func codeModeInnerResult(response map[string]any) map[string]any {
	raw, exists := response["response"]
	if !exists || raw == nil {
		return nil
	}
	if typed, ok := raw.(map[string]any); ok {
		return typed
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil
	}
	return decoded
}

// codeModeCallPayload normalizes one nested call. The Hub envelope reports
// "completed" even for a failed inner response, so completion is re-derived
// here. Anything that is not a plain 2xx completion is ok:false with no result.
func codeModeCallPayload(response map[string]any, status int, target, tool string) map[string]any {
	inner := codeModeInnerResult(response)
	terminal, detail := codeModeCallTerminal(response, inner, status)
	payload := map[string]any{
		"target": target,
		"tool":   tool,
		"ok":     terminal == "completed",
		"status": terminal,
	}
	if detail != "" {
		payload["detail"] = detail
	}
	if status != 0 && status != http.StatusOK {
		payload["http_status"] = status
	}
	if jobID := firstString(response, "job_id", "task_id"); jobID != "" {
		payload["job_id"] = jobID
	}
	// A pending approval is a real outcome the script must stop on, not success.
	for _, key := range []string{"approval_id", "approval_required", "expires_at", "message", "retryable", "owner_target"} {
		if value, ok := response[key]; ok {
			payload[key] = value
		}
	}
	if terminal == "completed" && inner != nil {
		payload["result"] = inner
	}
	return payload
}

// codeModeCallTerminal classifies a nested call. Only an explicit 2xx
// completion is success; a running receipt is never presented as usable data.
func codeModeCallTerminal(response, inner map[string]any, status int) (string, string) {
	switch firstString(response, "status") {
	case "approval_required":
		return "approval_required", "this call needs an administrator approval before it can run"
	case "bounded_autonomous_limit":
		return "bounded_autonomous_limit", "the bounded autonomous write budget is exhausted"
	case "failed", "error":
		detail := firstString(mapValue(response["error"]), "message", "error", "detail")
		if detail == "" {
			detail = firstString(response, "error", "message", "detail")
		}
		if detail == "" {
			detail = firstString(mapValue(inner["error"]), "message", "error", "detail")
		}
		if detail == "" {
			detail = "nested tool failed"
		}
		return "failed", detail
	case "running", "queued":
		return "nonterminal", "the call is still running; read it with the job endpoint instead of retrying"
	}
	if status != 0 && (status < http.StatusOK || status >= http.StatusMultipleChoices) {
		return "failed", firstString(response, "error", "detail", "message")
	}
	if detail := firstString(response, "error", "detail"); detail != "" {
		return "failed", detail
	}
	if truthyAny(response["isError"]) || truthyAny(inner["isError"]) {
		return "failed", firstString(response, "error", "detail")
	}
	// mapValue returns an empty map for a missing key, so presence is checked
	// explicitly: only a real nested error marks the call failed.
	if raw, exists := inner["error"]; exists && raw != nil {
		if errorValue, ok := raw.(map[string]any); ok && len(errorValue) > 0 {
			return "failed", firstString(errorValue, "message", "code", "error")
		}
		return "failed", firstString(inner, "error")
	}
	if detail := firstString(inner, "error", "detail"); detail != "" {
		return "failed", detail
	}
	if firstString(response, "job_id", "task_id") != "" && firstString(response, "status") != "completed" {
		return "nonterminal", "the call returned a job that has not completed"
	}
	return "completed", ""
}

// classify turns a sandbox failure into a stable code, keeping deadline, memory
// and stack signals distinct from an ordinary script error.
func (b *codeModeBridge) classify(err error, elapsed time.Duration) *codeModeError {
	text := strings.ToLower(firstLineOfError(err))
	switch {
	case strings.Contains(text, strings.ToLower(codeModeHostPollingPanic)):
		return codeModeFailure("capability_denied", "blocking host timers and polling are not available in code_mode")
	case strings.Contains(text, "context deadline exceeded"),
		strings.Contains(text, "module closed"),
		strings.Contains(text, "context canceled"):
		if errors.Is(b.ctx.Err(), context.DeadlineExceeded) {
			return codeModeFailure("timeout", fmt.Sprintf("code_mode exceeded its %s budget after %s", b.limits.timeout, elapsed.Round(time.Millisecond)))
		}
		if b.ctx.Err() != nil {
			return codeModeFailure("cancelled", "code_mode was cancelled before it finished")
		}
		return codeModeFailure("timeout", fmt.Sprintf("code_mode exceeded its %s budget after %s", b.limits.timeout, elapsed.Round(time.Millisecond)))
	case strings.Contains(text, "out of memory"):
		return codeModeFailure("memory_limit", fmt.Sprintf("script exceeded its %d byte heap limit", b.limits.heap))
	case strings.Contains(text, "stack overflow"):
		return codeModeFailure("stack_limit", "script recursion exceeded the sandbox stack limit")
	}
	return codeModeFailure("script_error", firstLineOfError(err))
}

// codeModeStringArg reads one optional string argument.
func codeModeStringArg(this *qjs.This, index int) string {
	args := this.Args()
	if index >= len(args) {
		return ""
	}
	value := args[index]
	if value == nil || value.IsUndefined() || value.IsNull() {
		return ""
	}
	return value.String()
}

// codeModeObjectArg decodes one plain-JSON object argument. A JS object does not
// stringify through Value.String (that yields "[object Object]"), so JSON is used.
func codeModeObjectArg(this *qjs.This, index int) map[string]any {
	args := this.Args()
	if index >= len(args) {
		return map[string]any{}
	}
	value := args[index]
	if value == nil || value.IsUndefined() || value.IsNull() {
		return map[string]any{}
	}
	encoded, err := value.JSONStringify()
	if err != nil || strings.TrimSpace(encoded) == "" {
		return map[string]any{}
	}
	var decoded map[string]any
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil || decoded == nil {
		return map[string]any{}
	}
	return decoded
}

// codeModeMaxIntermediateJSON bounds a single value crossing into the sandbox,
// so a huge upstream response cannot be parsed inside WASM before the script's
// own output cap ever applies.
const codeModeMaxIntermediateJSON = 8 << 20

// codeModeJSON converts plain Go data into a sandbox value. Only JSON types
// ever cross the boundary; no host object is exposed. A payload that cannot be
// encoded or is too large is a hard, sticky failure rather than a silent
// partial object the script might trust.
func (b *codeModeBridge) jsonValue(this *qjs.This, payload map[string]any) *qjs.Value {
	encoded, err := json.Marshal(payload)
	if err != nil {
		b.fail("invalid_intermediate", "a tool response could not be encoded as JSON", nil)
		return this.Context().ParseJSON(`{"ok":false,"status":"invalid_intermediate","detail":"a tool response could not be encoded as JSON"}`)
	}
	if len(encoded) > codeModeMaxIntermediateJSON {
		b.fail("intermediate_too_large", fmt.Sprintf("a tool response exceeded %d bytes", codeModeMaxIntermediateJSON), nil)
		return this.Context().ParseJSON(fmt.Sprintf(`{"ok":false,"status":"intermediate_too_large","detail":"a tool response exceeded %d bytes"}`, codeModeMaxIntermediateJSON))
	}
	return this.Context().ParseJSON(string(encoded))
}
