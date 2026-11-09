package indexer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// What the sources need beyond their raw answers: the DMM title clean-up on every feed, the
// TMDb episode counts for Torrentio, and seadexerr's shapes, scoring table, flags and AniList mapping.

func TestTorznabStripsDMMSuffixOnEveryFeed(t *testing.T) {
	loadFixtures(t, "prowlarr_caps.xml")
	n := newNet(t)
	serveProwlarr(n, &recorder{}, "prowlarr_caps.xml", func(r *http.Request) string {
		return feedOf(0, 2, 2, " {imdb-tt0133093}")
	})
	h := newHarness(t, n, testPolicy(), false)
	res := search(t, h, "prowlarr", Query{Type: QueryText, Title: "release"})
	if len(res.Releases) != 2 {
		t.Fatalf("%d releases", len(res.Releases))
	}
	for _, r := range res.Releases {
		if strings.Contains(r.Title, "{imdb") || r.IMDb != "tt0133093" || r.Extras["cached_on"] != "" {
			t.Errorf("release %q imdb %q extras %v", r.Title, r.IMDb, r.Extras)
		}
	}
}

func serveSeadex(t *testing.T, n *testNet, rec *recorder, byID map[string]string) {
	t.Helper()
	n.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		f, ok := byID[strings.TrimPrefix(r.URL.Query().Get("filter"), "alID=")]
		if !ok {
			fmt.Fprint(w, `{"page":1,"perPage":100,"totalItems":0,"totalPages":0,"items":[]}`)
			return
		}
		w.Write(fixtureBytes(f))
	}))
}

func titles(rs []Release) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Title)
	}
	return out
}

func preferredOf(rs []Release) []string {
	var out []string
	for _, r := range rs {
		if slices.Contains(r.Flags, FlagSeaDexPreferred) {
			out = append(out, r.Extras["seadex_id"])
		}
	}
	return out
}

// A season search keeps the multi-file entries only, prefers the best one by default, and every entry carries the
// rare flag combination of seadexerr.
func TestSeadexSeasonShapeAndDefaultScoring(t *testing.T) {
	loadFixtures(t, "seadex_scoring.json")
	n := newNet(t)
	serveSeadex(t, n, &recorder{}, map[string]string{"500": "seadex_scoring.json"})
	h := newHarness(t, n, testPolicy(), false)
	res := search(t, h, "seadex", Query{Type: QuerySeries, AniList: 500, Title: "Show", Season: 1, Anime: true})
	// one1 has one feature (not a pack); deb1 has two features and no exclusion by default.
	ids := []string{}
	for _, r := range res.Releases {
		ids = append(ids, r.Extras["seadex_id"])
	}
	if !slices.Equal(ids, []string{"best1", "alt1", "deb1"}) {
		t.Fatalf("kept %v", ids)
	}
	if p := preferredOf(res.Releases); !slices.Equal(p, []string{"best1"}) {
		t.Errorf("preferred %v, want the best entry", p)
	}
	for _, r := range res.Releases {
		want := 10
		if r.Extras["seadex_id"] == "best1" {
			want = 1000
		}
		if r.Seeders != want || !slices.Contains(r.Flags, "internal") || !slices.Contains(r.Flags, "scene") ||
			r.Extras["downloadvolumefactor"] != "0.75" || r.Extras["uploadvolumefactor"] != "2" {
			t.Errorf("%s: seeders %d flags %v extras %v", r.Extras["seadex_id"], r.Seeders, r.Flags, r.Extras)
		}
	}
	// The mapped title and season retain the feature files' actual source and resolution.
	if got := titles(res.Releases)[:2]; got[0] != "Show S01 Bluray 1080p (best1)" ||
		got[1] != "Show S01 WEBDL 1080p (alt1)" {
		t.Errorf("titles %q", got)
	}
	if !slices.ContainsFunc(res.Notes, func(s string) bool { return strings.Contains(s, "dropped 1 single-file") }) {
		t.Errorf("notes %v", res.Notes)
	}
}

// The scoring table (seadexerr's scoring.toml) excludes tags and moves the preference.
func TestSeadexScoringOverrides(t *testing.T) {
	loadFixtures(t, "seadex_scoring.json")
	n := newNet(t)
	serveSeadex(t, n, &recorder{}, map[string]string{"500": "seadex_scoring.json"})
	p := testPolicy()
	p.Indexers["seadex"].SeaDex = &policy.SeaDexScoring{DualAudio: 50, Tags: map[string]int{"HDR": 60},
		ExcludeTags: []string{"Deband Required"}}
	h := newHarness(t, n, p, false)
	res := search(t, h, "seadex", Query{Type: QuerySeries, AniList: 500, Title: "Show", Season: 1})
	if len(res.Releases) != 2 {
		t.Fatalf("kept %v", titles(res.Releases))
	}
	// alt1: dual audio 50 + HDR 60 = 110 beats best1's 100.
	if pr := preferredOf(res.Releases); !slices.Equal(pr, []string{"alt1"}) {
		t.Errorf("preferred %v", pr)
	}
	if !slices.ContainsFunc(res.Notes, func(s string) bool { return strings.Contains(s, "excluded tag") }) {
		t.Errorf("notes %v", res.Notes)
	}
}

// Ports of seadexerr's scoring tests (src/scoring.rs).
func TestSeadexPriorities(t *testing.T) {
	tor := func(best bool) *sdTorrent { return &sdTorrent{IsBest: best} }
	for name, c := range map[string]struct {
		sc    *policy.SeaDexScoring
		ts    []*sdTorrent
		sizes []int64
		want  []bool
	}{
		"best weight wins":         {nil, []*sdTorrent{tor(true), tor(false)}, []int64{100, 50}, []bool{true, false}},
		"size weight favors small": {&policy.SeaDexScoring{SizeWeight: -30}, []*sdTorrent{tor(false), tor(false)}, []int64{1000, 100}, []bool{false, true}},
		"size weight favors large": {&policy.SeaDexScoring{SizeWeight: 30}, []*sdTorrent{tor(false), tor(false)}, []int64{1000, 100}, []bool{true, false}},
		"ties all win":             {nil, []*sdTorrent{tor(true), tor(true)}, []int64{100, 100}, []bool{true, true}},
		"no best: all tie":         {nil, []*sdTorrent{tor(false), tor(false)}, []int64{1, 2}, []bool{true, true}},
		"best zero":                {&policy.SeaDexScoring{Best: new(int)}, []*sdTorrent{tor(true), tor(false)}, []int64{5, 5}, []bool{true, true}},
		"empty":                    {nil, nil, nil, []bool{}},
	} {
		if got := seadexPriorities(c.sc, c.ts, c.sizes); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

// A movie search keeps single-feature entries only.
func TestSeadexMovieShape(t *testing.T) {
	loadFixtures(t, "seadex_scoring.json")
	n := newNet(t)
	serveSeadex(t, n, &recorder{}, map[string]string{"500": "seadex_scoring.json"})
	h := newHarness(t, n, testPolicy(), false)
	res := search(t, h, "seadex", Query{Type: QueryMovie, AniList: 500, Title: "Show OVA"})
	if len(res.Releases) != 1 || res.Releases[0].Extras["seadex_id"] != "one1" || res.Releases[0].Title != "Show OVA Bluray 1080p (one1)" {
		t.Fatalf("kept %v", titles(res.Releases))
	}
	if pr := preferredOf(res.Releases); !slices.Equal(pr, []string{"one1"}) {
		t.Errorf("preferred %v", pr)
	}
}

// Without an AniList id the resolver maps the TVDB season (or the movie) to ids, one request each.
func TestSeadexResolver(t *testing.T) {
	loadFixtures(t, "seadex_scoring.json", "seadex_entry.json")
	n := newNet(t)
	rec := &recorder{}
	serveSeadex(t, n, rec, map[string]string{"500": "seadex_scoring.json", "154587": "seadex_entry.json"})
	h := newHarness(t, n, testPolicy(), false)
	q := Query{Type: QuerySeries, TVDB: 1234, Season: 1, Title: "Show"}
	if h.svc.Searcher("seadex").Supports(q) {
		t.Fatal("supports a TVDB query without a resolver")
	}
	var mu sync.Mutex
	var asked []Query
	h.svc.SetMetadata(Metadata{AniList: AniListFunc(func(_ context.Context, q Query) ([]int, error) {
		mu.Lock()
		asked = append(asked, q)
		mu.Unlock()
		if q.TVDB == 1234 && q.Season == 1 {
			return []int{500, 154587}, nil
		}
		return nil, nil
	})})
	res := search(t, h, "seadex", q)
	if len(rec.all()) != 2 || res.Requests != 2 {
		t.Fatalf("%d requests", len(rec.all()))
	}
	if len(res.Releases) != 4 { // best1, alt1, deb1 and Frieren's pack
		t.Errorf("releases %v", titles(res.Releases))
	}
	// Best on both ids: both preferred (one table over the whole search).
	if pr := preferredOf(res.Releases); !slices.Equal(pr, []string{"best1", "abc123def456ghi"}) {
		t.Errorf("preferred %v", pr)
	}
	// No mapping: an empty answer with a note, no request.
	res = search(t, h, "seadex", Query{Type: QuerySeries, TVDB: 1234, Season: 2})
	if len(res.Releases) != 0 || len(rec.all()) != 2 || len(res.Notes) == 0 {
		t.Errorf("unmapped season: %d releases, %d requests, notes %v", len(res.Releases), len(rec.all()), res.Notes)
	}
	if !h.svc.Searcher("seadex").Supports(Query{Type: QueryMovie, TMDb: 149}) {
		t.Error("a movie by TMDb id is not supported with a resolver")
	}
}

// Torrentio asks the episode counter when the query has no counts (a TMDb lookup), and keeps the file
// size with a note when the counter fails.
func TestTorrentioEpisodeCounter(t *testing.T) {
	loadFixtures(t, "torrentio_series.json")
	n := newNet(t)
	n.serve("torrentio.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixtureBytes("torrentio_series.json"))
	}))
	h := newHarness(t, n, testPolicy(), false)
	calls := 0
	fail := false
	h.svc.SetMetadata(Metadata{Episodes: EpisodeCountsFunc(func(_ context.Context, q Query) (map[int]int, error) {
		calls++
		if fail {
			return nil, errors.New("metadata down")
		}
		if q.IMDb != "tt0000001" {
			t.Errorf("asked for %+v", q)
		}
		return map[int]int{0: 3, 1: 8, 2: 10, 3: 6}, nil
	})})
	res := search(t, h, "torrentio", Query{Type: QuerySeries, IMDb: "tt0000001", Season: 2})
	if calls != 1 || res.Releases[0].Size != 10*4<<30 || res.Releases[0].Extras["size_scaled_by"] != "10" {
		t.Fatalf("calls %d first %+v", calls, res.Releases[0])
	}
	// Counts given by the caller win: no lookup.
	search(t, h, "torrentio", Query{Type: QuerySeries, IMDb: "tt0000001", Season: 3, EpisodeCounts: map[int]int{3: 6}})
	if calls != 1 {
		t.Errorf("looked up although the query had counts")
	}
	fail = true
	res = search(t, h, "torrentio", Query{Type: QuerySeries, IMDb: "tt0000001", Season: 1})
	if res.Releases[0].Extras["size_is_file"] != "true" ||
		!slices.ContainsFunc(res.Notes, func(s string) bool { return strings.Contains(s, "metadata down") }) {
		t.Errorf("failed lookup: %+v notes %v", res.Releases[0], res.Notes)
	}
}

func TestFirstLookups(t *testing.T) {
	ctx := context.Background()
	none := AniListFunc(func(context.Context, Query) ([]int, error) { return nil, nil })
	bad := AniListFunc(func(context.Context, Query) ([]int, error) { return nil, errors.New("down") })
	some := AniListFunc(func(context.Context, Query) ([]int, error) { return []int{7}, nil })
	if ids, err := FirstAniList(nil, none, bad, some).AniListIDs(ctx, Query{}); err != nil || !slices.Equal(ids, []int{7}) {
		t.Errorf("ids %v err %v", ids, err)
	}
	if _, err := FirstAniList(none, bad).AniListIDs(ctx, Query{}); err == nil {
		t.Error("an error was swallowed when nothing answered")
	}
	c := FirstEpisodeCounts(EpisodeCountsFunc(func(context.Context, Query) (map[int]int, error) { return nil, nil }),
		EpisodeCountsFunc(func(context.Context, Query) (map[int]int, error) { return map[int]int{1: 2}, nil }))
	if m, err := c.EpisodeCounts(ctx, Query{}); err != nil || m[1] != 2 {
		t.Errorf("counts %v err %v", m, err)
	}
}
