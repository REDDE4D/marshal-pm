package server

import (
	"testing"

	"marshal/internal/logstore"
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
