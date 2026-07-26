package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const schemaContractVersion = "gptadmin.mcp-schema/v1"

// withSchemaContractMetadata attaches a deterministic identity to a relay
// schema after policy filtering and action hints have been applied.
func withSchemaContractMetadata(result map[string]any) map[string]any {
	response := mapValue(result["response"])
	tools := mcpToolsFromResult(response)
	encoded, err := json.Marshal(tools)
	if err != nil {
		return result
	}
	response["schema_version"] = schemaContractVersion
	response["schema_digest_sha256"] = sha256Hex(encoded)
	result["response"] = response
	return result
}

func schemaContractFields(args map[string]any) (version, digest string, provided bool) {
	version = strings.TrimSpace(firstString(args, "schema_version"))
	digest = strings.TrimSpace(firstString(args, "schema_digest_sha256"))
	return version, digest, version != "" || digest != ""
}

// validateSchemaContract rejects an execute call that was built from a stale
// or incomplete schema. The metadata is optional for migration compatibility,
// but when supplied both fields must match the selected target's current view.
func (s *Server) validateSchemaContract(r *http.Request, target string, args map[string]any) (map[string]any, bool) {
	version, digest, provided := schemaContractFields(args)
	if !provided {
		return nil, false
	}
	if version == "" || digest == "" || version != schemaContractVersion {
		return map[string]any{
			"server_id": target,
			"status":    "failed",
			"error":     map[string]any{"code": "schema_mismatch", "message": "schema version and digest are required and must be current"},
		}, true
	}
	currentValue := s.appsSDKSchemaForRequest(r, map[string]any{"target": target})
	current, ok := currentValue.(map[string]any)
	if !ok {
		return map[string]any{
			"server_id": target,
			"status":    "failed",
			"error":     map[string]any{"code": "schema_mismatch", "message": "selected target returned no usable schema"},
		}, true
	}
	current = withSchemaContractMetadata(current)
	response := mapValue(current["response"])
	if firstString(response, "schema_version") != version || firstString(response, "schema_digest_sha256") != digest {
		return map[string]any{
			"server_id": target,
			"status":    "failed",
			"error": map[string]any{
				"code":    "schema_mismatch",
				"message": fmt.Sprintf("schema metadata does not match target %q", target),
			},
		}, true
	}
	return nil, false
}
