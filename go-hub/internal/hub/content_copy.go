package hub

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const contentCopyPath = "/internal/owner/content-copy"
const maxContentCopyWire = 16 << 20

type copyChunk struct {
	ID    int    `json:"id"`
	Start int    `json:"start"`
	End   int    `json:"end"`
	Body  string `json:"body"`
}

type contentCopy struct {
	Retire     []contentRetirement `json:"retire,omitempty"`
	Generation string              `json:"generation"`
	Principal  string              `json:"principal"`
	ID         string              `json:"id"`
	Expires    int64               `json:"expires"`
	Source     *ContentSource      `json:"source,omitempty"`
	Chunks     []copyChunk         `json:"chunks,omitempty"`
}

// Copy operations carry immutable source bytes and authorization provenance.
// A tombstone is irreversible within one generation and outlives every valid
// copy of this ID, including a delayed backfill from the original owner.
func (s *Server) sendContentCopy(ctx context.Context, c contentCopy) error {
	owner := s.quorumNode.Status().NodeID
	peer := s.receiptBackup(owner)
	if peer == "" || peer == owner {
		return errors.New("content standby unavailable")
	}
	budget := int64(256 << 10)
	if c.Source != nil {
		budget += 6 * c.Source.Bytes
	}
	release, admitted := s.reserveManagedMemory(budget)
	if !admitted {
		return errors.New("content transfer memory budget is busy")
	}
	defer release()
	raw, err := json.Marshal(c)
	if err != nil || len(raw) > maxContentCopyWire {
		return errors.New("content exceeds standby transfer limit")
	}
	h := http.Header{"Content-Type": []string{"application/json"}}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	resp, err := s.receiptPeerRequest(ctx, peer, contentCopyPath, h, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("content standby HTTP %d", resp.StatusCode)
	}
	return nil
}

const copyContentDDL = `CREATE TABLE IF NOT EXISTS content_standby(generation TEXT NOT NULL,id TEXT NOT NULL,principal TEXT NOT NULL,expires INTEGER NOT NULL,size INTEGER NOT NULL,body BLOB,PRIMARY KEY(generation,id)) WITHOUT ROWID`

func (s *Server) receiveContentCopy(w http.ResponseWriter, r *http.Request) {
	generation := s.receiptCopyGeneration()
	owner := s.receiptPeerID(r)
	if r.Method != http.MethodPost || generation == "" || owner == "" || s.receiptBackup(owner) != s.quorumNode.Status().NodeID {
		w.WriteHeader(403)
		return
	}
	release, ok := s.reserveManagedMemory(3 * maxContentCopyWire)
	if !ok {
		w.WriteHeader(503)
		return
	}
	defer release()
	var c contentCopy
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxContentCopyWire))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(&struct{}{}) != io.EOF || c.Generation != generation || c.Principal == "" || len(c.Principal) > 1024 {
		w.WriteHeader(400)
		return
	}
	last := time.Now().Add(defaultExecContentLimits.TTL + time.Minute).Unix()
	authorized := func(id string, expires int64) bool {
		h, scoped, e := decodeOwnerHandle(id)
		return e == nil && scoped && h.Kind == "content" && h.OwnerID == owner && expires > 0 && expires <= last
	}
	if len(c.Retire) > 0 {
		if c.ID != "" || c.Source != nil || len(c.Chunks) != 0 || len(c.Retire) > defaultExecContentLimits.MaxSources {
			w.WriteHeader(400)
			return
		}
		for _, x := range c.Retire {
			if !authorized(x.ID, x.Expires) {
				w.WriteHeader(403)
				return
			}
		}
	} else if !authorized(c.ID, c.Expires) || !validContentCopy(c, defaultExecContentLimits) {
		w.WriteHeader(403)
		return
	}
	if c.Source != nil {
		// Preserve the configured disk reserve and the SQLite write-ahead log.
		n := int(r.ContentLength)
		if n < 1 {
			n = maxContentCopyWire
		}
		if s.checkCopyDisk(n) != nil {
			w.WriteHeader(507)
			return
		}
	}
	db, err := openTaskDatabase(s.taskDatabasePath())
	if err != nil {
		w.WriteHeader(503)
		return
	}
	defer db.Close()
	if err = storeContentCopy(db, c, time.Now()); err != nil {
		w.WriteHeader(503)
		return
	}
	w.WriteHeader(204)
}

func validContentCopy(c contentCopy, limits ContentLimits) bool {
	if c.Source == nil {
		return len(c.Chunks) == 0
	}
	m := c.Source
	if m.ID != c.ID || m.Origin == "" || m.ExpiresAt.Unix() != c.Expires || m.CreatedAt.After(m.ExpiresAt) || m.ExpiresAt.Sub(m.CreatedAt) > limits.TTL+time.Second || m.Bytes < 0 || m.Bytes > limits.MaxSourceBytes || m.Chunks != len(c.Chunks) || m.Chunks < 1 || m.Chunks > int(limits.MaxSourceBytes)+1 {
		return false
	}
	var size int64
	for i, k := range c.Chunks {
		if k.ID != i+1 || k.Start < 1 || k.End < k.Start || len(k.Body) > 4000 {
			return false
		}
		size += int64(len(k.Body))
	}
	return size == m.Bytes
}

func storeContentCopy(db *sql.DB, c contentCopy, now time.Time) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM content_standby WHERE expires<=?", now.Unix()); err != nil {
		return err
	}
	retire := func(id string, expiry int64) error {
		if expiry <= now.Unix() {
			return nil
		}
		_, e := tx.Exec("INSERT INTO content_standby VALUES(?,?,?,?,0,NULL) ON CONFLICT(generation,id) DO UPDATE SET size=0,body=NULL,expires=max(expires,excluded.expires) WHERE principal=excluded.principal", c.Generation, id, c.Principal, expiry)
		return e
	}
	if len(c.Retire) > 0 {
		for _, x := range c.Retire {
			if e := retire(x.ID, x.Expires); e != nil {
				return e
			}
		}
		return tx.Commit()
	}
	if c.Expires <= now.Unix() {
		return tx.Commit()
	}
	if c.Source == nil {
		err = retire(c.ID, c.Expires)
	} else {
		var found int
		if err = tx.QueryRow("SELECT count(*) FROM content_standby WHERE generation=? AND id=?", c.Generation, c.ID).Scan(&found); err != nil {
			return err
		}
		if found > 0 {
			// Index state can only advance. It never revives a consumed source.
			if c.Source.Indexed {
				if _, err = tx.Exec("UPDATE content_standby SET body=json_set(body,'$.source.indexed',json('true')) WHERE generation=? AND id=? AND principal=? AND body IS NOT NULL AND json_extract(body,'$.source.indexed')=0", c.Generation, c.ID, c.Principal); err != nil {
					return err
				}
			}
			return tx.Commit()
		}
		var allBytes, allN, userBytes, userN int64
		err = tx.QueryRow(`SELECT coalesce(sum(size),0),count(*),coalesce(sum(CASE WHEN principal=? THEN size ELSE 0 END),0),count(CASE WHEN principal=? THEN 1 END) FROM content_standby WHERE generation=? AND body IS NOT NULL`, c.Principal, c.Principal, c.Generation).Scan(&allBytes, &allN, &userBytes, &userN)
		if err != nil {
			return err
		}
		limits := defaultExecContentLimits
		if allBytes+c.Source.Bytes > 128<<20 || allN >= 1000 || userBytes+c.Source.Bytes > limits.MaxTotalBytes || userN >= int64(limits.MaxSources) {
			return errors.New("standby content quota exceeded")
		}
		raw, e := json.Marshal(c)
		if e != nil {
			return e
		}
		_, err = tx.Exec("INSERT INTO content_standby VALUES(?,?,?,?,?,?)", c.Generation, c.ID, c.Principal, c.Expires, c.Source.Bytes, raw)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) contentStandbyRead(r *http.Request, q ownerRead) ownerReadReply {
	reply := ownerReadReply{OwnerID: q.Hint.OwnerID, ID: q.ID, Failure: &readMissing}
	if r == nil || offlineShellMCP(r) || s.receiptCopyGeneration() == "" || s.receiptBackup(q.Hint.OwnerID) != s.quorumNode.Status().NodeID {
		return reply
	}
	if q.Hint.OwnerEndpoint != "" && strings.TrimRight(s.quorumPrivateTLS.peers[q.Hint.OwnerID].HubURL, "/") != q.Hint.OwnerEndpoint {
		return reply
	}
	if q.Hint.OwnerTarget != "" {
		s.membershipMu.Lock()
		matches := s.membership != nil && s.membership.Nodes[q.Hint.OwnerID] != nil && s.membership.Nodes[q.Hint.OwnerID].Name == q.Hint.OwnerTarget
		s.membershipMu.Unlock()
		if !matches {
			return reply
		}
	}
	principal := operationPrincipal(r)
	if principal == "" || q.Action == "indexSource" {
		return reply
	}
	db, err := openTaskReadDatabase(s.taskDatabasePath())
	if err != nil {
		reply.Failure = &readOffline
		return reply
	}
	defer db.Close()
	release, admitted := s.reserveManagedMemory(3 * maxContentCopyWire)
	if !admitted {
		reply.Failure = &readOffline
		return reply
	}
	defer release()
	var body []byte
	err = db.QueryRow("SELECT body FROM content_standby WHERE generation=? AND id=? AND principal=? AND expires>? AND body IS NOT NULL", s.receiptCopyGeneration(), q.ID, principal, time.Now().Unix()).Scan(&body)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			reply.Failure = &readOffline
		}
		return reply
	}
	var c contentCopy
	b := &codeModeBridge{server: s, request: r, ctx: r.Context(), readOnly: readOnlyExecRequest(r)}
	if json.Unmarshal(body, &c) != nil || c.Source == nil || c.Principal != principal || c.ID != q.ID || !b.contentSourceAllowed(*c.Source) {
		return reply
	}
	// The normal read and FTS implementation uses the original chunks and ACL.
	// No new durable source, quota entry or executable input is created.
	store, err := OpenContentStore(":memory:", defaultExecContentLimits)
	if err != nil {
		reply.Failure = &readOffline
		return reply
	}
	defer store.Close()
	if err = projectContentCopy(store, c); err != nil {
		reply.Failure = &readOffline
		return reply
	}
	reply.Value, reply.Failure = b.readContentSource(store, *c.Source, q)
	reply.Origin = c.Source.Origin
	return reply
}

func projectContentCopy(dest *ContentStore, c contentCopy) error {
	m := c.Source
	tx, err := dest.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO content_sources(id,principal,name,origin,format,bytes,chunks,created,expires,indexed,complete) VALUES(?,?,?,?,?,?,?,?,?,?,?)", m.ID, c.Principal, m.Name, m.Origin, m.Format, m.Bytes, m.Chunks, m.CreatedAt.Unix(), m.ExpiresAt.Unix(), m.Indexed, m.Complete); err != nil {
		return err
	}
	for _, k := range c.Chunks {
		if _, err = tx.Exec("INSERT INTO content_chunks VALUES(?,?,?,?,?,?)", m.ID, c.Principal, k.ID, k.Start, k.End, k.Body); err != nil {
			return err
		}
		if m.Indexed {
			if _, err = tx.Exec("INSERT INTO content_fts(source_id,principal,chunk_id,body) VALUES(?,?,?,?)", m.ID, c.Principal, k.ID, k.Body); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

type contentCopyHooks struct {
	generation string
	owner      string
	put        func(context.Context, contentCopy) error
}

// Fencing a retained source is a three-stage operation. First persist its
// intention locally, then retire all copies in one private request with no
// SQLite transaction held, then recheck each ID in the actual delete/insert.
type contentRetirement struct {
	ID      string `json:"id"`
	Expires int64  `json:"expires"`
}

func (s *ContentStore) prepareRetirements(principal, one string, bytes int64) ([]string, error) {
	if s.standby == nil {
		return nil, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ids := []string{one}
	if one == "" {
		ids, err = s.reclaimCandidates(tx, principal, bytes, true)
		if err != nil {
			return nil, err
		}
	}
	pending, ready := "pending:"+s.standby.generation, "retired:"+s.standby.generation
	batch := make([]contentRetirement, 0, len(ids))
	for _, id := range ids {
		h, scoped, e := decodeOwnerHandle(id)
		if e != nil {
			return nil, e
		}
		if !scoped || h.Kind != "content" || h.OwnerID != s.standby.owner {
			continue
		}
		var expires int64
		var state string
		e = tx.QueryRow("SELECT expires,standby_generation FROM content_sources WHERE id=? AND principal=?", id, principal).Scan(&expires, &state)
		if errors.Is(e, sql.ErrNoRows) || state == ready {
			continue
		}
		if e != nil {
			return nil, e
		}
		if _, e = tx.Exec("UPDATE content_sources SET standby_generation=? WHERE id=? AND principal=?", pending, id, principal); e != nil {
			return nil, e
		}
		batch = append(batch, contentRetirement{id, expires})
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	if len(batch) == 0 {
		return ids, nil
	}
	if err = s.standby.put(context.Background(), contentCopy{Generation: s.standby.generation, Principal: principal, Retire: batch}); err != nil {
		return nil, err
	}
	tx, err = s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, x := range batch {
		if _, err = tx.Exec("UPDATE content_sources SET standby_generation=? WHERE id=? AND principal=? AND standby_generation=?", ready, x.ID, principal, pending); err != nil {
			return nil, err
		}
	}
	return ids, tx.Commit()
}

func (s *ContentStore) requireRetirement(tx *sql.Tx, principal, id string) error {
	if s.standby == nil {
		return nil
	}
	h, ok, err := decodeOwnerHandle(id)
	if err != nil {
		return err
	}
	if !ok || h.Kind != "content" || h.OwnerID != s.standby.owner {
		return nil
	}
	var state string
	err = tx.QueryRow("SELECT standby_generation FROM content_sources WHERE id=? AND principal=?", id, principal).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if state != "retired:"+s.standby.generation {
		return errors.New("content capacity changed before retirement confirmation")
	}
	return nil
}

func (s *ContentStore) publishContent(ctx context.Context, principal, id string) error {
	if s.standby == nil {
		return nil
	}
	// One SQLite snapshot gives consistent metadata and chunk boundaries.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var x ContentSource
	var created, expires int64
	err = tx.QueryRowContext(ctx, "SELECT id,name,origin,format,bytes,chunks,created,expires,indexed,complete FROM content_sources WHERE id=? AND principal=? AND expires>? AND standby_generation NOT IN (?,?)", id, principal, time.Now().Unix(), "pending:"+s.standby.generation, "retired:"+s.standby.generation).Scan(&x.ID, &x.Name, &x.Origin, &x.Format, &x.Bytes, &x.Chunks, &created, &expires, &x.Indexed, &x.Complete)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	x.CreatedAt = time.Unix(created, 0).UTC()
	x.ExpiresAt = time.Unix(expires, 0).UTC()
	rows, err := tx.QueryContext(ctx, "SELECT chunk_id,line_start,line_end,body FROM content_chunks WHERE source_id=? AND principal=? ORDER BY chunk_id", id, principal)
	if err != nil {
		return err
	}
	c := contentCopy{Generation: s.standby.generation, Principal: principal, ID: id, Expires: expires, Source: &x}
	for rows.Next() {
		var k copyChunk
		if err = rows.Scan(&k.ID, &k.Start, &k.End, &k.Body); err != nil {
			rows.Close()
			return err
		}
		c.Chunks = append(c.Chunks, k)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if !validContentCopy(c, s.limits) {
		return errors.New("source snapshot cannot be replicated")
	}
	if err = s.standby.put(ctx, c); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "UPDATE content_sources SET standby_generation=? WHERE id=? AND principal=? AND indexed=? AND standby_generation NOT IN (?,?)", s.standby.generation, id, principal, x.Indexed, "pending:"+s.standby.generation, "retired:"+s.standby.generation)
	return err
}

// Only retained sources with this physical owner and unconfirmed generation
// need transfer. Other IDs have no authenticated migration provenance.
func (s *Server) catchUpContentCopies(ctx context.Context) {
	delay := time.Second
	cursor := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = 5 * time.Second
		if s.quorumNode.Status().LeaderID == "" {
			continue
		}
		st, err := s.contentStoreForExec()
		if err != nil || st.standby == nil {
			continue
		}
		prefix, _ := encodeOwnerHandle("content", s.quorumNode.Status().NodeID, "probe")
		prefix = strings.TrimSuffix(prefix, "probe")
		var principal, id string
		err = st.db.QueryRowContext(ctx, "SELECT principal,id FROM content_sources WHERE standby_generation NOT IN (?,?,?) AND substr(id,1,?)=? AND expires>? AND id>? ORDER BY id LIMIT 1", st.standby.generation, "pending:"+st.standby.generation, "retired:"+st.standby.generation, len(prefix), prefix, time.Now().Unix(), cursor).Scan(&principal, &id)
		if errors.Is(err, sql.ErrNoRows) {
			cursor = ""
			if db, e := openTaskDatabase(s.taskDatabasePath()); e == nil {
				_, _ = db.ExecContext(ctx, "DELETE FROM content_standby WHERE generation<>? OR expires<=?", st.standby.generation, time.Now().Unix())
				db.Close()
			}
			delay = time.Minute
			continue
		}
		if err == nil {
			cursor = id
			if st.publishContent(ctx, principal, id) == nil {
				delay = 100 * time.Millisecond
			}
		}
	}
}

func (s *Server) readStandby(r *http.Request, q ownerRead) ownerReadReply {
	if q.kind() == "content" {
		return s.contentStandbyRead(r, q)
	}
	return s.readTerminalCopy(r, q)
}

const contentDeletePath = "/internal/owner/content-delete"

type contentDelete struct {
	ID        string `json:"id"`
	Principal string `json:"principal"`
	Index     uint64 `json:"authority_index"`
}

// A delete is separately authorized. The private read-only endpoint cannot
// become a mutating relay. An unconfirmed request is never replayed.
func (s *Server) deleteAtContentOwner(r *http.Request, owner, id string) error {
	if r == nil || offlineShellMCP(r) || readOnlyExecRequest(r) || r.Header.Get("Authorization") == "" || s.quorumNode.VerifyLeader(r.Context()) != nil {
		return errors.New("content owner unavailable")
	}
	body, err := json.Marshal(contentDelete{id, operationPrincipal(r), s.quorumNode.ExternalAppliedIndex()})
	if err != nil {
		return err
	}
	headers := http.Header{"Authorization": []string{r.Header.Get("Authorization")}, "Content-Type": []string{"application/json"}}
	releaseAuthAdmission(r)
	resp, err := s.receiptPeerRequest(r.Context(), owner, contentDeletePath, headers, bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 204 {
		return errors.New("content delete not confirmed")
	}
	return nil
}

func (s *Server) acceptContentDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || s.receiptCopyGeneration() == "" || !s.readFromQuorumLeader(r) {
		w.WriteHeader(403)
		return
	}
	var d contentDelete
	raw := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	raw.DisallowUnknownFields()
	if raw.Decode(&d) != nil || raw.Decode(&struct{}{}) != io.EOF {
		w.WriteHeader(400)
		return
	}
	h, scoped, err := decodeOwnerHandle(d.ID)
	if err != nil || !scoped || h.Kind != "content" || h.OwnerID != s.quorumNode.Status().NodeID {
		w.WriteHeader(400)
		return
	}
	r, ok := s.authenticateVoterRead(w, r, d.Index)
	if !ok {
		return
	}
	if d.Principal == "" || d.Principal != operationPrincipal(r) || requestAccessMode(r) == accessModeReadonly || s.authorizePublicFacade(r, "exec", nil) != nil {
		w.WriteHeader(403)
		return
	}
	st, err := s.contentStoreForExec()
	if err == nil {
		err = st.Delete(d.Principal, d.ID)
	}
	if err != nil {
		w.WriteHeader(503)
		return
	}
	w.WriteHeader(204)
}
