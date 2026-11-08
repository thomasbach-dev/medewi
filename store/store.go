// Package store persists brews and their sampled curves in SQLite
// (modernc.org/sqlite, pure Go).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/thomasbach-dev/medewi/brew"
)

// ErrNotFound is returned for an unknown brew id.
var ErrNotFound = errors.New("store: not found")

const schemaVersion = 1

const schemaV1 = `
CREATE TABLE IF NOT EXISTS brews (
	id INTEGER PRIMARY KEY,
	started_at TEXT NOT NULL,
	ended_at TEXT,
	kind TEXT,
	duration_s REAL,
	peak_bar REAL,
	volume_ml REAL,
	notes TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS brew_points (
	brew_id INTEGER NOT NULL REFERENCES brews(id),
	seq INTEGER NOT NULL,
	t_ms INTEGER NOT NULL,
	temp REAL,
	setpoint REAL,
	pressure REAL,
	flow REAL,
	heat REAL,
	PRIMARY KEY(brew_id, seq)
);
CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL);
`

// Store is the brew database.
type Store struct {
	db *sql.DB
}

// Brew is one row of the brews table.
type Brew struct {
	ID        int64
	StartedAt time.Time
	EndedAt   *time.Time // nil while the brew is open
	Kind      string
	DurationS float64
	PeakBar   float64
	VolumeML  float64
	Notes     string
}

// Point is one sampled curve point; nil means the field was masked.
type Point struct {
	TMs                                  int64 // milliseconds since the brew started
	Temp, Setpoint, Pressure, Flow, Heat *float64
}

// Open opens (and if empty, initialises) the database at path.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(schemaV1); err != nil {
		return err
	}
	var version sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil {
		return err
	}
	switch {
	case !version.Valid:
		if _, err := tx.Exec(`INSERT INTO schema_version (version) VALUES (?)`, schemaVersion); err != nil {
			return err
		}
	case version.Int64 != schemaVersion:
		return fmt.Errorf("store: unsupported schema version %d (want %d)", version.Int64, schemaVersion)
	}
	return tx.Commit()
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// tsLayout is RFC 3339 with all nine fraction digits kept: fixed-width UTC
// text sorts chronologically. time.RFC3339Nano parses it.
const tsLayout = "2006-01-02T15:04:05.000000000Z07:00"

func ts(t time.Time) string { return t.UTC().Format(tsLayout) }

// BeginBrew inserts an open brew and returns its id.
func (s *Store) BeginBrew(ctx context.Context, startedAt time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO brews (started_at) VALUES (?)`, ts(startedAt))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// AddPoints appends pts to a brew in one transaction. t_ms is relative to
// startedAt; seq continues from the brew's existing points.
func (s *Store) AddPoints(ctx context.Context, brewID int64, startedAt time.Time, pts []brew.Sample) error {
	if len(pts) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var seq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), -1) + 1 FROM brew_points WHERE brew_id = ?`, brewID).Scan(&seq); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO brew_points
		(brew_id, seq, t_ms, temp, setpoint, pressure, flow, heat) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, p := range pts {
		if _, err := stmt.ExecContext(ctx, brewID, seq, p.At.Sub(startedAt).Milliseconds(),
			p.Temp, p.Setpoint, p.Pressure, p.Flow, p.Heat); err != nil {
			return err
		}
		seq++
	}
	return tx.Commit()
}

// FinishBrew records the summary of a closed brew.
func (s *Store) FinishBrew(ctx context.Context, id int64, sum brew.Summary) error {
	res, err := s.db.ExecContext(ctx, `UPDATE brews
		SET ended_at = ?, kind = ?, duration_s = ?, peak_bar = ?, volume_ml = ? WHERE id = ?`,
		ts(sum.EndedAt), string(sum.Kind), sum.Duration.Seconds(), sum.PeakBar, sum.VolumeML, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

// CloseDangling marks brews left open by a crash as truncated-crash and returns
// how many it changed.
func (s *Store) CloseDangling(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE brews SET kind = ?, ended_at = started_at WHERE ended_at IS NULL`, string(brew.TruncatedCrash))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Brews returns up to limit brews, newest first.
func (s *Store) Brews(ctx context.Context, limit int) ([]Brew, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, started_at, ended_at, kind,
		duration_s, peak_bar, volume_ml, notes FROM brews ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Brew
	for rows.Next() {
		var (
			b                 Brew
			started           string
			ended, kind       sql.NullString
			dur, peak, volume sql.NullFloat64
		)
		if err := rows.Scan(&b.ID, &started, &ended, &kind, &dur, &peak, &volume, &b.Notes); err != nil {
			return nil, err
		}
		if b.StartedAt, err = time.Parse(time.RFC3339Nano, started); err != nil {
			return nil, err
		}
		if ended.Valid {
			t, err := time.Parse(time.RFC3339Nano, ended.String)
			if err != nil {
				return nil, err
			}
			b.EndedAt = &t
		}
		b.Kind, b.DurationS, b.PeakBar, b.VolumeML = kind.String, dur.Float64, peak.Float64, volume.Float64
		out = append(out, b)
	}
	return out, rows.Err()
}

// Points returns a brew's curve in recorded order, or ErrNotFound.
func (s *Store) Points(ctx context.Context, brewID int64) ([]Point, error) {
	var one int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM brews WHERE id = ?`, brewID).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT t_ms, temp, setpoint, pressure, flow, heat
		FROM brew_points WHERE brew_id = ? ORDER BY seq`, brewID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Point
	for rows.Next() {
		var p Point
		var temp, sp, pr, fl, heat sql.NullFloat64
		if err := rows.Scan(&p.TMs, &temp, &sp, &pr, &fl, &heat); err != nil {
			return nil, err
		}
		p.Temp, p.Setpoint, p.Pressure, p.Flow, p.Heat = nf(temp), nf(sp), nf(pr), nf(fl), nf(heat)
		out = append(out, p)
	}
	return out, rows.Err()
}

func nf(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	return &n.Float64
}
