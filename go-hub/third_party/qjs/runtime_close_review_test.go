package qjs

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
	"weak"

	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
)

// These tests instantiate the real pinned qjs/Wazero engines. No Hub or
// external MCP call is made. Assertions run before fixture-only cleanup.
func TestReviewOwnedRuntimeClose(t *testing.T) {
	// Warm compilation outside the short timeout case.
	warm, err := New(Option{CWD: os.DevNull, CloseOnContextDone: true, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	warmOwner := warm.wrt
	warm.Close()
	_ = warmOwner.Close(context.Background())

	for _, mode := range []string{"normal", "hub_cancel_before_close", "timeout", "panic_unwind", "cleanup_panic"} {
		t.Run(mode, func(t *testing.T) {
			var notifications atomic.Int32
			parent := experimental.WithCloseNotifier(context.Background(), experimental.CloseNotifyFunc(func(context.Context, uint32) { notifications.Add(1) }))
			var ctx context.Context
			var cancel context.CancelFunc
			if mode == "timeout" {
				ctx, cancel = context.WithTimeout(parent, time.Second)
			} else {
				ctx, cancel = context.WithCancel(parent)
			}
			defer cancel()
			rt, err := New(Option{CWD: os.DevNull, Context: ctx, CloseOnContextDone: true, MemoryLimit: 64 << 20, MaxStackSize: 1 << 20, Stdout: io.Discard, Stderr: io.Discard})
			if err != nil {
				t.Fatal(err)
			}
			owner, registry := rt.wrt, rt.registry
			modules := map[string]api.Module{"guest": rt.module, "env": owner.Module("env"), "wasi": owner.Module("wasi_snapshot_preview1")}
			for name, m := range modules {
				if m == nil {
					t.Fatalf("missing real module %s", name)
				}
			}
			rt.Context().SetFunc("reviewKeep", func(this *This) (*Value, error) { return this.Context().NewString("synthetic-marker"), nil })
			if registry.Len() == 0 {
				t.Fatal("test callback not registered")
			}
			// Cleanup for a red baseline test: this is not the production close path.
			defer func() { _ = owner.Close(context.Background()); registry.Clear() }()
			var closePanic any
			closeUnderReview := func() { defer func() { closePanic = recover() }(); rt.Close() }
			if mode == "timeout" {
				evalErr := func() (err error) {
					defer func() {
						if recover() != nil {
							err = errors.New("qjs evaluation panicked on closed module")
						}
					}()
					_, err = rt.Context().Eval("review_timeout.js", Code("while (true) {}"))
					return err
				}()
				if evalErr == nil || ctx.Err() != context.DeadlineExceeded {
					t.Fatalf("timeout not exercised: eval=%v ctx=%v", evalErr, ctx.Err())
				}
				closeUnderReview()
			} else {
				value, evalErr := rt.Context().Eval("review_normal.js", Code("1 + 1"))
				if evalErr != nil {
					t.Fatal(evalErr)
				}
				value.Free()
				if mode == "cleanup_panic" {
					// Fault injection only: an invalid guest handle forces a real
					// QJS_Free WASM trap, testing finally-style resource cleanup.
					rt.handle.raw = ^uint64(0)
				}
				if mode == "hub_cancel_before_close" {
					cancel()
				}
				if mode == "panic_unwind" {
					var caught any
					func() { defer func() { caught = recover() }(); defer closeUnderReview(); panic("review-user-panic") }()
					if caught != "review-user-panic" {
						t.Fatalf("original panic changed: %v", caught)
					}
				} else {
					closeUnderReview()
				}
			}
			if mode == "cleanup_panic" && closePanic == nil {
				t.Fatal("cleanup trap was not exercised")
			}
			closed := map[string]bool{}
			for name, m := range modules {
				closed[name] = m.IsClosed()
			}
			t.Logf("mode=%s closed=%v registry=%d notifications=%d close_panicked=%t", mode, closed, registry.Len(), notifications.Load(), closePanic != nil)
			for name, ok := range closed {
				if !ok {
					t.Errorf("owned module %s remains open", name)
				}
			}
			if registry.Len() != 0 {
				t.Errorf("retained callback registry: %d", registry.Len())
			}
			if rt.Context() != nil || rt.Mem() != nil {
				t.Error("context or linear-memory references remain attached")
			}
			if notifications.Load() != 3 {
				t.Errorf("closed-module notifications = %d, want all three", notifications.Load())
			}
		})
	}
}

func TestReviewCloseDoesNotBreakSiblingRuntime(t *testing.T) {
	first, err := New(Option{CWD: os.DevNull, CloseOnContextDone: true, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(Option{CWD: os.DevNull, CloseOnContextDone: true, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	firstOwner, secondOwner := first.wrt, second.wrt
	defer func() { _ = firstOwner.Close(context.Background()); _ = secondOwner.Close(context.Background()) }()
	first.Close()
	v, err := second.Context().Eval("sibling.js", Code("40 + 2"))
	if err != nil {
		t.Fatalf("sibling broken: %v", err)
	}
	if v.Int32() != 42 {
		t.Fatal("sibling result changed")
	}
	v.Free()
	second.Close()
}

func TestReviewConstructorPanicClosesCreatedModules(t *testing.T) {
	var notifications atomic.Int32
	ctx := experimental.WithCloseNotifier(context.Background(), experimental.CloseNotifyFunc(func(context.Context, uint32) { notifications.Add(1) }))
	// Invalid callback type reaches Wazero host compilation after WASI exists.
	rt, err := New(Option{CWD: os.DevNull, Context: ctx, CloseOnContextDone: true, ProxyFunction: 42, Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || rt != nil {
		t.Fatal("invalid host function unexpectedly accepted")
	}
	t.Logf("constructor_error=true closed_modules=%d", notifications.Load())
	if notifications.Load() != 1 {
		t.Errorf("created WASI module not closed after failed initialization: notifications=%d", notifications.Load())
	}
}

// A weak marker detects actual Go reachability after caller references are
// gone. Only 64 KiB of synthetic data is captured; no heap dump is needed.
func reviewCancelledMarker(t *testing.T) weak.Pointer[[]byte] {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	rt, err := New(Option{CWD: os.DevNull, Context: ctx, CloseOnContextDone: true, MemoryLimit: 64 << 20, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	marker := new([]byte)
	*marker = make([]byte, 64<<10)
	reference := weak.Make(marker)
	rt.Context().SetFunc("reviewRetainedMarker", func(this *This) (*Value, error) { return this.Context().NewInt32(int32(len(*marker))), nil })
	cancel()
	func() { defer func() { _ = recover() }(); rt.Close() }()
	runtime.KeepAlive(marker)
	return reference
}

func TestReviewCancelledBindingBecomesUnreachable(t *testing.T) {
	marker := reviewCancelledMarker(t)
	for i := 0; i < 5; i++ {
		runtime.GC() // Only the isolated test process, never the live Hub.
		if marker.Value() == nil {
			t.Log("captured synthetic marker released after cleanup")
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("cancelled runtime still retains its registered Go callback and captured marker after caller references and five test-process GCs")
}

func TestReviewConstructorCancellationClosesCreatedModules(t *testing.T) {
	var notifications atomic.Int32
	parent := experimental.WithCloseNotifier(context.Background(), experimental.CloseNotifyFunc(func(context.Context, uint32) { notifications.Add(1) }))
	ctx, cancel := context.WithCancel(parent)
	cancel()
	rt, err := New(Option{CWD: os.DevNull, Context: ctx, CloseOnContextDone: true, Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || rt != nil {
		t.Fatal("cancelled construction unexpectedly succeeded")
	}
	t.Logf("constructor_cancelled=true closed_modules=%d", notifications.Load())
	if notifications.Load() != 3 {
		t.Errorf("cancelled constructor did not close guest, env and WASI: notifications=%d", notifications.Load())
	}
}
