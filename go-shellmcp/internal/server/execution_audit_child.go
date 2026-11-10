package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// auditChildInvocation must succeed on the original invocation before calling
// childMCP.CallToolScopedWithMeta. It records intent only: no response, terminal
// PID, session capability, or downstream effect is inferred from this record.
// Call after the existing access/guard/scope checks, using their callCtx.
func (s *Server) auditChildInvocation(ctx context.Context, ref, tool string, inputs map[string]any, scope string) error {
	if s.cfg.AuditLog == "" && s.cfg.IdentityDir == "" {
		return nil
	}
	if inputs == nil {
		inputs = map[string]any{}
	}
	encoded, err := json.Marshal(inputs)
	if err != nil {
		return errors.New("child invocation inputs cannot be audited; request not forwarded")
	}
	digest := sha256.Sum256(encoded)
	fields, err := executionAuditFields(ctx, "")
	if err != nil {
		return err
	}
	fields["phase"] = "before_forward"
	fields["child_ref"] = auditText(ref)
	fields["child_tool"] = auditText(tool)
	fields["inputs_sha256"] = hex.EncodeToString(digest[:])
	fields["stdio_pid"] = nil
	fields["stdio_pid_status"] = "unknown"
	fields["stdio_process"] = nil
	fields["terminal_pid"] = nil
	fields["terminal_pid_status"] = "not_recorded"
	// Status(ref) observes only the unscoped session. Scoped forwarding uses
	// a separate partition; without its exact observer, retain unknown PID.
	if scope == "" && s.childMCP != nil {
		status := s.childMCP.Status(ref)
		if status.Running && status.PID > 0 {
			fields["stdio_pid"] = status.PID
			fields["stdio_pid_status"] = "observed_running"
			fields["stdio_process"] = executionProcess(status.PID)
		}
	}
	if err := s.auditLog.EventDurable("child_invocation_intent", fields); err != nil {
		return errors.New("durable child invocation audit unavailable; request not forwarded")
	}
	return nil
}
