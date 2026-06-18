package logstore

import (
	"strings"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir() + "/logs.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestAppendTailMaxTsLabels(t *testing.T) {
	st := open(t)
	if mx, _ := st.MaxTs(); mx != 0 {
		t.Fatalf("MaxTs on empty = %d, want 0", mx)
	}
	err := st.Append([]Line{
		{TsMs: 1000, Label: "api#0", Stderr: false, Text: "a"},
		{TsMs: 2000, Label: "api#0", Stderr: true, Text: "b"},
		{TsMs: 3000, Label: "api#1", Stderr: false, Text: "c"},
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if mx, _ := st.MaxTs(); mx != 3000 {
		t.Fatalf("MaxTs = %d, want 3000", mx)
	}
	labels, _ := st.Labels()
	if len(labels) != 2 || labels[0] != "api#0" || labels[1] != "api#1" {
		t.Fatalf("Labels = %v, want [api#0 api#1]", labels)
	}
	got, _ := st.Tail("api#0", 10, StreamAny, "")
	if len(got) != 2 || got[0].Text != "a" || got[1].Text != "b" {
		t.Fatalf("Tail(api#0) = %+v, want a then b ascending", got)
	}
}

func TestTailLimitAndStreamFilter(t *testing.T) {
	st := open(t)
	_ = st.Append([]Line{
		{TsMs: 1, Label: "x#0", Stderr: false, Text: "out1"},
		{TsMs: 2, Label: "x#0", Stderr: true, Text: "err1"},
		{TsMs: 3, Label: "x#0", Stderr: false, Text: "out2"},
	})
	// limit keeps the newest, still ascending
	got, _ := st.Tail("x#0", 2, StreamAny, "")
	if len(got) != 2 || got[0].Text != "err1" || got[1].Text != "out2" {
		t.Fatalf("Tail limit=2 = %+v, want err1 then out2", got)
	}
	// stderr filter
	gotErr, _ := st.Tail("x#0", 10, StreamStderr, "")
	if len(gotErr) != 1 || gotErr[0].Text != "err1" {
		t.Fatalf("Tail stderr = %+v, want [err1]", gotErr)
	}
	// stdout filter
	gotOut, _ := st.Tail("x#0", 10, StreamStdout, "")
	if len(gotOut) != 2 || gotOut[0].Text != "out1" || gotOut[1].Text != "out2" {
		t.Fatalf("Tail stdout = %+v, want out1 then out2", gotOut)
	}
}

func TestPrune(t *testing.T) {
	st := open(t)
	_ = st.Append([]Line{
		{TsMs: 1000, Label: "x#0", Text: "old"},
		{TsMs: 5000, Label: "x#0", Text: "new"},
	})
	n, _ := st.Prune(3000)
	if n != 1 {
		t.Fatalf("Prune removed %d, want 1", n)
	}
	if mx, _ := st.MaxTs(); mx != 5000 {
		t.Fatalf("MaxTs after prune = %d, want 5000", mx)
	}
}

func TestMergeTail(t *testing.T) {
	a := []StoredLine{{TsMs: 1, Text: "a1"}, {TsMs: 3, Text: "a3"}}
	b := []StoredLine{{TsMs: 2, Text: "b2"}, {TsMs: 4, Text: "b4"}}
	got := MergeTail([][]StoredLine{a, b}, 3)
	if len(got) != 3 || got[0].Text != "b2" || got[1].Text != "a3" || got[2].Text != "b4" {
		t.Fatalf("MergeTail = %+v, want b2,a3,b4", got)
	}
}

func TestSinceBackfillAndFollow(t *testing.T) {
	st := open(t)
	_ = st.Append([]Line{
		{TsMs: 1, Label: "a#0", Text: "l1"},
		{TsMs: 1, Label: "a#1", Text: "l2"}, // same ts, different instance
		{TsMs: 2, Label: "a#0", Stderr: true, Text: "l3"},
	})
	// backfill: newest 2 across both labels, ascending by rowid
	got, cur, err := st.Since([]string{"a#0", "a#1"}, 0, 2, StreamAny, "")
	if err != nil {
		t.Fatalf("since backfill: %v", err)
	}
	if len(got) != 2 || got[0].Text != "l2" || got[1].Text != "l3" {
		t.Fatalf("backfill = %+v, want l2 then l3", got)
	}
	if cur != got[1].RowID || got[1].RowID == 0 {
		t.Fatalf("cursor = %d, want max rowid %d", cur, got[1].RowID)
	}
	// follow after cursor: nothing new, cursor unchanged
	got2, cur2, _ := st.Since([]string{"a#0", "a#1"}, cur, 100, StreamAny, "")
	if len(got2) != 0 || cur2 != cur {
		t.Fatalf("follow empty = %+v cur=%d, want none and cur=%d", got2, cur2, cur)
	}
	// append then follow returns only the new line, advancing the cursor
	_ = st.Append([]Line{{TsMs: 3, Label: "a#0", Text: "l4"}})
	got3, cur3, _ := st.Since([]string{"a#0", "a#1"}, cur, 100, StreamAny, "")
	if len(got3) != 1 || got3[0].Text != "l4" || cur3 <= cur {
		t.Fatalf("follow new = %+v cur=%d", got3, cur3)
	}
}

func TestSinceStreamFilter(t *testing.T) {
	st := open(t)
	_ = st.Append([]Line{
		{TsMs: 1, Label: "a#0", Text: "out"},
		{TsMs: 2, Label: "a#0", Stderr: true, Text: "err"},
	})
	got, _, _ := st.Since([]string{"a#0"}, 0, 100, StreamStderr, "")
	if len(got) != 1 || got[0].Text != "err" {
		t.Fatalf("stderr filter = %+v, want [err]", got)
	}
}

func TestEscapeLikeLiteral(t *testing.T) {
	if got := escapeLike(`a%b_c\d`); got != `a\%b\_c\\d` {
		t.Fatalf("escapeLike = %q; want %q", got, `a\%b\_c\\d`)
	}
}

func TestTailTextFilter(t *testing.T) {
	st := open(t)
	if err := st.Append([]Line{
		{TsMs: 1, Label: "x#0", Stderr: false, Text: "hello world"},
		{TsMs: 2, Label: "x#0", Stderr: false, Text: "goodbye"},
		{TsMs: 3, Label: "x#0", Stderr: false, Text: "HELLO again"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Tail("x#0", 10, StreamAny, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("text filter got %d; want 2 (case-insensitive substring)", len(got))
	}
	all, err := st.Tail("x#0", 10, StreamAny, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("empty filter got %d; want 3 (unchanged)", len(all))
	}
}

func TestSinceTextFilter(t *testing.T) {
	st := open(t)
	if err := st.Append([]Line{
		{TsMs: 1, Label: "a#0", Stderr: false, Text: "error: boom"},
		{TsMs: 2, Label: "a#0", Stderr: false, Text: "ok"},
		{TsMs: 3, Label: "a#0", Stderr: false, Text: "another ERROR"},
	}); err != nil {
		t.Fatal(err)
	}
	got, _, err := st.Since([]string{"a#0"}, 0, 100, StreamAny, "error")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("Since text filter got %d; want 2", len(got))
	}
	for _, ln := range got {
		if !strings.Contains(strings.ToLower(ln.Text), "error") {
			t.Errorf("non-matching line returned: %q", ln.Text)
		}
	}
}

func TestTextFilterWildcardLiteral(t *testing.T) {
	st := open(t)
	if err := st.Append([]Line{
		{TsMs: 1, Label: "w#0", Stderr: false, Text: "100% done"},
		{TsMs: 2, Label: "w#0", Stderr: false, Text: "1000 done"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Tail("w#0", 10, StreamAny, "100%")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "100% done" {
		t.Fatalf("literal %% match = %+v; want [100%% done]", got)
	}
}

func TestSinceCursorSafeAfterPrune(t *testing.T) {
	st := open(t)
	_ = st.Append([]Line{
		{TsMs: 1000, Label: "a#0", Text: "old"},
		{TsMs: 5000, Label: "a#0", Text: "new"},
	})
	_, cur, _ := st.Since([]string{"a#0"}, 0, 100, StreamAny, "")
	if _, err := st.Prune(3000); err != nil { // removes "old" (smallest rowid)
		t.Fatalf("prune: %v", err)
	}
	_ = st.Append([]Line{{TsMs: 6000, Label: "a#0", Text: "newer"}})
	got, _, _ := st.Since([]string{"a#0"}, cur, 100, StreamAny, "")
	if len(got) != 1 || got[0].Text != "newer" {
		t.Fatalf("after prune follow = %+v, want [newer]", got)
	}
}
