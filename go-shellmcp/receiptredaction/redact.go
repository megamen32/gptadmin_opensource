// Package receiptredaction applies GPTAdmin's literal and binary stdout
// redaction to JSON output before it crosses a receipt storage boundary.
package receiptredaction

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

const Version = 1

// Policy contains the names of environment variables already delivered to
// the physical executor. Their plaintext values stay within that executor.
type Policy struct {
	Version  int      `json:"version"`
	EnvNames []string `json:"env_names"`
}

func (p Policy) Result(value any, env map[string]string) (any, error) {
	if p.Version != Version || len(p.EnvNames) == 0 {
		return nil, errors.New("unsupported receipt redaction policy")
	}
	secrets := make([]string, 0, len(p.EnvNames))
	for _, key := range p.EnvNames {
		v, ok := env[key]
		if !ok || v == "" {
			return nil, errors.New("receipt redaction environment unavailable")
		}
		secrets = append(secrets, v)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var normalized any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err = dec.Decode(&normalized); err != nil {
		return nil, err
	}
	return SanitizedResult(normalized, secrets), nil
}

// SanitizedResult also removes indirect references to raw spilled bytes.
func SanitizedResult(value any, secrets []string) any {
	result := Values(value, secrets)
	if m, ok := result.(map[string]any); ok && m["_spilled"] == true {
		// The remote receipt may include a sanitized inline preview. The original
		// spill can contain raw secret bytes and must not become a read resource.
		delete(m, "stdout_path")
		delete(m, "stderr_path")
		delete(m, "files")
	}
	return result
}

func Values(value any, secrets []string) any {
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
			out[i] = Values(item, secrets)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(current))
		for key, item := range current {
			if key == "stdout_base64" && len(secrets) > 0 {
				// Binary stdout crosses the same disclosure/persistence boundary
				// as text. Redact decoded bytes before restoring the encoding.
				encoded, ok := item.(string)
				if !ok {
					continue
				}
				raw, err := base64.StdEncoding.DecodeString(encoded)
				if err != nil {
					// Omit malformed data whose redaction cannot be verified.
					continue
				}
				redacted := Values(string(raw), secrets).(string)
				encoded = base64.StdEncoding.EncodeToString([]byte(redacted))
				// The encoding itself must also obey the existing literal-secret rule.
				if Values(encoded, secrets).(string) != encoded {
					continue
				}
				out[key] = encoded
				continue
			}
			out[key] = Values(item, secrets)
		}
		return out
	default:
		return value
	}
}

// PayloadVersion reads only the leading metadata field written by TaskResult.
// Old outboxes can have large output immediately after the ID.
func PayloadVersion(raw []byte) int {
	d := json.NewDecoder(bytes.NewReader(raw))
	t, e := d.Token()
	if e != nil || t != json.Delim('{') {
		return 0
	}
	t, e = d.Token()
	if e != nil || t != "receipt_redaction_version" {
		return 0
	}
	var v int
	if d.Decode(&v) != nil || v != Version {
		return 0
	}
	return v
}
