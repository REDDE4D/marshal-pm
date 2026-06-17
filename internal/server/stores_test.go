package server

import (
	"testing"

	"marshal/internal/metricstore"
)

func TestStoresLazyOpenAndHas(t *testing.T) {
	dir := t.TempDir()
	ss := newStores(dir)
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

func TestStoresPruneAll(t *testing.T) {
	ss := newStores(t.TempDir())
	defer ss.closeAll()
	st, _ := ss.get("web-1")
	_ = st.Append(1000, []metricstore.Sample{{Label: "a#0", Cpu: 1, Mem: 1}})
	_ = st.Append(5000, []metricstore.Sample{{Label: "a#0", Cpu: 2, Mem: 2}})
	ss.pruneAll(3000) // drop ts < 3000
	if mx, _ := st.MaxTs(); mx != 5000 {
		t.Fatalf("MaxTs after prune = %d, want 5000", mx)
	}
	rows, _ := st.SamplesSince(0)
	if len(rows) != 1 {
		t.Fatalf("rows after prune = %d, want 1", len(rows))
	}
}

func TestSanitizeAgent(t *testing.T) {
	cases := map[string]string{
		"web-1":  "web-1",
		"a/b":    "a_b",
		"../etc": "___etc",
		"":       "_",
		"x\\y":   "x_y",
	}
	for in, want := range cases {
		if got := sanitizeAgent(in); got != want {
			t.Errorf("sanitizeAgent(%q) = %q, want %q", in, got, want)
		}
	}
}
