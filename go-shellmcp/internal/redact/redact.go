// Package redact removes recognizable credentials before diagnostic data is
// returned to an MCP client.
package redact

import (
	"regexp"
	"strings"
)

var (
	privateKeyRE = regexp.MustCompile(`(?s)-----BEGIN(?: [A-Z0-9]+)? PRIVATE KEY-----.*?-----END(?: [A-Z0-9]+)? PRIVATE KEY-----`)
	bearerRE     = regexp.MustCompile(`(?i)\bBearer\s+[^\s,;]+`)
	cookieRE     = regexp.MustCompile(`(?im)^(Set-Cookie|Cookie)(\s*:\s*).*$`)
	credentialRE = regexp.MustCompile(`(?im)\b(api[_-]?key|access[_-]?token|refresh[_-]?token|token|secret|password|passwd)(\s*[:=]\s*)([^\s;&]+)`)
	jwtRE        = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`)
)

// Secrets returns text with common credential formats replaced by typed
// markers. It intentionally preserves surrounding diagnostic context.
func Secrets(value string) string {
	value = privateKeyRE.ReplaceAllString(value, "<redacted:private-key>")
	value = bearerRE.ReplaceAllString(value, "Bearer <redacted:bearer>")
	value = cookieRE.ReplaceAllString(value, "$1$2<redacted:cookie>")
	value = credentialRE.ReplaceAllStringFunc(value, redactCredential)
	return jwtRE.ReplaceAllString(value, "<redacted:jwt>")
}

func redactCredential(value string) string {
	parts := credentialRE.FindStringSubmatch(value)
	if len(parts) != 4 {
		return "<redacted:credential>"
	}
	kind := "secret"
	normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(parts[1]))
	switch {
	case normalized == "apikey":
		kind = "api-key"
	case strings.Contains(normalized, "password") || strings.Contains(normalized, "passwd"):
		kind = "password"
	case strings.Contains(normalized, "token"):
		kind = "token"
	}
	return parts[1] + parts[2] + "<redacted:" + kind + ">"
}
