package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestNew_EmptyPathIsNoOp(t *testing.T) {
	l, err := New("")
	if err != nil {
		t.Fatalf("New(\"\") returned error: %v", err)
	}
	if l == nil {
		t.Fatal("New(\"\") returned nil logger")
	}
	// Must not panic on nil-equivalent (no file) logger.
	l.Event(ExecStart, map[string]any{"cmd": "ls"})
	if err := l.Close(); err != nil {
		t.Fatalf("Close on no-op logger returned error: %v", err)
	}
}

func TestNew_NilLoggerEventDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Event on nil *Logger panicked: %v", r)
		}
	}()
	var l *Logger
	l.Event(ExecStart, map[string]any{"cmd": "ls"})
	l.Event(AuthFail, nil)
	if err := l.Close(); err != nil {
		t.Fatalf("Close on nil *Logger returned error: %v", err)
	}
}

func TestNew_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "audit.log")
	l, err := New(path)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer l.Close()

	l.Event(ExecStart, map[string]any{"cmd": "ls"})

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file to exist at %s: %v", path, err)
	}
}

func TestEvent_WritesValidJSONLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	l, err := New(path)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer l.Close()

	l.Event(ExecStart, map[string]any{
		"cmd":       "ls -la",
		"cmd_sha":   "deadbeef",
		"timeout_s": 30,
		"async":     true,
		"meta":      map[string]any{"ip": "127.0.0.1"},
	})

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		t.Fatalf("expected one line, got none; err=%v", sc.Err())
	}
	line := sc.Bytes()
	if sc.Scan() {
		t.Fatalf("expected exactly one line, got a second: %q", sc.Text())
	}

	var got map[string]any
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatalf("line is not valid JSON: %v\nline=%s", err, line)
	}

	if got["type"] != ExecStart {
		t.Errorf("type=%v, want %q", got["type"], ExecStart)
	}
	ts, ok := got["ts"].(string)
	if !ok || ts == "" {
		t.Errorf("ts missing or wrong type: %#v", got["ts"])
	}
	if got["cmd"] != "ls -la" {
		t.Errorf("cmd=%v, want ls -la", got["cmd"])
	}
	// JSON numbers decode to float64.
	if got["timeout_s"] != float64(30) {
		t.Errorf("timeout_s=%v, want 30", got["timeout_s"])
	}
	if got["async"] != true {
		t.Errorf("async=%v, want true", got["async"])
	}
	meta, ok := got["meta"].(map[string]any)
	if !ok || meta["ip"] != "127.0.0.1" {
		t.Errorf("meta=%v, want nested map with ip=127.0.0.1", got["meta"])
	}
}

func TestEvent_AppendBehavior(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")

	l1, err := New(path)
	if err != nil {
		t.Fatalf("New #1: %v", err)
	}
	l1.Event(ExecStart, map[string]any{"n": 1})
	l1.Event(ExecEnd, map[string]any{"n": 1})
	if err := l1.Close(); err != nil {
		t.Fatalf("Close #1: %v", err)
	}

	l2, err := New(path)
	if err != nil {
		t.Fatalf("New #2: %v", err)
	}
	defer l2.Close()
	l2.Event(ExecStart, map[string]any{"n": 2})
	l2.Event(ExecEnd, map[string]any{"n": 2})

	lines := readLines(t, path)
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines, got %d: %q", len(lines), lines)
	}
	// Oldest event should still be there.
	if lines[0]["n"] != float64(1) || lines[0]["type"] != ExecStart {
		t.Errorf("first line = %v, want n=1 type=exec_start", lines[0])
	}
	if lines[3]["n"] != float64(2) || lines[3]["type"] != ExecEnd {
		t.Errorf("last line = %v, want n=2 type=exec_end", lines[3])
	}
}

func TestEvent_NilFieldsMap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	l, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	l.Event(AuthOK, nil)

	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if lines[0]["type"] != AuthOK {
		t.Errorf("type=%v, want %q", lines[0]["type"], AuthOK)
	}
	if lines[0]["ts"] == "" {
		t.Errorf("expected ts to be auto-populated, got %v", lines[0])
	}
}

func TestEvent_Concurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	l, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	const goroutines = 50
	const perGoroutine = 20
	var wg sync.WaitGroup
	var seq uint64

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				id := atomic.AddUint64(&seq, 1)
				l.Event(ExecEnd, map[string]any{
					"goroutine": gid,
					"i":         i,
					"id":        id,
				})
			}
		}(g)
	}
	wg.Wait()

	lines := readLines(t, path)
	want := goroutines * perGoroutine
	if len(lines) != want {
		t.Fatalf("expected %d lines, got %d", want, len(lines))
	}

	// Every line must parse, must have type=exec_end, and every id in
	// 1..want must appear exactly once.
	seen := make(map[uint64]bool, want)
	for i, line := range lines {
		if line["type"] != ExecEnd {
			t.Errorf("line %d type=%v, want %q", i, line["type"], ExecEnd)
		}
		idF, ok := line["id"].(float64)
		if !ok {
			t.Fatalf("line %d missing numeric id: %v", i, line)
		}
		id := uint64(idF)
		if id < 1 || id > uint64(want) {
			t.Errorf("line %d id=%d out of range", i, id)
		}
		if seen[id] {
			t.Errorf("line %d duplicate id=%d", i, id)
		}
		seen[id] = true
	}
	if len(seen) != want {
		t.Errorf("expected %d distinct ids, got %d", want, len(seen))
	}
}

func TestEvent_CloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	l, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	// Event after close must not panic.
	l.Event(ExecStart, map[string]any{"after_close": true})
}

func TestEvent_TypeConstantsAreDistinct(t *testing.T) {
	all := []string{ExecStart, ExecEnd, AuthOK, AuthFail, UpdateApplied, UpdateFailed, ServiceAction, HeartbeatSent, PollJob, GenericError}
	sorted := append([]string(nil), all...)
	sort.Strings(sorted)
	for i := 1; i < len(sorted); i++ {
		if sorted[i] == sorted[i-1] {
			t.Errorf("duplicate event type constant: %q", sorted[i])
		}
	}
}

// --- helpers ---

func readLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	// Allow long-ish audit lines just in case.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		b := sc.Bytes()
		if len(b) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("invalid JSON line %q: %v", b, err)
		}
		out = append(out, m)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return out
}
func TestLoggerRotatesWhenLimitReached(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	logger, err := NewWithLimit(path, 160)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		logger.Event("test", map[string]any{"payload": strings.Repeat("x", 40), "n": i})
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 320 {
		t.Fatalf("audit log did not rotate, size=%d", info.Size())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"n":19`)) {
		t.Fatalf("latest event missing: %s", data)
	}
}
