// Package logstore persists per-instance captured log lines to a local SQLite
// database (pure-Go modernc.org/sqlite) and serves tail / since queries. It is
// the log analog of metricstore.
package logstore

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

// Line is one captured line to append.
type Line struct {
	TsMs   int64
	Label  string // "app#instance"
	Stderr bool
	Text   string
}

// StoredLine is one row read back from the store.
type StoredLine struct {
	RowID  int64
	TsMs   int64
	Label  string
	Stderr bool
	Text   string
}

// StreamFilter selects which streams a query returns.
type StreamFilter int

const (
	StreamAny    StreamFilter = iota // both stdout and stderr
	StreamStdout                     // stderr = 0 only
	StreamStderr                     // stderr = 1 only
)

// Store is a SQLite-backed log line store.
type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS log_line (
	ts     INTEGER NOT NULL,
	label  TEXT    NOT NULL,
	stderr INTEGER NOT NULL,
	text   TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_log_label_ts ON log_line(label, ts);`

// Open opens (creating if needed) the database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open logs db: %w", err)
	}
	// One process touches this DB from two goroutines (ingest + query handler);
	// serialize to sidestep SQLite locking entirely.
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

// Append writes all lines in a single transaction, in slice order.
func (s *Store) Append(lines []Line) error {
	if len(lines) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO log_line(ts, label, stderr, text) VALUES(?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, ln := range lines {
		if _, err := stmt.Exec(ln.TsMs, ln.Label, b2i(ln.Stderr), ln.Text); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// Tail returns the newest `limit` lines for one label (after stream filtering),
// ordered oldest-first. limit <= 0 means no limit.
func (s *Store) Tail(label string, limit int, filter StreamFilter, text string) ([]StoredLine, error) {
	q := `SELECT ts, label, stderr, text FROM log_line WHERE label = ?`
	args := []any{label}
	switch filter {
	case StreamStdout:
		q += ` AND stderr = 0`
	case StreamStderr:
		q += ` AND stderr = 1`
	}
	if text != "" {
		q += ` AND text LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLike(text)+"%")
	}
	q += ` ORDER BY ts DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var desc []StoredLine
	for rows.Next() {
		var ln StoredLine
		var se int64
		if err := rows.Scan(&ln.TsMs, &ln.Label, &se, &ln.Text); err != nil {
			return nil, err
		}
		ln.Stderr = se != 0
		desc = append(desc, ln)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Reverse to ascending.
	for i, j := 0, len(desc)-1; i < j; i, j = i+1, j-1 {
		desc[i], desc[j] = desc[j], desc[i]
	}
	return desc, nil
}

// Since returns lines for the given labels with rowid > afterRowID, ascending by
// rowid, plus the max rowid returned (the next cursor). When afterRowID <= 0 it
// returns the newest `limit` lines instead (backfill), still ascending by rowid.
// limit <= 0 means no limit. An empty result returns the unchanged afterRowID so
// the caller's cursor never goes backwards.
func (s *Store) Since(labels []string, afterRowID int64, limit int, filter StreamFilter, text string) ([]StoredLine, int64, error) {
	if len(labels) == 0 {
		return nil, afterRowID, nil
	}
	ph := make([]string, len(labels))
	args := make([]any, 0, len(labels)+3)
	for i, l := range labels {
		ph[i] = "?"
		args = append(args, l)
	}
	q := `SELECT rowid, ts, label, stderr, text FROM log_line WHERE label IN (` + strings.Join(ph, ",") + `)`
	switch filter {
	case StreamStdout:
		q += ` AND stderr = 0`
	case StreamStderr:
		q += ` AND stderr = 1`
	}
	if text != "" {
		q += ` AND text LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLike(text)+"%")
	}
	reverse := false
	if afterRowID > 0 {
		q += ` AND rowid > ? ORDER BY rowid`
		args = append(args, afterRowID)
		if limit > 0 {
			q += ` LIMIT ?`
			args = append(args, limit)
		}
	} else {
		// backfill: newest `limit` by rowid, reversed below to ascending.
		q += ` ORDER BY rowid DESC`
		if limit > 0 {
			q += ` LIMIT ?`
			args = append(args, limit)
		}
		reverse = true
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, afterRowID, err
	}
	defer rows.Close()
	var out []StoredLine
	for rows.Next() {
		var ln StoredLine
		var se int64
		if err := rows.Scan(&ln.RowID, &ln.TsMs, &ln.Label, &se, &ln.Text); err != nil {
			return nil, afterRowID, err
		}
		ln.Stderr = se != 0
		out = append(out, ln)
	}
	if err := rows.Err(); err != nil {
		return nil, afterRowID, err
	}
	if reverse {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	cursor := afterRowID
	for _, ln := range out {
		if ln.RowID > cursor {
			cursor = ln.RowID
		}
	}
	return out, cursor, nil
}

// MaxTs returns the largest stored ts, or 0 when the store is empty.
func (s *Store) MaxTs() (int64, error) {
	var mx sql.NullInt64
	if err := s.db.QueryRow(`SELECT max(ts) FROM log_line`).Scan(&mx); err != nil {
		return 0, err
	}
	if !mx.Valid {
		return 0, nil
	}
	return mx.Int64, nil
}

// Labels returns the distinct labels, ascending.
func (s *Store) Labels() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT label FROM log_line ORDER BY label`)
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

// ErrorCounts returns, per label, the number of stderr lines with ts >= sinceMs.
// Labels with no matching rows are omitted from the map.
func (s *Store) ErrorCounts(labels []string, sinceMs int64) (map[string]int64, error) {
	out := map[string]int64{}
	if len(labels) == 0 {
		return out, nil
	}
	ph := make([]string, len(labels))
	args := make([]any, 0, len(labels)+1)
	for i, l := range labels {
		ph[i] = "?"
		args = append(args, l)
	}
	args = append(args, sinceMs)
	q := `SELECT label, count(*) FROM log_line WHERE label IN (` + strings.Join(ph, ",") +
		`) AND stderr = 1 AND ts >= ? GROUP BY label`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var label string
		var n int64
		if err := rows.Scan(&label, &n); err != nil {
			return nil, err
		}
		out[label] = n
	}
	return out, rows.Err()
}

// StderrSince returns stderr lines (stderr = 1) for the given labels with
// ts >= sinceMs, ordered by (label, ts) ascending so each proc's lines are
// contiguous and time-ordered. An empty label set returns no rows.
func (s *Store) StderrSince(labels []string, sinceMs int64) ([]StoredLine, error) {
	if len(labels) == 0 {
		return nil, nil
	}
	ph := make([]string, len(labels))
	args := make([]any, 0, len(labels)+1)
	for i, l := range labels {
		ph[i] = "?"
		args = append(args, l)
	}
	args = append(args, sinceMs)
	q := `SELECT ts, label, stderr, text FROM log_line WHERE label IN (` + strings.Join(ph, ",") +
		`) AND stderr = 1 AND ts >= ? ORDER BY label, ts`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredLine
	for rows.Next() {
		var ln StoredLine
		var se int64
		if err := rows.Scan(&ln.TsMs, &ln.Label, &se, &ln.Text); err != nil {
			return nil, err
		}
		ln.Stderr = se != 0
		out = append(out, ln)
	}
	return out, rows.Err()
}

// Prune deletes lines with ts < beforeMs, returning the count removed.
func (s *Store) Prune(beforeMs int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM log_line WHERE ts < ?`, beforeMs)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// MergeTail merges per-label ascending series into one ascending stream and
// keeps the newest `limit` lines (limit <= 0 keeps all). Stable on equal ts.
func MergeTail(series [][]StoredLine, limit int) []StoredLine {
	var all []StoredLine
	for _, s := range series {
		all = append(all, s...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].TsMs < all[j].TsMs })
	if limit > 0 && len(all) > limit {
		all = all[len(all)-limit:]
	}
	return all
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// escapeLike backslash-escapes the LIKE metacharacters in s so it matches as a
// literal substring under `LIKE ? ESCAPE '\'`.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
