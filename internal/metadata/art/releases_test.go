package art

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/activity"
	"github.com/theatarr/theatarr/internal/availability"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
)

func TestMovieDates(t *testing.T) {
	day := func(d string) time.Time {
		v, err := time.Parse(time.DateOnly, d)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, tc := range []struct {
		name  string
		movie tmdb.Movie
		want  map[string]string
	}{
		{"US wide before limited, no premieres or TV", tmdb.Movie{ReleaseDates: tmdb.ReleaseDates{Results: []tmdb.CountryReleases{
			{Country: "US", Releases: []tmdb.Release{{Type: 1, Date: day("2026-01-01")}, {Type: 2, Date: day("2026-02-01")}, {Type: 3, Date: day("2026-03-01")}, {Type: 3, Date: day("2026-03-02")}, {Type: 4, Date: day("2026-04-01")}, {Type: 5, Date: day("2026-05-01")}, {Type: 6, Date: day("2026-01-01")}}},
			{Country: "DE", Releases: []tmdb.Release{{Type: 3, Date: day("2025-01-01")}, {Type: 4, Date: day("2025-01-01")}, {Type: 5, Date: day("2025-01-01")}}},
		}}}, map[string]string{"theatrical": "2026-03-01", "digital": "2026-04-01", "physical": "2026-05-01"}},
		{"limited and worldwide fallback", tmdb.Movie{ReleaseDates: tmdb.ReleaseDates{Results: []tmdb.CountryReleases{
			{Country: "US", Releases: []tmdb.Release{{Type: 2, Date: day("2026-02-01")}}},
			{Country: "GB", Releases: []tmdb.Release{{Type: 4, Date: day("2026-04-02")}}},
			{Country: "DE", Releases: []tmdb.Release{{Type: 4, Date: day("2026-04-01")}}},
		}}}, map[string]string{"theatrical": "2026-02-01", "digital": "2026-04-01"}},
		{"primary release fallback", tmdb.Movie{ReleaseDate: "2026-03-01"}, map[string]string{"theatrical": "2026-03-01"}},
		{"unknown", tmdb.Movie{}, map[string]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := movieDates(&tc.movie)
			for k, v := range got {
				if v.IsZero() {
					delete(got, k)
				}
			}
			if len(got) != len(tc.want) {
				t.Fatalf("dates %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k].Format(time.DateOnly) != v {
					t.Errorf("%s: %v, want %s", k, got[k], v)
				}
			}
		})
	}
}

func TestRefreshMovieDates(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	e.seed(t)
	e.exec(t, `INSERT INTO title_release_date (title_id, source, type, date) VALUES (1,'radarr','theatrical','1998-01-01')`)
	if err := e.svc.FillTitle(ctx, 1); err != nil {
		t.Fatal(err)
	}
	dates, err := availability.Dates(ctx, e.st.Reader(), 1)
	if err != nil || dates["theatrical"].Format(time.DateOnly) != "1999-03-31" {
		t.Fatalf("dates: %v %v", dates, err)
	}
	var status string
	if err := e.st.Reader().QueryRow(`SELECT status FROM title_meta WHERE title_id = 1`).Scan(&status); err != nil || status != "released" {
		t.Fatalf("status: %s %v", status, err)
	}
	// TMDb's collection membership is kept for movie intake's collection discovery, once.
	if err := e.svc.FillTitle(ctx, 1); err != nil {
		t.Fatal(err)
	}
	var collection string
	if err := e.st.Reader().QueryRow(`SELECT group_concat(external_id) FROM title_external_id WHERE title_id = 1
		AND source = 'tmdb_collection'`).Scan(&collection); err != nil || collection != "2344" {
		t.Fatalf("collection membership %q %v", collection, err)
	}
	items, err := activity.Calendar(ctx, e.st.Reader(), "1998-01-01", "2000-01-01", true)
	if err != nil || len(items) != 1 || items[0].Date != "1999-03-31" {
		t.Fatalf("calendar: %+v %v", items, err)
	}
	// Arr enrichment replaces its tmdb source; canonical dates have their own source and survive it.
	e.exec(t, `DELETE FROM title_release_date WHERE source = 'tmdb'`)
	dates, err = availability.Dates(ctx, e.st.Reader(), 1)
	if err != nil || dates["theatrical"].Format(time.DateOnly) != "1999-03-31" {
		t.Fatalf("after enrichment: %v %v", dates, err)
	}
	// A failed refresh preserves dates. A successful correction replaces them, including removing old dates.
	e.fake.mu.Lock()
	e.fake.fail = true
	e.fake.mu.Unlock()
	if err := e.svc.FillTitle(ctx, 1); err == nil {
		t.Fatal("expected failure")
	}
	dates, err = availability.Dates(ctx, e.st.Reader(), 1)
	if err != nil || dates["theatrical"].Format(time.DateOnly) != "1999-03-31" {
		t.Fatalf("after failure: %v %v", dates, err)
	}
	if err := e.st.WriteTx(ctx, func(tx *sql.Tx) error { return writeMovieDates(ctx, tx, 1, &tmdb.Movie{}, testNow) }); err != nil {
		t.Fatal(err)
	}
	dates, err = availability.Dates(ctx, e.st.Reader(), 1)
	if err != nil || len(dates) != 0 {
		t.Fatalf("removed dates: %v %v", dates, err)
	}
	items, err = activity.Calendar(ctx, e.st.Reader(), "1998-01-01", "2000-01-01", true)
	if err != nil || len(items) != 0 {
		t.Fatalf("stale calendar: %+v %v", items, err)
	}
}
