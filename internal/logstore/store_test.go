package logstore

import "testing"

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
	got, _ := st.Tail("api#0", 10, StreamAny)
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
	got, _ := st.Tail("x#0", 2, StreamAny)
	if len(got) != 2 || got[0].Text != "err1" || got[1].Text != "out2" {
		t.Fatalf("Tail limit=2 = %+v, want err1 then out2", got)
	}
	// stderr filter
	gotErr, _ := st.Tail("x#0", 10, StreamStderr)
	if len(gotErr) != 1 || gotErr[0].Text != "err1" {
		t.Fatalf("Tail stderr = %+v, want [err1]", gotErr)
	}
	// stdout filter
	gotOut, _ := st.Tail("x#0", 10, StreamStdout)
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
