package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminMCPTasksReadUsesOperatorAuthWithoutNewJobs(t *testing.T) {
	s := New(Config{CtlToken: "task-admin-token", ConfigDir: t.TempDir()})
	defer s.Close()
	s.mu.Lock()
	s.shellJobs["receipt-abc"] = &shellJob{ID: "receipt-abc", Server: "host", ToolName: "shell_exec",
		Status: "completed", CreatedAt: nowFloat() - 3, StartedAt: nowFloat() - 2, DoneAt: nowFloat() - 1,
		Result: map[string]any{"returncode": 0, "stdout": "acceptance-safe-output"}}
	beforeShell, beforeRelay := len(s.shellJobs), len(s.relayJobs)
	s.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/admin/api/tasks/receipt-abc", nil)
	req.Header.Set("Authorization", "Bearer task-admin-token")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin task read http=%d body=%s", rec.Code, rec.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if firstString(result, "taskId") != "receipt-abc" ||
		firstString(result, "status") != "completed" || firstString(result, "resultType") != "complete" ||
		!strings.Contains(rec.Body.String(), "acceptance-safe-output") {
		t.Fatalf("canonical MCP task details missing: %#v", result)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("task data was cacheable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.shellJobs) != beforeShell || len(s.relayJobs) != beforeRelay {
		t.Fatal("reading admin task submitted another job")
	}
}

func TestAdminMCPTasksAccessAndValidation(t *testing.T) {
	s := New(Config{CtlToken: "task-admin-token", ConfigDir: t.TempDir()})
	defer s.Close()
	s.mu.Lock()
	s.shellJobs["private-task"] = &shellJob{ID: "private-task", Server: "host", Status: "queued", CreatedAt: nowFloat()}
	s.mu.Unlock()
	for _, item := range []struct {
		method, path, token string
		want                int
	}{
		{http.MethodGet, "/admin/api/tasks/private-task", "", http.StatusUnauthorized},
		{http.MethodGet, "/admin/api/tasks/not-existing", "task-admin-token", http.StatusNotFound},
		{http.MethodPost, "/admin/api/tasks/private-task", "task-admin-token", http.StatusMethodNotAllowed},
		{http.MethodGet, "/admin/api/tasks/", "task-admin-token", http.StatusBadRequest},
		{http.MethodGet, "/admin/api/tasks/invalid/child", "task-admin-token", http.StatusBadRequest},
	} {
		req := httptest.NewRequest(item.method, item.path, nil)
		if item.token != "" {
			req.Header.Set("Authorization", "Bearer "+item.token)
		}
		res := httptest.NewRecorder()
		s.Handler().ServeHTTP(res, req)
		if res.Code != item.want {
			t.Errorf("%s %s: http=%d want=%d body=%s", item.method, item.path, res.Code, item.want, res.Body.String())
		}
		if res.Code != http.StatusOK && strings.Contains(res.Body.String(), "private-task") {
			t.Fatal("private task identifier leaked")
		}
	}
}
