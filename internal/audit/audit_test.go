package audit

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordReadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "login-audit.log")
	l := New(p, DefaultMaxBytes)
	t0 := time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC)
	l.Record(Event{Time: t0, User: "admin", IP: "1.2.3.4", Outcome: OutcomeSuccess})
	l.Record(Event{Time: t0.Add(time.Minute), User: "bob", IP: "5.6.7.8", Outcome: OutcomeInvalid})
	got, err := Read(p, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events; want 2", len(got))
	}
	if got[0].User != "admin" || got[0].Outcome != OutcomeSuccess {
		t.Errorf("e0 = %+v", got[0])
	}
	if got[1].User != "bob" || got[1].Outcome != OutcomeInvalid {
		t.Errorf("e1 = %+v", got[1])
	}
	if !got[0].Time.Equal(t0) {
		t.Errorf("time round-trip: %v != %v", got[0].Time, t0)
	}
}

func TestRotationAtCap(t *testing.T) {
	p := filepath.Join(t.TempDir(), "login-audit.log")
	l := New(p, 200) // tiny cap forces rotation
	for i := 0; i < 20; i++ {
		l.Record(Event{Time: time.Unix(int64(i), 0).UTC(), User: "u", IP: "1.1.1.1", Outcome: OutcomeInvalid})
	}
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Fatalf(".1 not created (no rotation): %v", err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() >= 2*int64(200) {
		t.Errorf("current file not bounded after rotation: %d bytes", fi.Size())
	}
	got, err := Read(p, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("read returned nothing after rotation")
	}
	// The most recent event must survive (rotation keeps current + one .1).
	last := got[len(got)-1]
	if !last.Time.Equal(time.Unix(19, 0).UTC()) {
		t.Errorf("most recent event lost; last time = %v", last.Time)
	}
}

func TestReadSkipsCorruptLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "login-audit.log")
	good := `{"time":"2026-06-18T10:00:00Z","user":"a","ip":"1.1.1.1","outcome":"success"}`
	content := good + "\n{ this is not json\n\n" + good + "\n"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Read(p, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d; want 2 (corrupt + blank lines skipped)", len(got))
	}
}

func TestReadFiltersAndLimit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "login-audit.log")
	l := New(p, DefaultMaxBytes)
	base := time.Unix(0, 0).UTC()
	l.Record(Event{Time: base, User: "a", IP: "i", Outcome: OutcomeSuccess})
	l.Record(Event{Time: base.Add(time.Second), User: "b", IP: "i", Outcome: OutcomeInvalid})
	l.Record(Event{Time: base.Add(2 * time.Second), User: "c", IP: "i", Outcome: OutcomeRateLimited})

	fails, err := Read(p, ReadOptions{FailuresOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(fails) != 2 {
		t.Fatalf("FailuresOnly got %d; want 2", len(fails))
	}
	for _, e := range fails {
		if e.Outcome == OutcomeSuccess {
			t.Errorf("success leaked into failures: %+v", e)
		}
	}

	last1, err := Read(p, ReadOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(last1) != 1 || last1[0].User != "c" {
		t.Fatalf("Limit 1 = %+v; want only most recent (c)", last1)
	}
}

func TestReadMissingFile(t *testing.T) {
	got, err := Read(filepath.Join(t.TempDir(), "nope.log"), ReadOptions{})
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d; want 0", len(got))
	}
}
