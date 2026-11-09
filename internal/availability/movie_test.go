package availability

import (
	"context"
	"maps"
	"path/filepath"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func TestAvailable(t *testing.T) {
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, minimum string
		dates         map[string]time.Time
		want          bool
	}{
		{"announced without dates", "announced", nil, true},
		{"tba without dates", "tba", nil, true},
		{"released unknown", "released", nil, false},
		{"default unknown", "", nil, false},
		{"cinema boundary", "inCinemas", map[string]time.Time{"theatrical": now}, true},
		{"cinema future", "inCinemas", map[string]time.Time{"theatrical": now.Add(time.Hour)}, false},
		{"cinema absent falls back to digital", "inCinemas", map[string]time.Time{"digital": now}, true},
		{"earliest digital", "released", map[string]time.Time{"digital": now, "physical": now.AddDate(0, 0, 20)}, true},
		{"earliest physical", "released", map[string]time.Time{"digital": now.AddDate(0, 0, 20), "physical": now}, true},
		{"90 day fallback", "released", map[string]time.Time{"theatrical": now.AddDate(0, 0, -90)}, true},
		{"before 90 days", "released", map[string]time.Time{"theatrical": now.AddDate(0, 0, -90).Add(time.Second)}, false},
		{"known future home release overrides fallback", "released", map[string]time.Time{"theatrical": now.AddDate(0, 0, -100), "digital": now.AddDate(0, 0, 1)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Available(tc.minimum, tc.dates, now); got != tc.want {
				t.Fatalf("available=%v, want %v", got, tc.want)
			}
		})
	}
}

// TestHomeRelease: the date the released minimum compares against, so Available agrees with it on every day around.
func TestHomeRelease(t *testing.T) {
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return now.AddDate(0, 0, n) }
	for _, tc := range []struct {
		name  string
		dates map[string]time.Time
		want  time.Time
	}{
		{"no dates", nil, time.Time{}},
		{"in cinemas, digital to come", map[string]time.Time{"theatrical": day(-10), "digital": day(20)}, day(20)},
		{"digital out, disc to come", map[string]time.Time{"theatrical": day(-60), "digital": day(-5), "physical": day(30)}, day(-5)},
		{"disc first", map[string]time.Time{"digital": day(30), "physical": day(3)}, day(3)},
		{"cinema 30 days ago", map[string]time.Time{"theatrical": day(-30)}, day(60)},
		{"cinema 100 days ago", map[string]time.Time{"theatrical": day(-100)}, day(-10)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := HomeRelease(tc.dates)
			if !got.Equal(tc.want) {
				t.Fatalf("home release %v, want %v", got, tc.want)
			}
			for _, at := range []time.Time{day(-200), day(-10), day(-5), now, day(3), day(20), day(60), day(200)} {
				if Available("released", tc.dates, at) != (!got.IsZero() && !at.Before(got)) {
					t.Errorf("at %v: Available disagrees with home release %v", at, got)
				}
			}
		})
	}
}

func TestStatus(t *testing.T) {
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		dates map[string]time.Time
		want  string
	}{
		{nil, "announced"},
		{map[string]time.Time{"theatrical": now.Add(-time.Hour)}, "inCinemas"},
		{map[string]time.Time{"theatrical": now.AddDate(0, 0, -91)}, "released"},
		{map[string]time.Time{"theatrical": now.AddDate(0, 0, -91), "digital": now.AddDate(0, 0, 1)}, "inCinemas"},
		{map[string]time.Time{"digital": now}, "released"},
		{map[string]time.Time{"physical": now}, "released"},
	} {
		if got := Status(tc.dates, now); got != tc.want {
			t.Errorf("%v: got %s, want %s", tc.dates, got, tc.want)
		}
	}
}

func TestFollowedMovieDatesRespectOwnership(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Load(filepath.Join("..", "..", "examples", "policy"))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.Writer().Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO title(id,kind,name,tmdb_id,created_at,updated_at) VALUES(1,'movie','Movie',100,0,0)`)
	exec(`INSERT INTO version(id,title_id,policy_key,created_at,updated_at) VALUES(1,1,'movie-remux',0,0),(2,1,'parallel',0,0)`)
	exec(`INSERT INTO version_source(version_id,instance,arr_id) VALUES(1,'radarr_4k_debrid',1)`)
	exec(`INSERT INTO version_follow(version_id,follows_id) VALUES(2,1)`)
	exec(`INSERT INTO title_meta(title_id,movie_dates_at,attempted_at,refresh_after) VALUES(1,1,1,1)`)
	exec(`INSERT INTO title_release_date(title_id,source,country,type,date,fetched_at) VALUES
  (1,'tmdb-art','','digital','2026-12-01',1),(1,'radarr_4k_debrid','','digital','2026-10-01',2)`)
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	check := func(vid int64, want bool) {
		t.Helper()
		got, err := Movie(ctx, st.Reader(), pol, 1, vid, "parallel", now)
		if err != nil || got != want {
			t.Fatalf("version %d available=%v want=%v: %v", vid, got, want, err)
		}
	}
	check(1, true)
	check(2, true)
	// DatesFor reads what Dates reads: the TMDb refresh over the import.
	one, err := Dates(ctx, st.Reader(), 1)
	if err != nil {
		t.Fatal(err)
	}
	many, err := DatesFor(ctx, st.Reader(), []any{int64(1), int64(2)})
	if err != nil || len(many) != 1 || !maps.Equal(many[1], one) || !one["digital"].Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("dates for: %v %v, Dates %v", many, err, one)
	}
	// More ids than SQLite binds in one query (32,766): every batch is read.
	exec(`INSERT INTO title(id,kind,name,tmdb_id,created_at,updated_at) VALUES(40000,'movie','Last',40000,0,0)`)
	exec(`INSERT INTO title_release_date(title_id,source,country,type,date) VALUES(40000,'tmdb','','physical','2026-05-01')`)
	ids := make([]any, 0, 40000)
	for id := int64(1); id <= 40000; id++ {
		ids = append(ids, id)
	}
	if many, err := DatesFor(ctx, st.Reader(), ids); err != nil || len(many) != 2 || many[40000]["physical"].IsZero() {
		t.Fatalf("batched dates for: %v %v", many, err)
	}
	// Corrections can move a date in either direction; the owning arr remains authoritative.
	exec(`UPDATE title_release_date SET date='2027-01-01',fetched_at=3 WHERE source='radarr_4k_debrid'`)
	check(2, false)
	exec(`UPDATE title_release_date SET date='2026-10-01' WHERE source='tmdb-art'`)
	check(2, false)
	delete(pol.Import.Arr, "radarr_4k_debrid")
	check(1, true)
	check(2, true)
}
