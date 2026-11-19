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

// TestPrimaryReleaseDates (K12): an old US film whose only digital date is a German re-release. Acquisition's dates
// (availability.Dates over the tmdb-art schedule) keep that digital date as before; only the US rows are stored, and the
// calendar does not list the film on the re-release's day. Without a US date the earliest country is the primary one.
func TestPrimaryReleaseDates(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	e.seed(t)
	day := func(d string) time.Time {
		v, err := time.Parse(time.DateOnly, d)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	m := &tmdb.Movie{ReleaseDates: tmdb.ReleaseDates{Results: []tmdb.CountryReleases{
		{Country: "DE", Releases: []tmdb.Release{{Type: 3, Date: day("1985-08-01")}, {Type: 4, Date: day("2026-12-01")}}},
		{Country: "US", Releases: []tmdb.Release{{Type: 3, Date: day("1985-07-03")}, {Type: 1, Date: day("1985-06-01")}}},
	}}}
	if got := m.PrimaryCountry(); got != "US" {
		t.Errorf("primary country %q", got)
	}
	e.exec(t, `INSERT OR IGNORE INTO title_meta (title_id, attempted_at, refresh_after) VALUES (1, 1, 1)`)
	if err := e.st.WriteTx(ctx, func(tx *sql.Tx) error { return writeMovieDates(ctx, tx, 1, m, testNow) }); err != nil {
		t.Fatal(err)
	}
	dates, err := availability.Dates(ctx, e.st.Reader(), 1)
	if err != nil || dates["theatrical"].Format(time.DateOnly) != "1985-07-03" ||
		dates["digital"].Format(time.DateOnly) != "2026-12-01" {
		t.Fatalf("acquisition's dates changed: %v %v", dates, err)
	}
	var rows string
	if err := e.st.Reader().QueryRow(`SELECT group_concat(country || type, ' ') FROM (SELECT * FROM title_release_date
		WHERE title_id = 1 AND source = 'tmdb' ORDER BY type)`).Scan(&rows); err != nil || rows != "USpremiere UStheatrical" {
		t.Errorf("stored country rows %q %v", rows, err)
	}
	items, err := activity.Calendar(ctx, e.st.Reader(), "2026-01-01", "2027-12-31", true)
	if err != nil || len(items) != 0 {
		t.Errorf("the re-release is on the calendar: %+v %v", items, err)
	}
	items, err = activity.Calendar(ctx, e.st.Reader(), "1985-01-01", "1985-12-31", true)
	if err != nil || len(items) != 1 || items[0].Type != "theatrical" || items[0].Date != "1985-07-03" {
		t.Errorf("cinema date: %+v %v", items, err)
	}
	fr := &tmdb.Movie{ReleaseDates: tmdb.ReleaseDates{Results: []tmdb.CountryReleases{
		{Country: "GB", Releases: []tmdb.Release{{Type: 5, Date: day("2025-03-01")}}},
		{Country: "FR", Releases: []tmdb.Release{{Type: 3, Date: day("2001-04-25")}, {Type: 1, Date: day("2000-01-01")}}},
		{Country: "BE", Releases: []tmdb.Release{{Type: 3, Date: day("2001-04-25")}}},
	}}}
	if got := fr.PrimaryCountry(); got != "BE" {
		t.Errorf("primary country without the US %q (the first by code on the earliest day)", got)
	}
}

// TestFirstCinemaAvailability (Z4.2): availability counts from the first real cinema date anywhere (wide or limited,
// any country) when it is earlier than the canonical cinema date; TMDb's general date (a festival premiere) only
// without any cinema date. The canonical dates and the dates shown stay.
func TestFirstCinemaAvailability(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	e.seed(t)
	e.exec(t, `INSERT OR IGNORE INTO title_meta (title_id, attempted_at, refresh_after) VALUES (1, 1, 1)`)
	day := func(d string) time.Time {
		v, err := time.Parse(time.DateOnly, d)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, tc := range []struct {
		name              string
		movie             tmdb.Movie
		cinemas, released bool
		home, shownCinema string
	}{
		{"1988 original abroad, 2026 US re-release, no home dates", tmdb.Movie{ReleaseDate: "1988-05-11",
			ReleaseDates: tmdb.ReleaseDates{Results: []tmdb.CountryReleases{
				{Country: "FR", Releases: []tmdb.Release{{Type: 3, Date: day("1988-05-11")}}},
				{Country: "US", Releases: []tmdb.Release{{Type: 3, Date: day("2026-09-01")}}},
			}}}, true, true, "1988-08-09", "2026-09-01"},
		{"festival general date, later cinema date", tmdb.Movie{ReleaseDate: "2026-01-25",
			ReleaseDates: tmdb.ReleaseDates{Results: []tmdb.CountryReleases{
				{Country: "US", Releases: []tmdb.Release{{Type: 1, Date: day("2026-01-25")}, {Type: 3, Date: day("2026-12-01")}}},
			}}}, false, false, "2027-03-01", "2026-12-01"},
		{"US first", tmdb.Movie{ReleaseDate: "2026-08-01",
			ReleaseDates: tmdb.ReleaseDates{Results: []tmdb.CountryReleases{
				{Country: "US", Releases: []tmdb.Release{{Type: 3, Date: day("2026-08-01")}}},
				{Country: "FR", Releases: []tmdb.Release{{Type: 2, Date: day("2026-08-15")}}},
			}}}, true, false, "2026-10-30", "2026-08-01"},
		{"home dates known", tmdb.Movie{ReleaseDate: "1988-05-11",
			ReleaseDates: tmdb.ReleaseDates{Results: []tmdb.CountryReleases{
				{Country: "FR", Releases: []tmdb.Release{{Type: 3, Date: day("1988-05-11")}}},
				{Country: "US", Releases: []tmdb.Release{{Type: 3, Date: day("2026-09-01")}, {Type: 4, Date: day("2026-12-01")}}},
			}}}, true, false, "2026-12-01", "2026-09-01"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := e.st.WriteTx(ctx, func(tx *sql.Tx) error { return writeMovieDates(ctx, tx, 1, &tc.movie, testNow) }); err != nil {
				t.Fatal(err)
			}
			dates, err := availability.Dates(ctx, e.st.Reader(), 1)
			if err != nil {
				t.Fatal(err)
			}
			if got := availability.Available("inCinemas", dates, testNow, 0); got != tc.cinemas {
				t.Errorf("in cinemas: %v, want %v (%v)", got, tc.cinemas, dates)
			}
			if got := availability.Available("released", dates, testNow, 0); got != tc.released {
				t.Errorf("released: %v, want %v (%v)", got, tc.released, dates)
			}
			if got := availability.HomeRelease(dates).Format(time.DateOnly); got != tc.home {
				t.Errorf("home release %s, want %s", got, tc.home)
			}
			// The canonical cinema date and the date the lists, the calendar and the title page show stay.
			if got := dates["theatrical"].Format(time.DateOnly); got != tc.shownCinema {
				t.Errorf("canonical cinema date %s, want %s", got, tc.shownCinema)
			}
			shown, err := activity.ReadMovieDates(ctx, e.st.Reader(), []any{int64(1)})
			if err != nil || shown[1].First["theatrical"] != tc.shownCinema {
				t.Errorf("shown cinema date %v %v, want %s", shown[1].First, err, tc.shownCinema)
			}
		})
	}
}
