package notify

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"marshal/internal/secretbox"
)

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

const defaultCooldownSeconds = 300

type storedChannel struct {
	Type         string            `json:"type"`
	Enabled      bool              `json:"enabled"`
	Config       map[string]string `json:"config"`
	SecretNonce  string            `json:"secret_nonce,omitempty"`
	SecretCipher string            `json:"secret_cipher,omitempty"`
	CreatedAt    int64             `json:"created_at"`
}

type fileModel struct {
	Channels map[string]storedChannel `json:"channels"`
	Rules    map[string]Rule          `json:"rules"`
	Settings Settings                 `json:"settings"`
}

// Store persists channels, rules, and settings to notifications.json, sealing
// per-channel secrets under the shared master key.
type Store struct {
	path string
	box  *secretbox.Box
	mu   sync.Mutex
	data fileModel
}

// Open loads or creates the store under dir.
func Open(dir string, box *secretbox.Box) (*Store, error) {
	s := &Store{
		path: filepath.Join(dir, "notifications.json"),
		box:  box,
		data: fileModel{Channels: map[string]storedChannel{}, Rules: map[string]Rule{}, Settings: Settings{CooldownSeconds: defaultCooldownSeconds}},
	}
	if b, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(b, &s.data); err != nil {
			return nil, fmt.Errorf("parse notifications.json: %w", err)
		}
		if s.data.Channels == nil {
			s.data.Channels = map[string]storedChannel{}
		}
		if s.data.Rules == nil {
			s.data.Rules = map[string]Rule{}
		}
		if s.data.Settings.CooldownSeconds == 0 {
			s.data.Settings.CooldownSeconds = defaultCooldownSeconds
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

// Channels returns non-secret channel metadata, sorted by name.
func (s *Store) Channels() []Channel {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Channel, 0, len(s.data.Channels))
	for name, c := range s.data.Channels {
		out = append(out, Channel{Name: name, Type: c.Type, Enabled: c.Enabled, Config: c.Config})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// HasSecret reports whether the named channel has a sealed secret.
func (s *Store) HasSecret(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.data.Channels[name]
	return ok && c.SecretCipher != ""
}

// PutChannel creates or updates a channel. Empty secrets on an existing channel
// keep its current sealed secret.
func (s *Store) PutChannel(c Channel, secrets map[string]string) error {
	if !nameRE.MatchString(c.Name) {
		return fmt.Errorf("invalid channel name %q", c.Name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, existed := s.data.Channels[c.Name]
	sc := storedChannel{Type: c.Type, Enabled: c.Enabled, Config: c.Config, CreatedAt: old.CreatedAt}
	if !existed {
		sc.CreatedAt = time.Now().Unix()
	}
	if len(secrets) > 0 {
		raw, err := json.Marshal(secrets)
		if err != nil {
			return err
		}
		nonce, ct, err := s.box.Seal(raw)
		if err != nil {
			return err
		}
		sc.SecretNonce, sc.SecretCipher = nonce, ct
	} else {
		sc.SecretNonce, sc.SecretCipher = old.SecretNonce, old.SecretCipher
	}
	s.data.Channels[c.Name] = sc
	return s.flushLocked()
}

// DeleteChannel removes a channel, reporting whether it existed.
func (s *Store) DeleteChannel(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Channels[name]; !ok {
		return false
	}
	delete(s.data.Channels, name)
	_ = s.flushLocked()
	return true
}

// ChannelSecrets decrypts a channel's secret map.
func (s *Store) ChannelSecrets(name string) (map[string]string, bool, error) {
	s.mu.Lock()
	c, ok := s.data.Channels[name]
	s.mu.Unlock()
	if !ok {
		return nil, false, nil
	}
	if c.SecretCipher == "" {
		return map[string]string{}, true, nil
	}
	raw, err := s.box.Open(c.SecretNonce, c.SecretCipher)
	if err != nil {
		return nil, false, err
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, false, err
	}
	return m, true, nil
}

// Rules returns all rules sorted by name.
func (s *Store) Rules() []Rule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Rule, 0, len(s.data.Rules))
	for _, r := range s.data.Rules {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// PutRule creates or updates a rule.
func (s *Store) PutRule(r Rule) error {
	if !nameRE.MatchString(r.Name) {
		return fmt.Errorf("invalid rule name %q", r.Name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Rules[r.Name] = r
	return s.flushLocked()
}

// DeleteRule removes a rule, reporting whether it existed.
func (s *Store) DeleteRule(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Rules[name]; !ok {
		return false
	}
	delete(s.data.Rules, name)
	_ = s.flushLocked()
	return true
}

// Settings returns the current settings.
func (s *Store) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Settings
}

// SetSettings replaces the settings.
func (s *Store) SetSettings(st Settings) error {
	if st.CooldownSeconds <= 0 {
		st.CooldownSeconds = defaultCooldownSeconds
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Settings = st
	return s.flushLocked()
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
	return os.Rename(tmp, s.path)
}
