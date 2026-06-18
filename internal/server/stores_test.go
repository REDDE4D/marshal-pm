package server

import (
	"testing"
	"time"

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

func TestStoresHistory(t *testing.T) {
	ss := newStores(t.TempDir())
	defer ss.closeAll()
	st, _ := ss.get("web-1")
	now := time.Now().UnixMilli()
	_ = st.Append(now-2000, []metricstore.Sample{{Label: "api#0", Cpu: 10, Mem: 100}, {Label: "api#1", Cpu: 5, Mem: 50}})
	_ = st.Append(now-1000, []metricstore.Sample{{Label: "api#0", Cpu: 30, Mem: 300}})

	// Missing agent → (nil, nil).
	bs, err := ss.History("ghost", "api", (time.Hour).Milliseconds(), 1000)
	if err != nil || bs != nil {
		t.Fatalf("missing agent = (%v, %v); want (nil, nil)", bs, err)
	}

	// Selector "api" matches api#0 and api#1, merged across instances.
	bs, err = ss.History("web-1", "api", (time.Hour).Milliseconds(), 1000)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(bs) == 0 {
		t.Fatal("expected merged buckets for api")
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
