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

var defaultExecContentLimits = ContentLimits{MaxSourceBytes: 2 << 20, MaxTotalBytes: 32 << 20, MaxSources: 128, TTL: 24 * time.Hour}

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
	source, err := store.IndexFormatted(principal, name, origin, format, text)
	if err != nil {
		return nil, err
	}
	return contentSourceMap(source), nil
}

func contentSourceMap(x ContentSource) map[string]any {
	var origin any = map[string]any{"kind": "exec_text"}
	if x.Origin != "" && x.Origin != "exec:index" {
		pair := strings.SplitN(x.Origin, "\x00", 2)
		if len(pair) == 2 {
			origin = map[string]any{"target": pair[0], "tool": pair[1]}
		}
	}
	return mapValue(redactPublicMetadata(map[string]any{"source": x.ID, "name": x.Name, "origin": origin, "format": x.Format, "bytes": x.Bytes, "chunks": x.Chunks, "created_at": x.CreatedAt.UTC().Format(time.RFC3339), "expires_at": x.ExpiresAt.UTC().Format(time.RFC3339)}))
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
	return !isChildMCPTarget(b.server, target, b.request) || isChildCatalogTarget(b.server, target, b.request)
}

func (b *codeModeBridge) contentSourceAllowed(meta ContentSource) bool {
	if meta.Origin == "exec:index" {
		return true
	}
	if meta.Origin == "" {
		return false
	}
	pair := strings.SplitN(meta.Origin, "\x00", 2)
	return len(pair) == 2 && b.contentOriginAllowed(pair[0], pair[1])
}

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
		name := firstString(opts, "name")
		origin := "exec:index"
		target, tool := firstString(opts, "target"), firstString(opts, "tool")
		if target != "" || tool != "" {
			if !b.contentOriginAllowed(target, tool) {
				return nil, b.fail("forbidden", "index origin target/tool is not authorized", nil)
			}
			origin = target + "\x00" + tool
		}
		meta, e := b.indexContent(arg, name, origin, "text")
		if e != nil {
			return nil, b.fail("content_index_failed", e.Error(), nil)
		}
		return b.jsonValue(this, meta), nil
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
		if err := store.Delete(principal, arg); err != nil {
			return nil, b.fail("content_delete_failed", "content source could not be deleted", nil)
		}
		return b.jsonValue(this, map[string]any{"deleted": true, "source": arg}), nil
	default:
		return nil, b.fail("invalid_arguments", "unknown content helper", nil)
	}
}
