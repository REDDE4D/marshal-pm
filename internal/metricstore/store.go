// Package metricstore persists per-instance CPU%/RSS samples to a local SQLite
// database (pure-Go modernc.org/sqlite) and serves time-bucketed history queries.
package metricstore

import (
	"database/sql"
	"fmt"
	"sort"

	_ "modernc.org/sqlite"
)

// Sample is one instance reading at the Append timestamp.
type Sample struct {
	Label string
	Cpu   float64
	Mem   uint64
}

// TimestampedSample is one stored row with its timestamp.
type TimestampedSample struct {
	TsMs  int64
	Label string
	Cpu   float64
	Mem   uint64
}

// Bucket is one aggregated time bucket for a label, oldest first in query order.
type Bucket struct {
	TsMs   int64
	CpuAvg float64
	CpuMax float64
	MemAvg uint64
	MemMax uint64
}

// QueryReq selects a single label's history.
type QueryReq struct {
	Label    string
	SinceMs  int64 // inclusive lower bound on ts
	BucketMs int64 // bucket width in ms; must be > 0
}

// Store is a SQLite-backed sample store.
type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS samples (
	ts    INTEGER NOT NULL,
	label TEXT    NOT NULL,
	cpu   REAL    NOT NULL,
	mem   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_samples_label_ts ON samples(label, ts);`

// Open opens (creating if needed) the database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open metrics db: %w", err)
	}
	// Single daemon process touches this DB from two goroutines (sampler append +
	// query handler); serialize to sidestep SQLite locking entirely. Volume is tiny.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Append writes one row per sample, all stamped tsMs, in a single transaction.
func (s *Store) Append(tsMs int64, samples []Sample) error {
	if len(samples) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO samples(ts, label, cpu, mem) VALUES(?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, sm := range samples {
		if _, err := stmt.Exec(tsMs, sm.Label, sm.Cpu, int64(sm.Mem)); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// Query returns time-bucketed aggregates for one label, oldest first.
func (s *Store) Query(req QueryReq) ([]Bucket, error) {
	if req.BucketMs <= 0 {
		return nil, fmt.Errorf("bucket width must be > 0")
	}
	rows, err := s.db.Query(`
		SELECT (ts/?)*? AS bucket, avg(cpu), max(cpu), avg(mem), max(mem)
		FROM samples
		WHERE label = ? AND ts >= ?
		GROUP BY bucket
		ORDER BY bucket`,
		req.BucketMs, req.BucketMs, req.Label, req.SinceMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bucket
	for rows.Next() {
		var b Bucket
		var memAvg float64
		var memMax int64
		if err := rows.Scan(&b.TsMs, &b.CpuAvg, &b.CpuMax, &memAvg, &memMax); err != nil {
			return nil, err
		}
		b.MemAvg = uint64(memAvg + 0.5) // round, not truncate
		b.MemMax = uint64(memMax)
		out = append(out, b)
	}
	return out, rows.Err()
}

// SamplesSince returns raw rows with ts strictly greater than tsMs, oldest first.
func (s *Store) SamplesSince(tsMs int64) ([]TimestampedSample, error) {
	rows, err := s.db.Query(`SELECT ts, label, cpu, mem FROM samples WHERE ts > ? ORDER BY ts`, tsMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TimestampedSample
	for rows.Next() {
		var ts int64
		var label string
		var cpu float64
		var mem int64
		if err := rows.Scan(&ts, &label, &cpu, &mem); err != nil {
			return nil, err
		}
		out = append(out, TimestampedSample{TsMs: ts, Label: label, Cpu: cpu, Mem: uint64(mem)})
	}
	return out, rows.Err()
}

// MaxTs returns the largest stored ts, or 0 when the store is empty.
func (s *Store) MaxTs() (int64, error) {
	var mx sql.NullInt64
	if err := s.db.QueryRow(`SELECT max(ts) FROM samples`).Scan(&mx); err != nil {
		return 0, err
	}
	if !mx.Valid {
		return 0, nil
	}
	return mx.Int64, nil
}

// Labels returns the distinct sample labels, ascending.
func (s *Store) Labels() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT label FROM samples ORDER BY label`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Prune deletes samples with ts < beforeMs, returning the count removed.
func (s *Store) Prune(beforeMs int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM samples WHERE ts < ?`, beforeMs)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

const (
	targetBuckets = 60
	minBucketMs   = 1000
)

// AutoBucketMs returns bucketMs when positive, else ~targetBuckets buckets over
// the window, floored at minBucketMs.
func AutoBucketMs(sinceMs, bucketMs int64) int64 {
	if bucketMs > 0 {
		return bucketMs
	}
	b := sinceMs / targetBuckets
	if b < minBucketMs {
		b = minBucketMs
	}
	return b
}

// MergeBuckets combines per-instance series sharing a bucket timestamp: averages
// are summed (whole-app total), maxes take the max across instances. Oldest first.
func MergeBuckets(series [][]Bucket) []Bucket {
	byTs := map[int64]*Bucket{}
	var order []int64
	for _, bs := range series {
		for _, b := range bs {
			cur, ok := byTs[b.TsMs]
			if !ok {
				nb := b
				byTs[b.TsMs] = &nb
				order = append(order, b.TsMs)
				continue
			}
			cur.CpuAvg += b.CpuAvg
			cur.MemAvg += b.MemAvg
			if b.CpuMax > cur.CpuMax {
				cur.CpuMax = b.CpuMax
			}
			if b.MemMax > cur.MemMax {
				cur.MemMax = b.MemMax
			}
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	out := make([]Bucket, 0, len(order))
	for _, ts := range order {
		out = append(out, *byTs[ts])
	}
	return out
}
