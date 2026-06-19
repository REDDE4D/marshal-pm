// Package credstore is an encrypted-at-rest store for git HTTPS credentials
// (M22). Tokens are sealed with AES-256-GCM under a server master key; List
// and the on-disk file never expose a plaintext token.
package credstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Meta is the non-secret view of a credential.
type Meta struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Username  string `json:"username"`
	CreatedAt int64  `json:"created_at"`
}

type entry struct {
	Type      string `json:"type"`
	Username  string `json:"username"`
	Nonce     string `json:"nonce"`  // base64 std
	Cipher    string `json:"cipher"` // base64 std
	CreatedAt int64  `json:"created_at"`
}

// Store is a file-backed, encrypted credential store.
type Store struct {
	path string
	key  [32]byte
	mu   sync.Mutex
	data map[string]entry
}

// Open loads or creates the store under dir, resolving the master key.
func Open(dir string) (*Store, error) {
	key, err := loadMasterKey(dir)
	if err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "credentials.json"), key: key, data: map[string]entry{}}
	if b, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(b, &s.data); err != nil {
			return nil, fmt.Errorf("parse credentials.json: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

func loadMasterKey(dir string) ([32]byte, error) {
	var key [32]byte
	if env := os.Getenv("MARSHAL_MASTER_KEY"); env != "" {
		raw, err := base64.StdEncoding.DecodeString(env)
		if err != nil || len(raw) != 32 {
			return key, fmt.Errorf("MARSHAL_MASTER_KEY must be base64 of exactly 32 bytes")
		}
		copy(key[:], raw)
		return key, nil
	}
	path := filepath.Join(dir, "master.key")
	if b, err := os.ReadFile(path); err == nil {
		if len(b) != 32 {
			return key, fmt.Errorf("%s must be exactly 32 bytes", path)
		}
		copy(key[:], b)
		return key, nil
	} else if !os.IsNotExist(err) {
		return key, err
	}
	if _, err := rand.Read(key[:]); err != nil {
		return key, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return key, err
	}
	if err := os.WriteFile(path, key[:], 0o600); err != nil {
		return key, err
	}
	return key, nil
}

// Put creates or rotates the credential named name.
func (s *Store) Put(name, username, token string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("invalid credential name %q", name)
	}
	if token == "" {
		return fmt.Errorf("token is required")
	}
	block, err := aes.NewCipher(s.key[:])
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	ct := gcm.Seal(nil, nonce, []byte(token), nil)
	s.mu.Lock()
	created := int64(0)
	if old, ok := s.data[name]; ok {
		created = old.CreatedAt
	}
	if created == 0 {
		created = nowUnix()
	}
	s.data[name] = entry{
		Type:      "https-token",
		Username:  username,
		Nonce:     base64.StdEncoding.EncodeToString(nonce),
		Cipher:    base64.StdEncoding.EncodeToString(ct),
		CreatedAt: created,
	}
	err = s.flushLocked()
	s.mu.Unlock()
	return err
}

// Get decrypts the credential named name.
func (s *Store) Get(name string) (username, token string, ok bool, err error) {
	s.mu.Lock()
	e, present := s.data[name]
	s.mu.Unlock()
	if !present {
		return "", "", false, nil
	}
	block, err := aes.NewCipher(s.key[:])
	if err != nil {
		return "", "", false, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", false, err
	}
	nonce, err := base64.StdEncoding.DecodeString(e.Nonce)
	if err != nil {
		return "", "", false, err
	}
	ct, err := base64.StdEncoding.DecodeString(e.Cipher)
	if err != nil {
		return "", "", false, err
	}
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", "", false, fmt.Errorf("decrypt %q: %w", name, err)
	}
	return e.Username, string(pt), true, nil
}

// List returns non-secret metadata for every credential, sorted by name.
func (s *Store) List() []Meta {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Meta, 0, len(s.data))
	for name, e := range s.data {
		out = append(out, Meta{Name: name, Type: e.Type, Username: e.Username, CreatedAt: e.CreatedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Delete removes the credential named name, reporting whether it existed.
func (s *Store) Delete(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data[name]
	if ok {
		delete(s.data, name)
		_ = s.flushLocked()
	}
	return ok
}

func (s *Store) flushLocked() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, b, 0o600)
}

// nowUnix is the creation timestamp source, in its own func for test seams.
func nowUnix() int64 { return time.Now().Unix() }
