package hub

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	_ "modernc.org/sqlite"
)

const (
	// The managed transient-memory envelope is shared by Code Mode guest heaps,
	// their fixed runtime overhead, and the per-principal search catalog. This is
	// an allocation budget for these paths, not a process RSS or cgroup limit.
	hubManagedMemoryBudget      int64 = 128 << 20
	codeModeRuntimeOverhead     int64 = 16 << 20
	toolSearchCacheEntries            = 4
	toolSearchIndexBudget       int64 = 8 << 20
	toolSearchCatalogInputLimit int64 = 1 << 20
	toolSearchCatalogRowLimit         = 2048
	toolSearchOperationBudget   int64 = 4 << 20
	toolSearchMaxConcurrent           = 2
	toolSearchSQLitePageSize          = 4096
	toolSearchSQLiteMaxPages          = int(toolSearchIndexBudget / toolSearchSQLitePageSize)
)

var errToolSearchBusy = errors.New("tool search is at its memory/concurrency limit")
var errToolSearchClosed = errors.New("tool search cache is closed")
var errToolSearchCatalogTooLarge = errors.New("tool search catalog exceeds its bounded input budget")

type hubMemoryBudget struct {
	mu    sync.Mutex
	used  int64
	limit int64
}

func (b *hubMemoryBudget) reserve(bytes int64) (func(), bool) {
	b.mu.Lock()
	if bytes <= 0 || b.limit <= 0 || bytes > b.limit-b.used {
		b.mu.Unlock()
		return nil, false
	}
	b.used += bytes
	b.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			b.used -= bytes
			b.mu.Unlock()
		})
	}, true
}

func (s *Server) reserveManagedMemory(bytes int64) (func(), bool) {
	s.toolSearchMu.Lock()
	defer s.toolSearchMu.Unlock()
	if s.toolSearchClosed {
		return nil, false
	}
	if s.memoryBudget == nil {
		s.memoryBudget = &hubMemoryBudget{limit: hubManagedMemoryBudget}
	}
	return s.memoryBudget.reserve(bytes)
}

func (s *Server) admitToolSearch() (func(), bool) {
	s.toolSearchMu.Lock()
	if s.toolSearchClosed {
		s.toolSearchMu.Unlock()
		return nil, false
	}
	if s.toolSearchActive >= toolSearchMaxConcurrent {
		s.toolSearchMu.Unlock()
		return nil, false
	}
	s.toolSearchActive++
	s.toolSearchMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.toolSearchMu.Lock()
			s.toolSearchActive--
			s.toolSearchMu.Unlock()
		})
	}, true
}

type toolSearchCacheEntry struct {
	index    *toolSearchIndex
	active   int
	lastUsed uint64
	release  func()
}

// toolSearchIndex is a local SQLite FTS5 catalog. It indexes built-in schemas,
// warm registered schemas, and schemas obtained by a bounded explicit-target
// cold refresh; searches never trigger a network-wide tools/list scan.
type toolSearchIndex struct {
	mu     sync.Mutex
	db     *sql.DB
	digest string
}

func newToolSearchIndex() (*toolSearchIndex, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	// The database is :memory:, so it has no file-backed mmap. The page count
	// and cache limits below bound SQLite-owned storage without a mapped-file
	// allowance that could escape the Go memory admission accounting.
	for _, pragma := range []string{
		`PRAGMA page_size=4096`,
		fmt.Sprintf(`PRAGMA max_page_count=%d`, toolSearchSQLiteMaxPages),
		`PRAGMA cache_size=256`,
	} {
		if _, err = db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("bound SQLite FTS memory: %w", err)
		}
	}
	if _, err = db.Exec(`CREATE VIRTUAL TABLE tool_search USING fts5(target UNINDEXED, name, title, description, labels, settings, tokenize='unicode61 remove_diacritics 2')`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create SQLite FTS5 tool catalog: %w", err)
	}
	return &toolSearchIndex{db: db}, nil
}

func (s *Server) acquireToolSearchForRequest(r *http.Request) (*toolSearchIndex, func(), error) {
	key := operationPrincipal(r)
	s.toolSearchMu.Lock()
	defer s.toolSearchMu.Unlock()
	if s.toolSearchClosed {
		return nil, nil, errToolSearchClosed
	}
	if s.toolSearches == nil {
		s.toolSearches = map[string]*toolSearchCacheEntry{}
	}
	entry := s.toolSearches[key]
	if entry == nil {
		for len(s.toolSearches) >= toolSearchCacheEntries {
			var oldestKey string
			var oldest *toolSearchCacheEntry
			for candidateKey, candidate := range s.toolSearches {
				if candidate.active == 0 && (oldest == nil || candidate.lastUsed < oldest.lastUsed) {
					oldestKey, oldest = candidateKey, candidate
				}
			}
			if oldest == nil {
				return nil, nil, errToolSearchBusy
			}
			if err := oldest.index.db.Close(); err != nil {
				return nil, nil, err
			}
			oldest.release()
			delete(s.toolSearches, oldestKey)
		}
		releaseMemory, ok := s.memoryBudget.reserve(toolSearchIndexBudget)
		if !ok {
			for len(s.toolSearches) > 0 {
				var oldestKey string
				var oldest *toolSearchCacheEntry
				for candidateKey, candidate := range s.toolSearches {
					if candidate.active == 0 && (oldest == nil || candidate.lastUsed < oldest.lastUsed) {
						oldestKey, oldest = candidateKey, candidate
					}
				}
				if oldest == nil {
					return nil, nil, errToolSearchBusy
				}
				if err := oldest.index.db.Close(); err != nil {
					return nil, nil, err
				}
				oldest.release()
				delete(s.toolSearches, oldestKey)
				releaseMemory, ok = s.memoryBudget.reserve(toolSearchIndexBudget)
				if ok {
					break
				}
			}
			if !ok {
				return nil, nil, errToolSearchBusy
			}
		}
		index, err := newToolSearchIndex()
		if err != nil {
			releaseMemory()
			return nil, nil, err
		}
		entry = &toolSearchCacheEntry{index: index, release: releaseMemory}
		s.toolSearches[key] = entry
	}
	s.toolSearchClock++
	entry.lastUsed = s.toolSearchClock
	entry.active++
	var once sync.Once
	release := func() {
		once.Do(func() {
			s.toolSearchMu.Lock()
			entry.active--
			s.toolSearchMu.Unlock()
		})
	}
	return entry.index, release, nil
}

func (i *toolSearchIndex) replaceLocked(rows []map[string]any) error {
	if len(rows) > toolSearchCatalogRowLimit {
		return errToolSearchCatalogTooLarge
	}
	var inputBytes int64
	for _, row := range rows {
		for _, field := range []string{"target", "name", "title", "description", "labels", "settings"} {
			inputBytes += int64(len(firstString(row, field)))
			if inputBytes > toolSearchCatalogInputLimit {
				return errToolSearchCatalogTooLarge
			}
		}
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	digest := sha256Hex(encoded)
	if digest == i.digest {
		return nil
	}
	tx, err := i.db.Begin()
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM tool_search`); err != nil {
		_ = tx.Rollback()
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO tool_search(target,name,title,description,labels,settings) VALUES(?,?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, row := range rows {
		if _, err = stmt.Exec(firstString(row, "target"), firstString(row, "name"), firstString(row, "title"), firstString(row, "description"), firstString(row, "labels"), firstString(row, "settings")); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err = tx.Commit(); err == nil {
		i.digest = digest
	}
	return err
}

func (i *toolSearchIndex) queryLocked(query, targetFilter string, limit int) ([]map[string]any, error) {
	terms := strings.FieldsFunc(query, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(terms) == 0 {
		return nil, nil
	}
	// Quote each token independently. This accepts punctuation, underscores,
	// hyphens and Unicode without exposing FTS operators or SQL syntax.
	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		if term != "" {
			quoted = append(quoted, `"`+strings.ReplaceAll(term, `"`, `""`)+`"*`)
		}
	}
	if len(quoted) == 0 {
		return nil, nil
	}
	hits, err := i.queryMatchLocked(strings.Join(quoted, " AND "), query, targetFilter, limit)
	if err != nil || len(hits) > 0 || len(quoted) == 1 {
		return hits, err
	}
	// A missing qualifier must not hide every relevant capability.
	return i.queryMatchLocked(strings.Join(quoted, " OR "), query, targetFilter, limit)
}

func (i *toolSearchIndex) queryMatchLocked(match, query, targetFilter string, limit int) ([]map[string]any, error) {
	rows, err := i.db.Query(`SELECT target,name,title,description,settings,bm25(tool_search,0.0,4.0,2.0,1.0,2.5,3.0) FROM tool_search WHERE tool_search MATCH ? AND (?='' OR target=?) ORDER BY CASE WHEN lower(name)=lower(?) THEN 0 WHEN lower(title)=lower(?) THEN 1 ELSE 2 END, bm25(tool_search,0.0,4.0,2.0,1.0,2.5,3.0), target, name LIMIT ?`, match, targetFilter, targetFilter, query, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var target, name, title, description string
		var settings string
		var score float64
		if err := rows.Scan(&target, &name, &title, &description, &settings, &score); err != nil {
			return nil, err
		}
		item := map[string]any{"target": target, "name": name, "title": title, "description": description, "score": score}
		if settings != "" {
			var metadata any
			if json.Unmarshal([]byte(settings), &metadata) == nil {
				item["settingSchema"] = metadata
			}
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (i *toolSearchIndex) search(query, target string, limit int, catalog []map[string]any) ([]map[string]any, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.replaceLocked(catalog); err != nil {
		return nil, err
	}
	return i.queryLocked(query, target, limit)
}

func (s *Server) searchToolsForRequest(r *http.Request, args map[string]any) map[string]any {
	args = cloneMap(args)
	if err := s.normalizePublicTargetArgs(args); err != nil {
		return map[string]any{"status": "failed", "error": err.Error()}
	}
	query := strings.TrimSpace(firstString(args, "query"))
	if query == "" || len(query) > 256 {
		return map[string]any{"status": "failed", "error": "query must contain 1..256 bytes"}
	}
	limit := intFromAny(args["limit"])
	if limit == 0 {
		limit = 10
	}
	if limit < 1 || limit > 20 {
		return map[string]any{"status": "failed", "error": "limit must be 1..20"}
	}
	targetFilter := strings.TrimSpace(firstString(args, "target"))
	if targetFilter != "" {
		if !profileAllowsTarget(r, targetFilter) {
			return map[string]any{"status": "failed", "error": "access profile denies this target"}
		}
		if _, status, detail := s.selectMCPRelayTarget(targetFilter); status != http.StatusOK {
			return map[string]any{"status": "failed", "error": detail}
		}
	}
	releaseAdmission, admitted := s.admitToolSearch()
	if !admitted {
		deadline := time.NewTimer(2 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for !admitted {
			select {
			case <-r.Context().Done():
				return map[string]any{"status": "failed", "error": "search cancelled", "retry_hint": true}
			case <-deadline.C:
				return map[string]any{"status": "failed", "error": "search concurrency limit reached", "retry_hint": true}
			case <-ticker.C:
				releaseAdmission, admitted = s.admitToolSearch()
			}
		}
	}
	defer releaseAdmission()
	releaseOperation, admitted := s.reserveManagedMemory(toolSearchOperationBudget)
	if !admitted {
		return map[string]any{"status": "failed", "error": "search memory budget is busy", "retry_hint": true}
	}
	defer releaseOperation()
	// Refresh at most the selected child/root target; unfiltered searches use
	// warm schema caches and never fan out over network targets.
	childRows, childCoverage := s.childSearchCatalogForRequest(r, targetFilter)
	rootRows, rootCoverage := s.rootSearchCatalogForRequest(r, targetFilter)
	childCoverage["complete"] = truthy(childCoverage["complete"]) && truthy(rootCoverage["complete"])
	for _, key := range []string{"pending", "failed"} {
		childCoverage[key] = intFromAny(childCoverage[key]) + intFromAny(rootCoverage[key])
	}
	childCoverage["missing"] = append(stringSlice(childCoverage["missing"]), stringSlice(rootCoverage["missing"])...)
	childCoverage["stale"] = append(stringSlice(childCoverage["stale"]), stringSlice(rootCoverage["stale"])...)
	childCoverage["unreachable"] = append(stringSlice(childCoverage["unreachable"]), stringSlice(rootCoverage["unreachable"])...)
	if !truthy(childCoverage["complete"]) {
		childCoverage["status"] = "incomplete"
	}
	rows := []map[string]any{}
	seen := map[string]bool{}
	definitions := map[string]map[string]any{}
	catalogExceeded := false
	add := func(target string, defs []map[string]any, labels string, settings string) {
		for _, tool := range toolsForRequest(r, target, defs) {
			if len(rows) >= toolSearchCatalogRowLimit {
				catalogExceeded = true
				return
			}
			name := firstString(tool, "name")
			key := target + "\x00" + name
			if name == "" || seen[key] {
				continue
			}
			seen[key] = true
			definitions[key] = tool
			row := map[string]any{"target": target, "name": name, "title": firstString(tool, "title"), "description": firstString(tool, "description"), "labels": labels, "settings": settings}
			if rule, ok := profileToolRule(r, target, name); ok {
				row["read_only_rule"] = rule.ReadOnly
				if len(rule.Actions) > 0 {
					row["allowed_actions"] = append([]string(nil), rule.Actions...)
				}
			}
			rows = append(rows, row)
		}
	}
	s.mu.Lock()
	agents := s.publicAgentsLocked(r)
	s.mu.Unlock()
	if targetFilter == "" || targetFilter == "hub" {
		add("hub", hubTools(), "GPTAdmin Hub settings webhook failover mesh", "")
	}
	if targetFilter == "" || targetFilter == "control:hub" {
		add("control:hub", controlTools("control:hub"), "hub administration failover webhooks mesh proxy", "")
	}
	for _, agent := range agents {
		id := agent.AgentID
		if targetFilter != "" && targetFilter != id {
			continue
		}
		if strings.HasPrefix(id, "shell:") {
			add(id, shellTools(), agent.Name+" shell command", "")
		}
		if strings.HasPrefix(id, "file:") {
			add(id, fileToolsForAgent(agent), agent.Name+" files", "")
		}
		if strings.HasPrefix(id, "control:") {
			add(id, controlTools(id), agent.Name+" node MCP child lifecycle", "")
		}
	}
	if catalogExceeded {
		return map[string]any{"status": "failed", "error": "search catalog exceeds its bounded row budget"}
	}
	settingsRow := targetFilter == "" || targetFilter == "control:hub" || targetFilter == "hub"
	extraSettingsRow := 0
	if settingsRow {
		extraSettingsRow = 1
	}
	if len(rows)+len(childRows)+len(rootRows)+extraSettingsRow > toolSearchCatalogRowLimit {
		return map[string]any{"status": "failed", "error": "search catalog exceeds its bounded row budget"}
	}
	for _, row := range childRows {
		target, name := firstString(row, "target"), firstString(row, "name")
		key := target + "\x00" + name
		if name == "" {
			continue
		}
		seen[key] = true
		definitions[key] = row
		rows = append(rows, row)
	}
	for _, row := range rootRows {
		target, name := firstString(row, "target"), firstString(row, "name")
		key := target + "\x00" + name
		if name == "" {
			continue
		}
		seen[key] = true
		definitions[key] = row
		rows = append(rows, row)
	}
	// Settings are indexed as read-only schema metadata, never current values.
	if targetFilter == "" || targetFilter == "control:hub" || targetFilter == "hub" {
		encoded, _ := json.Marshal(hubSettingsSchema())
		rows = append(rows, map[string]any{"target": "control:hub", "name": "settings", "title": "Hub settings", "description": "Typed Hub settings schema", "labels": "control configuration persisted settings restart requirements", "settings": string(encoded)})
	}
	if len(rows) > toolSearchCatalogRowLimit {
		return map[string]any{"status": "failed", "error": "search catalog exceeds its bounded row budget"}
	}
	// Keep request admission while schemas are assembled, but pin the principal's
	// SQLite database only during its transaction/query so remote refreshes do
	// not prevent safe idle-index eviction.
	index, releaseIndex, err := s.acquireToolSearchForRequest(r)
	if err != nil {
		return map[string]any{"status": "failed", "error": "SQLite FTS5 index is unavailable", "retry_hint": errors.Is(err, errToolSearchBusy)}
	}
	defer releaseIndex()
	hits, err := index.search(query, targetFilter, limit*4, rows)
	if err != nil {
		return map[string]any{"status": "failed", "error": "invalid or unavailable FTS query"}
	}
	out := make([]map[string]any, 0, limit)
	const maxSearchBytes = 32 << 10
	usedBytes := 0
	var schemaRefs []string
	omitted := 0
	for _, hit := range hits {
		target, name := firstString(hit, "target"), firstString(hit, "name")
		if !profileAllowsTarget(r, target) {
			continue
		}
		checkName := name
		if name == "settings" {
			checkName = "settings_schema"
		}
		if err := authorizeToolCall(r, target, checkName); err != nil {
			continue
		}
		// The FTS round trip persists only the indexed columns, so the action
		// level of the matching rule is re-attached here: a hit must publish
		// the same allowed_actions and read_only_rule the schema facade
		// publishes.
		if rule, ok := profileToolRule(r, target, name); ok {
			hit["read_only_rule"] = rule.ReadOnly
			if len(rule.Actions) > 0 {
				hit["allowed_actions"] = append([]string(nil), rule.Actions...)
			}
		}
		definition := definitions[target+"\x00"+name]
		var schema any
		if name == "settings" {
			schema = map[string]any{"type": "object", "description": "Hub settings schema metadata; read values with control:hub settings_get"}
		}
		if definition != nil {
			schema = definition["inputSchema"]
		}
		if schema == nil {
			continue
		}
		compactSchema := compactSearchInputSchema(schema, 0)
		hit["inputSchema"] = compactSchema
		callName := name
		if name == "settings" {
			callName = "settings_get"
		}
		hit["call"] = fmt.Sprintf("await call(%q, %q, %s)", s.shortTarget(target), callName, compactJSON(callArgsExample(compactSchema)))
		encoded, _ := json.Marshal(hit)
		if usedBytes+len(encoded) > maxSearchBytes {
			stub := cloneMap(hit)
			delete(stub, "inputSchema")
			stub["schema_ref"] = map[string]any{"target": target, "tool": callName}
			stub["schema_hint"] = fmt.Sprintf("await mcp.schema(%q, %q)", s.shortTarget(target), callName)
			stub["schema_included"] = false
			encoded, _ = json.Marshal(stub)
			if usedBytes+len(encoded) > maxSearchBytes {
				omitted++
				continue
			}
			out = append(out, stub)
			usedBytes += len(encoded)
			schemaRefs = append(schemaRefs, target+"/"+callName)
		} else {
			out = append(out, hit)
			usedBytes += len(encoded)
		}
		if len(out) == limit {
			break
		}
	}
	complete := omitted == 0 && len(schemaRefs) == 0
	childCoverage["search_schema_complete"] = complete
	childCoverage["search_result_bytes"] = usedBytes
	if len(schemaRefs) > 0 {
		childCoverage["schema_refs"] = schemaRefs
	}
	if omitted > 0 {
		childCoverage["omitted_matches"] = omitted
	}
	return map[string]any{"status": "completed", "query": query, "results": out, "coverage": childCoverage}
}

func compactSearchInputSchema(raw any, depth int) any {
	if depth > 4 {
		return map[string]any{"type": "object", "schema_truncated": true}
	}
	schema := mapValue(raw)
	out := map[string]any{}
	for _, key := range []string{"type", "enum", "minimum", "maximum", "minLength", "maxLength", "minItems", "maxItems", "additionalProperties"} {
		if value, ok := schema[key]; ok {
			out[key] = value
		}
	}
	if description := firstString(schema, "description"); description != "" {
		if len(description) > 160 {
			description = description[:160]
		}
		out["description"] = description
	}
	if required, ok := schema["required"]; ok {
		out["required"] = required
	}
	if items, ok := schema["items"]; ok {
		out["items"] = compactSearchInputSchema(items, depth+1)
	}
	if rawProps, ok := schema["properties"]; ok {
		props := mapValue(rawProps)
		compact := map[string]any{}
		keys := make([]string, 0, len(props))
		for key := range props {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) > 48 {
			keys = keys[:48]
			out["schema_truncated"] = true
		}
		for _, key := range keys {
			compact[key] = compactSearchInputSchema(props[key], depth+1)
		}
		out["properties"] = compact
	}
	if len(out) == 0 {
		return map[string]any{"type": "object", "schema_truncated": true}
	}
	return out
}

func callArgsExample(schema any) map[string]any {
	properties := mapValue(mapValue(schema)["properties"])
	out := map[string]any{}
	for key, raw := range properties {
		prop := mapValue(raw)
		typ := firstString(prop, "type")
		if list := stringSlice(prop["type"]); len(list) > 0 {
			typ = list[0]
		}
		switch typ {
		case "string":
			out[key] = "<" + key + ">"
		case "integer", "number":
			out[key] = 0
		case "boolean":
			out[key] = false
		case "array":
			out[key] = []any{}
		case "object":
			out[key] = map[string]any{}
		}
	}
	return out
}
