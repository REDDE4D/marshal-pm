# M-F Errors / Exceptions Subsystem — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Group the fleet's mirrored stderr into deduplicated error **signatures**, expose them via `GET /api/errors`, and surface them on a minimal transitional Errors page.

**Architecture:** Compute-on-read, entirely server-side. A new pure package `internal/errsig` does classification, normalization, signature hashing, source-location extraction, and aggregation. The dashboard handler pulls the time-window's stderr from the server's existing per-agent logstore (`logstore.StderrSince`), tags each line with its agent, runs `errsig.Aggregate`, and returns JSON. No proto, agent, or persisted-store changes.

**Tech Stack:** Go (stdlib `regexp`, `crypto/sha256`), modernc.org/sqlite (existing logstore), React/TypeScript SPA (Vite), `make` targets.

**Spec:** `docs/superpowers/specs/2026-06-24-mF-errors-subsystem-design.md`

## Global Constraints

- TDD: failing test first, then minimal implementation. Go table-driven tests.
- `internal/errsig` is **pure**: no DB, no I/O, no goroutines, no time-of-day calls (timestamps passed in).
- Server log retention is **7 days**; `range=all` clamps to a 7-day window.
- Signature key = normalized message, **fleet-wide**; track distinct affected proc labels.
- Bucket count is the constant **24** for every range.
- Commit messages: imperative subject + trailer `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Branch: `mF-errors-subsystem` (already created off `dev`; spec already committed).
- Final gates before done: `go test ./... -race -count=1`, `go vet ./...`, `gofmt -l .` (empty), `make build`, `make ui` (commit the rebuilt bundle).

---

### Task 1: `errsig` classification, normalization, signature

**Files:**
- Create: `internal/errsig/errsig.go`
- Test: `internal/errsig/errsig_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `func IsError(text string) bool`
  - `func Normalize(text string) string`
  - `func Signature(text string) string` — 12-hex-char stable id.

- [ ] **Step 1: Write the failing test**

```go
package errsig

import "testing"

func TestIsError(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"panic: nil pointer dereference", true},
		{"ERROR: connection refused", true},
		{"Traceback (most recent call last):", true},
		{"level=error msg=\"boom\"", true},
		{"plain stderr line with no level", true}, // stderr default
		{"level=info msg=started", false},
		{"[INFO] listening on :8080", false},
		{"level=warn retrying", false},
		{"DEBUG cache miss", false},
	}
	for _, c := range cases {
		if got := IsError(c.text); got != c.want {
			t.Errorf("IsError(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestNormalizeCollapsesVariants(t *testing.T) {
	a := Normalize("2026-06-24T10:00:00Z connection to 10.0.0.5:5432 failed after 1.5s")
	b := Normalize("2026-06-24T11:22:33Z connection to 10.0.0.9:6000 failed after 240ms")
	if a != b {
		t.Fatalf("variants did not collapse:\n a=%q\n b=%q", a, b)
	}
	if a == "" {
		t.Fatal("normalized to empty")
	}
}

func TestNormalizeKeepsDistinct(t *testing.T) {
	if Normalize("disk full") == Normalize("connection refused") {
		t.Fatal("distinct messages collapsed")
	}
}

func TestSignatureStableAndShort(t *testing.T) {
	s1 := Signature("error code 42 at 10.0.0.1:1")
	s2 := Signature("error code 99 at 10.0.0.2:2")
	if s1 != s2 {
		t.Fatalf("variant signatures differ: %s vs %s", s1, s2)
	}
	if len(s1) != 12 {
		t.Fatalf("signature length = %d, want 12", len(s1))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/errsig/ -run 'IsError|Normalize|Signature' -v`
Expected: FAIL (undefined: `IsError`, `Normalize`, `Signature`).

- [ ] **Step 3: Write minimal implementation**

```go
// Package errsig turns raw stderr lines into deduplicated error signatures.
// It is pure: no I/O, no DB, no wall-clock — all timestamps are passed in.
package errsig

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// infoWarnMarkers, when present and no error marker is present, mark a line as
// NOT an error (info/warn/debug on stderr). Lowercased substring match.
var infoWarnMarkers = []string{
	"level=info", "level=debug", "level=trace", "level=warn", "level=warning",
	"[info]", "[debug]", "[trace]", "[warn]", "[warning]", "[notice]",
}

// errorMarkers force a line to count as an error even if it also matches an
// info/warn token. Lowercased substring match.
var errorMarkers = []string{
	"error", "fatal", "panic", "exception",
	"traceback (most recent call last)", "level=error",
}

// IsError reports whether a stderr line should be grouped as an error. Explicit
// error markers always count; recognized info/warn/debug markers are excluded;
// everything else on stderr counts (stderr default).
func IsError(text string) bool {
	l := strings.ToLower(text)
	for _, m := range errorMarkers {
		if strings.Contains(l, m) {
			return true
		}
	}
	for _, m := range infoWarnMarkers {
		if strings.Contains(l, m) {
			return false
		}
	}
	return true
}

var (
	reTimestamp = regexp.MustCompile(`^\s*\[?\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:?\d{2})?\]?\s*`)
	reTimeOnly  = regexp.MustCompile(`^\s*\[?\d{2}:\d{2}:\d{2}(\.\d+)?\]?\s*`)
	reQuoted    = regexp.MustCompile("\"[^\"]*\"|'[^']*'|`[^`]*`")
	reUUID      = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	reHex       = regexp.MustCompile(`0x[0-9a-fA-F]+|\b[0-9a-fA-F]{12,}\b`)
	reAddr      = regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}(:\d+)?\b`)
	rePath      = regexp.MustCompile(`(?:[A-Za-z]:\\|/|\./)[^\s:]+(?::\d+)?`)
	reNum       = regexp.MustCompile(`(?i)\b\d+(\.\d+)?(ns|us|µs|ms|s|m|h|ki?b|mi?b|gi?b|b)?\b`)
	reSpace     = regexp.MustCompile(`\s+`)
)

// Normalize canonicalizes a message for grouping: strip leading timestamp,
// then replace quoted strings, UUIDs, hex/addresses, IPs, paths, and numbers
// with placeholders; collapse whitespace; lowercase.
func Normalize(text string) string {
	s := reTimestamp.ReplaceAllString(text, "")
	s = reTimeOnly.ReplaceAllString(s, "")
	s = reQuoted.ReplaceAllString(s, "<str>")
	s = reUUID.ReplaceAllString(s, "<uuid>")
	s = reHex.ReplaceAllString(s, "<hex>")
	s = reAddr.ReplaceAllString(s, "<addr>")
	s = rePath.ReplaceAllString(s, "<path>")
	s = reNum.ReplaceAllString(s, "<num>")
	s = reSpace.ReplaceAllString(s, " ")
	return strings.ToLower(strings.TrimSpace(s))
}

// Signature is a stable 12-hex-char id for a normalized message.
func Signature(text string) string {
	sum := sha256.Sum256([]byte(Normalize(text)))
	return hex.EncodeToString(sum[:])[:12]
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/errsig/ -run 'IsError|Normalize|Signature' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/errsig/errsig.go internal/errsig/errsig_test.go
git commit -m "$(printf 'feat(errsig): classification, normalization, signature\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

---

### Task 2: `errsig` best-effort source-location extraction

**Files:**
- Modify: `internal/errsig/errsig.go`
- Test: `internal/errsig/source_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `func Source(window []string) string` — `file:line` or `""`. `window[0]` is the error line; later entries are following lines (same proc) for stack-trace scanning.

- [ ] **Step 1: Write the failing test**

```go
package errsig

import "testing"

func TestSourceGoPanic(t *testing.T) {
	win := []string{
		"panic: runtime error: invalid memory address",
		"goroutine 1 [running]:",
		"main.work(...)",
		"\t/home/app/worker.go:142 +0x1a",
	}
	if got := Source(win); got != "worker.go:142" {
		t.Errorf("Source = %q, want worker.go:142", got)
	}
}

func TestSourcePythonTraceback(t *testing.T) {
	win := []string{
		"Traceback (most recent call last):",
		"  File \"/srv/app/main.py\", line 88, in handler",
		"ValueError: bad input",
	}
	if got := Source(win); got != "main.py:88" {
		t.Errorf("Source = %q, want main.py:88", got)
	}
}

func TestSourceNone(t *testing.T) {
	if got := Source([]string{"connection refused"}); got != "" {
		t.Errorf("Source = %q, want empty", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/errsig/ -run Source -v`
Expected: FAIL (undefined: `Source`).

- [ ] **Step 3: Write minimal implementation**

Append to `internal/errsig/errsig.go`:

```go
var (
	reSrcPy = regexp.MustCompile(`File "([^"]+)", line (\d+)`)
	reSrcGo = regexp.MustCompile(`([\w./\\-]+\.(?:go|py|js|ts|rb|rs|java|c|cc|cpp|h|hpp|php|cs|kt|swift|scala|ex|exs)):(\d+)`)
	reSrcAt = regexp.MustCompile(`\bat ([\w./\\-]+):(\d+)`)
)

// Source returns a best-effort "file:line" from the error line plus a few
// following lines (a stack trace), or "" when nothing recognizable is found.
func Source(window []string) string {
	for _, ln := range window {
		if m := reSrcPy.FindStringSubmatch(ln); m != nil {
			return baseName(m[1]) + ":" + m[2]
		}
		if m := reSrcGo.FindStringSubmatch(ln); m != nil {
			return baseName(m[1]) + ":" + m[2]
		}
		if m := reSrcAt.FindStringSubmatch(ln); m != nil {
			return baseName(m[1]) + ":" + m[2]
		}
	}
	return ""
}

// baseName returns the last path element (handles / and \).
func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/errsig/ -run Source -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/errsig/errsig.go internal/errsig/source_test.go
git commit -m "$(printf 'feat(errsig): best-effort source-location extraction\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

---

### Task 3: `errsig` aggregation (cluster + signatures + buckets)

**Files:**
- Create: `internal/errsig/aggregate.go`
- Test: `internal/errsig/aggregate_test.go`

**Interfaces:**
- Consumes: `IsError`, `Signature`, `Source` (Tasks 1–2).
- Produces:
  - `type Line struct { TsMs int64; Label, Text, Agent string }`
  - `type Sig struct { Id, Sample, Source, Agent, Proc string; Affected []string; Count int; FirstUnix, LastUnix int64; Buckets []int }`
  - `type Cluster struct { Errors, Signatures, AffectedProcs int; LastErrorUnix int64 }`
  - `type Result struct { Cluster Cluster; Signatures []Sig }`
  - `func Aggregate(lines []Line, sinceMs, nowMs int64, nBuckets int) Result` — input ascending by `(Label, TsMs)`; non-error lines and lines before `sinceMs` ignored; signatures sorted Count desc, then LastUnix desc.

- [ ] **Step 1: Write the failing test**

```go
package errsig

import "testing"

func mkLine(ts int64, label, text string) Line {
	return Line{TsMs: ts, Label: label, Text: text, Agent: "edge-1"}
}

func TestAggregateGroupsVariantsAndCounts(t *testing.T) {
	since, now := int64(0), int64(24_000)
	lines := []Line{
		mkLine(1000, "api#0", "connection to 10.0.0.1:5432 failed"),
		mkLine(2000, "api#1", "connection to 10.0.0.9:6000 failed"),
		mkLine(3000, "api#0", "level=info msg=started"), // excluded
		mkLine(4000, "api#0", "disk full"),
	}
	r := Aggregate(lines, since, now, 24)
	if r.Cluster.Errors != 3 {
		t.Fatalf("cluster.Errors = %d, want 3", r.Cluster.Errors)
	}
	if r.Cluster.Signatures != 2 {
		t.Fatalf("cluster.Signatures = %d, want 2", r.Cluster.Signatures)
	}
	if r.Cluster.AffectedProcs != 2 {
		t.Fatalf("cluster.AffectedProcs = %d, want 2", r.Cluster.AffectedProcs)
	}
	if r.Cluster.LastErrorUnix != 4 { // 4000ms -> 4s
		t.Fatalf("cluster.LastErrorUnix = %d, want 4", r.Cluster.LastErrorUnix)
	}
	// connection signature is first (count 2 > 1).
	top := r.Signatures[0]
	if top.Count != 2 {
		t.Fatalf("top.Count = %d, want 2", top.Count)
	}
	if len(top.Affected) != 2 {
		t.Fatalf("top.Affected = %v, want 2 procs", top.Affected)
	}
	if len(top.Buckets) != 24 {
		t.Fatalf("len(Buckets) = %d, want 24", len(top.Buckets))
	}
}

func TestAggregateBucketsPlaceByTime(t *testing.T) {
	// window [0,24000], 24 buckets -> 1000ms each. ts 500 -> bucket 0; ts 23999 -> bucket 23.
	r := Aggregate([]Line{
		mkLine(500, "a#0", "boom error"),
		mkLine(23999, "a#0", "boom error"),
	}, 0, 24_000, 24)
	b := r.Signatures[0].Buckets
	if b[0] != 1 || b[23] != 1 {
		t.Fatalf("buckets = %v, want b[0]=1 and b[23]=1", b)
	}
}

func TestAggregateEmpty(t *testing.T) {
	r := Aggregate(nil, 0, 1000, 24)
	if r.Cluster.Errors != 0 || len(r.Signatures) != 0 {
		t.Fatalf("empty input produced %+v", r)
	}
}

func TestAggregateIgnoresBeforeSince(t *testing.T) {
	r := Aggregate([]Line{mkLine(50, "a#0", "old error")}, 100, 1000, 24)
	if r.Cluster.Errors != 0 {
		t.Fatalf("pre-since line counted: %+v", r.Cluster)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/errsig/ -run Aggregate -v`
Expected: FAIL (undefined: `Aggregate`, `Line`, …).

- [ ] **Step 3: Write minimal implementation**

```go
package errsig

import "sort"

// Line is one stderr line tagged with its originating agent.
type Line struct {
	TsMs  int64
	Label string // "app#instance"
	Text  string
	Agent string
}

// Sig is one error signature's rollup over the window.
type Sig struct {
	Id        string
	Sample    string // first raw occurrence, for display
	Source    string // best-effort file:line, "" if unknown
	Agent     string // representative (most recent) origin
	Proc      string // representative (most recent) proc label
	Affected  []string
	Count     int
	FirstUnix int64
	LastUnix  int64
	Buckets   []int
}

// Cluster holds the headline totals for the window.
type Cluster struct {
	Errors        int
	Signatures    int
	AffectedProcs int
	LastErrorUnix int64
}

// Result is the full /api/errors payload (pre-JSON).
type Result struct {
	Cluster    Cluster
	Signatures []Sig
}

// Aggregate folds error lines (ascending by Label,TsMs) into the cluster totals
// and the signature ledger. Lines before sinceMs or failing IsError are ignored.
func Aggregate(lines []Line, sinceMs, nowMs int64, nBuckets int) Result {
	if nBuckets < 1 {
		nBuckets = 1
	}
	span := nowMs - sinceMs
	if span <= 0 {
		span = 1
	}
	type acc struct {
		sig      *Sig
		affected map[string]bool
	}
	m := map[string]*acc{}
	var order []*acc
	cluster := Cluster{}
	allProcs := map[string]bool{}

	for i := range lines {
		ln := lines[i]
		if ln.TsMs < sinceMs || !IsError(ln.Text) {
			continue
		}
		cluster.Errors++
		sec := ln.TsMs / 1000
		if sec > cluster.LastErrorUnix {
			cluster.LastErrorUnix = sec
		}
		allProcs[ln.Label] = true
		id := Signature(ln.Text)
		a := m[id]
		if a == nil {
			win := []string{ln.Text}
			for j := i + 1; j < len(lines) && len(win) < 6; j++ {
				if lines[j].Label != ln.Label {
					break
				}
				win = append(win, lines[j].Text)
			}
			a = &acc{
				sig: &Sig{
					Id: id, Sample: ln.Text, Source: Source(win),
					Buckets: make([]int, nBuckets), FirstUnix: sec,
				},
				affected: map[string]bool{},
			}
			m[id] = a
			order = append(order, a)
		}
		s := a.sig
		s.Count++
		s.LastUnix = sec
		s.Agent = ln.Agent
		s.Proc = ln.Label
		a.affected[ln.Label] = true
		b := int((ln.TsMs - sinceMs) * int64(nBuckets) / span)
		if b < 0 {
			b = 0
		}
		if b >= nBuckets {
			b = nBuckets - 1
		}
		s.Buckets[b]++
	}

	sigs := make([]Sig, 0, len(order))
	for _, a := range order {
		aff := make([]string, 0, len(a.affected))
		for p := range a.affected {
			aff = append(aff, p)
		}
		sort.Strings(aff)
		a.sig.Affected = aff
		sigs = append(sigs, *a.sig)
	}
	sort.SliceStable(sigs, func(i, j int) bool {
		if sigs[i].Count != sigs[j].Count {
			return sigs[i].Count > sigs[j].Count
		}
		return sigs[i].LastUnix > sigs[j].LastUnix
	})
	cluster.Signatures = len(sigs)
	cluster.AffectedProcs = len(allProcs)
	return Result{Cluster: cluster, Signatures: sigs}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/errsig/ -v`
Expected: PASS (all errsig tests).

- [ ] **Step 5: Commit**

```bash
git add internal/errsig/aggregate.go internal/errsig/aggregate_test.go
git commit -m "$(printf 'feat(errsig): aggregate lines into cluster + signature ledger\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

---

### Task 4: `logstore.StderrSince` query

**Files:**
- Modify: `internal/logstore/store.go`
- Test: `internal/logstore/store_test.go`

**Interfaces:**
- Consumes: existing `logstore.Store`, `StoredLine`.
- Produces: `func (s *Store) StderrSince(labels []string, sinceMs int64) ([]StoredLine, error)` — stderr rows (`stderr=1`) for `labels` with `ts >= sinceMs`, ordered by `(label, ts)` ascending.

- [ ] **Step 1: Write the failing test**

Add to `internal/logstore/store_test.go`:

```go
func TestStderrSince(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Append([]Line{
		{TsMs: 100, Label: "a#0", Stderr: true, Text: "old"},   // before since
		{TsMs: 200, Label: "a#0", Stderr: false, Text: "out"},  // stdout
		{TsMs: 300, Label: "a#1", Stderr: true, Text: "b-err"},
		{TsMs: 400, Label: "a#0", Stderr: true, Text: "a-err"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.StderrSince([]string{"a#0", "a#1"}, 150)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(got), got)
	}
	// ordered by (label, ts): a#0/400 then a#1/300.
	if got[0].Label != "a#0" || got[0].Text != "a-err" || got[1].Label != "a#1" {
		t.Fatalf("order wrong: %+v", got)
	}
}
```

(`filepath` is already imported in `store_test.go`; if not, add it.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/logstore/ -run StderrSince -v`
Expected: FAIL (undefined: `StderrSince`).

- [ ] **Step 3: Write minimal implementation**

Add to `internal/logstore/store.go` (near `ErrorCounts`):

```go
// StderrSince returns stderr lines (stderr = 1) for the given labels with
// ts >= sinceMs, ordered by (label, ts) ascending so each proc's lines are
// contiguous and time-ordered. An empty label set returns no rows.
func (s *Store) StderrSince(labels []string, sinceMs int64) ([]StoredLine, error) {
	if len(labels) == 0 {
		return nil, nil
	}
	ph := make([]string, len(labels))
	args := make([]any, 0, len(labels)+1)
	for i, l := range labels {
		ph[i] = "?"
		args = append(args, l)
	}
	args = append(args, sinceMs)
	q := `SELECT ts, label, stderr, text FROM log_line WHERE label IN (` + strings.Join(ph, ",") +
		`) AND stderr = 1 AND ts >= ? ORDER BY label, ts`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredLine
	for rows.Next() {
		var ln StoredLine
		var se int64
		if err := rows.Scan(&ln.TsMs, &ln.Label, &se, &ln.Text); err != nil {
			return nil, err
		}
		ln.Stderr = se != 0
		out = append(out, ln)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/logstore/ -run StderrSince -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/logstore/store.go internal/logstore/store_test.go
git commit -m "$(printf 'feat(logstore): StderrSince query for error aggregation\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

---

### Task 5: `server.logStores.StderrSince` agent wrapper

**Files:**
- Modify: `internal/server/logstores.go`
- Test: `internal/server/logstores_test.go` (create if absent)

**Interfaces:**
- Consumes: `logstore.StderrSince` (Task 4), existing `logStores` (`has`, `get`, `Labels`).
- Produces: `func (s *logStores) StderrSince(agent string, sinceMs int64) ([]logstore.StoredLine, error)` — resolves the agent's labels and returns its stderr since `sinceMs`. Unknown agent → `(nil, nil)`.

- [ ] **Step 1: Write the failing test**

Create/append `internal/server/logstores_test.go`:

```go
package server

import (
	"testing"

	"marshal/internal/logstore"
)

func TestLogStoresStderrSince(t *testing.T) {
	ls := newLogStores(t.TempDir())
	st, err := ls.get("edge-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Append([]logstore.Line{
		{TsMs: 100, Label: "api#0", Stderr: true, Text: "boom"},
		{TsMs: 200, Label: "api#0", Stderr: false, Text: "ok"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := ls.StderrSince("edge-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "boom" {
		t.Fatalf("got %+v, want one stderr line", got)
	}
	// Unknown agent -> empty, no error.
	got, err = ls.StderrSince("nope", 0)
	if err != nil || got != nil {
		t.Fatalf("unknown agent = (%+v, %v), want (nil, nil)", got, err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run StderrSince -v`
Expected: FAIL (undefined: `(*logStores).StderrSince`).

- [ ] **Step 3: Write minimal implementation**

Add to `internal/server/logstores.go` (after `ErrorCounts`):

```go
// StderrSince returns one agent's stderr lines with ts >= sinceMs across all
// its labels, ordered by (label, ts). Unknown agent yields (nil, nil).
func (s *logStores) StderrSince(agent string, sinceMs int64) ([]logstore.StoredLine, error) {
	if !s.has(agent) {
		return nil, nil
	}
	st, err := s.get(agent)
	if err != nil {
		return nil, err
	}
	labels, err := st.Labels()
	if err != nil {
		return nil, err
	}
	return st.StderrSince(labels, sinceMs)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/server/ -run StderrSince -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/logstores.go internal/server/logstores_test.go
git commit -m "$(printf 'feat(server): per-agent StderrSince wrapper\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

---

### Task 6: `GET /api/errors` dashboard endpoint

**Files:**
- Create: `internal/dashboard/errors.go`
- Modify: `internal/dashboard/logs.go` (extend `LogsHistory` interface)
- Modify: `internal/dashboard/handlers.go` (register route)
- Test: `internal/dashboard/errors_test.go`
- Modify (stubs): `internal/dashboard/logs_test.go` (add `StderrSince` to `fakeLogs`, `recordingLogs`, `statLogs`)

**Interfaces:**
- Consumes: `errsig.Aggregate`/`Line` (Task 3), `logStores.StderrSince` (Task 5), `FleetLister.List()` (`[]*pb.AgentState`, field `GetName()`).
- Produces:
  - Extended `LogsHistory` with `StderrSince(agent string, sinceMs int64) ([]logstore.StoredLine, error)`.
  - Handler `func (h *handler) errors(w http.ResponseWriter, r *http.Request)` serving `GET /api/errors?range=&agent=`.

- [ ] **Step 1: Write the failing test**

Create `internal/dashboard/errors_test.go`:

```go
package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"marshal/internal/logstore"
	"marshal/internal/pb"
)

// errLogs is a fakeLogs whose StderrSince returns canned lines per agent.
type errLogs struct {
	fakeLogs
	byAgent map[string][]logstore.StoredLine
}

func (e *errLogs) StderrSince(agent string, sinceMs int64) ([]logstore.StoredLine, error) {
	return e.byAgent[agent], nil
}

type twoAgentLister struct{}

func (twoAgentLister) List() []*pb.AgentState {
	return []*pb.AgentState{{Name: "edge-1"}, {Name: "edge-2"}}
}

func TestErrorsEndpointAggregatesFleet(t *testing.T) {
	now := time.Now().UnixMilli()
	el := &errLogs{byAgent: map[string][]logstore.StoredLine{
		"edge-1": {
			{TsMs: now - 1000, Label: "api#0", Stderr: true, Text: "connection to 10.0.0.1:1 failed"},
			{TsMs: now - 900, Label: "api#1", Stderr: true, Text: "connection to 10.0.0.2:2 failed"},
		},
		"edge-2": {
			{TsMs: now - 500, Label: "web#0", Stderr: true, Text: "level=info up"}, // excluded
		},
	}}
	h := newHandler(twoAgentLister{}, &fakeMetrics{}, el, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour, "", "", nil)
	srv := httptest.NewServer(authed(h, "admin", "pw"))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/errors?range=24h")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct {
		Cluster struct {
			Errors        int `json:"errors"`
			Signatures    int `json:"signatures"`
			AffectedProcs int `json:"affected_procs"`
		} `json:"cluster"`
		Signatures []struct {
			Count    int      `json:"count"`
			Affected []string `json:"affected"`
			Buckets  []int    `json:"buckets"`
		} `json:"signatures"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Cluster.Errors != 2 || out.Cluster.Signatures != 1 || out.Cluster.AffectedProcs != 2 {
		t.Fatalf("cluster = %+v, want errors=2 sigs=1 procs=2", out.Cluster)
	}
	if len(out.Signatures) != 1 || out.Signatures[0].Count != 2 || len(out.Signatures[0].Buckets) != 24 {
		t.Fatalf("signatures = %+v", out.Signatures)
	}
}
```

> **Note:** if no `authed(h, user, pass)` test helper exists in the package, drive the request through a real login first (see `logs_test.go` for the existing session-cookie pattern) or use the public `NewHandler` server form already used there. Reuse whatever pattern `logs_test.go` uses; do not invent a new auth path.

- [ ] **Step 2: Run test to verify it fails (and update fakes to compile)**

The `LogsHistory` interface will gain `StderrSince`, so the existing fakes must satisfy it. Add to `internal/dashboard/logs_test.go`:

```go
func (f *fakeLogs) StderrSince(string, int64) ([]logstore.StoredLine, error) { return nil, nil }
func (r *recordingLogs) StderrSince(string, int64) ([]logstore.StoredLine, error) { return nil, nil }
func (s statLogs) StderrSince(string, int64) ([]logstore.StoredLine, error) { return nil, nil }
```

Run: `go test ./internal/dashboard/ -run Errors -v`
Expected: FAIL (undefined: `errors` handler / route 404), compiles once stubs are added.

- [ ] **Step 3: Write minimal implementation**

Extend the interface in `internal/dashboard/logs.go`:

```go
type LogsHistory interface {
	Since(agent, selector string, afterRowID int64, limit int, filter logstore.StreamFilter, text string) ([]logstore.StoredLine, int64, error)
	ErrorCounts(agent string, sinceMs int64) (map[string]int64, error)
	StderrSince(agent string, sinceMs int64) ([]logstore.StoredLine, error)
}
```

Create `internal/dashboard/errors.go`:

```go
package dashboard

import (
	"net/http"

	"marshal/internal/errsig"
)

const (
	errSparkBuckets = 24
	errMaxScan      = 200_000 // worst-case lines per request before truncation
	dayMs           = 24 * 60 * 60 * 1000
	retentionMs     = 7 * dayMs
)

type errSigView struct {
	ID        string   `json:"id"`
	Sample    string   `json:"sample"`
	Source    string   `json:"source,omitempty"`
	Agent     string   `json:"agent"`
	Proc      string   `json:"proc"`
	Affected  []string `json:"affected"`
	Count     int      `json:"count"`
	FirstUnix int64    `json:"first_unix"`
	LastUnix  int64    `json:"last_unix"`
	Buckets   []int    `json:"buckets"`
}

type errClusterView struct {
	Errors        int   `json:"errors"`
	Signatures    int   `json:"signatures"`
	AffectedProcs int   `json:"affected_procs"`
	LastErrorUnix int64 `json:"last_error_unix"`
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
			agents = append(agents, ag.GetName())
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
	for _, s := range res.Signatures {
		out.Signatures = append(out.Signatures, errSigView{
			ID: s.Id, Sample: s.Sample, Source: s.Source, Agent: s.Agent, Proc: s.Proc,
			Affected: s.Affected, Count: s.Count, FirstUnix: s.FirstUnix, LastUnix: s.LastUnix,
			Buckets: s.Buckets,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
```

Register the route in `internal/dashboard/handlers.go` after the `logstats` line:

```go
	mux.HandleFunc("GET /api/errors", h.requireSession(h.errors))
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/dashboard/ -run Errors -v`
Expected: PASS.
Then the whole package: `go test ./internal/dashboard/ -v` — Expected: PASS (fakes satisfy the extended interface).

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/errors.go internal/dashboard/logs.go internal/dashboard/handlers.go internal/dashboard/errors_test.go internal/dashboard/logs_test.go
git commit -m "$(printf 'feat(dashboard): GET /api/errors signature ledger endpoint\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

---

### Task 7: Transitional Errors page (SPA) + CHANGELOG

**Files:**
- Modify: `web/src/router.ts` (add `errors` route)
- Modify: `web/src/api.ts` (types + `getErrors`)
- Create: `web/src/Errors.tsx`
- Modify: `web/src/App.tsx` (route → `<Errors/>`)
- Modify: `web/src/Overview.tsx` (nav button to `#/errors`)
- Modify: `CHANGELOG.md` (`[Unreleased]` Added entry)
- Build artifact: `internal/dashboard/dist/...` via `make ui`

**Interfaces:**
- Consumes: `GET /api/errors` (Task 6).
- Produces: `#/errors` route rendering the cluster + signature ledger.

> **Note:** the SPA has no unit-test harness; verify this task by `make ui` building cleanly and by the live demo (Task 8). Match existing component/style conventions (`Overview.tsx`, `Sparkline.tsx`, `styles.css`).

- [ ] **Step 1: Add the route**

In `web/src/router.ts`, extend the `Route` union and `parseHash`:

```ts
export type Route = { name: "overview" } | { name: "detail"; agent: string; proc: string } | { name: "credentials" } | { name: "notifications" } | { name: "errors" };
```

Add to `parseHash` (before the `#/notifications` checks is fine):

```ts
  if (hash === "#/errors") return { name: "errors" };
```

- [ ] **Step 2: Add the API client**

Append to `web/src/api.ts`:

```ts
export type ErrSignature = {
  id: string;
  sample: string;
  source?: string;
  agent: string;
  proc: string;
  affected: string[];
  count: number;
  first_unix: number;
  last_unix: number;
  buckets: number[];
};

export type ErrorsResponse = {
  range: string;
  since: number;
  now: number;
  cluster: { errors: number; signatures: number; affected_procs: number; last_error_unix: number };
  signatures: ErrSignature[];
  truncated: boolean;
};

export async function getErrors(range: string, agent?: string): Promise<ErrorsResponse> {
  const q = new URLSearchParams({ range });
  if (agent) q.set("agent", agent);
  const r = await fetch(`/api/errors?${q.toString()}`);
  if (r.status === 401) throw new Error("unauthorized");
  if (!r.ok) throw new Error(`errors ${r.status}`);
  return (await r.json()) as ErrorsResponse;
}
```

- [ ] **Step 3: Create the page component**

Create `web/src/Errors.tsx`:

```tsx
import { useEffect, useState } from "react";
import { getErrors, type ErrorsResponse } from "./api";
import { Sparkline } from "./Sparkline";

const RANGES = ["24h", "7d", "all"];

function ago(unixSec: number): string {
  if (!unixSec) return "—";
  const s = Math.max(0, Math.floor(Date.now() / 1000 - unixSec));
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}

export function Errors() {
  const [range, setRange] = useState("24h");
  const [data, setData] = useState<ErrorsResponse | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    setErr(null);
    setData(null);
    getErrors(range)
      .then((d) => live && setData(d))
      .catch((e) => live && setErr(String(e)));
    return () => {
      live = false;
    };
  }, [range]);

  return (
    <div className="page">
      <header className="topbar">
        <h1>Errors</h1>
        <button className="btn" onClick={() => { window.location.hash = "#/"; }}>← fleet</button>
      </header>

      <div className="range-tabs">
        {RANGES.map((r) => (
          <button key={r} className={r === range ? "btn active" : "btn"} onClick={() => setRange(r)}>{r}</button>
        ))}
      </div>

      {err && <p className="error">Failed to load: {err}</p>}
      {!err && !data && <p>Loading…</p>}

      {data && (
        <>
          <div className="cluster">
            <span>Errors <b>{data.cluster.errors}</b></span>
            <span>Signatures <b>{data.cluster.signatures}</b></span>
            <span>Affected procs <b>{data.cluster.affected_procs}</b></span>
            <span>Last error <b>{ago(data.cluster.last_error_unix)}</b></span>
          </div>
          {data.truncated && <p className="warn">Showing a partial window (scan cap reached).</p>}
          {data.signatures.length === 0 ? (
            <p>No errors in this window. 🎉</p>
          ) : (
            <table className="ledger">
              <thead>
                <tr><th>Message</th><th>Source</th><th>Where</th><th>Count</th><th>Last</th><th>Trend</th></tr>
              </thead>
              <tbody>
                {data.signatures.map((s) => (
                  <tr key={s.id}>
                    <td className="mono sample">{s.sample}</td>
                    <td className="mono">{s.source || "—"}</td>
                    <td>{s.agent} · {s.affected.length} proc{s.affected.length === 1 ? "" : "s"}</td>
                    <td>{s.count}</td>
                    <td>{ago(s.last_unix)}</td>
                    <td><Sparkline points={s.buckets} color="#E5707E" /></td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </>
      )}
    </div>
  );
}
```

- [ ] **Step 4: Wire the route and nav**

In `web/src/App.tsx`, add the import and route (alongside the other `route.name` checks):

```tsx
import { Errors } from "./Errors";
// ...
  if (route.name === "errors") return <Errors />;
```

In `web/src/Overview.tsx`, add a nav button next to the existing `credentials`/`notifications` buttons (around line 110):

```tsx
          <button className="btn" onClick={() => { window.location.hash = "#/errors"; }}>errors</button>
```

- [ ] **Step 5: Add the CHANGELOG entry**

Under `## [Unreleased]` → `### Added` in `CHANGELOG.md`, add:

```markdown
- **Errors/exceptions subsystem (M-F):** server-side error-signature grouping — stderr is
  normalized and deduplicated into signatures with occurrence counts, first/last-seen, affected
  processes, best-effort source location, and a 24-bucket occurrence trend. New `GET /api/errors`
  endpoint (range `24h`/`7d`/`all`, optional `agent` filter) and a transitional Errors page
  (`#/errors`).
```

- [ ] **Step 6: Build the bundle and verify it compiles**

Run: `make ui`
Expected: Vite builds with no TypeScript errors; `internal/dashboard/dist/assets/...` updates.
Then: `make build` — Expected: builds clean (embeds the new bundle).

- [ ] **Step 7: Commit**

```bash
git add web/src CHANGELOG.md internal/dashboard/dist
git commit -m "$(printf 'feat(ui): transitional Errors page + /api/errors client (M-F)\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

---

### Task 8: Full gates, live demo, handoff

**Files:**
- Create: `docs/handoffs/2026-06-24-mF-errors-subsystem.md`

- [ ] **Step 1: Run the full gate suite**

Run:
```bash
go test ./... -race -count=1
go vet ./...
gofmt -l .
make build
```
Expected: tests PASS (incl. `internal/errsig`), `vet` clean, `gofmt -l .` prints nothing, build OK. Fix anything that isn't, then re-run.

- [ ] **Step 2: Live demo (per CLAUDE.md convention)**

Use a scratch data dir and the standard demo ports (`:9000`/`:9001`), set the password + rotate the enroll token **while the server is down**, then start the server and enroll an agent (see prior milestone handoffs for the exact sequence). Run a demo app that emits varied stderr — at minimum:
- a Go-style `panic:` with a `worker.go:NN` stack frame,
- a Python-style `Traceback` ending in `File "x.py", line NN`,
- repeated `connection refused`/`connection to <ip>:<port> failed` with **varying** IPs/ports.

Confirm via `curl https://localhost:9001/api/errors?range=24h` (with the session cookie) and in-browser at `#/errors`:
- the connection variants **collapse to one signature** with `count` climbing and 2+ `affected`,
- the panic signature shows `source: worker.go:NN`,
- `level=info`/`[INFO]` lines do **not** appear,
- cluster totals and the bar-sparkline render.

Tear down: stop demo app + agent + server by data dir (no broad `pkill`), remove the scratch dir, confirm `pgrep -fl marshal` shows only the standing launchd daemon.

- [ ] **Step 3: Write the handoff**

Create `docs/handoffs/2026-06-24-mF-errors-subsystem.md` covering: what M-F added, the compute-on-read architecture + the key finding (server already mirrors stderr), files changed, quality-gate results, live-demo observations, deferred items (agent-keyed signatures, materialized sigstore, styled page → M-A), and the next step (merge `--no-ff` to `dev`; then only **M-A** remains).

- [ ] **Step 4: Commit the handoff**

```bash
git add docs/handoffs/2026-06-24-mF-errors-subsystem.md
git commit -m "$(printf 'docs(mF): errors-subsystem handoff\n\nCo-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>')"
```

---

## Self-Review

**Spec coverage:**
- `errsig` IsError/Normalize/Signature → Task 1. ✓
- Source extraction → Task 2. ✓
- Aggregate (cluster, signatures, buckets, affected, sorting) → Task 3. ✓
- `logstore.StderrSince` → Task 4. ✓
- server agent wrapper → Task 5. ✓
- `/api/errors` (range, agent filter, fleet-wide, truncation guard, JSON shape) → Task 6. ✓
- Transitional Errors page (route, api client, page, nav, sparkline) → Task 7. ✓
- CHANGELOG → Task 7. ✓
- Gates + live demo + handoff → Task 8. ✓
- Deferred items recorded in spec + handoff. ✓

**Placeholder scan:** No TBD/TODO; every code step shows full code. The Task 6 auth-helper note and Task 7 SPA-has-no-tests note point to existing patterns rather than leaving a blank — acceptable since they direct the engineer to a concrete in-repo reference.

**Type consistency:** `errsig.Line{TsMs,Label,Text,Agent}` used identically in Tasks 3 and 6. `Sig` fields (`Id`,`Sample`,`Source`,`Agent`,`Proc`,`Affected`,`Count`,`FirstUnix`,`LastUnix`,`Buckets`) map 1:1 to `errSigView` JSON tags in Task 6. `StderrSince(labels []string, sinceMs int64)` (logstore, Task 4) vs `StderrSince(agent string, sinceMs int64)` (server/dashboard, Tasks 5–6) — intentionally different receivers/first-arg, consistent within each layer. `errSparkBuckets = 24` matches the spec's fixed bucket count and the tests' `len(Buckets)==24`.
