package hub

// Code Mode runs one short JavaScript body that chains MCP tool calls inside an
// isolated QuickJS (WebAssembly) sandbox and returns only the value the script
// chooses to return.
//
// The sandbox has no host capabilities at all: no filesystem, environment,
// process, network, timers, or host Go objects. The only bridge is the three
// narrow functions below, which re-enter the ordinary request-scoped dispatch
// path with the *caller's own* claims and access profile. Code Mode therefore
// never widens what the caller could already do through execute.
//
// A script is NOT transactional. Every tool call that starts has already
// executed by the time a later call fails; the script cannot roll those effects
// back. Callers must treat a failed script as "some prefix may have run".

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fastschema/qjs"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
)

const (
	// exec is the only public entry to the isolated script runtime.
	codeModeTool = "exec"

	// Defaults are deliberately small: a script is a short fan-out, not a job.
	codeModeDefaultTimeout   = 30 * time.Second
	codeModeMaxTimeout       = 60 * time.Second
	codeModeDefaultMaxCalls  = 32
	codeModeMaxCallsCeiling  = 64
	codeModeDefaultHeapBytes = 64 << 20  // 64 MiB
	codeModeMaxHeapBytes     = 128 << 20 // 128 MiB
	codeModeMaxCodeBytes     = 64 << 10  // 64 KiB of script source
	codeModeDefaultOutBytes  = 16 << 10  // 16 KiB of returned JSON
	codeModeMaxOutBytes      = 64 << 10  // 64 KiB of returned JSON
	codeModeDefaultConcur    = 8

	// An object envelope preserves primitive and array results across the
	// QuickJS module boundary. Logs use the bounded host bridge so they cannot
	// be silently clipped or dropped.
	codeModeWrapperPrefix = "const __safeJSON=globalThis.__codeModeSafeJSON;globalThis.console=Object.freeze({log:(...a)=>globalThis.__codeModeLog(__safeJSON.stringify(a,(_,v)=>typeof v==='bigint'?v.toString():v))});const file=target=>{if(!target.startsWith('file:'))target='file:'+target;const f=(kind,path,opts={})=>globalThis.__codeModeFileCall(kind,target,path,opts);for(const k of ['ls','read','create','replace','batchEdit','checkpoint'])f[k]=(path,opts={})=>f(k,path,opts);f.readMany=async(paths,opts={})=>{const rows=[];for(const p of paths)rows.push(await f('read',p,opts));return rows;};f.readAll=async(path,opts={})=>{const cap=opts.max_bytes??2097152;if(!Number.isInteger(cap)||cap<1||cap>8388608)throw Error('readAll max_bytes must be 1..8388608');const parts=[];let start=1,bytes=0,hash='',last;for(;;){const r=await f('read',path,{start_line:start,end_line:start+2499,max_bytes:262144});if(!r.text_exact||!r.source_sha256)throw Error('Exact readAll requires an updated file node');if(r.file_bytes>cap)throw Error('File exceeds readAll byte budget');if(hash&&hash!==r.source_sha256)throw Error('File changed during readAll');hash=r.source_sha256;parts.push(r.text);bytes+=r.bytes;last=r;if(r.line_count===0||r.end_line>=r.line_count)break;if(r.end_line<start)throw Error('A line exceeds the bounded file read size');start=r.end_line+1;}if(bytes!==last.file_bytes)throw Error('Incomplete full-file read');return {path,text:parts.join(''),bytes,file_bytes:bytes,line_count:last.line_count,source_sha256:hash,text_exact:true,truncated:false};};f.index=async(path,opts={})=>{const r=await f.readAll(path,{max_bytes:opts.max_bytes??2097152});const d=await index(r.text,{name:opts.name??path,target,tool:'file_editor'});return {...d,source_sha256:r.source_sha256};};return Object.freeze(f);};export default {value: await (async () => {\n"
	codeModeWrapperSuffix = "\n})()};\n"
)

// codeModeLimits is the resolved, clamped budget for one run.
type codeModeLimits struct {
	timeout  time.Duration
	maxCalls int
	heap     int
	maxOut   int
}

func resolveCodeModeLimits(args map[string]any) (codeModeLimits, error) {
	limits := codeModeLimits{
		timeout:  codeModeDefaultTimeout,
		maxCalls: codeModeDefaultMaxCalls,
		heap:     codeModeDefaultHeapBytes,
		maxOut:   codeModeDefaultOutBytes,
	}
	if raw, ok := args["timeout_ms"]; ok && raw != nil {
		ms := intFromAny(raw)
		if ms < 100 {
			return limits, errors.New("timeout_ms must be at least 100")
		}
		if ms > int(codeModeMaxTimeout/time.Millisecond) {
			return limits, fmt.Errorf("timeout_ms must be at most %d", int(codeModeMaxTimeout/time.Millisecond))
		}
		limits.timeout = time.Duration(ms) * time.Millisecond
	}
	if raw, ok := args["max_calls"]; ok && raw != nil {
		n := intFromAny(raw)
		if n < 1 {
			return limits, errors.New("max_calls must be positive")
		}
		if n > codeModeMaxCallsCeiling {
			return limits, fmt.Errorf("max_calls must be at most %d", codeModeMaxCallsCeiling)
		}
		limits.maxCalls = n
	}
	if raw, ok := args["max_output_bytes"]; ok && raw != nil {
		n := intFromAny(raw)
		if n < 256 {
			return limits, errors.New("max_output_bytes must be at least 256")
		}
		if n > codeModeMaxOutBytes {
			return limits, fmt.Errorf("max_output_bytes must be at most %d", codeModeMaxOutBytes)
		}
		limits.maxOut = n
	}
	return limits, nil
}

// codeModeResult is the tool result. Only status, the script's own return
// value, and run accounting are exposed; intermediate tool results are never
// included unless the script explicitly returns them.
type codeModeResult struct {
	result  any
	logs    []any
	calls   int
	limits  codeModeLimits
	elapsed time.Duration
}

// codeModeError carries a machine-readable failure code so the caller can fail
// closed without parsing prose.
type codeModeError struct {
	code   string
	detail string
	// call is the tool call count when the failure happened, so the caller can
	// see how much of the script already ran.
	call int
	// receipt is the bounded original receipt of the first non-completing call,
	// preserved so a pending approval or job id is never lost.
	receipt map[string]any
}

func (e *codeModeError) Error() string { return e.detail }

func codeModeFailure(code, detail string) *codeModeError {
	return &codeModeError{code: code, detail: detail}
}

// codeModeGate bounds how many scripts may run at once across all callers.
type codeModeGate struct {
	mu    sync.Mutex
	slots chan struct{}
}

func newCodeModeGate(limit int) *codeModeGate {
	if limit < 1 {
		limit = 1
	}
	return &codeModeGate{slots: make(chan struct{}, limit)}
}

func (g *codeModeGate) acquire(ctx context.Context) (func(), error) {
	select {
	case g.slots <- struct{}{}:
		return func() { <-g.slots }, nil
	case <-ctx.Done():
		return nil, codeModeFailure("busy", "code_mode is at its concurrent run limit")
	}
}

var codeModeRuns = newCodeModeGate(codeModeDefaultConcur)

// runCodeMode executes one script under the caller's own request context.
//
// The bridge functions below close over the caller's *http.Request, so every
// nested tool call is authorized by the same claims and access profile as the
// outer execute. There is no synthetic, privileged, or nil context.
func (s *Server) runCodeMode(ctx context.Context, r *http.Request, code string, limits codeModeLimits) (*codeModeResult, *codeModeError) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, codeModeFailure("invalid_arguments", "code is required")
	}
	if len(code) > codeModeMaxCodeBytes {
		return nil, codeModeFailure("invalid_arguments", fmt.Sprintf("code must be at most %d bytes", codeModeMaxCodeBytes))
	}
	if s == nil || r == nil {
		// Never fabricate a privileged context for a nested script.
		return nil, codeModeFailure("unavailable", "code_mode requires an authenticated request")
	}

	// Every runtime gets a real deadline. MaxExecutionTime alone does not stop a
	// tight loop, because the interrupt handler only runs at tick points.
	// The request context is the parent, so an aborted caller cannot leave a
	// runtime or a semaphore slot behind.
	base := ctx
	if base == nil {
		base = r.Context()
	}
	runCtx, cancel := context.WithTimeout(base, limits.timeout)
	defer cancel()

	release, err := codeModeRuns.acquire(runCtx)
	if err != nil {
		var gateErr *codeModeError
		if errors.As(err, &gateErr) {
			return nil, gateErr
		}
		return nil, codeModeFailure("busy", "code_mode was cancelled before it started")
	}
	defer release()

	// A fresh sandbox per run: no globals, promises, or tool results are shared
	// between two scripts, and one script cannot observe another's data.
	rt, rtCancel, err := newCodeModeRuntime(runCtx, limits)
	if err != nil {
		return nil, codeModeFailure("runtime_unavailable", err.Error())
	}
	defer func() {
		// A context-killed runtime is already closed; qjs panics on Close/Free
		// in that state, so the teardown is guarded rather than assumed.
		closeCodeModeRuntime(rt)
		rtCancel()
	}()

	bridge := newCodeModeBridge(s, r, runCtx, time.Now().Add(limits.timeout), limits)
	if err := bridge.install(rt.Context()); err != nil {
		return nil, codeModeFailure("runtime_unavailable", err.Error())
	}

	source := codeModeWrapperPrefix + code + codeModeWrapperSuffix
	started := time.Now()
	value, evalErr := evalCodeMode(rt, source)
	elapsed := time.Since(started)

	// A nested call that did not complete stops the whole run, even if the script
	// caught the exception and returned normally. The original receipt is what
	// the caller is told.
	if fatal := bridge.fatalReceipt(); fatal != nil {
		return nil, &codeModeError{code: fatal.code, detail: fatal.message, call: bridge.calls(), receipt: fatal.receipt}
	}
	if evalErr != nil {
		return nil, bridge.annotate(bridge.classify(evalErr, elapsed))
	}
	// The envelope is decoded before the output budget is applied: an oversized
	// result is stored in full and replaced by a small descriptor, instead of
	// failing the run and losing what the script produced.
	//
	// A result that cannot be stored cannot be returned either, so a very
	// large envelope is refused before it is decoded. The failure is honest
	// about the operations that already executed.
	if len(value) > 2*contentMaxSourceBytes {
		return nil, bridge.annotate(&codeModeError{code: "content_storage_limit", detail: fmt.Sprintf("the serialized result is %d bytes and cannot be stored; the storage limit is %d bytes", len(value), contentMaxSourceBytes), call: bridge.calls(), receipt: bridge.lastReceiptSnapshot()})
	}
	decoded, convErr := decodeCodeModeValue(value)
	if convErr != nil {
		return nil, bridge.annotate(codeModeFailure("invalid_return", convErr.Error()))
	}
	envelope, ok := decoded.(map[string]any)
	if !ok {
		return nil, bridge.annotate(codeModeFailure("invalid_return", "sandbox returned an invalid result envelope"))
	}
	decoded = envelope["value"]
	logs := bridge.logValues()
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return nil, bridge.annotate(codeModeFailure("invalid_return", "returned value cannot be serialized as JSON"))
	}
	logsJSON, _ := json.Marshal(logs)
	if len(encoded)+len(logsJSON) <= limits.maxOut {
		// A small result is returned exactly as before: no descriptor and no
		// store write.
		return &codeModeResult{result: decoded, logs: logs, calls: bridge.calls(), limits: limits, elapsed: elapsed}, nil
	}
	// The result does not fit the output budget. It is stored in full and the
	// model receives a small descriptor instead. The script is never re-run.
	if bridge.readOnly {
		// A read-only run must not write into the user's stored sources, so it
		// cannot use the store to make an oversized result fit. The failure is
		// honest about the operations that already executed.
		return nil, bridge.annotate(&codeModeError{code: "output_too_large", detail: fmt.Sprintf("the serialized result and logs use %d bytes; limit is %d. A read-only run cannot store the result", len(encoded)+len(logsJSON), limits.maxOut), call: bridge.calls(), receipt: bridge.lastReceiptSnapshot()})
	}
	descriptor, saveErr := s.saveCodeModeResult(r, bridge, decoded, encoded)
	if saveErr != nil {
		// A storage failure is reported honestly, together with the number of
		// operations that already executed.
		return nil, bridge.annotate(&codeModeError{code: "content_storage_limit", detail: saveErr.Error(), call: bridge.calls(), receipt: bridge.lastReceiptSnapshot()})
	}
	descriptorJSON, err := json.Marshal(descriptor)
	if err != nil {
		return nil, bridge.annotate(codeModeFailure("invalid_return", "the saved result descriptor cannot be serialized"))
	}
	if len(descriptorJSON)+len(logsJSON) > limits.maxOut {
		// The descriptor and the logs together still have to fit the budget.
		// The saved source is named, so the result is not lost.
		return nil, bridge.annotate(&codeModeError{code: "output_too_large", detail: fmt.Sprintf("the saved result descriptor and the logs use %d bytes; limit is %d", len(descriptorJSON)+len(logsJSON), limits.maxOut), call: bridge.calls(), receipt: map[string]any{"source": firstString(descriptor, "source")}})
	}
	return &codeModeResult{result: descriptor, logs: logs, calls: bridge.calls(), limits: limits, elapsed: elapsed}, nil
}

// codeModeResultSourceName is the name of a result that was stored because it
// did not fit the output budget.
const codeModeResultSourceName = "exec-result"

// saveCodeModeResult stores an oversized result in full and returns the
// descriptor the model receives instead of the result itself.
//
// The stored bytes are the whole result: a string is stored as text, every
// other value as its JSON encoding. Nothing is truncated.
//
// Saving alone creates no search index. The stored source starts unindexed
// and the first search over it builds the index, so a large result that is
// only kept does not pay for an index nobody searches.
//
// The source is bound to every target/tool the run read, so a right that is
// withdrawn from any of them withdraws the saved result too.
func (s *Server) saveCodeModeResult(r *http.Request, bridge *codeModeBridge, decoded any, encoded []byte) (map[string]any, error) {
	principal := operationPrincipal(r)
	if principal == "" {
		return nil, errors.New("an authenticated principal is required to store a result")
	}
	store, err := s.contentStoreForExec()
	if err != nil {
		return nil, err
	}
	text, format, complete := codeModeResultPayload(decoded, encoded)
	if !complete {
		// The script returned a tail. The server may still know the complete
		// output of the operation that produced it.
		jobID, target := bridge.partialSource()
		if output, ok := s.codeModeConfirmedFullOutput(r, jobID, target); ok {
			text, format, complete = output.Text, "stdout", true
		}
	}
	origin := bridge.contentOrigins()
	var source ContentSource
	if complete {
		source, err = store.SaveFormatted(principal, codeModeResultSourceName, origin, format, text)
	} else {
		// The stored bytes are only a part. The marker is permanent.
		source, err = store.SaveIncompleteFormatted(principal, codeModeResultSourceName, origin, format, text)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"saved":      true,
		"source":     source.ID,
		"name":       source.Name,
		"bytes":      source.Bytes,
		"format":     source.Format,
		"complete":   source.Complete,
		"indexed":    source.Indexed,
		"chunks":     source.Chunks,
		"created_at": source.CreatedAt.UTC().Format(time.RFC3339),
		"expires_at": source.ExpiresAt.UTC().Format(time.RFC3339),
		"origin":     contentOriginDescription(source.Origin),
		"read":       map[string]any{"call": "mcp.content.read", "args": map[string]any{"source": source.ID, "chunk_id": 1}},
		"search":     map[string]any{"call": "mcp.content.search", "args": map[string]any{"source": source.ID, "query": "<term>"}},
		"index":      map[string]any{"call": "mcp.content.indexSource", "args": map[string]any{"source": source.ID}},
		"note":       "The result did not fit the output budget and was stored in full. It was not re-run. Read it with mcp.content.read, find something in it with mcp.content.search (the first search builds the index), or build the index now with mcp.content.indexSource.",
	}, nil
}

// codeModeResultPayload returns the text that is stored for a returned value.
//
// A string is stored as it is. Every other value is stored as its JSON
// encoding, so nothing of the result is lost. A returned tool result that is
// only a tail is recognised, because a tail must never be stored as if it
// were the whole result.
func codeModeResultPayload(value any, encoded []byte) (string, string, bool) {
	if text, ok := value.(string); ok {
		return text, "text", true
	}
	if text, format, tail := codeModeResultTail(value); tail {
		return text, format, false
	}
	return string(encoded), "json", true
}

// codeModeResultTail reports whether a returned value is only a tail of a
// larger result, and which text it carries.
func codeModeResultTail(value any) (string, string, bool) {
	m := mapValue(value)
	if len(m) == 0 {
		return "", "", false
	}
	text := firstString(m, "stdout")
	if text == "" {
		return "", "", false
	}
	if truthy(m["_spilled"]) || intFromAny(m["stdout_bytes"]) > len([]byte(text)) {
		return text, "stdout", true
	}
	return "", "", false
}

// newCodeModeRuntime builds an isolated runtime with no host capabilities.
//
// The WASI `poll_oneoff` host function is what powers sleep and timer waits.
// Blocking it removes the only way a script could idle past its wall budget
// before the context deadline is observed, so a script can never buy itself
// unbounded host time. Every run is fresh, so nothing is shared between callers.
func newCodeModeRuntime(ctx context.Context, limits codeModeLimits) (*qjs.Runtime, context.CancelFunc, error) {
	runtimeCtx, cancel := context.WithCancel(codeModeDenyHostPolling(ctx))
	rt, err := qjs.New(qjs.Option{
		// CWD is mounted as WASI "/" by qjs. os.DevNull is a character device,
		// never a directory, so no real path is reachable from inside the sandbox.
		// The default (the Hub's production CWD) must never be used here.
		CWD:         os.DevNull,
		Stdout:      io.Discard,
		Stderr:      io.Discard,
		MemoryLimit: limits.heap,
		// Deep recursion inside the script must fail as a RangeError rather than
		// growing the Go stack without bound.
		MaxStackSize:       1 << 20,
		CloseOnContextDone: true,
		Context:            runtimeCtx,
	})
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("sandbox unavailable: %w", err)
	}
	return rt, cancel, nil
}

// codeModeDenyHostPolling raises inside the WASI poll implementation, before a
// host sleep starts. The resulting wazero error is recovered and reported as a
// capability denial rather than an ordinary script failure.
func codeModeDenyHostPolling(ctx context.Context) context.Context {
	return experimental.WithFunctionListenerFactory(ctx, experimental.FunctionListenerFactoryFunc(
		func(def api.FunctionDefinition) experimental.FunctionListener {
			if def.ModuleName() != "wasi_snapshot_preview1" {
				return nil
			}
			for _, name := range def.ExportNames() {
				if name == "poll_oneoff" {
					return experimental.FunctionListenerFunc(
						func(context.Context, api.Module, api.FunctionDefinition, []uint64, experimental.StackIterator) {
							panic(codeModeHostPollingPanic)
						})
				}
			}
			return nil
		}))
}

// codeModeHostPollingPanic is the marker recovered from the WASI poll guard.
const codeModeHostPollingPanic = "code_mode blocking WASI polling is disabled"

func closeCodeModeRuntime(rt *qjs.Runtime) {
	if rt == nil {
		return
	}
	defer func() { _ = recover() }()
	rt.Close()
}

// evalCodeMode returns one JSON envelope. Runtime traps are failures, never
// silently converted to an empty successful result.
func evalCodeMode(rt *qjs.Runtime, source string) (out string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%s", firstLineOfPanic(recovered))
		}
	}()
	value, err := rt.Context().Eval("code_mode.js", qjs.Code(source), qjs.TypeModule(), qjs.FlagAsync())
	if err != nil {
		return "", fmt.Errorf("%s", firstLineOfError(err))
	}
	if value == nil || value.IsUndefined() {
		return "", errors.New("sandbox did not return its result envelope")
	}
	text, err := stringifyCodeModeValue(value)
	if err != nil {
		return "", fmt.Errorf("result is not JSON serializable: %s", firstLineOfError(err))
	}
	return text, nil
}

// stringifyCodeModeValue avoids qjs.Value.JSONStringify in v0.0.6: its C
// helper frees both JS_JSONStringify's value and ToCString buffer before Go
// copies the packed pointer. Invoke JSON.stringify from the retained JSON
// object, keep the resulting JS string Value alive while String copies it, then
// release that Value. This preserves the runtime's ordinary JSON semantics,
// including BigInt/cycle errors, without reading freed wasm memory.
func stringifyCodeModeValue(value *qjs.Value) (string, error) {
	if value == nil {
		return "undefined", nil
	}
	ctx := value.Context()
	safeJSON := ctx.Global().GetPropertyStr("__codeModeSafeJSON")
	defer safeJSON.Free()
	if safeJSON == nil || !safeJSON.IsObject() {
		return "", errors.New("safe JSON serializer is unavailable")
	}
	serialized, err := safeJSON.InvokeJS("stringify", value)
	if err != nil {
		return "", err
	}
	defer serialized.Free()
	if serialized.IsUndefined() {
		return "undefined", nil
	}
	return serialized.String(), nil
}

func firstLineOfPanic(r any) string {
	return firstLineOf(fmt.Sprintf("%v", r))
}

func firstLineOfError(err error) string {
	return firstLineOf(err.Error())
}

func firstLineOf(text string) string {
	text = strings.TrimSpace(text)
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		return strings.TrimSpace(text[:idx])
	}
	return text
}

// decodeCodeModeValue converts the script's JSON output into plain data. Only
// JSON types cross back out; nothing host-side is exposed to the script.
func decodeCodeModeValue(text string) (any, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || trimmed == "undefined" {
		// A script that returns nothing is a successful run with no value.
		return nil, nil
	}
	var decoded any
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, errors.New("script must return a JSON-serializable value")
	}
	if decoder.More() {
		return nil, errors.New("script must return a single JSON value")
	}
	return decoded, nil
}
