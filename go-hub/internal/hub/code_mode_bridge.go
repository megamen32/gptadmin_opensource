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
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/fastschema/qjs"
)

// codeModeAPISource exposes the host bridge under one frozen `mcp` namespace.
// qjs only supports global binding, so the functions are bound to hidden
// globals first and then removed again.
const codeModeAPISource = `const __startMCP = globalThis.__codeModeStartCall;
const __pollMCP = globalThis.__codeModePollCall;
const __yieldMCP = globalThis.__codeModeYield;
const __discoverMCP = globalThis.__codeModeDiscover;
const __schemaMCP = globalThis.__codeModeSchema;
const __searchMCP = globalThis.__codeModeSearch;
const __facadeMCP = globalThis.__codeModeFacade;
const __fileMCP = globalThis.__codeModeFile;
const call = (target, tool, args={}, opts={}) => { const v = typeof target === 'object' ? {...target} : {target, tool, args}; if (opts && opts.index === true) { v.__indexResult = true; v.__indexText = opts.text || ''; v.__indexName = opts.name || ''; } if (opts && opts.allowPartial === true) { v.__indexAllowPartial = true; } if (v.index === true) { v.__indexResult = true; v.__indexText = v.text || ''; v.__indexName = v.name || ''; delete v.index; } if (v.allowPartial === true) { v.__indexAllowPartial = true; delete v.allowPartial; } const id=__startMCP(v); return (async()=>{ await Promise.resolve(); for(;;){ const state=__pollMCP(id); if(state.ready){ if(state.error) throw new Error(state.error); return state.value; } __yieldMCP(); await Promise.resolve(); } })(); };
const search = (query, opts={}) => (typeof opts.source==='string' || opts.scope==='content') ? __contentMCP('search',query,opts) : __searchMCP(query,opts);
const __contentMCP = globalThis.__codeModeContent;
const __nativeJSONStringify = JSON.stringify.bind(JSON);
Object.defineProperty(globalThis, '__codeModeSafeJSON', {value:Object.freeze({stringify:(value,replacer)=>__nativeJSONStringify(value,replacer)}), configurable:false, writable:false});
globalThis.mcp = Object.freeze({
  discover: (...a) => __discoverMCP(...a),
  schema: (...a) => __schemaMCP(...a),
  search, call,
  job: (id, owner_target) => __facadeMCP('job', {id, owner_target}),
  expand: (id, path='', offset=0, limit=4096) => __facadeMCP('expand', {id, path, offset, limit}),
  files: Object.freeze({
    ls: (target,path,max_bytes=65536) => __fileMCP('ls',target,path,{max_bytes}),
    read: (target,path,opts={}) => __fileMCP('read',target,path,opts),
    readMany: async (target,paths,opts={}) => { const out=[]; for(const path of paths) out.push(await __fileMCP('read',target,path,opts)); return out; },
    create: (target,path,text) => __fileMCP('create',target,path,{text}),
    replace: (target,path,old_text,new_text,opts={}) => __fileMCP('replace',target,path,{old_text,new_text,...opts}),
    batchEdit: (target,path,operations,opts={}) => __fileMCP('batchEdit',target,path,{operations,...opts}),
    checkpoint: (target,path,opts={}) => __fileMCP('checkpoint',target,path,opts),
  }),
  content: Object.freeze({
    index: (text,opts={}) => __contentMCP('index',text,opts),
    indexSource: (source,opts={}) => __contentMCP('indexSource',source,opts),
    status: (source,opts={}) => __contentMCP('status',source,opts),
    search: (query,opts={}) => __contentMCP('search',query,opts),
    read: (source,chunk_id,opts={}) => __contentMCP('read',source,{chunk_id,...opts}),
    delete: (source) => __contentMCP('delete',source,{}),
  }),
});
globalThis.call = call;
globalThis.search = search;
globalThis.index = mcp.content.index;
globalThis.read = mcp.content.read;
globalThis.drop = mcp.content.delete;
globalThis.__codeModeFileCall = (kind,target,path,opts={}) => __fileMCP(kind,target,path,opts);
delete globalThis.__codeModeDiscover;
delete globalThis.__codeModeSchema;
delete globalThis.__codeModeSearch;
delete globalThis.__codeModeStartCall;
delete globalThis.__codeModePollCall;
delete globalThis.__codeModeYield;
delete globalThis.__codeModeFacade;
delete globalThis.__codeModeFile;
delete globalThis.__codeModeContent;
`

// codeModeBridge installs mcp.discover, mcp.schema and mcp.call into a sandbox.
type codeModeBridge struct {
	server   *Server
	request  *http.Request
	ctx      context.Context
	deadline time.Time
	limits   codeModeLimits
	readOnly bool

	mu             sync.Mutex
	pendingMu      sync.Mutex
	pendingCalls   map[string]*codeModePendingCall
	pendingStart   []string
	pendingNext    uint64
	pendingClosed  bool
	pendingWorkers sync.WaitGroup
	callCount      int
	logs           []any
	logBytes       int
	lastReceipt    map[string]any
	// origins records every target/tool this run read. A result derived from
	// the run is bound to all of them, not only to the last call, so a right
	// that is withdrawn from any of them also withdraws the derived result.
	origins []string
	// partialJob is the job of the last call that returned only a tail. It is
	// the handle to the confirmed full output of that operation.
	partialJob    string
	partialTarget string
	// fatal is set once, by the first nested call that did not complete. Later
	// bridge calls are refused even if the script catches the exception, and the
	// whole run ends as failed. A script cannot acknowledge a failed or pending
	// call and keep producing side effects.
	fatal *codeModeFatal
}

type codeModePendingCall struct {
	done  chan struct{}
	args  map[string]any
	value any
	err   error
}

// codeModeFatal is the bounded receipt of the first non-completing call.
type codeModeFatal struct {
	code    string
	message string
	receipt map[string]any
}

func newCodeModeBridge(s *Server, r *http.Request, ctx context.Context, deadline time.Time, limits codeModeLimits) *codeModeBridge {
	return &codeModeBridge{server: s, request: r, ctx: ctx, deadline: deadline, limits: limits, readOnly: readOnlyExecRequest(r)}
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

// recordOrigin remembers one source this run read.
//
// Every read is recorded, not only the last one: a result that is derived from
// the whole run must be revoked together with any of its sources.
func (b *codeModeBridge) recordOrigin(target, tool string) {
	if target == "" || tool == "" {
		return
	}
	origin := contentOriginSingle(target, tool)
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, existing := range b.origins {
		if existing == origin {
			return
		}
	}
	b.origins = append(b.origins, origin)
}

// contentOrigins returns the origin encoding for everything this run read.
func (b *codeModeBridge) contentOrigins(extra ...string) string {
	b.mu.Lock()
	origins := append([]string(nil), b.origins...)
	b.mu.Unlock()
	return contentOriginMerge(append(origins, extra...)...)
}

// lastReceiptSnapshot returns a copy of the receipt of the last completed
// nested call.
func (b *codeModeBridge) lastReceiptSnapshot() map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	return cloneMap(b.lastReceipt)
}

// recordPartialSource remembers the job of a call that returned only a tail.
func (b *codeModeBridge) recordPartialSource(jobID, target string) {
	if jobID == "" {
		return
	}
	b.mu.Lock()
	b.partialJob, b.partialTarget = jobID, target
	b.mu.Unlock()
}

// partialSource returns the job of the last call that returned only a tail.
func (b *codeModeBridge) partialSource() (string, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.partialJob, b.partialTarget
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
	ctx.SetFunc("__codeModeSearch", b.search)
	ctx.SetFunc("__codeModeStartCall", b.startCall)
	ctx.SetFunc("__codeModePollCall", b.pollCall)
	ctx.SetFunc("__codeModeYield", b.yieldCall)
	ctx.SetFunc("__codeModeFacade", b.facade)
	ctx.SetFunc("__codeModeFile", b.file)
	ctx.SetFunc("__codeModeContent", b.content)
	ctx.SetFunc("__codeModeLog", b.log)
	if _, err := ctx.Eval("code_mode_api.js", qjs.Code(codeModeAPISource)); err != nil {
		return fmt.Errorf("cannot expose the code_mode API: %w", err)
	}
	return nil
}

func (b *codeModeBridge) log(this *qjs.This) (*qjs.Value, error) {
	args := this.Args()
	if len(args) == 0 || args[0] == nil || args[0].IsUndefined() || args[0].IsNull() {
		return nil, b.fail("invalid_log", "console.log could not serialize its arguments", nil)
	}
	encoded := args[0].String()
	var values []any
	if json.Unmarshal([]byte(encoded), &values) != nil {
		return nil, b.fail("invalid_log", "console.log arguments must be JSON serializable", nil)
	}
	entry := any(values)
	entryJSON, _ := json.Marshal(entry)
	b.mu.Lock()
	used := b.logBytes + len(entryJSON)
	if used > b.limits.maxOut {
		b.mu.Unlock()
		return nil, b.fail("output_too_large", fmt.Sprintf("console.log would exceed the %d byte combined output budget", b.limits.maxOut), nil)
	}
	b.logBytes = used
	b.logs = append(b.logs, entry)
	b.mu.Unlock()
	return this.Context().ParseJSON("null"), nil
}

func (b *codeModeBridge) logValues() []any {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]any, len(b.logs))
	copy(out, b.logs)
	return out
}

func (b *codeModeBridge) annotate(err *codeModeError) *codeModeError {
	if err == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err.call == 0 {
		err.call = b.callCount
	}
	if err.receipt == nil && b.lastReceipt != nil {
		err.receipt = cloneMap(b.lastReceipt)
	}
	return err
}

func (b *codeModeBridge) search(this *qjs.This) (*qjs.Value, error) {
	if err := b.entry(); err != nil {
		return nil, err
	}
	args := codeModeObjectArg(this, 0)
	for k, v := range codeModeObjectArg(this, 1) {
		args[k] = v
	}
	if query := codeModeStringArg(this, 0); query != "" {
		args["query"] = query
	}
	request := b.request.WithContext(b.ctx)
	if b.readOnly && firstString(args, "target") != "" && !profileAllowsTarget(request, firstString(args, "target")) {
		return nil, b.fail("forbidden", "access profile denies this target", nil)
	}
	if err := authorizeFacadeCall(request, "search", args); err != nil {
		return nil, b.fail("forbidden", err.Error(), nil)
	}
	result := b.server.searchToolsForRequest(request, args)
	if firstString(result, "status") == "failed" {
		return nil, b.fail("search_unavailable", firstString(result, "error"), nil)
	}
	return b.jsonValue(this, result), nil
}

// facade exposes the small read-only Hub receipt facade inside the same
// authenticated request. It does not expose client-only apps or services.
func (b *codeModeBridge) facade(this *qjs.This) (*qjs.Value, error) {
	if err := b.entry(); err != nil {
		return nil, err
	}
	if _, err := b.remaining(); err != nil {
		return nil, err
	}
	name := codeModeStringArg(this, 0)
	args := codeModeObjectArg(this, 1)
	if name != "job" && name != "expand" {
		return nil, b.fail("invalid_arguments", "only job and expand facade helpers are available", nil)
	}
	request := b.request.WithContext(b.ctx)
	if b.readOnly && name == "job" && (!profileAllowsTarget(request, "hub") || !profileAllowsTool(request, "job")) {
		return nil, b.fail("forbidden", "access profile denies this facade", nil)
	}
	if err := authorizeFacadeCall(request, name, args); err != nil {
		return nil, b.fail("forbidden", err.Error(), nil)
	}
	result := mapValue(b.server.appsSDKCallForRequest(request, name, args))
	if status := firstString(result, "status"); status == "failed" || status == "approval_required" || status == "running" || status == "queued" {
		receipt := map[string]any{"status": status}
		for _, key := range []string{"job_id", "task_id", "operation_id"} {
			if v := firstString(result, key); v != "" {
				receipt[key] = v
			}
		}
		return nil, b.fail("facade_incomplete", firstString(result, "error", "message", "detail", "status"), receipt)
	}
	return b.jsonValue(this, redactPublicMetadata(result)), nil
}

// file calls are conveniences over the existing file:<host> MCP contract.
// Every operation remains a separately authorized dispatch.
func (b *codeModeBridge) file(this *qjs.This) (*qjs.Value, error) {
	kind := codeModeStringArg(this, 0)
	target, path := codeModeStringArg(this, 1), codeModeStringArg(this, 2)
	if !strings.HasPrefix(target, "file:") {
		return nil, b.fail("invalid_arguments", "file helpers require an explicit file:<host> target", nil)
	}
	opts := codeModeObjectArg(this, 3)
	var tool string
	args := map[string]any{"path": path}
	indexResult := truthy(opts["index"])
	switch kind {
	case "ls":
		tool = "system_inspect"
		args["action"], args["max_bytes"] = "list_directory", opts["max_bytes"]
	case "read":
		tool = "file_editor"
		args["action"] = "view"
		args["raw"] = true
		for _, k := range []string{"start_line", "end_line", "max_bytes"} {
			if v, ok := opts[k]; ok {
				args[k] = v
			}
		}
	case "replace":
		tool = "file_editor"
		args["action"] = "str_replace"
		for _, k := range []string{"old_text", "new_text", "replace_all", "max_bytes"} {
			if v, ok := opts[k]; ok {
				args[k] = v
			}
		}
	case "batchEdit":
		tool = "file_editor"
		args["action"] = "batch_edit"
		for _, k := range []string{"operations", "max_bytes"} {
			if v, ok := opts[k]; ok {
				args[k] = v
			}
		}
	case "checkpoint":
		tool = "file_checkpoint"
		for _, k := range []string{"action", "label", "ttl_days", "checkpoint_id"} {
			if v, ok := opts[k]; ok {
				args[k] = v
			}
		}
		if args["action"] == nil {
			args["action"] = "create"
		}
	case "create":
		tool = "file_editor"
		args["action"], args["content"] = "create", opts["text"]
	default:
		return nil, b.fail("invalid_arguments", "unknown file helper", nil)
	}
	call := map[string]any{"target": target, "tool": tool, "args": args}
	if indexResult {
		call["__indexResult"] = true
		call["__indexText"] = "file-text"
		call["__indexName"] = path
	}
	return b.runCall(this, call)
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
	if b.readOnly && !profileAllowsTarget(request, "hub") {
		return nil, b.fail("forbidden", "access profile denies this target", nil)
	}
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
	if b.readOnly && !profileAllowsTarget(schemaRequest, target) {
		return nil, b.fail("forbidden", "access profile denies this target", nil)
	}
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
	return b.runCall(this, codeModeObjectArg(this, 0))
}

// startCall reserves an authorized nested call synchronously, then runs its
// ordinary dispatch on a Go worker. JS polls the call ID from its own thread,
// so no worker accesses a QuickJS value or context.
func (b *codeModeBridge) startCall(this *qjs.This) (*qjs.Value, error) {
	args := codeModeObjectArg(this, 0)
	task := &codeModePendingCall{done: make(chan struct{}), args: args}
	b.pendingMu.Lock()
	if b.pendingClosed {
		b.pendingMu.Unlock()
		task.err = codeModeFailure("cancelled", "code_mode stopped before the nested call started")
		close(task.done)
	} else {
		b.pendingNext++
		id := fmt.Sprintf("%d", b.pendingNext)
		if b.pendingCalls == nil {
			b.pendingCalls = make(map[string]*codeModePendingCall)
		}
		b.pendingCalls[id] = task
		if err := b.entry(); err != nil {
			task.err = err
			close(task.done)
			b.pendingMu.Unlock()
		} else {
			b.pendingStart = append(b.pendingStart, id)
			b.pendingMu.Unlock()
		}
		return this.Context().NewString(id), nil
	}
	// A closed bridge still returns a completed handle so JS rejects the
	// Promise through the same polling path.
	b.pendingMu.Lock()
	b.pendingNext++
	id := fmt.Sprintf("%d", b.pendingNext)
	if b.pendingCalls == nil {
		b.pendingCalls = make(map[string]*codeModePendingCall)
	}
	b.pendingCalls[id] = task
	b.pendingMu.Unlock()
	return this.Context().NewString(id), nil
}

func (b *codeModeBridge) pollCall(this *qjs.This) (*qjs.Value, error) {
	id := codeModeStringArg(this, 0)
	b.startQueuedCalls()
	b.pendingMu.Lock()
	task := b.pendingCalls[id]
	if task == nil {
		b.pendingMu.Unlock()
		return b.jsonValue(this, map[string]any{"ready": true, "error": "unknown nested call id"}), nil
	}
	select {
	case <-task.done:
		delete(b.pendingCalls, id)
		value, err := task.value, task.err
		b.pendingMu.Unlock()
		if err != nil {
			return b.jsonValue(this, map[string]any{"ready": true, "error": err.Error()}), nil
		}
		return b.jsonValue(this, map[string]any{"ready": true, "value": value}), nil
	default:
		b.pendingMu.Unlock()
		return b.jsonValue(this, map[string]any{"ready": false}), nil
	}
}

// startQueuedCalls flushes all mcp.call() values created in the current JS
// turn. The wrapper yields one microtask before its first poll, so Promise.all
// has time to register every independent call before any dispatch can fail.
func (b *codeModeBridge) startQueuedCalls() {
	b.pendingMu.Lock()
	ids := b.pendingStart
	b.pendingStart = nil
	rows := make([]struct {
		id   string
		task *codeModePendingCall
		args map[string]any
	}, 0, len(ids))
	for _, id := range ids {
		if task := b.pendingCalls[id]; task != nil {
			rows = append(rows, struct {
				id   string
				task *codeModePendingCall
				args map[string]any
			}{id: id, task: task, args: task.args})
			b.pendingWorkers.Add(1)
		}
	}
	b.pendingMu.Unlock()
	for _, row := range rows {
		go func(task *codeModePendingCall, args map[string]any) {
			defer b.pendingWorkers.Done()
			value, err := b.runCallData(args, true)
			if err != nil {
				var modeErr *codeModeError
				if errors.As(err, &modeErr) {
					b.fail(modeErr.code, modeErr.detail, modeErr.receipt)
				}
			}
			// The channel close publishes both fields to pollCall.
			task.value, task.err = value, err
			close(task.done)
		}(row.task, row.args)
	}
}

func (b *codeModeBridge) yieldCall(this *qjs.This) (*qjs.Value, error) {
	// Yielding briefly lets Go dispatch workers run while QuickJS polls promises.
	time.Sleep(time.Millisecond)
	return this.Context().NewNull(), nil
}

func (b *codeModeBridge) stopAsync() bool {
	b.pendingMu.Lock()
	b.pendingClosed = true
	queued := len(b.pendingStart)
	b.pendingStart = nil
	b.pendingMu.Unlock()
	if queued > 0 {
		b.fail("unawaited_calls", "Code Mode returned before awaiting all nested MCP calls; they were not started", map[string]any{"calls_not_started": queued})
	}
	done := make(chan struct{})
	go func() { b.pendingWorkers.Wait(); close(done) }()
	select {
	case <-done:
		b.pendingMu.Lock()
		unobserved := len(b.pendingCalls)
		b.pendingMu.Unlock()
		if unobserved > 0 {
			b.fail("unawaited_calls", "Code Mode returned before collecting nested MCP call results", map[string]any{"calls_unobserved": unobserved})
		}
		return true
	case <-time.After(2 * time.Second):
		return false
	}
}

func (b *codeModeBridge) runCall(this *qjs.This, args map[string]any) (*qjs.Value, error) {
	result, err := b.runCallData(args, false)
	if err != nil {
		return nil, err
	}
	return b.jsonValue(this, result), nil
}

func (b *codeModeBridge) runCallData(args map[string]any, admitted bool) (any, error) {
	if !admitted {
		if err := b.entry(); err != nil {
			return nil, err
		}
	}
	remaining, err := b.remaining()
	if err != nil {
		return nil, err
	}
	ref := firstString(args, "ref")
	indexResult := truthy(args["__indexResult"])
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
	if b.readOnly && (truthy(args["__indexResult"]) || truthy(args["index"])) {
		return nil, b.fail("forbidden", "read-only execution cannot create persistent content sources", nil)
	}
	if target == "" || tool == "" {
		return nil, b.fail("invalid_arguments", "mcp.call requires ref, or target and tool", nil)
	}
	// Re-check recursion after resolution: the canonical tool is what runs.
	if target == "hub" && tool == codeModeTool {
		return nil, b.fail("recursive_call_denied", "a code_mode script cannot call code_mode", nil)
	}
	if target == "hub" && tool == "code_exec" {
		return nil, b.fail("migration_required", "code_exec was renamed to exec; use search then exec", nil)
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
		//
		// A refused call is a policy refusal, not a runtime fault: it is
		// reported as forbidden so the caller can tell the two apart.
		code := firstString(payload, "status")
		if intFromAny(payload["http_status"]) == http.StatusForbidden {
			code = "forbidden"
		}
		b.fail(code, firstString(payload, "detail"), payload)
		return payload, nil
	}
	// The call succeeded, so its target/tool is a source of everything this
	// run derives from it.
	b.recordOrigin(target, tool)
	usable := codeModeUsableValueForTool(response, target, tool)
	if strings.HasPrefix(target, "file:") && tool == "file_editor" {
		usable = normalizeCodeModeFileEditor(usable)
	}
	receipt := codeModeOperationReceipt(response, target, tool, payload)
	if terminal, _ := codeModeCallTerminal(response, codeModeInnerResult(response), status); terminal == "completed" {
		redactedResult := redactPublicMetadata(usable)
		if encoded, encodeErr := json.Marshal(redactedResult); encodeErr == nil && len(encoded) <= 2048 {
			receipt["result"] = redactedResult
		} else {
			receipt["result_omitted"] = "operation result exceeded 2048 bytes"
		}
	}
	b.mu.Lock()
	b.lastReceipt = receipt
	b.mu.Unlock()
	// A tail is not the full log. Remember the job, so the confirmed full
	// output can be used instead of the tail when this result is stored.
	if strings.HasPrefix(target, "shell:") && tool == "shell_exec" {
		if _, _, partial, _ := contentTextForIndex(usable, target, tool, ""); partial {
			b.recordPartialSource(firstString(receipt, "job_id", "task_id"), target)
		}
	}
	if indexResult {
		if b.readOnly {
			return nil, b.fail("forbidden", "read-only execution cannot create persistent content sources", receipt)
		}
		text, format, partial, extractErr := contentTextForIndex(usable, target, tool, firstString(args, "__indexText"))
		if extractErr != nil {
			return nil, b.fail("content_index_failed", extractErr.Error(), receipt)
		}
		complete := true
		if partial {
			// The tool returned only a tail. The server may still know the
			// complete output of this exact operation, so the confirmed source
			// is used instead of making the caller hunt for a path.
			jobID := firstString(receipt, "job_id", "task_id")
			output, confirmed := b.server.codeModeConfirmedFullOutput(b.request, jobID, target)
			if confirmed {
				text, format, partial, complete = output.Text, "stdout", false, true
			} else {
				// There is no confirmed full source. A tail is never indexed as
				// if it were the full log.
				reason := output.Reason
				if reason == "" {
					reason = "the tool returned only a part of its output"
				}
				part := map[string]any{
					"complete": false,
					"reason":   reason,
					"format":   format,
					"bytes":    len([]byte(text)),
				}
				if jobID != "" {
					part["job_id"] = jobID
				}
				if !truthy(args["__indexAllowPartial"]) {
					failed := cloneMap(receipt)
					for key, value := range part {
						failed[key] = value
					}
					return nil, b.fail("content_index_partial", "partial or spilled tool output cannot be indexed without an explicit complete read: "+reason, failed)
				}
				// The caller explicitly agreed to store a part. The
				// incompleteness marker is permanent.
				complete = false
				receipt = cloneMap(receipt)
				for key, value := range part {
					receipt[key] = value
				}
			}
		}
		name := firstString(args, "__indexName")
		// The indexed text is bound to this call and to every other source the
		// run read, so withdrawing any of them withdraws the saved text too.
		origin := contentOriginMerge(contentOriginSingle(target, tool), b.contentOrigins())
		meta, indexErr := b.saveContent(text, name, origin, format, true, complete)
		if indexErr != nil {
			return nil, b.fail("content_index_failed", indexErr.Error(), receipt)
		}
		return meta, nil
	}
	return usable, nil
}

func codeModeUsableValue(response map[string]any) any {
	return codeModeUsableValueForTool(response, "", "")
}

func codeModeUsableValueForTool(response map[string]any, target, tool string) any {
	if raw, ok := response["response"]; ok && raw != nil {
		encoded, err := json.Marshal(raw)
		if err == nil {
			var value any
			if json.Unmarshal(encoded, &value) == nil {
				business := mapValue(value)
				structured := mapValue(business["structuredContent"])
				if nested := mapValue(structured["result"]); len(nested) > 0 && firstString(structured, "server") != "" {
					// This is the ShellMCP child-call transport envelope, whose
					// result is an MCP response. Unwrap only this identified shape.
					business = nested
					structured = mapValue(nested["structuredContent"])
					if firstString(structured, "name") != "" && firstString(structured, "ref") != "" {
						business = mapValue(structured["result"])
						structured = mapValue(business["structuredContent"])
					}
				}
				// ShellMCP's command envelope is stable and identifiable. Unwrap
				// that known transport shape so short scripts can use r.stdout.
				if target != "" && strings.HasPrefix(target, "shell:") && tool == "shell_exec" {
					for depth := 0; depth < 4; depth++ {
						current := mapValue(structured["result"])
						if len(current) == 0 {
							if _, has := structured["stdout"]; has {
								return structured
							}
							break
						}
						structured = mapValue(current["structuredContent"])
						if len(structured) == 0 {
							structured = current
						}
					}
					if _, has := structured["stdout"]; has {
						return structured
					}
				}
				// For child and ordinary MCP tools, prefer structured business
				// data while preserving content alongside it. Do not unwrap an
				// arbitrary tool's `result` property.
				if len(structured) > 0 {
					if content, exists := business["content"]; exists {
						structured = cloneMap(structured)
						structured["content"] = content
					}
					return structured
				}
				return business
			}
		}
	}
	value := cloneMap(response)
	delete(value, "server_id")
	return value
}

func codeModeOperationReceipt(response map[string]any, target, tool string, payload map[string]any) map[string]any {
	receipt := map[string]any{"target": target, "tool": tool, "status": firstString(payload, "status")}
	for _, key := range []string{"job_id", "task_id", "operation_id", "approval_id"} {
		if value := firstString(response, key); value != "" {
			receipt[key] = value
		}
	}
	if result := mapValue(response["response"]); len(result) > 0 {
		for _, key := range []string{"job_id", "task_id", "operation_id", "approval_id"} {
			if value := firstString(result, key); value != "" {
				receipt[key] = value
			}
		}
	}
	return receipt
}

// dispatch mirrors the ordinary execute path for one nested call, in the same
// order, so authorization can never be weaker than a direct call.
func (b *codeModeBridge) dispatch(callRequest *http.Request, target, tool string, callArgs, contract map[string]any, timeout time.Duration, idempotencyKey string) (map[string]any, int) {
	if err := authorizeToolAction(callRequest, target, tool, callArgs); err != nil {
		return map[string]any{"status": "failed", "error": err.Error()}, http.StatusForbidden
	}
	if failure := b.server.requireInstructionContext(callRequest, target, tool); failure != nil {
		return failure, http.StatusPreconditionRequired
	}
	peerArgs := map[string]any{"target": target, "tool": tool, "arguments": callArgs}
	for k, v := range contract {
		peerArgs[k] = v
	}
	if idempotencyKey != "" {
		peerArgs["idempotency_key"] = idempotencyKey
	}
	body, err := json.Marshal(peerArgs)
	if err != nil {
		return map[string]any{"status": "failed", "error": err.Error()}, http.StatusBadRequest
	}
	recorder := httptest.NewRecorder()
	if b.server.forwardNodePeerCall(recorder, callRequest, target, body) {
		var result map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			return map[string]any{"status": "failed", "error": "invalid node peer response"}, http.StatusBadGateway
		}
		return result, recorder.Code
	}
	selected, status, detail := b.server.selectMCPRelayTarget(target)
	if status != http.StatusOK {
		return map[string]any{"status": "failed", "error": detail}, status
	}
	if err := authorizeToolAction(callRequest, selected, tool, callArgs); err != nil {
		var banErr *CodexCallerBanError
		if errors.As(err, &banErr) {
			b.server.auditToolDecision(callRequest, selected, tool, callArgs, "deny", banErr.Reason, nil, http.StatusForbidden)
		}
		return map[string]any{"status": "failed", "error": err.Error()}, http.StatusForbidden
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
// Inspect only identified MCP transport envelopes, never arbitrary business
// result fields. A completed transport can still carry a failed file/MCP call.
func codeModeNestedTransportFailure(inner map[string]any) (bool, string) {
	current := inner
	for depth := 0; depth < 6 && len(current) > 0; depth++ {
		if truthyAny(current["isError"]) || firstString(current, "resultType") == "error" || firstString(current, "error") != "" {
			detail := firstString(current, "error", "message", "detail")
			if detail == "" {
				for _, v := range outputRows(current["content"]) {
					if firstString(mapValue(v), "type") == "text" {
						detail = firstString(mapValue(v), "text")
						break
					}
				}
			}
			if detail == "" {
				detail = "nested MCP tool failed"
			}
			return true, string(utf8Prefix([]byte(detail), 2048))
		}
		structured := mapValue(current["structuredContent"])
		if firstString(structured, "server") != "" || (firstString(structured, "ref") != "" && firstString(structured, "name") != "") {
			current = mapValue(structured["result"])
			continue
		}
		break
	}
	return false, ""
}

func codeModeCallTerminal(response, inner map[string]any, status int) (string, string) {
	if failed, detail := codeModeNestedTransportFailure(inner); failed {
		return "failed", detail
	}
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
	encoded, err := stringifyCodeModeValue(value)
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
func (b *codeModeBridge) jsonValue(this *qjs.This, payload any) *qjs.Value {
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
