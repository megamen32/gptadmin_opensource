package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const maxEditableFileBytes = 8 << 20

type fileEditorArgs struct {
	Action     string              `json:"action"`
	Path       string              `json:"path"`
	Content    string              `json:"content"`
	OldText    string              `json:"old_text"`
	NewText    string              `json:"new_text"`
	ReplaceAll bool                `json:"replace_all"`
	StartLine  int                 `json:"start_line"`
	EndLine    int                 `json:"end_line"`
	MaxBytes   int                 `json:"max_bytes"`
	Operations []fileEditOperation `json:"operations"`
}

type fileEditOperation struct {
	Action  string `json:"action"`
	Start   any    `json:"start"`
	End     any    `json:"end"`
	Content string `json:"content"`
}

type resolvedEditOperation struct {
	Action  string
	Start   int // 1-indexed; insert permits 0 and len(lines)
	End     int // inclusive for replace
	Content string
}

func (s *Server) mcpFileEditor(args map[string]any) (map[string]any, error) {
	var req fileEditorArgs
	b, _ := json.Marshal(args)
	if err := json.Unmarshal(b, &req); err != nil {
		return nil, err
	}
	req.Action = strings.ToLower(strings.TrimSpace(req.Action))
	if strings.TrimSpace(req.Path) == "" {
		return nil, errors.New("file_editor requires path")
	}
	path, err := filepath.Abs(req.Path)
	if err != nil {
		return nil, err
	}

	switch req.Action {
	case "view":
		return s.fileEditorView(path, req.StartLine, req.EndLine, req.MaxBytes)
	case "create":
		return s.fileEditorCreate(path, req.Content)
	case "str_replace":
		return s.fileEditorReplace(path, req.OldText, req.NewText, req.ReplaceAll)
	case "batch_edit":
		return s.fileEditorBatch(path, req.Operations)
	case "delete":
		return s.fileEditorDelete(path)
	default:
		return nil, fmt.Errorf("unknown file_editor action %q", req.Action)
	}
}

func (s *Server) fileEditorView(path string, startLine, endLine, maxBytes int) (map[string]any, error) {
	raw, _, err := readEditableFile(path)
	if err != nil {
		return nil, err
	}
	content := normalizeLF(string(raw))
	lines := logicalLines(content)
	if startLine <= 0 {
		startLine = 1
	}
	if startLine > len(lines) && len(lines) > 0 {
		return nil, fmt.Errorf("start_line %d exceeds file line count %d", startLine, len(lines))
	}
	if endLine <= 0 {
		endLine = startLine + 199
	}
	if endLine < startLine {
		return nil, errors.New("end_line must be >= start_line")
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	if maxBytes <= 0 {
		maxBytes = 64 << 10
	}
	if maxBytes > 256<<10 {
		maxBytes = 256 << 10
	}
	if len(lines) == 0 {
		return mcpText("[empty file]", map[string]any{"ok": true, "action": "view", "path": path, "line_count": 0, "view": "[empty file]", "truncated": false}), nil
	}
	var b strings.Builder
	last := startLine - 1
	truncated := false
	for i := startLine; i <= endLine; i++ {
		line := fmt.Sprintf("%s|%s", lineID(i, lines[i-1]), lines[i-1])
		additional := len(line)
		if b.Len() > 0 {
			additional++
		}
		if b.Len()+additional > maxBytes {
			truncated = true
			break
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
		last = i
	}
	if last < endLine || endLine < len(lines) {
		truncated = true
	}
	view := b.String()
	return mcpText(view, map[string]any{
		"ok": true, "action": "view", "path": path, "line_count": len(lines),
		"start_line": startLine, "end_line": last, "view": view, "truncated": truncated,
	}), nil
}

func (s *Server) fileEditorCreate(path, content string) (map[string]any, error) {
	if _, err := os.Lstat(path); err == nil {
		return nil, fmt.Errorf("file already exists: %s; use str_replace or batch_edit", path)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := atomicWriteFile(path, []byte(content), 0o644); err != nil {
		return nil, err
	}
	view := renderFreshLines(content, 1, logicalLineCount(content))
	return mcpText("file created; fresh line ids follow\n"+view, map[string]any{
		"ok": true, "action": "create", "path": path, "line_count": logicalLineCount(content), "view": view,
	}), nil
}

func (s *Server) fileEditorDelete(path string) (map[string]any, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("file_editor delete refuses directories: %s", path)
	}
	if err := os.Remove(path); err != nil {
		return nil, err
	}
	return mcpText("file deleted: "+path, map[string]any{"ok": true, "action": "delete", "path": path}), nil
}

func (s *Server) fileEditorReplace(path, oldText, newText string, replaceAll bool) (map[string]any, error) {
	if oldText == "" {
		return nil, errors.New("str_replace requires non-empty old_text")
	}
	raw, mode, err := readEditableFile(path)
	if err != nil {
		return nil, err
	}
	usesCRLF := bytes.Contains(raw, []byte("\r\n"))
	content := normalizeLF(string(raw))
	oldText = normalizeLF(oldText)
	newText = normalizeLF(newText)
	count := strings.Count(content, oldText)

	if count > 1 && !replaceAll {
		return nil, errors.New(multipleMatchDiagnostic(content, oldText, count))
	}
	if count == 0 {
		if actual, ok := uniqueWhitespaceMatch(content, oldText); ok {
			oldText = actual
			count = 1
		} else {
			return nil, errors.New(noMatchDiagnostic(content, oldText))
		}
	}

	before := content
	var after string
	var starts []int
	if replaceAll {
		starts = allMatchOffsets(before, oldText)
		after = strings.ReplaceAll(before, oldText, newText)
	} else {
		offset := strings.Index(before, oldText)
		starts = []int{offset}
		after = before[:offset] + newText + before[offset+len(oldText):]
	}
	if usesCRLF {
		after = strings.ReplaceAll(after, "\n", "\r\n")
	}
	if err := atomicWriteFile(path, []byte(after), mode); err != nil {
		return nil, err
	}

	beforeLF := before
	afterLF := normalizeLF(after)
	diff := renderReplacementDiff(beforeLF, afterLF, starts[0], oldText, newText)
	if replaceAll && len(starts) > 1 {
		diff = fmt.Sprintf("replaced %d occurrences\n%s", len(starts), diff)
	}
	return mcpText(diff, map[string]any{
		"ok": true, "action": "str_replace", "path": path, "matches": len(starts), "diff": diff,
	}), nil
}

func (s *Server) fileEditorBatch(path string, ops []fileEditOperation) (map[string]any, error) {
	if len(ops) == 0 {
		return nil, errors.New("batch_edit requires operations")
	}
	if len(ops) > 50 {
		return nil, errors.New("batch_edit supports at most 50 operations")
	}
	raw, mode, err := readEditableFile(path)
	if err != nil {
		return nil, err
	}
	usesCRLF := bytes.Contains(raw, []byte("\r\n"))
	content := normalizeLF(string(raw))
	lines := logicalLines(content)
	resolved := make([]resolvedEditOperation, 0, len(ops))
	var stale []string
	for i, op := range ops {
		r, err := resolveEditOperation(op, lines)
		if err != nil {
			stale = append(stale, fmt.Sprintf("operation %d: %v", i+1, err))
			continue
		}
		resolved = append(resolved, r)
	}
	if len(stale) > 0 {
		return nil, fmt.Errorf("no operations were applied; %d target(s) are stale or invalid:\n%s", len(stale), strings.Join(stale, "\n"))
	}
	if err := validateNonOverlapping(resolved); err != nil {
		return nil, err
	}

	// Apply from the bottom of the original file upward, so all ids refer to one immutable view.
	sort.SliceStable(resolved, func(i, j int) bool {
		if resolved[i].Start == resolved[j].Start {
			return resolved[i].End > resolved[j].End
		}
		return resolved[i].Start > resolved[j].Start
	})
	working := append([]string(nil), lines...)
	for _, op := range resolved {
		switch op.Action {
		case "replace":
			newLines := contentToLines(op.Content)
			start := op.Start - 1
			end := op.End
			working = append(append(append([]string{}, working[:start]...), newLines...), working[end:]...)
		case "insert":
			newLines := contentToLines(op.Content)
			idx := op.Start // anchor: 0 is before line 1; N is after original line N
			working = append(append(append([]string{}, working[:idx]...), newLines...), working[idx:]...)
		}
	}
	afterLF := strings.Join(working, "\n")
	if strings.HasSuffix(content, "\n") {
		afterLF += "\n"
	}
	toWrite := afterLF
	if usesCRLF {
		toWrite = strings.ReplaceAll(toWrite, "\n", "\r\n")
	}
	if err := atomicWriteFile(path, []byte(toWrite), mode); err != nil {
		return nil, err
	}
	diff := renderLineDiff(content, afterLF)
	return mcpText(diff, map[string]any{
		"ok": true, "action": "batch_edit", "path": path, "operations": len(ops), "diff": diff,
	}), nil
}

func readEditableFile(path string) ([]byte, os.FileMode, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, 0, err
	}
	if info.IsDir() {
		return nil, 0, fmt.Errorf("path is a directory: %s", path)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, 0, fmt.Errorf("file_editor refuses symlinks to avoid replacing the link itself: %s", path)
	}
	if info.Size() > maxEditableFileBytes {
		return nil, 0, fmt.Errorf("file exceeds editor limit of %d bytes", maxEditableFileBytes)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	if bytes.IndexByte(b, 0) >= 0 {
		return nil, 0, errors.New("file_editor only edits text files (NUL byte found)")
	}
	return b, info.Mode().Perm(), nil
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	ownership := ownershipForWrite(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".gptadmin-edit-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if mode == 0 {
		mode = 0o644
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if err := applyOpenFileOwnership(f, ownership); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

func normalizeLF(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

func filepathDir(path string) string { return filepath.Dir(path) }

func logicalLines(content string) []string {
	content = normalizeLF(content)
	if content == "" {
		return []string{}
	}
	parts := strings.Split(content, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func contentToLines(content string) []string {
	content = normalizeLF(content)
	if content == "" {
		return []string{}
	}
	parts := strings.Split(content, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func logicalLineCount(content string) int { return len(logicalLines(content)) }

func lineHash(content string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(content))
	v := h.Sum32()
	folded := uint16((v >> 16) ^ (v & 0xffff))
	return fmt.Sprintf("%04x", folded)
}

func lineID(line int, content string) string { return fmt.Sprintf("%d:%s", line, lineHash(content)) }

func renderFreshLines(content string, start, end int) string {
	lines := logicalLines(content)
	if len(lines) == 0 {
		return "[empty file]"
	}
	if start < 1 {
		start = 1
	}
	if end <= 0 || end > len(lines) {
		end = len(lines)
	}
	var b strings.Builder
	for i := start; i <= end; i++ {
		fmt.Fprintf(&b, "%s|%s", lineID(i, lines[i-1]), lines[i-1])
		if i != end {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func parseLineID(v any) (int, string, error) {
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return 0, "", errors.New("edit targets must be line ids like \"12:4fa2\"")
	}
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, "", fmt.Errorf("invalid line id %q", s)
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil || n < 1 {
		return 0, "", fmt.Errorf("invalid line id %q", s)
	}
	if len(parts[1]) != 4 {
		return 0, "", fmt.Errorf("invalid line id hash in %q", s)
	}
	return n, strings.ToLower(parts[1]), nil
}

func verifyLineID(v any, lines []string) (int, error) {
	n, want, err := parseLineID(v)
	if err != nil {
		return 0, err
	}
	if n > len(lines) {
		return 0, fmt.Errorf("line id %q is out of range: file has %d lines%s", v, len(lines), relocationHint(want, n, lines))
	}
	got := lineHash(lines[n-1])
	if got == want {
		return n, nil
	}
	lo := n - 2
	if lo < 1 {
		lo = 1
	}
	hi := n + 2
	if hi > len(lines) {
		hi = len(lines)
	}
	return 0, fmt.Errorf("line id %q is stale: line %d is now %s. Current content:\n%s%s\nRetry with these fresh ids", v, n, lineID(n, lines[n-1]), renderLineSlice(lines, lo, hi), relocationHint(want, n, lines))
}

func relocationHint(hash string, expected int, lines []string) string {
	type candidate struct{ line, dist int }
	var c []candidate
	for i, line := range lines {
		if lineHash(line) == hash {
			d := i + 1 - expected
			if d < 0 {
				d = -d
			}
			c = append(c, candidate{i + 1, d})
		}
	}
	if len(c) == 0 {
		return ""
	}
	sort.Slice(c, func(i, j int) bool { return c[i].dist < c[j].dist })
	if len(c) > 3 {
		c = c[:3]
	}
	ids := make([]string, 0, len(c))
	for _, x := range c {
		ids = append(ids, lineID(x.line, lines[x.line-1]))
	}
	return "; matching content/hash is now at " + strings.Join(ids, ", ")
}

func renderLineSlice(lines []string, start, end int) string {
	var b strings.Builder
	for i := start; i <= end; i++ {
		fmt.Fprintf(&b, "%s|%s", lineID(i, lines[i-1]), lines[i-1])
		if i != end {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func resolveEditOperation(op fileEditOperation, lines []string) (resolvedEditOperation, error) {
	action := strings.ToLower(strings.TrimSpace(op.Action))
	switch action {
	case "replace":
		start, err := verifyLineID(op.Start, lines)
		if err != nil {
			return resolvedEditOperation{}, err
		}
		end := start
		if op.End != nil {
			end, err = verifyLineID(op.End, lines)
			if err != nil {
				return resolvedEditOperation{}, err
			}
		}
		if end < start {
			return resolvedEditOperation{}, errors.New("replace end precedes start")
		}
		return resolvedEditOperation{Action: action, Start: start, End: end, Content: normalizeLF(op.Content)}, nil
	case "insert":
		if n, ok := numericEndpoint(op.Start); ok {
			if n != 0 && n != -1 {
				return resolvedEditOperation{}, errors.New("plain insert anchors may only be 0 (start) or -1 (end)")
			}
			if n == -1 {
				n = len(lines)
			}
			return resolvedEditOperation{Action: action, Start: n, End: n, Content: normalizeLF(op.Content)}, nil
		}
		anchor, err := verifyLineID(op.Start, lines)
		if err != nil {
			return resolvedEditOperation{}, err
		}
		return resolvedEditOperation{Action: action, Start: anchor, End: anchor, Content: normalizeLF(op.Content)}, nil
	default:
		return resolvedEditOperation{}, fmt.Errorf("unknown batch operation %q", op.Action)
	}
}

func numericEndpoint(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		if n == float64(int(n)) {
			return int(n), true
		}
	case int:
		return n, true
	case json.Number:
		i, err := n.Int64()
		if err == nil {
			return int(i), true
		}
	}
	return 0, false
}

func validateNonOverlapping(ops []resolvedEditOperation) error {
	type span struct{ start, end int }
	var spans []span
	for _, op := range ops {
		if op.Action == "replace" {
			spans = append(spans, span{op.Start, op.End})
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	for i := 1; i < len(spans); i++ {
		if spans[i].start <= spans[i-1].end {
			return fmt.Errorf("batch_edit replace ranges overlap: %d-%d and %d-%d", spans[i-1].start, spans[i-1].end, spans[i].start, spans[i].end)
		}
	}
	return nil
}

func allMatchOffsets(content, needle string) []int {
	var out []int
	for base := 0; ; {
		i := strings.Index(content[base:], needle)
		if i < 0 {
			break
		}
		pos := base + i
		out = append(out, pos)
		base = pos + len(needle)
	}
	return out
}

func multipleMatchDiagnostic(content, needle string, count int) string {
	lines := logicalLines(content)
	positions := allMatchOffsets(content, needle)
	var b strings.Builder
	fmt.Fprintf(&b, "found %d exact matches; nothing was changed. Add context, use replace_all, or target one with batch_edit:\n", count)
	for i, off := range positions {
		line := strings.Count(content[:off], "\n") + 1
		if line > len(lines) {
			continue
		}
		fmt.Fprintf(&b, "  %d. %s|%s", i+1, lineID(line, lines[line-1]), lines[line-1])
		if i != len(positions)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func uniqueWhitespaceMatch(content, wanted string) (string, bool) {
	wantedLines := logicalLines(wanted)
	if len(wantedLines) == 0 {
		return "", false
	}
	lines := logicalLines(content)
	if len(lines) < len(wantedLines) {
		return "", false
	}
	normWanted := normalizeWhitespaceBlock(wantedLines)
	var match string
	count := 0
	for i := 0; i+len(wantedLines) <= len(lines); i++ {
		window := lines[i : i+len(wantedLines)]
		if normalizeWhitespaceBlock(window) == normWanted {
			match = strings.Join(window, "\n")
			count++
		}
	}
	return match, count == 1
}

func normalizeWhitespaceBlock(lines []string) string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = strings.Join(strings.Fields(line), " ")
	}
	return strings.Join(out, "\n")
}

func noMatchDiagnostic(content, wanted string) string {
	lines := logicalLines(content)
	wantedLines := logicalLines(wanted)
	if len(lines) == 0 {
		return "no exact match found; file is empty"
	}
	windowSize := len(wantedLines)
	if windowSize < 1 {
		windowSize = 1
	}
	if windowSize > len(lines) {
		windowSize = len(lines)
	}
	type cand struct {
		start int
		score float64
	}
	var cands []cand
	wantTokens := tokenSet(wanted)
	for i := 0; i+windowSize <= len(lines); i++ {
		window := strings.Join(lines[i:i+windowSize], "\n")
		cands = append(cands, cand{start: i + 1, score: jaccard(wantTokens, tokenSet(window))})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	if len(cands) > 3 {
		cands = cands[:3]
	}
	var b strings.Builder
	b.WriteString("no exact match found; nothing was changed. Closest current content:\n")
	for idx, c := range cands {
		end := c.start + windowSize - 1
		fmt.Fprintf(&b, "\n%d. %s .. %s (%.0f%% token similarity)\n%s", idx+1, lineID(c.start, lines[c.start-1]), lineID(end, lines[end-1]), c.score*100, renderLineSlice(lines, c.start, end))
	}
	b.WriteString("\nUse the fresh text/ids above to retry; no separate read is required.")
	return b.String()
}

func tokenSet(s string) map[string]struct{} {
	m := map[string]struct{}{}
	for _, t := range strings.Fields(strings.ToLower(s)) {
		m[t] = struct{}{}
	}
	return m
}

func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	intersection := 0
	union := map[string]struct{}{}
	for k := range a {
		union[k] = struct{}{}
		if _, ok := b[k]; ok {
			intersection++
		}
	}
	for k := range b {
		union[k] = struct{}{}
	}
	if len(union) == 0 {
		return 0
	}
	return float64(intersection) / float64(len(union))
}

func renderReplacementDiff(before, after string, byteOffset int, oldText, newText string) string {
	oldLines := logicalLines(before)
	newLines := logicalLines(after)
	start := strings.Count(before[:byteOffset], "\n") + 1
	oldCount := len(logicalLines(oldText))
	if oldCount == 0 {
		oldCount = 1
	}
	newCount := len(logicalLines(newText))
	ctxStart := start - 2
	if ctxStart < 1 {
		ctxStart = 1
	}
	ctxEnd := start + newCount + 1
	if ctxEnd > len(newLines) {
		ctxEnd = len(newLines)
	}
	var b strings.Builder
	if ctxStart > 1 {
		b.WriteString("...\n")
	}
	for i := ctxStart; i < start && i <= len(newLines); i++ {
		fmt.Fprintf(&b, " %s|%s\n", lineID(i, newLines[i-1]), newLines[i-1])
	}
	if oldCount > 0 && start <= len(oldLines) {
		oldEnd := start + oldCount - 1
		if oldEnd > len(oldLines) {
			oldEnd = len(oldLines)
		}
		fmt.Fprintf(&b, "-%s..%s (%d line%s)\n", lineID(start, oldLines[start-1]), lineID(oldEnd, oldLines[oldEnd-1]), oldEnd-start+1, plural(oldEnd-start+1))
	}
	for i := 0; i < newCount; i++ {
		line := start + i
		if line <= len(newLines) {
			fmt.Fprintf(&b, "+%s|%s\n", lineID(line, newLines[line-1]), newLines[line-1])
		}
	}
	for i := start + newCount; i <= ctxEnd && i <= len(newLines); i++ {
		fmt.Fprintf(&b, " %s|%s\n", lineID(i, newLines[i-1]), newLines[i-1])
	}
	if ctxEnd < len(newLines) {
		b.WriteString("...")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func renderLineDiff(before, after string) string {
	// Batch edits deliberately return a bounded fresh view around all changed lines.
	oldLines := logicalLines(before)
	newLines := logicalLines(after)
	first := 1
	for first <= len(oldLines) && first <= len(newLines) && oldLines[first-1] == newLines[first-1] {
		first++
	}
	if first > len(oldLines) && first > len(newLines) {
		return "[no content change]"
	}
	lastOld, lastNew := len(oldLines), len(newLines)
	for lastOld >= first && lastNew >= first && oldLines[lastOld-1] == newLines[lastNew-1] {
		lastOld--
		lastNew--
	}
	start := first - 2
	if start < 1 {
		start = 1
	}
	end := lastNew + 2
	if end > len(newLines) {
		end = len(newLines)
	}
	var b strings.Builder
	if start > 1 {
		b.WriteString("...\n")
	}
	for i := start; i < first && i <= len(newLines); i++ {
		fmt.Fprintf(&b, " %s|%s\n", lineID(i, newLines[i-1]), newLines[i-1])
	}
	if lastOld >= first && first <= len(oldLines) {
		fmt.Fprintf(&b, "-%s..%s (%d line%s)\n", lineID(first, oldLines[first-1]), lineID(lastOld, oldLines[lastOld-1]), lastOld-first+1, plural(lastOld-first+1))
	}
	for i := first; i <= lastNew && i <= len(newLines); i++ {
		fmt.Fprintf(&b, "+%s|%s\n", lineID(i, newLines[i-1]), newLines[i-1])
	}
	for i := lastNew + 1; i <= end && i <= len(newLines); i++ {
		fmt.Fprintf(&b, " %s|%s\n", lineID(i, newLines[i-1]), newLines[i-1])
	}
	if end < len(newLines) {
		b.WriteString("...")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
