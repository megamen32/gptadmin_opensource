package hub

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"
)

const searchSnapshotLimit = 32 << 20

type searchSchemaSnapshot struct {
	Version int                           `json:"version"`
	Entries map[string]searchSchemaRecord `json:"entries"`
}
type searchSchemaRecord struct {
	CatalogDigest string           `json:"catalog_digest,omitempty"`
	Versioned     bool             `json:"versioned"`
	ChildID       string           `json:"child_id"`
	ParentID      string           `json:"parent_id"`
	Fingerprint   string           `json:"fingerprint"`
	LoadedAt      time.Time        `json:"loaded_at"`
	Tools         []map[string]any `json:"tools"`
}

func (s *Server) searchSnapshotPath(kind string) string {
	if s.cfg.ConfigDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.ConfigDir, "search_"+kind+"_catalog.json")
}
func (s *Server) loadSearchSnapshot(kind string) map[string]*childSearchSchemaEntry {
	out := map[string]*childSearchSchemaEntry{}
	path := s.searchSnapshotPath(kind)
	if path == "" {
		return out
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > searchSnapshotLimit {
		return out
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var snapshot searchSchemaSnapshot
	if json.Unmarshal(data, &snapshot) != nil || snapshot.Version != 1 {
		return out
	}
	for key, record := range snapshot.Entries {
		if record.ChildID == "" || record.Fingerprint == "" || record.LoadedAt.IsZero() {
			continue
		}
		out[key] = &childSearchSchemaEntry{catalogDigest: record.CatalogDigest, versioned: record.Versioned, childID: record.ChildID, parentID: record.ParentID, fingerprint: record.Fingerprint, loadedAt: record.LoadedAt, tools: boundedSearchChildTools(record.Tools)}
	}
	return out
}

// Called with the cache lock held. Only opaque principal hashes and bounded
// schemas are persisted, never request headers, claims, cookies or credentials.
func (s *Server) saveSearchSnapshot(kind string, entries map[string]*childSearchSchemaEntry) {
	path := s.searchSnapshotPath(kind)
	if path == "" {
		return
	}
	snapshot := searchSchemaSnapshot{Version: 1, Entries: map[string]searchSchemaRecord{}}
	for key, e := range entries {
		if e.fingerprint == "" || e.loadedAt.IsZero() || len(e.tools) == 0 {
			continue
		}
		snapshot.Entries[key] = searchSchemaRecord{CatalogDigest: e.catalogDigest, Versioned: e.versioned, ChildID: e.childID, ParentID: e.parentID, Fingerprint: e.fingerprint, LoadedAt: e.loadedAt, Tools: e.tools}
	}
	data, err := json.Marshal(snapshot)
	if err != nil || len(data) > searchSnapshotLimit {
		return
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".search-catalog-")
	if err != nil {
		return
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		log.Printf("search catalog snapshot write failed: %v", err)
	}
}
