package dashboard

import (
	"net/http"
	"strconv"

	"github.com/REDDE4D/marshal-pm/internal/metricstore"
)

// MetricsHistory is the read side of stored CPU/mem history. *server.stores satisfies it.
type MetricsHistory interface {
	History(agent, selector string, sinceMs, bucketMs int64) ([]metricstore.Bucket, error)
}

type bucketView struct {
	Ts     int64   `json:"ts"`
	CpuAvg float64 `json:"cpu_avg"`
	CpuMax float64 `json:"cpu_max"`
	MemAvg uint64  `json:"mem_avg"`
	MemMax uint64  `json:"mem_max"`
}

type procMetricsView struct {
	Name    string       `json:"name"`
	Buckets []bucketView `json:"buckets"`
}

type agentMetricsView struct {
	Agent string            `json:"agent"`
	Procs []procMetricsView `json:"procs"`
}

const (
	defaultSparkSinceMs  = int64(5 * 60 * 1000)  // 5m, batched sparklines
	defaultDetailSinceMs = int64(60 * 60 * 1000) // 1h, single-series detail
)

func bucketViews(bs []metricstore.Bucket) []bucketView {
	out := make([]bucketView, 0, len(bs))
	for _, b := range bs {
		out = append(out, bucketView{Ts: b.TsMs, CpuAvg: b.CpuAvg, CpuMax: b.CpuMax, MemAvg: b.MemAvg, MemMax: b.MemMax})
	}
	return out
}

// metrics serves GET /api/metrics. With agent+selector query params it returns a
// single proc's series (detail panel); otherwise it returns recent history for
// every agent/proc in the live fleet (sparklines).
func (h *handler) metrics(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	agent := q.Get("agent")
	selector := q.Get("selector")

	if agent != "" && selector != "" {
		sinceMs := parseMs(q.Get("since"), defaultDetailSinceMs)
		bucketMs := parseMs(q.Get("bucket"), 0)
		bs, err := h.metricsHist.History(agent, selector, sinceMs, bucketMs)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, []agentMetricsView{{
			Agent: agent,
			Procs: []procMetricsView{{Name: selector, Buckets: bucketViews(bs)}},
		}})
		return
	}

	sinceMs := parseMs(q.Get("since"), defaultSparkSinceMs)
	agents := h.lister.List()
	out := make([]agentMetricsView, 0, len(agents))
	for _, a := range agents {
		procs := make([]procMetricsView, 0, len(a.GetProcs()))
		for _, p := range a.GetProcs() {
			bs, err := h.metricsHist.History(a.GetAgentName(), p.GetName(), sinceMs, 0)
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			procs = append(procs, procMetricsView{Name: p.GetName(), Buckets: bucketViews(bs)})
		}
		out = append(out, agentMetricsView{Agent: a.GetAgentName(), Procs: procs})
	}
	writeJSON(w, http.StatusOK, out)
}

// parseMs parses a positive int64 ms value, returning def for empty or invalid input.
func parseMs(s string, def int64) int64 {
	if s == "" {
		return def
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v < 0 {
		return def
	}
	return v
}
