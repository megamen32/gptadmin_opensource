package receiptredaction

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestResultRedactsBeforeSerialization(t *testing.T) {
	const secret = "synthetic-private-value"
	data := map[string]any{
		"stdout": secret, "stderr": "prefix" + secret, "stdout_base64": base64.StdEncoding.EncodeToString([]byte("binary" + secret)),
		"nested": []any{map[string]any{"detail": secret}}, "_spilled": true, "stdout_path": "/private/file", "files": []string{"/private/file"}}
	p := Policy{Version: Version, EnvNames: []string{"PRIVATE"}}
	v, err := p.Result(data, map[string]string{"PRIVATE": secret})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "private/file") {
		t.Fatal("raw data or spill reference survived")
	}
	m := v.(map[string]any)
	if m["stdout"] != "***" || m["stderr"] != "prefix***" || m["stdout_base64"] != base64.StdEncoding.EncodeToString([]byte("binary***")) {
		t.Fatal("redacted output mismatch")
	}
	if v, err = p.Result(data, nil); err == nil || v != nil {
		t.Fatal("missing policy value allowed raw output")
	}
}
func TestPayloadVersionNegotiation(t *testing.T) {
	for _, v := range []string{`{"id":"older","result":{"receipt_redaction_version":1}}`, `{"receipt_redaction_version":2}`, `{"result":"raw","receipt_redaction_version":1}`} {
		if PayloadVersion([]byte(v)) != 0 {
			t.Fatalf("bad proof accepted %s", v)
		}
	}
	if PayloadVersion([]byte(`{"receipt_redaction_version":1,"id":"new","result":{}}`)) != Version {
		t.Fatal("canonical redaction metadata not accepted")
	}
}
