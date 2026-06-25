package server

import (
	"testing"

	"github.com/REDDE4D/marshal-pm/internal/logstore"
)

func TestLogStoresLazyOpenAndHas(t *testing.T) {
	ss := newLogStores(t.TempDir())
	defer ss.closeAll()
	if ss.has("web-1") {
		t.Fatal("has(web-1) true before any contact")
	}
	st, err := ss.get("web-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ss.has("web-1") {
		t.Fatal("has(web-1) false after get")
	}
	st2, _ := ss.get("web-1")
	if st != st2 {
		t.Fatal("get returned a different handle for the same agent")
	}
}

func TestLogStoresPruneAll(t *testing.T) {
	ss := newLogStores(t.TempDir())
	defer ss.closeAll()
	st, _ := ss.get("web-1")
	_ = st.Append([]logstore.Line{{TsMs: 1000, Label: "a#0", Text: "old"}})
	_ = st.Append([]logstore.Line{{TsMs: 5000, Label: "a#0", Text: "new"}})
	ss.pruneAll(3000)
	if mx, _ := st.MaxTs(); mx != 5000 {
		t.Fatalf("MaxTs after prune = %d, want 5000", mx)
	}
}

func TestLogStoresSinceSelector(t *testing.T) {
	ls := newLogStores(t.TempDir())
	st, err := ls.get("dev-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = st.Append([]logstore.Line{
		{TsMs: 1, Label: "web#0", Text: "a"},
		{TsMs: 2, Label: "web#1", Text: "b"},
		{TsMs: 3, Label: "api#0", Text: "c"},
	})
	// selector "web" matches web#0 and web#1 (prefix), not api#0
	got, cur, err := ls.Since("dev-1", "web", 0, 100, logstore.StreamAny, "")
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(got) != 2 || got[0].Text != "a" || got[1].Text != "b" {
		t.Fatalf("Since web = %+v, want a then b", got)
	}
	if cur != got[1].RowID {
		t.Fatalf("cursor = %d, want %d", cur, got[1].RowID)
	}
	// unknown agent -> graceful empty
	got2, cur2, err := ls.Since("ghost", "web", 0, 100, logstore.StreamAny, "")
	if err != nil || len(got2) != 0 || cur2 != 0 {
		t.Fatalf("unknown agent = (%+v, %d, %v), want empty/0/nil", got2, cur2, err)
	}
}

func TestLogStoresErrorCounts(t *testing.T) {
	ls := newLogStores(t.TempDir())
	defer ls.closeAll()
	st, err := ls.get("dev-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Append([]logstore.Line{
		{TsMs: 200, Label: "web#0", Stderr: true, Text: "e"},
		{TsMs: 200, Label: "web#0", Stderr: false, Text: "o"},
		{TsMs: 200, Label: "api#0", Stderr: true, Text: "e"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := ls.ErrorCounts("dev-1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if got["web#0"] != 1 || got["api#0"] != 1 {
		t.Fatalf("counts = %v; want web#0:1 api#0:1", got)
	}
	// Unknown agent -> empty, no error.
	g2, err := ls.ErrorCounts("ghost", 0)
	if err != nil || len(g2) != 0 {
		t.Fatalf("unknown agent = (%v, %v); want ({}, nil)", g2, err)
	}
}

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
