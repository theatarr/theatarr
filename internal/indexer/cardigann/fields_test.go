package cardigann

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

const trackerFieldsDef = `---
id: trackerfields
name: Tracker Fields
type: private
links:
  - https://trackerfields.test/
caps:
  categorymappings:
    - {id: 1, cat: Movies, desc: "Movies"}
  modes:
    search: [q]
search:
  paths:
    - path: api
      response:
        type: json
  inputs:
    q: "{{ .Keywords }}"
  rows:
    selector: results
  fields:
    title:
      selector: name
    infohash:
      selector: hash
    downloadvolumefactor:
      selector: dl
    uploadvolumefactor:
      selector: ul
    minimumratio:
      selector: ratio
      optional: true
    minimumseedtime:
      selector: seedtime
      optional: true
    grabs:
      selector: grabs
      optional: true
    description:
      selector: desc
    tmdbid:
      selector: tmdb
    tvdbid:
      selector: tvdb
`

// The tracker fields Prowlarr's parser reads reach the Result: volume factors, ratio and seed time, grabs, TMDb and
// TVDB, and the Internal flag from the description. An unreadable factor is left out rather than read as 0.
func TestResultTrackerFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"results":[
			{"name":"Movie.2024.1080p","hash":"1111111111111111111111111111111111111111","dl":0,"ul":2,"ratio":"0.8",
			 "seedtime":"172800","grabs":"1,234","desc":"Internal release","tmdb":"https://www.themoviedb.org/movie/603","tvdb":"81189"},
			{"name":"Other.2024.720p","hash":"2222222222222222222222222222222222222222","dl":"n/a","ul":"1","desc":"Scene"}
		]}`)
	}))
	defer srv.Close()
	m := NewManager(memDefs{"trackerfields": []byte(trackerFieldsDef)}, nil)
	m.DefaultDelay = 0
	inst := Instance{Key: "tf", Transport: direct, DefinitionID: "trackerfields", BaseURL: srv.URL}
	res, err := m.Search(context.Background(), inst, "movie", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("results %+v", res)
	}
	r := res[0]
	want := map[string]string{"downloadvolumefactor": "0", "uploadvolumefactor": "2", "minimumratio": "0.8",
		"minimumseedtime": "172800", "grabs": "1234"}
	for k, v := range want {
		if r.Extras[k] != v {
			t.Errorf("%s = %q, want %q (%v)", k, r.Extras[k], v, r.Extras)
		}
	}
	if r.TMDb != 603 || r.TVDB != 81189 || !slices.Equal(r.Flags, []string{"internal"}) {
		t.Errorf("ids and flags: tmdb %d tvdb %d flags %v", r.TMDb, r.TVDB, r.Flags)
	}
	o := res[1]
	if _, ok := o.Extras["downloadvolumefactor"]; ok || o.Extras["uploadvolumefactor"] != "1" || len(o.Flags) != 0 ||
		o.TMDb != 0 {
		t.Errorf("second result: extras %v flags %v tmdb %d", o.Extras, o.Flags, o.TMDb)
	}
}
