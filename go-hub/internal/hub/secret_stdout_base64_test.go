package hub

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestShellResultBase64SecretRedaction(t *testing.T) {
	const fakeSecret = "fixture-only-secret-20261108"
	// Exercise both the public receipt and the persistence boundary using
	// disposable data. No credential store or real shell command is involved.
	boundaries := []struct {
		name string
		read func(*shellJob) map[string]any
	}{
		{"public_receipt", func(job *shellJob) map[string]any {
			response := mapValue(shellJobResponse(job)["response"])
			return mapValue(mapValue(response["structuredContent"])["result"])
		}},
		{"persisted_result", func(job *shellJob) map[string]any {
			return mapValue(persistableShellResult(job))
		}},
	}
	for _, boundary := range boundaries {
		t.Run(boundary.name, func(t *testing.T) {
			for _, output := range []struct {
				name string
				raw  []byte
			}{
				{"ansi", []byte("\x1b[31mprefix:" + fakeSecret + " suffix:" + fakeSecret + "\x1b[0m")},
				{"invalid_utf8", append([]byte{0xff, 0xfe}, []byte("prefix:"+fakeSecret+"\x00suffix:"+fakeSecret)...)},
			} {
				t.Run(output.name, func(t *testing.T) {
					encoded := base64.StdEncoding.EncodeToString(output.raw)
					for _, test := range []struct {
						name    string
						secrets []string
						want    []byte
					}{
						{"matched_secret", []string{fakeSecret}, bytes.ReplaceAll(output.raw, []byte(fakeSecret), []byte("***"))},
						{"no_secrets", nil, output.raw},
						{"unrelated_secret", []string{"different-fixture-secret"}, output.raw},
					} {
						t.Run(test.name, func(t *testing.T) {
							job := &shellJob{Server: "fixture", Status: "completed", SecretValues: test.secrets,
								Result: map[string]any{"stdout": string(output.raw), "stdout_base64": encoded}}
							result := boundary.read(job)
							value, ok := result["stdout_base64"].(string)
							if !ok {
								t.Fatal("valid binary stdout disappeared")
							}
							raw, err := base64.StdEncoding.DecodeString(value)
							if err != nil {
								t.Fatal("binary stdout is no longer valid base64")
							}
							if test.name == "matched_secret" {
								if bytes.Contains(raw, []byte(fakeSecret)) {
									t.Fatal("decoded stdout_base64 still contains the fixture secret")
								}
								if strings.Contains(firstString(result, "stdout"), fakeSecret) {
									t.Fatal("plain stdout still contains the fixture secret")
								}
							}
							if !bytes.Equal(raw, test.want) {
								t.Fatal("redaction changed unrelated stdout bytes")
							}
							if test.name == "no_secrets" && value != encoded {
								t.Fatal("stdout_base64 changed without secret values")
							}
						})
					}
				})
			}
			t.Run("encoded_text_matches_secret", func(t *testing.T) {
				raw := append([]byte("\x1b[31m"+strings.Repeat("ABC", 8)), 0xff)
				encodedSecret := base64.StdEncoding.EncodeToString(raw)
				job := &shellJob{Server: "fixture", Status: "completed", SecretValues: []string{encodedSecret},
					Result: map[string]any{"stdout": string(raw), "stdout_base64": encodedSecret}}
				if _, present := boundary.read(job)["stdout_base64"]; present {
					t.Fatal("encoded stdout must not expose a literal registered secret")
				}
			})
			for _, malformed := range []struct {
				name  string
				value any
			}{
				{"invalid_encoding", base64.StdEncoding.EncodeToString([]byte(fakeSecret)) + "%"},
				{"non_string", []any{base64.StdEncoding.EncodeToString([]byte(fakeSecret))}},
			} {
				t.Run(malformed.name, func(t *testing.T) {
					job := &shellJob{Server: "fixture", Status: "completed", SecretValues: []string{fakeSecret},
						Result: map[string]any{"stdout": fakeSecret, "stdout_base64": malformed.value}}
					result := boundary.read(job)
					if _, present := result["stdout_base64"]; present {
						t.Fatal("malformed binary stdout must be omitted when secret redaction cannot be verified")
					}
					if strings.Contains(firstString(result, "stdout"), fakeSecret) {
						t.Fatal("plain stdout still contains the fixture secret")
					}
				})
			}
		})
	}
}
