package mcpclient

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// Fast unit: measure constructor allocations, not production transport RSS.
func TestScopedHTTPNamespaceSupportsBoundedMultiServerStartup(t *testing.T) {
	c := New()
	defer c.CloseAll()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < maxScopedPartitions; i++ {
		a := scopedTestAgent()
		a.Ref = fmt.Sprintf("enabled-http-%d", i)
		p, _, err := c.acquireScope(a, fmt.Sprintf("%064x", i))
		if err != nil {
			t.Fatal("enabled multi-server inventory hit old process-slot ceiling", i, err)
		}
		c.releaseScope(p)
	}
	runtime.ReadMemStats(&after)
	t.Logf("128 isolated HTTP namespace constructors: allocated_bytes=%d (no processes, sockets or production RSS claim)", after.TotalAlloc-before.TotalAlloc)
	if len(c.scoped) != maxScopedPartitions {
		t.Fatal("namespace cardinality changed")
	}
	if _, _, err := c.acquireScope(scopedTestAgent(), strings.Repeat("f", 64)); err == nil {
		t.Fatal("namespace failed to reject 129th owner")
	}
}

func TestScopedHTTPBusyRefusesBeforePartitionAndPayload(t *testing.T) {
	c := New()
	c.scopedHTTPAdmission = make(chan struct{}, maxScopedHTTPRequests)
	for i := 0; i < maxScopedHTTPRequests; i++ {
		c.scopedHTTPAdmission <- struct{}{}
	}
	_, err := c.RequestScoped(context.Background(), scopedTestAgent(), strings.Repeat("a", 64), "tools/list", nil)
	if err == nil || len(c.scoped) != 0 || len(c.remote) != 0 {
		t.Fatal("busy HTTP admitted a namespace or attempted network I/O")
	}
}
