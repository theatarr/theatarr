package shadow

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
// picked with its episodes. The unmapped entry is dropped by the indexer.
func TestRunnerSeaDexRSS(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	newShow(e.tv)
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
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
	if rs.Matched != 1 || rs.Picks != 1 {
		t.Fatalf("rss %+v", rs)
	}
	got := one[string](t, e.st, `SELECT indexer || ' | ' || release_title || ' | ' || version_key || ' | ' || quality || ' | ' ||
		json_array_length(episodes) FROM shadow_rss_pick WHERE info_hash = ?`, "5555555555555555555555555555555555555555")
	if want := "seadex | The Show S01 Bluray 1080p (sdx0000000001ab) | tv | Bluray-1080p | 3"; got != want {
		t.Errorf("pick %q, want %q", got, want)
	}
	if !strings.Contains(e.logBuf.String(), "shadow rss pick") {
		t.Error("no pick logged")
	}
}

// lookupTargets is a fixed reverse AniList mapping (the anibridge list's, in production).
type lookupTargets map[int][]indexer.AniListTarget

func (l lookupTargets) AniListTargets(_ context.Context, al int) ([]indexer.AniListTarget, error) {
	return l[al], nil
}
