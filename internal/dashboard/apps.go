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

type addAppRequest struct {
	Agent  string        `json:"agent"`
	Source commandSource `json:"source"`
}

// apps serves POST /api/apps: creates and launches a new app on one agent via
// ControlOp_Start. 400 on bad input / unsupported source / validation error;
// 502 when the op never reached the agent; 200 {"ok":bool,"error"?} when the
// agent executed (or rejected) it. The authoritative validation (restart mode,
// kill_timeout parse, instances >= 0, duplicate name) happens in the agent's
// start chain and is surfaced verbatim.
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
	if body.Source.Type != "command" {
		http.Error(w, "unsupported source type", http.StatusBadRequest)
		return
	}
	if body.Source.Name == "" || body.Source.Cmd == "" {
		http.Error(w, "name and cmd required", http.StatusBadRequest)
		return
	}
	op := startOp(body.Source)
	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()
	res, err := h.controller.Control(ctx, body.Agent, op)
	user, _ := r.Context().Value(userKey).(string)
	if err != nil {
		log.Printf("dashboard: add app %s -> %s by %s: %v", body.Source.Name, body.Agent, user, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	log.Printf("dashboard: add app %s -> %s by %s: ok=%v", body.Source.Name, body.Agent, user, res.GetOk())
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
