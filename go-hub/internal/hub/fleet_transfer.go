package hub

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
)

const fleetTransferToolName = "fleet_transfer"

type fleetTransferAllowlistEntry struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

type fleetTransferRequest struct {
	Source          string `json:"source"`
	Destination     string `json:"destination"`
	SourcePath      string `json:"source_path"`
	DestinationPath string `json:"destination_path"`
}

func fleetTransferToolDefinition() map[string]any {
	return map[string]any{
		"name":        fleetTransferToolName,
		"title":       "Fleet transfer",
		"description": "Move one bounded file between an explicitly allowlisted pair of enrolled GPTAdmin file targets through the existing authenticated Hub path. No direct LAN/SSH access is opened. The destination must be a new file. The receipt includes source, destination, size and SHA-256.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"source":           map[string]any{"type": "string", "pattern": "^file:[^/\\\\:]+$"},
				"destination":      map[string]any{"type": "string", "pattern": "^file:[^/\\\\:]+$"},
				"source_path":      map[string]any{"type": "string", "minLength": 1},
				"destination_path": map[string]any{"type": "string", "minLength": 1},
			},
			"required":             []string{"source", "destination", "source_path", "destination_path"},
			"additionalProperties": false,
		},
		"outputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status":           map[string]any{"type": "string"},
				"operation":        map[string]any{"type": "string"},
				"source":           map[string]any{"type": "string"},
				"destination":      map[string]any{"type": "string"},
				"source_path":      map[string]any{"type": "string"},
				"destination_path": map[string]any{"type": "string"},
				"size":             map[string]any{"type": "integer", "minimum": 0, "maximum": transferFileLimit},
				"sha256":           map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
				"verified":         map[string]any{"type": "boolean"},
			},
			"required": []string{"status", "operation", "source", "destination", "size", "sha256", "verified"},
		},
		"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": true, "openWorldHint": false},
	}
}

func canonicalFleetFileTarget(raw string) (string, error) {
	target := strings.TrimSpace(raw)
	if target != raw || !strings.HasPrefix(target, "file:") {
		return "", errors.New("fleet transfer targets must be canonical file:<node> IDs")
	}
	name := strings.TrimPrefix(target, "file:")
	if name == "" || name != canonicalShellQueueName(name) || strings.ContainsAny(name, "/\\:") {
		return "", fmt.Errorf("invalid fleet transfer target %q", raw)
	}
	return target, nil
}

func fleetTransferPairKey(source, destination string) string {
	return source + "\x00" + destination
}

func parseFleetTransferAllowlist(raw string) (map[string]struct{}, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]struct{}{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.DisallowUnknownFields()
	var entries []fleetTransferAllowlistEntry
	if err := decoder.Decode(&entries); err != nil {
		return nil, errors.New("GPTADMIN_FLEET_TRANSFER_ALLOWLIST must be a JSON array of source/destination pairs")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("GPTADMIN_FLEET_TRANSFER_ALLOWLIST must contain one JSON value")
	}
	if len(entries) > 256 {
		return nil, errors.New("GPTADMIN_FLEET_TRANSFER_ALLOWLIST contains too many pairs")
	}
	allowed := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		source, err := canonicalFleetFileTarget(entry.Source)
		if err != nil {
			return nil, err
		}
		destination, err := canonicalFleetFileTarget(entry.Destination)
		if err != nil {
			return nil, err
		}
		if source == destination {
			return nil, errors.New("fleet transfer source and destination must differ")
		}
		key := fleetTransferPairKey(source, destination)
		if _, exists := allowed[key]; exists {
			return nil, fmt.Errorf("duplicate fleet transfer pair %s -> %s", source, destination)
		}
		allowed[key] = struct{}{}
	}
	return allowed, nil
}

func fleetTransferDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validFleetTransferDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func fleetTransferPayload(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		if transfer := mapValue(typed["transfer"]); len(transfer) > 0 {
			return transfer
		}
		for _, child := range typed {
			if transfer := fleetTransferPayload(child); transfer != nil {
				return transfer
			}
		}
	case []any:
		for _, child := range typed {
			if transfer := fleetTransferPayload(child); transfer != nil {
				return transfer
			}
		}
	}
	return nil
}

func (s *Server) fleetTransferPairAllowed(source, destination string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.fleetTransferAllowlist[fleetTransferPairKey(source, destination)]
	return ok
}

func (s *Server) fleetTransferFileCall(r *http.Request, target string, args map[string]any) (map[string]any, int, error) {
	if r != nil {
		body, err := json.Marshal(map[string]any{"target": target, "tool": "file_transfer", "arguments": args})
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		peer := httptest.NewRecorder()
		routeTarget := "shell:" + strings.TrimPrefix(target, "file:")
		if s.forwardNodePeerCall(peer, r, routeTarget, body) {
			var response map[string]any
			if err := json.Unmarshal(peer.Body.Bytes(), &response); err != nil {
				return nil, http.StatusBadGateway, errors.New("node peer returned an invalid file transfer receipt")
			}
			if peer.Code >= http.StatusBadRequest || firstString(response, "status") == "failed" {
				return nil, peer.Code, fmt.Errorf("file transfer on %s failed: %s", target, firstString(response, "error", "detail"))
			}
			if payload := fleetTransferPayload(response); payload != nil {
				return payload, peer.Code, nil
			}
			return nil, http.StatusBadGateway, errors.New("node peer receipt has no file transfer payload")
		}
	}
	response, status := s.executeMCPTool(r, target, "file_transfer", args, false, s.cfg.DefaultTimeout, "")
	if status >= http.StatusBadRequest || firstString(response, "status") == "failed" {
		return nil, status, fmt.Errorf("file transfer on %s failed: %s", target, firstString(response, "error", "detail"))
	}
	if payload := fleetTransferPayload(response); payload != nil {
		return payload, status, nil
	}
	return nil, http.StatusBadGateway, fmt.Errorf("file transfer on %s returned no receipt", target)
}

func (s *Server) fleetTransferForRequest(r *http.Request, args map[string]any) (map[string]any, int) {
	r = requestWithInstructionContext(r, args)
	if failure := s.requireFacadeInstructionContext(r, fleetTransferToolName, args); failure != nil {
		return failure, http.StatusPreconditionRequired
	}
	args = withoutInstructionContext(args)
	if s.fleetTransferAllowlistErr != nil {
		return map[string]any{"status": "failed", "error": "fleet transfer allowlist is invalid"}, http.StatusServiceUnavailable
	}
	var req fleetTransferRequest
	raw, err := json.Marshal(args)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&req)
	var extra any
	trailingErr := decoder.Decode(&extra)
	if err != nil || decodeErr != nil || trailingErr != io.EOF {
		return map[string]any{"status": "failed", "error": "invalid fleet transfer arguments"}, http.StatusBadRequest
	}
	source, err := canonicalFleetFileTarget(req.Source)
	if err != nil {
		return map[string]any{"status": "failed", "error": err.Error()}, http.StatusBadRequest
	}
	destination, err := canonicalFleetFileTarget(req.Destination)
	if err != nil {
		return map[string]any{"status": "failed", "error": err.Error()}, http.StatusBadRequest
	}
	if source == destination {
		return map[string]any{"status": "failed", "error": "fleet transfer source and destination must differ"}, http.StatusBadRequest
	}
	if req.SourcePath == "" || req.DestinationPath == "" {
		return map[string]any{"status": "failed", "error": "source_path and destination_path are required"}, http.StatusBadRequest
	}
	if !s.fleetTransferPairAllowed(source, destination) {
		return map[string]any{"status": "failed", "error": "fleet transfer pair is not allowlisted", "source": source, "destination": destination}, http.StatusForbidden
	}
	if s.fileTransferVersion(source) < 3 || s.fileTransferVersion(destination) < 3 {
		return map[string]any{
			"status":      "failed",
			"error":       "fleet transfer requires file_transfer_version 3 on both peers",
			"source":      source,
			"destination": destination,
		}, http.StatusPreconditionFailed
	}

	receipt := map[string]any{
		"status":           "failed",
		"operation":        fleetTransferToolName,
		"source":           source,
		"destination":      destination,
		"source_path":      req.SourcePath,
		"destination_path": req.DestinationPath,
		"verified":         false,
	}
	phase := "source_begin"
	sourceBegin, status, err := s.fleetTransferFileCall(r, source, map[string]any{"action": "download_begin", "path": req.SourcePath})
	if err != nil {
		receipt["phase"] = phase
		receipt["error"] = err.Error()
		return receipt, transferFailureStatus(status)
	}
	sourceID := firstString(sourceBegin, "transfer_id")
	destinationID := ""
	destinationCommitted := false
	cleanup := func() {
		if destinationID != "" && !destinationCommitted {
			_, _, _ = s.fleetTransferFileCall(r, destination, map[string]any{"action": "upload_abort", "path": req.DestinationPath, "transfer_id": destinationID})
		}
		if sourceID != "" {
			_, _, _ = s.fleetTransferFileCall(r, source, map[string]any{"action": "download_abort", "path": req.SourcePath, "transfer_id": sourceID})
		}
	}
	defer cleanup()
	size, sizeOK := int64FromAny(sourceBegin["size"])
	digest := firstString(sourceBegin, "sha256")
	if sourceID == "" || !sizeOK || size < 0 || size > transferFileLimit || !validFleetTransferDigest(digest) {
		receipt["phase"] = phase
		receipt["error"] = "source download receipt is invalid"
		return receipt, http.StatusBadGateway
	}
	phase = "destination_begin"
	destinationBegin, status, err := s.fleetTransferFileCall(r, destination, map[string]any{
		"action": "upload_begin", "path": req.DestinationPath, "size": size, "sha256": digest,
	})
	if err != nil {
		receipt["phase"] = phase
		receipt["error"] = err.Error()
		return receipt, transferFailureStatus(status)
	}
	destinationID = firstString(destinationBegin, "transfer_id")
	if destinationID == "" || int64FromAnyValue(destinationBegin["size"]) != size || firstString(destinationBegin, "sha256") != digest {
		receipt["phase"] = phase
		receipt["error"] = "destination upload receipt is invalid"
		return receipt, http.StatusBadGateway
	}

	offset := int64(0)
	for offset < size {
		if r != nil {
			if err := r.Context().Err(); err != nil {
				receipt["phase"] = "transfer"
				receipt["error"] = err.Error()
				return receipt, http.StatusRequestTimeout
			}
		}
		phase = "download_chunk"
		chunk, status, err := s.fleetTransferFileCall(r, source, map[string]any{
			"action": "download_chunk", "path": req.SourcePath, "transfer_id": sourceID, "offset": offset, "max_bytes": transferChunkLimit,
		})
		if err != nil {
			receipt["phase"] = phase
			receipt["error"] = err.Error()
			return receipt, transferFailureStatus(status)
		}
		encoded := firstString(chunk, "data_base64")
		data, decodeErr := base64.StdEncoding.Strict().DecodeString(encoded)
		nextOffset, nextOK := int64FromAny(chunk["next_offset"])
		chunkDigest := firstString(chunk, "chunk_sha256")
		if decodeErr != nil || len(data) == 0 || len(data) > transferChunkLimit || !nextOK || nextOffset != offset+int64(len(data)) || nextOffset > size || !validFleetTransferDigest(chunkDigest) || fleetTransferDigest(data) != chunkDigest {
			receipt["phase"] = phase
			receipt["error"] = "source chunk receipt failed digest or offset verification"
			return receipt, http.StatusBadGateway
		}
		phase = "upload_chunk"
		uploaded, status, err := s.fleetTransferFileCall(r, destination, map[string]any{
			"action": "upload_chunk", "path": req.DestinationPath, "transfer_id": destinationID, "offset": offset,
			"data_base64": encoded, "sha256": chunkDigest,
		})
		if err != nil {
			receipt["phase"] = phase
			receipt["error"] = err.Error()
			return receipt, transferFailureStatus(status)
		}
		received, receivedOK := int64FromAny(uploaded["received"])
		if !receivedOK || received != nextOffset {
			receipt["phase"] = phase
			receipt["error"] = "destination chunk receipt has an unexpected offset"
			return receipt, http.StatusBadGateway
		}
		offset = nextOffset
	}

	phase = "upload_commit"
	committed, status, err := s.fleetTransferFileCall(r, destination, map[string]any{"action": "upload_commit", "path": req.DestinationPath, "transfer_id": destinationID})
	if err != nil {
		receipt["phase"] = phase
		receipt["error"] = err.Error()
		receipt["outcome"] = "unknown"
		return receipt, transferFailureStatus(status)
	}
	if !truthy(committed["completed"]) || int64FromAnyValue(committed["size"]) != size || firstString(committed, "sha256") != digest {
		receipt["phase"] = phase
		receipt["error"] = "destination commit receipt failed verification"
		return receipt, http.StatusBadGateway
	}
	destinationCommitted = true
	receipt["status"] = "completed"
	receipt["size"] = size
	receipt["sha256"] = digest
	receipt["verified"] = true
	receipt["source_transfer_id"] = sourceID
	receipt["destination_transfer_id"] = destinationID
	receipt["transport"] = "authenticated_hub"
	return receipt, http.StatusOK
}

func int64FromAnyValue(value any) int64 {
	result, ok := int64FromAny(value)
	if !ok {
		return -1
	}
	return result
}

func transferFailureStatus(status int) int {
	if status < http.StatusBadRequest {
		return http.StatusBadGateway
	}
	return status
}
