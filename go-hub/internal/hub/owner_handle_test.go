package hub

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOwnerHandle(t *testing.T) {
	for kind, want := range map[string]string{"task": "ot1.bm9kZS0x.3ff1", "content": "oc1.bm9kZS0x.3ff1"} {
		id, err := encodeOwnerHandle(kind, "node-1", "3ff1")
		if err != nil || id != want {
			t.Fatal(id, err)
		}
		h, scoped, err := decodeOwnerHandle(id)
		if err != nil || !scoped || h != (ownerHandle{kind, "node-1", "3ff1"}) {
			t.Fatal(h, scoped, err)
		}
	}
	if _, scoped, err := decodeOwnerHandle("existing-opaque-id"); scoped || err != nil {
		t.Fatal("legacy ID treated as scoped")
	}
	for _, id := range []string{"ot1", "oc1.", "ot1..x", "ot1.YQ==.x", "ot1.YR.x", "oc1.YQ./file", "ot1.YQ.x.y", "ot1.YQ."} {
		if _, scoped, err := decodeOwnerHandle(id); !scoped || err == nil {
			t.Errorf("accepted malformed handle %q", id)
		}
	}
}

func TestNewOwnerTaskIDUsesHubIssuanceTime(t *testing.T) {
	issued := time.Date(2026, 11, 20, 13, 27, 43, 125000000, time.FixedZone("UTC+3", 3*3600))
	reads := 0
	s := &Server{cfg: Config{Now: func() time.Time {
		reads++
		return issued
	}}}
	seen := map[string]bool{}
	for _, owner := range []string{"", "not/routable", strings.Repeat("m", 64), "node.a", "node.a"} {
		id, created := s.newOwnerTaskID(owner)
		if created != float64(issued.Unix())+.125 || seen[id] {
			t.Fatal("lost creation time or reused nonce")
		}
		seen[id] = true
		h, scoped, err := decodeOwnerHandle(id)
		if err != nil || scoped != validOwnerID(owner) {
			t.Fatalf("routing of %q: %v", id, err)
		}
		local := id
		if scoped {
			if h.Kind != "task" || h.OwnerID != owner {
				t.Fatal("changed task owner")
			}
			local = h.LocalID
		}
		nonce, ok := strings.CutPrefix(local, "t1_"+strconv.FormatInt(issued.Unix(), 10)+"_")
		raw, err := hex.DecodeString(nonce)
		if !ok || err != nil || len(raw) != 16 || hex.EncodeToString(raw) != nonce {
			t.Fatalf("bad issuance or nonce %q", local)
		}
	}
	if reads != len(seen) {
		t.Fatalf("%d clock samples for %d tasks", reads, len(seen))
	}
	issued = issued.Truncate(time.Second).Add(time.Second - time.Nanosecond)
	id, created := s.newOwnerTaskID("")
	if int64(created) != issued.Unix() || !strings.HasPrefix(id, "t1_"+strconv.FormatInt(issued.Unix(), 10)+"_") {
		t.Fatal("rounding the creation timestamp moved the task into the next second")
	}
}

func TestOwnerTaskIDSurvivesDelayedPollAndKeyedRetry(t *testing.T) {
	var clock atomic.Int64
	start := time.Now().Add(-time.Minute).Truncate(time.Second)
	clock.Store(start.Unix())
	dir := t.TempDir()
	cfg := Config{ConfigDir: dir, Now: func() time.Time { return time.Unix(clock.Load(), 0) }}
	s := New(cfg)
	defer s.Close()
	submit := func(owner *Server) string {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/mcp-relay/call", nil)
		body, status := owner.executeMCPTool(r, "shell:phone", "shell_exec", map[string]any{"cmd": "printf receipt"}, true, time.Second, "stable-request")
		id := firstString(body, "job_id")
		if status != http.StatusOK || id == "" {
			t.Fatalf("submission HTTP %d, job ID missing", status)
		}
		return id
	}
	id := submit(s)
	state, err := s.readTaskRecords(id)
	if err != nil || state.Shell[id].CreatedAt != float64(start.Unix()) {
		t.Fatal("creation time was not persisted")
	}
	h, scoped, err := decodeOwnerHandle(id)
	local := id
	if scoped {
		local = h.LocalID
	}
	if err != nil || !strings.HasPrefix(local, "t1_"+strconv.FormatInt(start.Unix(), 10)+"_") {
		t.Fatal("stored ID was not dated at creation")
	}
	clock.Add(3600)
	if submit(s) != id {
		t.Fatal("retry reminted timestamp")
	}
	poll := httptest.NewRecorder()
	s.pollShellQueue(poll, httptest.NewRequest(http.MethodGet, "/queue/phone?timeout=1", nil), "phone")
	var wire map[string]any
	if err := json.Unmarshal(poll.Body.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	if poll.Code != http.StatusOK || firstString(wire, "id") != id {
		t.Fatal("dequeue reminted the task")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := New(cfg)
	defer reopened.Close()
	if submit(reopened) != id {
		t.Fatal("restart reminted an already dispatched task")
	}
}
