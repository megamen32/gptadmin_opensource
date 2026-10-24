package hub

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const instructionContextHeader = "X-GPTAdmin-Context"
const instructionContextMeta = "io.gptadmin/context"
const instructionContextTTL = 30 * time.Minute
const instructionBootstrapHint = "Before exec, read the current profile instructions returned by search (discover/schema remain read-only compatibility tools). Pass the returned context to exec when required. instructions_required means no call ran. Read job/expand to inspect existing operations; never replay an uncertain write."

type instructionContextKey struct{}
type instructionOriginKey struct{}
type nonAgentWorkflowKey struct{}

// Called only after an authenticated operator route or configured webhook
// selected a deterministic action. It is not settable by an HTTP header.
func nonAgentWorkflow(r *http.Request) *http.Request {
	if r == nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), nonAgentWorkflowKey{}, true))
}

// Context is an explicit delivery receipt, not a 'read' flag on an account.
// No assertion here can prove that a model understood or followed the text.
func instructionCaller(r *http.Request) *http.Request {
	if r != nil {
		if origin, ok := r.Context().Value(instructionOriginKey{}).(*http.Request); ok && origin != nil {
			return origin
		}
	}
	return r
}
func inheritInstructionCaller(origin, r *http.Request) *http.Request {
	if r == nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), instructionOriginKey{}, instructionCaller(origin)))
}
func requestWithInstructionContext(r *http.Request, args map[string]any) *http.Request {
	if r == nil {
		return r
	}
	token := firstString(args, "context")
	if token == "" {
		token = firstString(mapValue(args["_meta"]), instructionContextMeta)
	}
	if token == "" {
		if existing, _ := r.Context().Value(instructionContextKey{}).(string); existing != "" {
			return r
		}
		token = r.Header.Get(instructionContextHeader)
	}
	if token == "" {
		return r
	}
	copy := r.Clone(context.WithValue(r.Context(), instructionContextKey{}, token))
	copy.Header.Set(instructionContextHeader, token)
	return copy
}
func instructionToken(r *http.Request) string {
	r = instructionCaller(r)
	if r == nil {
		return ""
	}
	if token, ok := r.Context().Value(instructionContextKey{}).(string); ok {
		return token
	}
	return r.Header.Get(instructionContextHeader)
}
func withoutInstructionContext(args map[string]any) map[string]any {
	_, hasContext := args["context"]
	meta := mapValue(args["_meta"])
	_, hasMeta := meta[instructionContextMeta]
	if !hasContext && !hasMeta {
		return args
	}
	out := cloneMap(args)
	delete(out, "context")
	if hasMeta {
		copy := cloneMap(meta)
		delete(copy, instructionContextMeta)
		if len(copy) == 0 {
			delete(out, "_meta")
		} else {
			out["_meta"] = copy
		}
	}
	return out
}

func (s *Server) instructionDocument(r *http.Request) (InstructionSet, string, error) {
	r = instructionCaller(r)
	profile, bound := AccessProfileFromRequest(r)
	if bound && profile.InstructionSetID != "" && profile.InstructionSetID != defaultInstructionSetID {
		set, ok := s.namedInstructionSetSnapshot(profile.InstructionSetID)
		if !ok {
			return InstructionSet{}, profile.ID, errors.New("the selected instruction set is unavailable")
		}
		if err := validateInstructionContent(set.Content); err != nil {
			return InstructionSet{}, profile.ID, err
		}
		return set, profile.ID, nil
	}
	set := s.instructionSetForRequest(r)
	if strings.TrimSpace(set.Content) == "" {
		return InstructionSet{}, profile.ID, errors.New("profile instructions are empty")
	}
	return set, profile.ID, nil
}
func (s *Server) instructionSigningKey() []byte {
	// Reuse the configured signing authority; never create a new secret/store.
	// Replicas with the same authority and effective profile can verify a receipt.
	if s.cfg.OAuthClientSecret != "" {
		return []byte(s.cfg.OAuthClientSecret)
	}
	return []byte(s.cfg.CtlToken)
}
func instructionBinding(r *http.Request) []byte {
	r = instructionCaller(r)
	if r == nil {
		return nil
	}
	profile, _ := AccessProfileFromRequest(r)
	data, _ := json.Marshal([]any{r.Header.Get("Authorization"), secretOwnerFingerprint(r), profile})
	digest := sha256.Sum256(data)
	return digest[:]
}
func (s *Server) issueInstructionContext(r *http.Request, set InstructionSet) (string, error) {
	key := s.instructionSigningKey()
	if len(key) == 0 || r == nil {
		return "", errors.New("instruction context signing is unavailable")
	}
	// Versioned purpose + expiration + random context ID + exact document digest.
	// A token carries no scopes and can never authenticate as an OAuth credential.
	data := make([]byte, 56)
	binary.BigEndian.PutUint64(data[:8], uint64(time.Now().Add(instructionContextTTL).Unix()))
	if _, err := rand.Read(data[8:24]); err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(set.ID + "\x00" + set.Content))
	copy(data[24:], digest[:])
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("gptadmin/instruction-context/v1\x00"))
	mac.Write(instructionBinding(r))
	mac.Write(data)
	return "ic1_" + base64.RawURLEncoding.EncodeToString(append(data, mac.Sum(nil)...)), nil
}
func (s *Server) validInstructionContext(r *http.Request) bool {
	token := instructionToken(r)
	if !strings.HasPrefix(token, "ic1_") || len(token) > 256 {
		return false
	}
	data, err := base64.RawURLEncoding.DecodeString(token[4:])
	if err != nil || len(data) != 88 {
		return false
	}
	expires := int64(binary.BigEndian.Uint64(data[:8]))
	now := time.Now().Unix()
	if expires <= now || expires > now+int64(instructionContextTTL/time.Second)+2 {
		return false
	}
	key := s.instructionSigningKey()
	if len(key) == 0 {
		return false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("gptadmin/instruction-context/v1\x00"))
	mac.Write(instructionBinding(r))
	mac.Write(data[:56])
	if !hmac.Equal(mac.Sum(nil), data[56:]) {
		return false
	}
	set, _, err := s.instructionDocument(r)
	if err != nil {
		return false
	}
	digest := sha256.Sum256([]byte(set.ID + "\x00" + set.Content))
	return hmac.Equal(digest[:], data[24:56])
}
func instructionExempt(target, tool string) bool {
	if target != "hub" {
		return false
	}
	switch tool {
	case "discover", "list_mcp_servers", "listMcpServers", "search", "schema", "list_mcp_tools", "listMcpTools", "expand", "job", "get_mcp_job", "getMcpJob", "status", "hub_status", "diagnose", "demo", "ui", "render_gptadmin_dashboard", "renderGptadminDashboard", "resource_receipt", "pending", "list_pending_servers", "settings_get", "settings_schema", "settings_history", "handover_status", "secret_status", "operations", "stale_cleanup_preview", "agent_policy_get", "agent_tombstones", "ro":
		return true
	}
	return false
}
func (s *Server) instructionFailure(r *http.Request) map[string]any {
	fail := map[string]any{"status": "failed", "executed": false, "error": map[string]any{"code": "instructions_required", "message": "Read the attached profile instructions, then repeat this unexecuted call with context (or obtain a fresh schema call_ref)."}}
	set, profile, err := s.instructionDocument(r)
	if err == nil {
		var token string
		token, err = s.issueInstructionContext(r, set)
		if err == nil {
			fail["context"] = token
			fail["instructions"] = set.Content
			if profile != "" {
				fail["profile"] = profile
			}
		}
	}
	if err != nil {
		fail["error"] = map[string]any{"code": "instructions_unavailable", "message": err.Error()}
	}
	return fail
}
func (s *Server) requireInstructionContext(r *http.Request, target, tool string) map[string]any {
	if !s.cfg.InstructionContextRequired || instructionExempt(target, tool) || (r != nil && r.Context().Value(nonAgentWorkflowKey{}) == true) {
		return nil
	}
	if !s.validInstructionContext(r) {
		return s.instructionFailure(r)
	}
	return nil
}
func (s *Server) requireFacadeInstructionContext(r *http.Request, name string, args map[string]any) map[string]any {
	target, tool := "hub", name
	if name == "execute" || name == "callMcpTool" || name == "call_mcp_tool" {
		target = firstString(args, "target", "server_id", "agent_id")
		tool = firstString(args, "tool", "tool_name", "name")
	} else if name == "exec" || name == "ro" {
		target, tool = "hub", name
	}
	failure := s.requireInstructionContext(r, target, tool)
	if failure == nil {
		return nil
	}
	if name == codeModeTool || name == "ro" {
		return codeModeInstructionFailure(failure)
	}
	return failure
}

// codeModeInstructionFailure keeps an instructions_required refusal inside the
// published codeModeOutputSchema. The code-mode tools require "status" and
// "calls", so a refusal without "calls" is not schema-valid: the client then
// rejects the whole answer with a generic schema error and the caller never
// sees the actionable receipt that carries the context to repeat the call with.
func codeModeInstructionFailure(failure map[string]any) map[string]any {
	out := cloneMap(failure)
	out["status"] = "failed"
	out["calls"] = 0
	detail := cloneMap(mapValue(out["error"]))
	if detail == nil {
		detail = map[string]any{}
	}
	if detail["code"] == nil {
		detail["code"] = "instructions_required"
	}
	if detail["message"] == nil {
		detail["message"] = "Read the attached profile instructions, then repeat this unexecuted call with context."
	}
	if detail["calls_completed"] == nil {
		detail["calls_completed"] = 0
	}
	out["error"] = detail
	return out
}
func (s *Server) bindInstructionContext(ref, token string) {
	if ref == "" {
		return
	}
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	if item, ok := s.operations[ref]; ok {
		item.InstructionContext = token
		s.operations[ref] = item
	}
}

// The actual instruction text is visible to the model, never hidden in _meta.
// A context supplied by the caller suppresses repetition only while still valid.
func (s *Server) presentInstructionContext(r *http.Request, name string, args map[string]any, raw any) any {
	bootstrap := name == "discover" || name == "list_mcp_servers" || name == "listMcpServers" || name == "search" || name == "schema" || name == "list_mcp_tools" || name == "listMcpTools"
	if !bootstrap {
		return raw
	}
	out, ok := raw.(map[string]any)
	if !ok || out["error"] != nil || firstString(out, "status") == "failed" {
		return raw
	}
	r = requestWithInstructionContext(r, args)
	selected := (name == "schema" || name == "list_mcp_tools" || name == "listMcpTools") && firstString(args, "tool") != "" && firstString(out, "status") == "completed"
	out = cloneMap(out)
	ref := firstString(out, "call_ref")
	if selected && ref == "" {
		ref = s.rememberOperation(r, "schema", firstString(args, "target", "server_id", "agent_id"), raw, firstString(args, "tool"))
		if ref != "" {
			out["call_ref"] = ref
		}
	}
	if ref != "" && out["operation_id"] == ref {
		delete(out, "operation_id")
	}
	if !s.cfg.InstructionContextRequired {
		return out
	}
	set, profile, err := s.instructionDocument(r)
	if err != nil {
		return map[string]any{"status": "failed", "executed": false, "error": map[string]any{"code": "instructions_unavailable", "message": err.Error()}}
	}
	token := instructionToken(r)
	if !s.validInstructionContext(r) {
		token, err = s.issueInstructionContext(r, set)
		if err != nil {
			return s.instructionFailure(r)
		}
		out["context"] = token
		out["instructions"] = set.Content
		if profile != "" {
			out["profile"] = profile
		}
	}
	if selected {
		s.bindInstructionContext(ref, token)
	}
	return out
}

func instructionAwareFacadeTools(tools []map[string]any) []map[string]any {
	for _, tool := range tools {
		name := firstString(tool, "name")
		// Header/_meta also work. This convenience field belongs only to our
		// facades, not to any upstream child's strict argument schema.
		if name != "discover" && name != "schema" && name != "execute" && name != "fleetExec" && name != "ro" {
			continue
		}
		properties := mapValue(mapValue(tool["inputSchema"])["properties"])
		if properties != nil {
			properties["context"] = map[string]any{"type": "string", "description": "Instruction receipt; call_ref carries it."}
		}
	}
	return tools
}

// Prune only known, successful GPTAdmin envelopes. Native protocol envelopes,
// failure details, pending jobs, upstream business keys and resources stay intact.
func leanExecutionOutput(raw, out map[string]any, target, tool string) map[string]any {
	if hasMediaOrFailure(raw) || firstString(raw, "status") != "completed" {
		return out
	}
	if target == "hub" && tool == codeModeTool {
		response := mapValue(raw["response"])
		if firstString(response, "status") == "completed" {
			return map[string]any{"result": response["result"]}
		}
	}
	if strings.HasPrefix(target, "shell:") && tool == "shell_exec" {
		result := mapValue(mapValue(mapValue(raw["response"])["structuredContent"])["result"])
		code, exists := result["returncode"]
		if exists && intFromAny(code) == 0 && !truthyAny(result["_spilled"]) && !truthyAny(result["stdout_truncated"]) && !truthyAny(result["stderr_truncated"]) {
			compact := cloneMap(out)
			compact["returncode"] = code
			delete(compact, "status")
			delete(compact, "job_id")
			delete(compact, "task_id")
			if firstString(compact, "run_as_user") != "root" {
				delete(compact, "run_as_user")
			}
			return compact
		}
	}
	return out
}
