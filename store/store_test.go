package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/brew"
)

func f(v float64) *float64 { return &v }

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "brews.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	start := time.Date(2026, 11, 3, 8, 0, 0, 123456789, time.UTC)
	id, err := s.BeginBrew(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	same := start.Add(250 * time.Millisecond)
	pts := []brew.Sample{
		{At: start, Temp: f(93.5), Setpoint: f(94), Pressure: f(1.5), Flow: f(0.5), Heat: f(20)},
		{At: same, Temp: f(93.6), Pressure: f(6)}, // nil Setpoint, Flow, Heat
		{At: same, Temp: f(93.7), Pressure: f(7)}, // same millisecond
	}
	if err := s.AddPoints(ctx, id, start, pts); err != nil {
		t.Fatal(err)
	}
	sum := brew.Summary{
		StartedAt: start, EndedAt: start.Add(28 * time.Second), Kind: brew.Shot,
		Duration: 28 * time.Second, PeakBar: 9.1, VolumeML: 36,
	}
	if err := s.FinishBrew(ctx, id, sum); err != nil {
		t.Fatal(err)
	}

	brews, err := s.Brews(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(brews) != 1 {
		t.Fatalf("got %d brews", len(brews))
	}
	b := brews[0]
	if b.ID != id || !b.StartedAt.Equal(start) || b.EndedAt == nil || !b.EndedAt.Equal(sum.EndedAt) ||
		b.Kind != "shot" || b.DurationS != 28 || b.PeakBar != 9.1 || b.VolumeML != 36 {
		t.Errorf("brew = %+v", b)
	}

	got, err := s.Points(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d points", len(got))
	}
	if got[0].TMs != 0 || got[1].TMs != 250 || got[2].TMs != 250 {
		t.Errorf("t_ms = %d %d %d", got[0].TMs, got[1].TMs, got[2].TMs)
	}
	if got[0].Temp == nil || *got[0].Temp != 93.5 || got[0].Heat == nil || *got[0].Heat != 20 {
		t.Errorf("point 0 = %+v", got[0])
	}
	if got[1].Setpoint != nil || got[1].Flow != nil || got[1].Heat != nil {
		t.Errorf("nil fields not preserved: %+v", got[1])
	}
	if got[1].Pressure == nil || *got[1].Pressure != 6 || got[2].Pressure == nil || *got[2].Pressure != 7 {
		t.Errorf("order lost: %+v %+v", got[1], got[2])
	}
}

func TestReopenKeepsData(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)
	if _, err := s.BeginBrew(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	brews, err := s2.Brews(ctx, 10)
	if err != nil || len(brews) != 1 {
		t.Fatalf("brews = %v, err = %v", brews, err)
	}
}

func TestCloseDangling(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	start := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	open, _ := s.BeginBrew(ctx, start)
	done, _ := s.BeginBrew(ctx, start.Add(time.Hour))
	if err := s.FinishBrew(ctx, done, brew.Summary{
		StartedAt: start.Add(time.Hour), EndedAt: start.Add(time.Hour + 20*time.Second), Kind: brew.Flush,
	}); err != nil {
		t.Fatal(err)
	}
	n, err := s.CloseDangling(ctx)
	if err != nil || n != 1 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
	brews, _ := s.Brews(ctx, 10)
	for _, b := range brews {
		switch b.ID {
		case open:
			if b.Kind != "interrupted" || b.EndedAt == nil || !b.EndedAt.Equal(start) {
				t.Errorf("dangling = %+v", b)
			}
		case done:
			if b.Kind != "flush" {
				t.Errorf("finished brew touched: %+v", b)
			}
		}
	}
	if n, _ := s.CloseDangling(ctx); n != 0 {
		t.Errorf("second call n = %d", n)
	}
}

func TestUnknownSchemaVersion(t *testing.T) {
	s, path := openTemp(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE schema_version SET version = 99`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if s2, err := Open(path); err == nil {
		_ = s2.Close()
		t.Fatal("Open accepted schema version 99")
	}
}

func TestBrewsLimitNewestFirst(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	base := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	var ids []int64
	for i := range 3 {
		id, err := s.BeginBrew(ctx, base.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	got, err := s.Brews(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != ids[2] || got[1].ID != ids[1] {
		t.Errorf("got %+v", got)
	}
}

func TestPointsUnknownBrew(t *testing.T) {
	s, _ := openTemp(t)
	if _, err := s.Points(context.Background(), 42); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}

// TestTimestampsSortAsText: started_at and ended_at are fixed-width UTC
// text, so ORDER BY on them is chronological.
func TestTimestampsSortAsText(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	base := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	early, err := s.BeginBrew(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	late, err := s.BeginBrew(ctx, base.Add(500*time.Millisecond).In(time.FixedZone("CET", 3600)))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, started_at FROM brews ORDER BY started_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var (
		ids  []int64
		txts []string
	)
	for rows.Next() {
		var (
			id  int64
			txt string
		)
		if err := rows.Scan(&id, &txt); err != nil {
			t.Fatal(err)
		}
		ids, txts = append(ids, id), append(txts, txt)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != early || ids[1] != late {
		t.Errorf("ORDER BY started_at = %v (%q), want [%d %d]", ids, txts, early, late)
	}
	if len(txts) == 2 && len(txts[0]) != len(txts[1]) {
		t.Errorf("timestamps differ in width: %q", txts)
	}
	got, err := s.Brews(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].StartedAt.Equal(base.Add(500*time.Millisecond)) || !got[1].StartedAt.Equal(base) {
		t.Errorf("round trip = %+v", got)
	}
}
