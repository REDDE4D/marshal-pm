package server

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"marshal/internal/fleetauth"
)

type authAgentEntry struct {
	TokenHash  string `json:"token_hash"`
	EnrolledAt int64  `json:"enrolled_at"`
}

type authData struct {
	EnrollTokenHash string                    `json:"enroll_token_hash"`
	AdminTokenHash  string                    `json:"admin_token_hash"`
	Agents          map[string]authAgentEntry `json:"agents"`
}

type authStore struct {
	path string
	mu   sync.Mutex
	data authData
}

type initSecrets struct {
	EnrollToken string
	AdminToken  string
}

func loadOrInitAuth(dir string) (*authStore, *initSecrets, error) {
	path := filepath.Join(dir, "auth.json")
	a := &authStore{path: path, data: authData{Agents: map[string]authAgentEntry{}}}
	b, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(b, &a.data); err != nil {
			return nil, nil, fmt.Errorf("parse auth.json: %w", err)
		}
		if a.data.Agents == nil {
			a.data.Agents = map[string]authAgentEntry{}
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
	return a, &initSecrets{EnrollToken: enroll, AdminToken: admin}, nil
}

// save writes auth.json atomically (0600). Caller holds a.mu, or it is called
// during init before the store is shared.
func (a *authStore) save() error {
	b, err := json.MarshalIndent(a.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.path)
}

func (a *authStore) verifyAdmin(token string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return fleetauth.VerifyToken(token, a.data.AdminTokenHash)
}

func (a *authStore) verifyEnroll(token string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return fleetauth.VerifyToken(token, a.data.EnrollTokenHash)
}

func (a *authStore) enrollAgent(name string) (string, error) {
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

func (a *authStore) authAgent(token string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for name, e := range a.data.Agents {
		if fleetauth.VerifyToken(token, e.TokenHash) {
			return name, true
		}
	}
	return "", false
}

func (a *authStore) removeAgent(name string) bool {
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

func (a *authStore) listAgents() []listedAgent {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]listedAgent, 0, len(a.data.Agents))
	for name, e := range a.data.Agents {
		out = append(out, listedAgent{Name: name, EnrolledAt: e.EnrolledAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (a *authStore) rotate(which string) (string, error) {
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
