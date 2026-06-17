// Package logs captures per-instance process output to rotated files and an
// in-memory ring buffer, and fans new lines out to live followers.
package logs

import (
	"bytes"
	"io"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	maxSizeMB  = 10   // rotate at 10MB
	maxBackups = 5    // keep 5 rotated files
	ringCap    = 1000 // ~1000 lines/instance in memory
	subBuffer  = 256  // per-subscriber channel buffer
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

func newSink(dir, label string, now func() time.Time) *Sink {
	return newSinkWithLimits(dir, label, maxSizeMB, maxBackups, now)
}

func newSinkWithLimits(dir, label string, sizeMB, backups int, now func() time.Time) *Sink {
	return &Sink{
		outFile: &lumberjack.Logger{Filename: filepath.Join(dir, label+".out.log"), MaxSize: sizeMB, MaxBackups: backups},
		errFile: &lumberjack.Logger{Filename: filepath.Join(dir, label+".err.log"), MaxSize: sizeMB, MaxBackups: backups},
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
