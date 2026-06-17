// Package logs captures per-instance process output to rotated files and an
// in-memory ring buffer, and fans new lines out to live followers.
package logs

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	maxSizeMB    = 10        // rotate at 10MB
	maxBackups   = 5         // keep 5 rotated files
	ringCap      = 1000      // ~1000 lines/instance in memory
	subBuffer    = 256       // per-subscriber channel buffer
	maxLineBytes = 64 * 1024 // force-flush a newline-less line at this size
)

// Line is one captured output line with its origin.
type Line struct {
	Ts     time.Time
	Stderr bool
	Text   string
}

// Sink captures one instance's stdout and stderr.
type Sink struct {
	outFile, errFile *lumberjack.Logger
	now              func() time.Time

	mu      sync.Mutex
	ring    *ring
	outPart []byte // partial (newline-less) stdout tail
	errPart []byte // partial stderr tail
	subs    map[int]chan Line
	nextID  int
	closed  bool
}

// Policy controls log-file rotation, retention, and compression for one sink.
type Policy struct {
	MaxSizeMB  int  // rotate threshold in MB (lumberjack MaxSize)
	MaxBackups int  // rotated files kept (lumberjack MaxBackups)
	MaxAgeDays int  // delete rotated files older than this many days (0 = no age limit)
	Compress   bool // gzip rotated files
}

// DefaultPolicy is the daemon-wide default when an app declares no override.
var DefaultPolicy = Policy{MaxSizeMB: maxSizeMB, MaxBackups: maxBackups, MaxAgeDays: 14, Compress: true}

func newSink(dir, label string, now func() time.Time) *Sink {
	return newSinkP(dir, label, DefaultPolicy, now)
}

// newSinkWithLimits is retained for existing tests; size/backups only, no age/compress.
func newSinkWithLimits(dir, label string, sizeMB, backups int, now func() time.Time) *Sink {
	return newSinkP(dir, label, Policy{MaxSizeMB: sizeMB, MaxBackups: backups}, now)
}

func newSinkP(dir, label string, p Policy, now func() time.Time) *Sink {
	mk := func(suffix string) *lumberjack.Logger {
		return &lumberjack.Logger{
			Filename:   filepath.Join(dir, label+suffix),
			MaxSize:    p.MaxSizeMB,
			MaxBackups: p.MaxBackups,
			MaxAge:     p.MaxAgeDays,
			Compress:   p.Compress,
		}
	}
	return &Sink{
		outFile: mk(".out.log"),
		errFile: mk(".err.log"),
		now:     now,
		ring:    newRing(ringCap),
		subs:    map[int]chan Line{},
	}
}

// Writer returns the io.Writer for one stream; the process's stdout/stderr is
// wired to it. Each write tees raw bytes to the rotated file and split lines to
// the ring buffer + subscribers.
func (s *Sink) Writer(stderr bool) io.Writer {
	return writerFunc(func(p []byte) (int, error) { return s.write(stderr, p) })
}

func (s *Sink) write(stderr bool, p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return len(p), nil
	}
	if stderr {
		_, _ = s.errFile.Write(p)
	} else {
		_, _ = s.outFile.Write(p)
	}
	part := &s.outPart
	if stderr {
		part = &s.errPart
	}
	*part = append(*part, p...)
	for {
		i := bytes.IndexByte(*part, '\n')
		if i < 0 {
			break
		}
		text := string((*part)[:i])
		*part = (*part)[i+1:]
		s.emit(Line{Ts: s.now(), Stderr: stderr, Text: text})
	}
	for len(*part) >= maxLineBytes {
		s.emit(Line{Ts: s.now(), Stderr: stderr, Text: string((*part)[:maxLineBytes])})
		*part = (*part)[maxLineBytes:]
	}
	return len(p), nil
}

// emit appends to the ring and fans out to subscribers. Caller holds s.mu.
func (s *Sink) emit(ln Line) {
	s.ring.add(ln)
	for _, ch := range s.subs {
		select {
		case ch <- ln:
		default: // drop: a slow follower must never stall the process
		}
	}
}

// FileBackfill returns up to n lines for one stream read from the rotated
// files on disk (deep history that outlives the in-memory ring).
func (s *Sink) FileBackfill(stderr bool, n int) ([]Line, error) {
	f := s.outFile.Filename
	if stderr {
		f = s.errFile.Filename
	}
	dir := filepath.Dir(f)
	// base file name is "<label>.out.log"; strip ".out.log"/".err.log" to recover the label.
	name := filepath.Base(f)
	label := strings.TrimSuffix(strings.TrimSuffix(name, ".err.log"), ".out.log")
	return fileBackfill(dir, label, stderr, n)
}

// RingSaturated reports whether the in-memory ring has wrapped — i.e. older
// history exists on disk beyond what the ring can serve.
func (s *Sink) RingSaturated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ring.full
}

// Backfill returns the last n captured lines (merged across streams, in order).
func (s *Sink) Backfill(n int) []Line {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ring.last(n)
}

// Subscribe registers a live follower; call the returned func to unsubscribe.
func (s *Sink) Subscribe() (<-chan Line, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan Line, subBuffer)
	if s.closed {
		close(ch)
		return ch, func() {}
	}
	id := s.nextID
	s.nextID++
	s.subs[id] = ch
	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if c, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(c)
		}
	}
}

// SubscribeWithRing atomically snapshots the last n ring lines and registers a
// live follower under one lock, so no line falls between backfill and live and
// none is delivered twice. Call cancel to unsubscribe.
func (s *Sink) SubscribeWithRing(n int) ([]Line, <-chan Line, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	backfill := s.ring.last(n)
	ch := make(chan Line, subBuffer)
	if s.closed {
		close(ch)
		return backfill, ch, func() {}
	}
	id := s.nextID
	s.nextID++
	s.subs[id] = ch
	return backfill, ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if c, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(c)
		}
	}
}

// Close flushes the files and closes all subscribers.
func (s *Sink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	for id, ch := range s.subs {
		delete(s.subs, id)
		close(ch)
	}
	e1 := s.outFile.Close()
	e2 := s.errFile.Close()
	if e1 != nil {
		return e1
	}
	return e2
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// ring is a fixed-capacity circular buffer of Lines.
type ring struct {
	buf  []Line
	size int
	head int
	full bool
}

func newRing(n int) *ring { return &ring{buf: make([]Line, n), size: n} }

func (r *ring) add(l Line) {
	r.buf[r.head] = l
	r.head = (r.head + 1) % r.size
	if r.head == 0 {
		r.full = true
	}
}

func (r *ring) snapshot() []Line {
	if !r.full {
		out := make([]Line, r.head)
		copy(out, r.buf[:r.head])
		return out
	}
	out := make([]Line, r.size)
	copy(out, r.buf[r.head:])
	copy(out[r.size-r.head:], r.buf[:r.head])
	return out
}

func (r *ring) last(n int) []Line {
	all := r.snapshot()
	if n > 0 && n < len(all) {
		return all[len(all)-n:]
	}
	return all
}
