package hub

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// Model an admitted run blocked in a downstream tool, then exercise the real
// sandbox for a second caller. This tests local admission, not remote delivery.
func TestCodeModeSlowCallerDoesNotBlockIndependentRun(t *testing.T) {
	s, r := codeModeFixture(t)
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := codeModeRuns.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	releaseMemory, ok := s.reserveManagedMemory(int64(codeModeDefaultHeapBytes) + codeModeRuntimeOverhead)
	if !ok {
		t.Fatal("first caller admission failed")
	}
	defer releaseMemory()
	result, status := codeModeRun(t, s, r, map[string]any{"code": "return {healthy:true};", "timeout_ms": 10000})
	if status != http.StatusOK || result["status"] != "completed" {
		t.Fatalf("independent caller was blocked by one slow caller: %d %#v", status, result)
	}
}

func TestCodeModeConcurrentCallersLeaveRoomForSearch(t *testing.T) {
	s, r := codeModeFixture(t)
	defer s.Close()
	// Eight normal runs were supported before the single-slot regression.
	for i := 0; i < 8; i++ {
		release, ok := s.reserveManagedMemory(int64(codeModeDefaultHeapBytes) + codeModeRuntimeOverhead)
		if !ok {
			t.Fatalf("ordinary caller %d exhausted the shared budget", i+1)
		}
		defer release()
	}
	result := s.searchToolsForRequest(r, map[string]any{"query": "settings"})
	if result["status"] == "failed" {
		t.Fatalf("concurrent scripts starved discovery: %#v", result)
	}
}
