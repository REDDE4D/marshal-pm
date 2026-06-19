package dashboard

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

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
	Type      string            `json:"type"`
	Name      string            `json:"name"`
	Cmd       string            `json:"cmd"`
	Args      []string          `json:"args"`
	Instances int32             `json:"instances"`
	Env       map[string]string `json:"env"`
	Restart   string            `json:"restart"`
	Repo      string            `json:"repo"`
	Ref       string            `json:"ref"`
	Build     string            `json:"build"`
	Subdir    string            `json:"subdir"`
}

type addAppRequest struct {
	Agent  string          `json:"agent"`
	Source json.RawMessage `json:"source"`
}

type redeployRequest struct {
	Agent string `json:"agent"`
	Name  string `json:"name"`
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
		op, name = deployOp(g), g.Name
	default:
		http.Error(w, "unsupported source type", http.StatusBadRequest)
		return
	}
	h.dispatchApp(w, r, body.Agent, name, op)
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
	op := &pb.ControlOp{Op: &pb.ControlOp_Redeploy{Redeploy: &pb.RedeployRequest{Target: body.Name}}}
	h.dispatchApp(w, r, body.Agent, body.Name, op)
}

// dispatchApp forwards op to the named agent via h.controller.Control and
// writes the standard 200/502 response. Both apps and redeploy share this path.
func (h *handler) dispatchApp(w http.ResponseWriter, r *http.Request, agent, name string, op *pb.ControlOp) {
	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()
	res, err := h.controller.Control(ctx, agent, op)
	user, _ := r.Context().Value(userKey).(string)
	if err != nil {
		log.Printf("dashboard: add app %s -> %s by %s: %v", name, agent, user, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	log.Printf("dashboard: add app %s -> %s by %s: ok=%v", name, agent, user, res.GetOk())
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
func deployOp(g gitSource) *pb.ControlOp {
	spec := &pb.AppSpec{
		Name:      g.Name,
		Cmd:       g.Cmd,
		Args:      g.Args,
		Instances: g.Instances,
		Env:       g.Env,
		Restart:   g.Restart,
		Source:    &pb.GitSource{Repo: g.Repo, Ref: g.Ref, Build: g.Build, Subdir: g.Subdir},
	}
	return &pb.ControlOp{Op: &pb.ControlOp_Deploy{Deploy: &pb.DeployRequest{App: spec}}}
}
