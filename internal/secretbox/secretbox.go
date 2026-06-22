// Package secretbox is the shared AES-256-GCM seal/open used by the credstore
// and the notification store. Both seal secrets under one server master key.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
)

// Box seals and opens secrets under a 32-byte master key.
type Box struct{ key [32]byte }

// FromKey builds a Box from an explicit key (used by tests).
func FromKey(key [32]byte) *Box { return &Box{key: key} }

// Load resolves the master key from $MARSHAL_MASTER_KEY (base64, 32 bytes) or
// <dir>/master.key, generating the file (0600) on first run.
func Load(dir string) (*Box, error) {
	var key [32]byte
	if env := os.Getenv("MARSHAL_MASTER_KEY"); env != "" {
		raw, err := base64.StdEncoding.DecodeString(env)
		if err != nil || len(raw) != 32 {
			return nil, fmt.Errorf("MARSHAL_MASTER_KEY must be base64 of exactly 32 bytes")
		}
		copy(key[:], raw)
		return &Box{key: key}, nil
	}
	path := filepath.Join(dir, "master.key")
	if b, err := os.ReadFile(path); err == nil {
		if len(b) != 32 {
			return nil, fmt.Errorf("%s must be exactly 32 bytes", path)
		}
		copy(key[:], b)
		return &Box{key: key}, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if _, err := rand.Read(key[:]); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, key[:], 0o600); err != nil {
		return nil, err
	}
	return &Box{key: key}, nil
}

// Seal encrypts plaintext, returning base64 nonce + ciphertext.
func (b *Box) Seal(plaintext []byte) (nonceB64, cipherB64 string, err error) {
	gcm, err := b.gcm()
	if err != nil {
		return "", "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", "", err
	}
	ct := gcm.Seal(nil, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(nonce), base64.StdEncoding.EncodeToString(ct), nil
}

// Open decrypts base64 nonce + ciphertext.
func (b *Box) Open(nonceB64, cipherB64 string) ([]byte, error) {
	gcm, err := b.gcm()
	if err != nil {
		return nil, err
	}
	nonce, err := base64.StdEncoding.DecodeString(nonceB64)
	if err != nil {
		return nil, err
	}
	ct, err := base64.StdEncoding.DecodeString(cipherB64)
	if err != nil {
		return nil, err
	}
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return pt, nil
}

func (b *Box) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(b.key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
