package hub

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// The content store separates saving from indexing.
//
// Saving a result stores it in full: the chunks are written, the FTS5 rows are
// not. The search index is built on demand, so a large result that the caller
// only wants to keep does not pay for an index it never searches.
//
// Lazy indexing has to be safe under two very different conditions:
//
//   - The source is gone (expired or deleted). The search then simply finds
//     nothing. That is not an error: the caller asked a question about data
//     that is no longer there, and the empty answer is the honest one.
//   - The source exists but belongs to another principal. That is refused. The
//     index of a foreign source must never be built on its behalf, and the
//     error must not confirm that the id exists.
//
// Everything else is unchanged: one transaction per batch, so two concurrent
// first searches cannot both build the same index.

// ensureContentIndexed materializes only explicitly authorized sources. A
// single transaction makes concurrent first searches idempotent; the ordinary
// content reader has already rechecked current origin permissions before here.
func (s *ContentStore) ensureContentIndexed(principal string, ids []string) error {
	if s == nil || s.db == nil {
		return errors.New("content store disabled")
	}
	if principal == "" {
		return errors.New("principal required")
	}
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		var owner string
		var indexed bool
		// The owner is read without the principal filter on purpose: a missing
		// row and a row owned by somebody else are different outcomes and must
		// not be reported the same way.
		err := tx.QueryRow("SELECT principal,indexed FROM content_sources WHERE id=?", id).Scan(&owner, &indexed)
		if errors.Is(err, sql.ErrNoRows) {
			// The source expired or was deleted. There is nothing to index and
			// nothing to report.
			continue
		}
		if err != nil {
			return err
		}
		if owner != principal {
			// A foreign source is never indexed and never confirmed.
			return fmt.Errorf("content source %s is not available for this principal", id)
		}
		if indexed {
			continue
		}
		if _, err = tx.Exec("INSERT INTO content_fts(source_id,principal,chunk_id,body) SELECT source_id,principal,chunk_id,body FROM content_chunks WHERE source_id=? AND principal=?", id, principal); err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE content_sources SET indexed=1 WHERE id=? AND principal=?", id, principal); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// IndexSource builds the search index of one already saved source.
//
// It is the explicit counterpart of the lazy index: a caller that wants the
// index now, instead of at the first search, asks for it here. It never reads
// the source tool again and never re-runs the operation that produced the
// source: it only materializes the rows that were stored with it.
func (s *ContentStore) IndexSource(principal, sourceID string) (ContentSource, error) {
	var out ContentSource
	if s == nil || s.db == nil {
		return out, errors.New("content store disabled")
	}
	if principal == "" {
		return out, errors.New("principal required")
	}
	if sourceID == "" {
		return out, errors.New("content source id required")
	}
	if err := s.expire(time.Now()); err != nil {
		return out, err
	}
	if err := s.ensureContentIndexed(principal, []string{sourceID}); err != nil {
		return out, err
	}
	return s.Source(principal, sourceID)
}

// SearchIndexRows reports how many FTS5 rows one source currently owns. It is
// the observable proof that an index was built, and that a repeated search did
// not build it a second time.
func (s *ContentStore) SearchIndexRows(principal, sourceID string) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("content store disabled")
	}
	if principal == "" {
		return 0, errors.New("principal required")
	}
	var rows int
	if err := s.db.QueryRow("SELECT count(*) FROM content_fts WHERE source_id=? AND principal=?", sourceID, principal).Scan(&rows); err != nil {
		return 0, err
	}
	return rows, nil
}
