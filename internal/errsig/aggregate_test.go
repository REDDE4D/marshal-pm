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
