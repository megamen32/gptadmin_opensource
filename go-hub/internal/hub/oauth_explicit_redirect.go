package hub

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// Explicit callbacks are exact operator-provisioned URLs, never host suffixes
// or wildcard patterns. Reject the entire configuration if any entry is bad.
func validExplicitOAuthRedirect(raw string) bool {
	if raw == "" || len(raw) > 2048 || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\r\n\t#") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return true
}

func parseExplicitOAuthRedirects(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var values []string
	if json.Unmarshal([]byte(raw), &values) != nil || len(values) > 64 {
		return nil
	}
	for _, value := range values {
		if !validExplicitOAuthRedirect(value) {
			return nil
		}
	}
	return values
}
