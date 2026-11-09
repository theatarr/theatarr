package catalog

import (
	"context"
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
	if _, err := st.Writer().Exec(`INSERT INTO movie_import_list(name,tmdb_id,versions,monitor,enabled,enable_auto,created_at,updated_at)
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
