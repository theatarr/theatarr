package store

import (
	"context"
	"testing"
	"time"
)

// 00097 marks a back-off running at the deploy as down when its stored reason is one an outage writes;
// rate limits and auth stay not down, and so does a back-off that already ended.
func TestBackoffDownBackfillMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openAt(t, 97)
	p, err := s.provider(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 96); err != nil {
		t.Fatal(err)
	}
	later, past := Millis(time.Now().Add(time.Hour)), Millis(time.Now().Add(-time.Hour))
	if _, err := s.Writer().ExecContext(ctx, `INSERT INTO router_pacing (provider, backoff_until, reason, updated_at)
		VALUES ('a', ?, 'error', 0), ('b', ?, 'unavailable', 0), ('c', ?, 'provider unavailable: downloads stalled', 0),
		('d', ?, 'rate_limited', 0), ('e', ?, 'auth', 0), ('f', ?, 'error', 0)`,
		later, later, later, later, later, past); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 97); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Reader().QueryContext(ctx, `SELECT provider FROM router_pacing WHERE backoff_down = 1 ORDER BY provider`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got string
	for rows.Next() {
		var pk string
		if err := rows.Scan(&pk); err != nil {
			t.Fatal(err)
		}
		got += pk
	}
	if got != "abc" {
		t.Errorf("down: %q, want abc", got)
	}
	if _, err := p.DownTo(ctx, 96); err != nil {
		t.Fatal(err)
	}
}
