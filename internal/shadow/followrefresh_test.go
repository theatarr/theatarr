package shadow

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

// TestImportRefreshesFollowers: after an instance's import, the follower refresh gets the followed version and the
// titles whose arr files changed, and only those: the first import (new files), not an import with nothing new, and
// again after an arr upgrade. Instances whose version has no follower never call it, and a refresh that fails does
// not fail the import.
func TestImportRefreshesFollowers(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	k := *e.pol.Versions["movie-remux"]
	k.Follows, k.Mode = "movie-remux", policy.ModeActive
	e.pol.Versions["movie-remux-theatarr"] = &k
	e.build(nil)
	var calls []string
	var fail error
	e.r.SetFollowerRefresh(func(_ context.Context, followed string, titles []int64) error {
		calls = append(calls, fmt.Sprint(followed, titles))
		return fail
	})
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	dune := one[int64](t, e.st, `SELECT title_id FROM version_file WHERE instance = 'radarr_zurg'`)
	want := fmt.Sprint("movie-remux", []int64{dune})
	if len(calls) != 1 || calls[0] != want {
		t.Fatalf("first import: %v, want [%s]", calls, want)
	}
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("nothing changed, yet: %v", calls)
	}

	// The arr upgrades Dune: a new file id at a new path.
	e.zurg.mu.Lock()
	f := map[string]any{}
	for k, v := range e.zurg.files[1] {
		f[k] = v
	}
	f["id"], f["path"], f["relativePath"] = 12, "/movies/Dune/y.mkv", "y.mkv"
	e.zurg.files[1] = f
	e.zurg.movies[0]["movieFileId"] = 12
	e.zurg.mu.Unlock()
	fail = errors.New("mount down")
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatalf("a failed refresh failed the import: %v", err)
	}
	if len(calls) != 2 || calls[1] != want {
		t.Fatalf("after the upgrade: %v", calls)
	}
}

// TestImportFollowerCatchUp: the per-import refresh fires only on the import that sees the
// arr's file change, so a refresh that fails there (a mount outage) used to lose the upgrade. The catch-up refreshes
// every title: at the first import, then once per interval, and at the first import after a failed refresh, until
// one succeeds.
func TestImportFollowerCatchUp(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	k := *e.pol.Versions["movie-remux"]
	k.Follows, k.Mode = "movie-remux", policy.ModeActive
	e.pol.Versions["movie-remux-theatarr"] = &k
	e.build(nil)
	var titled, full []string
	var failTitled, failFull error
	e.r.SetFollowerRefresh(func(_ context.Context, followed string, titles []int64) error {
		titled = append(titled, fmt.Sprint(followed, titles))
		return failTitled
	})
	e.r.SetFollowerCatchUp(func(_ context.Context, followed string) error {
		full = append(full, followed)
		return failFull
	}, time.Hour)
	imp := func() {
		t.Helper()
		if _, err := e.r.Import(e.ctx); err != nil {
			t.Fatal(err)
		}
	}
	imp()
	if len(full) != 1 || full[0] != "movie-remux" || len(titled) != 0 {
		t.Fatalf("first import: full %v, per title %v (the catch-up covers the new files)", full, titled)
	}
	imp()
	if len(full) != 1 || len(titled) != 0 {
		t.Fatalf("nothing changed and the hour has not passed: full %v, per title %v", full, titled)
	}

	// The arr upgrades Dune while the mount is down: the per-import refresh fails.
	e.zurg.mu.Lock()
	f := map[string]any{}
	for k, v := range e.zurg.files[1] {
		f[k] = v
	}
	f["id"], f["path"], f["relativePath"] = 12, "/movies/Dune/y.mkv", "y.mkv"
	e.zurg.files[1] = f
	e.zurg.movies[0]["movieFileId"] = 12
	e.zurg.mu.Unlock()
	failTitled = errors.New("mount down")
	imp()
	if len(titled) != 1 || len(full) != 1 {
		t.Fatalf("after the upgrade: full %v, per title %v", full, titled)
	}
	// The next import sees no change, but the failure made the catch-up due; still down, so it stays due.
	failFull = errors.New("mount down")
	imp()
	if len(full) != 2 {
		t.Fatalf("no catch-up after the failed refresh: full %v", full)
	}
	failFull = nil
	imp()
	if len(full) != 3 {
		t.Fatalf("no catch-up after the failed catch-up: full %v", full)
	}
	imp()
	if len(full) != 3 || len(titled) != 1 {
		t.Fatalf("the catch-up succeeded: nothing is due: full %v, per title %v", full, titled)
	}
	e.advance(time.Hour)
	imp()
	if len(full) != 4 {
		t.Fatalf("an hour later: full %v", full)
	}
}
