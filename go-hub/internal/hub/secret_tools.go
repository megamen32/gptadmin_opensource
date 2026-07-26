package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

func secretOwnerFingerprint(r *http.Request) string {
	if r == nil {
		return ""
	}
	if claims, ok := r.Context().Value(authClaimsContextKey{}).(map[string]any); ok {
		if subject := firstString(claims, "sub", "client_id"); subject != "" {
			return secretOwnerFingerprintValue("claim:" + subject)
		}
	}
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authorization), "bearer ") {
		if token := strings.TrimSpace(authorization[7:]); token != "" {
			return secretOwnerFingerprintValue("bearer:" + token)
		}
	}
	return ""
}

func secretOwnerFingerprintValue(value string) string {
	digest := sha256.Sum256([]byte("gptadmin-secret-owner\x00" + value))
	return hex.EncodeToString(digest[:])
}

func (s *Server) secretToolForRequest(r *http.Request, name string, args map[string]any) any {
	if s.secretStoreErr != nil || s.secretStore == nil {
		return map[string]any{"status": "failed", "error": "secret input is unavailable"}
	}
	owner := secretOwnerFingerprint(r)
	if owner == "" {
		return map[string]any{"status": "failed", "error": "authenticated secret owner is required"}
	}
	switch name {
	case "secret_request":
		return s.secretRequestForOwner(r, owner, args)
	case "secret_status":
		return s.secretStatusForOwner(owner, args)
	default:
		return map[string]any{"status": "failed", "error": "unknown secret operation"}
	}
}

func (s *Server) secretRequestForOwner(r *http.Request, owner string, args map[string]any) map[string]any {
	for key := range args {
		if key != "label" && key != "env_name" && key != "ttl_seconds" {
			return map[string]any{"status": "failed", "error": "secret_request accepts only label, env_name and ttl_seconds"}
		}
	}
	label := firstString(args, "label")
	envName := firstString(args, "env_name")
	if envName == "" {
		envName = "GPTADMIN_SECRET"
	}
	ttl := s.cfg.SecretIngressTTL
	if seconds := intFromAny(args["ttl_seconds"]); seconds != 0 {
		ttl = time.Duration(seconds) * time.Second
	}
	request, token, err := s.secretStore.CreateRequest(owner, label, envName, ttl)
	if err != nil {
		return map[string]any{"status": "failed", "error": err.Error()}
	}
	return map[string]any{
		"status":     request.Status,
		"request_id": request.Ref,
		"input_url":  strings.TrimRight(s.origin(r), "/") + "/secret-input/" + token,
		"secret_ref": request.Ref,
		"env_name":   request.EnvName,
		"file":       filepath.Join(s.cfg.SecretStoreDir, request.Ref+".json"),
		"expires_at": request.ExpiresAt,
	}
}

func (s *Server) secretStatusForOwner(owner string, args map[string]any) map[string]any {
	for key := range args {
		if key != "secret_ref" {
			return map[string]any{"status": "failed", "error": "secret_status accepts only secret_ref"}
		}
	}
	ref := firstString(args, "secret_ref")
	if ref == "" {
		return map[string]any{"status": "failed", "error": "secret_ref is required"}
	}
	secret, err := s.secretStore.Status(ref, owner)
	if err != nil {
		return map[string]any{"status": "failed", "error": err.Error()}
	}
	return map[string]any{
		"status":     secret.Status,
		"secret_ref": secret.Ref,
		"env_name":   secret.EnvName,
		"file":       filepath.Join(s.cfg.SecretStoreDir, secret.Ref+".json"),
		"expires_at": secret.ExpiresAt,
	}
}

func (s *Server) resolveSecretEnvForRequest(r *http.Request, target string, args map[string]any) (map[string]any, []string, error) {
	cloned := cloneMap(args)
	raw, present := args["secret_env"]
	if !present {
		return cloned, nil, nil
	}
	if !strings.HasPrefix(target, "shell:") {
		return nil, nil, ErrSecretNotFound
	}
	secretEnv, ok := raw.(map[string]any)
	if !ok {
		return nil, nil, ErrSecretInvalidEnvironment
	}
	owner := secretOwnerFingerprint(r)
	if owner == "" || s.secretStore == nil || s.secretStoreErr != nil {
		return nil, nil, ErrSecretNotFound
	}
	env := cloneMap(mapValue(args["env"]))
	values := make([]string, 0, len(secretEnv))
	for envName, rawRef := range secretEnv {
		if !validSecretEnvName.MatchString(envName) {
			return nil, nil, ErrSecretInvalidEnvironment
		}
		ref, ok := rawRef.(string)
		if !ok || strings.TrimSpace(ref) == "" {
			return nil, nil, ErrSecretNotFound
		}
		value, err := s.secretStore.Resolve(ref, owner)
		if err != nil {
			return nil, nil, err
		}
		env[envName] = value
		values = append(values, value)
	}
	delete(cloned, "secret_env")
	cloned["env"] = env
	return cloned, values, nil
}

func redactSecretValues(value any, secrets []string) any {
	switch current := value.(type) {
	case string:
		for _, secret := range secrets {
			if secret != "" {
				current = strings.ReplaceAll(current, secret, "***")
			}
		}
		return current
	case []any:
		out := make([]any, len(current))
		for i, item := range current {
			out[i] = redactSecretValues(item, secrets)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(current))
		for key, item := range current {
			out[key] = redactSecretValues(item, secrets)
		}
		return out
	default:
		return value
	}
}

func secretHubTools() []map[string]any {
	return []map[string]any{
		{
			"name":        "secret_request",
			"description": "Create a short-lived one-time browser input link. The value must never be sent through MCP.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"label":       map[string]any{"type": "string", "minLength": 1},
				"env_name":    map[string]any{"type": "string"},
				"ttl_seconds": map[string]any{"type": "integer", "minimum": 60, "maximum": 3600},
			}, "required": []string{"label"}, "additionalProperties": false},
			"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": true, "openWorldHint": false},
		},
		{
			"name":        "secret_status",
			"description": "Read secret request status and metadata without returning the value.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"secret_ref": map[string]any{"type": "string", "minLength": 1}}, "required": []string{"secret_ref"}, "additionalProperties": false},
			"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": true, "openWorldHint": false},
		},
	}
}

func secretAppsTools() []map[string]any {
	readExec := []map[string]any{{"type": "oauth2", "scopes": []string{"gptadmin.read", "gptadmin.exec"}}}
	tools := secretHubTools()
	for _, tool := range tools {
		tool["title"] = tool["name"]
		tool["outputSchema"] = map[string]any{"type": "object", "additionalProperties": true}
		tool["securitySchemes"] = readExec
		tool["_meta"] = map[string]any{
			"securitySchemes":                readExec,
			"openai/toolInvocation/invoking": "Waiting for secure input…",
			"openai/toolInvocation/invoked":  "Secure input request ready.",
		}
	}
	return tools
}
