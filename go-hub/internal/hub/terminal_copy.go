package hub

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const receiptCopyPath = "/internal/owner/terminal-copy"
const maxReceiptCopy = 64 << 20

// The opt-in generation is shared by exactly one enrolled voter set. Use a
// fresh value after a downgrade, cluster reformation or rollback. Retired
// generations cannot answer reads, including reads of previously consumed IDs.
func (s *Server) receiptCopyGeneration() string {
	g := os.Getenv("GPTADMIN_RECEIPT_COPY_GENERATION")
	if !s.quorumControlOnly() || s.quorumNode == nil || s.quorumPrivateTLS == nil || !validHandlePart(g) {
		return ""
	}
	return g
}

// Every issuer has one distinct enrolled backup. Only that copy can serve a
// fallback, so revoking it is sufficient even while the third voter is down.
func (s *Server) receiptBackup(owner string) string {
	ids := make([]string, 0, len(s.quorumPrivateTLS.peers))
	for id := range s.quorumPrivateTLS.peers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	i := sort.SearchStrings(ids, owner)
	if len(ids) < 2 || i >= len(ids) || ids[i] != owner {
		return ""
	}
	return ids[(i+1)%len(ids)]
}

type terminalCopy struct {
	RevokeDispatch bool           `json:"revoke_dispatch,omitempty"`
	Dispatch       *dispatchVoter `json:"dispatch,omitempty"`
	Generation     string         `json:"generation"`
	ID             string         `json:"id"`
	Expires        float64        `json:"expires"`
	Job            *shellJob      `json:"job,omitempty"`
}

func (s *Server) terminalCopyLocked(j *shellJob) *terminalCopy {
	g := s.receiptCopyGeneration()
	h, scoped, err := decodeOwnerHandle(j.ID)
	if g == "" || err != nil || !scoped || h.Kind != "task" || h.OwnerID != s.quorumNode.Status().NodeID || !taskTerminal(j.Status) || j.DoneAt <= 0 || firstString(mapValue(j.Error), "code") == "orphaned_running_task" {
		return nil
	}
	return &terminalCopy{Generation: g, ID: j.ID, Expires: j.DoneAt + float64(3600*s.hubSettingIntLocked("completed_job_retention_hours")), Job: readbackShell(j)}
}

func readbackShell(j *shellJob) *shellJob {
	// Store only readback fields. Never transfer executable input, pending
	// work, environment, credentials, or unredacted output.
	out := &shellJob{ID: j.ID, Server: j.Server, ToolName: j.ToolName, CreatedAt: j.CreatedAt,
		StartedAt: j.StartedAt, DoneAt: j.DoneAt, Status: j.Status, TraceID: j.TraceID, TraceParent: j.TraceParent,
		Caller: j.Caller, ReceiptTarget: j.ReceiptTarget, ParentTaskID: j.ParentTaskID,
		Result: redactSecretValues(j.Result, j.SecretValues), Error: redactSecretValues(j.Error, j.SecretValues)}
	if j.FilesystemACL != nil {
		out.FilesystemACL = &filesystemACLPolicy{ProfileVersion: j.FilesystemACL.ProfileVersion}
	}
	return out
}

func (s *Server) sendTerminalCopy(ctx context.Context, copy *terminalCopy) error {
	if copy == nil {
		return nil
	}
	if !copy.RevokeDispatch {
		needed, err := s.sourceCopyState(copy)
		if err != nil || !needed {
			return err
		}
	}
	h, _, err := decodeOwnerHandle(copy.ID)
	if err != nil {
		return err
	}
	backup := s.receiptBackup(h.OwnerID)
	transport, err := s.quorumPrivateTLS.transport(backup)
	if err != nil {
		return err
	}
	defer transport.CloseIdleConnections()
	body, err := json.Marshal(copy)
	if err != nil || len(body) > maxReceiptCopy {
		return errors.New("terminal copy exceeds bounded private transfer")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.quorumPrivateTLS.peers[backup].InternalHubURL, "/")+receiptCopyPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Transport: transport, CheckRedirect: refuseNodeRedirect, Timeout: 4 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("terminal copy HTTP %d", resp.StatusCode)
	}
	if copy.Dispatch != nil || copy.RevokeDispatch {
		return nil
	}
	return s.confirmCopyState(copy)
}

const terminalCopyTable = "CREATE TABLE IF NOT EXISTS task_terminal_copies (generation TEXT NOT NULL,id TEXT NOT NULL,expires REAL NOT NULL,body BLOB,PRIMARY KEY(generation,id)) WITHOUT ROWID"

func (s *Server) storeTerminalCopy(copy terminalCopy) error {
	// A separate table in the existing private task DB has no path into
	// hydration, dispatch, control delivery, or idempotency reservations.
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
	now := float64(s.now().UnixNano()) / 1e9
	if _, err = tx.Exec("DELETE FROM task_terminal_copies WHERE expires<=?", now); err != nil {
		return err
	}
	if copy.Expires <= now {
		return tx.Commit()
	}
	if copy.RevokeDispatch {
		_, err = tx.Exec("INSERT INTO task_terminal_dispatch VALUES(?,?,?,'','','','{}',NULL) ON CONFLICT(generation,id) DO UPDATE SET voter='',physical='',fingerprint='',body='{}',result=NULL,expires=max(expires,excluded.expires)", copy.Generation, copy.ID, copy.Expires)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	if copy.Dispatch != nil {
		var auth *dispatchVoter
		if copy.Dispatch.Kind != "" {
			auth = copy.Dispatch
		}
		b, err := json.Marshal(struct {
			*shellJob
			Auth *dispatchVoter `json:"dispatch_authority,omitempty"`
		}{copy.Job, auth})
		if err != nil {
			return err
		}
		e := copy.Dispatch
		if err = s.checkCopyDisk(len(b)); err != nil {
			return err
		}
		_, err = tx.Exec("INSERT INTO task_terminal_dispatch SELECT ?,?,?,?,?,?,?,NULL WHERE NOT EXISTS(SELECT 1 FROM task_terminal_copies WHERE generation=? AND id=?) ON CONFLICT(generation,id) DO NOTHING", copy.Generation, copy.ID, copy.Expires, e.ID, e.Physical, e.Fingerprint, b, copy.Generation, copy.ID)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	if _, err = tx.Exec("DELETE FROM task_terminal_dispatch WHERE generation=? AND id=?", copy.Generation, copy.ID); err != nil {
		return err
	}
	if copy.Job == nil {
		// A tombstone also fences a delayed or duplicate pre-ACK upload.
		_, err = tx.Exec("INSERT INTO task_terminal_copies VALUES(?,?,?,NULL) ON CONFLICT(generation,id) DO UPDATE SET body=NULL,expires=max(expires,excluded.expires)", copy.Generation, copy.ID, copy.Expires)
	} else {
		body, e := json.Marshal(copy.Job)
		if e != nil {
			return e
		}
		if err = s.checkCopyDisk(len(body)); err != nil {
			return err
		}
		_, err = tx.Exec("INSERT INTO task_terminal_copies VALUES(?,?,?,?) ON CONFLICT(generation,id) DO NOTHING", copy.Generation, copy.ID, copy.Expires, body)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) terminalCopyHTTP(w http.ResponseWriter, r *http.Request) {
	g := s.receiptCopyGeneration()
	from := s.receiptPeerID(r)
	if r.Method != http.MethodPost || g == "" || from == "" || s.receiptBackup(from) != s.quorumNode.Status().NodeID {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var c terminalCopy
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxReceiptCopy))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if dec.Decode(&c) != nil || c.Generation != g || c.Expires <= 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	h, ok, err := decodeOwnerHandle(c.ID)
	if err != nil || !ok || h.Kind != "task" || h.OwnerID != from {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if c.RevokeDispatch && (c.Job != nil || c.Dispatch != nil) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if j := c.Job; j != nil {
		valid := taskTerminal(j.Status) && j.DoneAt > 0 && c.Expires >= j.DoneAt
		if e := c.Dispatch; e != nil {
			_, voter := s.quorumPrivateTLS.peers[e.ID]
			direct := voter && e.Kind == "" && e.Credential == ""
			indirect := !voter && (e.Kind == "member" || e.Kind == "agent") && e.Credential != "" && e.ID != ""
			valid = (direct || indirect) && e.ID != from && e.Physical != "" && e.Fingerprint != "" &&
				j.Status == "running" && j.StartedAt > 0 && j.DoneAt == 0 && j.Error == nil && j.Result == nil && c.Expires > j.StartedAt
		}
		if !valid || j.ID != c.ID || j.Server == "" || j.Cmd != "" || len(j.Arguments) != 0 || len(j.Env) != 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	} else if c.Dispatch != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := s.storeTerminalCopy(c); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) readTerminalCopy(r *http.Request, q ownerRead) ownerReadReply {
	reply := ownerReadReply{OwnerID: q.Hint.OwnerID, ID: q.ID, Failure: &readMissing}
	g := s.receiptCopyGeneration()
	if g == "" || r == nil || offlineShellMCP(r) || q.kind() != "task" || s.receiptBackup(q.Hint.OwnerID) != s.quorumNode.Status().NodeID {
		return reply
	}
	if endpoint := q.Hint.OwnerEndpoint; endpoint != "" && strings.TrimRight(s.quorumPrivateTLS.peers[q.Hint.OwnerID].HubURL, "/") != endpoint {
		return reply
	}
	db, err := openTaskReadDatabase(s.taskDatabasePath())
	if err != nil {
		reply.Failure = &readOffline
		return reply
	}
	defer db.Close()
	var body []byte
	now := float64(s.now().UnixNano()) / 1e9
	err = db.QueryRow("SELECT body FROM task_terminal_copies WHERE generation=? AND id=? AND expires>? AND body IS NOT NULL UNION ALL SELECT result FROM task_terminal_dispatch WHERE generation=? AND id=? AND expires>? AND result IS NOT NULL AND NOT EXISTS(SELECT 1 FROM task_terminal_copies WHERE generation=? AND id=?) LIMIT 1", g, q.ID, now, g, q.ID, now, g, q.ID).Scan(&body)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			reply.Failure = &readOffline
		}
		return reply
	}
	var j shellJob
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if d.Decode(&j) != nil || j.ID != q.ID || !taskTerminal(j.Status) {
		return reply
	}
	s.mu.Lock()
	retention := s.hubSettingIntLocked("completed_job_retention_hours")
	s.mu.Unlock()
	if j.DoneAt+float64(retention*3600) <= float64(s.now().UnixNano())/1e9 {
		return reply
	}
	if target := q.Hint.OwnerTarget; target != "" {
		s.membershipMu.Lock()
		match := s.membership != nil && s.membership.Nodes[q.Hint.OwnerID] != nil && s.membership.Nodes[q.Hint.OwnerID].Name == target
		s.membershipMu.Unlock()
		if !match && !(q.Action == "job" && (target == "shell:"+j.Server || target == j.ReceiptTarget)) {
			return reply
		}
	}
	if (j.ReceiptTarget != "" && !profileAllowsTarget(r, j.ReceiptTarget)) || s.authorizeTaskReceipt(r, j.Caller, j.FilesystemACL) != nil {
		return reply
	}
	if q.Action == "task" {
		reply.Value = s.detailedShellTask(&j)
	} else {
		if validateToolOutputDetail(q.Opts["detail"]) != nil {
			reply.Failure = &readInvalid
			return reply
		}
		reply.Value = s.formatToolOutput(shellJobResponse(&j), j.ToolName, q.Opts["detail"])
	}
	reply.Failure = nil
	return reply
}

func (s *Server) finishReceiptCopy(w http.ResponseWriter, r *http.Request, ack map[string]any, copy *terminalCopy, offline bool) {
	if copy != nil {
		if err := s.sendTerminalCopy(r.Context(), copy); err != nil {
			// Isolated execution retains its pre-existing local durability contract.
			// In a healthy quorum the executor retains its original durable outbox
			// entry until the second copy has committed. No command is re-dispatched.
			if !offline && s.quorumNode.Status().LeaderID != "" {
				receiptUnavailable(w)
				return
			}
			ack["receipt_copy"] = "owner_only"
		} else {
			ack["receipt_copy"] = "confirmed"
		}
	}
	writeJSON(w, http.StatusOK, ack)
}

// sourceCopyState fences a delayed mirror against destructive acknowledgment.
// Use SQLite's existing immediate writer transaction. State 0 can have an
// in-flight upload, 1 is confirmed, 2 needs revocation, 3 is revoked.
func (s *Server) sourceCopyState(c *terminalCopy) (bool, error) {
	db, err := openTaskDatabase(s.taskDatabasePath())
	if err != nil {
		return false, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var state int
	err = tx.QueryRow("SELECT state FROM task_terminal_copy_source WHERE generation=? AND id=?", c.Generation, c.ID).Scan(&state)
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	next := state
	if c.Job == nil {
		if !found || state == 3 {
			next = 3
		} else {
			next = 2
		}
	} else {
		if found && state != 0 {
			return false, nil
		}
		var completed float64
		err = tx.QueryRow("SELECT done FROM task_records WHERE kind='shell' AND key=?", c.ID).Scan(&completed)
		if errors.Is(err, sql.ErrNoRows) || err == nil && completed != c.Job.DoneAt {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	_, err = tx.Exec("INSERT INTO task_terminal_copy_source VALUES(?,?,?,?) ON CONFLICT(generation,id) DO UPDATE SET state=excluded.state,expires=max(expires,excluded.expires)", c.Generation, c.ID, c.Expires, next)
	if err != nil {
		return false, err
	}
	return next != 3, tx.Commit()
}

func (s *Server) confirmCopyState(c *terminalCopy) error {
	db, err := openTaskDatabase(s.taskDatabasePath())
	if err != nil {
		return err
	}
	defer db.Close()
	from, to := 0, 1
	if c.Job == nil {
		from, to = 2, 3
	}
	_, err = db.Exec("UPDATE task_terminal_copy_source SET state=? WHERE generation=? AND id=? AND state=?", to, c.Generation, c.ID, from)
	return err
}

func (s *Server) initReceiptCopies() error {
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
	for _, ddl := range []string{dispatchTable, "CREATE INDEX IF NOT EXISTS task_terminal_dispatch_expiry ON task_terminal_dispatch(expires)", copyContentDDL, "CREATE INDEX IF NOT EXISTS content_standby_expiry ON content_standby(expires)", terminalCopyTable, "CREATE INDEX IF NOT EXISTS task_terminal_copy_expiry ON task_terminal_copies(expires)",
		"CREATE TABLE IF NOT EXISTS task_terminal_copy_source (generation TEXT NOT NULL,id TEXT NOT NULL,expires REAL NOT NULL,state INTEGER NOT NULL,PRIMARY KEY(generation,id)) WITHOUT ROWID",
		"CREATE INDEX IF NOT EXISTS task_terminal_copy_source_expiry ON task_terminal_copy_source(expires)"} {
		if _, err = tx.Exec(ddl); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Catch up historical and isolated completions from the authoritative local
// ledger. The cursor only changes scan cost: durable per-ID confirmation and
// revocation decide every upload. A restarted process can safely rescan.
func (s *Server) catchUpReceiptCopies(ctx context.Context) {
	g := s.receiptCopyGeneration()
	owner := s.quorumNode.Status().NodeID
	prefix := "ot1." + base64.RawURLEncoding.EncodeToString([]byte(owner)) + "."
	cursor := prefix
	delay := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.taskRuntimeStop:
			return
		case <-time.After(delay):
		}
		delay = 10 * time.Second
		if s.quorumNode.Status().LeaderID == "" {
			continue
		}
		next, n, err := s.copyRetainedBatch(ctx, g, prefix, cursor)
		if err != nil {
			continue
		}
		if n == 0 {
			s.pruneTerminalCopies()
			cursor = prefix
			delay = time.Minute
			continue
		}
		cursor = next
		delay = 150 * time.Millisecond
	}
}

func (s *Server) copyRetainedBatch(ctx context.Context, g, prefix, cursor string) (string, int, error) {
	db, err := openTaskReadDatabase(s.taskDatabasePath())
	if err != nil {
		return cursor, 0, err
	}
	defer db.Close()
	s.mu.Lock()
	retention := s.hubSettingIntLocked("completed_job_retention_hours")
	s.mu.Unlock()
	after := float64(s.now().UnixNano())/1e9 - float64(retention*3600)
	rows, err := db.QueryContext(ctx, "SELECT key,payload FROM task_records r WHERE kind='shell' AND key>? AND key<? AND done>? AND NOT EXISTS (SELECT 1 FROM task_terminal_copy_source s WHERE s.generation=? AND s.id=r.key AND s.state!=0) ORDER BY key LIMIT 16", cursor, strings.TrimSuffix(prefix, ".")+"/", after, g)
	if err != nil {
		return cursor, 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id string
		var payload []byte
		if err = rows.Scan(&id, &payload); err != nil {
			return cursor, n, err
		}
		var j shellJob
		d := json.NewDecoder(bytes.NewReader(payload))
		d.UseNumber()
		if err = d.Decode(&j); err != nil {
			return cursor, n, err
		}
		if id != j.ID || !taskTerminal(j.Status) {
			return cursor, n, errors.New("invalid owner task row")
		}
		s.mu.Lock()
		copy := s.terminalCopyLocked(&j)
		s.mu.Unlock()
		if copy == nil {
			cursor = id
			n++
			continue
		}
		if err = s.sendTerminalCopy(ctx, copy); err != nil {
			return cursor, n, err
		}
		cursor = id
		n++
	}
	return cursor, n, rows.Err()
}

func (s *Server) pruneTerminalCopies() {
	db, err := openTaskDatabase(s.taskDatabasePath())
	if err != nil {
		return
	}
	defer db.Close()
	now := float64(s.now().UnixNano()) / 1e9
	_, _ = db.Exec("DELETE FROM task_terminal_copies WHERE expires<=?", now)
	_, _ = db.Exec("DELETE FROM task_terminal_copy_source WHERE expires<=?", now)
	_, _ = db.Exec("DELETE FROM task_terminal_dispatch WHERE expires<=?", now)
}

func (s *Server) checkCopyDisk(size int) error {
	// Preserve the already configured voter disk reserve and the SQLite WAL.
	return requireTaskQuorumSnapshotCapacity(filepath.Dir(s.taskDatabasePath()), int64(size)+4096, max(s.quorumSnapshotFreeReserve, 16<<20), 2)
}
