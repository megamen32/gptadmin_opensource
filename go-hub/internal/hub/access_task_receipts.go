package hub

import (
	"errors"
	"net/http"
)

// taskCaller is persisted with each newly submitted result-bearing job. It is
// transport identity only: callers cannot supply or override it in tool args.
type taskCaller struct {
	ClientID       string `json:"client_id,omitempty"`
	TokenID        string `json:"token_id,omitempty"`
	Subject        string `json:"subject,omitempty"`
	ProfileID      string `json:"profile_id,omitempty"`
	ProfileVersion int64  `json:"profile_version,omitempty"`
}

func taskCallerFromRequest(r *http.Request) *taskCaller {
	if r == nil {
		return nil
	}
	claims, _ := r.Context().Value(authClaimsContextKey{}).(map[string]any)
	if len(claims) == 0 {
		return nil
	}
	caller := &taskCaller{
		ClientID:  firstString(claims, "client_id"),
		TokenID:   firstString(claims, "jti"),
		Subject:   firstString(claims, "sub"),
		ProfileID: AccessProfileIDFromRequest(r),
	}
	if profile, ok := AccessProfileFromRequest(r); ok {
		caller.ProfileVersion = int64(profile.Version)
	}
	if caller.ClientID == "" && caller.TokenID == "" && caller.Subject == "" {
		return nil
	}
	return caller
}

func sameTaskCaller(a, b *taskCaller) bool {
	return a != nil && b != nil && a.ClientID == b.ClientID && a.TokenID == b.TokenID &&
		a.Subject == b.Subject && a.ProfileID == b.ProfileID && a.ProfileVersion == b.ProfileVersion
}

// authorizeTaskReceipt is called before formatting, polling, or acknowledging
// a persisted job. Admin/control credentials retain their operator view; a
// managed client must prove the exact submitting identity and current profile.
func (s *Server) authorizeTaskReceipt(r *http.Request, owner *taskCaller, filesystemACL *filesystemACLPolicy) error {
	if r == nil {
		return nil // internal worker paths use their existing owner binding
	}
	claims, _ := r.Context().Value(authClaimsContextKey{}).(map[string]any)
	if len(claims) == 0 {
		return nil
	}
	profile, bound := AccessProfileFromRequest(r)
	if s.requestHasAdminRole(r) && !bound {
		return nil // unscoped administrators retain the operator history view
	}
	caller := taskCallerFromRequest(r)
	if !sameTaskCaller(caller, owner) {
		return errors.New("task receipt is not available to this MCP client")
	}
	if owner.ProfileID != "" {
		if !bound || profile.ID != owner.ProfileID || profile.Version != owner.ProfileVersion {
			return errors.New("task receipt profile is stale")
		}
		profiles, err := s.accessProfilesSnapshot()
		if err != nil {
			return errors.New("task receipt profile state is unavailable")
		}
		current, ok := profiles[owner.ProfileID]
		if !ok || current.Version != owner.ProfileVersion {
			return errors.New("task receipt profile is stale")
		}
	}
	if filesystemACL != nil && (!bound || owner.ProfileID == "" || int64(profile.Version) != filesystemACL.ProfileVersion) {
		return errors.New("filesystem-scoped task receipt is not available under the current profile")
	}
	return nil
}

func (s *Server) taskReceiptForRequest(r *http.Request, jobID string, detail any) (map[string]any, bool) {
	s.mu.Lock()
	if err := s.refreshTaskRecordsLocked(jobID); err != nil {
		s.mu.Unlock()
		return taskPersistenceFailure(err), true
	}
	relay, shell := s.relayJobs[jobID], s.shellJobs[jobID]
	var caller *taskCaller
	var filesystemACL *filesystemACLPolicy
	if relay != nil && relay.Caller != nil {
		copy := *relay.Caller
		caller = &copy
	}
	if shell != nil {
		if shell.Caller != nil {
			copy := *shell.Caller
			caller = &copy
		}
		filesystemACL = cloneFilesystemACLPolicy(shell.FilesystemACL)
	}
	s.mu.Unlock()
	if relay == nil && shell == nil {
		return map[string]any{"status": "failed", "error": "unknown job", "job_id": jobID}, false
	}
	if s.authorizeTaskReceipt(r, caller, filesystemACL) != nil {
		return map[string]any{"status": "failed", "error": "unknown job", "job_id": jobID}, false
	}
	s.mu.Lock()
	var response map[string]any
	toolName := ""
	if relay != nil {
		if s.relayJobs[jobID] != relay {
			s.mu.Unlock()
			return map[string]any{"status": "failed", "error": "unknown job", "job_id": jobID}, false
		}
		response = relayJobResponse(relay)
	} else if s.shellJobs[jobID] != shell {
		s.mu.Unlock()
		return map[string]any{"status": "failed", "error": "unknown job", "job_id": jobID}, false
	} else {
		response = shellJobResponse(shell)
		toolName = shell.ToolName
	}
	s.mu.Unlock()
	return s.formatToolOutput(response, toolName, detail), true
}
