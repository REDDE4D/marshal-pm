// Package audit records dashboard login attempts to an append-only, size-rotating
// JSONL file, and reads them back. It is a leaf package: it imports neither the
// dashboard nor the server package, so both may depend on it without a cycle.
package audit

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"
)

// DefaultMaxBytes is the rotation threshold the dashboard passes by default.
const DefaultMaxBytes int64 = 5 << 20 // 5 MiB

// Outcome values for Event.Outcome.
const (
	OutcomeSuccess     = "success"
	OutcomeInvalid     = "invalid_credentials"
	OutcomeRateLimited = "rate_limited"
)

// Event is one recorded login attempt. Passwords are never stored — only the
// submitted username, the source IP, and the outcome.
type Event struct {
	Time    time.Time `json:"time"`
	User    string    `json:"user"`
	IP      string    `json:"ip"`
	Outcome string    `json:"outcome"`
}

// Log is a concurrency-safe append-only writer that rotates at maxBytes.
type Log struct {
	path     string
	maxBytes int64
	mu       sync.Mutex
}

// New returns a writer appending to path. maxBytes <= 0 uses DefaultMaxBytes.
func New(path string, maxBytes int64) *Log {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	return &Log{path: path, maxBytes: maxBytes}
}

// Record appends ev as one JSON line. A nil *Log is a no-op. Before writing it
// rotates path to path+".1" (overwriting any prior .1) once the file reaches
// maxBytes, bounding disk to ~2x maxBytes. All I/O errors are logged and
// swallowed: auditing must never break the login path.
func (l *Log) Record(ev Event) {
	if l == nil {
		return
	}
	b, err := json.Marshal(ev)
	if err != nil {
		log.Printf("audit: marshal event: %v", err)
		return
	}
	b = append(b, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	if fi, err := os.Stat(l.path); err == nil && fi.Size() >= l.maxBytes {
		if err := os.Rename(l.path, l.path+".1"); err != nil {
			log.Printf("audit: rotate: %v", err)
		}
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("audit: open: %v", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(b); err != nil {
		log.Printf("audit: write: %v", err)
	}
}
