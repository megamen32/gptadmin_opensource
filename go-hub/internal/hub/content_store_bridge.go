package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fastschema/qjs"
)

// contentMaxSourceBytes is the largest single source the content store keeps.
// It is also the largest result the executor can store instead of returning.
const contentMaxSourceBytes = 2 << 20

var defaultExecContentLimits = ContentLimits{MaxSourceBytes: contentMaxSourceBytes, MaxTotalBytes: 32 << 20, MaxSources: 128, TTL: 24 * time.Hour}

func (s *Server) contentStoreForExec() (*ContentStore, error) {
	s.contentMu.Lock()
	defer s.contentMu.Unlock()
	if s.contentStore != nil {
		return s.contentStore, nil
	}
	path := ":memory:"
	if strings.TrimSpace(s.cfg.ConfigDir) != "" {
		if err := os.MkdirAll(s.cfg.ConfigDir, 0700); err != nil {
			return nil, err
		}
		path = filepath.Join(s.cfg.ConfigDir, "exec-content.sqlite")
		if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
			return nil, errors.New("content store path is not a regular file")
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		if err = f.Close(); err != nil {
			return nil, err
		}
		if err = os.Chmod(path, 0600); err != nil {
			return nil, err
		}
	}
	store, err := OpenContentStore(path, defaultExecContentLimits)
	if err != nil {
		return nil, err
	}
	if path != ":memory:" {
		if err := os.Chmod(path, 0600); err != nil {
			store.Close()
			return nil, err
		}
	}
	s.contentStore = store
	return store, nil
}

func (b *codeModeBridge) indexContent(text, name, origin, format string) (map[string]any, error) {
	return b.saveContent(text, name, origin, format, true, true)
}

// saveContent stores one result.
//
// eager builds the search index right away. complete records whether the
// stored bytes are the whole result; it is false only for a source that the
// caller explicitly agreed to store as a part.
func (b *codeModeBridge) saveContent(text, name, origin, format string, eager, complete bool) (map[string]any, error) {
	principal := operationPrincipal(b.request)
	if principal == "" {
		return nil, errors.New("authenticated principal required")
	}
	if len([]rune(name)) > 128 {
		name = string([]rune(name)[:128])
	}
	if len(origin) > 384 {
		return nil, errors.New("content origin is too long")
	}
	store, err := b.server.contentStoreForExec()
	if err != nil {
		return nil, err
	}
	var source ContentSource
	switch {
	case eager && complete:
		source, err = store.IndexFormatted(principal, name, origin, format, text)
	case eager:
		source, err = store.IndexIncompleteFormatted(principal, name, origin, format, text)
	case complete:
		source, err = store.SaveFormatted(principal, name, origin, format, text)
	default:
		source, err = store.SaveIncompleteFormatted(principal, name, origin, format, text)
	}
	if err != nil {
		return nil, err
	}
	return contentSourceMap(source), nil
}

func contentSourceMap(x ContentSource) map[string]any {
	origin := contentOriginDescription(x.Origin)
	return mapValue(redactPublicMetadata(map[string]any{"source": x.ID, "name": x.Name, "origin": origin, "format": x.Format, "bytes": x.Bytes, "chunks": x.Chunks, "indexed": x.Indexed, "complete": x.Complete, "created_at": x.CreatedAt.UTC().Format(time.RFC3339), "expires_at": x.ExpiresAt.UTC().Format(time.RFC3339)}))
}

// contentSourceStatusMap describes one stored source, including whether its
// search index has been built yet and how many index rows it owns. It is the
// observable proof that saving does not index and that the first search does.
func contentSourceStatusMap(s *ContentStore, principal string, x ContentSource) map[string]any {
	out := contentSourceMap(x)
	indexedRows := 0
	if s != nil {
		if rows, err := s.SearchIndexRows(principal, x.ID); err == nil {
			indexedRows = rows
		}
	}
	out["indexed_rows"] = indexedRows
	return out
}

func normalizeCodeModeFileEditor(raw any) any {
	value := mapValue(raw)
	if _, ok := value["text"].(string); ok && truthy(value["text_exact"]) {
		return raw
	}
	if firstString(value, "action") != "view" && firstString(value, "action") != "create" {
		return raw
	}
	view, ok := value["view"].(string)
	if !ok {
		return raw
	}
	if intFromAny(value["line_count"]) == 0 {
		value["text"], value["line_ids"], value["bytes"] = "", []any{}, 0
		return value
	}
	lines := strings.Split(view, "\n")
	ids := make([]any, 0, len(lines))
	textLines := make([]string, 0, len(lines))
	for _, line := range lines {
		sep := strings.IndexByte(line, '|')
		if sep > 0 && validFileLineID(line[:sep]) {
			ids = append(ids, line[:sep])
			textLines = append(textLines, line[sep+1:])
		} else {
			textLines = append(textLines, line)
		}
	}
	plain := strings.Join(textLines, "\n")
	value = cloneMap(value)
	value["text"], value["line_ids"], value["bytes"] = plain, ids, len([]byte(plain))
	value["text_exact"] = false // Legacy annotated views do not preserve original newline bytes.
	return value
}

func validFileLineID(id string) bool {
	colon := strings.IndexByte(id, ':')
	if colon < 1 || len(id)-colon != 5 {
		return false
	}
	for _, r := range id[:colon] {
		if r < '0' || r > '9' {
			return false
		}
	}
	for _, r := range id[colon+1:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func contentTextForIndex(value any, target, tool, selector string) (string, string, bool, error) {
	if selector == "json" {
		data, err := json.Marshal(value)
		return string(data), "json", false, err
	}
	m := mapValue(value)
	if selector != "" {
		if selector == "file-text" {
			selector = "text"
		}
		if v, ok := m[selector].(string); ok {
			if strings.HasPrefix(target, "file:") && tool == "file_editor" && selector == "text" {
				return v, "file-text", truthy(m["truncated"]), nil
			}
			return v, selector, truthy(m["truncated"]) || truthy(m["_spilled"]) || (selector == "stdout" && intFromAny(m["stdout_bytes"]) > len([]byte(v))), nil
		}
		return "", "", false, fmt.Errorf("selected text field %q is unavailable", selector)
	}
	if strings.HasPrefix(target, "file:") && tool == "file_editor" {
		text, ok := m["text"].(string)
		if !ok {
			return "", "", false, errors.New("file tool did not return a complete normalised text view")
		}
		return text, "file-text", truthy(m["truncated"]), nil
	}
	if strings.HasPrefix(target, "shell:") && tool == "shell_exec" {
		text, _ := m["stdout"].(string)
		partial := truthy(m["_spilled"]) || intFromAny(m["stdout_bytes"]) > len([]byte(text))
		return text, "stdout", partial, nil
	}
	if text, ok := value.(string); ok {
		return text, "text", false, nil
	}
	if text, ok := m["text"].(string); ok {
		return text, "text", truthy(m["truncated"]), nil
	}
	if rows, ok := m["content"].([]any); ok {
		parts := []string{}
		for _, row := range rows {
			block := mapValue(row)
			if firstString(block, "type") == "text" {
				if text, ok := block["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n"), "content-text", false, nil
		}
	}
	data, err := json.Marshal(value)
	return string(data), "json", false, err
}

func isChildMCPTarget(s *Server, target string, r *http.Request) bool {
	s.mu.Lock()
	child, ok := s.exposedAgentByIDLocked(target)
	s.mu.Unlock()
	return ok && isChildMCPAgent(child)
}

func (b *codeModeBridge) contentOriginAllowed(target, tool string) bool {
	if target == "" || tool == "" || authorizeToolCall(b.request, target, tool) != nil {
		return false
	}
	if err := b.server.authorizeMCPConnectionOwner(b.request, target, tool, map[string]any{}); err != nil {
		return false
	}
	if _, status, _ := b.server.selectMCPRelayTarget(target); status != 200 {
		return false
	}
	b.server.mu.Lock()
	current, exists := b.server.exposedAgentByIDLocked(target)
	b.server.mu.Unlock()
	if exists {
		if enabled, specified := current.Meta["enabled"]; specified && !truthyAny(enabled) {
			return false
		}
	}
	if !b.contentOriginAllowedInMode(target, tool) {
		return false
	}
	return !isChildMCPTarget(b.server, target, b.request) || isChildCatalogTarget(b.server, target, b.request)
}

// contentOriginAllowedInMode applies the read-only execution ceiling to one
// origin.
//
// The origin does not record which action produced the data, so a mixed tool
// is allowed when at least one of its read actions is allowed. A read-only
// script must not be able to read content that was produced by an operation
// the read-only ceiling refuses, for example a shell command.
func (b *codeModeBridge) contentOriginAllowedInMode(target, tool string) bool {
	if !b.readOnly {
		return true
	}
	if readOnlyExecToolAllowed(b.request, target, tool, map[string]any{}) {
		return true
	}
	for action := range readActionTools[tool] {
		if readOnlyExecToolAllowed(b.request, target, tool, map[string]any{"action": action}) {
			return true
		}
	}
	return false
}

// contentSourceAllowed re-checks the caller's current rights on the data of a
// stored source.
//
// A derived result carries every source it was built from. Every one of them
// must still be allowed: a result that was assembled from two reads is not
// readable again as soon as one of the two is denied.
func (b *codeModeBridge) contentSourceAllowed(meta ContentSource) bool {
	if meta.Origin == "" {
		return false
	}
	origins := contentOriginList(meta.Origin)
	if len(origins) == 0 {
		return false
	}
	for _, origin := range origins {
		if origin == contentFreeTextOrigin {
			continue
		}
		pair := strings.SplitN(origin, contentOriginSeparator, 2)
		if len(pair) != 2 || !b.contentOriginAllowed(pair[0], pair[1]) {
			return false
		}
	}
	return true
}

// content serves the content helpers inside one script.
//
// What a script may write is decided by the read-only execution ceiling, and
// the distinction is deliberate:
//
//   - A service audit is the Hub's own trail of what happened. It is not the
//     caller's data and it is not the user's stored sources.
//   - A bounded response cache and the search cache of an allowed read are
//     caches. Building them changes what the Hub has to recompute, not what
//     the user stored.
//   - Creating, changing or deleting a stored source IS a change to the user's
//     data. A read-only run never does it.
//
// So a read-only run may read a stored source, search it and build its search
// index, and it may not create, re-index as a new source, or delete one.
func (b *codeModeBridge) content(this *qjs.This) (*qjs.Value, error) {
	if err := b.entry(); err != nil {
		return nil, err
	}
	if _, err := b.remaining(); err != nil {
		return nil, err
	}
	kind := codeModeStringArg(this, 0)
	arg := codeModeStringArg(this, 1)
	opts := codeModeObjectArg(this, 2)
	principal := operationPrincipal(b.request)
	if principal == "" {
		return nil, b.fail("unauthenticated", "content store requires an authenticated caller", nil)
	}
	store, err := b.server.contentStoreForExec()
	if err != nil {
		return nil, b.fail("content_store_unavailable", "opt-in content store is unavailable", nil)
	}
	switch kind {
	case "index":
		if b.readOnly {
			return nil, b.fail("forbidden", "read-only execution cannot create persistent content sources", nil)
		}
		name := firstString(opts, "name")
		origin := contentFreeTextOrigin
		target, tool := firstString(opts, "target"), firstString(opts, "tool")
		if target != "" || tool != "" {
			if !b.contentOriginAllowed(target, tool) {
				return nil, b.fail("forbidden", "index origin target/tool is not authorized", nil)
			}
			origin = contentOriginSingle(target, tool)
		}
		// The indexed text may have been assembled from several reads. The
		// source is bound to every one of them, so a later revocation of any of
		// them also revokes the saved text.
		origin = contentOriginMerge(origin, b.contentOrigins())
		meta, e := b.indexContent(arg, name, origin, "text")
		if e != nil {
			return nil, b.fail("content_index_failed", e.Error(), nil)
		}
		return b.jsonValue(this, meta), nil
	case "indexSource":
		// Building the search index of a source the caller may already read is
		// a cache build, not a change to the user's data. It is allowed in a
		// read-only run; the origin permission of the source is still
		// re-checked, exactly as for a read or a search.
		source := arg
		if source == "" {
			source = firstString(opts, "source")
		}
		if source == "" {
			return nil, b.fail("invalid_arguments", "indexSource requires a source id", nil)
		}
		meta, e := store.Source(principal, source)
		if e != nil || !b.contentSourceAllowed(meta) {
			return nil, b.fail("content_unavailable", "content source is unavailable or its origin permission changed", nil)
		}
		// Building the index of a stored source never re-reads the source tool
		// and never re-runs the operation that produced it.
		indexed, e := store.IndexSource(principal, source)
		if e != nil {
			return nil, b.fail("content_index_failed", e.Error(), nil)
		}
		return b.jsonValue(this, contentSourceStatusMap(store, principal, indexed)), nil
	case "status":
		source := arg
		if source == "" {
			source = firstString(opts, "source")
		}
		if source == "" {
			return nil, b.fail("invalid_arguments", "status requires a source id", nil)
		}
		meta, e := store.Source(principal, source)
		if e != nil || !b.contentSourceAllowed(meta) {
			return nil, b.fail("content_unavailable", "content source is unavailable or its origin permission changed", nil)
		}
		return b.jsonValue(this, contentSourceStatusMap(store, principal, meta)), nil
	case "search":
		source := firstString(opts, "source")
		limit := intFromAny(opts["limit"])
		var allowed []string
		if source != "" {
			meta, e := store.Source(principal, source)
			if e != nil || !b.contentSourceAllowed(meta) {
				return nil, b.fail("content_unavailable", "content source is unavailable or its origin permission changed", nil)
			}
			allowed = []string{source}
		} else {
			sources, e := store.Sources(principal)
			if e != nil {
				return nil, b.fail("content_search_failed", "content search failed", nil)
			}
			for _, meta := range sources {
				if b.contentSourceAllowed(meta) {
					allowed = append(allowed, meta.ID)
				}
			}
		}
		hits, e := store.SearchSources(principal, allowed, arg, limit)
		if e != nil {
			return nil, b.fail("content_search_failed", "content search failed", nil)
		}
		return b.jsonValue(this, map[string]any{"hits": hits, "count": len(hits)}), nil
	case "read":
		source := arg
		meta, e := store.Source(principal, source)
		if e != nil {
			return nil, b.fail("content_unavailable", "content source is unavailable or expired", nil)
		}
		allowed := b.contentSourceAllowed(meta)
		chunkID := intFromAny(opts["chunk_id"])
		before, after := intFromAny(opts["before"]), intFromAny(opts["after"])
		value, e := store.Read(principal, source, chunkID, before, after, allowed)
		if e != nil {
			return nil, b.fail("content_unavailable", "content source is unavailable or its origin permission changed", nil)
		}
		return b.jsonValue(this, map[string]any{"source": source, "chunk_id": chunkID, "text": value}), nil
	case "delete":
		if b.readOnly {
			return nil, b.fail("forbidden", "read-only execution cannot delete content sources", nil)
		}
		if err := store.Delete(principal, arg); err != nil {
			return nil, b.fail("content_delete_failed", "content source could not be deleted", nil)
		}
		return b.jsonValue(this, map[string]any{"deleted": true, "source": arg}), nil
	default:
		return nil, b.fail("invalid_arguments", "unknown content helper", nil)
	}
}
