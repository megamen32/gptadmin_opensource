package hub

// These fixtures test the bridge and durable ledger contract only. Release
// acceptance must additionally execute real commands through the live Node.
import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func taskBridgeFixture(t *testing.T) (*Server, *http.Request) {
	s, r := codeModeFixture(t)
	t.Cleanup(func() { s.Close() })
	s.mu.Lock()
	s.agents["shell:fixture"] = &Agent{AgentID: "shell:fixture", Name: "Fixture", Kind: "virtual_shell", Transport: "long_poll", Status: "online", LastSeen: nowFloat(), Meta: map[string]any{"approved": true}}
	s.mu.Unlock()
	return s, r
}

func startBridgeTask(t *testing.T, s *Server, r *http.Request) string {
	t.Helper()
	out, status := codeModeRun(t, s, r, map[string]any{"code": "return await mcp.tasks.start('shell:fixture','shell_exec',{cmd:'printf task-fixture',timeout:20});"})
	task := mapValue(out["result"])
	if status != http.StatusOK || firstString(out, "status") != "completed" || firstString(task, "resultType") != "task" || firstString(task, "taskId") == "" {
		t.Fatalf("start status=%d result=%v", status, out)
	}
	return firstString(task, "taskId")
}

func TestCodeModeTasksStartFastAndContinue(t *testing.T) {
	s, r := taskBridgeFixture(t)
	started := time.Now()
	out, status := codeModeRun(t, s, r, map[string]any{"code": "const tasks=await Promise.all([mcp.tasks.start('shell:fixture','shell_exec',{cmd:'sleep 10'}),mcp.call('shell:fixture','shell_exec',{cmd:'sleep 10'},{task:true})]); const state=mcp.tasks.get(tasks[0].taskId); const pending=mcp.tasks.wait(tasks[1].taskId,{timeout_ms:0}); return {tasks,state,pending,continued:true};"})
	if status != http.StatusOK || firstString(out, "status") != "completed" {
		t.Fatalf("run %d %v", status, out)
	}
	result := mapValue(out["result"])
	if !truthy(result["continued"]) || firstString(mapValue(result["state"]), "status") != "working" || firstString(mapValue(result["pending"]), "status") != "working" {
		t.Fatalf("task state %v", result)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("creation waited for execution: %s", time.Since(started))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.shellJobs) != 2 || len(s.shellQueues["fixture"]) != 2 {
		t.Fatalf("not exactly once: jobs=%d queue=%v", len(s.shellJobs), s.shellQueues)
	}
	for _, job := range s.shellJobs {
		if !sameTaskCaller(job.Caller, taskCallerFromRequest(r)) {
			t.Fatal("caller identity not persisted")
		}
	}
}

func TestCodeModeTasksWaitNativeResultWithoutResubmit(t *testing.T) {
	s, r := taskBridgeFixture(t)
	id := startBridgeTask(t, s, r)
	go func() {
		time.Sleep(70 * time.Millisecond)
		s.mu.Lock()
		defer s.mu.Unlock()
		change := s.beginTaskMutationLocked(id)
		j := s.shellJobs[id]
		j.Status = "completed"
		j.DoneAt = nowFloat()
		j.Result = map[string]any{"stdout": "TASK_RESULT_MARKER", "returncode": 0}
		_ = change.commit()
		s.cond.Broadcast()
	}()
	out, status := codeModeRun(t, s, r, map[string]any{"code": fmt.Sprintf("return mcp.tasks.wait(%q,{timeout_ms:2200});", id), "timeout_ms": 3000})
	if status != http.StatusOK || firstString(mapValue(out["result"]), "status") != "completed" || !strings.Contains(string(mustJSON(t, out)), "TASK_RESULT_MARKER") {
		t.Fatalf("wait %d %v", status, out)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.shellJobs) != 1 {
		t.Fatalf("wait resubmitted: %d", len(s.shellJobs))
	}
}

func TestCodeModeTasksOwnershipAndCancellation(t *testing.T) {
	s, r := taskBridgeFixture(t)
	id := startBridgeTask(t, s, r)
	foreign := requestWithAuthClaims(r, map[string]any{"sub": "foreign", "client_id": "foreign", "scope": "gptadmin.read gptadmin.exec", "access_mode": accessModeFull})
	for _, action := range []string{"get", "cancel", "update"} {
		out, status := codeModeRun(t, s, foreign, map[string]any{"code": fmt.Sprintf("return mcp.tasks.%s(%q,{});", action, id)})
		if status == http.StatusOK || firstString(out, "status") == "completed" {
			t.Fatalf("foreign %s allowed: %v", action, out)
		}
	}
	out, status := s.callCodeModeToolMode(r, map[string]any{"code": fmt.Sprintf("return mcp.tasks.cancel(%q);", id)}, true)
	if status == http.StatusOK {
		t.Fatalf("read-only cancel allowed: %v", out)
	}
	out, status = codeModeRun(t, s, r, map[string]any{"code": fmt.Sprintf("mcp.tasks.cancel(%q); return mcp.tasks.get(%q);", id, id)})
	if status != http.StatusOK || firstString(mapValue(out["result"]), "status") != "cancelled" {
		t.Fatalf("cancel %d %v", status, out)
	}
}

func TestCodeModeTasksReadOnlyAndInvalidStartHaveNoEffects(t *testing.T) {
	s, r := taskBridgeFixture(t)
	out, status := s.callCodeModeToolMode(r, map[string]any{"code": "try { await mcp.tasks.start('shell:fixture','shell_exec',{cmd:'printf forbidden'}); } catch(e) {} return 'caught';"}, true)
	if status == http.StatusOK {
		t.Fatalf("RO start allowed %v", out)
	}
	out, status = codeModeRun(t, s, r, map[string]any{"code": "return await mcp.tasks.start('hub','settings_get',{});"})
	if status == http.StatusOK {
		t.Fatalf("non-executor task allowed %v", out)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.shellJobs) != 0 {
		t.Fatalf("refused starts dispatched %d tasks", len(s.shellJobs))
	}
}

func TestCodeModeTasksIdempotencyIsPreserved(t *testing.T) {
	s, r := taskBridgeFixture(t)
	script := "return await mcp.tasks.start('shell:fixture','shell_exec',{cmd:'printf once'},{idempotency_key:'code-mode-native-task-once'});"
	one, st1 := codeModeRun(t, s, r, map[string]any{"code": script})
	two, st2 := codeModeRun(t, s, r, map[string]any{"code": script})
	if st1 != http.StatusOK || st2 != http.StatusOK || firstString(mapValue(one["result"]), "taskId") != firstString(mapValue(two["result"]), "taskId") {
		t.Fatalf("idempotency: %v / %v", one, two)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.shellJobs) != 1 {
		t.Fatalf("idempotent start replayed: %d", len(s.shellJobs))
	}
}
