package hub

import (
	"runtime"
	"testing"
	"time"
)

// This uses the real Hub Code Mode and qjs/Wazero lifecycle. It does not
// call external tools, touch production, capture heap data or force GC
// outside this test process. Upstream qjs v0.0.6 retains several MiB here.
func TestCodeModeRuntimeReleasePlateau(t *testing.T) {
	s, r := codeModeFixture(t)
	defer s.Close()
	limits := codeModeLimits{timeout: 3 * time.Second, maxCalls: 1, heap: 64 << 20, maxOut: 1024}
	run := func() {
		result, err := s.runCodeMode(r.Context(), r, "return {ok:true};", limits)
		if err != nil {
			t.Fatalf("Code Mode failed: %v", err)
		}
		payload, ok := result.result.(map[string]any)
		if !ok || payload["ok"] != true {
			t.Fatalf("unexpected result: %#v", result.result)
		}
	}
	live := func() uint64 {
		runtime.GC()
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return m.HeapAlloc
	}
	for i := 0; i < 8; i++ {
		run()
	}
	before := live()
	for i := 0; i < 24; i++ {
		run()
	}
	after := live()
	delta := int64(after) - int64(before)
	t.Logf("24 post-warmup runs: live heap before=%d after=%d delta=%d", before, after, delta)
	if delta > 2<<20 {
		t.Fatalf("Code Mode retains memory across completed runs: +%d bytes", delta)
	}
}
