package store_test

import (
	"context"
	"testing"

	"github.com/theatarr/theatarr/internal/store"
)

// 00133: the task table is made, the job rows of the recurring runs go (jobs with a subject stay), and a queued
// nightly backup's due time becomes the nightly task's next run.
func TestMigrationTask(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.OpenAt(t, 131)
	db := st.Writer()
	if _, err := db.ExecContext(ctx, `INSERT INTO job (kind, payload, state, dedupe_key, run_at, created_at, updated_at) VALUES
		('acquire.wanted', '{}', 'done', NULL, 0, 0, 0),
		('shadow.rss', '{}', 'queued', 'shadow.rss', 0, 0, 0),
		('db.nightly', '{}', 'failed', NULL, 0, 0, 0),
		('db.nightly', '{}', 'queued', 'db.nightly:2026-11-15', 7000, 0, 0),
		('provider.sync', '{}', 'done', NULL, 0, 0, 0),
		('provider.sync', '{"providers":["rd"],"retry":true}', 'done', 'keep1', 0, 0, 0),
		('meta.art', '{"title_id":1}', 'done', 'keep2', 0, 0, 0),
		('torrents.backup', '{"reason":"scheduled"}', 'done', NULL, 0, 0, 0),
		('torrents.backup', '{"reason":"manual"}', 'done', 'keep3', 0, 0, 0),
		('acquire.search', '{}', 'done', 'keep4', 0, 0, 0)`); err != nil {
		t.Fatal(err)
	}
	if err := st.UpTo(ctx, 133); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRowContext(ctx, `SELECT group_concat(dedupe_key, ' ') FROM (SELECT dedupe_key FROM job ORDER BY dedupe_key)`).Scan(&got); err != nil || got != "keep1 keep2 keep3 keep4" {
		t.Fatalf("jobs left: %q %v", got, err)
	}
	var next int64
	if err := db.QueryRowContext(ctx, `SELECT next_run FROM task WHERE key = 'db.nightly'`).Scan(&next); err != nil || next != 7000 {
		t.Fatalf("nightly next run: %d %v", next, err)
	}
}
