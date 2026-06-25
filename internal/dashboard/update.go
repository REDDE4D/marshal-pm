package dashboard

import (
	"net/http"
	"time"

	"github.com/REDDE4D/marshal-pm/internal/updatecheck"
)

// UpdateStatus surfaces the latest-release check to the dashboard banner. The
// server's *updatecheck.Checker satisfies it; nil disables the feature.
type UpdateStatus interface {
	Enabled() bool
	Snapshot() updatecheck.Result
}

type updateView struct {
	Enabled        bool      `json:"enabled"`
	Current        string    `json:"current"`
	Latest         string    `json:"latest"`
	Outdated       bool      `json:"outdated"`
	CheckedAt      time.Time `json:"checked_at"`
	OutdatedAgents []string  `json:"outdated_agents"`
}

// updateStatus reports whether a newer Marshal release exists and which
// connected agents are running an older version than the latest release.
func (h *handler) updateStatus(w http.ResponseWriter, r *http.Request) {
	if h.updater == nil || !h.updater.Enabled() {
		writeJSON(w, http.StatusOK, updateView{Enabled: false})
		return
	}
	res := h.updater.Snapshot()
	v := updateView{
		Enabled:        true,
		Current:        res.Current,
		Latest:         res.Latest,
		Outdated:       res.Outdated,
		CheckedAt:      res.CheckedAt,
		OutdatedAgents: []string{},
	}
	if res.Latest != "" && h.lister != nil {
		for _, a := range h.lister.List() {
			if updatecheck.Outdated(a.GetMarshalVersion(), res.Latest) {
				v.OutdatedAgents = append(v.OutdatedAgents, a.GetAgentName())
			}
		}
	}
	writeJSON(w, http.StatusOK, v)
}
