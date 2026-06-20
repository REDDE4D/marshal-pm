package dashboard

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"path"
	"strings"

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

// readFileFiles serves GET /api/fleet/{agent}/apps/{app}/file?path=<rel>[&raw=1].
// When raw=1, responds with raw bytes as an attachment (application/octet-stream).
// Otherwise returns a JSON fileContentDTO; binary content is omitted from the DTO.
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

	if r.URL.Query().Get("raw") == "1" {
		// Raw download: serve bytes as an attachment.
		base := path.Base(f.GetPath())
		// Sanitize: reject empty, ".", "/" results from path.Base; strip unsafe chars.
		if base == "" || base == "." || base == "/" {
			base = "download"
		}
		// Strip double-quotes, all ASCII control characters, and DEL to prevent header injection.
		base = strings.Map(func(c rune) rune {
			if c == '"' || c < 0x20 || c == 0x7F {
				return -1
			}
			return c
		}, base)
		if base == "" {
			base = "download"
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="`+base+`"`)
		w.WriteHeader(http.StatusOK)
		w.Write(f.GetContent()) //nolint:errcheck
		return
	}

	// JSON view: omit binary bytes from the DTO (the client can't display them).
	content := string(f.GetContent())
	if f.GetBinary() {
		content = ""
	}
	writeJSON(w, http.StatusOK, fileContentDTO{
		Path: f.GetPath(), Content: content, Size: f.GetSize(),
		Truncated: f.GetTruncated(), Binary: f.GetBinary(),
	})
}

const maxCommitBytes = 1 << 20 // 1 MiB content cap, matches the read cap

type writeBody struct {
	Content    string `json:"content"`
	Message    string `json:"message"`
	Credential string `json:"credential"`
}
type deleteBody struct {
	Message    string `json:"message"`
	Credential string `json:"credential"`
}
type renameBody struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Message    string `json:"message"`
	Credential string `json:"credential"`
}

// writeFileFiles serves PUT /api/fleet/{agent}/apps/{app}/file?path=<rel>[&create=1].
// With create=1 the op uses COMMIT_CREATE (path need not exist yet); otherwise
// COMMIT_EDIT (path must already exist). Commits and pushes via the agent.
func (h *handler) writeFileFiles(w http.ResponseWriter, r *http.Request) {
	agent, app := r.PathValue("agent"), r.PathValue("app")
	if agent == "" || app == "" {
		http.Error(w, "agent and app required", http.StatusBadRequest)
		return
	}
	p := r.URL.Query().Get("path")
	if p == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path required"})
		return
	}
	var body writeBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(body.Content) > maxCommitBytes {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file too large (max 1 MiB)"})
		return
	}
	cred, cerr := h.resolveCredential(body.Credential)
	if cerr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": cerr.Error()})
		return
	}
	create := r.URL.Query().Get("create") == "1"
	kind := pb.CommitKind_COMMIT_EDIT
	defaultMsg := "Update " + p
	if create {
		kind = pb.CommitKind_COMMIT_CREATE
		defaultMsg = "Create " + p
	}
	msg := body.Message
	if msg == "" {
		msg = defaultMsg
	}
	op := &pb.ControlOp{Op: &pb.ControlOp_Commit{Commit: &pb.CommitRequest{
		App: app, Kind: kind, Path: p,
		Content: []byte(body.Content), Message: msg, Credential: cred,
	}}}
	h.commitControl(w, r, agent, app, "edit", op)
}

// deleteFileFiles serves DELETE /api/fleet/{agent}/apps/{app}/file?path=<rel>.
func (h *handler) deleteFileFiles(w http.ResponseWriter, r *http.Request) {
	agent, app := r.PathValue("agent"), r.PathValue("app")
	if agent == "" || app == "" {
		http.Error(w, "agent and app required", http.StatusBadRequest)
		return
	}
	p := r.URL.Query().Get("path")
	if p == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path required"})
		return
	}
	var body deleteBody
	_ = json.NewDecoder(r.Body).Decode(&body) // empty body is fine
	cred, cerr := h.resolveCredential(body.Credential)
	if cerr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": cerr.Error()})
		return
	}
	msg := body.Message
	if msg == "" {
		msg = "Delete " + p
	}
	op := &pb.ControlOp{Op: &pb.ControlOp_Commit{Commit: &pb.CommitRequest{
		App: app, Kind: pb.CommitKind_COMMIT_DELETE, Path: p,
		Message: msg, Credential: cred,
	}}}
	h.commitControl(w, r, agent, app, "delete", op)
}

// renameFiles serves POST /api/fleet/{agent}/apps/{app}/rename.
func (h *handler) renameFiles(w http.ResponseWriter, r *http.Request) {
	agent, app := r.PathValue("agent"), r.PathValue("app")
	if agent == "" || app == "" {
		http.Error(w, "agent and app required", http.StatusBadRequest)
		return
	}
	var body renameBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.From == "" || body.To == "" {
		http.Error(w, "from and to required", http.StatusBadRequest)
		return
	}
	cred, cerr := h.resolveCredential(body.Credential)
	if cerr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": cerr.Error()})
		return
	}
	msg := body.Message
	if msg == "" {
		msg = "Rename " + body.From + " → " + body.To
	}
	op := &pb.ControlOp{Op: &pb.ControlOp_Commit{Commit: &pb.CommitRequest{
		App: app, Kind: pb.CommitKind_COMMIT_RENAME, Path: body.From, NewPath: body.To,
		Message: msg, Credential: cred,
	}}}
	h.commitControl(w, r, agent, app, "rename", op)
}

// commitControl dispatches a commit op, maps errors (503/400), audit-logs the
// outcome (never the token), and returns {sha,branch} on success.
func (h *handler) commitControl(w http.ResponseWriter, r *http.Request, agent, app, kind string, op *pb.ControlOp) {
	res, ok := h.fileControl(w, r, agent, op)
	if !ok {
		return
	}
	cr := res.GetCommit()
	user, _ := r.Context().Value(userKey).(string)
	log.Printf("dashboard: commit %s %s/%s -> %s by %s: sha=%s branch=%s",
		kind, agent, app, op.GetCommit().GetPath(), user, cr.GetSha(), cr.GetBranch())
	writeJSON(w, http.StatusOK, map[string]string{"sha": cr.GetSha(), "branch": cr.GetBranch()})
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
