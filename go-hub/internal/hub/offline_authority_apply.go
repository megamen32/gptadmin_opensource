package hub

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type AuthorityHandoffApplyOptions struct {
	PreparedDir string
	PlanDigest  string
	BackupDir   string
	// Runtime plans require a concrete reviewed last-precedence environment
	// file. Fixtures can exercise the file-format API without a launcher.
	RequireEnvironment      bool
	RequirePhysicalIdentity bool
	afterFileWrite          func(string) error // per-operation crash injection for regression fixtures
}

type AuthorityHandoffApplied struct {
	Schema        string    `json:"schema"`
	PlanDigest    string    `json:"plan_digest"`
	BackupDir     string    `json:"backup_dir"`
	ConfigDir     string    `json:"config_dir"`
	AppliedAt     time.Time `json:"applied_at"`
	Files         int       `json:"files"`
	LegacyRecords int       `json:"legacy_records"`
	Activated     bool      `json:"activated"`
}

type authorityHandoffRollback struct {
	Schema            string               `json:"schema"`
	PlanDigest        string               `json:"plan_digest"`
	Plan              AuthorityHandoffPlan `json:"plan"`
	PendingAuthDigest string               `json:"pending_auth_digest"`
}

func handoffReadPlan(directory, digest string) (AuthorityHandoffPlan, error) {
	var plan AuthorityHandoffPlan
	path := filepath.Join(directory, "plan.json")
	actual, err := handoffFileDigest(path)
	if err != nil {
		return plan, err
	}
	if digest == "" || actual != digest {
		return plan, errors.New("handoff plan differs from the reviewed digest")
	}
	if err := handoffReadJSON(path, &plan, false, authorityHandoffBudget); err != nil {
		return plan, err
	}
	if plan.Schema != authorityHandoffSchema || (plan.Role != "writer" && plan.Role != "reader") || !filepath.IsAbs(plan.ConfigDir) || plan.LocalID == "" || len(plan.Files) == 0 {
		return plan, errors.New("invalid authority handoff plan")
	}
	return plan, nil
}

func handoffDestination(cfg Config, plan AuthorityHandoffPlan, file AuthorityHandoffFile) (string, error) {
	if file.Relative == "" || filepath.IsAbs(file.Relative) || filepath.Clean(file.Relative) != file.Relative || file.Relative == ".." || strings.HasPrefix(file.Relative, ".."+string(os.PathSeparator)) {
		return "", errors.New("handoff staged path is invalid")
	}
	destination := filepath.Join(cfg.ConfigDir, file.Relative)
	if file.Destination != "" {
		if file.Destination != handoffIdentityPath(cfg) && (plan.EnvironmentFile == "" || file.Destination != plan.EnvironmentFile) {
			return "", errors.New("handoff external destination is not an identity or reviewed environment file")
		}
		destination = file.Destination
	}
	// A pathname containing an existing symlink is not a writable authority
	// boundary, even when the final regular file itself is not a symlink.
	for p := destination; p != filepath.Dir(p); p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("handoff destination contains a symlink")
		}
	}
	return destination, nil
}

func handoffAtomicFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".authority-handoff-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func handoffWriteFileJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return handoffAtomicFile(path, append(raw, '\n'), 0600)
}

func handoffValidateLegacy(records []quorumTaskRecordRow, requests []quorumTaskRequestRow) error {
	ids := map[string]persistedShellTask{}
	if len(records) > 4096 || len(requests) > 4096 {
		return errors.New("legacy immutable receipt budget exceeded")
	}
	for _, row := range records {
		var task persistedShellTask
		if row.Kind != "shell" || row.Key == "" || row.Revision <= 0 || row.Done <= 0 || json.Unmarshal(row.Payload, &task) != nil || task.ID != row.Key || task.DoneAt != row.Done || task.Server == "" || len(task.SealedPending) != 0 {
			return errors.New("invalid immutable legacy receipt")
		}
		if task.Status != "completed" && task.Status != "failed" && task.Status != "expired" && task.Status != "cancelled" {
			return errors.New("nonterminal legacy task cannot be imported")
		}
		// A cancelled started task is not immutable: normal startup recreates
		// its cancellation control. Leave that receipt at its original owner.
		if task.Status == "cancelled" && task.StartedAt > 0 {
			return errors.New("legacy receipt would recreate a cancellation control")
		}
		var spill func(any) bool
		spill = func(value any) bool {
			switch value := value.(type) {
			case map[string]any:
				for key, item := range value {
					if (key == "stdout_path" || key == "stderr_path") && firstString(map[string]any{key: item}, key) != "" {
						return true
					}
					if key == "_spilled" && truthy(item) {
						return true
					}
					if spill(item) {
						return true
					}
				}
			case []any:
				for _, item := range value {
					if spill(item) {
						return true
					}
				}
			}
			return false
		}
		if spill(task.Result) {
			return errors.New("legacy spilled output needs explicit owner-specific byte preservation")
		}
		if _, exists := ids[row.Key]; exists {
			return errors.New("duplicate legacy receipt ID")
		}
		ids[row.Key] = task
	}
	seen := map[string]bool{}
	for _, request := range requests {
		task, ok := ids[request.JobID]
		if !ok || request.Key == "" || task.RequestKey != request.Key || seen[request.Key] {
			return errors.New("legacy request does not bind an exact imported receipt")
		}
		seen[request.Key] = true
	}
	for _, task := range ids {
		if task.RequestKey != "" && !seen[task.RequestKey] {
			return errors.New("legacy receipt is missing its idempotency binding")
		}
	}
	return nil
}

func handoffImportLegacy(cfg Config, records []quorumTaskRecordRow, requests []quorumTaskRequestRow, dry bool) error {
	if err := handoffValidateLegacy(records, requests); err != nil {
		return err
	}
	if len(records) == 0 {
		return nil
	}
	// Use the canonical database schema/transaction manager, without New,
	// runtime loading, pruning, queue recovery, task revision rewriting or
	// pending-input decryption. Import is append-only, exact and idempotent.
	cfg.QuorumConfigFile = ""
	s := &Server{cfg: cfg}
	return s.withTaskDatabase(func(tx *taskTransaction) error {
		for _, row := range records {
			var existing quorumTaskRecordRow
			err := tx.tx.QueryRow(`SELECT kind,key,updated,done,payload,revision FROM task_records WHERE kind=? AND key=?`, row.Kind, row.Key).Scan(&existing.Kind, &existing.Key, &existing.Updated, &existing.Done, &existing.Payload, &existing.Revision)
			if err == nil {
				if !equalHandoffRecord(existing, row) {
					return errors.New("legacy receipt conflicts with a different existing exact ID")
				}
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			var conflicting int
			if err := tx.tx.QueryRow(`SELECT count(*) FROM task_records WHERE key=?`, row.Key).Scan(&conflicting); err != nil {
				return err
			}
			if conflicting != 0 {
				return errors.New("legacy shell ID collides with another task kind")
			}
			if !dry {
				if _, err := tx.tx.Exec(`INSERT INTO task_records(kind,key,updated,done,payload,revision) VALUES(?,?,?,?,?,?)`, row.Kind, row.Key, row.Updated, row.Done, row.Payload, row.Revision); err != nil {
					return err
				}
			}
		}
		for _, row := range requests {
			var old quorumTaskRequestRow
			err := tx.tx.QueryRow(`SELECT key,fingerprint,owner,created,job_id,response,status FROM task_requests WHERE key=?`, row.Key).Scan(&old.Key, &old.Fingerprint, &old.Owner, &old.Created, &old.JobID, &old.Response, &old.Status)
			if err == nil {
				if old.Key != row.Key || old.Fingerprint != row.Fingerprint || old.Owner != row.Owner || old.Created != row.Created || old.JobID != row.JobID || old.Status != row.Status || !bytes.Equal(old.Response, row.Response) {
					return errors.New("legacy idempotency binding conflicts with existing state")
				}
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if !dry {
				if _, err := tx.tx.Exec(`INSERT INTO task_requests(key,fingerprint,owner,created,job_id,response,status) VALUES(?,?,?,?,?,?,?)`, row.Key, row.Fingerprint, row.Owner, row.Created, row.JobID, row.Response, row.Status); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// ApplyStoppedAuthority changes real canonical files and appends only legacy
// terminal receipts. Its durable backup/journal supports pre-activation
// restoration. Runtime activation is a separate explicit irreversible phase:
// after any new writer admission, use a fresh reverse transfer, never old state.
func ApplyStoppedAuthority(cfg Config, options AuthorityHandoffApplyOptions) (AuthorityHandoffApplied, error) {
	var receipt AuthorityHandoffApplied
	var err error
	cfg, err = handoffConfig(cfg)
	if err != nil {
		return receipt, err
	}
	lock, err := handoffLock(cfg)
	if err != nil {
		return receipt, err
	}
	defer lock.Close()
	plan, err := handoffReadPlan(options.PreparedDir, options.PlanDigest)
	if err != nil {
		return receipt, err
	}
	if plan.ConfigDir != cfg.ConfigDir {
		return receipt, errors.New("handoff plan names another config directory")
	}
	if options.RequireEnvironment && plan.EnvironmentFile == "" && !(plan.Role == "reader" && plan.IdentityDerivedEnvironment) {
		return receipt, errors.New("runtime handoff requires a reviewed effective environment source")
	}
	if options.RequirePhysicalIdentity && plan.PhysicalIdentityFile == "" {
		return receipt, errors.New("runtime handoff requires configured physical identity proof")
	}
	if _, _, err := handoffLocalExecutor(cfg, plan.Target, plan.LocalID, plan.PhysicalIdentityFile); err != nil {
		return receipt, err
	}
	for _, guard := range plan.Guards {
		current, err := handoffOptionalDigest(guard.Path)
		if err != nil {
			return receipt, err
		}
		if current != guard.Digest {
			return receipt, errors.New("physical credential/identity guard changed")
		}
	}
	seen := map[string]bool{}
	for _, file := range plan.Files {
		path, err := handoffDestination(cfg, plan, file)
		if err != nil {
			return receipt, err
		}
		if seen[path] {
			return receipt, errors.New("duplicate handoff destination")
		}
		seen[path] = true
		before, err := handoffOptionalDigest(path)
		if err != nil {
			return receipt, err
		}
		if before != file.Before {
			return receipt, fmt.Errorf("authority CAS changed at %s", filepath.Base(path))
		}
		after, err := handoffOptionalDigest(filepath.Join(options.PreparedDir, "config", file.Relative))
		if err != nil {
			return receipt, err
		}
		if after != file.After {
			return receipt, errors.New("staged authority bytes differ from reviewed plan")
		}
	}
	if err := handoffImportLegacy(cfg, plan.LegacyRecords, plan.LegacyRequests, true); err != nil {
		return receipt, err
	}
	if err := handoffPrivateOutput(options.BackupDir); err != nil {
		return receipt, err
	}
	for i, file := range plan.Files {
		if file.Before == "" {
			continue
		}
		path, _ := handoffDestination(cfg, plan, file)
		if err := handoffCopyFile(path, filepath.Join(options.BackupDir, fmt.Sprintf("file-%04d", i))); err != nil {
			return receipt, err
		}
	}
	authPath := filepath.Join(cfg.ConfigDir, "auth-continuity", "state.json")
	var pending authSnapshotState
	if err := handoffReadJSON(authPath, &pending, false, 4096); err != nil {
		return receipt, err
	}
	pending.Pending = true
	pendingBytes, err := json.MarshalIndent(pending, "", "  ")
	if err != nil {
		return receipt, err
	}
	pendingBytes = append(pendingBytes, '\n')
	rollback := authorityHandoffRollback{Schema: authorityHandoffSchema, PlanDigest: options.PlanDigest, Plan: plan, PendingAuthDigest: handoffDigest(pendingBytes)}
	if err := handoffWritePrivate(filepath.Join(options.BackupDir, "rollback.json"), rollback); err != nil {
		return receipt, err
	}
	if err := handoffAtomicFile(authPath, pendingBytes, 0600); err != nil {
		return receipt, err
	}
	files := append([]AuthorityHandoffFile(nil), plan.Files...)
	sort.SliceStable(files, func(i, j int) bool {
		rank := func(f AuthorityHandoffFile) int {
			if f.Destination == plan.EnvironmentFile && plan.EnvironmentFile != "" {
				return 1
			}
			if f.Relative == filepath.Join("auth-continuity", "state.json") {
				return 2
			}
			return 0
		}
		return rank(files[i]) < rank(files[j])
	})
	// Import while admission is durably closed. A crash can leave harmless
	// duplicate immutable history, but cannot erase or replay existing work.
	if err := handoffImportLegacy(cfg, plan.LegacyRecords, plan.LegacyRequests, false); err != nil {
		return receipt, err
	}
	for _, file := range files {
		path, _ := handoffDestination(cfg, plan, file)
		if file.After == "" {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return receipt, err
			}
			if err := handoffSyncParent(path); err != nil {
				return receipt, err
			}
			continue
		}
		raw, err := os.ReadFile(filepath.Join(options.PreparedDir, "config", file.Relative))
		if err != nil {
			return receipt, err
		}
		if handoffDigest(raw) != file.After {
			return receipt, errors.New("staged authority changed during apply")
		}
		if err := handoffAtomicFile(path, raw, os.FileMode(file.Mode)); err != nil {
			return receipt, err
		}
		if options.afterFileWrite != nil {
			if err := options.afterFileWrite(path); err != nil {
				return receipt, err
			}
		}
	}
	receipt = AuthorityHandoffApplied{Schema: authorityHandoffSchema, PlanDigest: options.PlanDigest, BackupDir: options.BackupDir, ConfigDir: cfg.ConfigDir, AppliedAt: time.Now().UTC(), Files: len(files), LegacyRecords: len(plan.LegacyRecords)}
	if err := handoffWritePrivate(filepath.Join(options.BackupDir, "applied.json"), receipt); err != nil {
		return receipt, err
	}
	return receipt, nil
}

// MarkAuthorityActivation must run before the service manager starts a changed
// runtime. This retires snapshot rollback even if the caller later loses its
// response. Recovery after this point requires a fresh reverse authority image.
func MarkAuthorityActivation(cfg Config, backupDir, expectedPlanDigest string) error {
	var err error
	cfg, err = handoffConfig(cfg)
	if err != nil {
		return err
	}
	lock, err := handoffLock(cfg)
	if err != nil {
		return err
	}
	defer lock.Close()
	var receipt AuthorityHandoffApplied
	if err := handoffReadJSON(filepath.Join(backupDir, "applied.json"), &receipt, false, 1<<20); err != nil {
		return err
	}
	if receipt.Schema != authorityHandoffSchema || receipt.ConfigDir != cfg.ConfigDir || receipt.BackupDir != backupDir || receipt.PlanDigest != expectedPlanDigest || expectedPlanDigest == "" {
		return errors.New("activation receipt mismatch")
	}
	for _, name := range []string{"restored.json", "restore-started.json"} {
		if _, err := os.Lstat(filepath.Join(backupDir, name)); !errors.Is(err, os.ErrNotExist) {
			return errors.New("restoration began; stale apply receipt cannot authorize activation")
		}
	}
	var rollback authorityHandoffRollback
	if err := handoffReadJSON(filepath.Join(backupDir, "rollback.json"), &rollback, false, authorityHandoffBudget); err != nil {
		return err
	}
	plan := rollback.Plan
	if rollback.Schema != authorityHandoffSchema || rollback.PlanDigest != expectedPlanDigest || plan.ConfigDir != cfg.ConfigDir {
		return errors.New("activation journal mismatch")
	}
	for _, guard := range plan.Guards {
		actual, err := handoffOptionalDigest(guard.Path)
		if err != nil {
			return err
		}
		if actual != guard.Digest {
			return errors.New("activation physical state guard changed")
		}
	}
	for _, file := range plan.Files {
		path, err := handoffDestination(cfg, plan, file)
		if err != nil {
			return err
		}
		actual, err := handoffOptionalDigest(path)
		if err != nil {
			return err
		}
		if actual != file.After {
			return errors.New("activation requires every applied after-state byte")
		}
	}
	var state authSnapshotState
	if err := handoffReadJSON(filepath.Join(cfg.ConfigDir, "auth-continuity", "state.json"), &state, false, 4096); err != nil {
		return err
	}
	if state.Pending {
		return errors.New("activation auth manifest is pending")
	}
	return handoffWritePrivate(filepath.Join(backupDir, "activation-authorized.json"), map[string]any{"plan_digest": expectedPlanDigest, "at": time.Now().UTC()})
}

func RestoreStoppedAuthorityBeforeActivation(cfg Config, backupDir, expectedPlanDigest string) error {
	return restoreStoppedAuthorityBeforeActivation(cfg, backupDir, expectedPlanDigest, nil)
}

func restoreStoppedAuthorityBeforeActivation(cfg Config, backupDir, expectedPlanDigest string, afterFileWrite func(string) error) error {
	var err error
	cfg, err = handoffConfig(cfg)
	if err != nil {
		return err
	}
	lock, err := handoffLock(cfg)
	if err != nil {
		return err
	}
	defer lock.Close()
	if _, err := os.Lstat(filepath.Join(backupDir, "activation-authorized.json")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("runtime activation was authorized; fresh reverse handoff is required")
	}
	var rollback authorityHandoffRollback
	if err := handoffReadJSON(filepath.Join(backupDir, "rollback.json"), &rollback, false, authorityHandoffBudget); err != nil {
		return err
	}
	plan := rollback.Plan
	if rollback.Schema != authorityHandoffSchema || expectedPlanDigest == "" || rollback.PlanDigest != expectedPlanDigest || plan.ConfigDir != cfg.ConfigDir {
		return errors.New("rollback receipt does not bind this exact handoff")
	}
	for _, guard := range plan.Guards {
		actual, err := handoffOptionalDigest(guard.Path)
		if err != nil {
			return err
		}
		if actual != guard.Digest {
			return errors.New("physical state changed after staging; stale rollback is forbidden")
		}
	}
	for i, file := range plan.Files {
		path, err := handoffDestination(cfg, plan, file)
		if err != nil {
			return err
		}
		actual, err := handoffOptionalDigest(path)
		if err != nil {
			return err
		}
		pending := file.Relative == filepath.Join("auth-continuity", "state.json") && actual == rollback.PendingAuthDigest
		if actual != file.Before && actual != file.After && !pending {
			return errors.New("authority changed after staging; stale rollback is forbidden")
		}
		if file.Before != "" {
			digest, err := handoffFileDigest(filepath.Join(backupDir, fmt.Sprintf("file-%04d", i)))
			if err != nil {
				return err
			}
			if digest != file.Before {
				return errors.New("rollback backup bytes changed")
			}
		}
	}
	// Publish the recovery direction before touching any dependency. Its
	// durable existence prevents a stale applied receipt from authorizing
	// activation, even if the final restored receipt is lost.
	started := filepath.Join(backupDir, "restore-started.json")
	if _, err := os.Lstat(started); errors.Is(err, os.ErrNotExist) {
		if err := handoffWritePrivate(started, map[string]any{"plan_digest": expectedPlanDigest, "at": time.Now().UTC()}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	authPath := filepath.Join(cfg.ConfigDir, "auth-continuity", "state.json")
	var pendingBytes []byte
	for i, file := range plan.Files {
		if file.Relative == filepath.Join("auth-continuity", "state.json") && file.Before != "" {
			raw, err := os.ReadFile(filepath.Join(backupDir, fmt.Sprintf("file-%04d", i)))
			if err != nil {
				return err
			}
			var state authSnapshotState
			if err := json.Unmarshal(raw, &state); err != nil {
				return err
			}
			state.Pending = true
			pendingBytes, err = json.MarshalIndent(state, "", "  ")
			if err != nil {
				return err
			}
			pendingBytes = append(pendingBytes, '\n')
		}
	}
	if len(pendingBytes) == 0 || handoffDigest(pendingBytes) != rollback.PendingAuthDigest {
		return errors.New("rollback has no verified pending admission manifest")
	}
	if err := handoffAtomicFile(authPath, pendingBytes, 0600); err != nil {
		return err
	}
	if afterFileWrite != nil {
		if err := afterFileWrite(authPath); err != nil {
			return err
		}
	}
	// Restore auth manifest last, after every dependency it names is restored.
	order := make([]int, len(plan.Files))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		return plan.Files[order[i]].Relative != filepath.Join("auth-continuity", "state.json") && plan.Files[order[j]].Relative == filepath.Join("auth-continuity", "state.json")
	})
	for _, i := range order {
		file := plan.Files[i]
		path, _ := handoffDestination(cfg, plan, file)
		if file.Before == "" {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := handoffSyncParent(path); err != nil {
				return err
			}
		} else {
			raw, err := os.ReadFile(filepath.Join(backupDir, fmt.Sprintf("file-%04d", i)))
			if err != nil {
				return err
			}
			if err := handoffAtomicFile(path, raw, 0600); err != nil {
				return err
			}
		}
		if afterFileWrite != nil {
			if err := afterFileWrite(path); err != nil {
				return err
			}
		}
	}
	return handoffWritePrivate(filepath.Join(backupDir, "restored.json"), map[string]any{"plan_digest": expectedPlanDigest, "at": time.Now().UTC(), "immutable_legacy_history_retained": true})
}

// InspectAuthorityHandoff resolves an interrupted invocation by reading its
// actual durable journal and byte state. It never repeats or rolls back work.
func InspectAuthorityHandoff(cfg Config, backupDir, expectedPlanDigest string) (map[string]any, error) {
	var err error
	cfg, err = handoffConfig(cfg)
	if err != nil {
		return nil, err
	}
	var rollback authorityHandoffRollback
	if err := handoffReadJSON(filepath.Join(backupDir, "rollback.json"), &rollback, false, authorityHandoffBudget); err != nil {
		return nil, err
	}
	if expectedPlanDigest == "" || rollback.PlanDigest != expectedPlanDigest || rollback.Plan.ConfigDir != cfg.ConfigDir {
		return nil, errors.New("inspection receipt mismatch")
	}
	before, after, pending, conflicts := 0, 0, 0, 0
	for _, file := range rollback.Plan.Files {
		path, err := handoffDestination(cfg, rollback.Plan, file)
		if err != nil {
			return nil, err
		}
		digest, err := handoffOptionalDigest(path)
		if err != nil {
			return nil, err
		}
		switch {
		case digest == file.After:
			after++
		case digest == file.Before:
			before++
		case file.Relative == filepath.Join("auth-continuity", "state.json") && digest == rollback.PendingAuthDigest:
			pending++
		default:
			conflicts++
		}
	}
	exists := func(name string) bool { _, err := os.Stat(filepath.Join(backupDir, name)); return err == nil }
	guardConflicts := 0
	for _, guard := range rollback.Plan.Guards {
		actual, err := handoffOptionalDigest(guard.Path)
		if err != nil {
			return nil, err
		}
		if actual != guard.Digest {
			guardConflicts++
		}
	}
	return map[string]any{"schema": authorityHandoffSchema, "plan_digest": expectedPlanDigest, "config_dir": cfg.ConfigDir, "files_before": before, "files_after": after, "pending_auth_marker": pending, "conflicts": conflicts, "guard_conflicts": guardConflicts, "applied_receipt": exists("applied.json"), "activation_authorized": exists("activation-authorized.json"), "restore_started": exists("restore-started.json"), "restored_receipt": exists("restored.json")}, nil
}
