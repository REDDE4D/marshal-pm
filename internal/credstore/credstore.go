// Package credstore is an encrypted-at-rest store for git HTTPS credentials
// (M22). Tokens are sealed with AES-256-GCM under a server master key; List
// and the on-disk file never expose a plaintext token.
//
// M25 adds an ssh-key credential type whose secret is an ed25519 private key
// (sealed identically to https-token), with a non-secret public key and a
// (later-populated) pinned known_hosts string.
package credstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Meta is the non-secret view of a credential.
type Meta struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Username  string `json:"username"`
	PublicKey string `json:"public_key,omitempty"` // ssh-key only
	CreatedAt int64  `json:"created_at"`
}

type entry struct {
	Type       string `json:"type"`
	Username   string `json:"username"`
	PublicKey  string `json:"public_key,omitempty"`  // ssh-key only (not secret)
	KnownHosts string `json:"known_hosts,omitempty"` // ssh-key only (not secret)
	Nonce      string `json:"nonce"`                 // base64 std
	Cipher     string `json:"cipher"`                // base64 std (token OR private key)
	CreatedAt  int64  `json:"created_at"`
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

// seal encrypts plaintext under the master key, returning base64 nonce + cipher.
func (s *Store) seal(plaintext string) (nonceB64, cipherB64 string, err error) {
	block, err := aes.NewCipher(s.key[:])
	if err != nil {
		return "", "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", "", err
	}
	ct := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(nonce), base64.StdEncoding.EncodeToString(ct), nil
}

// openCipher decrypts base64-encoded nonce + ciphertext under the master key.
func (s *Store) openCipher(nonceB64, cipherB64 string) (string, error) {
	block, err := aes.NewCipher(s.key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce, err := base64.StdEncoding.DecodeString(nonceB64)
	if err != nil {
		return "", err
	}
	ct, err := base64.StdEncoding.DecodeString(cipherB64)
	if err != nil {
		return "", err
	}
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(pt), nil
}

// genKeypair mints an ed25519 keypair via ssh-keygen, returning OpenSSH-format
// private key bytes and the authorized_keys public-key line. A var seam so tests can stub it.
var genKeypair = func() (priv, pub []byte, err error) {
	tmp, err := os.MkdirTemp("", "marshal-keygen-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(tmp)
	key := filepath.Join(tmp, "id_ed25519")
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-C", "marshal", "-f", key, "-q")
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, nil, fmt.Errorf("ssh-keygen: %v: %s", err, out)
	}
	priv, err = os.ReadFile(key)
	if err != nil {
		return nil, nil, err
	}
	pub, err = os.ReadFile(key + ".pub")
	if err != nil {
		return nil, nil, err
	}
	return priv, pub, nil
}

// Put creates or rotates the credential named name.
func (s *Store) Put(name, username, token string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("invalid credential name %q", name)
	}
	if token == "" {
		return fmt.Errorf("token is required")
	}
	nonceB64, cipherB64, err := s.seal(token)
	if err != nil {
		return err
	}
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
		Nonce:     nonceB64,
		Cipher:    cipherB64,
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
	pt, err := s.openCipher(e.Nonce, e.Cipher)
	if err != nil {
		return "", "", false, fmt.Errorf("decrypt %q: %w", name, err)
	}
	return e.Username, pt, true, nil
}

// List returns non-secret metadata for every credential, sorted by name.
func (s *Store) List() []Meta {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Meta, 0, len(s.data))
	for name, e := range s.data {
		out = append(out, Meta{Name: name, Type: e.Type, Username: e.Username, PublicKey: e.PublicKey, CreatedAt: e.CreatedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Generate mints and stores an ssh-key credential, returning the public key line.
func (s *Store) Generate(name string) (string, error) {
	if !nameRE.MatchString(name) {
		return "", fmt.Errorf("invalid credential name %q", name)
	}
	priv, pub, err := genKeypair()
	if err != nil {
		return "", err
	}
	pubLine := strings.TrimSpace(string(pub))
	nonceB64, cipherB64, err := s.seal(string(priv))
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	created := int64(0)
	if old, ok := s.data[name]; ok {
		created = old.CreatedAt
	}
	if created == 0 {
		created = nowUnix()
	}
	s.data[name] = entry{
		Type:      "ssh-key",
		Username:  "git",
		PublicKey: pubLine,
		Nonce:     nonceB64,
		Cipher:    cipherB64,
		CreatedAt: created,
	}
	err = s.flushLocked()
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	return pubLine, nil
}

// GetKey decrypts an ssh-key credential's private key and returns it with the pinned known_hosts.
func (s *Store) GetKey(name string) (privateKey, knownHosts string, ok bool, err error) {
	s.mu.Lock()
	e, present := s.data[name]
	s.mu.Unlock()
	if !present || e.Type != "ssh-key" {
		return "", "", false, nil
	}
	pt, err := s.openCipher(e.Nonce, e.Cipher)
	if err != nil {
		return "", "", false, err
	}
	return pt, e.KnownHosts, true, nil
}

// SetKnownHosts records the pinned host key for an ssh-key credential.
func (s *Store) SetKnownHosts(name, line string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.data[name]
	if !ok || e.Type != "ssh-key" {
		return fmt.Errorf("no ssh credential %q", name)
	}
	e.KnownHosts = line
	s.data[name] = e
	return s.flushLocked()
}

// Delete removes the credential named name, reporting whether it existed.
func (s *Store) Delete(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data[name]
	if ok {
		delete(s.data, name)
		if err := s.flushLocked(); err != nil {
			log.Printf("credstore: persist after delete %q: %v", name, err)
		}
	}
	return ok
}

func (s *Store) flushLocked() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, s.path)
}

// nowUnix is the creation timestamp source, in its own func for test seams.
func nowUnix() int64 { return time.Now().Unix() }
