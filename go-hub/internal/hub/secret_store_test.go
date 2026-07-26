package hub

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSecretStoreCreatesSingleUseRequestAndEncryptedRecord(t *testing.T) {
	store := newTestSecretStore(t)
	request, rawToken, err := store.CreateRequest("owner-a", "OpenAI key", "OPENAI_API_KEY", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_ = request
	ref, err := store.ConsumeRequest(rawToken, "value-that-must-not-be-printed")
	if err != nil {
		t.Fatal(err)
	}
	if ref.EnvName != "OPENAI_API_KEY" || ref.Status != "ready" {
		t.Fatalf("unexpected ref metadata: env=%q status=%q", ref.EnvName, ref.Status)
	}
	if _, err := store.ConsumeRequest(rawToken, "second-use"); !errors.Is(err, ErrSecretRequestConsumed) {
		t.Fatalf("second use error = %v", err)
	}
	got, err := store.Resolve(ref.Ref, "owner-a")
	if err != nil {
		t.Fatalf("resolve error = %v", err)
	}
	if got != "value-that-must-not-be-printed" {
		t.Fatal("resolved value did not match submitted value")
	}
	for _, path := range store.filesForTest() {
		if strings.Contains(string(mustReadFile(t, path)), "value-that-must-not-be-printed") {
			t.Fatalf("plaintext leaked to %s", path)
		}
	}
}

func TestSecretStoreRejectsExpiredAndWrongOwnerRequests(t *testing.T) {
	clock := newTestClock(time.Unix(100, 0))
	store := newTestSecretStoreWithClock(t, clock)
	request, token, err := store.CreateRequest("owner-a", "key", "KEY", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Status(request.Ref, "owner-b"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("wrong owner status error = %v", err)
	}
	clock.Advance(61 * time.Second)
	if _, err := store.ConsumeRequest(token, "expired"); !errors.Is(err, ErrSecretRequestExpired) {
		t.Fatalf("expired request error = %v", err)
	}
}

func TestSecretStoreFailsClosedWhenReadyRecordIsMissing(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	storeDir := filepath.Join(root, "secrets")
	keyFile := filepath.Join(configDir, "secret-store.key")
	store, err := NewSecretStore(configDir, storeDir, keyFile, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	request, token, err := store.CreateRequest("owner-a", "key", "KEY", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeRequest(token, "value"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.recordPath(request.Ref)); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSecretStore(configDir, storeDir, keyFile, time.Now); !errors.Is(err, ErrSecretStoreCorrupt) {
		t.Fatalf("missing ready record error=%v, want ErrSecretStoreCorrupt", err)
	}
}

func TestSecretStoreRestoresPrivateFileModesOnLoad(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	storeDir := filepath.Join(root, "secrets")
	keyFile := filepath.Join(configDir, "secret-store.key")
	store, err := NewSecretStore(configDir, storeDir, keyFile, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	request, token, err := store.CreateRequest("owner-a", "key", "KEY", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeRequest(token, "value"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.stateFile, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.recordPath(request.Ref), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSecretStore(configDir, storeDir, keyFile, time.Now); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{keyFile, store.stateFile, store.recordPath(request.Ref)} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("%s mode=%#o, want 0600", path, got)
		}
	}
}

type testClock struct {
	now time.Time
}

func newTestClock(now time.Time) *testClock {
	return &testClock{now: now}
}

func (c *testClock) Now() time.Time {
	return c.now
}

func (c *testClock) Advance(delta time.Duration) {
	c.now = c.now.Add(delta)
}

func newTestSecretStore(t *testing.T) *SecretStore {
	t.Helper()
	return newTestSecretStoreWithClock(t, newTestClock(time.Unix(100, 0)))
}

func newTestSecretStoreWithClock(t *testing.T, clock *testClock) *SecretStore {
	t.Helper()
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	storeDir := filepath.Join(root, "secrets")
	keyFile := filepath.Join(configDir, "secret-store.key")
	store, err := NewSecretStore(configDir, storeDir, keyFile, clock.Now)
	if err != nil {
		t.Fatalf("new secret store: %v", err)
	}
	return store
}

func (s *SecretStore) filesForTest() []string {
	entries, err := os.ReadDir(s.storeDir)
	if err != nil {
		return []string{s.keyFile, s.stateFile}
	}
	paths := []string{s.keyFile, s.stateFile}
	for _, entry := range entries {
		if !entry.IsDir() {
			paths = append(paths, filepath.Join(s.storeDir, entry.Name()))
		}
	}
	return paths
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}
