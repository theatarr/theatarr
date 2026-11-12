package catalog

import (
	"context"
	"github.com/theatarr/theatarr/internal/availability"
	"testing"

	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/policy"
)

// A second list targeting another version adds that version to a movie the first list added; a version
// the title already has, unmonitored or removed, stays as it is; an excluded movie gets nothing.
func TestListIntakeAddsMissingVersionsOfHeldMovie(t *testing.T) {
	t.Parallel()
	s, st := newIntake(t, intakeSource{list: &tmdb.List{Items: []tmdb.MovieSummary{{ID: 201}}}})
	s.Policy.Versions["movie-remux"] = &policy.VersionPolicy{Kind: policy.KindMovie, Enable: "default"}
	s.Policy.Versions["movie-hd"] = &policy.VersionPolicy{Kind: policy.KindMovie, Enable: "default"}
	if _, err := st.Writer().Exec(`INSERT INTO import_list(name,tmdb_id,versions,monitor,enabled,enable_auto,created_at,updated_at)
		VALUES('web',9,'["movie-web"]','movieOnly',1,1,0,0),('remux',10,'["movie-remux","movie-hd"]','movieOnly',1,1,0,0)`); err != nil {
		t.Fatal(err)
	}
	// movie-hd exists already, unmonitored on purpose.
	if _, err := st.Writer().Exec(`INSERT INTO title(id,kind,name,tmdb_id,created_at,updated_at) VALUES(50,'movie','Held',201,0,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().Exec(`INSERT INTO version(title_id,policy_key,monitored,state,created_at,updated_at) VALUES(50,'movie-hd',0,'wanted',0,0)`); err != nil {
		t.Fatal(err)
	}
	got, err := s.SyncLists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := st.Reader().QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, k := range []string{"movie-web", "movie-remux"} {
		if count(`SELECT count(*) FROM version WHERE title_id=50 AND policy_key=? AND monitored=1 AND add_method='list'`, k) != 1 {
			t.Errorf("held movie did not get %s from its list", k)
		}
	}
	if count(`SELECT count(*) FROM version WHERE title_id=50 AND policy_key='movie-hd' AND monitored=0`) != 1 {
		t.Error("deliberately unmonitored movie-hd was turned on")
	}
	if got.Added != 2 {
		t.Errorf("added %d, want 2 (one per list that added versions)", got.Added)
	}
	// A second sync adds nothing.
	if got, err = s.SyncLists(context.Background()); err != nil || got.Added != 0 {
		t.Fatalf("second sync %+v %v", got, err)
	}
	// An excluded movie gets no version.
	if _, err := st.Writer().Exec(`INSERT INTO movie_exclusion(tmdb_id,created_at) VALUES(201,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().Exec(`UPDATE version SET policy_key='movie-old' WHERE title_id=50 AND policy_key='movie-remux'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncLists(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count(`SELECT count(*) FROM version WHERE title_id=50 AND policy_key='movie-remux'`) != 0 {
		t.Error("excluded movie got a version")
	}
}

// A list through a combined view that adds a library to a movie already held in another stores its minimum
// availability for the library it adds only: the held copy keeps its rule, whether or not the view already has a
// record of the movie, and the added library gets the list's value and its search on add.
func TestListIntakeMinimumReachesAddedLibraryOnly(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		record bool
	}{{"the view has no record", false}, {"the view has a record", true}} {
		t.Run(tc.name, func(t *testing.T) {
			s, st := newIntake(t, intakeSource{list: &tmdb.List{Items: []tmdb.MovieSummary{{ID: 201}}}})
			s.Policy.Versions["movie-remux"] = &policy.VersionPolicy{Kind: policy.KindMovie, Enable: "default"}
			s.Policy.Views = map[string]*policy.View{"radarr": {Flavour: "radarr", Versions: []string{"movie-web", "movie-remux"}}}
			exec := func(q string) {
				t.Helper()
				if _, err := st.Writer().Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			exec(`INSERT INTO import_list(name,tmdb_id,versions,minimum_availability,monitor,view,search_on_add,
				enabled,enable_auto,created_at,updated_at) VALUES('remux',9,'["movie-remux"]','announced','movieOnly','radarr',1,1,1,0,0)`)
			exec(`INSERT INTO title(id,kind,name,tmdb_id,created_at,updated_at) VALUES(50,'movie','Held',201,0,0)`)
			exec(`INSERT INTO version(id,title_id,policy_key,monitored,state,created_at,updated_at) VALUES(1,50,'movie-web',1,'wanted',0,0)`)
			if tc.record {
				exec(`INSERT INTO arrview_title(view,title_id,quality_profile_id,root_folder_path,added_at,updated_at) VALUES('radarr',50,3,'/held',1,1)`)
			}
			var searched []string
			s.OnAdd = func(_ context.Context, _ int64, keys []string) error {
				searched = append(searched, keys...)
				return nil
			}
			if got, err := s.SyncLists(context.Background()); err != nil || got.Added != 1 {
				t.Fatalf("sync: %+v %v", got, err)
			}
			if len(searched) != 1 || searched[0] != "movie-remux" {
				t.Errorf("searched %v, want movie-remux only", searched)
			}
			rules, err := availability.Rules(context.Background(), st.Reader(), s.Policy, []int64{50})
			if err != nil {
				t.Fatal(err)
			}
			var remux int64
			if err := st.Reader().QueryRow(`SELECT id FROM version WHERE title_id=50 AND policy_key='movie-remux'`).Scan(&remux); err != nil {
				t.Fatal(err)
			}
			if rules[1] != (availability.Rule{Minimum: "released", Source: availability.SourceDefault}) ||
				rules[remux] != (availability.Rule{Minimum: "announced", Source: availability.SourceChoice}) {
				t.Errorf("rules: held %+v, added %+v", rules[1], rules[remux])
			}
			var n int
			if err := st.Reader().QueryRow(`SELECT count(*) FROM arrview_title WHERE view='radarr' AND title_id=50
				AND minimum_availability=''`).Scan(&n); err != nil || n != 1 {
				t.Errorf("the view's record: %d rows without a minimum: %v", n, err)
			}
			if tc.record {
				if err := st.Reader().QueryRow(`SELECT count(*) FROM arrview_title WHERE view='radarr' AND title_id=50
					AND quality_profile_id=3 AND root_folder_path='/held'`).Scan(&n); err != nil || n != 1 {
					t.Errorf("the view's record was changed: %v", err)
				}
			}
		})
	}
}
