package dashboard

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/REDDE4D/marshal-pm/internal/pb"
)

// FleetController is the write side of the fleet. *server.Server satisfies it.
type FleetController interface {
	Control(ctx context.Context, agent string, op *pb.ControlOp) (*pb.ControlResult, error)
}

const controlTimeout = 10 * time.Second

type controlRequest struct {
	Agent    string `json:"agent"`
	Selector string `json:"selector"`
	Action   string `json:"action"`
}

// control serves POST /api/control: routes a Restart/Stop to one agent's app.
// 400 on bad input; 502 when the op never reached the agent; 200 with
// {"ok":bool,"error"?} when the agent executed (or rejected) it.
func (h *handler) control(w http.ResponseWriter, r *http.Request) {
	var body controlRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Agent == "" || body.Selector == "" {
		http.Error(w, "agent and selector required", http.StatusBadRequest)
		return
	}
	op := controlOp(body.Action, body.Selector)
	if op == nil {
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()
	res, err := h.controller.Control(ctx, body.Agent, op)
	user, _ := r.Context().Value(userKey).(string)
	if err != nil {
		log.Printf("dashboard: control %s %s/%s by %s: %v", body.Action, body.Agent, body.Selector, user, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	log.Printf("dashboard: control %s %s/%s by %s: ok=%v", body.Action, body.Agent, body.Selector, user, res.GetOk())
	if !res.GetOk() {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": res.GetError()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// controlOp builds the ControlOp for an action, or nil if the action is unknown.
func controlOp(action, selector string) *pb.ControlOp {
	switch action {
	case "restart":
		return &pb.ControlOp{Op: &pb.ControlOp_Restart{Restart: &pb.Selector{Target: selector}}}
	case "reload":
		return &pb.ControlOp{Op: &pb.ControlOp_Reload{Reload: &pb.Selector{Target: selector}}}
	case "stop":
		return &pb.ControlOp{Op: &pb.ControlOp_Stop{Stop: &pb.Selector{Target: selector}}}
	case "delete":
		return &pb.ControlOp{Op: &pb.ControlOp_Delete{Delete: &pb.Selector{Target: selector}}}
	default:
		return nil
	}
}
