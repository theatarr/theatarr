package importarr

import (
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// The arr's tag labels reach the views' tags of the source version: Seerr requester tags in Seerr's
// current form, behaviour and hand labels as they are. An existing view tag is reused ignoring case. The copy is
// edge-triggered: a label is copied once, when it first appears on the arr's title, so a tag a view took off stays
// off, a label the arr adds later is copied on the next run, and nothing a view set is removed.
func TestImportCopiesTagsToVersions(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	fake := newFake("/mnt/movies-a")
	fake.tags = []map[string]any{{"id": 1, "label": "best-available"}, {"id": 2, "label": "1 - alice"},
		{"id": 3, "label": "2-someone"}, {"id": 4, "label": "local"}, {"id": 5, "label": "skip-upgrade-check"},
		{"id": 6, "label": "debrid-upgrade"}}
	fake.movies[0]["tags"] = []int{1, 2, 4} // Alpha
	fake.movies[1]["tags"] = []int{1, 5}    // Beta
	fake.movies[3]["tags"] = []int{3, 2, 9} // Delta, 9 is no tag the arr lists
	srv := httptest.NewServer(fake)
	defer srv.Close()
	// A tag a view client made before, in other case.
	if _, err := st.Writer().Exec(`INSERT INTO arrview_tag (id, label, created_at) VALUES (50, '2-Someone', 0)`); err != nil {
		t.Fatal(err)
	}
	im := &Importer{Store: st, Now: fixedNow}
	src := Source{Instance: "radarr_a", Flavour: "radarr", Version: "movie-remux", Library: "/mnt/movies-a"}
	runImport(t, im, src, srv)

	viewTags := func(tmdb int, version string) string {
		return one[string](t, st, `SELECT COALESCE(group_concat(label, ','), '') FROM (SELECT g.label FROM arrview_title_tag tt
			JOIN arrview_tag g ON g.id = tt.tag_id JOIN title ti ON ti.id = tt.title_id WHERE ti.tmdb_id = ? AND tt.version = ?
			ORDER BY g.label)`, tmdb, version)
	}
	all := func(version string) map[string]string {
		return map[string]string{"Alpha": viewTags(100, version), "Beta": viewTags(200, version), "Delta": viewTags(400, version)}
	}
	want := map[string]string{"Alpha": "1-alice,best-available,local", "Beta": "best-available,skip-upgrade-check",
		"Delta": "1-alice,2-Someone"}
	if got := all("movie-remux"); !reflect.DeepEqual(got, want) {
		t.Errorf("view tags %v, want %v", got, want)
	}
	if n := one[int](t, st, `SELECT COUNT(*) FROM arrview_title_tag WHERE version <> 'movie-remux'`); n != 0 {
		t.Errorf("%d tags on other versions", n)
	}
	if n := one[int](t, st, `SELECT COUNT(*) FROM arrview_tag WHERE label LIKE '#%'`); n != 0 {
		t.Errorf("unknown arr tag ids became %d view tags", n)
	}
	// The arr's own record keeps every label as it was; tags_copied is what was copied.
	if l := one[string](t, st, `SELECT tags FROM arr_title WHERE arr_id = 1`); !strings.Contains(l, `"1 - alice"`) ||
		!strings.Contains(l, `"best-available"`) || !strings.Contains(l, `"local"`) {
		t.Errorf("arr_title tags %s", l)
	}
	if l := one[string](t, st, `SELECT tags_copied FROM arr_title WHERE arr_id = 1`); l != `["1 - alice","best-available","local"]` {
		t.Errorf("arr_title tags_copied %s", l)
	}

	// A view tag set by hand stays, and a second run with the arr's tags unchanged writes nothing.
	if _, err := st.Writer().Exec(`INSERT INTO arrview_tag (id, label, created_at) VALUES (60, 'manual', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().Exec(`INSERT INTO arrview_title_tag (version, title_id, tag_id) SELECT 'movie-remux', id, 60
		FROM title WHERE tmdb_id = 100`); err != nil {
		t.Fatal(err)
	}
	if sum := runImport(t, im, src, srv); sum.Changes.Total() != 0 {
		t.Errorf("second run changed %s", sum.Changes)
	}
	if got := viewTags(100, "movie-remux"); got != "1-alice,best-available,local,manual" {
		t.Errorf("Alpha's view tags after a re-run: %s", got)
	}

	// A view takes best-available off Alpha (an upgrade script after an upgrade): the next run does not put it back
	// while the arr still has it.
	if _, err := st.Writer().Exec(`DELETE FROM arrview_title_tag WHERE version = 'movie-remux' AND tag_id =
		(SELECT id FROM arrview_tag WHERE label = 'best-available') AND title_id = (SELECT id FROM title WHERE tmdb_id = 100)`); err != nil {
		t.Fatal(err)
	}
	if sum := runImport(t, im, src, srv); sum.Changes.Total() != 0 {
		t.Errorf("run after a view removal changed %s", sum.Changes)
	}
	if got := viewTags(100, "movie-remux"); got != "1-alice,local,manual" {
		t.Errorf("Alpha's tags after a view removal: %s", got)
	}

	// The arr adds debrid-upgrade to Alpha: it is copied once; best-available stays off.
	fake.movies[0]["tags"] = []int{1, 2, 4, 6}
	runImport(t, im, src, srv)
	if got := viewTags(100, "movie-remux"); got != "1-alice,debrid-upgrade,local,manual" {
		t.Errorf("Alpha's source tags after an arr add: %s", got)
	}
	if sum := runImport(t, im, src, srv); sum.Changes.Total() != 0 {
		t.Errorf("run after the arr add changed %s", sum.Changes)
	}
}

// A version that gets a title in a later run (a title new on the arr) takes every current label of it once; versions
// that had the title keep the edge trigger.
func TestImportTagsToNewVersions(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	fake := newFake("/mnt/movies-a")
	fake.tags = []map[string]any{{"id": 1, "label": "best-available"}, {"id": 2, "label": "1 - alice"}}
	fake.movies[0]["tags"] = []int{1, 2} // Alpha
	srv := httptest.NewServer(fake)
	defer srv.Close()
	im := &Importer{Store: st, Now: fixedNow}
	src := Source{Instance: "radarr_a", Flavour: "radarr", Version: "movie-remux", Library: "/mnt/movies-a"}
	runImport(t, im, src, srv)
	viewTags := func(tmdb int, version string) string {
		return one[string](t, st, `SELECT COALESCE(group_concat(label, ','), '') FROM (SELECT g.label FROM arrview_title_tag tt
			JOIN arrview_tag g ON g.id = tt.tag_id JOIN title ti ON ti.id = tt.title_id WHERE ti.tmdb_id = ? AND tt.version = ?
			ORDER BY g.label)`, tmdb, version)
	}
	// A view took best-available off the source version.
	if _, err := st.Writer().Exec(`DELETE FROM arrview_title_tag WHERE version = 'movie-remux' AND tag_id =
		(SELECT id FROM arrview_tag WHERE label = 'best-available')`); err != nil {
		t.Fatal(err)
	}
	runImport(t, im, src, srv)
	if got := viewTags(100, "movie-remux"); got != "1-alice" {
		t.Errorf("source tags after the next run %q", got)
	}

	// A title new on the arr, with labels another title already had: the version takes them.
	fake.movies = append(fake.movies, map[string]any{"id": 5, "title": "Epsilon", "sortTitle": "epsilon", "year": 2005,
		"tmdbId": 500, "monitored": true, "hasFile": false, "qualityProfileId": 7, "minimumAvailability": "released",
		"status": "released", "path": "/mnt/movies-a/Epsilon", "rootFolderPath": "/mnt/movies-a", "tags": []int{1, 2},
		"added": "2024-01-02T03:04:05Z"})
	runImport(t, im, src, srv)
	if got := viewTags(500, "movie-remux"); got != "1-alice,best-available" {
		t.Errorf("new title's tags %q", got)
	}
}
