package ws

import (
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strings"
)

// HUBWSURL converts an http(s)://hub base URL into its ws(s):// equivalent
// for the websocket transport. This mirrors the Python _hub_ws_url() helper
// in /home/roomhacker/gptadmin/client/shellmcp.py around line 870.
//
// http://x.y/z  -> ws://x.y/z
// https://x.y/z -> wss://x.y/z
//
// Returns "" if rawURL is empty.
func HUBWSURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" {
		return ""
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return rawURL // already ws/wss or other
	}
	return u.String()
}

// decodeAck parses a hello ack envelope and writes the result into out.
func decodeAck(b []byte, out any) error {
	return json.Unmarshal(b, out)
}

// decodeEnvelope parses a hub envelope from a raw JSON frame.
func decodeEnvelope(b []byte) (envelope, error) {
	var env envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return env, err
	}
	return env, nil
}

// writeJSON marshals v to JSON and writes it as a text frame.
func writeJSON(c *Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.WriteMessage(OpcodeText, b)
}

// mustJSON marshals v or returns a tiny {"error":"json marshal failed"}.
// Used in error fallbacks; we never want to panic from a bad payload.
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"error":"json marshal failed"}`)
	}
	return b
}

// jsonRaw lets us embed already-marshalled JSON inside another envelope.
type jsonRaw []byte

func (r jsonRaw) MarshalJSON() ([]byte, error) {
	if len(r) == 0 {
		return []byte("null"), nil
	}
	return []byte(r), nil
}

// isJSON returns true if b looks like a JSON object or array. Cheap
// sniff only — proper parsing is done via encoding/json.
func isJSON(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		return c == '{' || c == '['
	}
	return false
}

// isTimeoutErr reports whether err looks like a network read/write
// deadline expiry. Both net.Error timeout returns and context.DeadlineExceeded
// are treated as "no data yet, keep looping".
func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return ne.Timeout()
	}
	return errors.Is(err, net.ErrClosed)
}