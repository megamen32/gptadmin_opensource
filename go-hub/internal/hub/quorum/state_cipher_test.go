package quorum

import (
	"crypto/aes"
	"crypto/cipher"
	"strings"
	"testing"
)

func TestExternalMutationCipherBindsIdentityAndHidesPayload(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	original := Mutation{Namespace: "task_records/shell", Key: "task-1", External: true,
		Record: Record{Revision: 7, Value: []byte(`{"command":"private-marker"}`)}}
	sealed, err := sealMutation(aead, "cluster-a", original)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed.Record.Value), "private-marker") {
		t.Fatal("external state payload appeared in the Raft command in plaintext")
	}
	opened, err := openMutation(aead, "cluster-a", sealed)
	if err != nil {
		t.Fatal(err)
	}
	if string(opened.Record.Value) != string(original.Record.Value) {
		t.Fatalf("decrypted payload mismatch: %s", opened.Record.Value)
	}
	if _, err := openMutation(aead, "cluster-b", sealed); err == nil {
		t.Fatal("encrypted row authenticated under a different cluster identity")
	}
	sealed.Key = "another-task"
	if _, err := openMutation(aead, "cluster-a", sealed); err == nil {
		t.Fatal("encrypted row authenticated under a different row key")
	}
}
