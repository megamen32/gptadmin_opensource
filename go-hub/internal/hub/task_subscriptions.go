package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Bound the number of long-lived listeners in this process. Individual
// streams are also limited to 64 task IDs.
var taskSubscriptionSlots = make(chan struct{}, 128)

// serveTaskSubscription uses the same authorization and durable receipts as
// tasks/get. The HTTP stream is only a listener; it never admits work again.
func (s *Server) serveTaskSubscription(w http.ResponseWriter, r *http.Request, body map[string]any, owner string) {
	id := body["id"]
	params := mapValue(body["params"])
	filter := mapValue(params["notifications"])
	rawValue, hasTasks := filter["taskIds"]
	if hasTasks && !mcpTasksOptedIn(params) {
		writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32021, "message": "Missing required client capability", "data": map[string]any{"requiredCapabilities": map[string]any{"extensions": map[string]any{"io.modelcontextprotocol/tasks": map[string]any{}}}}}})
		return
	}
	raw, ok := rawValue.([]any)
	if id == nil || (hasTasks && (!ok || len(raw) > 64)) {
		writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32602, "message": "request id and at most 64 taskIds are required"}})
		return
	}
	ids := make([]string, 0, len(raw))
	seen := make(map[string]bool)
	for _, value := range raw {
		taskID, valid := value.(string)
		if !valid || taskID == "" || len(taskID) > 256 {
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32602, "message": "invalid taskId"}})
			return
		}
		if seen[taskID] {
			continue
		}
		if _, rpcErr := s.mcpTaskGetForRequest(r, taskID, owner); rpcErr != nil {
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": id, "error": rpcErr})
			return
		}
		seen[taskID] = true
		ids = append(ids, taskID)
	}
	// Task access has been checked. Neither the initial network write nor the
	// long-lived listener may retain the authentication generation gate.
	releaseAuthAdmission(r)
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32603, "message": "streaming is unavailable"}})
		return
	}
	select {
	case taskSubscriptionSlots <- struct{}{}:
		defer func() { <-taskSubscriptionSlots }()
	default:
		writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32603, "message": "subscription capacity reached"}})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	writeEvent := func(v any) bool {
		data, err := json.Marshal(v)
		if err != nil {
			return false
		}
		if _, err = fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	meta := map[string]any{"io.modelcontextprotocol/subscriptionId": id}
	accepted := map[string]any{}
	if len(ids) > 0 {
		accepted["taskIds"] = ids
	}
	if !writeEvent(map[string]any{"jsonrpc": "2.0", "method": "notifications/subscriptions/acknowledged", "params": map[string]any{"notifications": accepted, "_meta": meta}}) {
		return
	}
	if len(ids) == 0 {
		<-r.Context().Done()
		return
	}
	last := make(map[string][]byte)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		for _, taskID := range ids {
			value, rpcErr := s.mcpTaskGetForRequest(r, taskID, owner)
			// A revoked profile or lost receipt closes the stream without leaking
			// either the previous result or the existence of the task.
			if rpcErr != nil {
				return
			}
			task := cloneMap(mapValue(value))
			delete(task, "resultType")
			taskMeta := cloneMap(mapValue(task["_meta"]))
			taskMeta["io.modelcontextprotocol/subscriptionId"] = id
			task["_meta"] = taskMeta
			fingerprint := cloneMap(task)
			fingerprintMeta := cloneMap(taskMeta)
			progress := cloneMap(mapValue(fingerprintMeta["io.gptadmin/taskProgress"]))
			delete(progress, "elapsedMs")
			if len(progress) > 0 {
				fingerprintMeta["io.gptadmin/taskProgress"] = progress
			}
			fingerprint["_meta"] = fingerprintMeta
			encoded, err := json.Marshal(fingerprint)
			if err != nil {
				return
			}
			if bytes.Equal(encoded, last[taskID]) {
				continue
			}
			if !writeEvent(map[string]any{"jsonrpc": "2.0", "method": "notifications/tasks", "params": task}) {
				return
			}
			last[taskID] = encoded
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}
