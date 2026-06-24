package dashboard

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"marshal/internal/logstore"
)

// LogsHistory is the read side of stored log lines. *server.logStores satisfies it.
type LogsHistory interface {
	Since(agent, selector string, afterRowID int64, limit int, filter logstore.StreamFilter, text string) ([]logstore.StoredLine, int64, error)
	ErrorCounts(agent string, sinceMs int64) (map[string]int64, error)
	StderrSince(agent string, sinceMs int64) ([]logstore.StoredLine, error)
}

type logLineView struct {
	Ts       int64  `json:"ts"`
	Name     string `json:"name"`
	Instance int    `json:"instance"`
	Stderr   bool   `json:"stderr"`
	Text     string `json:"text"`
}

type logsView struct {
	Cursor int64         `json:"cursor"`
	Lines  []logLineView `json:"lines"`
}

const (
	defaultLogLimit = 500
	maxLogLimit     = 5000
)

// logs serves GET /api/logs for a single proc selector. With ?after=<cursor> it
// returns only lines newer than the cursor (follow); otherwise the newest `limit`
// lines (backfill). Unknown agent -> 200 {"cursor":0,"lines":[]}.
func (h *handler) logs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	agent := q.Get("agent")
	selector := q.Get("selector")
	if agent == "" || selector == "" {
		http.Error(w, "agent and selector required", http.StatusBadRequest)
		return
	}
	lines, cursor, err := h.logsHist.Since(agent, selector, parseAfter(q.Get("after")), parseLimit(q.Get("limit")), streamFilterFor(q.Get("stream")), q.Get("q"))
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := logsView{Cursor: cursor, Lines: make([]logLineView, 0, len(lines))}
	for _, ln := range lines {
		name, idx := splitLogLabel(ln.Label)
		out.Lines = append(out.Lines, logLineView{Ts: ln.TsMs, Name: name, Instance: idx, Stderr: ln.Stderr, Text: ln.Text})
	}
	writeJSON(w, http.StatusOK, out)
}

// logsDownload serves GET /api/logs/download: the full retained log history for
// one agent/selector as a plain-text attachment, honoring the same stream/q
// filters as /api/logs (no line limit).
func (h *handler) logsDownload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	agent := q.Get("agent")
	selector := q.Get("selector")
	if agent == "" || selector == "" {
		http.Error(w, "agent and selector required", http.StatusBadRequest)
		return
	}
	lines, _, err := h.logsHist.Since(agent, selector, 0, 0, streamFilterFor(q.Get("stream")), q.Get("q"))
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", downloadName(agent, selector)))
	for _, ln := range lines {
		stream := "out"
		if ln.Stderr {
			stream = "err"
		}
		ts := time.UnixMilli(ln.TsMs).UTC().Format(time.RFC3339)
		fmt.Fprintf(w, "%s %s %s | %s\n", ts, stream, ln.Label, ln.Text)
	}
}

// downloadName builds a filesystem-safe "<agent>-<selector>.log" attachment name.
func downloadName(agent, selector string) string {
	repl := func(s string) string {
		return strings.Map(func(r rune) rune {
			if r == '/' || r == '\\' || r == '"' {
				return '-'
			}
			return r
		}, s)
	}
	return repl(agent) + "-" + repl(selector) + ".log"
}

func streamFilterFor(s string) logstore.StreamFilter {
	switch s {
	case "stdout":
		return logstore.StreamStdout
	case "stderr":
		return logstore.StreamStderr
	default:
		return logstore.StreamAny
	}
}

// parseLimit clamps to [1, maxLogLimit]; empty/invalid -> defaultLogLimit.
func parseLimit(s string) int {
	if s == "" {
		return defaultLogLimit
	}
	v, err := strconv.Atoi(s)
	if err != nil || v <= 0 {
		return defaultLogLimit
	}
	if v > maxLogLimit {
		return maxLogLimit
	}
	return v
}

// parseAfter parses a non-negative cursor; empty/invalid -> 0 (backfill).
func parseAfter(s string) int64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// splitLogLabel parses "name#idx" into its parts (idx 0 when absent/unparseable).
func splitLogLabel(label string) (string, int) {
	i := strings.LastIndexByte(label, '#')
	if i < 0 {
		return label, 0
	}
	n, _ := strconv.Atoi(label[i+1:])
	return label[:i], n
}

const errorWindowMs = 5 * 60 * 1000

// logstats serves GET /api/logstats?agent=<a>: per-label recent stderr counts
// (last 5 min). Best-effort; an empty/unknown agent returns {"counts":{}}.
func (h *handler) logstats(w http.ResponseWriter, r *http.Request) {
	agent := r.URL.Query().Get("agent")
	if agent == "" {
		http.Error(w, "agent required", http.StatusBadRequest)
		return
	}
	since := nowMs() - errorWindowMs
	counts, err := h.logsHist.ErrorCounts(agent, since)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if counts == nil {
		counts = map[string]int64{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"counts": counts})
}

func nowMs() int64 { return time.Now().UnixMilli() }
