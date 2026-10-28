package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// filesystemACLPolicy travels only in a Hub-owned queue envelope, outside MCP
// tool arguments. The authenticated executor checks it against the job and
// asks the Hub to revalidate it immediately before running the callback.
type filesystemACLPolicy struct {
	Version         int       `json:"version"`
	ProfileID       string    `json:"profile_id"`
	ProfileVersion  int64     `json:"profile_version"`
	RootsDigest     string    `json:"roots_digest"`
	JobID           string    `json:"job_id"`
	Target          string    `json:"target"`
	Tool            string    `json:"tool"`
	ArgumentsSHA256 string    `json:"args_sha256"`
	ReadRoots       *[]string `json:"read_roots,omitempty"`
	WriteRoots      *[]string `json:"write_roots,omitempty"`
}

func cloneFilesystemACLPolicy(policy *filesystemACLPolicy) *filesystemACLPolicy {
	if policy == nil {
		return nil
	}
	cloned := *policy
	cloned.ReadRoots = cloneStringSlicePointer(policy.ReadRoots)
	cloned.WriteRoots = cloneStringSlicePointer(policy.WriteRoots)
	return &cloned
}

func filesystemACLRootsDigest(profile AccessProfile) string {
	b, _ := json.Marshal(struct {
		ReadRoots  *[]string `json:"read_roots,omitempty"`
		WriteRoots *[]string `json:"write_roots,omitempty"`
	}{profile.ReadRoots, profile.WriteRoots})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func filesystemACLArgumentsDigest(args map[string]any) (string, error) {
	b, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func filesystemACLForRequest(r *http.Request, target, tool string, args map[string]any) (*filesystemACLPolicy, error) {
	profile, ok := AccessProfileFromRequest(r)
	if !ok || !filesystemACLActive(profile) {
		return nil, nil
	}
	if tool == "mcp_call" {
		if !filesystemACLSearchCall(args) {
			return nil, errors.New("filesystem-bound profile permits only GrepMesh search/read_text")
		}
		if profile.ReadRoots == nil || len(*profile.ReadRoots) == 0 {
			return nil, errors.New("GrepMesh access requires non-empty read_roots")
		}
	} else if tool != "file_editor" && tool != "system_inspect" {
		return nil, nil
	}
	digest, err := filesystemACLArgumentsDigest(args)
	if err != nil {
		return nil, fmt.Errorf("cannot bind filesystem ACL to arguments: %w", err)
	}
	return &filesystemACLPolicy{
		Version: 1, ProfileID: profile.ID, ProfileVersion: profile.Version,
		RootsDigest: filesystemACLRootsDigest(profile), Target: target, Tool: tool,
		ArgumentsSHA256: digest, ReadRoots: cloneStringSlicePointer(profile.ReadRoots),
		WriteRoots: cloneStringSlicePointer(profile.WriteRoots),
	}, nil
}

func filesystemACLSearchCall(args map[string]any) bool {
	ref := strings.ToLower(strings.TrimSpace(firstString(args, "ref")))
	name := strings.ToLower(strings.TrimSpace(firstString(args, "name")))
	if ref != "grepmesh" || (name != "search" && name != "search_text" && name != "read_text") {
		return false
	}
	return true
}

func (s *Server) filesystemACLExecutorCapableLocked(server string) bool {
	agent := s.agents["shell:"+canonicalShellQueueName(server)]
	return agent != nil && intFromAny(agent.Meta["filesystem_acl_version"]) >= 1 && truthyAny(agent.Meta["approved"])
}

func (s *Server) validateFilesystemACLJobLocked(job *shellJob, argsDigest string) error {
	policy := job.FilesystemACL
	if policy == nil || policy.Version != 1 || policy.JobID != job.ID || policy.Tool != job.ToolName ||
		policy.Target == "" || policy.ProfileID == "" || policy.ProfileVersion < 1 || policy.RootsDigest == "" ||
		policy.ArgumentsSHA256 != argsDigest {
		return errors.New("invalid filesystem ACL job binding")
	}
	policyServer, ok := filesystemACLTargetServer(policy.Target)
	if !ok || policyServer != canonicalShellQueueName(job.Server) {
		return errors.New("filesystem ACL target does not match executor")
	}
	profile, ok := s.accessProfiles[policy.ProfileID]
	if !ok || profile.Version != policy.ProfileVersion || !filesystemACLActive(profile) ||
		filesystemACLRootsDigest(profile) != policy.RootsDigest {
		return errors.New("filesystem ACL profile changed after enqueue")
	}
	if filesystemACLRootsDigest(AccessProfile{ReadRoots: policy.ReadRoots, WriteRoots: policy.WriteRoots}) != policy.RootsDigest {
		return errors.New("filesystem ACL roots do not match the bound profile")
	}
	return nil
}

func filesystemACLTargetServer(target string) (string, bool) {
	for _, prefix := range []string{"file:", "shell:"} {
		if strings.HasPrefix(target, prefix) {
			name := strings.TrimSpace(strings.TrimPrefix(target, prefix))
			if name == "" || strings.ContainsAny(name, "/\\") {
				return "", false
			}
			return canonicalShellQueueName(name), true
		}
	}
	return "", false
}
