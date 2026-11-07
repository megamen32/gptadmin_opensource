package hub

// Code Mode Tasks is a bridge to the existing durable MCP task ledger, not a
// second job queue. A start still traverses ordinary dispatch authorization,
// approval, schema validation, audit, idempotency and drain controls.
import (
	"github.com/fastschema/qjs"
	"net/http"
	"time"
)

func (b *codeModeBridge) createdTask(r *http.Request, response map[string]any, target, tool string) (any, bool, error) {
	id := firstString(response, "taskId", "task_id", "job_id")
	if id == "" {
		return nil, false, nil
	}
	// A durable handle must resolve under this caller before it is returned.
	receipt, rpcErr := b.server.mcpTaskGetForRequest(r, id, "")
	if rpcErr != nil {
		return nil, true, b.fail("task_receipt_unavailable", "submitted task could not be read; do not submit it again", map[string]any{"taskId": id, "target": target, "tool": tool})
	}
	task := cloneMap(mapValue(receipt))
	task["resultType"] = "task"
	b.recordOrigin(target, tool)
	b.recordToolCall(target, tool, firstString(task, "status"))
	b.mu.Lock()
	b.lastReceipt = map[string]any{"taskId": id, "target": target, "tool": tool, "status": task["status"]}
	b.mu.Unlock()
	return task, true, nil
}

func (b *codeModeBridge) task(this *qjs.This) (*qjs.Value, error) {
	if err := b.entry(); err != nil {
		return nil, err
	}
	// Waiting observes already accepted work; it is bounded by the whole
	// Code Mode request, not the shorter single-dispatch relay timeout.
	_, err := b.remaining()
	remaining := time.Until(b.deadline)
	if err != nil {
		return nil, err
	}
	action := codeModeStringArg(this, 0)
	args := codeModeObjectArg(this, 1)
	id := firstString(args, "taskId")
	if id == "" || len(id) > 256 {
		return nil, b.fail("invalid_arguments", "taskId is required", nil)
	}
	switch action {
	case "get", "result", "wait", "cancel", "update":
	default:
		return nil, b.fail("invalid_arguments", "unknown Tasks action", nil)
	}
	if b.readOnly && (action == "cancel" || action == "update") {
		return nil, b.fail("forbidden", "read-only execution cannot modify tasks", nil)
	}
	request := b.request.WithContext(b.ctx)
	read := func() (map[string]any, error) {
		value, rpcErr := b.server.mcpTaskGetForRequest(request, id, "")
		if rpcErr != nil {
			return nil, b.fail("task_unavailable", "task is absent or inaccessible to this caller", map[string]any{"taskId": id})
		}
		task := mapValue(value)
		progress := mapValue(mapValue(task["_meta"])["io.gptadmin/taskProgress"])
		b.recordOrigin(firstString(progress, "target"), firstString(progress, "tool", "method"))
		return task, nil
	}
	task, readErr := read()
	if readErr != nil {
		return nil, readErr
	}
	if action == "get" {
		b.recordToolCall("hub", "tasks/get", firstString(task, "status"))
		return b.jsonValue(this, task), nil
	}
	if action == "result" {
		if firstString(task, "status") != "completed" {
			return nil, b.fail("task_not_completed", "task result is available only after successful completion", map[string]any{"taskId": id, "status": task["status"]})
		}
		b.recordToolCall("hub", "tasks/result", "completed")
		return b.jsonValue(this, task["result"]), nil
	}
	if action == "cancel" || action == "update" {
		// Reuse native mutation logic only after the native receipt authorization
		// above; knowing a task ID never grants control of another caller's task.
		var result, rpcErr any
		if action == "cancel" {
			result, rpcErr = b.server.mcpTaskCancel(id, "")
		} else {
			result, rpcErr = b.server.mcpTaskUpdate(request, id, mapValue(args["inputResponses"]), "")
		}
		if rpcErr != nil {
			return nil, b.fail("task_control_failed", firstString(mapValue(rpcErr), "message"), map[string]any{"taskId": id})
		}
		b.recordToolCall("hub", "tasks/"+action, "completed")
		return b.jsonValue(this, result), nil
	}
	// A bounded, single connector wait performs protocol-cadenced reads inside
	// the server. It never resubmits the operation, consumes model turns, or
	// changes the command's own runtime deadline. Expiry returns the live Task.
	millis := 30000
	if raw, ok := args["timeout_ms"]; ok {
		millis = intFromAny(raw)
		if millis < 0 || millis > 55000 {
			return nil, b.fail("invalid_arguments", "task wait timeout_ms must be 0..55000", nil)
		}
	}
	budget := time.Duration(millis) * time.Millisecond
	if cap := remaining - 100*time.Millisecond; budget > cap {
		budget = cap
	}
	until := time.Now().Add(budget)
	for firstString(task, "status") == "working" && time.Now().Before(until) {
		delay := time.Duration(intFromAny(task["pollIntervalMs"])) * time.Millisecond
		if delay <= 0 {
			delay = time.Second
		}
		left := time.Until(until)
		if delay > left {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-b.ctx.Done():
			timer.Stop()
			return b.jsonValue(this, task), nil
		case <-timer.C:
		}
		task, readErr = read()
		if readErr != nil {
			return nil, readErr
		}
	}
	b.recordToolCall("hub", "tasks/get", firstString(task, "status"))
	return b.jsonValue(this, task), nil
}
