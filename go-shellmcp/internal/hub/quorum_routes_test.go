package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/megamen32/gptadmin/go-shellmcp/hubroute"
)

func TestPollingLearnsRoutesAndRetriesOnlyTheOutboxReceipt(t *testing.T) {
	ctx := context.Background()
	var posted atomic.Int32
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/queue/android/result" {
			t.Errorf("unexpected alternate route %s %s", r.Method, r.URL.Path)
			return
		}
		var receipt TaskResult
		if json.NewDecoder(r.Body).Decode(&receipt) != nil || receipt.ID != "accepted" {
			t.Error("outbox lost the original task ID")
		}
		posted.Add(1)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer other.Close()
	seed := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/queue/android" || r.Header.Get("Authorization") != "Bearer fixture" {
			w.WriteHeader(403)
			return
		}
		w.Header().Set(hubroute.Header, other.URL)
		w.Write([]byte(`{}`))
	}))
	defer seed.Close()
	dir := t.TempDir()
	makeClient := func() *Client {
		client := New(seed.URL, nil, "fixture", "", "")
		client.HTTP, client.unpinned = seed.Client(), other.Client()
		client.RestoreQuorumRoutes(dir)
		return client
	}
	client := makeClient()
	if _, job, err := client.PollQueue(ctx, Beat{Name: "android"}, 1); err != nil || job {
		t.Fatalf("idle TLS poll: job=%v err=%v", job, err)
	}
	seed.Close()
	client = makeClient() // Android may restart while the initial Hub is down.
	payload := TaskResult{ID: "accepted", Result: "done"}
	if err := client.PostResult(ctx, "android", payload); err == nil || posted.Load() != 0 {
		t.Fatal("failed delivery was replayed within the same HTTP request")
	}
	if err := client.PostResult(ctx, "android", payload); err != nil || posted.Load() != 1 {
		t.Fatalf("subsequent outbox delivery failed: %v", err)
	}
	unrelated := New("https://another.example", nil, "fixture", "", "")
	unrelated.RestoreQuorumRoutes(dir)
	if unrelated.routes.Current() != "" {
		t.Fatal("another configured Hub inherited the old enrollment's routes")
	}
}

func TestResultNeedsPositiveHubAcknowledgement(t *testing.T) {
	for _, body := range []string{"", `{}`, `{"ok":false}`, "<html>gateway</html>"} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(body))
		}))
		client := New(s.URL, nil, "fixture", "", "")
		err := client.PostResult(context.Background(), "android", TaskResult{ID: "pending"})
		s.Close()
		if err == nil || !strings.Contains(err.Error(), "acknowledge") {
			t.Fatalf("unconfirmed receipt would be removed from outbox")
		}
	}
}

func TestScopedReceiptRequiresOwnerAndCompleteJobID(t *testing.T) {
	const id = "ot1.bm9kZS0x.job"
	// Exact-number preservation covers outboxes created by earlier versions.
	const data = "{\"id\":\"" + id + "\",\"result\":{\"n\":9007199254740993}}"
	var ack map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw struct{ Result struct{ N json.Number } }
		if r.URL.Query().Get("job_id") != id || json.NewDecoder(r.Body).Decode(&raw) != nil || raw.Result.N != "9007199254740993" {
			t.Error("receipt body/route changed")
		}
		_ = json.NewEncoder(w).Encode(ack)
	}))
	defer srv.Close()
	client := New(srv.URL, nil, "", "", "")
	for _, tc := range []struct {
		ack   map[string]any
		valid bool
	}{
		{map[string]any{"ok": true}, false},
		{map[string]any{"ok": true, "id": "job", "owner_id": "node-1"}, false},
		{map[string]any{"ok": true, "id": id, "owner_id": "another"}, false},
		{map[string]any{"ok": true, "id": id, "owner_id": "node-1"}, true},
	} {
		ack = tc.ack
		if err := client.PostResultJSON(context.Background(), "phone", id, []byte(data)); (err == nil) != tc.valid {
			t.Fatalf("unexpected acknowledgement %v", err)
		}
	}
}
