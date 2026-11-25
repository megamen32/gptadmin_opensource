//go:build go1.24

package qjs

import (
	"context"
	"io"
	"os"
	"runtime"
	"testing"
	"time"
	"weak"
)

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
