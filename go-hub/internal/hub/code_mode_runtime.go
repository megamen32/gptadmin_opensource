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
	codeModeWrapperPrefix = "const __safeJSON=globalThis.__codeModeSafeJSON;globalThis.console=Object.freeze({log:(...a)=>globalThis.__codeModeLog(__safeJSON.stringify(a,(_,v)=>typeof v==='bigint'?v.toString():v))});const file=target=>{if(!target.startsWith('file:'))target='file:'+target;const f=(kind,path,opts={})=>globalThis.__codeModeFileCall(kind,target,path,opts);for(const k of ['ls','read','create','replace','batchEdit','checkpoint'])f[k]=(path,opts={})=>f(k,path,opts);f.readMany=async(paths,opts={})=>{const rows=[];for(const p of paths)rows.push(await f('read',p,opts));return rows;};return Object.freeze(f);};export default {value: await (async () => {\n"
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
	if len(value) > limits.maxOut+64 {
		return nil, bridge.annotate(codeModeFailure("output_too_large", "serialized result exceeds the configured output budget"))
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
	if len(encoded)+len(logsJSON) > limits.maxOut {
		return nil, bridge.annotate(codeModeFailure("output_too_large", fmt.Sprintf("serialized result and logs use %d bytes; limit is %d", len(encoded)+len(logsJSON), limits.maxOut)))
	}
	return &codeModeResult{result: decoded, logs: logs, calls: bridge.calls(), limits: limits, elapsed: elapsed}, nil
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
