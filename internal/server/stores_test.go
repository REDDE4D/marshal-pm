package server

import (
	"testing"
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
