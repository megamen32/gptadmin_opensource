package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Fast units for the reader that silently cut real 44 queue JSON at 1 MiB.
// Expected <1s, maximum 10s; only local reader logic is claimed.
func TestQueueResponseReadsLargeEnvelope(t *testing.T) {
	payload := strings.Repeat("x", 1748195)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("max_response_bytes") != "67108864" {
			t.Error("queue reader bound not advertised")
		}
		json.NewEncoder(w).Encode(QueueJob{ID: "same-job", ToolName: "mcp_call", Arguments: map[string]any{"payload": payload}})
	}))
	defer server.Close()
	job, found, err := New(server.URL, nil, "", "", "").PollQueue(context.Background(), Beat{Name: "host"}, 1)
	if err != nil || !found || job.ID != "same-job" || job.Arguments["payload"] != payload {
		t.Fatalf("large envelope lost: found=%v err=%v", found, err)
	}
}

type queueResponseTransport struct{ body io.ReadCloser }

func (t queueResponseTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: t.body, Header: make(http.Header)}, nil
}

type queueResponseBody struct {
	io.Reader
	closed bool
}

func (b *queueResponseBody) Close() error { b.closed = true; return nil }

type queueResponseBrokenReader struct{}

func (queueResponseBrokenReader) Read(p []byte) (int, error) {
	copy(p, `{"id":`)
	return 6, io.ErrUnexpectedEOF
}

func TestQueueResponseRejectsOverflowAndReadFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reader io.Reader
		want   error
	}{
		{"overflow", strings.NewReader(strings.Repeat("x", 33)), nil},
		{"partial", queueResponseBrokenReader{}, io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &queueResponseBody{Reader: tc.reader}
			client := &Client{BaseURL: "http://hub.invalid", HTTP: &http.Client{Transport: queueResponseTransport{body}}}
			resp, data, err := client.doWithResponseLimit(context.Background(), http.MethodGet, "/queue/host", nil, 32)
			if err == nil || resp != nil || data != nil || !body.closed || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("partial response escaped: closed=%v err=%v", body.closed, err)
			}
		})
	}
}

func TestQueueResponseClosesOriginalBody(t *testing.T) {
	body := &queueResponseBody{Reader: strings.NewReader(`{"id":"one"}`)}
	client := &Client{BaseURL: "http://hub.invalid", HTTP: &http.Client{Transport: queueResponseTransport{body}}}
	resp, data, err := client.do(context.Background(), http.MethodGet, "/ready", nil)
	if err != nil || !body.closed || string(data) != `{"id":"one"}` {
		t.Fatalf("body lifecycle: %v", err)
	}
	defer resp.Body.Close()
	again, err := io.ReadAll(resp.Body)
	if err != nil || string(again) != string(data) {
		t.Fatal("returned buffered response changed")
	}
}
