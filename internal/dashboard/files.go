package dashboard

import (
	"context"
	"net/http"

	"marshal/internal/pb"
)

// JSON DTOs — the dashboard never serializes raw pb messages (M21 lesson).
type dirEntryDTO struct {
	Name    string `json:"name"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	ModUnix int64  `json:"mod_unix"`
	Mode    uint32 `json:"mode"`
}
type dirListingDTO struct {
	Path    string        `json:"path"`
	Entries []dirEntryDTO `json:"entries"`
}
type fileContentDTO struct {
	Path      string `json:"path"`
	Content   string `json:"content"` // text; empty when binary
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
	Binary    bool   `json:"binary"`
}

// listDirFiles serves GET /api/fleet/{agent}/apps/{app}/dir?path=<rel>.
func (h *handler) listDirFiles(w http.ResponseWriter, r *http.Request) {
	agent := r.PathValue("agent")
	app := r.PathValue("app")
	if agent == "" || app == "" {
		http.Error(w, "agent and app required", http.StatusBadRequest)
		return
	}
	op := &pb.ControlOp{Op: &pb.ControlOp_ListDir{ListDir: &pb.ListDirRequest{
		App: app, Path: r.URL.Query().Get("path"),
	}}}
	res, ok := h.fileControl(w, r, agent, op)
	if !ok {
		return
	}
	d := res.GetDir()
	out := dirListingDTO{Path: d.GetPath()}
	for _, e := range d.GetEntries() {
		out.Entries = append(out.Entries, dirEntryDTO{
			Name: e.GetName(), IsDir: e.GetIsDir(), Size: e.GetSize(),
			ModUnix: e.GetModUnix(), Mode: e.GetMode(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// readFileFiles serves GET /api/fleet/{agent}/apps/{app}/file?path=<rel>.
func (h *handler) readFileFiles(w http.ResponseWriter, r *http.Request) {
	agent := r.PathValue("agent")
	app := r.PathValue("app")
	if agent == "" || app == "" {
		http.Error(w, "agent and app required", http.StatusBadRequest)
		return
	}
	op := &pb.ControlOp{Op: &pb.ControlOp_ReadFile{ReadFile: &pb.ReadFileRequest{
		App: app, Path: r.URL.Query().Get("path"),
	}}}
	res, ok := h.fileControl(w, r, agent, op)
	if !ok {
		return
	}
	f := res.GetFile()
	writeJSON(w, http.StatusOK, fileContentDTO{
		Path: f.GetPath(), Content: string(f.GetContent()), Size: f.GetSize(),
		Truncated: f.GetTruncated(), Binary: f.GetBinary(),
	})
}

// fileControl dispatches op to the agent and handles the shared error mapping.
// Returns (result, true) only when the agent executed the op successfully;
// otherwise it has already written the response and returns (_, false).
func (h *handler) fileControl(w http.ResponseWriter, r *http.Request, agent string, op *pb.ControlOp) (*pb.ControlResult, bool) {
	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()
	res, err := h.controller.Control(ctx, agent, op)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return nil, false
	}
	if !res.GetOk() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": res.GetError()})
		return nil, false
	}
	return res, true
}
