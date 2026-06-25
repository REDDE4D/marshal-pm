package dashboard

import (
	"encoding/json"
	"net/http"

	"github.com/REDDE4D/marshal-pm/internal/errsig"
)

// Acks records which error signatures have been acknowledged. *ackstore.Store
// satisfies it; nil disables acknowledgement (everything stays unacknowledged).
type Acks interface {
	AckedAt(id string) (int64, bool)
	Ack(id string, atMs int64) error
	Unack(id string) error
}

// acknowledged reports whether a signature counts as acknowledged: it must have
// an ack, and must not have recurred since (lastUnix seconds ≤ the ack ms).
func acknowledged(acked bool, ackedAtMs, lastUnix int64) bool {
	return acked && lastUnix*1000 <= ackedAtMs
}

func (h *handler) ackedAt(id string) (int64, bool) {
	if h.acks == nil {
		return 0, false
	}
	return h.acks.AckedAt(id)
}

const (
	errSparkBuckets = 24
	errMaxScan      = 200_000 // worst-case lines per request before truncation
	dayMs           = 24 * 60 * 60 * 1000
	retentionMs     = 7 * dayMs
)

type errSigView struct {
	ID           string   `json:"id"`
	Sample       string   `json:"sample"`
	Source       string   `json:"source,omitempty"`
	Agent        string   `json:"agent"`
	Proc         string   `json:"proc"`
	Affected     []string `json:"affected"`
	Count        int      `json:"count"`
	FirstUnix    int64    `json:"first_unix"`
	LastUnix     int64    `json:"last_unix"`
	Buckets      []int    `json:"buckets"`
	Acknowledged bool     `json:"acknowledged"`
}

type errClusterView struct {
	Errors         int   `json:"errors"`
	Signatures     int   `json:"signatures"`
	Unacknowledged int   `json:"unacknowledged"`
	AffectedProcs  int   `json:"affected_procs"`
	LastErrorUnix  int64 `json:"last_error_unix"`
}

type errorsView struct {
	Range      string         `json:"range"`
	Since      int64          `json:"since"`
	Now        int64          `json:"now"`
	Cluster    errClusterView `json:"cluster"`
	Signatures []errSigView   `json:"signatures"`
	Truncated  bool           `json:"truncated"`
}

// rangeMs maps the range token to a window length; default and "all" both clamp
// to the 7-day retention. Returns the canonical token too.
func rangeMs(tok string) (string, int64) {
	switch tok {
	case "7d":
		return "7d", 7 * dayMs
	case "all":
		return "all", retentionMs
	default:
		return "24h", dayMs
	}
}

// errors serves GET /api/errors?range=&agent=. Fleet-wide unless agent= is set.
func (h *handler) errors(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rng, window := rangeMs(q.Get("range"))
	now := nowMs()
	since := now - window

	var agents []string
	if a := q.Get("agent"); a != "" {
		agents = []string{a}
	} else {
		for _, ag := range h.lister.List() {
			agents = append(agents, ag.GetAgentName())
		}
	}

	var lines []errsig.Line
	truncated := false
	for _, ag := range agents {
		rows, err := h.logsHist.StderrSince(ag, since)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		for _, ln := range rows {
			lines = append(lines, errsig.Line{TsMs: ln.TsMs, Label: ln.Label, Text: ln.Text, Agent: ag})
			if len(lines) >= errMaxScan {
				truncated = true
				break
			}
		}
		if truncated {
			break
		}
	}

	res := errsig.Aggregate(lines, since, now, errSparkBuckets)
	out := errorsView{
		Range: rng, Since: since, Now: now, Truncated: truncated,
		Cluster: errClusterView{
			Errors: res.Cluster.Errors, Signatures: res.Cluster.Signatures,
			AffectedProcs: res.Cluster.AffectedProcs, LastErrorUnix: res.Cluster.LastErrorUnix,
		},
		Signatures: make([]errSigView, 0, len(res.Signatures)),
	}
	unack := 0
	for _, s := range res.Signatures {
		at, ok := h.ackedAt(s.Id)
		ack := acknowledged(ok, at, s.LastUnix)
		if !ack {
			unack++
		}
		out.Signatures = append(out.Signatures, errSigView{
			ID: s.Id, Sample: s.Sample, Source: s.Source, Agent: s.Agent, Proc: s.Proc,
			Affected: s.Affected, Count: s.Count, FirstUnix: s.FirstUnix, LastUnix: s.LastUnix,
			Buckets: s.Buckets, Acknowledged: ack,
		})
	}
	out.Cluster.Unacknowledged = unack
	writeJSON(w, http.StatusOK, out)
}

// ackError serves POST /api/errors/ack {"id":..., "ack":true|false}: acknowledge
// (silence until it recurs) or un-acknowledge an error signature.
func (h *handler) ackError(w http.ResponseWriter, r *http.Request) {
	if h.acks == nil {
		http.Error(w, "acknowledgement unavailable", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		ID  string `json:"id"`
		Ack bool   `json:"ack"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var err error
	if body.Ack {
		err = h.acks.Ack(body.ID, nowMs())
	} else {
		err = h.acks.Unack(body.ID)
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
