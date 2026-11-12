package rss

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/indexer/lookup"
	"github.com/theatarr/theatarr/internal/policy"
)

// seadexRecent is SeaDex's recent feed: a season pack of AniList 5001 (filed under The Show season 1), and an entry
// of an AniList id that maps to nothing.
const seadexRecent = `{"page":1,"perPage":100,"totalItems":2,"totalPages":1,"items":[
 {"alID":5001,"trs":["sdx0000000001ab"],"expand":{"trs":[
  {"id":"sdx0000000001ab","url":"https://nyaa.si/view/501","infoHash":"5555555555555555555555555555555555555555",
   "isBest":true,"dualAudio":false,"tags":[],"tracker":"Nyaa","releaseGroup":"Grp","created":"2026-10-26 11:00:00.000Z",
   "updated":"2026-10-26 11:00:00.000Z","files":[{"length":9000000000,"name":"[Grp] The Show - 01 (BD 1080p).mkv"},
   {"length":9000000000,"name":"[Grp] The Show - 02 (BD 1080p).mkv"},{"length":9000000000,"name":"[Grp] The Show - 03 (BD 1080p).mkv"}]}]}},
 {"alID":6002,"trs":["sdx0000000002cd"],"expand":{"trs":[
  {"id":"sdx0000000002cd","url":"https://nyaa.si/view/502","infoHash":"6666666666666666666666666666666666666666",
   "isBest":true,"dualAudio":false,"tags":[],"tracker":"Nyaa","releaseGroup":"Grp","created":"2026-10-26 11:00:00.000Z",
   "updated":"2026-10-26 11:00:00.000Z","files":[{"length":1000,"name":"a - 01.mkv"},{"length":1000,"name":"a - 02.mkv"}]}]}}
]}`

// A SeaDex recent-feed item is filed under our title by its AniList id (seadexerr's generic search) and the RSS run
// treats it like any other feed's item: matched to The Show by TVDB id, decided for the tv version's season 1 and
// routed with its episodes. The unmapped entry is dropped by the indexer.
func TestRunnerSeaDexRSS(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	e.seed(t)
	e.seedShow(t)
	e.net.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("sort") != "-updated" {
			fmt.Fprint(w, `{"items":[]}`)
			return
		}
		fmt.Fprint(w, seadexRecent)
	}))
	// The tv version lists SeaDex and its profile takes the files' actual Bluray-1080p quality.
	e.pol.Versions["tv"].Indexers = append(e.pol.Versions["tv"].Indexers, "seadex")
	tvp := e.pol.Profiles["tv-2160p"]
	tvp.Qualities = append(tvp.Qualities, policy.QualityItem{Name: "Bluray-1080p"})
	// As a typical anime profile: Blu-ray encodes have no maximum size.
	tvp.Sizes = map[string]policy.SizeLimit{"Bluray-1080p": {Min: 4}}
	eng, err := decide.New(e.pol, decide.Options{Now: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	e.eng = eng
	db := lookup.DB{DB: e.st.Reader()}
	e.svc.SetMetadata(indexer.Metadata{Names: db, Targets: indexer.FirstAniListTargets(db,
		lookupTargets{5001: {{TVDB: 1001, Season: 1}}})})
	e.build(nil)
	e.prow.setRSS()

	rs, err := e.r.RSS(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var sd *RSSIndexer
	for i := range rs.Indexers {
		if rs.Indexers[i].Indexer == "seadex" {
			sd = &rs.Indexers[i]
		}
	}
	if sd == nil || sd.Items != 1 || sd.New != 1 {
		t.Fatalf("seadex feed %+v (all %+v)", sd, rs.Indexers)
	}
	if rs.Matched != 1 || rs.Routed != 1 || len(e.rec.reqs) != 1 {
		t.Fatalf("rss %+v", rs)
	}
	c := e.rec.reqs[0].Candidates[0]
	got := fmt.Sprintf("%s | %s | %s | %s | %d", c.Release.Indexer, c.Release.Title, e.rec.reqs[0].Version, c.Quality,
		len(c.Episodes))
	if want := "seadex | The Show S01 Bluray 1080p (sdx0000000001ab) | tv | Bluray-1080p | 3"; got != want {
		t.Errorf("routed %q, want %q", got, want)
	}
	if !strings.Contains(e.logBuf.String(), "rss: routed") {
		t.Error("no route logged")
	}
}

// lookupTargets is a fixed reverse AniList mapping (the anibridge list's, in production).
type lookupTargets map[int][]indexer.AniListTarget

func (l lookupTargets) AniListTargets(_ context.Context, al int) ([]indexer.AniListTarget, error) {
	return l[al], nil
}

// seadexRSSEnv is the RSS environment with the SeaDex feed ready: its recent feed is empty, every entry answers empty.
func seadexRSSEnv(t *testing.T, prefer bool, entry http.HandlerFunc) *env {
	e := newEnv(t, false)
	e.seed(t)
	e.net.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if entry != nil && r.URL.Query().Get("sort") != "-updated" {
			entry(w, r)
			return
		}
		fmt.Fprint(w, `{"page":1,"perPage":100,"totalItems":0,"totalPages":0,"items":[]}`)
	}))
	e.pol.Versions["tv"].Indexers = append(e.pol.Versions["tv"].Indexers, "seadex")
	e.pol.Versions["tv"].PreferSeaDex = prefer
	e.build(nil)
	e.prow.setRSS()
	return e
}

func seadexInfo(t *testing.T, rs *RSSSummary) RSSIndexer {
	t.Helper()
	for _, ix := range rs.Indexers {
		if ix.Indexer == "seadex" {
			return ix
		}
	}
	t.Fatalf("no seadex feed in %+v", rs.Indexers)
	return RSSIndexer{}
}
