package arrview

import (
	"slices"
	"strconv"
	"testing"
)

// Tags are per version: a tag set through one per-version view is not seen through another view of the same title,
// and a view over several versions shows the union of its versions' tags (no duplicates) and writes to all of them.
func TestTagsPerVersion(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var tag map[string]any
	e.call("POST", "/radarr/api/v3/tag", map[string]any{"label": "debrid-upgrade"}, 201, &tag)
	tid := tag["id"].(float64)
	movieTags := func(base string) []float64 {
		t.Helper()
		var m map[string]any
		e.call("GET", base+"/api/v3/movie/"+strconv.Itoa(IDBase+1), nil, 200, &m)
		var out []float64
		for _, x := range m["tags"].([]any) {
			out = append(out, x.(float64))
		}
		return out
	}
	detailIDs := func(base string) []any {
		t.Helper()
		var d map[string]any
		e.call("GET", base+"/api/v3/tag/detail/"+strconv.Itoa(int(tid)), nil, 200, &d)
		return d["movieIds"].([]any)
	}

	// Through /v/movie-web only: Alpha's web version carries it, its remux version does not.
	e.call("PUT", "/v/movie-web/radarr/api/v3/movie/editor", map[string]any{"movieIds": []int{IDBase + 1},
		"tags": []float64{tid}, "applyTags": "add"}, 202, nil)
	if got := movieTags("/v/movie-web/radarr"); !slices.Equal(got, []float64{tid}) {
		t.Errorf("web view tags %v", got)
	}
	if got := movieTags("/v/movie-remux/radarr"); len(got) != 0 {
		t.Errorf("remux view sees the web view's tag: %v", got)
	}
	if ids := detailIDs("/v/movie-remux/radarr"); len(ids) != 0 {
		t.Errorf("remux tag/detail lists %v", ids)
	}
	if ids := detailIDs("/v/movie-web/radarr"); len(ids) != 1 {
		t.Errorf("web tag/detail lists %v", ids)
	}

	// /radarr serves both versions: it shows the union, once.
	if got := movieTags("/radarr"); !slices.Equal(got, []float64{tid}) {
		t.Errorf("union tags %v", got)
	}
	// A write through /radarr reaches both versions; reading it back shows the tag once.
	e.call("PUT", "/radarr/api/v3/movie/editor", map[string]any{"movieIds": []int{IDBase + 1},
		"tags": []float64{tid}, "applyTags": "add"}, 202, nil)
	if got := movieTags("/v/movie-remux/radarr"); !slices.Equal(got, []float64{tid}) {
		t.Errorf("remux view after a /radarr write: %v", got)
	}
	if got := movieTags("/radarr"); !slices.Equal(got, []float64{tid}) {
		t.Errorf("union tags after both: %v", got)
	}

	// A remove through one per-version view leaves the other version's tag.
	e.call("PUT", "/v/movie-remux/radarr/api/v3/movie/editor", map[string]any{"movieIds": []int{IDBase + 1},
		"tags": []float64{tid}, "applyTags": "remove"}, 202, nil)
	if got := movieTags("/v/movie-web/radarr"); !slices.Equal(got, []float64{tid}) {
		t.Errorf("web view after a remux remove: %v", got)
	}
	if got := movieTags("/v/movie-remux/radarr"); len(got) != 0 {
		t.Errorf("remux view after its remove: %v", got)
	}

	// replace through one view replaces only within its versions.
	var other map[string]any
	e.call("POST", "/radarr/api/v3/tag", map[string]any{"label": "skip-upgrade-check"}, 201, &other)
	oid := other["id"].(float64)
	e.call("PUT", "/v/movie-remux/radarr/api/v3/movie/editor", map[string]any{"movieIds": []int{IDBase + 1},
		"tags": []float64{oid}, "applyTags": "replace"}, 202, nil)
	if got := movieTags("/v/movie-web/radarr"); !slices.Equal(got, []float64{tid}) {
		t.Errorf("web view after a remux replace: %v", got)
	}
	if got := movieTags("/v/movie-remux/radarr"); !slices.Equal(got, []float64{oid}) {
		t.Errorf("remux view after its replace: %v", got)
	}
	// A PUT movie with tags through the web view replaces the web version's tags only.
	var m map[string]any
	e.call("GET", "/v/movie-web/radarr/api/v3/movie/"+strconv.Itoa(IDBase+1), nil, 200, &m)
	m["tags"] = []float64{}
	e.call("PUT", "/v/movie-web/radarr/api/v3/movie/"+strconv.Itoa(IDBase+1), m, 202, nil)
	if got := movieTags("/v/movie-web/radarr"); len(got) != 0 {
		t.Errorf("web view after PUT movie: %v", got)
	}
	if got := movieTags("/v/movie-remux/radarr"); !slices.Equal(got, []float64{oid}) {
		t.Errorf("remux view after a web PUT movie: %v", got)
	}
}

// A removed version's tags are not the view's: a view over several versions shows and writes only the ones it shows.
func TestTagsSkipRemovedVersions(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var tag, other map[string]any
	e.call("POST", "/radarr/api/v3/tag", map[string]any{"label": "debrid-upgrade"}, 201, &tag)
	e.call("POST", "/radarr/api/v3/tag", map[string]any{"label": "local"}, 201, &other)
	tid, oid := tag["id"].(float64), other["id"].(float64)
	e.call("PUT", "/v/movie-web/radarr/api/v3/movie/editor", map[string]any{"movieIds": []int{IDBase + 1},
		"tags": []float64{tid}, "applyTags": "add"}, 202, nil)
	e.start()
	e.call("DELETE", "/v/movie-web/radarr/api/v3/movie/"+strconv.Itoa(IDBase+1), nil, 200, nil)

	var m map[string]any
	e.call("GET", "/radarr/api/v3/movie/"+strconv.Itoa(IDBase+1), nil, 200, &m)
	if tags := m["tags"].([]any); len(tags) != 0 {
		t.Errorf("/radarr shows the removed web version's tags %v", tags)
	}
	var d map[string]any
	e.call("GET", "/radarr/api/v3/tag/detail/"+strconv.Itoa(int(tid)), nil, 200, &d)
	if ids := d["movieIds"].([]any); len(ids) != 0 {
		t.Errorf("tag/detail lists %v", ids)
	}
	// A replace through /radarr changes the remux version only.
	e.call("PUT", "/radarr/api/v3/movie/editor", map[string]any{"movieIds": []int{IDBase + 1},
		"tags": []float64{oid}, "applyTags": "replace"}, 202, nil)
	got := e.str(`SELECT group_concat(version || ':' || tag_id, ',') FROM (SELECT version, tag_id FROM arrview_title_tag
		WHERE title_id = 1 ORDER BY version, tag_id)`)
	if want := "movie-remux:" + strconv.Itoa(int(oid)) + ",movie-web:" + strconv.Itoa(int(tid)); got != want {
		t.Errorf("assignments %s, want %s", got, want)
	}
	// The tag held only by the removed version is free to delete.
	e.call("DELETE", "/radarr/api/v3/tag/"+strconv.Itoa(int(tid)), nil, 200, nil)
}
