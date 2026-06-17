package logs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stepClock returns a now() that advances 1ns per call, for deterministic ordering.
func stepClock() func() time.Time {
	var n int64
	return func() time.Time { n++; return time.Unix(0, n) }
}

func TestSinkTeesToFileAndRing(t *testing.T) {
	dir := t.TempDir()
	s := newSink(dir, "app#0", stepClock())
	defer s.Close()

	if _, err := s.Writer(false).Write([]byte("hello\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "app#0.out.log"))
	if err != nil {
		t.Fatalf("read out file: %v", err)
	}
	if !strings.Contains(string(data), "hello") {
		t.Fatalf("file = %q, want hello", data)
	}

	lines := s.Backfill(10)
	if len(lines) != 1 || lines[0].Text != "hello" || lines[0].Stderr {
		t.Fatalf("backfill = %+v, want one stdout line 'hello'", lines)
	}
}

func TestSinkAssemblesPartialLines(t *testing.T) {
	s := newSink(t.TempDir(), "app#0", stepClock())
	defer s.Close()
	w := s.Writer(false)
	w.Write([]byte("ab"))
	w.Write([]byte("cd\n"))
	lines := s.Backfill(10)
	if len(lines) != 1 || lines[0].Text != "abcd" {
		t.Fatalf("backfill = %+v, want one line 'abcd'", lines)
	}
}

func TestSinkBackfillOrderedAcrossStreams(t *testing.T) {
	s := newSink(t.TempDir(), "app#0", stepClock())
	defer s.Close()
	s.Writer(false).Write([]byte("1\n"))
	s.Writer(true).Write([]byte("2\n"))
	s.Writer(false).Write([]byte("3\n"))
	lines := s.Backfill(10)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	if lines[0].Text != "1" || lines[1].Text != "2" || lines[2].Text != "3" {
		t.Fatalf("order = %+v", lines)
	}
	if !lines[1].Stderr {
		t.Fatalf("line 2 should be stderr")
	}
}

func TestSinkRingCaps(t *testing.T) {
	s := newSink(t.TempDir(), "app#0", stepClock())
	defer s.Close()
	w := s.Writer(false)
	for i := 0; i < ringCap+5; i++ {
		w.Write([]byte("x\n"))
	}
	if got := len(s.Backfill(ringCap + 100)); got != ringCap {
		t.Fatalf("ring held %d, want %d", got, ringCap)
	}
}

func TestSinkSubscribeReceivesLive(t *testing.T) {
	s := newSink(t.TempDir(), "app#0", stepClock())
	defer s.Close()
	ch, cancel := s.Subscribe()
	defer cancel()
	s.Writer(false).Write([]byte("live\n"))
	select {
	case ln := <-ch:
		if ln.Text != "live" {
			t.Fatalf("got %q, want live", ln.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("no line received")
	}
}

func TestSinkSubscribeDropsWhenFull(t *testing.T) {
	s := newSink(t.TempDir(), "app#0", stepClock())
	defer s.Close()
	_, cancel := s.Subscribe() // never drained
	defer cancel()
	w := s.Writer(false)
	for i := 0; i < subBuffer+50; i++ {
		w.Write([]byte("x\n")) // must not block
	}
	// Ring still has everything despite the slow subscriber.
	if got := len(s.Backfill(subBuffer + 100)); got != subBuffer+50 {
		t.Fatalf("ring = %d, want %d", got, subBuffer+50)
	}
}

func TestSinkCloseClosesSubscribers(t *testing.T) {
	s := newSink(t.TempDir(), "app#0", stepClock())
	ch, _ := s.Subscribe()
	s.Close()
	if _, ok := <-ch; ok {
		t.Fatal("subscriber channel should be closed after Close")
	}
}

func TestSinkRotates(t *testing.T) {
	dir := t.TempDir()
	s := newSinkWithLimits(dir, "app#0", 1, 2, stepClock()) // 1MB cap
	defer s.Close()
	line := strings.Repeat("a", 1024) + "\n" // 1KB
	w := s.Writer(false)
	for i := 0; i < 1200; i++ { // ~1.2MB > 1MB → rotates once
		w.Write([]byte(line))
	}
	// lumberjack names backups "<prefix>-<timestamp><ext>": app#0.out-<ts>.log
	entries, _ := filepath.Glob(filepath.Join(dir, "app#0.out-*.log"))
	if len(entries) == 0 {
		t.Fatalf("expected at least one rotated backup file, found none")
	}
}

func TestPolicyReachesLoggers(t *testing.T) {
	p := Policy{MaxSizeMB: 7, MaxBackups: 3, MaxAgeDays: 9, Compress: true}
	s := newSinkP(t.TempDir(), "app#0", p, stepClock())
	defer s.Close()
	if s.outFile.MaxSize != 7 || s.outFile.MaxBackups != 3 || s.outFile.MaxAge != 9 || !s.outFile.Compress {
		t.Fatalf("out logger not configured from policy: %+v", s.outFile)
	}
	if s.errFile.MaxAge != 9 || !s.errFile.Compress {
		t.Fatalf("err logger not configured from policy: %+v", s.errFile)
	}
}

func TestDefaultPolicyAppliedByNewSink(t *testing.T) {
	s := newSink(t.TempDir(), "app#0", stepClock())
	defer s.Close()
	if s.outFile.MaxAge != DefaultPolicy.MaxAgeDays || !s.outFile.Compress {
		t.Fatalf("newSink did not apply DefaultPolicy: %+v", s.outFile)
	}
}
