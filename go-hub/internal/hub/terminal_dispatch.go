package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/megamen32/gptadmin/go-shellmcp/receiptredaction"
)

const executorCopyPath = "/internal/owner/executed-copy"
const dispatchTable = "CREATE TABLE IF NOT EXISTS task_terminal_dispatch (generation TEXT NOT NULL,id TEXT NOT NULL,expires REAL NOT NULL,voter TEXT NOT NULL,physical TEXT NOT NULL,fingerprint TEXT NOT NULL,body BLOB NOT NULL,result BLOB,PRIMARY KEY(generation,id)) WITHOUT ROWID"

type dispatchVoter struct {
	Kind        string `json:"kind,omitempty"`
	Credential  string `json:"credential,omitempty"`
	ID          string
	Physical    string
	Fingerprint string
}

// Called with the Hub mutex after the running transition has committed and
// after removal from the dispatch queue. Only the original executor gets the
// command. The other voter receives a read authorization with no executable
// input, output redaction secrets, or permission to dispatch.
func (s *Server) protectIssuedShellLocked(r *http.Request, j *shellJob) {
	g := s.receiptCopyGeneration()
	if g == "" || j.Status != "running" || j.StartedAt <= 0 || j.offlineShell {
		return
	}
	owner := s.quorumNode.Status().NodeID
	h, scoped, err := decodeOwnerHandle(j.ID)
	if err != nil || !scoped || h.Kind != "task" || h.OwnerID != owner {
		return
	}
	binding := s.receiptExecutorBindingLocked(j.Server)
	if binding == nil || binding.ID == owner {
		return
	}
	jcopy := readbackShell(j)
	if len(j.SecretValues) > 0 {
		policy := s.queueReceiptRedaction(j, r)
		if policy == nil {
			return
		}
		jcopy.CopyRedactionVersion = policy.Version
	}
	c := &terminalCopy{Generation: g, ID: j.ID, Job: jcopy, Dispatch: binding,
		Expires: j.StartedAt + float64(max(j.Timeout, int(s.cfg.DefaultTimeout/time.Second))+3600*s.hubSettingIntLocked("completed_job_retention_hours"))}
	needed, err := s.sourceCopyState(c)
	if err != nil || !needed {
		return
	}
	s.mu.Unlock()
	// Loss of this additional backup never creates a new lease or suppresses
	// delivery of the already committed command to its sole executor.
	_ = s.sendTerminalCopy(r.Context(), c)
	s.mu.Lock()
}

type forwardedExecutorResult struct {
	RedactionVersion int            `json:"receipt_redaction_version,omitempty"`
	Delegated        *dispatchVoter `json:"delegated,omitempty"`
	Authority        uint64         `json:"authority,omitempty"`
	Generation       string
	ID               string
	Server           string
	Physical         string
	Fingerprint      string
	Body             json.RawMessage `json:"body,omitempty"`
}

// Only the enrolled executor's Hub can produce this attestation, after
// authenticating its live local process credential on the result route.
func (s *Server) preserveExecutorResult(ctx context.Context, id, target string, body []byte) {
	g := s.receiptCopyGeneration()
	h, ok, e := decodeOwnerHandle(id)
	if g == "" || e != nil || !ok || h.Kind != "task" || target == "" {
		return
	}
	s.mu.Lock()
	p := s.localShellIdentityLocked(target)
	s.mu.Unlock()
	if p == nil {
		return
	}
	from := s.quorumNode.Status().NodeID
	c := forwardedExecutorResult{Generation: g, ID: id, Server: target, Physical: p.ID, Fingerprint: p.Fingerprint, Body: body, RedactionVersion: receiptredaction.PayloadVersion(body)}
	backup := s.receiptBackup(h.OwnerID)
	if backup == from {
		_ = s.acceptExecutorResult(from, c)
		return
	}
	s.transmitExecutorResult(ctx, backup, c)
}

// The backup must positively authorize this exact callback before any outcome
// bytes leave its executor. A missing grant includes secret-bearing tasks.
func (s *Server) transmitExecutorResult(ctx context.Context, backup string, c forwardedExecutorResult) bool {
	body := c.Body
	c.Body = nil
	if !s.postExecutorCopy(ctx, backup, c) {
		return false
	}
	c.Body = body
	return s.postExecutorCopy(ctx, backup, c)
}
func (s *Server) postExecutorCopy(ctx context.Context, backup string, c forwardedExecutorResult) bool {
	data, err := json.Marshal(c)
	if err != nil || len(data) > maxReceiptCopy {
		return false
	}
	resp, err := s.receiptPeerRequest(ctx, backup, executorCopyPath, http.Header{"Content-Type": []string{"application/json"}}, bytes.NewReader(data))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusNoContent
}

func (s *Server) executorCopyHTTP(w http.ResponseWriter, r *http.Request) {
	from := s.receiptPeerID(r)
	if r.Method != http.MethodPost || s.receiptCopyGeneration() == "" || from == "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var c forwardedExecutorResult
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxReceiptCopy))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := s.acceptExecutorResult(from, c); err != nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) acceptExecutorResult(from string, c forwardedExecutorResult) error {
	h, scoped, err := decodeOwnerHandle(c.ID)
	if err != nil || !scoped || h.Kind != "task" || s.receiptCopyGeneration() == "" || s.receiptCopyGeneration() != c.Generation || s.receiptBackup(h.OwnerID) != s.quorumNode.Status().NodeID {
		return errOwnerHandle
	}
	original := from
	if b := c.Delegated; b != nil {
		if from != s.quorumNode.Status().LeaderID || c.Authority == 0 || s.quorumNode.ExternalAppliedIndex() < c.Authority || (b.Kind != "member" && b.Kind != "agent") || b.Physical != c.Physical || b.Fingerprint != c.Fingerprint || b.Credential == "" {
			return errExecutorUnavailable
		}
		original = b.ID
	} else {
		s.mu.Lock()
		enrolled, e := s.executorEnrollmentLocked(c.Server)
		valid := e == nil && enrolled != nil && enrolled.voter && enrolled.bound() && enrolled.NodeID == from && enrolled.matches(c.Physical, c.Fingerprint)
		s.mu.Unlock()
		if !valid {
			return errExecutorUnavailable
		}
	}
	db, err := openTaskDatabase(s.taskDatabasePath())
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw []byte
	now := float64(s.now().UnixNano()) / 1e9
	err = tx.QueryRow("SELECT body FROM task_terminal_dispatch WHERE generation=? AND id=? AND voter=? AND physical=? AND fingerprint=? AND expires>? AND NOT EXISTS(SELECT 1 FROM task_terminal_copies WHERE generation=? AND id=?)", c.Generation, c.ID, original, c.Physical, c.Fingerprint, now, c.Generation, c.ID).Scan(&raw)
	if err != nil {
		return err
	}
	var grant struct {
		shellJob
		Auth *dispatchVoter `json:"dispatch_authority,omitempty"`
	}
	if json.Unmarshal(raw, &grant) != nil || (c.Delegated != nil && (grant.Auth == nil || *grant.Auth != *c.Delegated)) || (c.Delegated == nil && grant.Auth != nil) {
		return errExecutorUnavailable
	}
	j := grant.shellJob
	if j.ID == "" || j.Status != "running" || j.ID != c.ID || "shell:"+j.Server != c.Server {
		return errOwnerHandle
	}
	if j.CopyRedactionVersion != 0 && (j.CopyRedactionVersion != receiptredaction.Version || c.RedactionVersion != j.CopyRedactionVersion) {
		return errExecutorUnavailable
	}
	if len(c.Body) == 0 {
		return nil
	}
	if j.CopyRedactionVersion != 0 && receiptredaction.PayloadVersion(c.Body) != j.CopyRedactionVersion {
		return errExecutorUnavailable
	}
	var outcome struct {
		ID     string
		Result any
		Error  any
	}
	d := json.NewDecoder(bytes.NewReader(c.Body))
	d.UseNumber()
	if d.Decode(&outcome) != nil || d.Decode(new(any)) != io.EOF || outcome.ID != c.ID {
		return errOwnerHandle
	}
	j.DoneAt = now
	j.Status = "completed"
	j.Result = outcome.Result
	j.Error = outcome.Error
	if j.Error != nil {
		j.Status = "failed"
	}
	result, err := json.Marshal(j)
	if err != nil {
		return err
	}
	if err = s.checkCopyDisk(len(result)); err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE task_terminal_dispatch SET result=? WHERE generation=? AND id=? AND result IS NULL", result, c.Generation, c.ID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Revoke the provisional callback before changing any task in memory. The
// primary terminal row remains authoritative if completion wins a race.
func (s *Server) fenceCancelCopiesLocked(taskID string) error {
	g := s.receiptCopyGeneration()
	if g == "" {
		return nil
	}
	ids := []string{taskID}
	if group := s.relayJobs[taskID]; group != nil && group.Method == "task/group" {
		ids = append(ids, taskGroupChildIDs(group)...)
	}
	if err := s.refreshTaskRecordsLocked(ids...); err != nil {
		return err
	}
	snapshots := map[string]int64{}
	var revokes []*terminalCopy
	for _, id := range ids {
		j := s.shellJobs[id]
		if j == nil {
			snapshots[id] = -1
			continue
		}
		snapshots[id] = j.Revision
		if j.Status != "running" {
			continue
		}
		db, err := openTaskReadDatabase(s.taskDatabasePath())
		if err != nil {
			return err
		}
		var exists bool
		err = db.QueryRow("SELECT EXISTS(SELECT 1 FROM task_terminal_copy_source WHERE generation=? AND id=? AND state=0)", g, id).Scan(&exists)
		db.Close()
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		revokes = append(revokes, &terminalCopy{Generation: g, ID: id, Expires: float64(s.now().Unix()) + 720*3600, RevokeDispatch: true})
	}
	if len(revokes) == 0 {
		return nil
	}
	err := s.revokeCopiesOutsideLock(revokes)
	if err != nil {
		return err
	}
	if err = s.refreshTaskRecordsLocked(ids...); err != nil {
		return err
	}
	for id, rev := range snapshots {
		j := s.shellJobs[id]
		if (j == nil && rev != -1) || (j != nil && j.Revision != rev) {
			return errTaskConflict
		}
	}
	return nil
}

func (s *Server) revokeCopiesOutsideLock(revokes []*terminalCopy) error {
	s.mu.Unlock()
	var err error
	for _, c := range revokes {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		err = s.sendTerminalCopy(ctx, c)
		cancel()
		if err != nil {
			break
		}
	}
	s.mu.Lock()
	return err
}
