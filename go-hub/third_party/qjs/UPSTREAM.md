# Scoped QJS lifecycle patch

Origin: github.com/fastschema/qjs v0.0.6 (MIT; LICENSE preserved).
The Go production files and embedded qjs.wasm are copied from the verified Go module cache.
Only runtime.go changes upstream behavior. No change to guest capabilities, auth, timeouts, quotas or WASM code.

Runtime.Close now closes the owner Wazero runtime (including env and WASI host modules) in a non-cancelled cleanup context, and always releases registry/closure references. Cancellation cannot skip host cleanup. Runtime.New also cleans a partially constructed runtime.
The upstream compilation cache is left intact; runtime instances remain isolated.

The original Hub cancels its run context before guarded Close. With upstream v0.0.6, each cancellation caused a recovered panic before registry cleanup; standalone real-Wazero tests retained about 746 KiB per run. Merely swapping close/cancel left smaller retained state.
The scoped fix kept live heap at about 2.85 MB after warmup in the same 24-run test; no panic. This is source-level reproduction, not production acceptance.

Embedded qjs.wasm SHA-256: 88fcbeead33cca3ec8394feb0fb3eec8047ec9ac5062aba7a63799cee11989b7

Remove the local replacement only after an upstream release passes both runtime_release_test.go and the Hub CodeModeRuntimeReleasePlateau regression.
