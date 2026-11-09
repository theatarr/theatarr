package availability

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// New content: an episode by its air date, a movie by its digital or physical date (never theatrical).
func TestFresh(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'series', 'Show', 0, 0),
			(2, 'movie', 'Home', 0, 0), (3, 'movie', 'Old', 0, 0), (4, 'movie', 'Cinema', 0, 0), (5, 'movie', 'None', 0, 0),
			(6, 'movie', 'Disc', 0, 0)`,
		`INSERT INTO title_release_date (title_id, source, type, date) VALUES
			(2, 'fixture', 'theatrical', '2026-09-01'), (2, 'fixture', 'digital', '2026-11-04'),
			(2, 'fixture', 'physical', '2026-12-01'),
			(3, 'fixture', 'digital', '2026-10-20'), (3, 'fixture', 'theatrical', '2026-11-03'),
			(4, 'fixture', 'theatrical', '2026-11-02'),
			(6, 'fixture', 'digital', '2026-10-01'), (6, 'fixture', 'physical', '2026-11-05')`,
	} {
		if _, err := st.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for id, aired := range map[int64]time.Time{11: now.Add(-2 * day), 12: now.Add(-10 * day), 13: now.Add(day)} {
		if _, err := st.Writer().Exec(`INSERT INTO episode (id, title_id, season, number, air_date_utc_at, created_at,
			updated_at) VALUES (?, 1, 1, ?, ?, 0, 0)`, id, id, store.Millis(aired)); err != nil {
			t.Fatal(err)
		}
	}
	date := func(s string) time.Time { d, _ := time.Parse(time.DateOnly, s); return d }
	for _, q := range []struct {
		name  string
		kind  policy.Kind
		title int64
		eps   []int64
		want  time.Time
	}{
		{"episode aired 2 days ago", policy.KindSeries, 1, []int64{11}, now.Add(5 * day)},
		{"episode aired 10 days ago", policy.KindSeries, 1, []int64{12}, time.Time{}},
		{"not aired yet", policy.KindSeries, 1, []int64{13}, time.Time{}},
		{"pack: the latest aired", policy.KindSeries, 1, []int64{12, 11}, now.Add(5 * day)},
		{"no episodes", policy.KindSeries, 1, nil, time.Time{}},
		{"movie by its digital date", policy.KindMovie, 2, nil, date("2026-11-04").Add(FreshWindow)},
		{"movie: home release old, theatrical new", policy.KindMovie, 3, nil, time.Time{}},
		{"movie with only a theatrical date", policy.KindMovie, 4, nil, time.Time{}},
		{"movie: digital old, physical new", policy.KindMovie, 6, nil, date("2026-11-05").Add(FreshWindow)},
		{"pack with an episode not aired yet", policy.KindSeries, 1, []int64{13, 11}, now.Add(5 * day)},
		{"movie without dates", policy.KindMovie, 5, nil, time.Time{}},
	} {
		got, err := Fresh(ctx, st.Reader(), q.kind, q.title, q.eps, now)
		if err != nil || !got.Equal(q.want) {
			t.Errorf("%s: %v %v, want %v", q.name, got, err, q.want)
		}
	}
}

// A series' new episodes: aired within the window; not yet aired, older or removed left out.
func TestFreshEpisodes(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	if _, err := st.Writer().Exec(`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'series', 'Show', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	for id, aired := range map[int64]time.Time{11: now.Add(-2 * day), 12: now.Add(-10 * day), 13: now.Add(day),
		14: now.Add(-day), 15: now.Add(-day)} {
		if _, err := st.Writer().Exec(`INSERT INTO episode (id, title_id, season, number, air_date_utc_at, created_at,
			updated_at) VALUES (?, 1, 1, ?, ?, 0, 0)`, id, id, store.Millis(aired)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Writer().Exec(`UPDATE episode SET removed_at = 1 WHERE id = 15`); err != nil {
		t.Fatal(err)
	}
	ids, until, err := FreshEpisodes(ctx, st.Reader(), 1, now)
	if err != nil || len(ids) != 2 || ids[0] != 11 || ids[1] != 14 || !until.Equal(now.Add(6*day)) {
		t.Fatalf("%v %v %v", ids, until, err)
	}
	if ids, until, err := FreshEpisodes(ctx, st.Reader(), 1, now.Add(9*day)); err != nil || ids != nil || !until.IsZero() {
		t.Fatalf("nine days later: %v %v %v", ids, until, err)
	}
}
