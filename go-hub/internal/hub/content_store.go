package hub

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ContentLimits bounds the explicitly enabled, durable tool-result store.
type ContentLimits struct {
	MaxSourceBytes int64
	MaxTotalBytes  int64
	MaxSources     int
	TTL            time.Duration
}

func (l ContentLimits) normalized() ContentLimits {
	if l.MaxSourceBytes <= 0 {
		l.MaxSourceBytes = 2 << 20
	}
	if l.MaxTotalBytes <= 0 {
		l.MaxTotalBytes = 32 << 20
	}
	if l.MaxSources <= 0 {
		l.MaxSources = 128
	}
	if l.TTL <= 0 {
		l.TTL = 24 * time.Hour
	}
	if l.MaxSourceBytes > 8<<20 {
		l.MaxSourceBytes = 8 << 20
	}
	if l.MaxTotalBytes > 128<<20 {
		l.MaxTotalBytes = 128 << 20
	}
	if l.MaxSources > 1000 {
		l.MaxSources = 1000
	}
	if l.TTL > 7*24*time.Hour {
		l.TTL = 7 * 24 * time.Hour
	}
	return l
}

// ContentStore exists only after an explicit OpenContentStore call.
type ContentStore struct {
	db     *sql.DB
	limits ContentLimits
}

type ContentSource struct {
	Indexed   bool      `json:"indexed"`
	ID        string    `json:"source"`
	Name      string    `json:"name,omitempty"`
	Origin    string    `json:"origin,omitempty"`
	Format    string    `json:"format"`
	Bytes     int64     `json:"bytes"`
	Chunks    int       `json:"chunks"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	// Complete reports whether the stored bytes are the complete result. It is
	// false only for a source that was stored with the explicit consent of the
	// caller and that is known to be a part. The marker is permanent: it is
	// stored with the source and never recomputed.
	Complete bool `json:"complete"`
}

type ContentHit struct {
	Source    string  `json:"source"`
	ChunkID   int     `json:"chunk_id"`
	LineStart int     `json:"line_start"`
	LineEnd   int     `json:"line_end"`
	Snippet   string  `json:"snippet"`
	Score     float64 `json:"score"`
}

// OpenContentStore opts in to SQLite persistence and FTS5. Call Close when done.
func OpenContentStore(path string, limits ContentLimits) (*ContentStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("content store path required")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &ContentStore{db: db, limits: limits.normalized()}
	for _, q := range []string{
		`PRAGMA busy_timeout=5000`,
		`PRAGMA max_page_count=131072`,
		`CREATE TABLE IF NOT EXISTS content_sources(id TEXT PRIMARY KEY, principal TEXT NOT NULL, name TEXT NOT NULL, origin TEXT NOT NULL, format TEXT NOT NULL DEFAULT 'text', bytes INTEGER NOT NULL, chunks INTEGER NOT NULL, created INTEGER NOT NULL, expires INTEGER NOT NULL, complete INTEGER NOT NULL DEFAULT 1)`,
		`CREATE TABLE IF NOT EXISTS content_chunks(source_id TEXT NOT NULL, principal TEXT NOT NULL, chunk_id INTEGER NOT NULL, line_start INTEGER NOT NULL, line_end INTEGER NOT NULL, body TEXT NOT NULL, PRIMARY KEY(source_id,chunk_id), FOREIGN KEY(source_id) REFERENCES content_sources(id) ON DELETE CASCADE)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS content_fts USING fts5(source_id UNINDEXED, principal UNINDEXED, chunk_id UNINDEXED, body, tokenize='unicode61')`,
		`CREATE INDEX IF NOT EXISTS content_sources_principal ON content_sources(principal,expires)`,
	} {
		if _, err = db.Exec(q); err != nil {
			db.Close()
			return nil, err
		}
	}
	if _, err = db.Exec(`ALTER TABLE content_sources ADD COLUMN format TEXT NOT NULL DEFAULT 'text'`); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		db.Close()
		return nil, err
	}
	if _, err = db.Exec("ALTER TABLE content_sources ADD COLUMN indexed INTEGER NOT NULL DEFAULT 1"); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		db.Close()
		return nil, err
	}
	if _, err = db.Exec("ALTER TABLE content_sources ADD COLUMN complete INTEGER NOT NULL DEFAULT 1"); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *ContentStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *ContentStore) expire(now time.Time) error {
	_, err := s.db.Exec(`DELETE FROM content_fts WHERE source_id IN (SELECT id FROM content_sources WHERE expires<=?)`, now.Unix())
	if err != nil {
		return err
	}
	if _, err = s.db.Exec(`DELETE FROM content_chunks WHERE source_id IN (SELECT id FROM content_sources WHERE expires<=?)`, now.Unix()); err != nil {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM content_sources WHERE expires<=?`, now.Unix())
	return err
}

// Index stores text in bounded line chunks and returns metadata only.
func (s *ContentStore) Index(principal, name, origin, text string) (ContentSource, error) {
	return s.IndexFormatted(principal, name, origin, "text", text)
}

func (s *ContentStore) IndexFormatted(principal, name, origin, format, text string) (ContentSource, error) {
	return s.storeFormatted(principal, name, origin, format, text, true, true)
}

// SaveFormatted preserves a complete result without building a search index.
func (s *ContentStore) SaveFormatted(principal, name, origin, format, text string) (ContentSource, error) {
	return s.storeFormatted(principal, name, origin, format, text, false, true)
}

// SaveIncompleteFormatted preserves a result that is known to be only a part.
// The caller must have asked for it explicitly; the incompleteness marker is
// stored with the source and is permanent.
func (s *ContentStore) SaveIncompleteFormatted(principal, name, origin, format, text string) (ContentSource, error) {
	return s.storeFormatted(principal, name, origin, format, text, false, false)
}

// IndexIncompleteFormatted stores a part and builds its search index right
// away. The incompleteness marker is stored with the source and is permanent.
func (s *ContentStore) IndexIncompleteFormatted(principal, name, origin, format, text string) (ContentSource, error) {
	return s.storeFormatted(principal, name, origin, format, text, true, false)
}

func (s *ContentStore) storeFormatted(principal, name, origin, format, text string, eager, complete bool) (ContentSource, error) {
	var out ContentSource
	if s == nil || s.db == nil {
		return out, errors.New("content store disabled")
	}
	if principal == "" {
		return out, errors.New("principal required")
	}
	switch format {
	case "text", "stdout", "stderr", "content-text", "file-text", "json":
	default:
		return out, errors.New("unsupported content format")
	}
	b := []byte(text)
	if int64(len(b)) > s.limits.MaxSourceBytes {
		return out, fmt.Errorf("source exceeds %d byte limit", s.limits.MaxSourceBytes)
	}
	now := time.Now()
	if err := s.expire(now); err != nil {
		return out, err
	}
	idb := make([]byte, 16)
	if _, err := rand.Read(idb); err != nil {
		return out, err
	}
	id := hex.EncodeToString(idb)
	const chunkMax = 4000
	type chunk struct {
		id, start, end int
		body           string
	}
	chunks := make([]chunk, 0, len(text)/chunkMax+1)
	lineStart := 1
	for offset, chunkID := 0, 1; offset < len(text); chunkID++ {
		cut := min(offset+chunkMax, len(text))
		// Keep complete log/source lines together whenever they fit. A fixed
		// byte cut can place the matching token in the next chunk while losing
		// the beginning of its line. Preserve every byte, including newlines.
		if cut < len(text) {
			if last := strings.LastIndexByte(text[offset:cut], '\n'); last >= 0 {
				cut = offset + last + 1
			}
		}
		// Keep UTF-8 code points intact when a long line crosses a chunk edge.
		for cut > offset && cut < len(text) && text[cut]&0xc0 == 0x80 {
			cut--
		}
		if cut == offset {
			cut = min(offset+chunkMax, len(text))
		}
		body := text[offset:cut]
		newlines := strings.Count(body, "\n")
		lineEnd := lineStart + newlines
		if strings.HasSuffix(body, "\n") {
			lineEnd--
		}
		if lineEnd < lineStart {
			lineEnd = lineStart
		}
		chunks = append(chunks, chunk{chunkID, lineStart, lineEnd, body})
		lineStart += newlines
		offset = cut
	}
	if len(chunks) == 0 {
		chunks = append(chunks, chunk{1, 1, 1, ""})
	}
	tx, err := s.db.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var total int64
	var count int
	if err = tx.QueryRow(`SELECT COALESCE(sum(bytes),0),count(*) FROM content_sources WHERE principal=?`, principal).Scan(&total, &count); err != nil {
		return out, err
	}
	if total+int64(len(b)) > s.limits.MaxTotalBytes {
		return out, errors.New("principal content byte limit reached")
	}
	if count >= s.limits.MaxSources {
		return out, errors.New("principal content source limit reached")
	}
	// Bound all principals together as well as each principal separately.
	var globalBytes int64
	var globalCount int
	if err = tx.QueryRow(`SELECT COALESCE(sum(bytes),0),count(*) FROM content_sources`).Scan(&globalBytes, &globalCount); err != nil {
		return out, err
	}
	if globalBytes+int64(len(b)) > 128<<20 || globalCount >= 1000 {
		return out, errors.New("global content storage limit reached")
	}
	expires := now.Add(s.limits.TTL)
	if _, err = tx.Exec(`INSERT INTO content_sources(id,principal,name,origin,format,bytes,chunks,created,expires,indexed,complete) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, principal, name, origin, format, len(b), len(chunks), now.Unix(), expires.Unix(), eager, complete); err != nil {
		return out, err
	}
	for _, c := range chunks {
		if _, err = tx.Exec(`INSERT INTO content_chunks VALUES(?,?,?,?,?,?)`, id, principal, c.id, c.start, c.end, c.body); err != nil {
			return out, err
		}
		if eager {
			if _, err = tx.Exec(`INSERT INTO content_fts(source_id,principal,chunk_id,body) VALUES(?,?,?,?)`, id, principal, c.id, c.body); err != nil {
				return out, err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	return ContentSource{Indexed: eager, ID: id, Name: name, Origin: origin, Format: format, Bytes: int64(len(b)), Chunks: len(chunks), CreatedAt: now, ExpiresAt: expires, Complete: complete}, nil
}

// Search is source-scoped when sourceID is nonempty. Query terms are converted
// to quoted FTS phrases, so punctuation cannot inject FTS operators.
func (s *ContentStore) Search(principal, sourceID, query string, limit int) ([]ContentHit, error) {
	ids := []string{}
	if sourceID != "" {
		ids = []string{sourceID}
	} else {
		sources, err := s.Sources(principal)
		if err != nil {
			return nil, err
		}
		for _, source := range sources {
			ids = append(ids, source.ID)
		}
	}
	return s.SearchSources(principal, ids, query, limit)
}

// Sources returns metadata for this principal only. Callers must filter origin
// access before returning any field or passing source ids to SearchSources.
func (s *ContentStore) Sources(principal string) ([]ContentSource, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("content store disabled")
	}
	if principal == "" {
		return nil, errors.New("principal required")
	}
	if err := s.expire(time.Now()); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id,name,origin,format,bytes,chunks,created,expires,indexed,complete FROM content_sources WHERE principal=? AND expires>? ORDER BY created DESC LIMIT ?`, principal, time.Now().Unix(), s.limits.MaxSources)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ContentSource{}
	for rows.Next() {
		var x ContentSource
		var created, expires int64
		if err := rows.Scan(&x.ID, &x.Name, &x.Origin, &x.Format, &x.Bytes, &x.Chunks, &created, &expires, &x.Indexed, &x.Complete); err != nil {
			return nil, err
		}
		x.CreatedAt, x.ExpiresAt = time.Unix(created, 0), time.Unix(expires, 0)
		out = append(out, x)
	}
	return out, rows.Err()
}

// SearchSources searches only the explicitly authorized source handles.
func (s *ContentStore) SearchSources(principal string, sourceIDs []string, query string, limit int) ([]ContentHit, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("content store disabled")
	}
	if principal == "" {
		return nil, errors.New("principal required")
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if err := s.expire(time.Now()); err != nil {
		return nil, err
	}
	if len(sourceIDs) == 0 {
		return []ContentHit{}, nil
	}
	if len(sourceIDs) > s.limits.MaxSources {
		sourceIDs = sourceIDs[:s.limits.MaxSources]
	}
	terms := strings.FieldsFunc(query, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r > 127)
	})
	if len(terms) == 0 {
		return []ContentHit{}, nil
	}
	quoted := make([]string, 0, len(terms))
	for _, t := range terms {
		if t != "" {
			quoted = append(quoted, "\""+strings.ReplaceAll(t, "\"", "")+"\"")
		}
	}
	if len(quoted) == 0 {
		return []ContentHit{}, nil
	}
	if err := s.ensureContentIndexed(principal, sourceIDs); err != nil {
		return nil, err
	}
	q := strings.Join(quoted, " AND ")
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(sourceIDs)), ",")
	args := []any{principal, principal, time.Now().Unix(), q}
	for _, id := range sourceIDs {
		args = append(args, id)
	}
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT f.source_id,CAST(f.chunk_id AS INTEGER),c.line_start,c.line_end,snippet(content_fts,3,'[',']','...',12),bm25(content_fts) FROM content_fts f JOIN content_chunks c ON c.source_id=f.source_id AND c.chunk_id=CAST(f.chunk_id AS INTEGER) JOIN content_sources x ON x.id=f.source_id WHERE f.principal=? AND x.principal=? AND x.expires>? AND content_fts MATCH ? AND f.source_id IN (`+placeholders+`) ORDER BY bm25(content_fts) LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ContentHit{}
	for rows.Next() {
		var h ContentHit
		if err = rows.Scan(&h.Source, &h.ChunkID, &h.LineStart, &h.LineEnd, &h.Snippet, &h.Score); err != nil {
			return nil, err
		}
		if len(h.Snippet) > 512 {
			focus := strings.Index(h.Snippet, "[")
			start := 0
			if focus > 128 {
				start = focus - 128
			}
			for start < len(h.Snippet) && h.Snippet[start]&0xc0 == 0x80 {
				start++
			}
			h.Snippet = string(utf8Prefix([]byte(h.Snippet[start:]), 512))
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Read returns a bounded window around a chunk. Callers must revalidate the
// origin permission before invoking it; allowed=false fails closed.
func (s *ContentStore) Read(principal, sourceID string, chunkID, before, after int, allowed bool) (string, error) {
	if s == nil || s.db == nil {
		return "", errors.New("content store disabled")
	}
	if principal == "" {
		return "", errors.New("principal required")
	}
	if err := s.expire(time.Now()); err != nil {
		return "", err
	}
	if !allowed {
		return "", errors.New("source origin permission denied")
	}
	if before < 0 {
		before = 0
	}
	if after < 0 {
		after = 0
	}
	if before > 10 {
		before = 10
	}
	if after > 10 {
		after = 10
	}
	rows, err := s.db.Query(`SELECT c.body FROM content_chunks c JOIN content_sources s ON s.id=c.source_id WHERE c.principal=? AND s.principal=? AND c.source_id=? AND c.chunk_id BETWEEN ? AND ? AND s.expires>? ORDER BY c.chunk_id LIMIT 21`, principal, principal, sourceID, chunkID-before, chunkID+after, time.Now().Unix())
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var part string
		if err = rows.Scan(&part); err != nil {
			return "", err
		}
		parts = append(parts, part)
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	if len(parts) == 0 {
		return "", errors.New("content source or chunk unavailable")
	}
	return strings.Join(parts, ""), nil
}

func (s *ContentStore) Delete(principal, sourceID string) error {
	if s == nil || s.db == nil {
		return errors.New("content store disabled")
	}
	if principal == "" {
		return errors.New("principal required")
	}
	if err := s.expire(time.Now()); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM content_fts WHERE source_id IN (SELECT id FROM content_sources WHERE id=? AND principal=?)`, sourceID, principal); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM content_chunks WHERE source_id IN (SELECT id FROM content_sources WHERE id=? AND principal=?)`, sourceID, principal); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM content_sources WHERE id=? AND principal=?`, sourceID, principal)
	return err
}

func (s *ContentStore) Source(principal, sourceID string) (ContentSource, error) {
	var x ContentSource
	if s == nil || s.db == nil {
		return x, errors.New("content store disabled")
	}
	if principal == "" {
		return x, errors.New("principal required")
	}
	if err := s.expire(time.Now()); err != nil {
		return x, err
	}
	var c, e int64
	err := s.db.QueryRow(`SELECT id,name,origin,format,bytes,chunks,created,expires,indexed,complete FROM content_sources WHERE principal=? AND id=? AND expires>?`, principal, sourceID, time.Now().Unix()).Scan(&x.ID, &x.Name, &x.Origin, &x.Format, &x.Bytes, &x.Chunks, &c, &e, &x.Indexed, &x.Complete)
	if err != nil {
		return x, err
	}
	x.CreatedAt = time.Unix(c, 0)
	x.ExpiresAt = time.Unix(e, 0)
	return x, nil
}
