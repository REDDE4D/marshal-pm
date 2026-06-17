// Package metricstore persists per-instance CPU%/RSS samples to a local SQLite
// database (pure-Go modernc.org/sqlite) and serves time-bucketed history queries.
package metricstore

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Sample is one instance reading at the Append timestamp.
type Sample struct {
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
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open metrics db: %w", err)
	}
	// Single daemon process touches this DB from two goroutines (sampler append +
	// query handler); serialize to sidestep SQLite locking entirely. Volume is tiny.
	db.SetMaxOpenConns(1)
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
		b.MemAvg = uint64(memAvg)
		b.MemMax = uint64(memMax)
		out = append(out, b)
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
