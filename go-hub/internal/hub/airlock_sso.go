package hub

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// airlockUIPrincipal admits one short native request via the configured private
// UI gateway. It never creates a password cookie or a fabricated user JWT.
type airlockUIPrincipal struct {
	AppID   string `json:"app_id"`
	UserID  string `json:"user_id"`
	Email   string `json:"email"`
	Role    string `json:"role"`
	Host    string `json:"host"`
	Method  string `json:"method"`
	URI     string `json:"uri"`
	Expires int64  `json:"expires"`
}

func (s *Server) airlockUIIdentity(r *http.Request) (airlockUIPrincipal, bool) {
	var p airlockUIPrincipal
	if r == nil {
		return p, false
	}
	values := r.Header.Values("X-GPTAdmin-Airlock-Identity")
	if len(values) != 1 || len(values[0]) > 16384 {
		return p, false
	}
	// The only enabled sender is the host-local UI gateway; public ingress
	// clients do not gain authority merely by supplying the same header name.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return p, false
	}
	file := os.Getenv("GPTADMIN_AIRLOCK_UI_KEY_FILE")
	if file == "" {
		return p, false
	}
	key, err := os.ReadFile(file)
	if err != nil || len(strings.TrimSpace(string(key))) < 32 {
		return p, false
	}
	parts := strings.Split(values[0], ".")
	if len(parts) != 2 {
		return p, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return p, false
	}
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(string(key))))
	mac.Write([]byte(parts[0]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return p, false
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return p, false
	}
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil {
		return p, false
	}
	now := time.Now().Unix()
	if p.AppID != "178f37c3-283d-48b9-82ef-03775e772438" || p.Host != "gptadmin.airlock.bezrabotnyi.com" || p.Role != "admin" || p.UserID == "" || p.Email == "" || p.Method != r.Method || p.URI != r.URL.RequestURI() || p.Expires < now || p.Expires > now+60 {
		return airlockUIPrincipal{}, false
	}
	return p, true
}
