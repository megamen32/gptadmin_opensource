package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// These tests exercise an archived source snapshot in a standalone scratch
// directory. They prove classification/redaction, not a deployed integration.
type syntheticFailingSchemaReader struct {
	err   error
	calls int
}

func (*syntheticFailingSchemaReader) ActiveCallbacks() int { return 0 }
func (*syntheticFailingSchemaReader) OutboxPending() int   { return 0 }
func (r *syntheticFailingSchemaReader) DirectChildTools(context.Context, map[string]any) (map[string]any, error) {
	r.calls++
	return nil, r.err
}

func requireSyntheticSchemaFailure(t *testing.T, got map[string]any, stage, reason string, httpStatus int) string {
	t.Helper()
	if firstString(got, "status") != "failed" {
		t.Fatalf("schema unexpectedly admitted: %#v", got)
	}
	err := mapValue(got["error"])
	if firstString(err, "code") != "child_schema_unavailable" {
		t.Fatalf("legacy failure code changed: %#v", err)
	}
	diag := mapValue(err["diagnostic"])
	if firstString(diag, "stage") != stage || firstString(diag, "reason_code") != reason {
		t.Fatalf("wrong safe diagnostic: stage=%q reason=%q, wanted %q/%q",
			firstString(diag, "stage"), firstString(diag, "reason_code"), stage, reason)
	}
	if httpStatus > 0 {
		if diag["upstream_status"] != httpStatus {
			t.Fatalf("upstream status mismatch: got %#v, want %d", diag["upstream_status"], httpStatus)
		}
	} else if _, present := diag["upstream_status"]; present {
		t.Fatalf("invented an upstream status: %#v", diag)
	}
	correlation := firstString(diag, "correlation_id")
	if !regexp.MustCompile("^[a-f0-9]{24}$").MatchString(correlation) {
		t.Fatalf("correlation is not an opaque random receipt ID: length=%d", len(correlation))
	}
	return correlation
}

func TestSyntheticLocalChildSchemaFailureReasons(t *testing.T) {
	cases := []struct {
		name, reason string
		err          error
	}{
		{"unknown_secret", "unknown", errors.New("SECRET_LOCAL_COOKIE must remain private")},
		{"deadline_wrapped", "timeout", fmt.Errorf("SECRET_LOCAL_DEADLINE: %w", context.DeadlineExceeded)},
		{"cancelled", "cancelled", context.Canceled},
		{"exact_notification_sentinel", "notification_limit", errors.New("mcp child: too many notifications before response")},
		{"lookalike_notification_secret", "unknown", errors.New("mcp child: too many notifications before response; SECRET_TOKEN")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, child, remoteCalls := directSchemaFixture(t, func(w http.ResponseWriter, r *http.Request) {
				t.Error("local schema reached remote owner")
			})
			reader := &syntheticFailingSchemaReader{err: tc.err}
			s.mu.Lock()
			if s.localExecutors == nil {
				s.localExecutors = make(map[string]map[string]string)
			}
			s.localExecutors["shell:peer"] = map[string]string{"server_id": "synthetic-local"}
			s.nodeRuntime = reader
			s.mu.Unlock()
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			req.Header.Set("Authorization", "Bearer synthetic-fixture")
			oldLogOutput := log.Writer()
			var logs bytes.Buffer
			log.SetOutput(&logs)
			got := s.directChildSchemaForRequest(req, child)
			log.SetOutput(oldLogOutput)
			correlation := requireSyntheticSchemaFailure(t, got, "local_reader", tc.reason, 0)
			if !strings.Contains(logs.String(), "correlation_id="+correlation) ||
				!strings.Contains(logs.String(), "reason_code="+tc.reason) {
				t.Fatalf("safe correlation was not logged")
			}
			body, _ := json.Marshal(got)
			if strings.Contains(string(body), "SECRET_") ||
				strings.Contains(logs.String(), "SECRET_") ||
				strings.Contains(logs.String(), "synthetic-fixture") {
				t.Fatalf("upstream error or authorization marker leaked")
			}
			if reader.calls != 1 || remoteCalls.Load() != 0 {
				t.Fatalf("unexpected effects: local=%d remote=%d", reader.calls, remoteCalls.Load())
			}
			s.mu.Lock()
			jobs := len(s.shellJobs) + len(s.relayJobs) + len(s.shellQueues)
			s.mu.Unlock()
			if jobs != 0 {
				t.Fatalf("schema failure queued work: jobs=%d", jobs)
			}
		})
	}
}

func TestSyntheticRemoteChildSchemaFailureReasons(t *testing.T) {
	cases := []struct {
		name, body, stage, reason string
		status                    int
	}{
		{"owner_denied_403", "{\"detail\":\"SECRET_REMOTE_CREDENTIAL\"}", "remote_owner", "owner_denied", 403},
		{"owner_unauthorized_401", "{\"detail\":\"SECRET_REMOTE_SESSION\"}", "remote_owner", "owner_denied", 401},
		{"owner_timeout_504", "{\"error\":\"SECRET_REMOTE_OWNER\"}", "remote_owner", "timeout", 504},
		{"owner_http_503", "{\"detail\":\"SECRET_REMOTE_FILE\"}", "remote_owner", "upstream_http_failure", 503},
		{"owner_malformed_proxy_failure", "{\"token\":\"SECRET_REMOTE_UNTERMINATED\"", "remote_owner", "upstream_http_failure", 502},
		{"owner_status_failed", "{\"status\":\"failed\",\"error\":\"SECRET_REMOTE_ERROR\"}", "remote_owner", "owner_status_unavailable", 200},
		{"invalid_tool_shape", "{\"status\":\"completed\",\"response\":{\"tools\":{\"SECRET_REMOTE_PAYLOAD\":true}}}", "response_validation", "invalid_tools", 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, child, calls := directSchemaFixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				upstreamStatus := tc.status
				if tc.name == "owner_malformed_proxy_failure" {
					upstreamStatus = http.StatusOK
				}
				w.WriteHeader(upstreamStatus)
				_, _ = w.Write([]byte(tc.body))
			})
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			req.Header.Set("Authorization", "Bearer schema-test-token")
			oldLogOutput := log.Writer()
			var logs bytes.Buffer
			log.SetOutput(&logs)
			got := s.directChildSchemaForRequest(req, child)
			log.SetOutput(oldLogOutput)
			correlation := requireSyntheticSchemaFailure(t, got, tc.stage, tc.reason, tc.status)
			if !strings.Contains(logs.String(), "correlation_id="+correlation) ||
				!strings.Contains(logs.String(), "reason_code="+tc.reason) {
				t.Fatalf("safe correlation was not logged")
			}
			body, _ := json.Marshal(got)
			if strings.Contains(string(body), "SECRET_") ||
				strings.Contains(logs.String(), "SECRET_") ||
				strings.Contains(logs.String(), "schema-test-token") {
				t.Fatalf("owner response, token or payload leaked")
			}
			if calls.Load() != 1 {
				t.Fatalf("unexpected remote request count: %d", calls.Load())
			}
			s.mu.Lock()
			jobs := len(s.shellJobs) + len(s.relayJobs) + len(s.shellQueues)
			s.mu.Unlock()
			if jobs != 0 {
				t.Fatalf("schema failure enqueued durable jobs: %d", jobs)
			}
		})
	}
}

func TestSyntheticChildSchemaAccessDenialFailsClosed(t *testing.T) {
	s, child, calls := directSchemaFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("denied profile contacted owner")
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req = requestWithAccessProfile(req, AccessProfile{
		ID: "synthetic-denied", Version: 1, AccessMode: "full",
		AllowedTargets: []string{"mcp:shell:other:sample"},
	})
	req.Header.Set("Authorization", "Bearer schema-test-token")
	got := s.directChildSchemaForRequest(req, child)
	requireSyntheticSchemaFailure(t, got, "authorization", "access_denied", 0)
	if calls.Load() != 0 {
		t.Fatal("denied profile reached the child owner")
	}
}

func TestSyntheticChildSchemaOwnerMetadataFailsClosed(t *testing.T) {
	s, child, calls := directSchemaFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("unapproved owner was contacted")
	})
	s.mu.Lock()
	s.agents["shell:peer"].Meta["approved"] = false
	s.mu.Unlock()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer schema-test-token")
	got := s.directChildSchemaForRequest(req, child)
	requireSyntheticSchemaFailure(t, got, "owner_lookup", "metadata_unavailable", 0)
	if calls.Load() != 0 {
		t.Fatal("owner metadata deny reached upstream")
	}
}

func TestSyntheticMalformedChildSchemaOwnerRawReply(t *testing.T) {
	// Unit-only fixture: the normal peer relay intentionally normalizes malformed
	// owner JSON to HTTP 502 before it reaches the direct child-schema parser.
	if _, ok := childSchemaParseOwnerReply([]byte("{")); ok {
		t.Fatal("malformed upstream response parsed successfully")
	}
	valid, _ := json.Marshal(map[string]any{
		"status":   "completed",
		"response": map[string]any{"tools": []any{}},
	})
	if reply, ok := childSchemaParseOwnerReply(valid); !ok ||
		firstString(reply, "status") != "completed" {
		t.Fatalf("valid upstream JSON was not recognized")
	}
}
