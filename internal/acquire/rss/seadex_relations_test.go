package rss

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/indexer/lookup"
	"github.com/theatarr/theatarr/internal/policy"
)

// Through the RSS sync and its decisions: a recent-feed entry without a relation list (absent or null
// "trs") files nothing under The Show and nothing is picked, for prefer-on and prefer-off versions alike.
func TestRSSMissingRelationsNotPicked(t *testing.T) {
	t.Parallel()
	const hash = "5555555555555555555555555555555555555555"
	for _, relation := range []string{"", `"trs":null,`} {
		feed := `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[
 {"alID":5001,` + relation + `"expand":{"trs":[
  {"id":"sdx0000000001ab","url":"https://nyaa.si/view/501","infoHash":"` + hash + `",
   "isBest":true,"dualAudio":false,"tags":[],"tracker":"Nyaa","releaseGroup":"Grp","created":"2026-10-26 11:00:00.000Z",
   "updated":"2026-10-26 11:00:00.000Z","files":[{"length":9000000000,"name":"[Grp] The Show - 01 (BD 1080p).mkv"},
   {"length":9000000000,"name":"[Grp] The Show - 02 (BD 1080p).mkv"},{"length":9000000000,"name":"[Grp] The Show - 03 (BD 1080p).mkv"}]}]}}
]}`
		for _, prefer := range []bool{false, true} {
			t.Run(fmt.Sprintf("relation=%q/prefer=%v", relation, prefer), func(t *testing.T) {
				e := newEnv(t, false)
				e.seed(t)
				e.seedShow(t)
				e.net.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Query().Get("sort") != "-updated" {
						fmt.Fprint(w, `{"items":[]}`)
						return
					}
					fmt.Fprint(w, feed)
				}))
				e.pol.Versions["tv"].Indexers = append(e.pol.Versions["tv"].Indexers, "seadex")
				e.pol.Versions["tv"].PreferSeaDex = prefer
				tvp := e.pol.Profiles["tv-2160p"]
				tvp.Qualities = append(tvp.Qualities, policy.QualityItem{Name: "Bluray-1080p"})
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
				if sd := seadexInfo(t, rs); sd.Items != 0 {
					t.Fatalf("seadex feed items %+v, want none from an entry without relations", sd)
				}
				if e.rec.routed(hash) {
					t.Fatalf("a record its entry does not list was picked for The Show")
				}
			})
		}
	}
}
