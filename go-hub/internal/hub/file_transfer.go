package hub

import (
	"encoding/base64"
	"strings"
)

const transferFileLimit = 16 << 20
const transferChunkLimit = 32 << 10

func fileTransferToolDefinition() map[string]any {
	return map[string]any{"name": "file_transfer", "description": "Private binary file exchange through existing authenticated MCP, without public URLs. Paths must stay in configured roots. 16 MiB per file, four transfers per node, 32 KiB chunks, 15-minute TTL. Upload begin declares size and SHA256; chunk retries are idempotent; commit atomically creates a NEW file (never overwrites). Download begin snapshots and hashes the file. Resume after transport loss using transfer_id and offsets, not after node restart. Abort to release memory. Binary data is base64; do not print it into chat.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"action":      map[string]any{"type": "string", "enum": []string{"upload_begin", "upload_chunk", "upload_status", "upload_commit", "upload_abort", "download_begin", "download_chunk", "download_abort"}},
			"path":        map[string]any{"type": "string"},
			"transfer_id": map[string]any{"type": "string"},
			"size":        map[string]any{"type": "integer", "minimum": 0, "maximum": transferFileLimit},
			"sha256":      map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
			"offset":      map[string]any{"type": "integer", "minimum": 0, "maximum": transferFileLimit},
			"data_base64": map[string]any{"type": "string", "maxLength": base64.StdEncoding.EncodedLen(transferChunkLimit)},
			"max_bytes":   map[string]any{"type": "integer", "minimum": 1, "maximum": transferChunkLimit},
		}, "required": []string{"action", "path"}, "additionalProperties": false}}
}

func fileTransferReadAction(action string) bool {
	return action == "download_begin" || action == "download_chunk" || action == "download_abort"
}

func fileToolsForAgent(agent Agent) []map[string]any {
	result := []map[string]any{}
	for _, tool := range fileTools() {
		if tool["name"] != "file_transfer" || intFromAny(agent.Meta["file_transfer_version"]) >= 1 {
			result = append(result, tool)
		}
	}
	return result
}
func (s *Server) fileTransferCapable(target string) bool {
	if !strings.HasPrefix(target, "file:") {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	agent := s.agents["shell:"+strings.TrimPrefix(target, "file:")]
	return agent != nil && intFromAny(agent.Meta["file_transfer_version"]) >= 1
}
func (s *Server) fileToolsForTarget(target string) []map[string]any {
	meta := map[string]any{}
	if s.fileTransferCapable(target) {
		meta["file_transfer_version"] = 1
	}
	return fileToolsForAgent(Agent{AgentID: target, Meta: meta})
}
