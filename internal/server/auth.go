package server

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/REDDE4D/marshal-pm/internal/audit"
	"github.com/REDDE4D/marshal-pm/internal/fleetauth"
	"github.com/REDDE4D/marshal-pm/internal/ratelimit"
)

type authAgentEntry struct {
	TokenHash  string `json:"token_hash"`
	EnrolledAt int64  `json:"enrolled_at"`
}

type dashboardUser struct {
	PBKDF2 string `json:"pbkdf2"` // base64 std of the derived key
	Salt   string `json:"salt"`   // base64 std of the random salt
	Iter   int    `json:"iter"`
}

type authData struct {
	EnrollTokenHash string                    `json:"enroll_token_hash"`
	AdminTokenHash  string                    `json:"admin_token_hash"`
	Agents          map[string]authAgentEntry `json:"agents"`
	Users           map[string]dashboardUser  `json:"users,omitempty"`
}

// grpcThrottlePolicy is the default per-IP lockout for gRPC auth failures: 10
// consecutive failures → 5s lock, doubling up to 5min. Higher threshold than the
// dashboard since agents reconnect; a success resets the IP (see ratelimit).
func grpcThrottlePolicy() ratelimit.Policy {
	return ratelimit.Policy{Threshold: 10, Base: 5 * time.Second, Cap: 5 * time.Minute}
}

// AuthStore holds the persisted auth tokens for a Marshal server instance.
type AuthStore struct {
	path     string
	mu       sync.Mutex
	data     authData
	mtime    time.Time
	audit    *audit.Log         // optional; records gRPC auth failures. Set once at startup.
	throttle *ratelimit.Limiter // per-IP gRPC auth-failure lockout.
}

// SetAuditLog attaches an audit log so the gRPC interceptors record auth
// failures. Call once during startup, before serving. A nil log disables it.
func (a *AuthStore) SetAuditLog(l *audit.Log) { a.audit = l }

// SetThrottle overrides the per-IP gRPC auth throttle (used by tests; a default
// is installed at construction).
func (a *AuthStore) SetThrottle(l *ratelimit.Limiter) { a.throttle = l }

// InitSecrets carries the plaintext tokens generated on first init.
// It is non-nil only when auth.json is created for the first time.
type InitSecrets struct {
	EnrollToken string
	AdminToken  string
}

// loadOrInitAuth is the internal variant (returns unexported aliases for
// backwards compat with internal callers that already use the exported types).
func loadOrInitAuth(dir string) (*AuthStore, *InitSecrets, error) {
	return LoadOrInitAuth(dir)
}

// LoadOrInitAuth loads or creates the auth store for dir.
// On first call it creates auth.json and returns the plaintext tokens in
// secrets; on subsequent calls secrets is nil (tokens are only available once).
// It creates dir (0700) if absent.
func LoadOrInitAuth(dir string) (*AuthStore, *InitSecrets, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("create data dir: %w", err)
	}
	path := filepath.Join(dir, "auth.json")
	a := &AuthStore{
		path:     path,
		data:     authData{Agents: map[string]authAgentEntry{}, Users: map[string]dashboardUser{}},
		throttle: ratelimit.New(grpcThrottlePolicy(), nil),
	}
	b, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(b, &a.data); err != nil {
			return nil, nil, fmt.Errorf("parse auth.json: %w", err)
		}
		if a.data.Agents == nil {
			a.data.Agents = map[string]authAgentEntry{}
		}
		if a.data.Users == nil {
			a.data.Users = map[string]dashboardUser{}
		}
		if fi, statErr := os.Stat(path); statErr == nil {
			a.mtime = fi.ModTime()
		}
		return a, nil, nil
	}
	if !os.IsNotExist(err) {
		return nil, nil, err
	}
	enroll, err := fleetauth.GenerateToken()
	if err != nil {
		return nil, nil, err
	}
	admin, err := fleetauth.GenerateToken()
	if err != nil {
		return nil, nil, err
	}
	a.data.EnrollTokenHash = fleetauth.HashToken(enroll)
	a.data.AdminTokenHash = fleetauth.HashToken(admin)
	if err := a.save(); err != nil {
		return nil, nil, err
	}
	if fi, statErr := os.Stat(path); statErr == nil {
		a.mtime = fi.ModTime()
	}
	return a, &InitSecrets{EnrollToken: enroll, AdminToken: admin}, nil
}

// save writes auth.json atomically (0600). Caller holds a.mu, or it is called
// during init before the store is shared.
func (a *AuthStore) save() error {
	b, err := json.MarshalIndent(a.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, a.path); err != nil {
		return err
	}
	if fi, statErr := os.Stat(a.path); statErr == nil {
		a.mtime = fi.ModTime()
	}
	return nil
}

// Reload re-reads auth.json if its modification time changed since the last
// successful load, swapping in the new data under the lock. A read or parse
// error leaves the current in-memory data intact and is returned to the caller.
// Atomic-rename writes guarantee we never observe a half-written file.
//
// The file read and JSON parse happen outside a.mu so the gRPC verify hot path
// is not blocked while reading from disk; the lock is taken only to snapshot the
// current mtime and, after a successful parse, to swap the new data in.
func (a *AuthStore) Reload() error {
	fi, err := os.Stat(a.path)
	if err != nil {
		return err
	}
	a.mu.Lock()
	unchanged := fi.ModTime().Equal(a.mtime)
	a.mu.Unlock()
	if unchanged {
		return nil // unchanged — cheap no-op, no disk read
	}
	b, err := os.ReadFile(a.path)
	if err != nil {
		return err
	}
	var fresh authData
	if err := json.Unmarshal(b, &fresh); err != nil {
		return fmt.Errorf("parse auth.json: %w", err)
	}
	if fresh.Agents == nil {
		fresh.Agents = map[string]authAgentEntry{}
	}
	if fresh.Users == nil {
		fresh.Users = map[string]dashboardUser{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	// Re-check under the lock: a concurrent save() may have written newer data
	// (and a newer mtime) while we read. Only swap if the file we parsed is still
	// strictly newer than what's in memory, so we never clobber a fresher write.
	if fi.ModTime().After(a.mtime) {
		a.data = fresh
		a.mtime = fi.ModTime()
	}
	return nil
}

// ReloadLoop polls Reload every interval until ctx is canceled, so a running
// server picks up `server passwd` / `token --rotate` changes without a restart.
func (a *AuthStore) ReloadLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := a.Reload(); err != nil {
				log.Printf("auth: reload failed: %v", err)
			}
		}
	}
}

func (a *AuthStore) verifyAdmin(token string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return fleetauth.VerifyToken(token, a.data.AdminTokenHash)
}

func (a *AuthStore) verifyEnroll(token string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return fleetauth.VerifyToken(token, a.data.EnrollTokenHash)
}

func (a *AuthStore) enrollAgent(name string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, exists := a.data.Agents[name]; exists {
		return "", fmt.Errorf("agent %q already enrolled", name)
	}
	tok, err := fleetauth.GenerateToken()
	if err != nil {
		return "", err
	}
	a.data.Agents[name] = authAgentEntry{TokenHash: fleetauth.HashToken(tok), EnrolledAt: time.Now().Unix()}
	if err := a.save(); err != nil {
		delete(a.data.Agents, name)
		return "", err
	}
	return tok, nil
}

func (a *AuthStore) authAgent(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	// Hash once, then compare against every agent with a constant-time compare
	// and NO early return. Short-circuiting on the first match leaked, via the
	// number of comparisons, information about set membership/position (the map
	// is iterated in randomized order). Iterating the full set with a constant
	// amount of work per agent removes that timing oracle.
	want := []byte(fleetauth.HashToken(token))
	a.mu.Lock()
	defer a.mu.Unlock()
	matched := ""
	found := false
	for name, e := range a.data.Agents {
		if subtle.ConstantTimeCompare([]byte(e.TokenHash), want) == 1 {
			matched, found = name, true
		}
	}
	return matched, found
}

func (a *AuthStore) removeAgent(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.data.Agents[name]; !ok {
		return false
	}
	entry := a.data.Agents[name]
	delete(a.data.Agents, name)
	if err := a.save(); err != nil {
		a.data.Agents[name] = entry // restore: revocation must persist or fail visibly
		log.Printf("auth: failed to persist removal of agent %q: %v", name, err)
		return false
	}
	return true
}

type listedAgent struct {
	Name       string
	EnrolledAt int64
}

func (a *AuthStore) listAgents() []listedAgent {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]listedAgent, 0, len(a.data.Agents))
	for name, e := range a.data.Agents {
		out = append(out, listedAgent{Name: name, EnrolledAt: e.EnrolledAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (a *AuthStore) rotate(which string) (string, error) {
	tok, err := fleetauth.GenerateToken()
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch which {
	case "enroll":
		old := a.data.EnrollTokenHash
		a.data.EnrollTokenHash = fleetauth.HashToken(tok)
		if err := a.save(); err != nil {
			a.data.EnrollTokenHash = old
			return "", err
		}
		return tok, nil
	case "admin":
		old := a.data.AdminTokenHash
		a.data.AdminTokenHash = fleetauth.HashToken(tok)
		if err := a.save(); err != nil {
			a.data.AdminTokenHash = old
			return "", err
		}
		return tok, nil
	default:
		return "", fmt.Errorf("unknown token %q (want enroll|admin)", which)
	}
}

// EnrollAgent is the exported variant of enrollAgent, allowing external
// packages (e.g. tests and CLI helpers) to enroll a new agent by name.
func (a *AuthStore) EnrollAgent(name string) (string, error) {
	return a.enrollAgent(name)
}

// ensureDataDir creates dataDir (mode 0700) if it does not exist.
func ensureDataDir(dataDir string) error {
	return os.MkdirAll(dataDir, 0o700)
}

// FingerprintForDir returns the TLS certificate fingerprint for the server
// at dataDir, generating the cert pair if absent.
func FingerprintForDir(dataDir string) (string, error) {
	if err := ensureDataDir(dataDir); err != nil {
		return "", fmt.Errorf("create data dir: %w", err)
	}
	_, fp, err := LoadOrCreateCert(dataDir, "", "")
	return fp, err
}

// AgentInfo describes a single enrolled agent.
type AgentInfo struct {
	Name       string
	EnrolledAt int64
}

// RotateToken rotates the enroll or admin token for the server at dataDir and
// returns the new plaintext token. which must be "enroll" or "admin".
func RotateToken(dataDir, which string) (string, error) {
	if err := ensureDataDir(dataDir); err != nil {
		return "", fmt.Errorf("create data dir: %w", err)
	}
	a, _, err := loadOrInitAuth(dataDir)
	if err != nil {
		return "", err
	}
	return a.rotate(which)
}

// ListAgents returns the enrolled agents for the server at dataDir.
func ListAgents(dataDir string) ([]AgentInfo, error) {
	if err := ensureDataDir(dataDir); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	a, _, err := loadOrInitAuth(dataDir)
	if err != nil {
		return nil, err
	}
	out := make([]AgentInfo, 0)
	for _, la := range a.listAgents() {
		out = append(out, AgentInfo{Name: la.Name, EnrolledAt: la.EnrolledAt})
	}
	return out, nil
}

// RemoveAgent revokes the named agent for the server at dataDir.
// Returns false (no error) when the agent does not exist.
func RemoveAgent(dataDir, name string) (bool, error) {
	if err := ensureDataDir(dataDir); err != nil {
		return false, fmt.Errorf("create data dir: %w", err)
	}
	a, _, err := loadOrInitAuth(dataDir)
	if err != nil {
		return false, err
	}
	return a.removeAgent(name), nil
}

// InitAuthPrint calls loadOrInitAuth for dir and, if fresh secrets were
// generated, writes them to out. This lets the server command print the
// secrets to its stdout before calling ServeDir (which also calls
// loadOrInitAuth — idempotent because auth.json exists by then).
func InitAuthPrint(dir string, out io.Writer) error {
	_, secrets, err := loadOrInitAuth(dir)
	if err != nil {
		return err
	}
	if secrets != nil {
		fmt.Fprintf(out, "marshal server: enroll token %s\n", secrets.EnrollToken)
		fmt.Fprintf(out, "marshal server: admin token  %s\n", secrets.AdminToken)
	}
	return nil
}

const dashboardPBKDF2Iter = 600000

// SetDashboardUser creates or replaces the dashboard credential for user,
// storing a PBKDF2-HMAC-SHA256 hash with a fresh random salt. It persists
// atomically and rolls back the in-memory map on save failure.
func (a *AuthStore) SetDashboardUser(user, password string) error {
	if user == "" {
		return errors.New("dashboard user name required")
	}
	if password == "" {
		return errors.New("dashboard password required")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	dk, err := pbkdf2.Key(sha256.New, password, salt, dashboardPBKDF2Iter, 32)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.data.Users == nil {
		a.data.Users = map[string]dashboardUser{}
	}
	old, existed := a.data.Users[user]
	a.data.Users[user] = dashboardUser{
		PBKDF2: base64.StdEncoding.EncodeToString(dk),
		Salt:   base64.StdEncoding.EncodeToString(salt),
		Iter:   dashboardPBKDF2Iter,
	}
	if err := a.save(); err != nil {
		if existed {
			a.data.Users[user] = old
		} else {
			delete(a.data.Users, user)
		}
		return err
	}
	return nil
}

// VerifyDashboardUser reports whether password matches the stored credential
// for user (constant-time). Unknown user or malformed record returns false.
func (a *AuthStore) VerifyDashboardUser(user, password string) bool {
	a.mu.Lock()
	u, ok := a.data.Users[user]
	a.mu.Unlock()
	if !ok {
		return false
	}
	salt, err := base64.StdEncoding.DecodeString(u.Salt)
	if err != nil {
		return false
	}
	want, err := base64.StdEncoding.DecodeString(u.PBKDF2)
	if err != nil {
		return false
	}
	dk, err := pbkdf2.Key(sha256.New, password, salt, u.Iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(dk, want) == 1
}

// DashboardCredentialStamp returns an opaque fingerprint of user's current
// dashboard credential, or ok=false if user has no credential. The fingerprint
// changes whenever the password is (re)set, because SetDashboardUser draws a
// fresh random salt each time. It reveals nothing useful: a hash over an
// already-hashed secret plus its salt and iteration count.
func (a *AuthStore) DashboardCredentialStamp(user string) (string, bool) {
	a.mu.Lock()
	u, ok := a.data.Users[user]
	a.mu.Unlock()
	if !ok {
		return "", false
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s.%s.%d", u.PBKDF2, u.Salt, u.Iter)))
	return hex.EncodeToString(sum[:]), true
}

// HasDashboardUser reports whether any dashboard user is configured.
func (a *AuthStore) HasDashboardUser() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.data.Users) > 0
}

// SetDashboardPassword sets (or replaces) the dashboard credential for the
// server rooted at dataDir.
func SetDashboardPassword(dataDir, user, password string) error {
	if err := ensureDataDir(dataDir); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	a, _, err := loadOrInitAuth(dataDir)
	if err != nil {
		return err
	}
	return a.SetDashboardUser(user, password)
}

// HasDashboardUserDir reports whether the server at dataDir has a dashboard
// user configured.
func HasDashboardUserDir(dataDir string) (bool, error) {
	if err := ensureDataDir(dataDir); err != nil {
		return false, fmt.Errorf("create data dir: %w", err)
	}
	a, _, err := loadOrInitAuth(dataDir)
	if err != nil {
		return false, err
	}
	return a.HasDashboardUser(), nil
}
