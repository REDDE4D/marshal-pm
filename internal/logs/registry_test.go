package logs

import "testing"

func TestRegistryReusesSinkPerLabel(t *testing.T) {
	r := NewRegistry(t.TempDir())
	a := r.For("app#0")
	b := r.For("app#0")
	if a != b {
		t.Fatal("For should return the same sink for the same label")
	}
	if r.For("app#1") == a {
		t.Fatal("different labels must get different sinks")
	}
}

func TestRegistryWriterPair(t *testing.T) {
	r := NewRegistry(t.TempDir())
	out, errw := r.WriterPair("app#0")
	out.Write([]byte("o\n"))
	errw.Write([]byte("e\n"))
	lines := r.For("app#0").Backfill(10)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
}

func TestRegistryRemoveDropsSink(t *testing.T) {
	r := NewRegistry(t.TempDir())
	first := r.For("app#0")
	r.Remove("app#0")
	if r.For("app#0") == first {
		t.Fatal("Remove should drop the sink; For must build a fresh one")
	}
}

func TestRegistryResolveLabeledSkipsUnknown(t *testing.T) {
	r := NewRegistry(t.TempDir())
	r.For("app#0")
	got := r.ResolveLabeled([]string{"app#0", "ghost#0"})
	if len(got) != 1 || got[0].Label != "app#0" {
		t.Fatalf("ResolveLabeled = %+v, want only app#0", got)
	}
}
