package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
)

// Native hints are untrusted, client-declared diagnostics. They neither prove
// a native session exists nor participate in task identity, permissions or ACLs.
type executionOrigin struct {
	Source              string `json:"source"`
	ClientID            string `json:"client_id"`
	Subject             string `json:"subject"`
	ProfileID           string `json:"profile_id"`
	ProfileVersion      int64  `json:"profile_version"`
	NativeHarnessHint   string `json:"native_harness_hint"`
	NativeSessionHint   string `json:"native_session_hint"`
	NativeSessionStatus string `json:"native_session_status"`
	MCPSessionHash      string `json:"mcp_session_hash"`
}

const executionOriginUUID = `[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`

var executionOriginNativeSession = regexp.MustCompile(`^(` + executionOriginUUID + `|sess_` + executionOriginUUID + `|ses_[0-9a-f]{12}[A-Za-z0-9]{14})$`)
var executionOriginHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

func executionHarnessHint(raw string) string {
	switch raw {
	case "codex", "claude", "claude-code", "opencode", "zcode", "hermes", "openclaw":
		return raw
	default:
		return ""
	}
}

func executionNativeSessionHint(raw string) string {
	if len(raw) > 80 || !executionOriginNativeSession.MatchString(raw) {
		return ""
	}
	return raw
}

func captureExecutionOrigin(r *http.Request, caller *taskCaller) *executionOrigin {
	if r == nil || caller == nil {
		return nil
	}
	origin := executionOriginForCaller(caller)
	origin.Source = "authenticated_mcp"
	origin.NativeHarnessHint = executionHarnessHint(r.Header.Get("X-GptAdmin-Harness"))
	origin.NativeSessionHint = executionNativeSessionHint(r.Header.Get("X-GptAdmin-Native-Session-Id"))
	origin.NativeSessionStatus = "not_supplied"
	if origin.NativeSessionHint != "" {
		origin.NativeSessionStatus = "client_declared"
	}
	if session := r.Header.Get("Mcp-Session-Id"); session != "" {
		digest := sha256.Sum256([]byte(session))
		origin.MCPSessionHash = hex.EncodeToString(digest[:])
	}
	return &origin
}

// Render from the persisted caller, never from tool arguments. Missing old
// metadata stays explicitly unknown; restoring it cannot invent a native ID.
func executionOriginForCaller(caller *taskCaller) executionOrigin {
	origin := executionOrigin{Source: "unknown_legacy_or_operator", NativeSessionStatus: "not_recorded"}
	if caller == nil {
		return origin
	}
	origin.ClientID, origin.Subject = caller.ClientID, caller.Subject
	origin.ProfileID, origin.ProfileVersion = caller.ProfileID, caller.ProfileVersion
	if saved := caller.CallerOrigin; saved != nil {
		if saved.Source == "authenticated_mcp" {
			origin.Source = saved.Source
		}
		origin.NativeHarnessHint = executionHarnessHint(saved.NativeHarnessHint)
		origin.NativeSessionHint = executionNativeSessionHint(saved.NativeSessionHint)
		origin.NativeSessionStatus = "not_supplied"
		if origin.NativeSessionHint != "" {
			origin.NativeSessionStatus = "client_declared"
		}
		if executionOriginHash.MatchString(saved.MCPSessionHash) {
			origin.MCPSessionHash = saved.MCPSessionHash
		}
	}
	return origin
}
