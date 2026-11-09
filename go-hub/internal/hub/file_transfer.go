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
	return action == "download_begin" || action == "download_chunk" || action == "download_abort" ||
		action == "stat" || action == "list"
}

// v2TransferAction reports actions introduced by file_transfer_version 2.
func v2TransferAction(action string) bool {
	return action == "stat" || action == "list" || action == "upload_replace"
}

func fileToolsForAgent(agent Agent) []map[string]any {
	result := []map[string]any{}
	for _, tool := range fileTools() {
		if tool["name"] != "file_transfer" {
			result = append(result, tool)
			continue
		}
		switch intFromAny(agent.Meta["file_transfer_version"]) {
		case 0:
		case 1:
			result = append(result, tool)
		default:
			result = append(result, fileTransferToolDefinitionV2())
		}
	}
	return result
}
func (s *Server) fileTransferVersion(target string) int {
	if !strings.HasPrefix(target, "file:") {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	name := "shell:" + strings.TrimPrefix(target, "file:")
	if agent := s.agents[name]; agent != nil {
		return intFromAny(agent.Meta["file_transfer_version"])
	}
	// Route-only members carry their executor capability meta on the
	// membership route; the registry parent may not exist for them.
	if meta, ok := s.membershipRouteMeta[name]; ok {
		return intFromAny(meta["file_transfer_version"])
	}
	return 0
}

func (s *Server) fileTransferCapable(target string) bool {
	return s.fileTransferVersion(target) >= 1
}
func (s *Server) fileToolsForTarget(target string) []map[string]any {
	meta := map[string]any{}
	if v := s.fileTransferVersion(target); v > 0 {
		meta["file_transfer_version"] = v
	}
	return fileToolsForAgent(Agent{AgentID: target, Meta: meta})
}

func fileTransferToolDefinitionV2() map[string]any {
	return map[string]any{"name": "file_transfer", "description": "Private binary file exchange through existing authenticated MCP, without public URLs. Paths must stay in configured roots. 16 MiB per file, four transfers per node, 32 KiB chunks, 15-minute TTL. Upload begin declares size and SHA256; chunk retries are idempotent; commit atomically creates a NEW file (never overwrites). upload_replace instead atomically replaces an existing file at commit, and only while its digest still equals expected_sha256. Download begin snapshots and hashes the file. stat and list are stateless metadata reads within the same roots; stat can hash with hash=true. Resume after transport loss using transfer_id and offsets, not after node restart. Abort to release memory. Binary data is base64; do not print it into chat.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"action":          map[string]any{"type": "string", "enum": []string{"upload_begin", "upload_replace", "upload_chunk", "upload_status", "upload_commit", "upload_abort", "download_begin", "download_chunk", "download_abort", "stat", "list"}},
			"path":            map[string]any{"type": "string"},
			"transfer_id":     map[string]any{"type": "string"},
			"size":            map[string]any{"type": "integer", "minimum": 0, "maximum": transferFileLimit},
			"sha256":          map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
			"expected_sha256": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$", "description": "upload_replace: current destination digest; commit aborts when it no longer matches"},
			"pattern":         map[string]any{"type": "string", "maxLength": 256, "description": "list: glob matched against entry names"},
			"hash":            map[string]any{"type": "boolean", "description": "stat: include sha256 of a regular file"},
			"offset":          map[string]any{"type": "integer", "minimum": 0, "maximum": transferFileLimit},
			"data_base64":     map[string]any{"type": "string", "maxLength": base64.StdEncoding.EncodedLen(transferChunkLimit)},
			"max_bytes":       map[string]any{"type": "integer", "minimum": 1, "maximum": transferChunkLimit},
		}, "required": []string{"action", "path"}, "additionalProperties": false}}
}
