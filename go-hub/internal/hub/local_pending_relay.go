package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
)

const localPendingRelayNamespace = "queued-relay-input"

// Only never-dispatched standalone requests are executable after recovery.
// Task groups remain metadata; approvals must not turn into executable work.
func (s *Server) sealPendingRelayInput(j *relayJob) (json.RawMessage, error) {
	if s.cfg.ConfigDir == "" || (j.Method == "task/group" || j.Method == childSchemaMethod) || j.Status != "queued" || j.StartedAt != 0 || j.DoneAt != 0 {
		return nil, nil
	}
	if j.pendingUnmaterialized {
		if len(j.pendingSealed) == 0 {
			return nil, errors.New("queued relay input unavailable")
		}
		return append(json.RawMessage(nil), j.pendingSealed...), nil
	}
	plain, err := json.Marshal(j.Params)
	if err != nil {
		return nil, err
	}
	return s.sealPendingValue(localPendingRelayNamespace, j.ID, plain)
}

// Poll, not receipt lookup, claims executable input. Shared DB revisions and
// the original owner's OS lock fence concurrent local owners; quorum uses the
// same leader/replica checks as the existing pending-shell path.
func (s *Server) recoverPendingRelayLocked(ctx context.Context, agentID string) error {
	if s.cfg.ConfigDir == "" {
		return nil
	}
	if s.cfg.QuorumConfigFile != "" {
		if s.quorumNode == nil || s.quorumNode.Status().VoterCount < quorumMinimumVoters {
			return errors.New("pending quorum unavailable")
		}
		if err := s.quorumNode.VerifyLeader(ctx); err != nil {
			return err
		}
		if err := s.taskQuorumReplicaReady(); err != nil {
			return err
		}
	}
	owner, err := s.ensureTaskOwnerLocked()
	if err != nil {
		return err
	}
	state, err := s.readTaskRecordsSQL("kind='relay' AND json_extract(payload,'$.agent_id')=? AND json_extract(payload,'$.status')='queued' AND COALESCE(json_extract(payload,'$.started_at'),0)=0 AND COALESCE(json_extract(payload,'$.completed_at'),0)=0 AND json_type(payload,'$.sealed_pending')='object' AND COALESCE(json_extract(payload,'$.owner_id'),'')<>? ORDER BY json_extract(payload,'$.created_at'),key LIMIT 100", false, []any{agentID, owner})
	if err != nil {
		return err
	}
	tasks := make([]persistedRelayTask, 0, len(state.Relay))
	for _, p := range state.Relay {
		tasks = append(tasks, p)
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].CreatedAt != tasks[j].CreatedAt {
			return tasks[i].CreatedAt < tasks[j].CreatedAt
		}
		return tasks[i].ID < tasks[j].ID
	})
	for _, p := range tasks {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.cfg.QuorumConfigFile != "" {
			if err := s.quorumNode.VerifyLeader(ctx); err != nil {
				return err
			}
		} else {
			alive, err := s.taskOwnerAlive(p.OwnerID)
			if err != nil {
				return err
			}
			if alive {
				continue
			}
		}
		if err := s.refreshTaskRecordsLocked(p.ID); err != nil {
			return err
		}
		j := s.relayJobs[p.ID]
		if j == nil || j.Revision != p.Revision || j.Status != "queued" || j.StartedAt != 0 || j.DoneAt != 0 || (j.Method == "task/group" || j.Method == childSchemaMethod) || !j.pendingUnmaterialized {
			continue
		}
		if expired, err := s.expireQueuedRequestLocked(j.ID, j.CreatedAt); err != nil {
			return err
		} else if expired {
			continue
		}
		plain, err := s.openPendingValue(localPendingRelayNamespace, j.ID, p.SealedPending)
		if err != nil {
			return errors.New("authenticate queued relay input failed")
		}
		decoder := json.NewDecoder(bytes.NewReader(plain))
		decoder.UseNumber()
		var params map[string]any
		if err := decoder.Decode(&params); err != nil {
			return errors.New("invalid queued relay input")
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return errors.New("invalid trailing queued relay input")
		}
		change := s.beginTaskMutationLocked(j.ID)
		j.OwnerID = owner
		j.Params = params
		j.pendingUnmaterialized = false
		if err := change.commit(); err != nil {
			return err
		}
		found := false
		for _, id := range s.relayQueues[agentID] {
			if id == j.ID {
				found = true
				break
			}
		}
		if !found {
			s.relayQueues[agentID] = append(s.relayQueues[agentID], j.ID)
		}
	}
	return nil
}
