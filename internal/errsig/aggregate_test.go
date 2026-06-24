package errsig

import (
	"strings"
	"testing"
)

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

func TestAggregateSourceStopsAtAgentBoundary(t *testing.T) {
	lines := []Line{
		// agent A: error line, NO source in its own following lines
		{TsMs: 1000, Label: "api#0", Text: "boom error", Agent: "edge-1"},
		// agent B: SAME label, would be the "following line" if no agent guard;
		// contains a Go source frame that must NOT be attributed to agent A.
		{TsMs: 2000, Label: "api#0", Text: "\t/srv/b/worker.go:99 +0x1a", Agent: "edge-2"},
	}
	r := Aggregate(lines, 0, 10_000, 24)
	// The "boom error" signature (agent edge-1) must have empty Source — the worker.go
	// frame belongs to edge-2 and must not bleed across the agent boundary.
	var boom *Sig
	for idx := range r.Signatures {
		if r.Signatures[idx].Sample == "boom error" {
			boom = &r.Signatures[idx]
		}
	}
	if boom == nil {
		t.Fatal("expected a 'boom error' signature")
	}
	if boom.Source != "" {
		t.Fatalf("Source bled across agent boundary: got %q, want empty", boom.Source)
	}
}

func TestAggregateSourceOnlyFromTraceHeaders(t *testing.T) {
	lines := []Line{
		{TsMs: 1000, Label: "api#0", Agent: "a", Text: "ERROR: connection refused to db"},
		{TsMs: 1001, Label: "api#0", Agent: "a", Text: "panic: nil pointer"},
		{TsMs: 1002, Label: "api#0", Agent: "a", Text: "\t/srv/app/worker.go:142 +0x1a"},
	}
	r := Aggregate(lines, 0, 10_000, 24)
	var conn, pan *Sig
	for idx := range r.Signatures {
		switch {
		case strings.HasPrefix(r.Signatures[idx].Sample, "ERROR: connection"):
			conn = &r.Signatures[idx]
		case strings.HasPrefix(r.Signatures[idx].Sample, "panic:"):
			pan = &r.Signatures[idx]
		}
	}
	if conn == nil || pan == nil {
		t.Fatalf("missing signatures: conn=%v pan=%v", conn, pan)
	}
	if conn.Source != "" {
		t.Errorf("plain error stole a frame: Source=%q, want empty", conn.Source)
	}
	if pan.Source != "worker.go:142" {
		t.Errorf("panic Source=%q, want worker.go:142", pan.Source)
	}
}
