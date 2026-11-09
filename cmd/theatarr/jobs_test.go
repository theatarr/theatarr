package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func TestJobsCommand(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "theatarr.db")
	st, err := store.Open(ctx, storetest.Seed(t, db), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().Exec(`INSERT INTO job (id, kind, payload, state, run_at, last_error, created_at, updated_at,
		finished_at) VALUES (2, 'provider.sync', '{}', 'failed', 1, 'AUTH_BLOCKED', 1, 1, 1),
		(3, 'provider.sync', '{}', 'done', 2, NULL, 2, 2, 2),
		(4, 'db.nightly', '{}', 'failed', 3, 'disk full', 3, 3, 3)`); err != nil {
		t.Fatal(err)
	}
	st.Close()

	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runJobs(append([]string{args[0], "-db", db}, args[1:]...), &out)
		return out.String(), err
	}
	out, err := run("list", "-state", "failed")
	if err != nil || !strings.Contains(out, "resolved by #3") || !strings.Contains(out, "needs attention") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	out, err = run("list", "-attention", "-format", "json")
	if err != nil || strings.Count(out, "\n") != 1 || !strings.Contains(out, `"id":4`) {
		t.Fatalf("list -attention: %v\n%s", err, out)
	}
	if out, err = run("retry", "4"); err != nil || !strings.Contains(out, "retry queued as #5") {
		t.Fatalf("retry: %v %s", err, out)
	}
	if _, err = run("retry", "3"); err == nil {
		t.Error("retried a done job")
	}
	if out, err = run("dismiss", "-resolved"); err != nil || !strings.Contains(out, "dismissed 1") {
		t.Fatalf("dismiss -resolved: %v %s", err, out)
	}
	if out, err = run("dismiss", "#4"); err != nil || !strings.Contains(out, "job 4: dismissed by cli") {
		t.Fatalf("dismiss: %v %s", err, out)
	}
	if out, err = run("list", "-dismissed=false", "-state", "failed"); err != nil || strings.Contains(out, "failed ") {
		t.Errorf("list without dismissed: %v\n%s", err, out)
	}
	for _, bad := range [][]string{{}, {"nope"}, {"retry"}, {"dismiss"}, {"dismiss", "-resolved", "4"}, {"retry", "x"}} {
		if err := runJobs(bad, &bytes.Buffer{}); err == nil {
			t.Errorf("%v: no error", bad)
		}
	}
}
