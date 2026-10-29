package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
)

// Pending executable input is encrypted with the quorum state key before it
// enters SQLite or the replicated log. It is retained only while a shell job
// is known not to have been dispatched. Started and uncertain jobs are never
// reconstructed for replay.
const quorumPendingShellNamespace = "queued-shell-input"

type quorumPendingShellInput struct {
	Arguments     map[string]any       `json:"arguments,omitempty"`
	Cmd           string               `json:"cmd,omitempty"`
	Cwd           string               `json:"cwd,omitempty"`
	Env           map[string]any       `json:"env,omitempty"`
	SecretValues  []string             `json:"secret_values,omitempty"`
	FilesystemACL *filesystemACLPolicy `json:"filesystem_acl,omitempty"`
}

func (s *Server) sealPendingShellInput(job *shellJob) (json.RawMessage, error) {
	if s.cfg.QuorumConfigFile == "" || job == nil || job.Status != "queued" || job.StartedAt != 0 {
		return nil, nil
	}
	if job.quorumPendingUnmaterialized {
		if len(job.quorumPendingSealed) == 0 {
			return nil, errors.New("queued quorum input is unavailable")
		}
		return append(json.RawMessage(nil), job.quorumPendingSealed...), nil
	}
	if s.quorumNode == nil {
		return nil, errors.New("queued quorum input cipher is unavailable")
	}
	payload := quorumPendingShellInput{Arguments: job.Arguments, Cmd: job.Cmd, Cwd: job.Cwd, Env: job.Env, SecretValues: job.SecretValues, FilesystemACL: job.FilesystemACL}
	plain, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(plain)
	if len(job.quorumPendingSealed) > 0 && job.quorumPendingDigest == digest {
		return append(json.RawMessage(nil), job.quorumPendingSealed...), nil
	}
	sealed, err := s.quorumNode.SealExternalValue(quorumPendingShellNamespace, job.ID, 1, plain)
	if err != nil {
		return nil, err
	}
	job.quorumPendingSealed = append(json.RawMessage(nil), sealed...)
	job.quorumPendingDigest = digest
	return sealed, nil
}

// recoverQuorumPendingShellLocked claims a bounded page only after the local
// node proves majority leadership and an up-to-date replicated state. A queued
// record was never dispatched: dispatch durably marks it running before any
// bytes are delivered. Started and uncertain records are never selected.
// Callers hold s.mu and must still persist the later dispatch before delivery.
func (s *Server) recoverQuorumPendingShellLocked(ctx context.Context, name string) error {
	if s.quorumNode == nil || s.cfg.QuorumConfigFile == "" {
		return nil
	}
	if s.quorumNode.Status().VoterCount < quorumMinimumVoters {
		return errors.New("quorum voter set is not ready")
	}
	if err := s.quorumNode.VerifyLeader(ctx); err != nil {
		return err
	}
	if err := s.taskQuorumReplicaReady(); err != nil {
		return err
	}
	owner, err := s.ensureTaskOwnerLocked()
	if err != nil {
		return err
	}
	where := "kind='shell' AND json_extract(payload,'$.server')=? AND json_extract(payload,'$.status')='queued' AND COALESCE(json_extract(payload,'$.started_at'),0)=0 AND json_type(payload,'$.sealed_pending')='object' AND COALESCE(json_extract(payload,'$.owner_id'),'')<>? ORDER BY json_extract(payload,'$.created_at'),key LIMIT 100"
	state, err := s.readTaskRecordsSQL(where, false, []any{name, owner})
	if err != nil {
		return err
	}
	tasks := make([]persistedShellTask, 0, len(state.Shell))
	for _, p := range state.Shell {
		tasks = append(tasks, p)
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].CreatedAt != tasks[j].CreatedAt {
			return tasks[i].CreatedAt < tasks[j].CreatedAt
		}
		return tasks[i].ID < tasks[j].ID
	})
	recovered := false
	for _, p := range tasks {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.quorumNode.VerifyLeader(ctx); err != nil {
			return err
		}
		if err := s.refreshTaskRecordsLocked(p.ID); err != nil {
			return err
		}
		job := s.shellJobs[p.ID]
		if job == nil || job.Revision != p.Revision || job.Status != "queued" || job.StartedAt != 0 || !job.quorumPendingUnmaterialized {
			continue
		}
		plain, err := s.quorumNode.OpenExternalValue(quorumPendingShellNamespace, p.ID, 1, p.SealedPending)
		if err != nil {
			return fmt.Errorf("authenticate queued quorum task input: %w", err)
		}
		decoder := json.NewDecoder(bytes.NewReader(plain))
		decoder.UseNumber()
		decoder.DisallowUnknownFields()
		var input quorumPendingShellInput
		if err := decoder.Decode(&input); err != nil {
			return errors.New("invalid queued quorum task input")
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return errors.New("invalid trailing queued quorum task input")
		}
		change := s.beginTaskMutationLocked(p.ID)
		job.OwnerID = owner
		job.Arguments = input.Arguments
		job.Cmd = input.Cmd
		job.Cwd = input.Cwd
		job.Env = input.Env
		job.SecretValues = input.SecretValues
		job.FilesystemACL = input.FilesystemACL
		job.quorumPendingSealed = append(json.RawMessage(nil), p.SealedPending...)
		job.quorumPendingDigest = sha256.Sum256(plain)
		job.quorumPendingUnmaterialized = false
		if err := change.commit(); err != nil {
			return err
		}
		found := false
		for _, id := range s.shellQueues[name] {
			if id == p.ID {
				found = true
				break
			}
		}
		if !found {
			s.shellQueues[name] = append(s.shellQueues[name], p.ID)
		}
		recovered = true
	}
	if recovered {
		s.cond.Broadcast()
	}
	return nil
}
