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
	keyPath := filepath.Join(dir, "shellmcp_ed25519")
	identPath := filepath.Join(dir, "shellmcp_identity.json")
	pemBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("invalid PEM private key %s", keyPath)
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	priv, ok := keyAny.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is %T, not ed25519", keyAny)
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
