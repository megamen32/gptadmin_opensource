package hub

// A narrow, operator-reviewed recovery path for a missing, already executed
// shell receipt. It never imports queued/running state, acknowledges an outbox,
// runs a command, changes authority, or repairs the old application chain.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/megamen32/gptadmin/go-hub/internal/hub/quorum"
)

type TerminalResultPrepareOptions struct {
	Target, ExpectedLocalID, PhysicalIdentityFile           string
	RuntimePID                                              int
	RuntimeExecutableSHA256                                 string
	LocalQuorumConfig, SourceEvidence, SourceEvidenceSHA256 string
	OutboxFile, OutputDir                                   string
}

type terminalRuntimeBinding struct {
	PID               int    `json:"pid"`
	StartTicks        string `json:"start_ticks"`
	ExecutableSHA256  string `json:"executable_sha256"`
	EnvironmentSHA256 string `json:"environment_sha256"`
	CommandSHA256     string `json:"command_sha256"`
	OutboxDirectory   string `json:"outbox_directory"`
}

type terminalResultPlan struct {
	Schema                 string                        `json:"schema"`
	PreparedAt             time.Time                     `json:"prepared_at"`
	ConfigDir              string                        `json:"config_dir"`
	Target                 string                        `json:"target"`
	LocalID                string                        `json:"local_id"`
	PhysicalIdentityFile   string                        `json:"physical_identity_file"`
	PhysicalIdentitySHA256 string                        `json:"physical_identity_sha256"`
	LocalQuorumConfig      string                        `json:"local_quorum_config"`
	Runtime                terminalRuntimeBinding        `json:"runtime"`
	SourceEvidence         quorum.TerminalOriginEvidence `json:"source_evidence"`
	LocalEvidence          quorum.TerminalOriginEvidence `json:"local_evidence"`
	OutboxFile             string                        `json:"outbox_file"`
	OutboxPayloadSHA256    string                        `json:"outbox_payload_sha256"`
	ResultSHA256           string                        `json:"result_sha256"`
	ImmutableSHA256        string                        `json:"immutable_sha256"`
	OriginalCreatedAt      float64                       `json:"original_created_at"`
	OriginalStartedAt      float64                       `json:"original_started_at"`
	ReconciledAt           float64                       `json:"reconciled_at"`
	TimeSemantics          string                        `json:"time_semantics"`
	ApplicationChainClaim  string                        `json:"application_chain_claim"`
	Record                 quorumTaskRecordRow           `json:"terminal_record"`
}

func terminalCanonical(raw []byte) ([]byte, error) {
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		return nil, errors.New("unexpected trailing JSON")
	}
	return json.Marshal(value)
}

func terminalImmutable(raw []byte) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", err
	}
	for _, key := range []string{"status", "result", "error", "completed_at"} {
		delete(fields, key)
	}
	b, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	b, err = terminalCanonical(b)
	if err != nil {
		return "", err
	}
	return handoffDigest(b), nil
}

func terminalRuntime(cfg Config, pid int, expectedExecutable string) (terminalRuntimeBinding, error) {
	var result terminalRuntimeBinding
	if runtime.GOOS != "linux" || pid <= 1 || len(expectedExecutable) != 64 {
		return result, errors.New("explicit Linux runtime PID and binary SHA256 required")
	}
	base := fmt.Sprintf("/proc/%d/", pid)
	stat, err := os.ReadFile(base + "stat")
	if err != nil {
		return result, err
	}
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 {
		return result, errors.New("invalid process stat")
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) < 20 || fields[0] == "Z" {
		return result, errors.New("runtime is not a live process")
	}
	environment, err := os.ReadFile(base + "environ")
	if err != nil {
		return result, err
	}
	values := map[string]string{}
	for _, part := range bytes.Split(environment, []byte{0}) {
		if at := bytes.IndexByte(part, '='); at >= 0 {
			values[string(part[:at])] = string(part[at+1:])
		}
	}
	if values["GPTADMIN_CONFIG_DIR"] != cfg.ConfigDir || values["GPTADMIN_QUORUM_CONFIG_FILE"] != "" || cfg.QuorumConfigFile != "" {
		return result, errors.New("runtime is not the exact ordinary-mode configuration")
	}
	outbox := values["SHELL_OUTBOX_DIR"]
	resolved, e := filepath.EvalSymlinks(outbox)
	if e != nil || !filepath.IsAbs(outbox) || filepath.Clean(outbox) != outbox || resolved != outbox {
		return result, errors.New("runtime has no explicit canonical outbox directory")
	}
	executable, err := os.ReadFile(base + "exe")
	if err != nil {
		return result, err
	}
	sum := handoffDigest(executable)
	if sum != expectedExecutable {
		return result, errors.New("runtime executable changed")
	}
	command, err := os.ReadFile(base + "cmdline")
	if err != nil {
		return result, err
	}
	result = terminalRuntimeBinding{PID: pid, StartTicks: fields[19], ExecutableSHA256: sum, EnvironmentSHA256: handoffDigest(environment), CommandSHA256: handoffDigest(command), OutboxDirectory: outbox}
	return result, nil
}

func terminalReadEvidence(path, digest string) (quorum.TerminalOriginEvidence, error) {
	var evidence quorum.TerminalOriginEvidence
	actual, err := handoffFileDigest(path)
	if err != nil {
		return evidence, err
	}
	if len(digest) != 64 || actual != digest {
		return evidence, errors.New("source evidence digest differs")
	}
	err = handoffReadJSON(path, &evidence, false, 32<<20)
	return evidence, err
}

func terminalReadOutbox(path string) (map[string]json.RawMessage, string, error) {
	if !filepath.IsAbs(path) || filepath.Base(filepath.Dir(path)) != "outbox" {
		return nil, "", errors.New("explicit canonical outbox path required")
	}
	var entry struct {
		JobID   string          `json:"job_id"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := handoffReadJSON(path, &entry, false, 16<<20); err != nil {
		return nil, "", err
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return nil, "", err
	}
	var id string
	if err := json.Unmarshal(payload["id"], &id); err != nil || id == "" || entry.JobID != id || filepath.Base(path) != id+".json" {
		return nil, "", errors.New("outbox identity differs")
	}
	for key := range payload {
		if key != "id" && key != "result" && key != "error" && key != "trace_id" && key != "traceparent" {
			return nil, "", errors.New("unsupported outbox payload field")
		}
	}
	b, err := terminalCanonical(entry.Payload)
	if err != nil {
		return nil, "", err
	}
	return payload, handoffDigest(b), nil
}

func terminalRecordForResult(origin quorum.TerminalOriginEvidence, target string, payload map[string]json.RawMessage, at float64) (quorumTaskRecordRow, string, string, error) {
	var zero quorumTaskRecordRow
	var row quorumTaskRecordRow
	if err := json.Unmarshal(origin.Record, &row); err != nil {
		return zero, "", "", err
	}
	var task persistedShellTask
	if err := json.Unmarshal(row.Payload, &task); err != nil {
		return zero, "", "", err
	}
	if row.Kind != "shell" || row.Key != origin.JobID || row.Revision != int64(origin.JobRevision) || task.ID != row.Key || task.Server != strings.TrimPrefix(target, "shell:") || task.ReceiptTarget != target || task.Status != "running" || task.CreatedAt <= 0 || task.StartedAt < task.CreatedAt || task.DoneAt != 0 || row.Done != 0 || task.OwnerID == "" || task.Caller == nil || task.Caller.ClientID == "" || task.RequestKey != "" || len(task.SealedPending) != 0 || task.ParentTaskID != "" {
		return zero, "", "", errors.New("origin is not the supported exact independent running receipt")
	}
	var id, trace, parent string
	_ = json.Unmarshal(payload["id"], &id)
	_ = json.Unmarshal(payload["trace_id"], &trace)
	_ = json.Unmarshal(payload["traceparent"], &parent)
	if id != task.ID || trace != task.TraceID || parent != task.TraceParent {
		return zero, "", "", errors.New("outbox trace identity differs from admitted receipt")
	}
	if len(payload["result"]) == 0 {
		return zero, "", "", errors.New("outbox has no retained result")
	}
	// The installed HTTP receiver uses json.Unmarshal into interface{}. Refuse
	// numbers that would change through that exact receiver representation.
	canonicalResult, err := terminalCanonical(payload["result"])
	if err != nil {
		return zero, "", "", err
	}
	var receiverResult any
	if err = json.Unmarshal(payload["result"], &receiverResult); err != nil {
		return zero, "", "", err
	}
	receiverBytes, err := json.Marshal(receiverResult)
	if err != nil {
		return zero, "", "", err
	}
	if !bytes.Equal(canonicalResult, receiverBytes) {
		return zero, "", "", errors.New("result would change through installed HTTP JSON representation")
	}
	immutable, err := terminalImmutable(row.Payload)
	if err != nil {
		return zero, "", "", err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(row.Payload, &fields); err != nil {
		return zero, "", "", err
	}
	for _, key := range []string{"sealed_pending", "arguments", "cmd", "env", "cwd", "request_key", "parent_task_id"} {
		if _, present := fields[key]; present {
			return zero, "", "", errors.New("origin contains unsupported executable or grouped-task state")
		}
	}
	if at < task.StartedAt {
		return zero, "", "", errors.New("reconciliation clock precedes admitted start")
	}
	status := "completed"
	if e := bytes.TrimSpace(payload["error"]); len(e) > 0 && !bytes.Equal(e, []byte("null")) {
		status = "failed"
		fields["error"] = append(json.RawMessage(nil), e...)
	} else {
		delete(fields, "error")
	}
	fields["status"], _ = json.Marshal(status)
	fields["completed_at"], _ = json.Marshal(at)
	fields["result"] = append(json.RawMessage(nil), payload["result"]...)
	row.Payload, err = json.Marshal(fields)
	if err != nil {
		return zero, "", "", err
	}
	row.Revision++
	row.Updated = at
	row.Done = at
	if err = handoffValidateLegacy([]quorumTaskRecordRow{row}, nil); err != nil {
		return zero, "", "", err
	}
	return row, immutable, handoffDigest(canonicalResult), nil
}

func terminalReadDB(cfg Config) (*sql.DB, error) {
	path := filepath.Join(cfg.ConfigDir, taskDatabaseFilename)
	if err := regularAuthPath(path, false); err != nil {
		return nil, err
	}
	uri, err := sqliteFileURI(path)
	if err != nil {
		return nil, err
	}
	uri.RawQuery = "mode=ro"
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("PRAGMA query_only=1"); err == nil {
		_, err = db.Exec("PRAGMA busy_timeout=5000")
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

type terminalQuerier interface{ QueryRow(string, ...any) *sql.Row }

func terminalAbsent(q terminalQuerier, id string) error {
	for _, query := range []string{"SELECT count(*) FROM task_records WHERE key=?", "SELECT count(*) FROM task_controls WHERE task_id=?", "SELECT count(*) FROM task_requests WHERE job_id=?"} {
		var count int
		if err := q.QueryRow(query, id).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("exact task already has a record, control or request")
		}
	}
	var exists int
	if err := q.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='task_quorum_revisions'").Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		var count int
		if err := q.QueryRow("SELECT count(*) FROM task_quorum_revisions WHERE row_key=?", id).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("target retains a quorum revision or tombstone for this ID")
		}
	}
	return nil
}

func PrepareTerminalResult(cfg Config, options TerminalResultPrepareOptions) (map[string]any, error) {
	var err error
	cfg, err = handoffConfig(cfg)
	if err != nil {
		return nil, err
	}
	if options.PhysicalIdentityFile == "" || !strings.HasPrefix(options.Target, "shell:") {
		return nil, errors.New("explicit physical executor binding required")
	}
	if _, _, err = handoffLocalExecutor(cfg, options.Target, options.ExpectedLocalID, options.PhysicalIdentityFile); err != nil {
		return nil, err
	}
	binding, err := terminalRuntime(cfg, options.RuntimePID, options.RuntimeExecutableSHA256)
	if err != nil {
		return nil, err
	}
	source, err := terminalReadEvidence(options.SourceEvidence, options.SourceEvidenceSHA256)
	if err != nil {
		return nil, err
	}
	local, err := quorum.InspectTerminalOrigin(options.LocalQuorumConfig, source.JobID)
	if err != nil {
		return nil, err
	}
	if err = quorum.VerifyTerminalOriginPair(source, local); err != nil {
		return nil, err
	}
	identity, err := handoffLoadNodeIdentity(cfg, true)
	if err != nil || identity.NodeID != local.NodeID {
		return nil, errors.New("local voter does not match the retained node identity")
	}
	if options.OutboxFile != filepath.Join(binding.OutboxDirectory, source.JobID+".json") {
		return nil, errors.New("outbox path is not the exact live executor entry")
	}
	payload, payloadHash, err := terminalReadOutbox(options.OutboxFile)
	if err != nil {
		return nil, err
	}
	now := nowFloat()
	row, immutable, resultHash, err := terminalRecordForResult(local, options.Target, payload, now)
	if err != nil {
		return nil, err
	}
	db, err := terminalReadDB(cfg)
	if err != nil {
		return nil, err
	}
	err = terminalAbsent(db, row.Key)
	db.Close()
	if err != nil {
		return nil, err
	}
	physicalHash, err := handoffFileDigest(options.PhysicalIdentityFile)
	if err != nil {
		return nil, err
	}
	var original quorumTaskRecordRow
	_ = json.Unmarshal(local.Record, &original)
	var task persistedShellTask
	_ = json.Unmarshal(original.Payload, &task)
	plan := terminalResultPlan{Schema: "gptadmin.terminal-result-plan/v1", PreparedAt: time.Now().UTC(), ConfigDir: cfg.ConfigDir, Target: options.Target, LocalID: options.ExpectedLocalID, PhysicalIdentityFile: options.PhysicalIdentityFile, PhysicalIdentitySHA256: physicalHash, LocalQuorumConfig: options.LocalQuorumConfig, Runtime: binding, SourceEvidence: source, LocalEvidence: local, OutboxFile: options.OutboxFile, OutboxPayloadSHA256: payloadHash, ResultSHA256: resultHash, ImmutableSHA256: immutable, OriginalCreatedAt: task.CreatedAt, OriginalStartedAt: task.StartedAt, ReconciledAt: now, TimeSemantics: "created/started preserved from authenticated admission; completed_at is reconciliation time until the normal result callback records its own receipt time", ApplicationChainClaim: "not verified or modified; historical mismatch remains separate", Record: row}
	if !filepath.IsAbs(options.OutputDir) {
		return nil, errors.New("absolute new private plan directory required")
	}
	if err = os.Mkdir(options.OutputDir, 0700); err != nil {
		return nil, err
	}
	if err = handoffSyncParent(options.OutputDir); err != nil {
		return nil, err
	}
	path := filepath.Join(options.OutputDir, "plan.json")
	if err = handoffWritePrivate(path, plan); err != nil {
		return nil, err
	}
	hash, err := handoffFileDigest(path)
	if err != nil {
		return nil, err
	}
	return map[string]any{"phase": "prepared", "plan_file": path, "plan_sha256": hash, "job_id": row.Key, "target": options.Target, "result_sha256": resultHash, "immutable_sha256": immutable, "commit_boundary": local.BoundaryIndex, "commit_term": local.BoundaryTerm, "application_chain": plan.ApplicationChainClaim, "time_semantics": plan.TimeSemantics, "original_created_at": task.CreatedAt, "original_started_at": task.StartedAt, "reconciled_at": now}, nil
}

func terminalReadPlan(cfg Config, directory, digest string) (terminalResultPlan, error) {
	var plan terminalResultPlan
	path := filepath.Join(directory, "plan.json")
	hash, err := handoffFileDigest(path)
	if err != nil {
		return plan, err
	}
	if len(digest) != 64 || hash != digest {
		return plan, errors.New("reviewed plan digest differs")
	}
	if err = handoffReadJSON(path, &plan, false, 64<<20); err != nil {
		return plan, err
	}
	if plan.Schema != "gptadmin.terminal-result-plan/v1" || plan.ConfigDir != cfg.ConfigDir {
		return plan, errors.New("plan configuration differs")
	}
	return plan, nil
}

func ApplyTerminalResult(cfg Config, directory, digest string) (map[string]any, error) {
	var err error
	cfg, err = handoffConfig(cfg)
	if err != nil {
		return nil, err
	}
	plan, err := terminalReadPlan(cfg, directory, digest)
	if err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(directory, "apply.lock"))
	held, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !held {
		return nil, errors.New("another exact repair is active")
	}
	defer lock.Close()
	if _, err = os.Lstat(filepath.Join(directory, "intent.json")); err == nil {
		return nil, errors.New("repair was already dispatched; inspect without repeating")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err = quorum.VerifyTerminalOriginPair(plan.SourceEvidence, plan.LocalEvidence); err != nil {
		return nil, err
	}
	local, err := quorum.InspectTerminalOrigin(plan.LocalQuorumConfig, plan.Record.Key)
	if err != nil {
		return nil, err
	}
	if err = quorum.VerifyTerminalOriginPair(plan.SourceEvidence, local); err != nil {
		return nil, err
	}
	identity, err := handoffLoadNodeIdentity(cfg, true)
	if err != nil || identity.NodeID != local.NodeID {
		return nil, errors.New("local voter identity changed")
	}
	physicalHash, err := handoffFileDigest(plan.PhysicalIdentityFile)
	if err != nil || physicalHash != plan.PhysicalIdentitySHA256 {
		return nil, errors.New("physical identity changed")
	}
	if _, _, err = handoffLocalExecutor(cfg, plan.Target, plan.LocalID, plan.PhysicalIdentityFile); err != nil {
		return nil, err
	}
	binding, err := terminalRuntime(cfg, plan.Runtime.PID, plan.Runtime.ExecutableSHA256)
	if err != nil || binding != plan.Runtime {
		return nil, errors.New("runtime binding changed")
	}
	if plan.OutboxFile != filepath.Join(binding.OutboxDirectory, plan.Record.Key+".json") {
		return nil, errors.New("outbox path is not the exact live executor entry")
	}
	payload, payloadHash, err := terminalReadOutbox(plan.OutboxFile)
	if err != nil {
		return nil, err
	}
	if payloadHash != plan.OutboxPayloadSHA256 {
		return nil, errors.New("retained result changed")
	}
	row, immutable, resultHash, err := terminalRecordForResult(local, plan.Target, payload, plan.ReconciledAt)
	if err != nil {
		return nil, err
	}
	if !equalHandoffRecord(row, plan.Record) || immutable != plan.ImmutableSHA256 || resultHash != plan.ResultSHA256 {
		return nil, errors.New("terminal plan no longer matches original record and result")
	}
	if err = handoffWritePrivate(filepath.Join(directory, "intent.json"), map[string]any{"plan_sha256": digest, "job_id": row.Key, "runtime": binding, "dispatched_at": time.Now().UTC()}); err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg}
	err = s.withTaskDatabase(func(tx *taskTransaction) error {
		current, e := terminalRuntime(cfg, plan.Runtime.PID, plan.Runtime.ExecutableSHA256)
		if e != nil || current != plan.Runtime {
			return errors.New("runtime changed before canonical import")
		}
		if e = terminalAbsent(tx.tx, row.Key); e != nil {
			return e
		}
		if e = handoffValidateLegacy([]quorumTaskRecordRow{row}, nil); e != nil {
			return e
		}
		// The canonical transaction owns this append. No existing row, revision,
		// control, request, authority state or application chain is overwritten.
		_, e = tx.tx.Exec(`INSERT INTO task_records(kind,key,updated,done,payload,revision) VALUES(?,?,?,?,?,?)`, row.Kind, row.Key, row.Updated, row.Done, row.Payload, row.Revision)
		return e
	})
	if err != nil {
		_ = handoffWritePrivate(filepath.Join(directory, "failed.json"), map[string]any{"plan_sha256": digest, "phase": "transaction_failed_inspect_before_any_new_attempt", "error": err.Error(), "at": time.Now().UTC()})
		return nil, err
	}
	receipt, err := InspectTerminalResult(cfg, directory, digest)
	if err != nil {
		return nil, err
	}
	receipt["phase"] = "terminal_imported_awaiting_normal_callback"
	receipt["plan_sha256"] = digest
	if err = handoffWritePrivate(filepath.Join(directory, "applied.json"), receipt); err != nil {
		return nil, err
	}
	return receipt, nil
}

func InspectTerminalResult(cfg Config, directory, digest string) (map[string]any, error) {
	var err error
	cfg, err = handoffConfig(cfg)
	if err != nil {
		return nil, err
	}
	plan, err := terminalReadPlan(cfg, directory, digest)
	if err != nil {
		return nil, err
	}
	db, err := terminalReadDB(cfg)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var row quorumTaskRecordRow
	err = db.QueryRow("SELECT kind,key,updated,done,payload,revision FROM task_records WHERE kind='shell' AND key=?", plan.Record.Key).Scan(&row.Kind, &row.Key, &row.Updated, &row.Done, &row.Payload, &row.Revision)
	result := map[string]any{"job_id": plan.Record.Key, "target": plan.Target, "plan_sha256": digest, "application_chain": plan.ApplicationChainClaim, "manual_ack": false, "execution_replayed": false}
	_, outboxErr := os.Lstat(plan.OutboxFile)
	result["outbox_present"] = outboxErr == nil
	if outboxErr != nil && !errors.Is(outboxErr, os.ErrNotExist) {
		return nil, outboxErr
	}
	if errors.Is(err, sql.ErrNoRows) {
		result["phase"] = "record_absent"
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	var task persistedShellTask
	if err = json.Unmarshal(row.Payload, &task); err != nil {
		return nil, err
	}
	immutable, err := terminalImmutable(row.Payload)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(row.Payload, &fields)
	canonical, err := terminalCanonical(fields["result"])
	if err != nil {
		return nil, err
	}
	resultHash := handoffDigest(canonical)
	var planned persistedShellTask
	_ = json.Unmarshal(plan.Record.Payload, &planned)
	var plannedFields map[string]json.RawMessage
	_ = json.Unmarshal(plan.Record.Payload, &plannedFields)
	errorRaw := fields["error"]
	if len(errorRaw) == 0 {
		errorRaw = json.RawMessage("null")
	}
	errorBytes, err := terminalCanonical(errorRaw)
	if err != nil {
		return nil, err
	}
	plannedError := plannedFields["error"]
	if len(plannedError) == 0 {
		plannedError = json.RawMessage("null")
	}
	plannedErrorBytes, err := terminalCanonical(plannedError)
	if err != nil {
		return nil, err
	}
	exact := immutable == plan.ImmutableSHA256 && resultHash == plan.ResultSHA256 && bytes.Equal(errorBytes, plannedErrorBytes) && task.Status == planned.Status && task.ID == plan.Record.Key && task.Server == planned.Server && row.Revision >= plan.Record.Revision && row.Done >= plan.ReconciledAt && task.DoneAt == row.Done
	result["phase"] = "retained_record_readback"
	result["record_matches_plan"] = exact
	result["status"] = task.Status
	result["revision"] = row.Revision
	result["result_sha256"] = resultHash
	result["error_sha256"] = handoffDigest(errorBytes)
	result["immutable_sha256"] = immutable
	result["completed_at"] = task.DoneAt
	result["completed_at_semantics"] = "local reconciliation/callback receipt time, not a reconstructed historical completion timestamp"
	binding, e := terminalRuntime(cfg, plan.Runtime.PID, plan.Runtime.ExecutableSHA256)
	result["runtime_binding_current"] = e == nil && binding == plan.Runtime
	if exact && errors.Is(outboxErr, os.ErrNotExist) {
		result["phase"] = "normal_callback_acknowledged_retained_result"
	}
	return result, nil
}

// ExportTerminalOrigin writes a private, immutable receipt. Its output contains
// original caller metadata and must be transported as a private file, not chat.
func ExportTerminalOrigin(configFile, jobID, output string) (map[string]any, error) {
	evidence, err := quorum.InspectTerminalOrigin(configFile, jobID)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(output) {
		return nil, errors.New("absolute private output required")
	}
	if err = handoffWritePrivate(output, evidence); err != nil {
		return nil, err
	}
	hash, err := handoffFileDigest(output)
	if err != nil {
		return nil, err
	}
	return map[string]any{"evidence_file": output, "evidence_sha256": hash, "node_id": evidence.NodeID, "cluster_id": evidence.ClusterID, "job_id": jobID, "job_revision": evidence.JobRevision, "boundary_index": evidence.BoundaryIndex, "boundary_term": evidence.BoundaryTerm, "boundary_prefix_sha256": evidence.BoundaryPrefixSHA256, "full_prefix_sha256": evidence.FullPrefixSHA256, "application_chain_verified": false}, nil
}
