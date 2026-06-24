// Package eventstore persists per-instance restart events to a local SQLite
// database (pure-Go modernc.org/sqlite) and serves trailing-window rollups.
package eventstore

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Rollup is one label's restart summary.
type Rollup struct {
	Count24h int32 // events with ts >= the rollup's sinceMs
	LastMs   int64 // most recent event ts in millis (0 if none)
}

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS restarts (
	ts    INTEGER NOT NULL,
	label TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_restarts_label_ts ON restarts(label, ts);`

// Open opens (creating if needed) the database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open restarts db: %w", err)
	}
	// One daemon process touches this DB from a few goroutines (restart hook +
	// snapshot reader + prune); serialize to sidestep SQLite locking. Volume tiny.
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

// Record writes one restart event.
func (s *Store) Record(label string, tsMs int64) error {
	_, err := s.db.Exec(`INSERT INTO restarts(ts, label) VALUES(?, ?)`, tsMs, label)
	return err
}

// Rollups returns, per label, the count of events with ts >= sinceMs and the
// most recent event ts (set even when no event falls inside the window).
func (s *Store) Rollups(sinceMs int64) (map[string]Rollup, error) {
	rows, err := s.db.Query(`
		SELECT label,
		       SUM(CASE WHEN ts >= ? THEN 1 ELSE 0 END) AS count24h,
		       MAX(ts)                                  AS last_ms
		FROM restarts
		GROUP BY label`, sinceMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Rollup{}
	for rows.Next() {
		var label string
		var count int64
		var last int64
		if err := rows.Scan(&label, &count, &last); err != nil {
			return nil, err
		}
		out[label] = Rollup{Count24h: int32(count), LastMs: last}
	}
	return out, rows.Err()
}

// Prune deletes events with ts < beforeMs and returns the number removed.
func (s *Store) Prune(beforeMs int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM restarts WHERE ts < ?`, beforeMs)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }
