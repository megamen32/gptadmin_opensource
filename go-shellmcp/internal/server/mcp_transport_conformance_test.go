package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCPHTTPUsesStatelessStreamableHTTPContract(t *testing.T) {
	s := New(Config{Token: "test", SpillDir: t.TempDir()})
	defer s.Close()

	initialize := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`))
	initialize.Header.Set("Authorization", "Bearer test")
	initialize.Header.Set("Accept", "application/json, text/event-stream")
	initialize.Header.Set("Content-Type", "application/json")
	initializeRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(initializeRec, initialize)
	if initializeRec.Code != http.StatusOK {
		t.Fatalf("initialize status=%d body=%s", initializeRec.Code, initializeRec.Body.String())
	}
	if sessionID := initializeRec.Header().Get("Mcp-Session-Id"); sessionID != "" {
		t.Fatalf("stateless server invented session %q", sessionID)
	}
	if !strings.Contains(initializeRec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("initialize Content-Type=%q", initializeRec.Header().Get("Content-Type"))
	}

	initialized := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`))
	initialized.Header.Set("Authorization", "Bearer test")
	initialized.Header.Set("Accept", "application/json, text/event-stream")
	initialized.Header.Set("Content-Type", "application/json")
	initializedRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(initializedRec, initialized)
	if initializedRec.Code != http.StatusAccepted || initializedRec.Body.Len() != 0 {
		t.Fatalf("initialized status=%d body=%q want 202 with no body", initializedRec.Code, initializedRec.Body.String())
	}

	get := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	get.Header.Set("Authorization", "Bearer test")
	get.Header.Set("Accept", "text/event-stream")
	getRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(getRec, get)
	if getRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status=%d body=%s want 405 when no server stream is offered", getRec.Code, getRec.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/mcp", nil)
	deleteReq.Header.Set("Authorization", "Bearer test")
	deleteRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE status=%d body=%s want 405 for stateless transport", deleteRec.Code, deleteRec.Body.String())
	}
}
