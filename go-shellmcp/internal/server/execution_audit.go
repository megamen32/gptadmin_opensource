package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/megamen32/gptadmin/go-shellmcp/internal/shell"
)

type executionAuditKey struct{}
type executionAuditContext struct {
	JobID, TraceID string
	Origin         map[string]any
}

// Only the authenticated queue envelope populates Origin. Tool arguments and
// daemon environment variables are never authority for caller/native identity.
func withExecutionTask(ctx context.Context, jobID, traceID string, origin ...map[string]any) context.Context {
	value, _ := ctx.Value(executionAuditKey{}).(executionAuditContext)
	if jobID != "" {
		value.JobID = jobID
	}
	if traceID != "" {
		value.TraceID = traceID
	}
	if len(origin) > 0 {
		value.Origin = executionOriginFields(origin[0])
	}
	return context.WithValue(ctx, executionAuditKey{}, value)
}

func executionOriginFields(origin map[string]any) map[string]any {
	out := map[string]any{"source": "unknown", "client_id": "unknown", "subject": "unknown", "profile_id": "unknown", "profile_version": 0, "native_harness_hint": "unknown", "native_session_hint": "unknown", "native_session_status": "not_supplied", "mcp_session_hash": "unknown"}
	for _, key := range []string{"source", "client_id", "subject", "profile_id", "native_harness_hint", "native_session_hint"} {
		if value, ok := origin[key].(string); ok && value != "" {
			if len(value) > 256 {
				value = value[:256]
			}
			out[key] = value
		}
	}
	if n := intAny(origin["profile_version"]); n > 0 {
		out["profile_version"] = n
	}
	if status, ok := origin["native_session_status"].(string); ok {
		switch status {
		case "client_declared", "not_supplied", "not_recorded":
			out["native_session_status"] = status
		}
	}
	if value, ok := origin["mcp_session_hash"].(string); ok && len(value) == 64 {
		if _, err := hex.DecodeString(value); err == nil {
			out["mcp_session_hash"] = value
		}
	}
	return out
}

func executionCommandHash(req shell.Request) string {
	raw, _ := json.Marshal(struct {
		Cmd  string
		Argv []string
	}{req.Cmd, req.Argv})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// executionAuditFields captures the original caller and current executor before
// either local execution or child forwarding. It never inspects tool arguments.
func executionAuditFields(ctx context.Context, fallbackTrace string) (map[string]any, error) {
	caller, _ := ctx.Value(executionAuditKey{}).(executionAuditContext)
	fields := executionOriginFields(caller.Origin)
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, errors.New("execution audit identity unavailable")
	}
	fields["execution_id"] = hex.EncodeToString(id[:])
	fields["job_id"] = caller.JobID
	trace := caller.TraceID
	if trace == "" {
		trace = normalizeTraceID(fallbackTrace)
	}
	fields["trace_id"] = trace
	fields["executor_pid"] = os.Getpid()
	fields["executor_ppid"] = os.Getppid()
	fields["executor_process"] = executionProcess(os.Getpid())
	fields["process_ancestors"] = executionAncestors(os.Getppid())
	fields["build"] = BuildVersion
	fields["git_commit"] = GitCommit
	invocation := os.Getenv("INVOCATION_ID")
	if len(invocation) != 32 {
		invocation = "unknown"
	} else if _, err := hex.DecodeString(invocation); err != nil {
		invocation = "unknown"
	}
	fields["systemd_invocation_id"] = invocation
	return fields, nil
}

func (s *Server) prepareExecutionAudit(ctx context.Context, req shell.Request) (shell.Request, error) {
	if s.cfg.AuditLog == "" && s.cfg.IdentityDir == "" {
		return req, nil
	}
	fields, err := executionAuditFields(ctx, req.TraceID)
	if err != nil {
		return req, err
	}
	fields["command_sha256"] = executionCommandHash(req)
	if err := s.auditLog.EventDurable("execution_intent", fields); err != nil {
		return req, errors.New("durable execution audit unavailable; command not started")
	}
	previous := req.OnStarted
	req.OnStarted = func(pid int) error {
		started := make(map[string]any, len(fields)+3)
		for key, value := range fields {
			started[key] = value
		}
		started["child_pid"] = pid
		started["child_pgid"] = executionPGID(pid)
		started["child_process"] = executionProcess(pid)
		err := s.auditLog.EventDurable("execution_started", started)
		if previous != nil {
			err = errors.Join(err, previous(pid))
		}
		return err
	}
	return req, nil
}

// This label is deliberately independent of command text, including inline
// assignments. Command bytes are represented solely by the shared digest.
func safeExecutionLabel(shell.Request) string { return "command" }

func auditText(value string) string {
	value = strings.ReplaceAll(value, "\x00", "")
	if len(value) > 4096 {
		return value[:4096]
	}
	return value
}
