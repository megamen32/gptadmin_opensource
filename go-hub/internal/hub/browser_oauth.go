package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
)

// browserOAuthCallback hands an authorization code to the extension opener.
// The Hub never places an access token in this browser page; the extension
// exchanges the one-time code with PKCE over the OAuth token endpoint.
func (s *Server) browserOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	message := map[string]string{
		"type":  "gptadmin-oauth-callback",
		"code":  r.URL.Query().Get("code"),
		"state": r.URL.Query().Get("state"),
	}
	messageJSON, err := json.Marshal(message)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "failed to render OAuth callback"})
		return
	}
	var escapedMessage bytes.Buffer
	json.HTMLEscape(&escapedMessage, messageJSON)
	originJSON, err := json.Marshal(s.origin(r))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": "failed to render OAuth origin"})
		return
	}
	var escapedOrigin bytes.Buffer
	json.HTMLEscape(&escapedOrigin, originJSON)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; base-uri 'none'")
	page := `<!doctype html><html><head><meta charset="utf-8"><title>GPTAdmin connection</title></head><body><p>Connection completed. You may close this window.</p><script>(function(){const message=` + escapedMessage.String() + `;if(window.opener){window.opener.postMessage(message,` + escapedOrigin.String() + `);}})();</script></body></html>`
	_, _ = w.Write([]byte(page))
}
