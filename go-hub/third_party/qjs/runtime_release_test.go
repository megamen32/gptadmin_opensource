package qjs

import (
	"context"
	"github.com/tetratelabs/wazero/experimental"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestRuntimeReleaseHostModules(t *testing.T) {
	for _, mode := range []string{"success", "cancelled", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rt, err := New(Option{CWD: os.DevNull, Stdout: io.Discard, Stderr: io.Discard, MemoryLimit: 64 << 20, MaxStackSize: 1 << 20, CloseOnContextDone: true, Context: ctx})
			if err != nil {
				t.Fatal(err)
			}
			wrt, registry := rt.wrt, rt.registry
			env, wasi := wrt.Module("env"), wrt.Module("wasi_snapshot_preview1")
			if env == nil || wasi == nil {
				t.Fatal("host modules not instantiated")
			}
			payload := make([]byte, 256<<10)
			rt.Context().SetFunc("capture", func(this *This) (*Value, error) { return this.Context().NewString(string(payload[:1])), nil })
			if registry.Len() == 0 {
				t.Fatal("no host binding was registered")
			}
			if mode == "deadline" {
				timer := time.AfterFunc(30*time.Millisecond, cancel)
				failed := false
				func() {
					defer func() {
						if recover() != nil {
							failed = true
						}
					}()
					_, err = rt.Context().Eval("loop.js", Code("for(;;){}"))
					failed = err != nil
				}()
				timer.Stop()
				if !failed {
					t.Fatal("cancelled guest execution unexpectedly succeeded")
				}
			} else {
				v, err := rt.Context().Eval("value.js", Code("capture(); 42"))
				if err != nil {
					t.Fatal(err)
				}
				if v != nil {
					v.Free()
				}
				if mode == "cancelled" {
					cancel()
				}
			}
			rt.Close()
			rt.Close() // Cleanup is idempotent and does not recreate resources.
			if !env.IsClosed() || !wasi.IsClosed() {
				t.Fatal("owned host modules remain open")
			}
			if registry.Len() != 0 {
				t.Fatalf("retained %d host bindings", registry.Len())
			}
			if rt.wrt != nil || rt.registry != nil || rt.context != nil || rt.module != nil || rt.handle != nil || rt.mem != nil || rt.option.ProxyFunction != nil {
				t.Fatal("runtime still retains owned resources")
			}
		})
	}
}

func TestRuntimeReleaseOnInitializationFailure(t *testing.T) {
	var closed atomic.Int32
	ctx := experimental.WithCloseNotifier(context.Background(), experimental.CloseNotifyFunc(func(context.Context, uint32) { closed.Add(1) }))
	// Valid WASM with one exported memory but no QuickJS entrypoints.
	// Instantiation succeeds; initializeRuntime then fails, exercising real
	// partial construction rather than a fake runtime or patched boundary.
	wasm := []byte{0, 97, 115, 109, 1, 0, 0, 0, 5, 3, 1, 0, 1, 7, 10, 1, 6, 109, 101, 109, 111, 114, 121, 2, 0}
	rt, err := New(Option{CWD: os.DevNull, Stdout: io.Discard, Stderr: io.Discard, Context: ctx, CloseOnContextDone: true, QuickJSWasmBytes: wasm})
	if err == nil || rt != nil {
		if rt != nil {
			rt.Close()
		}
		t.Fatal("expected QuickJS initialization failure")
	}
	if closed.Load() < 3 {
		t.Fatalf("partial initialization leaked modules: close notifications=%d, want >=3", closed.Load())
	}
}
