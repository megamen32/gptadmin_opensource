package security

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Identity struct {
	Name        string             `json:"name"`
	ServerID    string             `json:"server_id"`
	PublicKey   string             `json:"public_key"`
	Fingerprint string             `json:"fingerprint"`
	PrivateKey  ed25519.PrivateKey `json:"-"`
}

func LoadIdentity(dir, name string) (*Identity, error) {
	if name == "" {
		if h, err := os.Hostname(); err == nil {
			name = h
		}
	}
	keyPath := filepath.Join(dir, "shellmcp_ed25519")
	pubPath := filepath.Join(dir, "shellmcp_ed25519.pub")
	identPath := filepath.Join(dir, "shellmcp_identity.json")
	if dir == "" {
		return nil, fmt.Errorf("identity dir is empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	pemBytes, err := os.ReadFile(keyPath)
	var priv ed25519.PrivateKey
	if err != nil {
		_, generated, genErr := ed25519.GenerateKey(rand.Reader)
		if genErr != nil {
			return nil, genErr
		}
		priv = generated
		pkcs8, marshalErr := x509.MarshalPKCS8PrivateKey(priv)
		if marshalErr != nil {
			return nil, marshalErr
		}
		pemBytes = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
		if writeErr := os.WriteFile(keyPath, pemBytes, 0o600); writeErr != nil {
			return nil, writeErr
		}
	} else {
		block, _ := pem.Decode(pemBytes)
		if block == nil {
			return nil, fmt.Errorf("invalid PEM private key %s", keyPath)
		}
		keyAny, parseErr := x509.ParsePKCS8PrivateKey(block.Bytes)
		if parseErr != nil {
			return nil, parseErr
		}
		parsed, ok := keyAny.(ed25519.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("private key is %T, not ed25519", keyAny)
		}
		priv = parsed
	}

	ident := Identity{Name: name, PrivateKey: priv}
	if b, err := os.ReadFile(identPath); err == nil {
		_ = json.Unmarshal(b, &ident)
	}
	if ident.Name == "" {
		ident.Name = name
	}
	pub := priv.Public().(ed25519.PublicKey)
	ident.PublicKey = B64(pub)
	ident.Fingerprint = Fingerprint(ident.PublicKey)
	if ident.ServerID == "" {
		ident.ServerID = "go-shellmcp-" + randomHex(8)
	}

	_ = os.WriteFile(pubPath, []byte(ident.PublicKey+"\n"), 0o644)
	identJSON, _ := json.MarshalIndent(map[string]any{
		"created_at":  time.Now().Unix(),
		"fingerprint": ident.Fingerprint,
		"name":        ident.Name,
		"public_key":  ident.PublicKey,
		"server_id":   ident.ServerID,
	}, "", "  ")
	_ = os.WriteFile(identPath, append(identJSON, '\n'), 0o600)
	return &ident, nil
}

func LoadPublicKey(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func B64(b []byte) string { return strings.TrimRight(base64.URLEncoding.EncodeToString(b), "=") }
func B64Decode(s string) ([]byte, error) {
	if m := len(s) % 4; m != 0 {
		s += strings.Repeat("=", 4-m)
	}
	return base64.URLEncoding.DecodeString(s)
}
func Fingerprint(publicKeyB64 string) string {
	b, _ := B64Decode(publicKeyB64)
	h := sha256.Sum256(b)
	return "SHA256:" + B64(h[:])
}
func Canonical(method, path, ts, nonce string, body []byte) []byte {
	h := sha256.Sum256(body)
	return []byte(fmt.Sprintf("%s\n%s\n%s\n%s\n%s", strings.ToUpper(method), path, ts, nonce, hex.EncodeToString(h[:])))
}
func (i *Identity) Sign(method, path string, body []byte) map[string]string {
	ts := fmt.Sprintf("%d", time.Now().Unix())
	nonce := B64(randomBytes(18))
	sig := ed25519.Sign(i.PrivateKey, Canonical(method, path, ts, nonce, body))
	return map[string]string{"X-GPTAdmin-Timestamp": ts, "X-GPTAdmin-Nonce": nonce, "X-GPTAdmin-Signature": B64(sig), "X-GPTAdmin-Server": i.Name, "X-GPTAdmin-Server-ID": i.ServerID}
}

// Verify checks the cryptographic signature and timestamp skew of a signed
// request. TODO: integrate NonceCache (internal/security/nonce.go) here to
// reject replayed nonces within the configured TTL window.
func Verify(publicKeyB64, method, path, ts, nonce string, body []byte, sigB64 string, maxSkew time.Duration) error {
	if publicKeyB64 == "" || ts == "" || nonce == "" || sigB64 == "" {
		return fmt.Errorf("missing signed request fields")
	}
	tsInt, err := strconvParseInt(ts)
	if err != nil {
		return fmt.Errorf("bad timestamp: %w", err)
	}
	if maxSkew > 0 && time.Since(time.Unix(tsInt, 0)) > maxSkew || maxSkew > 0 && time.Until(time.Unix(tsInt, 0)) > maxSkew {
		return fmt.Errorf("signature timestamp outside allowed skew")
	}
	pubBytes, err := B64Decode(publicKeyB64)
	if err != nil {
		return err
	}
	sig, err := B64Decode(sigB64)
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(pubBytes), Canonical(method, path, ts, nonce, body), sig) {
		return fmt.Errorf("invalid signature")
	}
	return nil
}

func strconvParseInt(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscan(s, &n)
	return n, err
}

func randomBytes(n int) []byte { b := make([]byte, n); _, _ = rand.Read(b); return b }
func randomHex(n int) string   { return hex.EncodeToString(randomBytes(n)) }
