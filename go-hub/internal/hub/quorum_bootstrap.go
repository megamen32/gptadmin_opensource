package hub

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/megamen32/gptadmin/go-hub/internal/hub/quorum"
)

const quorumSeedBatchBytes = 4 << 20
const quorumSeedMaxRowBytes = 8 << 20

// quorumBootstrapLoop seeds the existing durable task store exactly once,
// before any quorum-enabled Hub route becomes ready. If another voter wins the
// initial election, it transfers leadership to the configured bootstrapper;
// public reachability alone never authorizes a baseline.
func (s *Server) quorumBootstrapLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		node := s.quorumNode
		if node == nil {
			return
		}
		status := node.Status()
		if status.Leader {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			record, found, err := node.ReadRecord(ctx, "cluster_meta", "task_baseline")
			cancel()
			if err == nil && found && !record.Deleted {
				return
			}
			if err == nil && !found {
				if status.NodeID != s.quorumBootstrapperID {
					if err := node.TransferLeadershipTo(s.quorumBootstrapperID); err != nil {
						log.Printf("quorum initial baseline waits for bootstrapper leadership: %v", err)
					}
				} else {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
					err := s.seedTaskQuorumBaseline(ctx)
					cancel()
					if err == nil {
						log.Printf("quorum initial task baseline committed cluster=%s", s.quorumClusterID)
						return
					}
					log.Printf("quorum initial task baseline not ready: %v", err)
				}
			}
		}
		select {
		case <-s.taskRuntimeStop:
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) seedTaskQuorumBaseline(ctx context.Context) error {
	if s.quorumNode == nil || s.quorumNode.Status().NodeID != s.quorumBootstrapperID {
		return errors.New("only the enrolled bootstrapper can seed the initial task baseline")
	}
	if err := s.quorumNode.VerifyLeader(ctx); err != nil {
		return err
	}
	if err := s.withTaskDatabase(func(*taskTransaction) error { return nil }); err != nil {
		return err
	}
	digest := sha256.New()
	counts := map[string]int64{"task_records": 0, "task_controls": 0, "task_requests": 0}
	batch := make([]quorum.Mutation, 0, 32)
	batchBytes := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.quorumNode.ApplyBatch(ctx, batch); err != nil {
			return err
		}
		for _, mutation := range batch {
			counts[mutation.Namespace]++
			encoded, err := json.Marshal(mutation)
			if err != nil {
				return err
			}
			_, _ = digest.Write(encoded)
			_, _ = digest.Write([]byte{'\n'})
		}
		batch = batch[:0]
		batchBytes = 0
		return nil
	}
	appendMutation := func(mutation quorum.Mutation) error {
		encoded, err := json.Marshal(mutation)
		if err != nil {
			return err
		}
		if len(encoded) > quorumSeedMaxRowBytes {
			return fmt.Errorf("task baseline row exceeds %d-byte encrypted record budget", quorumSeedMaxRowBytes)
		}
		if len(batch) > 0 && batchBytes+len(encoded) > quorumSeedBatchBytes {
			if err := flush(); err != nil {
				return err
			}
		}
		batch = append(batch, mutation)
		batchBytes += len(encoded)
		return nil
	}

	var lastKind, lastKey string
	for {
		page := make([]quorum.Mutation, 0, 32)
		err := s.withTaskReadDatabase(func(tx *sql.Tx) error {
			rows, err := tx.Query(`SELECT r.kind,r.key,r.updated,r.done,r.payload,r.revision,COALESCE(v.revision,1)
FROM task_records AS r LEFT JOIN task_quorum_revisions AS v
ON v.namespace='task_records/'||r.kind AND v.row_key=r.key
WHERE r.kind>? OR (r.kind=? AND r.key>?) ORDER BY r.kind,r.key LIMIT 32`, lastKind, lastKind, lastKey)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var row quorumTaskRecordRow
				var externalRevision int64
				if err := rows.Scan(&row.Kind, &row.Key, &row.Updated, &row.Done, &row.Payload, &row.Revision, &externalRevision); err != nil {
					return err
				}
				value, err := json.Marshal(row)
				if err != nil {
					return err
				}
				page = append(page, quorum.Mutation{Namespace: "task_records/" + row.Kind, Key: row.Key, External: true, Record: quorum.Record{Revision: uint64(externalRevision), Value: value}})
			}
			return rows.Err()
		})
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		for _, mutation := range page {
			if err := appendMutation(mutation); err != nil {
				return err
			}
		}
		lastKind = strings.TrimPrefix(page[len(page)-1].Namespace, "task_records/")
		lastKey = page[len(page)-1].Key
	}

	var lastKeyCursor string
	for {
		page := make([]quorum.Mutation, 0, 32)
		err := s.withTaskReadDatabase(func(tx *sql.Tx) error {
			rows, err := tx.Query(`SELECT c.task_id,c.server,c.next_attempt,COALESCE(v.revision,1)
FROM task_controls AS c LEFT JOIN task_quorum_revisions AS v
ON v.namespace='task_controls' AND v.row_key=c.task_id
WHERE c.task_id>? ORDER BY c.task_id LIMIT 32`, lastKeyCursor)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var row quorumTaskControlRow
				var revision int64
				if err := rows.Scan(&row.TaskID, &row.Server, &row.NextAttempt, &revision); err != nil {
					return err
				}
				value, err := json.Marshal(row)
				if err != nil {
					return err
				}
				page = append(page, quorum.Mutation{Namespace: "task_controls", Key: row.TaskID, External: true, Record: quorum.Record{Revision: uint64(revision), Value: value}})
			}
			return rows.Err()
		})
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		for _, mutation := range page {
			if err := appendMutation(mutation); err != nil {
				return err
			}
		}
		lastKeyCursor = page[len(page)-1].Key
	}

	lastKeyCursor = ""
	for {
		page := make([]quorum.Mutation, 0, 32)
		err := s.withTaskReadDatabase(func(tx *sql.Tx) error {
			rows, err := tx.Query(`SELECT r.key,r.fingerprint,r.owner,r.created,r.job_id,r.response,r.status,COALESCE(v.revision,1)
FROM task_requests AS r LEFT JOIN task_quorum_revisions AS v
ON v.namespace='task_requests' AND v.row_key=r.key
WHERE r.key>? ORDER BY r.key LIMIT 32`, lastKeyCursor)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var row quorumTaskRequestRow
				var revision int64
				if err := rows.Scan(&row.Key, &row.Fingerprint, &row.Owner, &row.Created, &row.JobID, &row.Response, &row.Status, &revision); err != nil {
					return err
				}
				value, err := json.Marshal(row)
				if err != nil {
					return err
				}
				page = append(page, quorum.Mutation{Namespace: "task_requests", Key: row.Key, External: true, Record: quorum.Record{Revision: uint64(revision), Value: value}})
			}
			return rows.Err()
		})
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		for _, mutation := range page {
			if err := appendMutation(mutation); err != nil {
				return err
			}
		}
		lastKeyCursor = page[len(page)-1].Key
	}
	if err := flush(); err != nil {
		return err
	}
	if err := s.seedQuorumAuthorityBaseline(ctx); err != nil {
		return fmt.Errorf("seed quorum authority state: %w", err)
	}
	manifest, err := json.Marshal(map[string]any{
		"cluster_id":        s.quorumClusterID,
		"records":           counts["task_records"],
		"controls":          counts["task_controls"],
		"requests":          counts["task_requests"],
		"sha256":            hex.EncodeToString(digest.Sum(nil)),
		"authority_domains": quorumRequiredAuthorityDomains,
	})
	if err != nil {
		return err
	}
	return s.quorumNode.ApplyRecord(ctx, "cluster_meta", "task_baseline", quorum.Record{Revision: 1, Value: manifest})
}

var quorumRequiredAuthorityDomains = []string{
	"auth", "membership", "registry", "virtual_mcp", "instruction_sets_named",
	"instruction_sets_default", "secret_ingress", "webhooks", "webhook_routes", "security", "webauthn",
}

var quorumRequiredAuthorityRecords = map[string]string{
	"auth":                     "bundle",
	"membership":               "writer",
	"registry":                 "current",
	"virtual_mcp":              "current",
	"instruction_sets_named":   "current",
	"instruction_sets_default": "current",
	"secret_ingress":           "current",
	"webhooks":                 "current",
	"webhook_routes":           "current",
	"security":                 "settings",
	"webauthn":                 "credentials",
}

func (s *Server) seedQuorumAuthorityBaseline(ctx context.Context) error {
	if s.quorumNode == nil {
		return errors.New("quorum node is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.authMu.RLock()
	s.mu.Lock()
	if !s.authInitialized {
		s.mu.Unlock()
		s.authMu.RUnlock()
		return errors.New("auth continuity is not initialized")
	}
	authBundle, err := s.readAuthSlot(s.authState)
	if err == nil {
		s.excludeSnapshotControlCredentials(&authBundle)
	}
	s.mu.Unlock()
	s.authMu.RUnlock()
	if err != nil {
		return fmt.Errorf("read authoritative auth bundle: %w", err)
	}
	if err := s.commitQuorumAuthorityState("auth", "bundle", authBundle); err != nil {
		return err
	}

	s.membershipMu.Lock()
	membership := membershipQuorumAuthorityState(s.membership)
	s.membershipMu.Unlock()
	if membership == nil {
		return errors.New("authoritative node membership is not initialized")
	}
	if err := s.commitQuorumAuthorityState("membership", "writer", membership); err != nil {
		return err
	}

	s.mu.Lock()
	if err := s.saveRegistryStateLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	if err := s.saveVirtualMCPStateLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	if err := s.saveWebhookStateLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	webhookRoutes := make(map[string]WebhookRoute, len(s.webhookRoutes))
	for id, route := range s.webhookRoutes {
		webhookRoutes[id] = route
	}
	security := s.security
	webauthn := s.webauthnState
	s.mu.Unlock()
	if err := s.commitQuorumAuthorityState("webhook_routes", "current", webhookRoutes); err != nil {
		return err
	}
	if err := s.commitQuorumAuthorityState("security", "settings", security); err != nil {
		return err
	}
	if err := s.commitQuorumAuthorityState("webauthn", "credentials", webauthn); err != nil {
		return err
	}

	s.instructionSetsMu.RLock()
	namedInstructions := instructionSetsState{Sets: cloneInstructionSets(s.instructionSets)}
	s.instructionSetsMu.RUnlock()
	if err := s.commitQuorumAuthorityState("instruction_sets_named", "current", namedInstructions); err != nil {
		return err
	}
	if err := s.commitQuorumAuthorityState("instruction_sets_default", "current", s.instructionSetSnapshot()); err != nil {
		return err
	}
	if s.secretStore == nil || s.secretStoreErr != nil {
		return errors.New("secret ingress authority is unavailable")
	}
	secretState, err := s.secretStore.quorumAuthorityState()
	if err != nil {
		return err
	}
	if err := s.commitQuorumAuthorityState("secret_ingress", "current", secretState); err != nil {
		return err
	}
	return nil
}
