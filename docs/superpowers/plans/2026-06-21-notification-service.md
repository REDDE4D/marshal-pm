# Notification Service (M26) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Push fleet alerts (process crash, restart-loop, agent up/down, deploy failure) to user-configured webhook / Telegram / Slack / email channels, routed by a per-event/per-app rules engine, with per-event-key cooldown to suppress storms.

**Architecture:** A server-side detector polls the existing `server.Registry` snapshots and emits `Event`s on state transitions. A dispatcher applies a cooldown gate, matches events against rules, and fans out to channels (one transport each behind a `Sender` interface). Channels + rules + sealed secrets persist in `notifications.json`, reusing the credstore's AES master key via a new shared `internal/secretbox` package. A dashboard page manages it all.

**Tech Stack:** Go 1.26, standard library only (`net/http`, `net/smtp`, `crypto/aes`, `crypto/hmac`); React + TypeScript (Vite) for the dashboard page. No new Go module dependencies.

## Global Constraints

- Module path is `marshal`; imports are `marshal/internal/...`.
- TDD: failing test first, then minimal implementation. `go test ./... -race -count=1` green before finishing.
- `gofmt -l .` lists nothing; `go vet ./...` clean.
- Commit subject imperative; trailer `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- All work on branch `m26-notification-service` (already created).
- **Secrets never logged and never returned over the dashboard API** (redacted to a `has_secret` bool).
- No proto or agent changes — detection is purely server-side over `registry.List()`.
- Channel sends are best-effort: bounded retry, then log; a broken channel must never crash the server or block detection.
- `ProcInfo.State` strings come from the supervisor (`starting|online|stopping|stopped|restarting|errored`) and from synthetic deploy entries (`cloning|building|failed|committing`).

---

## File Structure

- Create `internal/secretbox/secretbox.go` — shared AES-256-GCM seal/open + master-key resolution.
- Create `internal/secretbox/secretbox_test.go`.
- Modify `internal/credstore/credstore.go` — delegate crypto to `secretbox` (on-disk format unchanged).
- Create `internal/notify/model.go` — `EventType`, `Event`, `Channel`, `Rule`, `Settings`, `Sender`, `Message`, `Rule.Matches`.
- Create `internal/notify/store.go` + `store_test.go` — `notifications.json` persistence, secret sealing.
- Create `internal/notify/detector.go` + `detector_test.go` — `diff()` pure function + `Detector.Run` loop.
- Create `internal/notify/dispatcher.go` + `dispatcher_test.go` — cooldown gate + rule match + fan-out.
- Create `internal/notify/render.go` — `render(Event) Message`.
- Create `internal/notify/channels/channels.go` — `New()` factory + shared `httpDoer` seam.
- Create `internal/notify/channels/webhook.go` + `webhook_test.go`.
- Create `internal/notify/channels/telegram.go` + `slack.go` + `chat_test.go`.
- Create `internal/notify/channels/email.go` + `email_test.go`.
- Create `internal/dashboard/notifications.go` + `notifications_test.go` — HTTP CRUD + test-send.
- Modify `internal/dashboard/handlers.go` — register routes, add `notifs` field.
- Modify `internal/dashboard/server.go` — thread the notify store + builder through `Serve`.
- Modify `internal/server/server.go` — construct box/store/dispatcher/detector, start the loop, pass to dashboard.
- Create `web/src/Notifications.tsx`; modify `web/src/api.ts`, `web/src/router.ts`, `web/src/App.tsx`.

---

## Phase 1 — Foundation

### Task 1: `internal/secretbox` package

**Files:**
- Create: `internal/secretbox/secretbox.go`
- Test: `internal/secretbox/secretbox_test.go`

**Interfaces:**
- Produces:
  - `func Load(dir string) (*Box, error)` — resolves `MARSHAL_MASTER_KEY` (base64, 32 bytes) or `<dir>/master.key` (generated 0600 if absent), identical to credstore's current `loadMasterKey`.
  - `func FromKey(key [32]byte) *Box`
  - `func (b *Box) Seal(plaintext []byte) (nonceB64, cipherB64 string, err error)`
  - `func (b *Box) Open(nonceB64, cipherB64 string) ([]byte, error)`

- [ ] **Step 1: Write the failing test**

```go
package secretbox

import "testing"

func TestSealOpenRoundTrip(t *testing.T) {
	var key [32]byte
	for i := range key {
		key[i] = byte(i)
	}
	b := FromKey(key)
	nonce, ct, err := b.Seal([]byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	if ct == "" || nonce == "" {
		t.Fatal("empty seal output")
	}
	pt, err := b.Open(nonce, ct)
	if err != nil {
		t.Fatal(err)
	}
	if string(pt) != "hunter2" {
		t.Fatalf("got %q", pt)
	}
}

func TestOpenWrongKeyFails(t *testing.T) {
	var k1, k2 [32]byte
	k2[0] = 1
	nonce, ct, _ := FromKey(k1).Seal([]byte("secret"))
	if _, err := FromKey(k2).Open(nonce, ct); err == nil {
		t.Fatal("expected decrypt failure with wrong key")
	}
}

func TestLoadGeneratesMasterKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MARSHAL_MASTER_KEY", "")
	b1, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	nonce, ct, _ := b1.Seal([]byte("x"))
	b2, err := Load(dir) // reloads the same generated master.key
	if err != nil {
		t.Fatal(err)
	}
	pt, err := b2.Open(nonce, ct)
	if err != nil || string(pt) != "x" {
		t.Fatalf("reload mismatch: %v %q", err, pt)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/secretbox/ -run TestSeal -v`
Expected: FAIL — package/`FromKey` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/secretbox/ -v`
Expected: PASS (all three tests).

- [ ] **Step 5: Commit**

```bash
git add internal/secretbox/
git commit -m "feat(m26): internal/secretbox shared AES-256-GCM seal/open

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Refactor credstore onto secretbox (behavior-identical)

**Files:**
- Modify: `internal/credstore/credstore.go`

**Interfaces:**
- Consumes: `secretbox.Load`, `(*Box).Seal`, `(*Box).Open`.
- Produces: no public API change. On-disk `credentials.json` format unchanged.

- [ ] **Step 1: Confirm the existing credstore tests are the safety net**

Run: `go test ./internal/credstore/ -v`
Expected: PASS (these must stay green after the refactor — do not edit them).

- [ ] **Step 2: Swap the key field + crypto for a secretbox.Box**

In `internal/credstore/credstore.go`:

Replace the `Store` struct field `key [32]byte` with `box *secretbox.Box`:

```go
type Store struct {
	path string
	box  *secretbox.Box
	mu   sync.Mutex
	data map[string]entry
}
```

In `Open`, replace `loadMasterKey(dir)` usage:

```go
func Open(dir string) (*Store, error) {
	box, err := secretbox.Load(dir)
	if err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "credentials.json"), box: box, data: map[string]entry{}}
	if b, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(b, &s.data); err != nil {
			return nil, fmt.Errorf("parse credentials.json: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}
```

Replace the `seal`/`openCipher` methods with delegations (keep the same names so call sites are untouched):

```go
func (s *Store) seal(plaintext string) (nonceB64, cipherB64 string, err error) {
	return s.box.Seal([]byte(plaintext))
}

func (s *Store) openCipher(nonceB64, cipherB64 string) (string, error) {
	pt, err := s.box.Open(nonceB64, cipherB64)
	return string(pt), err
}
```

Delete the now-unused `loadMasterKey` function and the `crypto/aes`, `crypto/cipher`, `crypto/rand`, `encoding/base64` imports if no longer referenced (keep `encoding/base64` only if still used elsewhere — it is not after this change). Add `"marshal/internal/secretbox"` to imports.

- [ ] **Step 3: Run credstore + secretbox tests**

Run: `go test ./internal/credstore/ ./internal/secretbox/ -race -count=1`
Expected: PASS — identical behavior, no test edits.

- [ ] **Step 4: Vet + format**

Run: `gofmt -l internal/credstore/credstore.go && go vet ./internal/credstore/`
Expected: no output.

- [ ] **Step 5: Commit**

```bash
git add internal/credstore/credstore.go
git commit -m "refactor(m26): credstore uses internal/secretbox (on-disk unchanged)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: notify domain model

**Files:**
- Create: `internal/notify/model.go`
- Test: `internal/notify/model_test.go`

**Interfaces:**
- Produces:
  - `type EventType string` + consts `EventCrash`, `EventRestartLoop`, `EventAgentDown`, `EventAgentUp`, `EventDeployFail`.
  - `type Event struct { Type EventType; Agent, Process, Detail string; Time time.Time }`
  - `type Channel struct { Name, Type string; Enabled bool; Config map[string]string }`
  - `type Rule struct { Name string; Enabled bool; Events []EventType; Agent, Process string; Channels []string }`
  - `type Settings struct { CooldownSeconds int }`
  - `type Message struct { Title, Body string; Event Event }`
  - `type Sender interface { Send(ctx context.Context, m Message) error }`
  - `func (r Rule) Matches(e Event) bool`

- [ ] **Step 1: Write the failing test**

```go
package notify

import "testing"

func TestRuleMatches(t *testing.T) {
	crash := Event{Type: EventCrash, Agent: "dev-1", Process: "api"}
	cases := []struct {
		name string
		rule Rule
		want bool
	}{
		{"wildcard all", Rule{Enabled: true}, true},
		{"event match", Rule{Enabled: true, Events: []EventType{EventCrash}}, true},
		{"event miss", Rule{Enabled: true, Events: []EventType{EventDeployFail}}, false},
		{"agent match", Rule{Enabled: true, Agent: "dev-1"}, true},
		{"agent miss", Rule{Enabled: true, Agent: "dev-2"}, false},
		{"agent star", Rule{Enabled: true, Agent: "*"}, true},
		{"process match", Rule{Enabled: true, Process: "api"}, true},
		{"process miss", Rule{Enabled: true, Process: "web"}, false},
		{"disabled", Rule{Enabled: false}, false},
	}
	for _, c := range cases {
		if got := c.rule.Matches(crash); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/notify/ -run TestRuleMatches -v`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Write minimal implementation**

```go
// Package notify detects fleet trouble (crashes, restart loops, agent up/down,
// deploy failures) from server snapshots and dispatches alerts to channels.
package notify

import (
	"context"
	"time"
)

// EventType enumerates the alertable fleet conditions.
type EventType string

const (
	EventCrash       EventType = "crash"
	EventRestartLoop EventType = "restart_loop"
	EventAgentDown   EventType = "agent_down"
	EventAgentUp     EventType = "agent_up"
	EventDeployFail  EventType = "deploy_fail"
)

// Event is a single detected condition. Process is "" for agent-level events.
type Event struct {
	Type    EventType
	Agent   string
	Process string
	Detail  string
	Time    time.Time
}

// Channel is the non-secret config of a delivery destination.
type Channel struct {
	Name    string            `json:"name"`
	Type    string            `json:"type"` // webhook | telegram | slack | email
	Enabled bool              `json:"enabled"`
	Config  map[string]string `json:"config"`
}

// Rule routes matching events to channels.
type Rule struct {
	Name     string      `json:"name"`
	Enabled  bool        `json:"enabled"`
	Events   []EventType `json:"events"` // empty = any
	Agent    string      `json:"agent"`  // "" or "*" = any
	Process  string      `json:"process"`
	Channels []string    `json:"channels"`
}

// Settings holds dispatcher tunables.
type Settings struct {
	CooldownSeconds int `json:"cooldown_seconds"`
}

// Message is a rendered alert handed to a Sender.
type Message struct {
	Title string
	Body  string
	Event Event
}

// Sender delivers a Message over one transport.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// Matches reports whether the event should route through this rule.
func (r Rule) Matches(e Event) bool {
	if !r.Enabled {
		return false
	}
	if len(r.Events) > 0 {
		ok := false
		for _, t := range r.Events {
			if t == e.Type {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if r.Agent != "" && r.Agent != "*" && r.Agent != e.Agent {
		return false
	}
	if r.Process != "" && r.Process != "*" && r.Process != e.Process {
		return false
	}
	return true
}

var _ = context.Background // context used by Sender; keep import
```

Note: remove the trailing `var _` line and instead reference `context` via the `Sender` interface (it already does). Drop the unused-import guard once the file compiles.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/notify/ -run TestRuleMatches -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/notify/model.go internal/notify/model_test.go
git commit -m "feat(m26): notify domain model + rule matching

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: notify store (persistence + secret sealing)

**Files:**
- Create: `internal/notify/store.go`
- Test: `internal/notify/store_test.go`

**Interfaces:**
- Consumes: `secretbox.FromKey` / `secretbox.Box`, the model types.
- Produces:
  - `func Open(dir string, box *secretbox.Box) (*Store, error)`
  - `func (s *Store) Channels() []Channel` (metadata only, sorted by name)
  - `func (s *Store) PutChannel(c Channel, secrets map[string]string) error` (empty `secrets` on an existing channel keeps the old sealed secret)
  - `func (s *Store) DeleteChannel(name string) bool`
  - `func (s *Store) ChannelSecrets(name string) (map[string]string, bool, error)`
  - `func (s *Store) HasSecret(name string) bool`
  - `func (s *Store) Rules() []Rule` / `PutRule(Rule) error` / `DeleteRule(name string) bool`
  - `func (s *Store) Settings() Settings` / `SetSettings(Settings) error`

- [ ] **Step 1: Write the failing test**

```go
package notify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/secretbox"
)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	var key [32]byte
	key[0] = 7
	s, err := Open(dir, secretbox.FromKey(key))
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestPutGetChannelSecretsSealed(t *testing.T) {
	s, dir := testStore(t)
	ch := Channel{Name: "tg", Type: "telegram", Enabled: true, Config: map[string]string{"chat_id": "42"}}
	if err := s.PutChannel(ch, map[string]string{"bot_token": "SECRET123"}); err != nil {
		t.Fatal(err)
	}
	// metadata view never carries the secret
	got := s.Channels()
	if len(got) != 1 || got[0].Name != "tg" || got[0].Config["chat_id"] != "42" {
		t.Fatalf("channels view wrong: %+v", got)
	}
	// secret retrievable for sending
	sec, ok, err := s.ChannelSecrets("tg")
	if err != nil || !ok || sec["bot_token"] != "SECRET123" {
		t.Fatalf("secret round-trip failed: %v %v %v", sec, ok, err)
	}
	// on-disk file has no plaintext secret
	raw, _ := os.ReadFile(filepath.Join(dir, "notifications.json"))
	if strings.Contains(string(raw), "SECRET123") {
		t.Fatal("plaintext secret leaked to disk")
	}
	if !s.HasSecret("tg") {
		t.Fatal("HasSecret should be true")
	}
}

func TestPutChannelEmptySecretKeepsOld(t *testing.T) {
	s, _ := testStore(t)
	_ = s.PutChannel(Channel{Name: "wh", Type: "webhook"}, map[string]string{"hmac": "k1"})
	_ = s.PutChannel(Channel{Name: "wh", Type: "webhook", Enabled: true}, nil) // update, no new secret
	sec, ok, _ := s.ChannelSecrets("wh")
	if !ok || sec["hmac"] != "k1" {
		t.Fatalf("expected kept secret, got %v", sec)
	}
}

func TestRulesAndSettingsPersist(t *testing.T) {
	s, dir := testStore(t)
	_ = s.PutRule(Rule{Name: "r1", Enabled: true, Events: []EventType{EventCrash}, Channels: []string{"wh"}})
	_ = s.SetSettings(Settings{CooldownSeconds: 120})
	var key [32]byte
	key[0] = 7
	s2, err := Open(dir, secretbox.FromKey(key)) // reload
	if err != nil {
		t.Fatal(err)
	}
	if rs := s2.Rules(); len(rs) != 1 || rs[0].Name != "r1" {
		t.Fatalf("rules not persisted: %+v", rs)
	}
	if s2.Settings().CooldownSeconds != 120 {
		t.Fatalf("settings not persisted: %+v", s2.Settings())
	}
}

func TestDefaultCooldown(t *testing.T) {
	s, _ := testStore(t)
	if s.Settings().CooldownSeconds != 300 {
		t.Fatalf("want default 300, got %d", s.Settings().CooldownSeconds)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/notify/ -run TestPutGetChannel -v`
Expected: FAIL — `Open`/`Store` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/notify/ -run 'TestPut|TestRules|TestDefault' -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/notify/store.go internal/notify/store_test.go
git commit -m "feat(m26): notify store with sealed per-channel secrets

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Phase 2 — Detection + Dispatch

### Task 5: detector diff (pure transition logic)

**Files:**
- Create: `internal/notify/detector.go`
- Test: `internal/notify/detector_test.go`

**Interfaces:**
- Consumes: `*pb.AgentState`, `*pb.ProcInfo` (fields: `AgentName`, `Connected`, `Procs`; `ProcInfo.Name`, `.State`, `.Restarts`, `.Detail`).
- Produces:
  - `type Lister interface { List() []*pb.AgentState }`
  - `type Emitter interface { Emit(Event) }`
  - `func diff(prev, next []*pb.AgentState, now time.Time) []Event` (unexported, pure)

Transition rules (key processes by `ProcInfo.Name`):
- new agent or new process → seed silently (no event).
- agent `connected:true → false` ⇒ `EventAgentDown`; `false → true` ⇒ `EventAgentUp`.
- process state entering `restarting` (prev != `restarting`) ⇒ `EventCrash`.
- process state entering `errored` (prev != `errored`) ⇒ `EventRestartLoop`.
- process state entering `failed` (prev != `failed`) ⇒ `EventDeployFail` (Detail = `ProcInfo.Detail`).
- nil/empty `prev` ⇒ everything is new ⇒ no events (seed).

- [ ] **Step 1: Write the failing test**

```go
package notify

import (
	"testing"
	"time"

	"marshal/internal/pb"
)

func agent(name string, connected bool, procs ...*pb.ProcInfo) *pb.AgentState {
	return &pb.AgentState{AgentName: name, Connected: connected, Procs: procs}
}
func proc(name, state string, restarts int32) *pb.ProcInfo {
	return &pb.ProcInfo{Name: name, State: state, Restarts: restarts}
}

func types(evs []Event) []EventType {
	out := make([]EventType, len(evs))
	for i, e := range evs {
		out[i] = e.Type
	}
	return out
}

func TestDiffSeedsSilently(t *testing.T) {
	next := []*pb.AgentState{agent("dev-1", true, proc("api", "online", 0))}
	if evs := diff(nil, next, time.Now()); len(evs) != 0 {
		t.Fatalf("seed should emit nothing, got %v", types(evs))
	}
}

func TestDiffCrash(t *testing.T) {
	prev := []*pb.AgentState{agent("dev-1", true, proc("api", "online", 0))}
	next := []*pb.AgentState{agent("dev-1", true, proc("api", "restarting", 1))}
	evs := diff(prev, next, time.Now())
	if len(evs) != 1 || evs[0].Type != EventCrash || evs[0].Agent != "dev-1" || evs[0].Process != "api" {
		t.Fatalf("want one crash for dev-1/api, got %+v", evs)
	}
}

func TestDiffRestartLoopAndDeployFail(t *testing.T) {
	prev := []*pb.AgentState{agent("dev-1", true, proc("api", "restarting", 5), proc("web", "building", 0))}
	next := []*pb.AgentState{agent("dev-1", true, proc("api", "errored", 6), proc("web", "failed", 0))}
	got := map[EventType]bool{}
	for _, e := range diff(prev, next, time.Now()) {
		got[e.Type] = true
	}
	if !got[EventRestartLoop] || !got[EventDeployFail] {
		t.Fatalf("want restart_loop + deploy_fail, got %v", got)
	}
}

func TestDiffAgentDownUp(t *testing.T) {
	prev := []*pb.AgentState{agent("dev-1", true)}
	next := []*pb.AgentState{agent("dev-1", false)}
	if evs := diff(prev, next, time.Now()); len(evs) != 1 || evs[0].Type != EventAgentDown {
		t.Fatalf("want agent_down, got %+v", evs)
	}
	if evs := diff(next, prev, time.Now()); len(evs) != 1 || evs[0].Type != EventAgentUp {
		t.Fatalf("want agent_up, got %+v", evs)
	}
}

func TestDiffNoEventOnSteadyState(t *testing.T) {
	prev := []*pb.AgentState{agent("dev-1", true, proc("api", "errored", 5))}
	next := []*pb.AgentState{agent("dev-1", true, proc("api", "errored", 5))}
	if evs := diff(prev, next, time.Now()); len(evs) != 0 {
		t.Fatalf("steady errored should not re-emit, got %v", types(evs))
	}
}

func TestDiffCleanStopNoEvent(t *testing.T) {
	prev := []*pb.AgentState{agent("dev-1", true, proc("api", "online", 0))}
	next := []*pb.AgentState{agent("dev-1", true, proc("api", "stopped", 0))}
	if evs := diff(prev, next, time.Now()); len(evs) != 0 {
		t.Fatalf("clean stop should not alert, got %v", types(evs))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/notify/ -run TestDiff -v`
Expected: FAIL — `diff` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
package notify

import (
	"fmt"
	"time"

	"marshal/internal/pb"
)

// Lister is the subset of *server.Registry the detector reads.
type Lister interface{ List() []*pb.AgentState }

// Emitter receives detected events (the Dispatcher implements it).
type Emitter interface{ Emit(Event) }

// diff compares two fleet snapshots and returns the events implied by the
// transitions. A nil/absent prev (or new agent/process) seeds silently.
func diff(prev, next []*pb.AgentState, now time.Time) []Event {
	prevAgents := map[string]*pb.AgentState{}
	for _, a := range prev {
		prevAgents[a.GetAgentName()] = a
	}
	var out []Event
	for _, a := range next {
		pa, known := prevAgents[a.GetAgentName()]
		if !known {
			continue // new agent: seed without events
		}
		if pa.GetConnected() && !a.GetConnected() {
			out = append(out, Event{Type: EventAgentDown, Agent: a.GetAgentName(), Detail: "agent stopped reporting", Time: now})
		} else if !pa.GetConnected() && a.GetConnected() {
			out = append(out, Event{Type: EventAgentUp, Agent: a.GetAgentName(), Detail: "agent reconnected", Time: now})
		}
		prevProcs := map[string]*pb.ProcInfo{}
		for _, p := range pa.GetProcs() {
			prevProcs[p.GetName()] = p
		}
		for _, p := range a.GetProcs() {
			pp, seen := prevProcs[p.GetName()]
			if !seen {
				continue // new process: seed without events
			}
			if e, ok := procEvent(a.GetAgentName(), pp.GetState(), p, now); ok {
				out = append(out, e)
			}
		}
	}
	return out
}

// procEvent maps a single process state transition to an event, if any.
func procEvent(agentName, prevState string, p *pb.ProcInfo, now time.Time) (Event, bool) {
	cur := p.GetState()
	if cur == prevState {
		return Event{}, false
	}
	base := Event{Agent: agentName, Process: p.GetName(), Time: now}
	switch cur {
	case "restarting":
		base.Type = EventCrash
		base.Detail = fmt.Sprintf("crashed (restart #%d)", p.GetRestarts())
		return base, true
	case "errored":
		base.Type = EventRestartLoop
		base.Detail = fmt.Sprintf("gave up after %d restarts", p.GetRestarts())
		return base, true
	case "failed":
		base.Type = EventDeployFail
		base.Detail = p.GetDetail()
		if base.Detail == "" {
			base.Detail = "deploy failed"
		}
		return base, true
	}
	return Event{}, false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/notify/ -run TestDiff -v`
Expected: PASS (all six).

- [ ] **Step 5: Commit**

```bash
git add internal/notify/detector.go internal/notify/detector_test.go
git commit -m "feat(m26): detector diff for crash/loop/deploy/agent transitions

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: detector loop

**Files:**
- Modify: `internal/notify/detector.go`
- Test: `internal/notify/detector_test.go`

**Interfaces:**
- Produces:
  - `type Detector struct {...}`
  - `func NewDetector(l Lister, e Emitter, interval time.Duration) *Detector`
  - `func (d *Detector) Run(ctx context.Context)` — ticks every `interval`, lists, diffs against the prior snapshot, emits, then stores the new snapshot. First tick seeds.

- [ ] **Step 1: Write the failing test**

```go
package notify

import (
	"context"
	"sync"
	"testing"
	"time"

	"marshal/internal/pb"
)

type fakeLister struct {
	mu    sync.Mutex
	snaps [][]*pb.AgentState
	i     int
}

func (f *fakeLister) List() []*pb.AgentState {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.i >= len(f.snaps) {
		return f.snaps[len(f.snaps)-1]
	}
	s := f.snaps[f.i]
	f.i++
	return s
}

type recEmitter struct {
	mu  sync.Mutex
	evs []Event
}

func (r *recEmitter) Emit(e Event) { r.mu.Lock(); r.evs = append(r.evs, e); r.mu.Unlock() }
func (r *recEmitter) count() int   { r.mu.Lock(); defer r.mu.Unlock(); return len(r.evs) }

func TestDetectorRunEmitsOnTransition(t *testing.T) {
	lst := &fakeLister{snaps: [][]*pb.AgentState{
		{agent("dev-1", true, proc("api", "online", 0))},     // seed
		{agent("dev-1", true, proc("api", "restarting", 1))}, // crash
	}}
	em := &recEmitter{}
	d := NewDetector(lst, em, time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	go d.Run(ctx)
	deadline := time.After(2 * time.Second)
	for em.count() < 1 {
		select {
		case <-deadline:
			t.Fatal("no event within deadline")
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
	cancel()
	em.mu.Lock()
	defer em.mu.Unlock()
	if em.evs[0].Type != EventCrash {
		t.Fatalf("want crash, got %v", em.evs[0].Type)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/notify/ -run TestDetectorRun -v`
Expected: FAIL — `NewDetector`/`Detector` undefined.

- [ ] **Step 3: Write minimal implementation**

Append to `internal/notify/detector.go`:

```go
import "context" // add alongside existing imports

// Detector polls fleet snapshots and emits events on transitions.
type Detector struct {
	lister   Lister
	emit     Emitter
	interval time.Duration
	now      func() time.Time
	prev     []*pb.AgentState
}

// NewDetector builds a detector polling l every interval.
func NewDetector(l Lister, e Emitter, interval time.Duration) *Detector {
	return &Detector{lister: l, emit: e, interval: interval, now: time.Now}
}

// Run polls until ctx is cancelled. The first poll seeds the baseline.
func (d *Detector) Run(ctx context.Context) {
	t := time.NewTicker(d.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			next := d.lister.List()
			for _, e := range diff(d.prev, next, d.now()) {
				d.emit.Emit(e)
			}
			d.prev = next
		}
	}
}
```

(Merge the `context` and existing imports into one import block; do not duplicate.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/notify/ -run TestDetector -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/notify/detector.go internal/notify/detector_test.go
git commit -m "feat(m26): detector poll loop seeds then emits on transitions

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: render + dispatcher (cooldown + rule match + fan-out)

**Files:**
- Create: `internal/notify/render.go`
- Create: `internal/notify/dispatcher.go`
- Test: `internal/notify/dispatcher_test.go`

**Interfaces:**
- Consumes: model types, `Sender`, `Message`.
- Produces:
  - `func render(e Event) Message`
  - `type StoreReader interface { Rules() []Rule; Channels() []Channel; ChannelSecrets(name string) (map[string]string, bool, error); Settings() Settings }` (the `*Store` satisfies it)
  - `type BuildFunc func(c Channel, secrets map[string]string) (Sender, error)`
  - `func NewDispatcher(store StoreReader, build BuildFunc, opts ...DispatchOption) *Dispatcher`
  - `func WithClock(fn func() time.Time) DispatchOption`
  - `func WithSyncDelivery() DispatchOption` — deliver inline (tests)
  - `func (d *Dispatcher) Emit(e Event)` — cooldown gate → match → fan-out

- [ ] **Step 1: Write the failing test**

```go
package notify

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeSender struct {
	mu    sync.Mutex
	sent  []Message
	chName string
}

func (f *fakeSender) Send(_ context.Context, m Message) error {
	f.mu.Lock()
	f.sent = append(f.sent, m)
	f.mu.Unlock()
	return nil
}

type fakeStore struct {
	rules    []Rule
	channels []Channel
	settings Settings
}

func (s *fakeStore) Rules() []Rule       { return s.rules }
func (s *fakeStore) Channels() []Channel { return s.channels }
func (s *fakeStore) Settings() Settings  { return s.settings }
func (s *fakeStore) ChannelSecrets(string) (map[string]string, bool, error) {
	return map[string]string{}, true, nil
}

func newTestDispatcher(t *testing.T, st *fakeStore, clock func() time.Time) (*Dispatcher, map[string]*fakeSender) {
	t.Helper()
	senders := map[string]*fakeSender{}
	build := func(c Channel, _ map[string]string) (Sender, error) {
		fs := &fakeSender{chName: c.Name}
		senders[c.Name] = fs
		return fs, nil
	}
	d := NewDispatcher(st, build, WithSyncDelivery(), WithClock(clock))
	return d, senders
}

func TestDispatcherFanOutToMatchingChannels(t *testing.T) {
	st := &fakeStore{
		channels: []Channel{{Name: "tg", Type: "telegram", Enabled: true}, {Name: "wh", Type: "webhook", Enabled: true}},
		rules:    []Rule{{Name: "crashes", Enabled: true, Events: []EventType{EventCrash}, Channels: []string{"tg"}}},
		settings: Settings{CooldownSeconds: 300},
	}
	now := time.Unix(1000, 0)
	d, senders := newTestDispatcher(t, st, func() time.Time { return now })
	d.Emit(Event{Type: EventCrash, Agent: "dev-1", Process: "api"})
	if len(senders["tg"].sent) != 1 {
		t.Fatalf("tg should get 1, got %d", len(senders["tg"].sent))
	}
	if s := senders["wh"]; s != nil && len(s.sent) != 0 {
		t.Fatalf("wh should not fire (no matching rule)")
	}
}

func TestDispatcherCooldownSuppressesRepeat(t *testing.T) {
	st := &fakeStore{
		channels: []Channel{{Name: "tg", Type: "telegram", Enabled: true}},
		rules:    []Rule{{Name: "all", Enabled: true, Channels: []string{"tg"}}},
		settings: Settings{CooldownSeconds: 300},
	}
	cur := time.Unix(1000, 0)
	d, senders := newTestDispatcher(t, st, func() time.Time { return cur })
	ev := Event{Type: EventCrash, Agent: "dev-1", Process: "api"}
	d.Emit(ev)
	cur = cur.Add(60 * time.Second) // within cooldown
	d.Emit(ev)
	if len(senders["tg"].sent) != 1 {
		t.Fatalf("cooldown should suppress, got %d sends", len(senders["tg"].sent))
	}
	cur = cur.Add(300 * time.Second) // past cooldown
	d.Emit(ev)
	if len(senders["tg"].sent) != 2 {
		t.Fatalf("should fire after cooldown, got %d", len(senders["tg"].sent))
	}
}

func TestDispatcherDedupAcrossRules(t *testing.T) {
	st := &fakeStore{
		channels: []Channel{{Name: "tg", Type: "telegram", Enabled: true}},
		rules: []Rule{
			{Name: "r1", Enabled: true, Channels: []string{"tg"}},
			{Name: "r2", Enabled: true, Events: []EventType{EventCrash}, Channels: []string{"tg"}},
		},
		settings: Settings{CooldownSeconds: 300},
	}
	now := time.Unix(1000, 0)
	d, senders := newTestDispatcher(t, st, func() time.Time { return now })
	d.Emit(Event{Type: EventCrash, Agent: "dev-1", Process: "api"})
	if len(senders["tg"].sent) != 1 {
		t.Fatalf("two matching rules → one send, got %d", len(senders["tg"].sent))
	}
}

func TestDispatcherSkipsDisabledChannel(t *testing.T) {
	st := &fakeStore{
		channels: []Channel{{Name: "tg", Type: "telegram", Enabled: false}},
		rules:    []Rule{{Name: "all", Enabled: true, Channels: []string{"tg"}}},
		settings: Settings{CooldownSeconds: 300},
	}
	now := time.Unix(1000, 0)
	d, senders := newTestDispatcher(t, st, func() time.Time { return now })
	d.Emit(Event{Type: EventCrash, Agent: "dev-1", Process: "api"})
	if s := senders["tg"]; s != nil && len(s.sent) != 0 {
		t.Fatal("disabled channel must not fire")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/notify/ -run TestDispatcher -v`
Expected: FAIL — `NewDispatcher` undefined.

- [ ] **Step 3: Write render.go**

```go
package notify

import "fmt"

var eventTitles = map[EventType]string{
	EventCrash:       "Process crashed",
	EventRestartLoop: "Process in restart loop",
	EventAgentDown:   "Agent disconnected",
	EventAgentUp:     "Agent reconnected",
	EventDeployFail:  "Deploy failed",
}

// render builds a human-facing Message for an event.
func render(e Event) Message {
	title := eventTitles[e.Type]
	if title == "" {
		title = string(e.Type)
	}
	who := e.Agent
	if e.Process != "" {
		who = fmt.Sprintf("%s / %s", e.Agent, e.Process)
	}
	body := fmt.Sprintf("[%s] %s: %s", who, title, e.Detail)
	return Message{Title: fmt.Sprintf("Marshal: %s (%s)", title, who), Body: body, Event: e}
}
```

- [ ] **Step 4: Write dispatcher.go**

```go
package notify

import (
	"context"
	"log"
	"time"
)

// StoreReader is the read surface the dispatcher needs (the *Store satisfies it).
type StoreReader interface {
	Rules() []Rule
	Channels() []Channel
	ChannelSecrets(name string) (map[string]string, bool, error)
	Settings() Settings
}

// BuildFunc constructs a Sender for a channel + its decrypted secrets.
type BuildFunc func(c Channel, secrets map[string]string) (Sender, error)

// Dispatcher gates events by cooldown, matches rules, and fans out to channels.
type Dispatcher struct {
	store StoreReader
	build BuildFunc
	now   func() time.Time
	sync  bool
	mu    sync.Mutex
	last  map[string]time.Time
}

// DispatchOption configures a Dispatcher.
type DispatchOption func(*Dispatcher)

// WithClock overrides the clock (tests).
func WithClock(fn func() time.Time) DispatchOption { return func(d *Dispatcher) { d.now = fn } }

// WithSyncDelivery delivers inline instead of in goroutines (tests).
func WithSyncDelivery() DispatchOption { return func(d *Dispatcher) { d.sync = true } }

// NewDispatcher builds a dispatcher.
func NewDispatcher(store StoreReader, build BuildFunc, opts ...DispatchOption) *Dispatcher {
	d := &Dispatcher{store: store, build: build, now: time.Now, last: map[string]time.Time{}}
	for _, o := range opts {
		o(d)
	}
	return d
}

// Emit gates the event by cooldown, then fans out to matching channels.
func (d *Dispatcher) Emit(e Event) {
	if !d.allow(e) {
		return
	}
	targets := d.matchChannels(e)
	if len(targets) == 0 {
		return
	}
	msg := render(e)
	for _, c := range targets {
		if d.sync {
			d.deliver(c, msg)
		} else {
			go d.deliver(c, msg)
		}
	}
}

// allow records and checks the per-(agent,process,type) cooldown.
func (d *Dispatcher) allow(e Event) bool {
	key := e.Agent + "\x00" + e.Process + "\x00" + string(e.Type)
	cooldown := time.Duration(d.store.Settings().CooldownSeconds) * time.Second
	now := d.now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if last, ok := d.last[key]; ok && now.Sub(last) < cooldown {
		return false
	}
	d.last[key] = now
	return true
}

// matchChannels returns the deduplicated, enabled channels for an event.
func (d *Dispatcher) matchChannels(e Event) []Channel {
	byName := map[string]Channel{}
	for _, c := range d.store.Channels() {
		byName[c.Name] = c
	}
	seen := map[string]bool{}
	var out []Channel
	for _, r := range d.store.Rules() {
		if !r.Matches(e) {
			continue
		}
		for _, name := range r.Channels {
			if seen[name] {
				continue
			}
			c, ok := byName[name]
			if !ok || !c.Enabled {
				continue
			}
			seen[name] = true
			out = append(out, c)
		}
	}
	return out
}

func (d *Dispatcher) deliver(c Channel, msg Message) {
	secrets, _, err := d.store.ChannelSecrets(c.Name)
	if err != nil {
		log.Printf("notify: channel %q secret: %v", c.Name, err)
		return
	}
	sender, err := d.build(c, secrets)
	if err != nil {
		log.Printf("notify: build channel %q: %v", c.Name, err)
		return
	}
	const attempts = 3
	for i := 0; i < attempts; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = sender.Send(ctx, msg)
		cancel()
		if err == nil {
			return
		}
		time.Sleep(time.Duration(i+1) * 200 * time.Millisecond)
	}
	log.Printf("notify: channel %q send failed after %d attempts: %v", c.Name, attempts, err)
}
```

Add `"sync"` to the dispatcher import block.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/notify/ -race -v`
Expected: PASS (all notify tests).

- [ ] **Step 6: Commit**

```bash
git add internal/notify/render.go internal/notify/dispatcher.go internal/notify/dispatcher_test.go
git commit -m "feat(m26): dispatcher with cooldown gate, rule match, fan-out

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Phase 3 — Channels

### Task 8: channel factory + webhook (with HMAC)

**Files:**
- Create: `internal/notify/channels/channels.go`
- Create: `internal/notify/channels/webhook.go`
- Test: `internal/notify/channels/webhook_test.go`

**Interfaces:**
- Consumes: `notify.Channel`, `notify.Message`, `notify.Sender`.
- Produces:
  - `type httpDoer interface { Do(*http.Request) (*http.Response, error) }`
  - `var httpClient httpDoer = http.DefaultClient` (package-level seam tests override)
  - `func New(c notify.Channel, secrets map[string]string) (notify.Sender, error)` — dispatches on `c.Type`.
  - webhook payload JSON: `{type, agent, process, detail, time}`; optional header `X-Marshal-Signature: sha256=<hex hmac>` over the raw body when `secrets["hmac"]` is set.

- [ ] **Step 1: Write the failing test**

```go
package channels

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"marshal/internal/notify"
)

type fakeDoer struct {
	req  *http.Request
	body string
}

func (f *fakeDoer) Do(r *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(r.Body)
	f.req, f.body = r, string(b)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}, nil
}

func withDoer(t *testing.T, d httpDoer) {
	t.Helper()
	old := httpClient
	httpClient = d
	t.Cleanup(func() { httpClient = old })
}

func TestWebhookSendsSignedJSON(t *testing.T) {
	fd := &fakeDoer{}
	withDoer(t, fd)
	s, err := New(notify.Channel{Name: "wh", Type: "webhook", Config: map[string]string{"url": "https://example.test/hook"}},
		map[string]string{"hmac": "topsecret"})
	if err != nil {
		t.Fatal(err)
	}
	ev := notify.Event{Type: notify.EventCrash, Agent: "dev-1", Process: "api", Detail: "crashed"}
	if err := s.Send(context.Background(), notify.Message{Title: "t", Body: "b", Event: ev}); err != nil {
		t.Fatal(err)
	}
	if fd.req.URL.String() != "https://example.test/hook" {
		t.Fatalf("url: %s", fd.req.URL)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(fd.body), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["type"] != "crash" || payload["agent"] != "dev-1" || payload["process"] != "api" {
		t.Fatalf("payload: %v", payload)
	}
	mac := hmac.New(sha256.New, []byte("topsecret"))
	mac.Write([]byte(fd.body))
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if got := fd.req.Header.Get("X-Marshal-Signature"); got != want {
		t.Fatalf("signature: got %s want %s", got, want)
	}
}

func TestNewUnknownType(t *testing.T) {
	if _, err := New(notify.Channel{Name: "x", Type: "carrier-pigeon"}, nil); err == nil {
		t.Fatal("expected error for unknown type")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/notify/channels/ -run TestWebhook -v`
Expected: FAIL — package undefined.

- [ ] **Step 3: Write channels.go**

```go
// Package channels implements notification transports behind notify.Sender.
package channels

import (
	"fmt"
	"net/http"

	"marshal/internal/notify"
)

// httpDoer is the HTTP seam; tests override httpClient.
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

var httpClient httpDoer = http.DefaultClient

// New builds a Sender for the channel's type.
func New(c notify.Channel, secrets map[string]string) (notify.Sender, error) {
	switch c.Type {
	case "webhook":
		return newWebhook(c, secrets)
	case "telegram":
		return newTelegram(c, secrets)
	case "slack":
		return newSlack(c, secrets)
	case "email":
		return newEmail(c, secrets)
	default:
		return nil, fmt.Errorf("unknown channel type %q", c.Type)
	}
}
```

- [ ] **Step 4: Write webhook.go**

```go
package channels

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"marshal/internal/notify"
)

type webhook struct {
	url  string
	hmac string
}

func newWebhook(c notify.Channel, secrets map[string]string) (*webhook, error) {
	url := c.Config["url"]
	if url == "" {
		return nil, fmt.Errorf("webhook: url required")
	}
	return &webhook{url: url, hmac: secrets["hmac"]}, nil
}

func (w *webhook) Send(ctx context.Context, m notify.Message) error {
	body, err := json.Marshal(map[string]any{
		"type":    string(m.Event.Type),
		"agent":   m.Event.Agent,
		"process": m.Event.Process,
		"detail":  m.Event.Detail,
		"time":    m.Event.Time.UTC().Format("2006-01-02T15:04:05Z07:00"),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if w.hmac != "" {
		mac := hmac.New(sha256.New, []byte(w.hmac))
		mac.Write(body)
		req.Header.Set("X-Marshal-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	return doExpectOK(req)
}

// doExpectOK runs the request via the seam and treats non-2xx as an error.
func doExpectOK(req *http.Request) error {
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s returned %d", req.URL.Host, resp.StatusCode)
	}
	return nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/notify/channels/ -run 'TestWebhook|TestNewUnknown' -v`
Expected: FAIL to compile until Task 9/10 add `newTelegram`/`newSlack`/`newEmail`. To keep this task self-contained, add temporary stubs at the bottom of `channels.go`:

```go
// temporary stubs — replaced in Tasks 9 and 10
func newTelegram(notify.Channel, map[string]string) (notify.Sender, error) { return nil, fmt.Errorf("telegram: not yet") }
func newSlack(notify.Channel, map[string]string) (notify.Sender, error)    { return nil, fmt.Errorf("slack: not yet") }
func newEmail(notify.Channel, map[string]string) (notify.Sender, error)    { return nil, fmt.Errorf("email: not yet") }
```

Re-run: `go test ./internal/notify/channels/ -run 'TestWebhook|TestNewUnknown' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/notify/channels/channels.go internal/notify/channels/webhook.go internal/notify/channels/webhook_test.go
git commit -m "feat(m26): channel factory + signed webhook transport

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 9: telegram + slack transports

**Files:**
- Create: `internal/notify/channels/telegram.go`
- Create: `internal/notify/channels/slack.go`
- Test: `internal/notify/channels/chat_test.go`
- Modify: `internal/notify/channels/channels.go` (remove the telegram/slack stubs)

**Interfaces:**
- Produces: `func newTelegram(c notify.Channel, secrets map[string]string) (notify.Sender, error)`, `func newSlack(...)`.
- telegram: `POST https://api.telegram.org/bot<bot_token>/sendMessage` body `{"chat_id":..,"text":..}`.
- slack: `POST <webhook_url>` body `{"text":..}`.

- [ ] **Step 1: Write the failing test**

```go
package channels

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/notify"
)

func TestTelegramSend(t *testing.T) {
	fd := &fakeDoer{}
	withDoer(t, fd)
	s, err := New(notify.Channel{Name: "tg", Type: "telegram", Config: map[string]string{"chat_id": "999"}},
		map[string]string{"bot_token": "BOT:TOK"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), notify.Message{Body: "hello"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fd.req.URL.String(), "/botBOT:TOK/sendMessage") {
		t.Fatalf("url: %s", fd.req.URL)
	}
	var p map[string]any
	_ = json.Unmarshal([]byte(fd.body), &p)
	if p["chat_id"] != "999" || p["text"] != "hello" {
		t.Fatalf("body: %v", p)
	}
}

func TestSlackSend(t *testing.T) {
	fd := &fakeDoer{}
	withDoer(t, fd)
	s, err := New(notify.Channel{Name: "sl", Type: "slack"}, map[string]string{"webhook_url": "https://hooks.slack.test/xyz"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), notify.Message{Body: "boom"}); err != nil {
		t.Fatal(err)
	}
	if fd.req.URL.String() != "https://hooks.slack.test/xyz" {
		t.Fatalf("url: %s", fd.req.URL)
	}
	var p map[string]any
	_ = json.Unmarshal([]byte(fd.body), &p)
	if p["text"] != "boom" {
		t.Fatalf("body: %v", p)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/notify/channels/ -run 'TestTelegram|TestSlack' -v`
Expected: FAIL — stubs return "not yet".

- [ ] **Step 3: Write telegram.go**

```go
package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"marshal/internal/notify"
)

type telegram struct {
	token  string
	chatID string
}

func newTelegram(c notify.Channel, secrets map[string]string) (notify.Sender, error) {
	tok := secrets["bot_token"]
	chat := c.Config["chat_id"]
	if tok == "" || chat == "" {
		return nil, fmt.Errorf("telegram: bot_token and chat_id required")
	}
	return &telegram{token: tok, chatID: chat}, nil
}

func (t *telegram) Send(ctx context.Context, m notify.Message) error {
	body, _ := json.Marshal(map[string]string{"chat_id": t.chatID, "text": m.Body})
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return doExpectOK(req)
}
```

- [ ] **Step 4: Write slack.go**

```go
package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"marshal/internal/notify"
)

type slack struct{ webhookURL string }

func newSlack(_ notify.Channel, secrets map[string]string) (notify.Sender, error) {
	url := secrets["webhook_url"]
	if url == "" {
		return nil, fmt.Errorf("slack: webhook_url required")
	}
	return &slack{webhookURL: url}, nil
}

func (s *slack) Send(ctx context.Context, m notify.Message) error {
	body, _ := json.Marshal(map[string]string{"text": m.Body})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.webhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return doExpectOK(req)
}
```

- [ ] **Step 5: Remove the telegram + slack stubs from channels.go**

Delete the two temporary stub functions `newTelegram` and `newSlack` added in Task 8 (keep the `newEmail` stub until Task 10).

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/notify/channels/ -run 'TestTelegram|TestSlack|TestWebhook|TestNewUnknown' -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/notify/channels/telegram.go internal/notify/channels/slack.go internal/notify/channels/chat_test.go internal/notify/channels/channels.go
git commit -m "feat(m26): telegram + slack transports

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 10: email (SMTP) transport

**Files:**
- Create: `internal/notify/channels/email.go`
- Test: `internal/notify/channels/email_test.go`
- Modify: `internal/notify/channels/channels.go` (remove the email stub)

**Interfaces:**
- Produces: `func newEmail(c notify.Channel, secrets map[string]string) (notify.Sender, error)`.
- Config: `host`, `port`, `from`, `to`, `username`; secret `password`.
- SMTP send seam: `var smtpSend = smtp.SendMail` (package var tests override).

- [ ] **Step 1: Write the failing test**

```go
package channels

import (
	"context"
	"net/smtp"
	"strings"
	"testing"

	"marshal/internal/notify"
)

func TestEmailSend(t *testing.T) {
	var gotAddr, gotFrom string
	var gotTo []string
	var gotMsg string
	old := smtpSend
	smtpSend = func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		gotAddr, gotFrom, gotTo, gotMsg = addr, from, to, string(msg)
		return nil
	}
	t.Cleanup(func() { smtpSend = old })

	s, err := New(notify.Channel{Name: "mail", Type: "email", Config: map[string]string{
		"host": "smtp.test", "port": "587", "from": "marshal@test", "to": "ops@test", "username": "marshal@test",
	}}, map[string]string{"password": "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), notify.Message{Title: "Subj", Body: "Body"}); err != nil {
		t.Fatal(err)
	}
	if gotAddr != "smtp.test:587" || gotFrom != "marshal@test" || len(gotTo) != 1 || gotTo[0] != "ops@test" {
		t.Fatalf("envelope wrong: %s %s %v", gotAddr, gotFrom, gotTo)
	}
	if !strings.Contains(gotMsg, "Subject: Subj") || !strings.Contains(gotMsg, "Body") {
		t.Fatalf("message wrong: %q", gotMsg)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/notify/channels/ -run TestEmail -v`
Expected: FAIL — stub returns "not yet".

- [ ] **Step 3: Write email.go**

```go
package channels

import (
	"context"
	"fmt"
	"net/smtp"

	"marshal/internal/notify"
)

// smtpSend is the SMTP seam; tests override it.
var smtpSend = smtp.SendMail

type email struct {
	host, port, from, to, username, password string
}

func newEmail(c notify.Channel, secrets map[string]string) (notify.Sender, error) {
	e := &email{
		host:     c.Config["host"],
		port:     c.Config["port"],
		from:     c.Config["from"],
		to:       c.Config["to"],
		username: c.Config["username"],
		password: secrets["password"],
	}
	if e.host == "" || e.port == "" || e.from == "" || e.to == "" {
		return nil, fmt.Errorf("email: host, port, from, to required")
	}
	return e, nil
}

func (e *email) Send(_ context.Context, m notify.Message) error {
	msg := []byte(fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s\r\n",
		e.from, e.to, m.Title, m.Body))
	var auth smtp.Auth
	if e.username != "" {
		auth = smtp.PlainAuth("", e.username, e.password, e.host)
	}
	return smtpSend(e.host+":"+e.port, auth, e.from, []string{e.to}, msg)
}
```

- [ ] **Step 4: Remove the email stub from channels.go**

Delete the temporary `newEmail` stub added in Task 8.

- [ ] **Step 5: Run the full channels suite**

Run: `go test ./internal/notify/channels/ -race -count=1 -v`
Expected: PASS (webhook, telegram, slack, email, unknown-type).

- [ ] **Step 6: Commit**

```bash
git add internal/notify/channels/email.go internal/notify/channels/email_test.go internal/notify/channels/channels.go
git commit -m "feat(m26): email (SMTP) transport

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Phase 4 — Dashboard API

### Task 11: notifications HTTP handlers (CRUD + settings + test-send)

**Files:**
- Create: `internal/dashboard/notifications.go`
- Test: `internal/dashboard/notifications_test.go`
- Modify: `internal/dashboard/handlers.go` (add `notifs` field + routes)

**Interfaces:**
- Consumes: `notify.Channel`, `notify.Rule`, `notify.Settings`, `notify.Sender`, `channels.New`.
- Produces (on `handler`):
  - field `notifs Notifications` and `notifBuild notify.BuildFunc`
  - `type Notifications interface { Channels() []notify.Channel; HasSecret(name string) bool; PutChannel(notify.Channel, map[string]string) error; DeleteChannel(name string) bool; ChannelSecrets(name string) (map[string]string, bool, error); Rules() []notify.Rule; PutRule(notify.Rule) error; DeleteRule(name string) bool; Settings() notify.Settings; SetSettings(notify.Settings) error }` (the `*notify.Store` satisfies it)
  - handlers `getNotifications`, `putChannel`, `deleteChannelHandler`, `testChannel`, `putRule`, `deleteRuleHandler`, `putSettings`.

- [ ] **Step 1: Write the failing test**

```go
package dashboard

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"marshal/internal/notify"
)

type fakeNotifs struct {
	channels []notify.Channel
	rules    []notify.Rule
	settings notify.Settings
	secrets  map[string]map[string]string
}

func (f *fakeNotifs) Channels() []notify.Channel { return f.channels }
func (f *fakeNotifs) HasSecret(name string) bool { _, ok := f.secrets[name]; return ok }
func (f *fakeNotifs) PutChannel(c notify.Channel, s map[string]string) error {
	f.channels = append(f.channels, c)
	if f.secrets == nil {
		f.secrets = map[string]map[string]string{}
	}
	if len(s) > 0 {
		f.secrets[c.Name] = s
	}
	return nil
}
func (f *fakeNotifs) DeleteChannel(name string) bool { return true }
func (f *fakeNotifs) ChannelSecrets(name string) (map[string]string, bool, error) {
	return f.secrets[name], true, nil
}
func (f *fakeNotifs) Rules() []notify.Rule        { return f.rules }
func (f *fakeNotifs) PutRule(r notify.Rule) error { f.rules = append(f.rules, r); return nil }
func (f *fakeNotifs) DeleteRule(name string) bool { return true }
func (f *fakeNotifs) Settings() notify.Settings   { return f.settings }
func (f *fakeNotifs) SetSettings(s notify.Settings) error { f.settings = s; return nil }

// testHandlerWithNotifs builds a *handler wired with a fake notifs store and an
// authenticated session, returning the handler and a session cookie.
func testHandlerWithNotifs(t *testing.T, n Notifications) *handler {
	t.Helper()
	h := newHandler(nil, nil, nil, nil, nil, 0, "", "", nil)
	h.notifs = n
	h.notifBuild = func(c notify.Channel, _ map[string]string) (notify.Sender, error) {
		return senderFunc(func() error { return nil }), nil
	}
	return h
}

type senderFunc func() error

func (s senderFunc) Send(_ interface{ Done() <-chan struct{} }, _ notify.Message) error { return s() }

func TestGetNotificationsRedactsSecrets(t *testing.T) {
	n := &fakeNotifs{channels: []notify.Channel{{Name: "tg", Type: "telegram", Enabled: true}}, secrets: map[string]map[string]string{"tg": {"bot_token": "SECRET"}}}
	h := testHandlerWithNotifs(t, n)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/notifications", nil)
	h.getNotifications(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "SECRET") {
		t.Fatal("secret leaked in GET response")
	}
	var out struct {
		Channels []struct {
			Name      string `json:"name"`
			HasSecret bool   `json:"has_secret"`
		} `json:"channels"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Channels) != 1 || !out.Channels[0].HasSecret {
		t.Fatalf("has_secret not surfaced: %+v", out.Channels)
	}
}

func TestPutChannelCreates(t *testing.T) {
	n := &fakeNotifs{}
	h := testHandlerWithNotifs(t, n)
	body, _ := json.Marshal(map[string]any{
		"name": "wh", "type": "webhook", "enabled": true,
		"config": map[string]string{"url": "https://x.test"}, "secrets": map[string]string{"hmac": "k"},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/notifications/channels", bytes.NewReader(body))
	h.putChannel(rec, req)
	if rec.Code != 201 {
		t.Fatalf("code %d body %s", rec.Code, rec.Body)
	}
	if len(n.channels) != 1 || n.channels[0].Name != "wh" || n.secrets["wh"]["hmac"] != "k" {
		t.Fatalf("channel not stored: %+v", n.channels)
	}
}
```

Note: the `senderFunc.Send` signature in the test sketch is illustrative — match it to the real `notify.Sender` (`Send(context.Context, notify.Message) error`). Use `context` in the test file and a `senderFunc` whose method is `func (s senderFunc) Send(context.Context, notify.Message) error { return s() }`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dashboard/ -run TestGetNotifications -v`
Expected: FAIL — `notifs`/`getNotifications` undefined.

- [ ] **Step 3: Add fields to the handler struct**

In `internal/dashboard/handlers.go`, add to the `handler` struct:

```go
	notifs     Notifications
	notifBuild notify.BuildFunc
```

Add `"marshal/internal/notify"` to imports. In `newHandler`, leave them zero by default (wired by `Serve` in Task 12). Register routes in the mux (after the credentials routes):

```go
	mux.HandleFunc("GET /api/notifications", h.requireSession(h.getNotifications))
	mux.HandleFunc("POST /api/notifications/channels", h.requireSession(h.putChannel))
	mux.HandleFunc("DELETE /api/notifications/channels/{name}", h.requireSession(h.deleteChannelHandler))
	mux.HandleFunc("POST /api/notifications/channels/{name}/test", h.requireSession(h.testChannel))
	mux.HandleFunc("POST /api/notifications/rules", h.requireSession(h.putRule))
	mux.HandleFunc("DELETE /api/notifications/rules/{name}", h.requireSession(h.deleteRuleHandler))
	mux.HandleFunc("PUT /api/notifications/settings", h.requireSession(h.putSettings))
```

- [ ] **Step 4: Write notifications.go**

```go
package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"marshal/internal/notify"
)

// Notifications is the subset of *notify.Store the dashboard needs.
type Notifications interface {
	Channels() []notify.Channel
	HasSecret(name string) bool
	PutChannel(c notify.Channel, secrets map[string]string) error
	DeleteChannel(name string) bool
	ChannelSecrets(name string) (map[string]string, bool, error)
	Rules() []notify.Rule
	PutRule(r notify.Rule) error
	DeleteRule(name string) bool
	Settings() notify.Settings
	SetSettings(s notify.Settings) error
}

type channelView struct {
	Name      string            `json:"name"`
	Type      string            `json:"type"`
	Enabled   bool              `json:"enabled"`
	Config    map[string]string `json:"config"`
	HasSecret bool              `json:"has_secret"`
}

func (h *handler) notifsReady(w http.ResponseWriter) bool {
	if h.notifs == nil {
		http.Error(w, "notifications unavailable", http.StatusServiceUnavailable)
		return false
	}
	return true
}

func (h *handler) getNotifications(w http.ResponseWriter, r *http.Request) {
	if !h.notifsReady(w) {
		return
	}
	views := make([]channelView, 0)
	for _, c := range h.notifs.Channels() {
		views = append(views, channelView{Name: c.Name, Type: c.Type, Enabled: c.Enabled, Config: c.Config, HasSecret: h.notifs.HasSecret(c.Name)})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"channels": views,
		"rules":    h.notifs.Rules(),
		"settings": h.notifs.Settings(),
	})
}

type channelReq struct {
	Name    string            `json:"name"`
	Type    string            `json:"type"`
	Enabled bool              `json:"enabled"`
	Config  map[string]string `json:"config"`
	Secrets map[string]string `json:"secrets"`
}

func (h *handler) putChannel(w http.ResponseWriter, r *http.Request) {
	if !h.notifsReady(w) {
		return
	}
	var body channelReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Name == "" || body.Type == "" {
		http.Error(w, "name and type required", http.StatusBadRequest)
		return
	}
	ch := notify.Channel{Name: body.Name, Type: body.Type, Enabled: body.Enabled, Config: body.Config}
	if err := h.notifs.PutChannel(ch, body.Secrets); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

func (h *handler) deleteChannelHandler(w http.ResponseWriter, r *http.Request) {
	if !h.notifsReady(w) {
		return
	}
	if !h.notifs.DeleteChannel(r.PathValue("name")) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) testChannel(w http.ResponseWriter, r *http.Request) {
	if !h.notifsReady(w) {
		return
	}
	name := r.PathValue("name")
	var target *notify.Channel
	for _, c := range h.notifs.Channels() {
		if c.Name == name {
			cc := c
			target = &cc
			break
		}
	}
	if target == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	secrets, _, err := h.notifs.ChannelSecrets(name)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	sender, err := h.notifBuild(*target, secrets)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	msg := notify.Message{
		Title: "Marshal test notification",
		Body:  "This is a test message from Marshal.",
		Event: notify.Event{Type: "test", Agent: "marshal", Detail: "test", Time: time.Now()},
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := sender.Send(ctx, msg); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *handler) putRule(w http.ResponseWriter, r *http.Request) {
	if !h.notifsReady(w) {
		return
	}
	var rule notify.Rule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if rule.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	if err := h.notifs.PutRule(rule); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

func (h *handler) deleteRuleHandler(w http.ResponseWriter, r *http.Request) {
	if !h.notifsReady(w) {
		return
	}
	if !h.notifs.DeleteRule(r.PathValue("name")) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) putSettings(w http.ResponseWriter, r *http.Request) {
	if !h.notifsReady(w) {
		return
	}
	var s notify.Settings
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := h.notifs.SetSettings(s); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
```

(If `writeJSON` is defined elsewhere in the package, reuse it; do not redefine.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/dashboard/ -run 'TestGetNotifications|TestPutChannel' -race -v`
Expected: PASS.

- [ ] **Step 6: Run the whole dashboard suite (no regressions)**

Run: `go test ./internal/dashboard/ -race -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/dashboard/notifications.go internal/dashboard/notifications_test.go internal/dashboard/handlers.go
git commit -m "feat(m26): dashboard notification CRUD + test-send (secrets write-only)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Phase 5 — Web UI

### Task 12: Notifications dashboard page

**Files:**
- Modify: `web/src/router.ts` (add `notifications` route)
- Modify: `web/src/api.ts` (add notification API calls + types)
- Create: `web/src/Notifications.tsx`
- Modify: `web/src/App.tsx` (render the new route + nav link)

**Interfaces:**
- Consumes the Task 11 endpoints.
- Produces TS types `NotifChannel`, `NotifRule`, `NotifSettings`, `NotifConfig` and functions `getNotifications`, `putChannel`, `deleteChannel`, `testChannel`, `putRule`, `deleteRule`, `putNotifSettings`.

- [ ] **Step 1: Extend the route union**

In `web/src/router.ts`, add `{ name: "notifications" }` to the `Route` type and parse `#/notifications` in `parseHash` (mirror the existing `#/credentials` case).

- [ ] **Step 2: Add API calls + types to api.ts**

Append to `web/src/api.ts` (follow the existing non-throwing-on-error convention used by `listCredentials`/`createCredential`):

```ts
export type NotifChannel = {
  name: string;
  type: "webhook" | "telegram" | "slack" | "email";
  enabled: boolean;
  config: Record<string, string>;
  has_secret: boolean;
};
export type NotifRule = {
  name: string;
  enabled: boolean;
  events: string[];
  agent: string;
  process: string;
  channels: string[];
};
export type NotifSettings = { cooldown_seconds: number };
export type NotifConfig = { channels: NotifChannel[]; rules: NotifRule[]; settings: NotifSettings };

export async function getNotifications(): Promise<NotifConfig> {
  const r = await fetch("/api/notifications", { credentials: "same-origin" });
  if (r.status === 401) throw new Error("unauthorized");
  if (!r.ok) return { channels: [], rules: [], settings: { cooldown_seconds: 300 } };
  return r.json();
}

export async function putChannel(body: {
  name: string; type: string; enabled: boolean;
  config: Record<string, string>; secrets: Record<string, string>;
}): Promise<{ ok: boolean; error?: string }> {
  const r = await fetch("/api/notifications/channels", {
    method: "POST", credentials: "same-origin",
    headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
  });
  if (r.ok) return { ok: true };
  return { ok: false, error: await r.text() };
}

export async function deleteChannel(name: string): Promise<{ ok: boolean }> {
  const r = await fetch(`/api/notifications/channels/${encodeURIComponent(name)}`, {
    method: "DELETE", credentials: "same-origin",
  });
  return { ok: r.ok };
}

export async function testChannel(name: string): Promise<{ ok: boolean; error?: string }> {
  const r = await fetch(`/api/notifications/channels/${encodeURIComponent(name)}/test`, {
    method: "POST", credentials: "same-origin",
  });
  if (!r.ok) return { ok: false, error: await r.text() };
  return r.json();
}

export async function putRule(rule: NotifRule): Promise<{ ok: boolean; error?: string }> {
  const r = await fetch("/api/notifications/rules", {
    method: "POST", credentials: "same-origin",
    headers: { "Content-Type": "application/json" }, body: JSON.stringify(rule),
  });
  if (r.ok) return { ok: true };
  return { ok: false, error: await r.text() };
}

export async function deleteRule(name: string): Promise<{ ok: boolean }> {
  const r = await fetch(`/api/notifications/rules/${encodeURIComponent(name)}`, {
    method: "DELETE", credentials: "same-origin",
  });
  return { ok: r.ok };
}

export async function putNotifSettings(s: NotifSettings): Promise<{ ok: boolean }> {
  const r = await fetch("/api/notifications/settings", {
    method: "PUT", credentials: "same-origin",
    headers: { "Content-Type": "application/json" }, body: JSON.stringify(s),
  });
  return { ok: r.ok };
}
```

- [ ] **Step 3: Create Notifications.tsx**

Create `web/src/Notifications.tsx` modeled on `Credentials.tsx`: a component that loads `getNotifications()` on mount and renders three sections — Channels, Rules, Settings. Function-first styling (reuse existing classes; no new CSS required).

```tsx
import { useEffect, useState } from "react";
import {
  getNotifications, putChannel, deleteChannel, testChannel,
  putRule, deleteRule, putNotifSettings,
  type NotifConfig, type NotifChannel, type NotifRule,
} from "./api";

const EVENT_TYPES = ["crash", "restart_loop", "agent_down", "agent_up", "deploy_fail"];
const CHANNEL_TYPES = ["webhook", "telegram", "slack", "email"];

// config + secret field names per channel type (secret fields are write-only)
const CONFIG_FIELDS: Record<string, string[]> = {
  webhook: ["url"],
  telegram: ["chat_id"],
  slack: [],
  email: ["host", "port", "from", "to", "username", "tls"],
};
const SECRET_FIELDS: Record<string, string[]> = {
  webhook: ["hmac"],
  telegram: ["bot_token"],
  slack: ["webhook_url"],
  email: ["password"],
};

export function Notifications() {
  const [cfg, setCfg] = useState<NotifConfig | null>(null);
  const [err, setErr] = useState("");

  async function reload() {
    try {
      setCfg(await getNotifications());
    } catch {
      setErr("failed to load");
    }
  }
  useEffect(() => {
    reload();
  }, []);

  if (!cfg) return <div className="panel">Loading… {err}</div>;

  return (
    <div className="panel">
      <h2>Notifications</h2>
      {err && <div className="error">{err}</div>}
      <ChannelSection cfg={cfg} onChange={reload} />
      <RuleSection cfg={cfg} onChange={reload} />
      <SettingsSection cfg={cfg} onChange={reload} />
    </div>
  );
}

function ChannelSection({ cfg, onChange }: { cfg: NotifConfig; onChange: () => void }) {
  const [type, setType] = useState("webhook");
  const [name, setName] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [fields, setFields] = useState<Record<string, string>>({});
  const [msg, setMsg] = useState("");

  async function submit() {
    const config: Record<string, string> = {};
    const secrets: Record<string, string> = {};
    for (const f of CONFIG_FIELDS[type]) config[f] = fields[f] ?? "";
    for (const f of SECRET_FIELDS[type]) if (fields[f]) secrets[f] = fields[f];
    const res = await putChannel({ name, type, enabled, config, secrets });
    setMsg(res.ok ? "saved" : res.error ?? "error");
    if (res.ok) {
      setName("");
      setFields({});
      onChange();
    }
  }

  return (
    <section>
      <h3>Channels</h3>
      <ul>
        {cfg.channels.map((c: NotifChannel) => (
          <li key={c.name}>
            <strong>{c.name}</strong> ({c.type}) {c.enabled ? "on" : "off"}{" "}
            {c.has_secret ? "🔒" : ""}
            <button onClick={async () => { const r = await testChannel(c.name); setMsg(r.ok ? "test sent" : r.error ?? "test failed"); }}>Test</button>
            <button onClick={async () => { await deleteChannel(c.name); onChange(); }}>Delete</button>
          </li>
        ))}
      </ul>
      <div>
        <select value={type} onChange={(e) => { setType(e.target.value); setFields({}); }}>
          {CHANNEL_TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
        </select>
        <input placeholder="name" value={name} onChange={(e) => setName(e.target.value)} />
        <label><input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} /> enabled</label>
        {[...CONFIG_FIELDS[type], ...SECRET_FIELDS[type]].map((f) => (
          <input
            key={f}
            placeholder={f}
            type={SECRET_FIELDS[type].includes(f) ? "password" : "text"}
            value={fields[f] ?? ""}
            onChange={(e) => setFields({ ...fields, [f]: e.target.value })}
          />
        ))}
        <button onClick={submit}>Save channel</button>
        <span>{msg}</span>
      </div>
    </section>
  );
}

function RuleSection({ cfg, onChange }: { cfg: NotifConfig; onChange: () => void }) {
  const [name, setName] = useState("");
  const [events, setEvents] = useState<string[]>([]);
  const [agent, setAgent] = useState("*");
  const [process, setProcess] = useState("*");
  const [chans, setChans] = useState<string[]>([]);
  const [msg, setMsg] = useState("");

  function toggle(list: string[], v: string, set: (x: string[]) => void) {
    set(list.includes(v) ? list.filter((x) => x !== v) : [...list, v]);
  }

  async function submit() {
    const rule: NotifRule = { name, enabled: true, events, agent, process, channels: chans };
    const res = await putRule(rule);
    setMsg(res.ok ? "saved" : res.error ?? "error");
    if (res.ok) { setName(""); setEvents([]); setChans([]); onChange(); }
  }

  return (
    <section>
      <h3>Rules</h3>
      <ul>
        {cfg.rules.map((r: NotifRule) => (
          <li key={r.name}>
            <strong>{r.name}</strong>: {r.events.length ? r.events.join(",") : "any"} ·
            {r.agent || "*"}/{r.process || "*"} → {r.channels.join(",")}
            <button onClick={async () => { await deleteRule(r.name); onChange(); }}>Delete</button>
          </li>
        ))}
      </ul>
      <div>
        <input placeholder="rule name" value={name} onChange={(e) => setName(e.target.value)} />
        <div>{EVENT_TYPES.map((ev) => (
          <label key={ev}><input type="checkbox" checked={events.includes(ev)} onChange={() => toggle(events, ev, setEvents)} /> {ev}</label>
        ))}</div>
        <input placeholder="agent (* = any)" value={agent} onChange={(e) => setAgent(e.target.value)} />
        <input placeholder="process (* = any)" value={process} onChange={(e) => setProcess(e.target.value)} />
        <div>{cfg.channels.map((c) => (
          <label key={c.name}><input type="checkbox" checked={chans.includes(c.name)} onChange={() => toggle(chans, c.name, setChans)} /> {c.name}</label>
        ))}</div>
        <button onClick={submit}>Save rule</button>
        <span>{msg}</span>
      </div>
    </section>
  );
}

function SettingsSection({ cfg, onChange }: { cfg: NotifConfig; onChange: () => void }) {
  const [cooldown, setCooldown] = useState(cfg.settings.cooldown_seconds);
  return (
    <section>
      <h3>Settings</h3>
      <label>Cooldown (seconds): <input type="number" value={cooldown} onChange={(e) => setCooldown(Number(e.target.value))} /></label>
      <button onClick={async () => { await putNotifSettings({ cooldown_seconds: cooldown }); onChange(); }}>Save</button>
    </section>
  );
}
```

- [ ] **Step 4: Wire the route in App.tsx**

In `web/src/App.tsx`, import `{ Notifications }` and render it when `route.name === "notifications"` (mirror the Credentials case), and add a nav link `<a href="#/notifications">Notifications</a>` next to the existing Credentials link.

- [ ] **Step 5: Build the UI bundle**

Run: `make ui`
Expected: rebuilds `internal/dashboard/dist` with no TypeScript/Vite errors.

- [ ] **Step 6: Verify Go still builds with the embedded bundle**

Run: `go build -o marshal ./cmd/marshal`
Expected: success.

- [ ] **Step 7: Commit**

```bash
git add web/src/router.ts web/src/api.ts web/src/Notifications.tsx web/src/App.tsx internal/dashboard/dist
git commit -m "feat(m26): notifications dashboard page

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Phase 6 — Wiring + Demo

### Task 13: wire detector + dispatcher + store into the server

**Files:**
- Modify: `internal/server/server.go` (construct box/store/dispatcher/detector; start loop; pass store + builder to dashboard)
- Modify: `internal/dashboard/server.go` (thread the notify store + builder through `Serve`)

**Interfaces:**
- Consumes: `secretbox.Load`, `notify.Open`, `notify.NewDispatcher`, `notify.NewDetector`, `channels.New`.
- Produces: `dashboard.Serve` gains two params: `notifs Notifications`, `notifBuild notify.BuildFunc` (pass `nil` from any other caller — e.g. `NewHandler` test helper stays nil).

- [ ] **Step 1: Extend dashboard.Serve + newHandler wiring**

In `internal/dashboard/server.go`, change `Serve` to accept the notify store and builder and assign them onto the handler:

```go
func Serve(ctx context.Context, addr string, lister FleetLister, metrics MetricsHistory, logs LogsHistory, controller FleetController, auth Authenticator, cert tls.Certificate, sessionsPath, auditPath string, creds Credentials, notifs Notifications, notifBuild notify.BuildFunc) error {
	h := newHandler(lister, metrics, logs, controller, auth, 24*time.Hour, sessionsPath, auditPath, creds)
	h.notifs = notifs
	h.notifBuild = notifBuild
	// ... existing TLS server setup unchanged, serving h.mux
}
```

Add `"marshal/internal/notify"` to that file's imports.

- [ ] **Step 2: Construct + wire in server.go**

In `internal/server/server.go`, in the block that opens credstore and starts the dashboard (around the existing `creds, cerr := credstore.Open(dataDir)`), add:

```go
	// Notification service: detector polls the registry; dispatcher routes to channels.
	var notifStore *notify.Store
	if box, berr := secretbox.Load(dataDir); berr != nil {
		log.Printf("server: notifications disabled: %v", berr)
	} else if ns, nerr := notify.Open(dataDir, box); nerr != nil {
		log.Printf("server: notifications disabled: %v", nerr)
	} else {
		notifStore = ns
		disp := notify.NewDispatcher(ns, channels.New)
		det := notify.NewDetector(reg, disp, 2*time.Second)
		go det.Run(ctx)
	}
	var nw dashboard.Notifications
	if notifStore != nil {
		nw = notifStore
	}
```

Then update the `dashboard.Serve(...)` call to pass `nw, channels.New` as the two new trailing args:

```go
		if err := dashboard.Serve(ctx, httpAddr, reg, ss, ls, srv, auth, cert, sessionsPath, auditPath, cw, nw, channels.New); err != nil {
```

Add imports `"marshal/internal/notify"`, `"marshal/internal/notify/channels"`, `"marshal/internal/secretbox"` to `server.go`.

- [ ] **Step 3: Build + vet + full test**

Run:
```bash
go build -o marshal ./cmd/marshal
go vet ./...
gofmt -l .
go test ./... -race -count=1
```
Expected: binary builds; vet clean; gofmt silent; all tests pass (the `cmd/marshal` SIGINT test may flake under heavy load — re-run `go test ./cmd/marshal/ -run TestRunSupervises -count=1` in isolation if so).

- [ ] **Step 4: Commit**

```bash
git add internal/server/server.go internal/dashboard/server.go
git commit -m "feat(m26): wire notification detector/dispatcher into the server

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 14: live demo + handoff

**Files:**
- Create: `docs/handoffs/2026-06-21-m26-notification-service.md`

- [ ] **Step 1: Run the live demo per CLAUDE.md conventions**

Use a scratch data dir under `/tmp/marshal-m26-demo`, standard demo ports `:9000`/`:9001` (per the demo memories). With the server **down**: set the dashboard password and rotate an enroll token, capture the fingerprint. Then start the server with `--http-listen`, enroll one agent. Start a small webhook sink (e.g. a tiny Go HTTP server or `nc -l 9099` loop) and:
1. Create a **webhook** channel pointing at the sink + a rule routing `crash` to it (via the dashboard API or UI).
2. Start a demo process that crashes/restart-loops; confirm the sink receives the expected JSON `{type:"crash",...}` and that the **cooldown** suppresses repeats (one alert, not a storm).
3. Force a **deploy failure** (bad repo) and confirm a `deploy_fail` event fires.
4. Click **Test** on the channel from the dashboard and confirm the sink receives the test payload.
5. Capture a screenshot of the `#/notifications` page (per the demo-viewable memory).

Confirm `notifications.json` on disk contains **no plaintext secret** (HMAC sealed). Tear down by data dir + pid; verify `pgrep -fl marshal` shows no demo orphans; preserve the user's standing launchd daemon.

- [ ] **Step 2: Write the handoff**

Document: current state (branch, what's merged), what changed + key decisions, build/run/test commands, deferred items (Telegram/Slack/email verified only by unit tests + manual; glob matchers; recovery notices; UI styling polish), and the concrete next step (merge `--no-ff` to `main`; follow-up milestones). Include the live-demo observations.

- [ ] **Step 3: Commit + finish the branch**

```bash
git add docs/handoffs/2026-06-21-m26-notification-service.md
git commit -m "docs(m26): notification-service handoff

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

Then invoke the `superpowers:finishing-a-development-branch` skill to choose merge/PR.

---

## Self-Review

**Spec coverage:**
- Four event types → Task 5 `diff` (crash/restart_loop/deploy_fail) + agent_down/up. ✓
- Four channels → Tasks 8–10. ✓
- Full rules engine (event + agent + process → channels) → Task 3 `Rule.Matches` + Task 7 `matchChannels`. ✓
- Per-event-key cooldown → Task 7 `allow`. ✓
- Sealed secrets reusing master key → Tasks 1, 2, 4. ✓
- Dashboard CRUD + test-send, secrets write-only → Task 11. ✓
- React page → Task 12. ✓
- Wiring + hot in-process store → Task 13. ✓
- Live demo + handoff → Task 14. ✓
- No proto/agent changes → detection via `registry.List()` only. ✓

**Placeholder scan:** Task 8/9/10 use explicit temporary stubs that are deliberately removed in later steps (documented, not open-ended). The Task 11 test note flags the illustrative `senderFunc` signature and gives the correct one. No "TBD"/"add error handling"-style gaps.

**Type consistency:** `Sender.Send(context.Context, Message) error` used consistently across notify, channels, dashboard. `BuildFunc`/`channels.New` signatures match `func(notify.Channel, map[string]string) (notify.Sender, error)`. `Notifications` interface in dashboard matches `*notify.Store` method set. `Lister`/`Emitter` match `*server.Registry` and `*Dispatcher`. Cooldown key construction identical in test and impl.
