package hub

import (
        "crypto/sha256"
        "encoding/base64"
        "encoding/hex"
        "net/http"
        "sort"
        "strings"
        "time"
)

// cloudOSWaitTimeout keeps the foreground wait long enough to actually
// collect a result: DefaultTimeout is 0 on several node installs, which used
// to return "still running" before the executor ever picked the job up.
func cloudOSWaitTimeout(req map[string]any, def, min time.Duration) time.Duration {
        if t := timeoutFromReq(req, def); t > min {
                return t
        }
        return min
}

// cloudOSFirstMeta pulls a string-ish value out of an agent meta map.
func cloudOSFirstMeta(meta map[string]any, keys ...string) string {
        return firstString(meta, keys...)
}

// cloudOSHostName resolves the hostname a shell agent is running on.
func cloudOSHostName(agentID string, meta map[string]any) string {
        if h := cloudOSFirstMeta(meta, "hostname"); h != "" {
                return h
        }
        return strings.TrimPrefix(agentID, "shell:")
}

// cloudOSResolveFileTarget maps a shell:<host> target to its paired virtual
// file:<host> target using the live agent registry.
func (s *Server) cloudOSResolveFileTarget(shellTarget string) string {
        s.mu.Lock()
        agent := s.agents[shellTarget]
        s.mu.Unlock()
        if agent == nil {
                return ""
        }
        return s.cloudOSFileTarget(agent.AgentID, agent.Meta)
}

// cloudOSFileTarget returns the paired file:<host> agent id for a shell
// agent, or "" when the host has no file transfer executor. File targets are
// virtual: derived from the shell agent on the fly, not stored in s.agents.
func (s *Server) cloudOSFileTarget(shellAgentID string, meta map[string]any) string {
        if !strings.HasPrefix(shellAgentID, "shell:") {
                return ""
        }
        if intFromAny(meta["build_version"]) >= 194 || intFromAny(meta["file_transfer_version"]) >= 1 {
                return "file:" + strings.TrimPrefix(shellAgentID, "shell:")
        }
        return ""
}

// cloudOSShellComputers is a narrow, UI-oriented projection of existing
// ShellMCP targets. CloudOS is only a client of ShellMCP, never a second host
// enrollment protocol.
func (s *Server) cloudOSShellComputers(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodGet {
                writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
                return
        }
        s.mu.Lock()
        type hostEntry struct {
                id         string
                name       string
                hostname   string
                os         string
                status     string
                lastSeen   float64
                meta       map[string]any
                targets    []string
                fileTarget string
        }
        hosts := map[string]*hostEntry{}
        // publicAgentsLocked applies the same presentation overlay the catalog
        // uses: membership routes recover stale approved shells to online and
        // project membership-only nodes that never hit the legacy registry.
        publicAgents := s.publicAgentsLocked(nil)
        for _, agent := range publicAgents {
                if agent.AgentID == "" || !strings.HasPrefix(agent.AgentID, "shell:") {
                        continue
                }
                hostname := cloudOSHostName(agent.AgentID, agent.Meta)
                agentStatus := agent.Status
                entry, seen := hosts[hostname]
                if !seen {
                        meta := agent.Meta
                        if meta == nil {
                                meta = map[string]any{}
                        }
                        entry = &hostEntry{
                                id:       agent.AgentID,
                                name:     strings.TrimPrefix(agent.AgentID, "shell:"),
                                hostname: hostname,
                                os:       cloudOSFirstMeta(meta, "os", "platform"),
                                status:   agentStatus,
                                lastSeen: agent.LastSeen,
                                meta:     meta,
                        }
                        hosts[hostname] = entry
                } else {
                        entry.targets = append(entry.targets, strings.TrimPrefix(agent.AgentID, "shell:"))
                        // prefer an online entry when the first one was offline
                        if entry.status != "online" && agentStatus == "online" {
                                entry.status = agentStatus
                                entry.id = agent.AgentID
                                entry.name = strings.TrimPrefix(agent.AgentID, "shell:")
                                entry.meta = agent.Meta
                                if entry.meta == nil {
                                        entry.meta = map[string]any{}
                                }
                        }
                        if agent.LastSeen > entry.lastSeen {
                                entry.lastSeen = agent.LastSeen
                        }
                }
        }
        s.mu.Unlock()

        computers := make([]map[string]any, 0, len(hosts))
        for hostname, entry := range hosts {
                meta := entry.meta
                cores := 0
                if v, ok := toFloat(meta["cores"]); ok {
                        cores = int(v)
                }
                memMB := 0.0
                if v, ok := toFloat(meta["mem_mb"]); ok {
                        memMB = v
                }
                storageBytes := 0.0
                if v, ok := toFloat(meta["storage_bytes"]); ok {
                        storageBytes = v
                }
                fileTarget := s.cloudOSFileTarget(entry.id, entry.meta)
                caps := []string{"terminal", "files"}
                if fileTarget != "" {
                        caps = append(caps, "file_transfer")
                }
                computer := map[string]any{
                        "id":           entry.id,
                        "name":         entry.name,
                        "hostname":     hostname,
                        "os":           entry.os,
                        "status":       entry.status,
                        "last_seen":    entry.lastSeen,
                        "capabilities": caps,
                        "cores":        cores,
                        "mem_mb":       memMB,
                        "storage_bytes": storageBytes,
                        "build":        cloudOSFirstMeta(meta, "build_version"),
                        "transport":    cloudOSFirstMeta(meta, "mode", "transport"),
                        "file_target": fileTarget,
                        "extra_targets": entry.targets,
                }
                if gpu := cloudOSFirstMeta(meta, "gpu", "gpus"); gpu != "" {
                        computer["gpu"] = gpu
                }
                computers = append(computers, computer)
        }
        sort.Slice(computers, func(i, j int) bool {
                return computers[i]["name"].(string) < computers[j]["name"].(string)
        })
        writeJSON(w, http.StatusOK, map[string]any{"computers": computers})
}

// toFloat coerces numeric-ish meta values to float64.
func toFloat(v any) (float64, bool) {
        switch n := v.(type) {
        case float64:
                return n, true
        case float32:
                return float64(n), true
        case int:
                return float64(n), true
        case int64:
                return float64(n), true
        case uint64:
                return float64(n), true
        }
        return 0, false
}

// cloudOSContentItems returns the deepest content array in an executeMCPTool
// response (envelope generations differ: response.content, result.result.content).
func cloudOSContentItems(resp map[string]any) []map[string]any {
        var best []map[string]any
        bestDepth := -1
        var visit func(node any, depth int)
        visit = func(node any, depth int) {
                switch v := node.(type) {
                case map[string]any:
                        if raw, ok := v["content"]; ok {
                                if items, ok := raw.([]any); ok {
                                        cands := make([]map[string]any, 0, len(items))
                                        hasTyped := false
                                        for _, item := range items {
                                                if m, ok := item.(map[string]any); ok {
                                                        if _, typed := m["type"]; typed {
                                                                hasTyped = true
                                                        }
                                                        cands = append(cands, m)
                                                }
                                        }
                                        if hasTyped && depth > bestDepth {
                                                best = cands
                                                bestDepth = depth
                                        }
                                }
                        }
                        for _, child := range v {
                                visit(child, depth+1)
                        }
                case []any:
                        for _, child := range v {
                                visit(child, depth)
                        }
                }
        }
        visit(resp, 0)
        return best
}

// cloudOSExtractImage finds the first inline image in an MCP tool result and
// returns a data: URL (or "").
func cloudOSExtractImage(resp map[string]any) string {
        for _, item := range cloudOSContentItems(resp) {
                if item["type"] == "image" {
                        data, _ := item["data"].(string)
                        mime, _ := item["mimeType"].(string)
                        if data == "" {
                                continue
                        }
                        if mime == "" {
                                mime = "image/png"
                        }
                        return "data:" + mime + ";base64," + data
                }
        }
        return ""
}

// cloudOSExtractError digs the executor error message out of a failed
// executeMCPTool response.
func cloudOSExtractError(resp map[string]any) string {
        if resp == nil {
                return ""
        }
        var visit func(node any) string
        visit = func(node any) string {
                switch v := node.(type) {
                case map[string]any:
                        if e, ok := v["error"].(string); ok && e != "" {
                                return e
                        }
                        if e, ok := v["error"].(map[string]any); ok {
                                if msg, ok := e["message"].(string); ok && msg != "" {
                                        return msg
                                }
                        }
                        for _, child := range v {
                                if msg := visit(child); msg != "" {
                                        return msg
                                }
                        }
                case []any:
                        for _, child := range v {
                                if msg := visit(child); msg != "" {
                                        return msg
                                }
                        }
                }
                return ""
        }
        return visit(resp)
}

// cloudOSExtractText joins the text parts of an MCP tool result.
func cloudOSExtractText(resp map[string]any) string {
        var parts []string
        for _, item := range cloudOSContentItems(resp) {
                if t, ok := item["text"].(string); ok && t != "" {
                        parts = append(parts, t)
                }
        }
        return strings.Join(parts, "\n")
}

// cloudOSExtractTransfer digs the transfer object out of a file_transfer
// response (stat results have no transfer_id, so accept any transfer map).
func cloudOSExtractTransfer(resp map[string]any) map[string]any {
        var found map[string]any
        var visit func(node any)
        visit = func(node any) {
                if found != nil {
                        return
                }
                switch v := node.(type) {
                case map[string]any:
                        if t, ok := v["transfer"].(map[string]any); ok {
                                found = t
                                return
                        }
                        for _, child := range v {
                                visit(child)
                        }
                case []any:
                        for _, child := range v {
                                visit(child)
                        }
                }
        }
        visit(resp)
        return found
}

func cloudOSReadReq(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
        if r.Method != http.MethodPost {
                writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
                return nil, false
        }
        var req map[string]any
        if err := readJSON(r, &req); err != nil {
                writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
                return nil, false
        }
        return req, true
}

func (s *Server) cloudOSShellExec(w http.ResponseWriter, r *http.Request) {
        req, ok := cloudOSReadReq(w, r)
        if !ok {
                return
        }
        target, cmd := firstString(req, "target", "computer"), firstString(req, "cmd", "command")
        if !strings.HasPrefix(target, "shell:") || cmd == "" {
                writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "target must be a ShellMCP computer and command is required"})
                return
        }
        resp, status := s.executeMCPTool(r, target, "shell_exec", map[string]any{"cmd": cmd, "cwd": req["cwd"], "timeout": req["timeout"]}, false, cloudOSWaitTimeout(req, s.cfg.DefaultTimeout, 20*time.Second), "")
        writeJSON(w, status, resp)
}

func (s *Server) cloudOSShellInspect(w http.ResponseWriter, r *http.Request) {
        req, ok := cloudOSReadReq(w, r)
        if !ok {
                return
        }
        target, path := firstString(req, "target", "computer"), firstString(req, "path")
        if !strings.HasPrefix(target, "shell:") || path == "" {
                writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "target must be a ShellMCP computer and path is required"})
                return
        }
        resp, status := s.executeMCPTool(r, target, "system_inspect", map[string]any{"action": "list_directory", "path": path, "max_bytes": 1048576}, false, cloudOSWaitTimeout(req, s.cfg.DefaultTimeout, 15*time.Second), "")
        writeJSON(w, status, resp)
}

// cloudOSShellRead streams a whole file back to the CloudOS editor through the
// paired file: target (download_begin + download_chunk loop).
func (s *Server) cloudOSShellRead(w http.ResponseWriter, r *http.Request) {
        req, ok := cloudOSReadReq(w, r)
        if !ok {
                return
        }
        target, path := firstString(req, "target", "computer"), firstString(req, "path")
        if target == "" || path == "" {
                writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "target and path are required"})
                return
        }
        if strings.HasPrefix(target, "shell:") {
                target = s.cloudOSResolveFileTarget(target)
                if target == "" {
                        writeJSON(w, http.StatusNotFound, map[string]any{"detail": "this node has no file transfer executor"})
                        return
                }
        }
        resp, status := s.executeMCPTool(r, target, "file_transfer", map[string]any{"action": "download_begin", "path": path}, false, cloudOSWaitTimeout(req, s.cfg.DefaultTimeout, 15*time.Second), "")
        transfer := cloudOSExtractTransfer(resp)
        if transfer == nil {
                if msg := cloudOSExtractError(resp); msg != "" {
                        writeJSON(w, status, map[string]any{"detail": msg})
                        return
                }
                writeJSON(w, status, resp)
                return
        }
        transferID, _ := transfer["transfer_id"].(string)
        size, _ := toFloat(transfer["size"])
        chunkBytes, hasChunk := toFloat(transfer["chunk_bytes"])
        if !hasChunk || chunkBytes <= 0 {
                chunkBytes = 32 * 1024
        }
        sha256Hex, _ := transfer["sha256"].(string)
        var assembled []byte
        for offset := 0.0; offset < size || size == 0; {
                chunkResp, status := s.executeMCPTool(r, target, "file_transfer", map[string]any{"action": "download_chunk", "path": path, "transfer_id": transferID, "offset": int(offset)}, false, cloudOSWaitTimeout(req, s.cfg.DefaultTimeout, 20*time.Second), "")
                chunkTransfer := cloudOSExtractTransfer(chunkResp)
                if chunkTransfer == nil {
                        s.executeMCPTool(r, target, "file_transfer", map[string]any{"action": "download_abort", "path": path, "transfer_id": transferID}, false, 5*time.Second, "")
                        writeJSON(w, status, chunkResp)
                        return
                }
                dataB64, _ := chunkTransfer["data_base64"].(string)
                raw, err := base64.StdEncoding.DecodeString(dataB64)
                if err != nil {
                        writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "corrupt download chunk"})
                        return
                }
                assembled = append(assembled, raw...)
                eof, _ := chunkTransfer["eof"].(bool)
                if eof {
                        break
                }
                next, ok := toFloat(chunkTransfer["next_offset"])
                if !ok || next <= offset {
                        offset += float64(len(raw))
                } else {
                        offset = next
                }
                if len(raw) == 0 && !eof {
                        break
                }
                if size == 0 {
                        break
                }
        }
        s.executeMCPTool(r, target, "file_transfer", map[string]any{"action": "download_abort", "path": path, "transfer_id": transferID}, false, 5*time.Second, "")
        if sha256Hex != "" {
                sum := sha256.Sum256(assembled)
                if hex.EncodeToString(sum[:]) != sha256Hex {
                        writeJSON(w, http.StatusConflict, map[string]any{"detail": "download checksum mismatch"})
                        return
                }
        }
        // 16 MiB transfer ceiling; refuse anything the editor cannot handle.
        if len(assembled) > 8<<20 {
                writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"detail": "file exceeds the 8 MiB editor limit"})
                return
        }
        writeJSON(w, http.StatusOK, map[string]any{"path": path, "size": len(assembled), "sha256": sha256Hex, "content_base64": base64.StdEncoding.EncodeToString(assembled)})
}

// cloudOSShellWrite saves editor content through upload_begin (new file) or
// upload_replace (existing file, CAS against the client-seen sha256).
func (s *Server) cloudOSShellWrite(w http.ResponseWriter, r *http.Request) {
        req, ok := cloudOSReadReq(w, r)
        if !ok {
                return
        }
        target, path := firstString(req, "target", "computer"), firstString(req, "path")
        contentB64, expectedSha := firstString(req, "content_base64"), firstString(req, "sha256")
        if target == "" || path == "" || contentB64 == "" {
                writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "target, path and content_base64 are required"})
                return
        }
        if strings.HasPrefix(target, "shell:") {
                target = s.cloudOSResolveFileTarget(target)
                if target == "" {
                        writeJSON(w, http.StatusNotFound, map[string]any{"detail": "this node has no file transfer executor"})
                        return
                }
        }
        content, err := base64.StdEncoding.DecodeString(contentB64)
        if err != nil {
                writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "content_base64 is not valid base64"})
                return
        }
        if len(content) > 8<<20 {
                writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"detail": "file exceeds the 8 MiB editor limit"})
                return
        }
        sum := sha256.Sum256(content)
        newSha := hex.EncodeToString(sum[:])
        // stat the destination to pick new-file vs replace
        statResp, _ := s.executeMCPTool(r, target, "file_transfer", map[string]any{"action": "stat", "path": path}, false, cloudOSWaitTimeout(req, s.cfg.DefaultTimeout, 10*time.Second), "")
        statTransfer := cloudOSExtractTransfer(statResp)
        exists := statTransfer != nil
        if exists && expectedSha == "" {
                // editor must send the sha it read; refuse blind overwrite
                writeJSON(w, http.StatusPreconditionRequired, map[string]any{"detail": "sha256 of the file you opened is required to overwrite it"})
                return
        }
        action := "upload_begin"
        args := map[string]any{"action": action, "path": path, "size": len(content), "sha256": newSha}
        if exists {
                action = "upload_replace"
                args = map[string]any{"action": action, "path": path, "size": len(content), "sha256": newSha, "expected_sha256": expectedSha}
        }
        resp, status := s.executeMCPTool(r, target, "file_transfer", args, false, cloudOSWaitTimeout(req, s.cfg.DefaultTimeout, 15*time.Second), "")
        transfer := cloudOSExtractTransfer(resp)
        if transfer == nil {
                if msg := cloudOSExtractError(resp); msg != "" {
                        writeJSON(w, status, map[string]any{"detail": msg})
                        return
                }
                writeJSON(w, status, resp)
                return
        }
        transferID, _ := transfer["transfer_id"].(string)
        chunkBytes, hasChunk := toFloat(transfer["chunk_bytes"])
        if !hasChunk || chunkBytes <= 0 {
                chunkBytes = 32 * 1024
        }
        for offset := 0; offset < len(content); {
                end := offset + int(chunkBytes)
                if end > len(content) {
                        end = len(content)
                }
                chunk := content[offset:end]
                chunkResp, status := s.executeMCPTool(r, target, "file_transfer", map[string]any{"action": "upload_chunk", "path": path, "transfer_id": transferID, "offset": offset, "data_base64": base64.StdEncoding.EncodeToString(chunk)}, false, cloudOSWaitTimeout(req, s.cfg.DefaultTimeout, 30*time.Second), "")
                if cloudOSExtractTransfer(chunkResp) == nil {
                        s.executeMCPTool(r, target, "file_transfer", map[string]any{"action": "upload_abort", "path": path, "transfer_id": transferID}, false, 5*time.Second, "")
                        writeJSON(w, status, chunkResp)
                        return
                }
                offset = end
        }
        commitResp, status := s.executeMCPTool(r, target, "file_transfer", map[string]any{"action": "upload_commit", "path": path, "transfer_id": transferID}, false, cloudOSWaitTimeout(req, s.cfg.DefaultTimeout, 20*time.Second), "")
        committed := cloudOSExtractTransfer(commitResp)
        result := map[string]any{"path": path, "size": len(content), "sha256": newSha, "created": !exists}
        if committed != nil {
                if completed, ok := committed["completed"].(bool); ok {
                        result["completed"] = completed
                }
                writeJSON(w, status, result)
                return
        }
        writeJSON(w, status, result)
}

// cloudOSBrowserTool runs one browser child-MCP tool and echoes its text
// payload; used by navigate/snapshot/tabs/click.
func (s *Server) cloudOSBrowserTool(w http.ResponseWriter, r *http.Request, req map[string]any, tool string, args map[string]any, minTimeout time.Duration) map[string]any {
        target := firstString(req, "connector")
        if !strings.HasPrefix(target, "mcp:") {
                writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "connector is required"})
                return nil
        }
        resp, status := s.executeMCPTool(r, target, tool, args, false, cloudOSWaitTimeout(req, s.cfg.DefaultTimeout, minTimeout), "")
        if status != http.StatusOK {
                writeJSON(w, status, resp)
                return nil
        }
        return resp
}

func (s *Server) cloudOSBrowserConnectors(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodGet {
                writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
                return
        }
        s.mu.Lock()
        connectors := []map[string]any{}
        for _, parent := range s.agents {
                if parent == nil || !strings.HasPrefix(parent.AgentID, "shell:") {
                        continue
                }
                for _, agent := range childMCPAgents(*parent) {
                        name := strings.ToLower(agent.Name + " " + agent.AgentID)
                        if !strings.Contains(name, "browser") {
                                continue
                        }
                        connectors = append(connectors, map[string]any{"id": agent.AgentID, "name": agent.Name, "status": agent.Status, "parent": firstString(agent.Meta, "parent_server_id")})
                }
        }
        s.mu.Unlock()
        writeJSON(w, http.StatusOK, map[string]any{"connectors": connectors})
}

// cloudOSBrowserNavigate opens a URL in the connector browser.
func (s *Server) cloudOSBrowserNavigate(w http.ResponseWriter, r *http.Request) {
        req, ok := cloudOSReadReq(w, r)
        if !ok {
                return
        }
        url := firstString(req, "url")
        if url == "" {
                writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "url is required"})
                return
        }
        resp := s.cloudOSBrowserTool(w, r, req, "agent_browser_open", map[string]any{"url": url}, 30*time.Second)
        if resp == nil {
                return
        }
        titleResp, _ := s.executeMCPTool(r, firstString(req, "connector"), "agent_browser_get_title", map[string]any{}, false, 15*time.Second, "")
        writeJSON(w, http.StatusOK, map[string]any{"ok": true, "url": url, "title": cloudOSExtractText(titleResp)})
}

// cloudOSBrowserScreenshot captures an inline JPEG screenshot.
func (s *Server) cloudOSBrowserScreenshot(w http.ResponseWriter, r *http.Request) {
        req, ok := cloudOSReadReq(w, r)
        if !ok {
                return
        }
        quality := 15
        if q, ok := toFloat(req["quality"]); ok && q >= 5 && q <= 40 {
                quality = int(q)
        }
        resp := s.cloudOSBrowserTool(w, r, req, "agent_browser_screenshot", map[string]any{"format": "jpeg", "quality": quality}, 30*time.Second)
        if resp == nil {
                return
        }
        image := cloudOSExtractImage(resp)
        if image == "" {
                // result likely exceeded the inline budget and was stored; surface a
                // clear message instead of an empty frame.
                writeJSON(w, http.StatusGone, map[string]any{"detail": "screenshot too large for inline delivery; lower the quality"})
                return
        }
        titleResp, _ := s.executeMCPTool(r, firstString(req, "connector"), "agent_browser_get_title", map[string]any{}, false, 10*time.Second, "")
        urlResp, _ := s.executeMCPTool(r, firstString(req, "connector"), "agent_browser_get_url", map[string]any{}, false, 10*time.Second, "")
        writeJSON(w, http.StatusOK, map[string]any{"image": image, "title": cloudOSExtractText(titleResp), "url": cloudOSExtractText(urlResp)})
}

// cloudOSBrowserSnapshot returns the accessibility tree text.
func (s *Server) cloudOSBrowserSnapshot(w http.ResponseWriter, r *http.Request) {
        req, ok := cloudOSReadReq(w, r)
        if !ok {
                return
        }
        resp := s.cloudOSBrowserTool(w, r, req, "agent_browser_snapshot", map[string]any{"compact": true}, 25*time.Second)
        if resp == nil {
                return
        }
        writeJSON(w, http.StatusOK, map[string]any{"snapshot": cloudOSExtractText(resp)})
}

// cloudOSBrowserTabs lists open tabs.
func (s *Server) cloudOSBrowserTabs(w http.ResponseWriter, r *http.Request) {
        req, ok := cloudOSReadReq(w, r)
        if !ok {
                return
        }
        resp := s.cloudOSBrowserTool(w, r, req, "agent_browser_tab_list", map[string]any{}, 20*time.Second)
        if resp == nil {
                return
        }
        writeJSON(w, http.StatusOK, map[string]any{"tabs": cloudOSExtractText(resp)})
}

// cloudOSBrowserClick clicks a snapshot ref (@e1) or CSS selector.
func (s *Server) cloudOSBrowserClick(w http.ResponseWriter, r *http.Request) {
        req, ok := cloudOSReadReq(w, r)
        if !ok {
                return
        }
        selector := firstString(req, "selector", "ref")
        if selector == "" {
                writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "selector is required"})
                return
        }
        resp := s.cloudOSBrowserTool(w, r, req, "agent_browser_click", map[string]any{"selector": selector}, 25*time.Second)
        if resp == nil {
                return
        }
        writeJSON(w, http.StatusOK, map[string]any{"ok": true, "clicked": selector})
}

// cloudOSBrowserTabsListLegacy keeps the old tabs endpoint working for
// existing clients.
func (s *Server) cloudOSBrowserTabsListLegacy(w http.ResponseWriter, r *http.Request) {
        req, ok := cloudOSReadReq(w, r)
        if !ok {
                return
        }
        target := firstString(req, "connector")
        if !strings.HasPrefix(target, "mcp:") {
                writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "connector is required"})
                return
        }
        resp, status := s.executeMCPTool(r, target, "tabs", map[string]any{"action": "list"}, false, cloudOSWaitTimeout(req, s.cfg.DefaultTimeout, 15*time.Second), "")
        writeJSON(w, status, resp)
}
