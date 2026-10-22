package hub

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"unicode"

	_ "modernc.org/sqlite"
)

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
	if _, err = db.Exec(`CREATE VIRTUAL TABLE tool_search USING fts5(target UNINDEXED, name, title, description, labels, settings, tokenize='unicode61 remove_diacritics 2')`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create SQLite FTS5 tool catalog: %w", err)
	}
	return &toolSearchIndex{db: db}, nil
}

func (s *Server) toolSearchForRequest(r *http.Request) (*toolSearchIndex, error) {
	key := operationPrincipal(r)
	s.toolSearchMu.Lock()
	defer s.toolSearchMu.Unlock()
	if s.toolSearches == nil {
		s.toolSearches = map[string]*toolSearchIndex{}
	}
	if index := s.toolSearches[key]; index != nil {
		return index, nil
	}
	index, err := newToolSearchIndex()
	if err != nil {
		return nil, err
	}
	s.toolSearches[key] = index
	return index, nil
}

func (i *toolSearchIndex) replaceLocked(rows []map[string]any) error {
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
	match := strings.Join(quoted, " AND ")
	rows, err := i.db.Query(`SELECT target,name,title,description,settings,bm25(tool_search,0.0,4.0,2.0,1.0,2.5,3.0) FROM tool_search WHERE tool_search MATCH ? AND (?='' OR target=?) ORDER BY bm25(tool_search,0.0,4.0,2.0,1.0,2.5,3.0) LIMIT ?`, match, targetFilter, targetFilter, limit)
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
	index, err := s.toolSearchForRequest(r)
	if err != nil {
		return map[string]any{"status": "failed", "error": "SQLite FTS5 index is unavailable"}
	}
	// Refresh at most the selected child/root target; unfiltered searches use
	// warm schema caches and never fan out over network targets.
	childRows, childCoverage := s.childSearchCatalogForRequest(r, targetFilter)
	rootRows, rootCoverage := s.rootSearchCatalogForRequest(r, targetFilter)
	childCoverage["complete"] = truthy(childCoverage["complete"]) && truthy(rootCoverage["complete"])
	for _, key := range []string{"pending", "failed"} {
		childCoverage[key] = intFromAny(childCoverage[key]) + intFromAny(rootCoverage[key])
	}
	childCoverage["missing"] = append(stringSlice(childCoverage["missing"]), stringSlice(rootCoverage["missing"])...)
	if !truthy(childCoverage["complete"]) {
		childCoverage["status"] = "incomplete"
	}
	rows := []map[string]any{}
	seen := map[string]bool{}
	definitions := map[string]map[string]any{}
	add := func(target string, defs []map[string]any, labels string, settings string) {
		for _, tool := range toolsForRequest(r, target, defs) {
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
			add(id, fileTools(), agent.Name+" files", "")
		}
		if strings.HasPrefix(id, "control:") {
			add(id, controlTools(id), agent.Name+" node MCP child lifecycle", "")
		}
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
		hit["call"] = fmt.Sprintf("await call(%q, %q, %s)", target, callName, compactJSON(callArgsExample(compactSchema)))
		encoded, _ := json.Marshal(hit)
		if usedBytes+len(encoded) > maxSearchBytes {
			stub := cloneMap(hit)
			delete(stub, "inputSchema")
			stub["schema_ref"] = map[string]any{"target": target, "tool": callName}
			stub["schema_hint"] = fmt.Sprintf("await mcp.schema(%q, %q)", target, callName)
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
