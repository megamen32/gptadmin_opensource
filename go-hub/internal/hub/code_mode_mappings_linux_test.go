//go:build linux

package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type execMapSample struct {
	count, anonymousExecutable int
	execKB                     int64
}

func execMaps(t *testing.T) execMapSample {
	t.Helper()
	// Collect unreachable transient compiler allocations and then the objects
	// released by finalizers. A permanent engine reference survives both.
	for i := 0; i < 3; i++ {
		runtime.GC()
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
	}
	raw, e := os.ReadFile("/proc/self/maps")
	if e != nil {
		t.Skip(e)
	}
	var m execMapSample
	for _, ln := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		f := strings.Fields(ln)
		if len(f) < 5 {
			continue
		}
		m.count++
		if len(f) != 5 || !strings.Contains(f[1], "x") {
			continue
		}
		a, b, _ := strings.Cut(f[0], "-")
		left, e1 := strconv.ParseUint(a, 16, 64)
		right, e2 := strconv.ParseUint(b, 16, 64)
		if e1 != nil || e2 != nil {
			t.Fatalf("malformed proc map: %q", ln)
		}
		m.anonymousExecutable++
		m.execKB += int64(right-left) / 1024
	}
	return m
}
func execCPU(t *testing.T) time.Duration {
	t.Helper()
	var v syscall.Rusage
	if e := syscall.Getrusage(syscall.RUSAGE_SELF, &v); e != nil {
		t.Fatal(e)
	}
	return time.Duration(v.Utime.Nano() + v.Stime.Nano())
}
func TestNativeExecExecutableMappingsRemainBounded(t *testing.T) {
	s, _ := codeModeFixture(t)
	defer s.Close()
	endpoint := httptest.NewServer(s.Handler())
	defer endpoint.Close()
	client := endpoint.Client()
	ctx := ""
	forCall := func(id int) {
		t.Helper()
		script := fmt.Sprintf("if(globalThis.probe!==undefined)throw Error('sandbox state reused');globalThis.probe=%d;return %d;", id, id)
		args := map[string]any{"code": script}
		for attempt := 0; attempt < 2; attempt++ {
			if ctx != "" {
				args["context"] = ctx
			}
			payload := map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": "exec", "arguments": args, "_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28", "io.modelcontextprotocol/clientInfo": map[string]any{"name": "native-map-probe", "version": "1"}, "io.modelcontextprotocol/clientCapabilities": map[string]any{}}}}
			b, e := json.Marshal(payload)
			if e != nil {
				t.Fatal(e)
			}
			req, e := http.NewRequest(http.MethodPost, endpoint.URL+"/mcp", bytes.NewReader(b))
			if e != nil {
				t.Fatal(e)
			}
			req.Header.Set("Authorization", "Bearer ctl")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json")
			req.Header.Set("MCP-Protocol-Version", "2026-07-28")
			req.Header.Set("Mcp-Method", "tools/call")
			req.Header.Set("Mcp-Name", "exec")
			resp, e := client.Do(req)
			if e != nil {
				t.Fatal(e)
			}
			var msg struct {
				Result struct {
					Body map[string]any `json:"structuredContent"`
				} `json:"result"`
				Error any `json:"error"`
			}
			e = json.NewDecoder(resp.Body).Decode(&msg)
			resp.Body.Close()
			if e != nil || resp.StatusCode != 200 || msg.Error != nil {
				t.Fatalf("native call %d: HTTP %d RPC %v decode %v", id, resp.StatusCode, msg.Error, e)
			}
			body := msg.Result.Body
			if attempt == 0 && body["executed"] == false && firstString(body, "context") != "" {
				ctx = firstString(body, "context")
				continue
			}
			if body["status"] != "completed" || intFromAny(body["result"]) != id {
				t.Fatalf("native exec %d: %v", id, body)
			}
			return
		}
		t.Fatal("context was not accepted")
	}
	for i := 0; i < 8; i++ {
		forCall(i + 1)
	}
	before := execMaps(t)
	var durations []time.Duration
	cpu := execCPU(t)
	for i := 0; i < 192; i++ {
		begin := time.Now()
		forCall(9 + i)
		durations = append(durations, time.Since(begin))
	}
	elapsedCPU := execCPU(t) - cpu
	after := execMaps(t)
	slices.Sort(durations)
	t.Logf("192 native calls: VMA %d -> %d, anonymous executable VMAs %d -> %d, executable KiB %d -> %d, CPU/call %s, wall p50=%s p95=%s", before.count, after.count, before.anonymousExecutable, after.anonymousExecutable, before.execKB, after.execKB, elapsedCPU/192, durations[96], durations[182])
	if after.execKB-before.execKB > 2048 {
		t.Fatalf("anonymous executable memory grew by %d KiB after teardown", after.execKB-before.execKB)
	}
}
