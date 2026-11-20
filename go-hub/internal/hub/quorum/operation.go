package quorum

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Only routing and deduplication metadata enter Raft. The owner keeps the
// original arguments, response and evidence of delivery in its local store.
type Operation struct {
	Owner       string `json:"owner"`
	Fingerprint string `json:"fingerprint"`
	Witness     string `json:"witness"` // identifies the owner's durable local reservation
	JobID       string `json:"job_id,omitempty"`
	Expires     int64  `json:"expires,omitempty"` // zero until the owner establishes a terminal outcome
}

type operationCommand struct {
	Action string `json:"action"`
	Key    string `json:"key"`
	Operation
	Now int64 `json:"now"`
}

type OperationDecision struct {
	Operation Operation
	Created   bool
}

// An offline owner cannot exhaust another voter's admission budget.
const OperationLimitPerOwner = 1024
const OperationRetention = 15 * time.Minute

var ErrOperationCapacity = errors.New("quorum: request reservation budget exhausted for this owner")
var ErrOperationConflict = errors.New("quorum: request key belongs to different arguments")
var ErrOperationOwner = errors.New("quorum: operation belongs to another reservation")

func operationDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && s == strings.ToLower(s)
}

func operationLabel(label string, max int) bool {
	if label == "" || len(label) > max {
		return false
	}
	for _, b := range []byte(label) {
		if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b == '-' || b == '_' || b == '.') {
			return false
		}
	}
	return true
}

func (c operationCommand) valid() bool {
	return operationDigest(c.Key) && operationDigest(c.Fingerprint) && operationDigest(c.Witness) &&
		operationLabel(c.Owner, 256) && (c.JobID == "" || operationLabel(c.JobID, 512)) &&
		c.Now > 0 && c.Expires == 0 &&
		(c.Action == "claim" || c.Action == "bind" || c.Action == "terminal" || c.Action == "release")
}

// applyOperation runs as one deterministic FSM command. It bypasses the
// external SQLite callback, including on the leader holding a local write tx.
func (s *stateMachine) applyOperation(c operationCommand) applyResult {
	if !c.valid() {
		return applyResult{err: errors.New("quorum: invalid operation metadata")}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.operations == nil {
		s.operations = make(map[string]Operation)
	}
	old, found := s.operations[c.Key]
	if c.Action == "claim" {
		if c.JobID != "" {
			return applyResult{err: errors.New("quorum: new claim carries a job ID")}
		}
		if found && old.Expires > 0 && old.Expires <= c.Now {
			delete(s.operations, c.Key)
			found = false
		}
		if found {
			if old.Fingerprint != c.Fingerprint {
				return applyResult{err: ErrOperationConflict}
			}
			return applyResult{operation: OperationDecision{Operation: old}}
		}
		if len(s.operations) >= OperationLimitPerOwner {
			count := 0
			for key, entry := range s.operations {
				if entry.Expires > 0 && entry.Expires <= c.Now {
					delete(s.operations, key)
				} else if entry.Owner == c.Owner {
					count++
				}
			}
			if count >= OperationLimitPerOwner {
				return applyResult{err: ErrOperationCapacity}
			}
		}
		s.operations[c.Key] = c.Operation
		return applyResult{operation: OperationDecision{Operation: c.Operation, Created: true}}
	}
	if !found || old.Owner != c.Owner || old.Witness != c.Witness || old.Fingerprint != c.Fingerprint {
		return applyResult{err: ErrOperationOwner}
	}
	if old.JobID != "" && old.JobID != c.JobID {
		return applyResult{err: ErrOperationOwner}
	}
	switch c.Action {
	case "bind":
		if c.JobID == "" || old.Expires != 0 && old.JobID == "" {
			return applyResult{err: ErrOperationOwner}
		}
		old.JobID = c.JobID
	case "terminal":
		old.JobID = c.JobID
		if old.Expires == 0 {
			old.Expires = c.Now + int64(OperationRetention/time.Second)
		}
	case "release":
		// The owning Hub has positively fenced a pre-dispatch rejection.
		if old.JobID != "" || old.Expires != 0 {
			return applyResult{err: ErrOperationOwner}
		}
		delete(s.operations, c.Key)
		return applyResult{}
	default:
		return applyResult{err: errors.New("quorum: unknown operation transition")}
	}
	s.operations[c.Key] = old
	return applyResult{operation: OperationDecision{Operation: old}}
}

// UpdateOperation waits for majority commit. In case of timeout the caller
// reconciles with the same scoped key; uncertainty never authorizes dispatch.
func (n *Node) UpdateOperation(ctx context.Context, action, key string, op Operation) (OperationDecision, error) {
	if op.Owner != n.config.NodeID {
		return OperationDecision{}, ErrOperationOwner
	}
	if err := n.verifyLeader(ctx); err != nil {
		return OperationDecision{}, err
	}
	cmd := operationCommand{Action: action, Key: key, Operation: op, Now: time.Now().Unix()}
	if !cmd.valid() {
		return OperationDecision{}, errors.New("quorum: invalid operation metadata")
	}
	data, err := json.Marshal(command{ClusterID: n.config.ClusterID, Operation: &cmd})
	if err != nil {
		return OperationDecision{}, err
	}
	future := n.raft.Apply(data, deadlineFor(ctx, n.config.ApplyDeadline))
	if err := future.Error(); err != nil {
		return OperationDecision{}, err
	}
	result, ok := future.Response().(applyResult)
	if !ok {
		return OperationDecision{}, errors.New("quorum: unexpected operation response")
	}
	return result.operation, result.err
}

// LocalOperation is only a hint for reconciliation on its original owner.
// Admission uses the majority-committed claim.
func (n *Node) LocalOperation(key string) (Operation, bool) {
	n.fsm.mu.RLock()
	defer n.fsm.mu.RUnlock()
	op, ok := n.fsm.operations[key]
	return op, ok
}
