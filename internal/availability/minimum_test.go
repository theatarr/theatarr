package availability

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// mainMinimum is the minimum availability Movie computed before Rules existed, kept as the reference: with no
// library setting, Rules must give the same value for the same database.
func mainMinimum(ctx context.Context, db *sql.DB, pol *policy.Policy, titleID, versionID int64, key string) (string, error) {
	minimum := "released"
	var imported string
	err := db.QueryRowContext(ctx, `SELECT a.minimum_availability FROM arr_title a
		LEFT JOIN version_source vs ON vs.version_id = a.version_id AND vs.instance = a.instance AND vs.arr_id = a.arr_id
		WHERE a.version_id = ? ORDER BY (vs.instance IS NOT NULL) DESC, a.instance LIMIT 1`, versionID).Scan(&imported)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if imported != "" {
		minimum = imported
	}
	rows, err := db.QueryContext(ctx, `SELECT view, minimum_availability FROM arrview_title
		WHERE title_id = ? AND minimum_availability <> '' ORDER BY updated_at DESC, view`, titleID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var view, value string
		if err := rows.Scan(&view, &value); err != nil {
			return "", err
		}
		v := pol.Views[view]
		if view == "v/"+key || v != nil && slices.Contains(v.Versions, key) {
			minimum = value
			break
		}
	}
	return minimum, rows.Err()
}

func minimumEnv(t *testing.T) (*store.Store, *policy.Policy, func(q string, args ...any)) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Load(filepath.Join("..", "..", "examples", "policy"))
	if err != nil {
		t.Fatal(err)
	}
	return st, pol, func(q string, args ...any) {
		t.Helper()
		if _, err := st.Writer().Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// Rules, one title or many, and Movie's single-copy form agree with what Movie computed on main, for every stored
// state main could hold; then a library default and delay reach only the copies without a choice or imported value.
func TestRulesMatchMain(t *testing.T) {
	ctx := context.Background()
	st, pol, exec := minimumEnv(t)
	// Titles 1..9, each with movie-remux (id n*10+1), movie-web (n*10+2) and anime-movie (n*10+3).
	type copyOf struct {
		title, version int64
		key            string
	}
	var copies []copyOf
	var ids []int64
	for n := int64(1); n <= 9; n++ {
		exec(`INSERT INTO title(id,kind,name,tmdb_id,created_at,updated_at) VALUES(?,'movie','M',?,0,0)`, n, 100+n)
		exec(`INSERT INTO version(id,title_id,policy_key,created_at,updated_at) VALUES(?,?,'movie-remux',0,0),
			(?,?,'movie-web',0,0),(?,?,'anime-movie',0,0)`, n*10+1, n, n*10+2, n, n*10+3, n)
		copies = append(copies, copyOf{n, n*10 + 1, "movie-remux"}, copyOf{n, n*10 + 2, "movie-web"},
			copyOf{n, n*10 + 3, "anime-movie"})
		ids = append(ids, n)
	}
	arr := func(instance string, version int64, owner bool, minimum string) {
		exec(`INSERT INTO arr_title(instance,arr_id,kind,title_id,version_id,monitored,minimum_availability)
			VALUES(?,?,'movie',?,?,1,?)`, instance, version, version/10, version, minimum)
		if owner {
			exec(`INSERT INTO version_source(version_id,instance,arr_id) VALUES(?,?,?)`, version, instance, version)
		}
	}
	row := func(view string, title int64, minimum string, updated int64) {
		exec(`INSERT INTO arrview_title(view,title_id,minimum_availability,added_at,updated_at) VALUES(?,?,?,0,?)`,
			view, title, minimum, updated)
	}
	exec(`INSERT INTO arr_instance(name,flavour,policy_key,created_at,updated_at) VALUES
		('radarr_4k_debrid','radarr','movie-remux',0,0),('radarr_4k_web','radarr','movie-web',0,0),
		('aaa_other','radarr','movie-remux',0,0)`)
	// 1: nothing stored.
	// 2: imported by the owner.
	arr("radarr_4k_debrid", 21, true, "announced")
	// 3: the owner's record is empty and another instance's, sorting first by name, has a value: the owner's wins.
	arr("radarr_4k_debrid", 31, true, "")
	arr("aaa_other", 31, false, "inCinemas")
	// 4: no owner; the first instance by name.
	arr("radarr_4k_debrid", 41, false, "announced")
	arr("aaa_other", 41, false, "inCinemas")
	// 5: a library's own row over the import.
	arr("radarr_4k_web", 52, true, "announced")
	row("v/movie-web", 5, "inCinemas", 5)
	// 6: a combined row older than a library's row, 7: newer, 8: tied (the view name decides).
	row("radarr", 6, "announced", 1)
	row("v/movie-remux", 6, "inCinemas", 2)
	row("radarr", 7, "announced", 2)
	row("v/movie-remux", 7, "inCinemas", 1)
	row("radarr", 8, "announced", 3)
	row("v/movie-remux", 8, "inCinemas", 3)
	// 9: a row of a view that is gone and a row with no value do not count.
	row("old-view", 9, "announced", 9)
	row("v/movie-web", 9, "", 10)

	compare := func() map[int64]Rule {
		t.Helper()
		all, err := Rules(ctx, st.Reader(), pol, ids)
		if err != nil {
			t.Fatal(err)
		}
		if len(all) != len(copies) {
			t.Fatalf("%d rules for %d copies", len(all), len(copies))
		}
		for _, c := range copies {
			want, err := mainMinimum(ctx, st.Reader(), pol, c.title, c.version, c.key)
			if err != nil {
				t.Fatal(err)
			}
			if all[c.version].Minimum != want {
				t.Errorf("title %d %s: Rules %q, main %q", c.title, c.key, all[c.version].Minimum, want)
			}
			one, err := Rules(ctx, st.Reader(), pol, []int64{c.title})
			if err != nil {
				t.Fatal(err)
			}
			single := map[int64]Rule{}
			if err := rulesInto(ctx, st.Reader(), pol, []copyRef{{c.title, c.version, c.key}}, single); err != nil {
				t.Fatal(err)
			}
			if one[c.version] != all[c.version] || single[c.version] != all[c.version] {
				t.Errorf("title %d %s: batch %+v, one title %+v, one copy %+v", c.title, c.key, all[c.version],
					one[c.version], single[c.version])
			}
		}
		return all
	}
	all := compare()
	for version, want := range map[int64]Rule{
		11: {Minimum: "released", Source: SourceDefault},
		21: {Minimum: "announced", Source: SourceImported},
		22: {Minimum: "released", Source: SourceDefault},
		23: {Minimum: "released", Source: SourceDefault},
		31: {Minimum: "released", Source: SourceDefault},
		41: {Minimum: "inCinemas", Source: SourceImported},
		52: {Minimum: "inCinemas", Source: SourceChoice},
		61: {Minimum: "inCinemas", Source: SourceChoice},
		62: {Minimum: "announced", Source: SourceChoice},
		71: {Minimum: "announced", Source: SourceChoice},
		81: {Minimum: "announced", Source: SourceChoice},
		92: {Minimum: "released", Source: SourceDefault},
	} {
		if all[version] != want {
			t.Errorf("version %d: %+v, want %+v", version, all[version], want)
		}
	}

	// A library default and delay: only copies with neither a choice nor an imported value take the default.
	pol.Versions["movie-remux"].MinimumAvailability = "inCinemas"
	pol.Versions["movie-remux"].AvailabilityDelay = 7
	all, err := Rules(ctx, st.Reader(), pol, ids)
	if err != nil {
		t.Fatal(err)
	}
	for version, want := range map[int64]Rule{
		11: {Minimum: "inCinemas", Delay: 7, Source: SourceDefault},
		12: {Minimum: "released", Source: SourceDefault},
		21: {Minimum: "announced", Delay: 7, Source: SourceImported},
		31: {Minimum: "inCinemas", Delay: 7, Source: SourceDefault},
		61: {Minimum: "inCinemas", Delay: 7, Source: SourceChoice},
		62: {Minimum: "announced", Source: SourceChoice},
	} {
		if all[version] != want {
			t.Errorf("with a default, version %d: %+v, want %+v", version, all[version], want)
		}
	}

	// Choose writes the libraries' own rows only, newest, and keeps what else a row holds.
	exec(`INSERT INTO arrview_title(view,title_id,quality_profile_id,root_folder_path,minimum_availability,added_at,
		updated_at) VALUES('v/movie-web',1,4,'/root','',77,1)`)
	if err := Choose(ctx, st.Writer(), 1, []string{"movie-web", "anime-movie"}, "announced", time.UnixMilli(50)); err != nil {
		t.Fatal(err)
	}
	if all, err = Rules(ctx, st.Reader(), pol, []int64{1}); err != nil {
		t.Fatal(err)
	}
	want := map[int64]Rule{11: {Minimum: "inCinemas", Delay: 7, Source: SourceDefault},
		12: {Minimum: "announced", Source: SourceChoice}, 13: {Minimum: "announced", Source: SourceChoice}}
	for version, w := range want {
		if all[version] != w {
			t.Errorf("after Choose, version %d: %+v, want %+v", version, all[version], w)
		}
	}
	var qp, added, updated int64
	var root string
	if err := st.Reader().QueryRow(`SELECT quality_profile_id, root_folder_path, added_at, updated_at FROM arrview_title
		WHERE view='v/movie-web' AND title_id=1`).Scan(&qp, &root, &added, &updated); err != nil {
		t.Fatal(err)
	}
	if qp != 4 || root != "/root" || added != 77 || updated != 50 {
		t.Errorf("Choose changed the row: profile %d root %q added %d updated %d", qp, root, added, updated)
	}
	if err := st.Reader().QueryRow(`SELECT quality_profile_id, root_folder_path, added_at, updated_at FROM arrview_title
		WHERE view='v/anime-movie' AND title_id=1`).Scan(&qp, &root, &added, &updated); err != nil {
		t.Fatal(err)
	}
	if qp != 0 || root != "" || added != 0 || updated != 50 {
		t.Errorf("Choose's new row: profile %d root %q added %d updated %d", qp, root, added, updated)
	}
}

func TestEarliest(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{nil, "released"},
		{[]string{"released"}, "released"},
		{[]string{"released", "inCinemas"}, "inCinemas"},
		{[]string{"inCinemas", "announced", "released"}, "announced"},
		{[]string{"announced", "tba"}, "tba"},
		{[]string{"deleted", "released"}, "deleted"},
		{[]string{"deleted", "inCinemas"}, "inCinemas"},
	} {
		if got := Earliest(tc.in...); got != tc.want {
			t.Errorf("Earliest(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The availability delay as Radarr applies it (Movie.cs IsAvailable): days added to the date compared against,
// negative for earlier; announced and tba ignore it, and a movie without a date is never available.
func TestAvailableDelay(t *testing.T) {
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return now.AddDate(0, 0, n) }
	for _, tc := range []struct {
		name, minimum string
		dates         map[string]time.Time
		delay         int
		want          bool
	}{
		{"released today, no delay", "released", map[string]time.Time{"digital": day(0)}, 0, true},
		{"released today, 3 days", "released", map[string]time.Time{"digital": day(0)}, 3, false},
		{"released 3 days ago, 3 days", "released", map[string]time.Time{"digital": day(-3)}, 3, true},
		{"released 2 days ago, 3 days", "released", map[string]time.Time{"digital": day(-2)}, 3, false},
		{"out in 5 days, 5 days early", "released", map[string]time.Time{"digital": day(5)}, -5, true},
		{"out in 6 days, 5 days early", "released", map[string]time.Time{"digital": day(6)}, -5, false},
		{"cinema today, 1 day", "inCinemas", map[string]time.Time{"theatrical": day(0)}, 1, false},
		{"cinema tomorrow, 1 day early", "inCinemas", map[string]time.Time{"theatrical": day(1)}, -1, true},
		{"cinema fallback 90 days, 10 days", "released", map[string]time.Time{"theatrical": day(-95)}, 10, false},
		{"cinema fallback 90 days, 10 days early", "released", map[string]time.Time{"theatrical": day(-85)}, -10, true},
		{"announced ignores a delay", "announced", map[string]time.Time{"digital": day(30)}, 30, true},
		{"announced without dates", "announced", nil, 30, true},
		{"tba ignores a delay", "tba", nil, 30, true},
		{"no date, positive", "released", nil, 30, false},
		{"no date, negative", "released", nil, -3000, false},
		{"no date, in cinemas, negative", "inCinemas", nil, -3000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Available(tc.minimum, tc.dates, now, tc.delay); got != tc.want {
				t.Fatalf("available=%v, want %v", got, tc.want)
			}
		})
	}
}

// Movie applies the library's delay and default.
func TestMovieDelayAndDefault(t *testing.T) {
	ctx := context.Background()
	st, pol, exec := minimumEnv(t)
	exec(`INSERT INTO title(id,kind,name,tmdb_id,created_at,updated_at) VALUES(1,'movie','M',101,0,0)`)
	exec(`INSERT INTO version(id,title_id,policy_key,created_at,updated_at) VALUES(1,1,'movie-remux',0,0),(2,1,'movie-web',0,0)`)
	exec(`INSERT INTO title_release_date(title_id,source,country,type,date) VALUES
		(1,'tmdb','','theatrical','2026-10-01'),(1,'tmdb','','digital','2026-10-25')`)
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	check := func(what string, vid int64, key string, want bool) {
		t.Helper()
		got, err := Movie(ctx, st.Reader(), pol, 1, vid, key, now)
		if err != nil || got != want {
			t.Errorf("%s: %s available=%v, want %v: %v", what, key, got, want, err)
		}
	}
	check("nothing set", 1, "movie-remux", true)
	check("nothing set", 2, "movie-web", true)
	pol.Versions["movie-remux"].AvailabilityDelay = 3
	check("3 days", 1, "movie-remux", false)
	check("3 days on the other library", 2, "movie-web", true)
	pol.Versions["movie-remux"].AvailabilityDelay = 2
	check("2 days", 1, "movie-remux", true)
	pol.Versions["movie-remux"].AvailabilityDelay = 30
	pol.Versions["movie-remux"].MinimumAvailability = "inCinemas"
	check("in cinemas 26 days ago, 30 days", 1, "movie-remux", false)
	pol.Versions["movie-remux"].MinimumAvailability = "announced"
	check("announced default ignores the delay", 1, "movie-remux", true)
	exec(`UPDATE title_release_date SET date='2026-11-05' WHERE type='digital'`)
	exec(`UPDATE title_release_date SET date='2026-11-01' WHERE type='theatrical'`)
	check("not out", 2, "movie-web", false)
	pol.Versions["movie-web"].AvailabilityDelay = -9
	check("9 days early", 2, "movie-web", true)
	pol.Versions["movie-web"].AvailabilityDelay = -8
	check("8 days early", 2, "movie-web", false)
}

// Without following, a former follower's copy reads the choice the detach migration (00121) wrote on its own row
// (v/<key>, updated_at 0), and a copy an arr feeds still reads its arr_title record.
func TestRulesWithoutFollow(t *testing.T) {
	ctx := context.Background()
	st, pol, exec := minimumEnv(t)
	exec(`INSERT INTO title(id,kind,name,tmdb_id,created_at,updated_at) VALUES(1,'movie','M',101,0,0)`)
	exec(`INSERT INTO version(id,title_id,policy_key,created_at,updated_at) VALUES(11,1,'movie-remux',0,0),
		(13,1,'anime-movie',0,0)`)
	exec(`INSERT INTO arr_instance(name,flavour,policy_key,created_at,updated_at) VALUES
		('radarr_4k_debrid','radarr','movie-remux',0,0)`)
	exec(`INSERT INTO arr_title(instance,arr_id,kind,title_id,version_id,monitored,minimum_availability)
		VALUES('radarr_4k_debrid',11,'movie',1,11,1,'inCinemas')`)
	exec(`INSERT INTO version_source(version_id,instance,arr_id) VALUES(11,'radarr_4k_debrid',11)`)
	exec(`INSERT INTO arrview_title(view,title_id,minimum_availability,added_at,updated_at)
		VALUES('v/anime-movie',1,'announced',0,0)`)
	all, err := Rules(ctx, st.Reader(), pol, []int64{1})
	if err != nil {
		t.Fatal(err)
	}
	if got := all[13]; got.Minimum != "announced" || got.Source != SourceChoice {
		t.Errorf("former follower %+v, want announced from its migrated row", got)
	}
	if got := all[11]; got.Minimum != "inCinemas" || got.Source != SourceImported {
		t.Errorf("mirror copy %+v, want inCinemas imported", got)
	}
	// A newer explicit choice on a view holding the library still wins over the migrated row.
	exec(`INSERT INTO title(id,kind,name,tmdb_id,created_at,updated_at) VALUES(2,'movie','N',102,0,0)`)
	exec(`INSERT INTO version(id,title_id,policy_key,created_at,updated_at) VALUES(21,2,'movie-remux',0,0)`)
	exec(`INSERT INTO arrview_title(view,title_id,minimum_availability,added_at,updated_at)
		VALUES('v/movie-remux',2,'announced',0,0),('radarr',2,'inCinemas',1,7)`)
	if all, err = Rules(ctx, st.Reader(), pol, []int64{2}); err != nil {
		t.Fatal(err)
	}
	if got := all[21]; got.Minimum != "inCinemas" || got.Source != SourceChoice {
		t.Errorf("view choice over the migrated row: %+v, want inCinemas", got)
	}
}
