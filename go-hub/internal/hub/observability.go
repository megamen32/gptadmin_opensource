package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
)

const requestTraceHeader = "X-Request-ID"
const traceParentHeader = "traceparent"

type requestTraceContextKey struct{}
type requestTraceParentContextKey struct{}

type traceParent struct {
	TraceID string
	Flags   string
}

// withRequestTrace assigns a bounded, non-secret correlation identifier to
// every HTTP request and exposes the same value in the response header. The
// identifier is useful for joining audit/job events without storing payloads.
func withRequestTrace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := normalizeRequestTraceID(r.Header.Get(requestTraceHeader))
		incomingParent, hasIncomingParent := parseTraceParent(r.Header.Get(traceParentHeader))
		if hasIncomingParent {
			traceID = incomingParent.TraceID
		}
		if traceID == "" {
			traceID = newRequestTraceID()
		}
		parentTraceID := traceID
		flags := "00"
		if hasIncomingParent {
			flags = incomingParent.Flags
		}
		if !isW3CTraceID(parentTraceID) {
			parentTraceID = newW3CTraceID()
		}
		parent := formatTraceParent(parentTraceID, flags)
		ctx := context.WithValue(r.Context(), requestTraceContextKey{}, traceID)
		ctx = context.WithValue(ctx, requestTraceParentContextKey{}, parent)
		r = r.WithContext(ctx)
		w.Header().Set(requestTraceHeader, traceID)
		w.Header().Set(traceParentHeader, parent)
		next.ServeHTTP(w, r)
	})
}

func requestTraceID(r *http.Request) string {
	if r == nil {
		return ""
	}
	if traceID, ok := r.Context().Value(requestTraceContextKey{}).(string); ok {
		return traceID
	}
	return normalizeRequestTraceID(r.Header.Get(requestTraceHeader))
}

func requestTraceParent(r *http.Request) string {
	if r == nil {
		return ""
	}
	if parent, ok := r.Context().Value(requestTraceParentContextKey{}).(string); ok {
		return parent
	}
	return ""
}

func normalizeRequestTraceID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return ""
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' && char != '_' && char != '.' {
			return ""
		}
	}
	return value
}

func newRequestTraceID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "generated"
	}
	return hex.EncodeToString(raw[:])
}

func newW3CTraceID() string {
	return newRequestTraceID()
}

func newW3CSpanID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "0000000000000001"
	}
	return hex.EncodeToString(raw[:])
}

func formatTraceParent(traceID, flags string) string {
	if len(flags) != 2 {
		flags = "00"
	}
	return "00-" + strings.ToLower(traceID) + "-" + newW3CSpanID() + "-" + strings.ToLower(flags)
}

func parseTraceParent(value string) (traceParent, bool) {
	value = strings.TrimSpace(value)
	if len(value) != 55 || value[2] != '-' || value[35] != '-' || value[52] != '-' {
		return traceParent{}, false
	}
	version, err := hex.DecodeString(value[:2])
	if err != nil || len(version) != 1 || version[0] == 0xff {
		return traceParent{}, false
	}
	traceID, err := hex.DecodeString(value[3:35])
	if err != nil || len(traceID) != 16 || allZero(traceID) {
		return traceParent{}, false
	}
	spanID, err := hex.DecodeString(value[36:52])
	if err != nil || len(spanID) != 8 || allZero(spanID) {
		return traceParent{}, false
	}
	flags, err := hex.DecodeString(value[53:55])
	if err != nil || len(flags) != 1 {
		return traceParent{}, false
	}
	return traceParent{TraceID: strings.ToLower(value[3:35]), Flags: strings.ToLower(value[53:55])}, true
}

func isW3CTraceID(value string) bool {
	parsed, ok := parseTraceParent("00-" + value + "-0000000000000001-00")
	return ok && parsed.TraceID == strings.ToLower(value)
}

func allZero(value []byte) bool {
	for _, b := range value {
		if b != 0 {
			return false
		}
	}
	return true
}
