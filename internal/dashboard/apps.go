package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"

	"marshal/internal/pb"
)

// commandSource is the "command" variant of an app-creation source. It maps
// 1:1 to pb.AppSpec. Only name and cmd are required; the rest fall through to
// backend defaults (instances→1, restart→always, max_restarts→16, kill_timeout→5s)
// applied by config.Prepare on the agent.
type commandSource struct {
	Type        string            `json:"type"`
	Name        string            `json:"name"`
	Cmd         string            `json:"cmd"`
	Args        []string          `json:"args"`
	Cwd         string            `json:"cwd"`
	Instances   int32             `json:"instances"`
	Env         map[string]string `json:"env"`
	Restart     string            `json:"restart"`
	MaxRestarts int32             `json:"max_restarts"`
	KillTimeout string            `json:"kill_timeout"`
}

// gitSource is the "git" variant of an app-creation source. Name and repo are
// required; the rest fall through to agent defaults.
type gitSource struct {
	Type       string            `json:"type"`
	Name       string            `json:"name"`
	Cmd        string            `json:"cmd"`
	Args       []string          `json:"args"`
	Instances  int32             `json:"instances"`
	Env        map[string]string `json:"env"`
	Restart    string            `json:"restart"`
	Repo       string            `json:"repo"`
	Ref        string            `json:"ref"`
	Build      string            `json:"build"`
	Subdir     string            `json:"subdir"`
	Credential string            `json:"credential"` // M22: credstore name to resolve on deploy
}

type addAppRequest struct {
	Agent  string          `json:"agent"`
	Source json.RawMessage `json:"source"`
}

type redeployRequest struct {
	Agent      string `json:"agent"`
	Name       string `json:"name"`
	Credential string `json:"credential"` // M22: credstore name to resolve on redeploy
}

// apps serves POST /api/apps: creates and launches a new app on one agent via
// ControlOp_Start (command) or ControlOp_Deploy (git). 400 on bad input /
// unsupported source / validation error; 502 when the op never reached the
// agent; 200 {"ok":bool,"error"?} when the agent executed (or rejected) it.
// The authoritative validation (restart mode, kill_timeout parse, instances >= 0,
// duplicate name) happens in the agent's start/deploy chain and is surfaced verbatim.
func (h *handler) apps(w http.ResponseWriter, r *http.Request) {
	var body addAppRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Agent == "" {
		http.Error(w, "agent required", http.StatusBadRequest)
		return
	}
	var probe struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(body.Source, &probe)
	var op *pb.ControlOp
	var name string
	switch probe.Type {
	case "command":
		var s commandSource
		if err := json.Unmarshal(body.Source, &s); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if s.Name == "" || s.Cmd == "" {
			http.Error(w, "name and cmd required", http.StatusBadRequest)
			return
		}
		op, name = startOp(s), s.Name
	case "git":
		var g gitSource
		if err := json.Unmarshal(body.Source, &g); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if g.Name == "" || g.Repo == "" {
			http.Error(w, "name and repo required", http.StatusBadRequest)
			return
		}
		cred, cerr := h.resolveCredential(g.Credential, g.Repo)
		if cerr != nil {
			http.Error(w, cerr.Error(), http.StatusBadRequest)
			return
		}
		op, name = deployOp(g, cred), g.Name
	default:
		http.Error(w, "unsupported source type", http.StatusBadRequest)
		return
	}
	h.dispatchApp(w, r, body.Agent, name, op, "add app")
}

// redeploy serves POST /api/apps/redeploy: triggers a re-deploy of a
// previously deployed git app on one agent via ControlOp_Redeploy.
func (h *handler) redeploy(w http.ResponseWriter, r *http.Request) {
	var body redeployRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Agent == "" || body.Name == "" {
		http.Error(w, "agent and name required", http.StatusBadRequest)
		return
	}
	cred, cerr := h.resolveCredential(body.Credential, "")
	if cerr != nil {
		http.Error(w, cerr.Error(), http.StatusBadRequest)
		return
	}
	op := &pb.ControlOp{Op: &pb.ControlOp_Redeploy{Redeploy: &pb.RedeployRequest{Target: body.Name, Credential: cred}}}
	h.dispatchApp(w, r, body.Agent, body.Name, op, "redeploy")
}

// dispatchApp forwards op to the named agent via h.controller.Control and
// writes the standard 200/502 response. Both apps and redeploy share this path.
// action is a human-readable label used in log lines (e.g. "add app" or "redeploy").
func (h *handler) dispatchApp(w http.ResponseWriter, r *http.Request, agent, name string, op *pb.ControlOp, action string) {
	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()
	res, err := h.controller.Control(ctx, agent, op)
	user, _ := r.Context().Value(userKey).(string)
	if err != nil {
		log.Printf("dashboard: %s %s -> %s by %s: %v", action, name, agent, user, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	log.Printf("dashboard: %s %s -> %s by %s: ok=%v", action, name, agent, user, res.GetOk())
	if !res.GetOk() {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": res.GetError()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// startOp builds a ControlOp_Start carrying one AppSpec from a command source.
func startOp(s commandSource) *pb.ControlOp {
	spec := &pb.AppSpec{
		Name:        s.Name,
		Cmd:         s.Cmd,
		Args:        s.Args,
		Cwd:         s.Cwd,
		Instances:   s.Instances,
		Env:         s.Env,
		Restart:     s.Restart,
		MaxRestarts: s.MaxRestarts,
		KillTimeout: s.KillTimeout,
	}
	return &pb.ControlOp{Op: &pb.ControlOp_Start{Start: &pb.StartRequest{Apps: []*pb.AppSpec{spec}}}}
}

// deployOp builds a ControlOp_Deploy carrying one AppSpec with a GitSource.
// cred is the resolved secret to attach to the op (may be nil for unauthenticated repos).
func deployOp(g gitSource, cred *pb.GitCredential) *pb.ControlOp {
	spec := &pb.AppSpec{
		Name:      g.Name,
		Cmd:       g.Cmd,
		Args:      g.Args,
		Instances: g.Instances,
		Env:       g.Env,
		Restart:   g.Restart,
		Source:    &pb.GitSource{Repo: g.Repo, Ref: g.Ref, Build: g.Build, Subdir: g.Subdir, Credential: g.Credential},
	}
	return &pb.ControlOp{Op: &pb.ControlOp_Deploy{Deploy: &pb.DeployRequest{App: spec, Credential: cred}}}
}

// resolveCredential turns a credential name into the secret to attach. Empty
// name → (nil, nil). repoURL supplies the host for the one-time SSH host-key
// scan; pass "" when the URL is not available (redeploy/commit — the pin is
// already set from the first deploy).
func (h *handler) resolveCredential(name, repoURL string) (*pb.GitCredential, error) {
	if name == "" {
		return nil, nil
	}
	if h.creds == nil {
		return nil, fmt.Errorf("credentials unavailable")
	}
	kind := ""
	for _, m := range h.creds.List() {
		if m.Name == name {
			kind = m.Type
			break
		}
	}
	if kind == "ssh-key" {
		priv, kh, ok, err := h.creds.GetKey(name)
		if err != nil {
			return nil, fmt.Errorf("credential %q: %v", name, err)
		}
		if !ok {
			return nil, fmt.Errorf("unknown credential %q", name)
		}
		if kh == "" && repoURL != "" {
			host, port := sshHostPort(repoURL)
			if host != "" {
				hostport := host
				if port != "" {
					hostport = host + ":" + port
				}
				scanned, serr := h.scanHost(hostport)
				if serr != nil {
					return nil, fmt.Errorf("host-key scan failed: %v", serr)
				}
				if err := h.creds.SetKnownHosts(name, scanned); err != nil {
					return nil, err
				}
				kh = scanned
			}
		}
		return &pb.GitCredential{Username: "git", PrivateKey: priv, KnownHosts: kh, Kind: pb.CredentialKind_CRED_SSH}, nil
	}

	user, tok, ok, err := h.creds.Get(name)
	if err != nil {
		return nil, fmt.Errorf("credential %q: %v", name, err)
	}
	if !ok {
		return nil, fmt.Errorf("unknown credential %q", name)
	}
	return &pb.GitCredential{Username: user, Token: tok, Kind: pb.CredentialKind_CRED_HTTPS}, nil
}

// sshHostPort extracts host and optional port from an SSH git URL. Handles the
// scp-like form (git@host:path) and the ssh:// URL form. Returns ("","") if not
// recognizably SSH.
func sshHostPort(repo string) (host, port string) {
	if strings.HasPrefix(repo, "https://") || strings.HasPrefix(repo, "http://") {
		return "", ""
	}
	if strings.HasPrefix(repo, "ssh://") {
		if u, err := url.Parse(repo); err == nil {
			return u.Hostname(), u.Port()
		}
		return "", ""
	}
	// scp-like: [user@]host:path  — host is between an optional "@" and the first ":"
	s := repo
	if at := strings.Index(s, "@"); at >= 0 {
		s = s[at+1:]
	}
	if colon := strings.Index(s, ":"); colon >= 0 {
		return s[:colon], ""
	}
	return "", ""
}
