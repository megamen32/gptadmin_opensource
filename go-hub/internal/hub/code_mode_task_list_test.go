package hub

import (
	"net/http"
	"strings"
	"testing"
)

func TestTasksListShowsOnlyOwnedReceiptsWithoutReplay(t *testing.T) {
	s, r := taskBridgeFixture(t)
	id := startBridgeTask(t, s, r)
	foreign := requestWithAuthClaims(r, map[string]any{"sub": "foreign", "client_id": "foreign", "jti": "foreign-key", "scope": "gptadmin.read gptadmin.exec", "access_mode": accessModeFull})

	own, st := codeModeRun(t, s, r, map[string]any{"code": "return mcp.tasks.list({limit:5});"})
	if st != http.StatusOK {
		t.Fatalf("owned task list failed: %d %#v", st, own)
	}
	rows := outputRows(mapValue(own["result"])["tasks"])
	if len(rows) != 1 || firstString(mapValue(rows[0]), "task_id") != id || firstString(mapValue(rows[0]), "get") == "" {
		t.Fatalf("task disappeared from durable index: %#v", own)
	}
	if mapValue(own["result"])["replay"] != false {
		t.Fatalf("task list should never replay operations: %#v", own)
	}
	other, status := codeModeRun(t, s, foreign, map[string]any{"code": "return mcp.tasks.list({limit:5});"})
	if status == http.StatusOK && len(outputRows(mapValue(other["result"])["tasks"])) > 0 {
		t.Fatalf("foreign caller viewed another principal's tasks: %#v", other)
	}
	s.mu.Lock()
	count := len(s.shellJobs)
	s.mu.Unlock()
	if count != 1 {
		t.Fatalf("task list created an operation: %d", count)
	}
}

func TestTasksListSurvivesNewHubProcessWithSameCaller(t *testing.T) {
	s, r := taskBridgeFixture(t)
	id := startBridgeTask(t, s, r)
	other := New(Config{ConfigDir: s.cfg.ConfigDir, DefaultTimeout: s.cfg.DefaultTimeout})
	defer other.Close()
	out, err := other.listOwnedTasksForRequest(r, 5, "")
	if err != nil || len(outputRows(out["tasks"])) != 1 || firstString(mapValue(outputRows(out["tasks"])[0]), "task_id") != id {
		t.Fatalf("restart lost the receipt: %v %#v", err, out)
	}
}

func TestTaskListPaginationDoesNotSkipUnreturnedItems(t *testing.T) {
	s, r := taskBridgeFixture(t)
	expected := map[string]bool{}
	for i := 0; i < 4; i++ {
		expected[startBridgeTask(t, s, r)] = true
	}
	cursor := ""
	seen := map[string]bool{}
	for i := 0; i < 6; i++ {
		page, err := s.listOwnedTasksForRequest(r, 1, cursor)
		if err != nil {
			t.Fatal(err)
		}
		rows := outputRows(page["tasks"])
		for _, item := range rows {
			id := firstString(mapValue(item), "task_id")
			if !expected[id] || seen[id] {
				t.Fatalf("missing or duplicate task: %s %#v", id, page)
			}
			seen[id] = true
		}
		cursor = firstString(page, "next_cursor")
		if cursor == "" {
			break
		}
	}
	if len(seen) != len(expected) {
		t.Fatalf("pagination skipped tasks: got %d want %d", len(seen), len(expected))
	}
}

func TestTasksListRejectsBadCursorAndDoesNotLeakOtherOwners(t *testing.T) {
	s, r := taskBridgeFixture(t)
	_ = startBridgeTask(t, s, r)
	for _, cursor := range []string{"-1", "not-a-cursor", "1000001"} {
		if _, err := s.listOwnedTasksForRequest(r, 5, cursor); err == nil {
			t.Errorf("invalid cursor %q accepted", cursor)
		}
	}
	unknown := requestWithAuthClaims(r, map[string]any{"client_id": "other", "sub": "other", "jti": "other-key", "scope": "gptadmin.read gptadmin.exec", "access_mode": accessModeFull})
	out, err := s.listOwnedTasksForRequest(unknown, 5, "")
	if err != nil || len(outputRows(out["tasks"])) != 0 {
		t.Fatalf("foreign task listing leaked data: %v %#v", err, out)
	}
	if strings.Contains(adminPreview(out), "task-fixture") {
		t.Fatal("request input leaked through list")
	}
}
