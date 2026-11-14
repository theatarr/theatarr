package indexer

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide/parser/radarr"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
)

// fakeNames names the titles we hold: JUJUTSU KAISEN (TVDB 377543) and Akira (TMDb 149).
type fakeNames struct{}

func (fakeNames) TitleName(_ context.Context, q Query) (string, int, bool, error) {
	switch {
	case q.Type == QuerySeries && q.TVDB == 377543:
		return "JUJUTSU KAISEN", 2020, true, nil
	case q.Type == QueryMovie && q.TMDb == 149:
		return "Akira", 1988, true, nil
	}
	return "", 0, false, nil
}

// fakeTargets is the reverse mapping: JJK's AniList entry maps into seasons 1 and 2 of 377543 (season 0 too), Akira's
// to its movie; 999 maps to nothing.
var fakeTargets = aniListTargetsFunc(func(_ context.Context, al int) ([]AniListTarget, error) {
	switch al {
	case 113415:
		return []AniListTarget{{TVDB: 377543, Season: 2}, {TVDB: 377543, Season: 0}, {TVDB: 377543, Season: 1}}, nil
	case 47:
		return []AniListTarget{{TMDb: 149}}, nil
	}
	return nil, nil
})

// The entry seadexerr serves for TVDB 377543 season 1: the mapped title of
// JUJUTSU KAISEN season 1 with the files' actual Bluray-1080p quality. The name comes from the query,
// else from our titles (seadexerr asks Sonarr).
func TestSeadexMappedTitleAndFeatureQuality(t *testing.T) {
	loadFixtures(t, "seadex_jjk.json")
	n := newNet(t)
	serveSeadex(t, n, &recorder{}, map[string]string{"113415": "seadex_jjk.json"})
	h := newHarness(t, n, testPolicy(), false)
	h.svc.SetMetadata(Metadata{AniList: AniListFunc(func(_ context.Context, q Query) ([]int, error) {
		if q.TVDB == 377543 && q.Season == 1 {
			return []int{113415}, nil
		}
		return nil, nil
	}), Names: fakeNames{}})
	const want = "JUJUTSU KAISEN S01 Bluray 1080p (mtf4qfn237dgt3x)"
	for _, q := range []Query{
		{Type: QuerySeries, TVDB: 377543, Season: 1},                          // no title: our titles name it
		{Type: QuerySeries, TVDB: 377543, Season: 1, Title: "JUJUTSU KAISEN"}, // the router's query
	} {
		res := search(t, h, "seadex", q)
		if len(res.Releases) != 1 {
			t.Fatalf("%+v: %d releases", q, len(res.Releases))
		}
		r := res.Releases[0]
		if r.Title != want || r.Size != 70296791677 || r.Seeders != 1000 || r.Extras["seadex_id"] != "mtf4qfn237dgt3x" ||
			r.Extras["downloadvolumefactor"] != "0.75" || r.Extras["uploadvolumefactor"] != "2" ||
			!slices.Contains(r.Flags, "scene") || !slices.Contains(r.Flags, "internal") {
			t.Errorf("%+v: %+v", q, r)
		}
	}
	p := sonarr.ParseTitle(want)
	if p == nil || p.SeriesTitle != "JUJUTSU KAISEN" || p.SeasonNumber != 1 || !p.FullSeason ||
		p.Quality.Quality.Name != "Bluray-1080p" {
		t.Errorf("sonarr parse %+v", p)
	}
	if m := radarr.ParseMovieTitle("Akira (1988) Bluray 1080p Remux (mov1)", false); m == nil ||
		m.Quality.Quality.Name != "Remux-1080p" || m.Year != 1988 {
		t.Errorf("radarr parse %+v", m)
	}
	// A title we hold no name for keeps the AniList id as its name.
	h.svc.SetMetadata(Metadata{AniList: AniListFunc(func(context.Context, Query) ([]int, error) { return []int{113415}, nil })})
	res := search(t, h, "seadex", Query{Type: QuerySeries, TVDB: 1, Season: 3})
	if len(res.Releases) != 1 || res.Releases[0].Title != "AniList 113415 S03 Bluray 1080p (mtf4qfn237dgt3x)" {
		t.Errorf("unnamed: %v", titles(res.Releases))
	}
}

// The recent feed is filed under our titles as seadexerr's generic search does: a pack under the earliest regular
// season its AniList id maps into, a single feature under its movie; the rest is dropped with a note.
func TestSeadexRSSFiledUnderTitles(t *testing.T) {
	loadFixtures(t, "seadex_recent.json")
	n := newNet(t)
	n.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("sort") != "-updated" {
			fmt.Fprint(w, `{"items":[]}`)
			return
		}
		w.Write(fixtureBytes("seadex_recent.json"))
	}))
	h := newHarness(t, n, testPolicy(), false)

	// Without the lookups nothing can be filed.
	res := search(t, h, "seadex", Query{Type: QueryRSS})
	if len(res.Releases) != 0 || !slices.ContainsFunc(res.Notes, func(s string) bool { return strings.Contains(s, "dropped 3 recent") }) {
		t.Errorf("no lookups: %v, notes %v", titles(res.Releases), res.Notes)
	}

	h = newHarness(t, n, testPolicy(), false)
	h.svc.SetMetadata(Metadata{Targets: fakeTargets, Names: fakeNames{}})
	res = search(t, h, "seadex", Query{Type: QueryRSS})
	got := map[string]Release{}
	for _, r := range res.Releases {
		got[r.Extras["seadex_id"]] = r
	}
	if len(res.Releases) != 2 {
		t.Fatalf("releases %v", titles(res.Releases))
	}
	if r := got["mtf4qfn237dgt3x"]; r.Title != "JUJUTSU KAISEN S01 Bluray 1080p (mtf4qfn237dgt3x)" || r.TVDB != 377543 ||
		r.TMDb != 0 || fmt.Sprint(r.Categories) != "[5000 5070]" || r.Extras["anilist"] != "113415" || r.Seeders != 1000 {
		t.Errorf("pack: %+v", r)
	}
	if r := got["mov1"]; r.Title != "Akira (1988) Bluray 1080p Remux (mov1)" || r.TMDb != 149 || r.TVDB != 0 ||
		fmt.Sprint(r.Categories) != "[2000]" || r.Seeders != 10 {
		t.Errorf("movie: %+v", r)
	}
	if !slices.ContainsFunc(res.Notes, func(s string) bool { return strings.Contains(s, "dropped 1 recent") }) {
		t.Errorf("notes %v", res.Notes)
	}
}

// A whole-season batch SeaDex lists under each cour of the season (Dr. STONE S04: one hash under three AniList
// entries) is returned once, carrying every entry it is listed under; a hash under one entry carries none.
func TestSeadexBatchCarriesEveryEntry(t *testing.T) {
	const batch = "5bf61ad8aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	files := make([]string, 37)
	for i := range files {
		files[i] = fmt.Sprintf(`{"length":3000000000,"name":"[sam] Dr. STONE - Science Future - %02d (BD 1080p).mkv"}`, i+1)
	}
	tor := func(id, hash string) string {
		return `{"id":"` + id + `","url":"https://nyaa.si/view/1","infoHash":"` + hash + `","isBest":true,"tags":[],` +
			`"tracker":"Nyaa","releaseGroup":"sam","files":[` + strings.Join(files, ",") + `]}`
	}
	entry := func(al string, tors ...[2]string) string {
		var ids, exp []string
		for _, x := range tors {
			ids = append(ids, `"`+x[0]+`"`)
			exp = append(exp, tor(x[0], x[1]))
		}
		return `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[{"alID":` + al + `,"trs":[` +
			strings.Join(ids, ",") + `],"expand":{"trs":[` + strings.Join(exp, ",") + `]}}]}`
	}
	answers := map[string]string{
		"172019": entry("172019", [2]string{"sam1", batch}),
		"189117": entry("189117", [2]string{"sam1", batch}, [2]string{"cour2", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}),
		"199221": entry("199221", [2]string{"sam1", batch}),
	}
	n := newNet(t)
	n.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, answers[strings.TrimPrefix(r.URL.Query().Get("filter"), "alID=")])
	}))
	h := newHarness(t, n, testPolicy(), false)
	h.svc.SetMetadata(Metadata{AniList: AniListFunc(func(context.Context, Query) ([]int, error) {
		return []int{172019, 189117, 199221}, nil
	})})
	res := search(t, h, "seadex", Query{Type: QuerySeries, TVDB: 3003, Season: 4, Title: "Dr. STONE"})
	if len(res.Releases) != 2 {
		t.Fatalf("releases %v", titles(res.Releases))
	}
	for _, r := range res.Releases {
		want := ""
		if r.InfoHash == batch {
			want = "172019,189117,199221"
		}
		if r.Extras[ExtraSeaDexEntries] != want || r.Extras["anilist"] == "" {
			t.Errorf("%s: extras %v", r.InfoHash, r.Extras)
		}
	}
}
