package quorum

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

type sealedMutationValue struct {
	Version    int    `json:"version"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

func loadStateAEAD(path string) (cipher.AEAD, error) {
	if path == "" {
		return nil, errors.New("quorum: state key file is required for external state")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("quorum: inspect state key file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("quorum: state key file must be a regular owner-only file")
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("quorum: read state key file: %w", err)
	}
	if len(key) != 32 {
		for i := range key {
			key[i] = 0
		}
		return nil, errors.New("quorum: state key must be exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	for i := range key {
		key[i] = 0
	}
	if err != nil {
		return nil, fmt.Errorf("quorum: initialize state cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("quorum: initialize state authentication: %w", err)
	}
	return aead, nil
}

func sealMutation(aead cipher.AEAD, clusterID string, mutation Mutation) (Mutation, error) {
	if mutation.Record.Deleted {
		return mutation, nil
	}
	if aead == nil {
		return Mutation{}, errors.New("quorum: external mutation encryption is not configured")
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Mutation{}, err
	}
	ciphertext := aead.Seal(nil, nonce, mutation.Record.Value, mutationAAD(clusterID, mutation))
	envelope, err := json.Marshal(sealedMutationValue{
		Version: 1, Nonce: base64.RawStdEncoding.EncodeToString(nonce),
		Ciphertext: base64.RawStdEncoding.EncodeToString(ciphertext),
	})
	if err != nil {
		return Mutation{}, err
	}
	mutation.Record.Value = envelope
	return mutation, nil
}

func openMutation(aead cipher.AEAD, clusterID string, mutation Mutation) (Mutation, error) {
	if mutation.Record.Deleted {
		return mutation, nil
	}
	if aead == nil {
		return Mutation{}, errors.New("quorum: external mutation decryption is not configured")
	}
	var envelope sealedMutationValue
	if err := json.Unmarshal(mutation.Record.Value, &envelope); err != nil || envelope.Version != 1 {
		return Mutation{}, errors.New("quorum: invalid encrypted external mutation")
	}
	nonce, err := base64.RawStdEncoding.DecodeString(envelope.Nonce)
	if err != nil || len(nonce) != aead.NonceSize() {
		return Mutation{}, errors.New("quorum: invalid encrypted mutation nonce")
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return Mutation{}, errors.New("quorum: invalid encrypted mutation payload")
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, mutationAAD(clusterID, mutation))
	if err != nil {
		return Mutation{}, errors.New("quorum: external mutation authentication failed")
	}
	mutation.Record.Value = plaintext
	return mutation, nil
}

func mutationAAD(clusterID string, mutation Mutation) []byte {
	data, _ := json.Marshal(struct {
		ClusterID string `json:"cluster_id"`
		Namespace string `json:"namespace"`
		Key       string `json:"key"`
		Revision  uint64 `json:"revision"`
	}{clusterID, mutation.Namespace, mutation.Key, mutation.Record.Revision})
	return data
}
