package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const instructionSetPath = "/admin/api/instruction-sets/default"

const namedInstructionSetPath = "/admin/api/instruction-sets/ops"

type instructionSetResponse struct {
	ID        string     `json:"id"`
	Content   string     `json:"content"`
	Version   string     `json:"version"`
	UpdatedAt *time.Time `json:"updated_at"`
}

func instructionSetServer(t *testing.T) (*Server, Config) {
	t.Helper()
	configDir := t.TempDir()
	cfg := Config{
		CtlToken:                "ctl",
		ConfigDir:               configDir,
		StartupInstructionsFile: filepath.Join(configDir, "startup_instructions.md"),
	}
	return New(cfg), cfg
}

func instructionSetRequest(t *testing.T, s *Server, method, path string, body []byte, etag string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer ctl")
	if etag != "" {
		req.Header.Set("If-Match", etag)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}

func decodeInstructionSet(t *testing.T, w *httptest.ResponseRecorder) instructionSetResponse {
	t.Helper()
	var got instructionSetResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode instruction set: %v; body=%s", err, w.Body.String())
	}
	return got
}

func mcpResult(t *testing.T, s *Server, id int, method string, params string) map[string]any {
	t.Helper()
	payload := `{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"` + method + `","params":` + params + `}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer ctl")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("MCP %s status=%d body=%s", method, w.Code, w.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response["result"].(map[string]any)
}

type blockingInstructionBody struct {
	prefix      []byte
	offset      int
	blocked     chan struct{}
	release     chan struct{}
	blockedOnce sync.Once
	releaseOnce sync.Once
}

func newBlockingInstructionBody(prefix []byte) *blockingInstructionBody {
	return &blockingInstructionBody{prefix: prefix, blocked: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockingInstructionBody) Read(p []byte) (int, error) {
	if b.offset < len(b.prefix) {
		n := copy(p, b.prefix[b.offset:])
		b.offset += n
		return n, nil
	}
	b.blockedOnce.Do(func() { close(b.blocked) })
	<-b.release
	return 0, io.EOF
}

func (b *blockingInstructionBody) Close() error {
	b.releaseOnce.Do(func() { close(b.release) })
	return nil
}

func serveMCPInitialize(s *Server) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	req.Header.Set("Authorization", "Bearer ctl")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}

func TestDefaultInstructionSetHTTPContract(t *testing.T) {
	s, _ := instructionSetServer(t)
	w := instructionSetRequest(t, s, http.MethodGet, instructionSetPath, nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", w.Code, w.Body.String())
	}
	got := decodeInstructionSet(t, w)
	if got.ID != "default" || got.Content == "" || got.Version == "" {
		t.Fatalf("instruction set=%+v, want default non-empty content and version", got)
	}
	if w.Header().Get("ETag") == "" {
		t.Fatal("GET did not return ETag")
	}
}

func TestNamedInstructionSetCRUDAndProfileInitialize(t *testing.T) {
	s, cfg := instructionSetServer(t)
	missingProfile := accessProfileTestRequest(t, s, http.MethodPut, "/admin/api/access-profiles/missing", map[string]any{
		"id":                 "missing",
		"instruction_set_id": "does-not-exist",
		"access_mode":        accessModeFull,
		"approval_mode":      approvalModeBoundedAutonomous,
	}, map[string]string{"If-Match": "*"})
	if missingProfile.Code != http.StatusNotFound {
		t.Fatalf("missing instruction-set profile status=%d body=%s, want %d", missingProfile.Code, missingProfile.Body.String(), http.StatusNotFound)
	}
	content := "Operate only inside the approved maintenance window."
	body, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		t.Fatal(err)
	}
	created := instructionSetRequest(t, s, http.MethodPut, namedInstructionSetPath, body, "*")
	if created.Code != http.StatusOK {
		t.Fatalf("named PUT status=%d body=%s", created.Code, created.Body.String())
	}
	if got := decodeInstructionSet(t, created); got.ID != "ops" || got.Content != content || got.Version == "" {
		t.Fatalf("created named instruction set=%+v", got)
	}
	stateInfo, err := os.Stat(filepath.Join(cfg.ConfigDir, instructionSetsStateFilename))
	if err != nil {
		t.Fatalf("named instruction state was not persisted: %v", err)
	}
	if stateInfo.Mode().Perm() != 0o600 {
		t.Fatalf("named instruction state mode=%o, want 0600", stateInfo.Mode().Perm())
	}

	profileBody := map[string]any{
		"id":                 "ops",
		"name":               "Operations",
		"instruction_set_id": "ops",
		"access_mode":        accessModeFull,
		"approval_mode":      approvalModeBoundedAutonomous,
	}
	profile := accessProfileTestRequest(t, s, http.MethodPut, "/admin/api/access-profiles/ops", profileBody, map[string]string{"If-Match": "*"})
	if profile.Code != http.StatusOK {
		t.Fatalf("profile PUT status=%d body=%s", profile.Code, profile.Body.String())
	}

	initialize := func(server *Server) map[string]any {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
		req = requestWithAccessProfile(req, AccessProfile{ID: "ops", InstructionSetID: "ops"})
		result, rpcErr, noContent := server.agentMCPJSONRPC(req, Agent{AgentID: "hub"}, map[string]any{"method": "initialize", "params": map[string]any{}})
		if rpcErr != nil || noContent {
			t.Fatalf("profile initialize result=%v error=%v no_content=%v", result, rpcErr, noContent)
		}
		return result.(map[string]any)
	}
	if got := initialize(s)["instructions"]; got != content {
		t.Fatalf("profile instructions=%q, want %q", got, content)
	}

	restarted := New(cfg)
	loaded := instructionSetRequest(t, restarted, http.MethodGet, namedInstructionSetPath, nil, "")
	if loaded.Code != http.StatusOK || decodeInstructionSet(t, loaded).Content != content {
		t.Fatalf("named instruction set did not survive restart: status=%d body=%s", loaded.Code, loaded.Body.String())
	}
	if got := initialize(restarted)["instructions"]; got != content {
		t.Fatalf("restarted profile instructions=%q, want %q", got, content)
	}

	deletedWhileBound := instructionSetRequest(t, restarted, http.MethodDelete, namedInstructionSetPath, nil, "")
	if deletedWhileBound.Code != http.StatusConflict {
		t.Fatalf("bound DELETE status=%d body=%s, want %d", deletedWhileBound.Code, deletedWhileBound.Body.String(), http.StatusConflict)
	}
}

func TestInstructionSetPutRequiresCurrentETagAndPublishesAtomically(t *testing.T) {
	s, cfg := instructionSetServer(t)
	initial := instructionSetRequest(t, s, http.MethodGet, instructionSetPath, nil, "")
	currentETag := initial.Header().Get("ETag")
	if currentETag == "" {
		t.Fatal("missing initial ETag")
	}

	updated := "Use the approved maintenance window."
	body, err := json.Marshal(map[string]string{"content": updated})
	if err != nil {
		t.Fatal(err)
	}
	put := instructionSetRequest(t, s, http.MethodPut, instructionSetPath, body, currentETag)
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", put.Code, put.Body.String())
	}
	if put.Header().Get("ETag") == currentETag || put.Header().Get("ETag") == "" {
		t.Fatalf("PUT ETag=%q, want a new non-empty version", put.Header().Get("ETag"))
	}

	initialized := mcpResult(t, s, 1, "initialize", `{}`)
	if initialized["instructions"] != updated {
		t.Fatalf("initialize instructions=%q, want %q", initialized["instructions"], updated)
	}
	resource := mcpResult(t, s, 2, "resources/read", `{"uri":"gptadmin://startup-instructions"}`)
	contents := resource["contents"].([]any)
	if contents[0].(map[string]any)["text"] != updated {
		t.Fatalf("startup resource=%v, want %q", contents, updated)
	}

	staleBody, err := json.Marshal(map[string]string{"content": "must not publish"})
	if err != nil {
		t.Fatal(err)
	}
	stale := instructionSetRequest(t, s, http.MethodPut, instructionSetPath, staleBody, currentETag)
	if stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale PUT status=%d body=%s", stale.Code, stale.Body.String())
	}
	active := decodeInstructionSet(t, instructionSetRequest(t, s, http.MethodGet, instructionSetPath, nil, ""))
	if active.Content != updated {
		t.Fatalf("stale PUT changed active content to %q", active.Content)
	}

	if _, err := os.Stat(cfg.StartupInstructionsFile); err != nil {
		t.Fatalf("instruction file was not persisted: %v", err)
	}
	restarted := New(cfg)
	persisted := decodeInstructionSet(t, instructionSetRequest(t, restarted, http.MethodGet, instructionSetPath, nil, ""))
	if persisted.Content != updated {
		t.Fatalf("new Server loaded content=%q, want %q", persisted.Content, updated)
	}
}

func TestInstructionSetPUTRejectsStaleETagAcrossServerInstances(t *testing.T) {
	configDir := t.TempDir()
	cfg := Config{
		CtlToken:                "ctl",
		ConfigDir:               configDir,
		StartupInstructionsFile: filepath.Join(configDir, "startup_instructions.md"),
	}
	first := New(cfg)
	second := New(cfg)

	firstInitial := instructionSetRequest(t, first, http.MethodGet, instructionSetPath, nil, "")
	secondInitial := instructionSetRequest(t, second, http.MethodGet, instructionSetPath, nil, "")
	firstETag := firstInitial.Header().Get("ETag")
	secondETag := secondInitial.Header().Get("ETag")
	if firstETag == "" || firstETag != secondETag {
		t.Fatalf("initial ETags differ: first=%q second=%q", firstETag, secondETag)
	}

	firstContent := "Published by the first Server instance."
	firstBody := []byte(`{"content":"` + firstContent + `"}`)
	if w := instructionSetRequest(t, first, http.MethodPut, instructionSetPath, firstBody, firstETag); w.Code != http.StatusOK {
		t.Fatalf("first PUT status=%d body=%s", w.Code, w.Body.String())
	}

	secondBody := []byte(`{"content":"Must not overwrite the first instance."}`)
	stale := instructionSetRequest(t, second, http.MethodPut, instructionSetPath, secondBody, secondETag)
	if stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale cross-instance PUT status=%d body=%s, want 412", stale.Code, stale.Body.String())
	}
	if got, err := os.ReadFile(cfg.StartupInstructionsFile); err != nil {
		t.Fatalf("read published instructions: %v", err)
	} else if string(got) != firstContent {
		t.Fatalf("stale cross-instance PUT changed file to %q, want %q", got, firstContent)
	}
}

func TestInstructionSetReadsRefreshAcrossServerInstances(t *testing.T) {
	configDir := t.TempDir()
	cfg := Config{
		CtlToken:                "ctl",
		ConfigDir:               configDir,
		StartupInstructionsFile: filepath.Join(configDir, "startup_instructions.md"),
	}
	first := New(cfg)
	second := New(cfg)

	initial := instructionSetRequest(t, second, http.MethodGet, instructionSetPath, nil, "")
	content := "Refresh file-backed instructions on every Server."
	if w := instructionSetRequest(t, first, http.MethodPut, instructionSetPath, []byte(`{"content":"`+content+`"}`), initial.Header().Get("ETag")); w.Code != http.StatusOK {
		t.Fatalf("first PUT status=%d body=%s", w.Code, w.Body.String())
	}

	refreshed := decodeInstructionSet(t, instructionSetRequest(t, second, http.MethodGet, instructionSetPath, nil, ""))
	if refreshed.Content != content {
		t.Fatalf("second Server GET content=%q, want %q", refreshed.Content, content)
	}
	if got := mcpResult(t, second, 1, "initialize", `{}`)["instructions"]; got != content {
		t.Fatalf("second Server MCP instructions=%q, want %q", got, content)
	}
}

func TestInstructionSetHTTPResponsesAreNotCached(t *testing.T) {
	s, _ := instructionSetServer(t)
	get := instructionSetRequest(t, s, http.MethodGet, instructionSetPath, nil, "")
	if got := get.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("GET Cache-Control=%q, want no-store", got)
	}

	put := instructionSetRequest(t, s, http.MethodPut, instructionSetPath, []byte(`{"content":"No caching for instruction updates."}`), get.Header().Get("ETag"))
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", put.Code, put.Body.String())
	}
	if got := put.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("PUT Cache-Control=%q, want no-store", got)
	}
}

func TestInstructionSetRejectsOversizedAndInvalidUTF8(t *testing.T) {
	s, _ := instructionSetServer(t)
	initial := instructionSetRequest(t, s, http.MethodGet, instructionSetPath, nil, "")
	etag := initial.Header().Get("ETag")

	oversized, err := json.Marshal(map[string]string{"content": strings.Repeat("x", 16*1024+1)})
	if err != nil {
		t.Fatal(err)
	}
	if w := instructionSetRequest(t, s, http.MethodPut, instructionSetPath, oversized, etag); w.Code != http.StatusBadRequest {
		t.Fatalf("oversized PUT status=%d body=%s", w.Code, w.Body.String())
	}

	invalidUTF8 := []byte(`{"content":"`)
	invalidUTF8 = append(invalidUTF8, 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`"}`)...)
	if w := instructionSetRequest(t, s, http.MethodPut, instructionSetPath, invalidUTF8, etag); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid UTF-8 PUT status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestInstructionSetCORSAllowsPutWithIfMatch(t *testing.T) {
	s, _ := instructionSetServer(t)
	req := httptest.NewRequest(http.MethodOptions, instructionSetPath, nil)
	req.Header.Set("Origin", "https://admin.example")
	req.Header.Set("Access-Control-Request-Method", http.MethodPut)
	req.Header.Set("Access-Control-Request-Headers", "authorization, content-type, if-match")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS status=%d body=%s", w.Code, w.Body.String())
	}
	if methods := strings.ToUpper(w.Header().Get("Access-Control-Allow-Methods")); !strings.Contains(methods, http.MethodPut) {
		t.Errorf("Access-Control-Allow-Methods=%q, want PUT", methods)
	}
	if headers := strings.ToLower(w.Header().Get("Access-Control-Allow-Headers")); !strings.Contains(headers, "if-match") {
		t.Errorf("Access-Control-Allow-Headers=%q, want If-Match", headers)
	}
}

func TestInstructionSetOversizedBodyDoesNotBlockMCPInitialize(t *testing.T) {
	s, _ := instructionSetServer(t)
	initial := instructionSetRequest(t, s, http.MethodGet, instructionSetPath, nil, "")
	body := newBlockingInstructionBody([]byte(`{"content":"` + strings.Repeat("x", startupInstructionsMaxBytes+1)))
	defer body.Close()
	putReq := httptest.NewRequest(http.MethodPut, instructionSetPath, body)
	putReq.Header.Set("Authorization", "Bearer ctl")
	putReq.Header.Set("Content-Type", "application/json")
	putReq.Header.Set("If-Match", initial.Header().Get("ETag"))
	putDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, putReq)
		putDone <- w
	}()

	putFinished := false
	select {
	case <-body.blocked:
		// The handler consumed an oversized prefix and is waiting for the rest.
	case <-putDone:
		putFinished = true
	case <-time.After(time.Second):
		t.Fatal("PUT neither rejected the oversized prefix nor reached the blocking read")
	}

	initializeDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { initializeDone <- serveMCPInitialize(s) }()
	select {
	case w := <-initializeDone:
		if w.Code != http.StatusOK {
			t.Fatalf("initialize status=%d body=%s", w.Code, w.Body.String())
		}
	case <-time.After(500 * time.Millisecond):
		body.Close()
		select {
		case <-initializeDone:
		case <-time.After(time.Second):
		}
		t.Fatal("MCP initialize blocked for 500ms while PUT waited on an oversized body")
	}

	body.Close()
	if !putFinished {
		select {
		case <-putDone:
		case <-time.After(time.Second):
			t.Fatal("PUT did not finish after request body was released")
		}
	}
}

func TestInstructionSetNormalizesContentAndPersistsExactValue(t *testing.T) {
	s, cfg := instructionSetServer(t)
	initial := instructionSetRequest(t, s, http.MethodGet, instructionSetPath, nil, "")
	w := instructionSetRequest(t, s, http.MethodPut, instructionSetPath, []byte(`{"content":"  Use the approved window.  \n"}`), initial.Header().Get("ETag"))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", w.Code, w.Body.String())
	}
	want := "Use the approved window."
	if got := decodeInstructionSet(t, w).Content; got != want {
		t.Errorf("published content=%q, want normalized %q", got, want)
	}
	initialized := mcpResult(t, s, 1, "initialize", `{}`)
	if got := initialized["instructions"]; got != want {
		t.Errorf("runtime instructions=%q, want normalized %q", got, want)
	}
	restarted := New(cfg)
	if got := decodeInstructionSet(t, instructionSetRequest(t, restarted, http.MethodGet, instructionSetPath, nil, "")).Content; got != want {
		t.Errorf("restarted content=%q, want exactly %q", got, want)
	}
}

func TestInstructionSetPutKeepsRuntimeInSyncWhenDirectorySyncFailsAfterRename(t *testing.T) {
	originalSync := syncInstructionDirectory
	syncInstructionDirectory = func(string) error { return errors.New("directory sync failed") }
	t.Cleanup(func() { syncInstructionDirectory = originalSync })

	s, cfg := instructionSetServer(t)
	initial := instructionSetRequest(t, s, http.MethodGet, instructionSetPath, nil, "")
	content := "Published despite a best-effort directory sync failure."
	put := instructionSetRequest(t, s, http.MethodPut, instructionSetPath, []byte(`{"content":"`+content+`"}`), initial.Header().Get("ETag"))
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", put.Code, put.Body.String())
	}
	if got := mcpResult(t, s, 1, "initialize", `{}`)["instructions"]; got != content {
		t.Fatalf("runtime instructions=%q, want %q", got, content)
	}
	if got, err := os.ReadFile(cfg.StartupInstructionsFile); err != nil {
		t.Fatalf("read committed instructions: %v", err)
	} else if string(got) != content {
		t.Fatalf("committed instructions=%q, want %q", got, content)
	}
	restarted := New(cfg)
	if got := decodeInstructionSet(t, instructionSetRequest(t, restarted, http.MethodGet, instructionSetPath, nil, "")).Content; got != content {
		t.Fatalf("restarted instructions=%q, want %q", got, content)
	}
}

func TestInstructionSetRejectsEmptyContentShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "whitespace-only", body: `{"content":" \n\t "}`},
		{name: "missing-content", body: `{}`},
		{name: "null-document", body: `null`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := instructionSetServer(t)
			initial := instructionSetRequest(t, s, http.MethodGet, instructionSetPath, nil, "")
			w := instructionSetRequest(t, s, http.MethodPut, instructionSetPath, []byte(tt.body), initial.Header().Get("ETag"))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("PUT status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestInstructionSetPutConflictsWithInlineOverride(t *testing.T) {
	configDir := t.TempDir()
	cfg := Config{
		CtlToken:                "ctl",
		ConfigDir:               configDir,
		StartupInstructionsFile: filepath.Join(configDir, "startup_instructions.md"),
		StartupInstructions:     "Inline owner override.",
	}
	s := New(cfg)
	initial := instructionSetRequest(t, s, http.MethodGet, instructionSetPath, nil, "")
	w := instructionSetRequest(t, s, http.MethodPut, instructionSetPath, []byte(`{"content":"Must not publish."}`), initial.Header().Get("ETag"))
	if w.Code != http.StatusConflict {
		t.Errorf("PUT status=%d body=%s, want 409", w.Code, w.Body.String())
	}
	if got := mcpResult(t, s, 1, "initialize", `{}`)["instructions"]; got != cfg.StartupInstructions {
		t.Errorf("runtime instructions=%q, want unchanged inline override %q", got, cfg.StartupInstructions)
	}
}

func TestInstructionSetPutOnlyConflictsWithEffectiveInlineOverride(t *testing.T) {
	tests := []struct {
		name             string
		inline           string
		wantStatus       int
		wantRuntimeValue string
	}{
		{
			name:             "whitespace-only",
			inline:           " \n\t ",
			wantStatus:       http.StatusOK,
			wantRuntimeValue: "File-backed instructions win.",
		},
		{
			name:             "invalid-utf8",
			inline:           string([]byte{'\xff', '\xfe', 'x'}),
			wantStatus:       http.StatusOK,
			wantRuntimeValue: "File-backed instructions win.",
		},
		{
			name:             "oversized",
			inline:           strings.Repeat("x", startupInstructionsMaxBytes+1),
			wantStatus:       http.StatusOK,
			wantRuntimeValue: "File-backed instructions win.",
		},
		{
			name:             "effective-valid-inline",
			inline:           "Inline owner override.",
			wantStatus:       http.StatusConflict,
			wantRuntimeValue: "Inline owner override.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configDir := t.TempDir()
			cfg := Config{
				CtlToken:                "ctl",
				ConfigDir:               configDir,
				StartupInstructionsFile: filepath.Join(configDir, "startup_instructions.md"),
				StartupInstructions:     tt.inline,
			}
			s := New(cfg)
			initial := instructionSetRequest(t, s, http.MethodGet, instructionSetPath, nil, "")
			w := instructionSetRequest(t, s, http.MethodPut, instructionSetPath, []byte(`{"content":"File-backed instructions win."}`), initial.Header().Get("ETag"))
			if w.Code != tt.wantStatus {
				t.Fatalf("PUT status=%d body=%s, want %d", w.Code, w.Body.String(), tt.wantStatus)
			}
			if got := mcpResult(t, s, 1, "initialize", `{}`)["instructions"]; got != tt.wantRuntimeValue {
				t.Fatalf("runtime instructions=%q, want %q", got, tt.wantRuntimeValue)
			}
		})
	}
}

func TestInstructionSetDefaultAndFallbackUpdatedAtAreNullable(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, path string)
	}{
		{
			name:    "missing-file-default",
			prepare: func(t *testing.T, path string) {},
		},
		{
			name: "invalid-file-fallback",
			prepare: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte{0xff, 0xfe, 'x'}, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configDir := t.TempDir()
			path := filepath.Join(configDir, "startup_instructions.md")
			tt.prepare(t, path)
			s := New(Config{CtlToken: "ctl", ConfigDir: configDir, StartupInstructionsFile: path})
			w := instructionSetRequest(t, s, http.MethodGet, instructionSetPath, nil, "")
			if w.Code != http.StatusOK {
				t.Fatalf("GET status=%d body=%s", w.Code, w.Body.String())
			}
			if got := decodeInstructionSet(t, w).UpdatedAt; got != nil {
				t.Fatalf("fallback updated_at=%s, want null or absent", got.Format(time.RFC3339Nano))
			}
		})
	}
}

func TestInstructionSetLoaderFallsBackFromInvalidUTF8(t *testing.T) {
	configDir := t.TempDir()
	path := filepath.Join(configDir, "startup_instructions.md")
	if err := os.WriteFile(path, []byte{0xff, 0xfe, 'x'}, 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(Config{CtlToken: "ctl", ConfigDir: configDir, StartupInstructionsFile: path})
	w := instructionSetRequest(t, s, http.MethodGet, instructionSetPath, nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", w.Code, w.Body.String())
	}
	got := decodeInstructionSet(t, w)
	if got.Content != defaultStartupInstructions {
		t.Errorf("loader content=%q, want default fallback", got.Content)
	}
	if etag := w.Header().Get("ETag"); etag != `"`+got.Version+`"` {
		t.Errorf("ETag=%q does not describe response version=%q", etag, got.Version)
	}
	if instructions := mcpResult(t, s, 1, "initialize", `{}`)["instructions"]; instructions != got.Content {
		t.Errorf("MCP instructions=%q differ from GET content=%q", instructions, got.Content)
	}
}

func TestInstructionSetEndpointRejectsInvalidAuthentication(t *testing.T) {
	s, _ := instructionSetServer(t)
	tests := []struct {
		name          string
		authorization string
		cookie        *http.Cookie
	}{
		{name: "missing"},
		{name: "wrong-bearer", authorization: "Bearer wrong"},
		{name: "invalid-admin-cookie", cookie: &http.Cookie{Name: adminSessionCookieName, Value: "invalid"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, instructionSetPath, nil)
			if tt.authorization != "" {
				req.Header.Set("Authorization", tt.authorization)
			}
			if tt.cookie != nil {
				req.AddCookie(tt.cookie)
			}
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("GET status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
