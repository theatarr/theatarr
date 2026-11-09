package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// Through search, decide and route: an entry without a relation list (absent or null "trs") offers no
// release, so its unrelated expanded torrent is never decided or added under the wanted title, for prefer-on and
// prefer-off versions alike.
func TestMissingRelationsNotRouted(t *testing.T) {
	t.Parallel()
	for _, relation := range []string{"", `"trs":null,`} {
		for _, prefer := range []bool{false, true} {
			t.Run(fmt.Sprintf("relation=%q/prefer=%v", relation, prefer), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					fmt.Fprint(w, `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[{"alID":1,`+relation+`"expand":{"trs":[{"id":"unrelated","infoHash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","tracker":"Nyaa","isBest":true,"files":[{"name":"Other Film BD 1080p.mkv","length":5000000000}]}]}}]}`)
				}))
				defer srv.Close()
				e := newEnv(t, true, policy.ModeActive)
				e.pol.Indexers["sd"] = &policy.Indexer{Kind: "seadex", URL: srv.URL, Privacy: policy.PrivacyPublic}
				e.pol.Profiles = map[string]*policy.Profile{"sd": {Qualities: []policy.QualityItem{{Name: "Bluray-1080p"}}, Cutoff: "Bluray-1080p"}}
				e.pol.Versions["movie"].Profile = "sd"
				e.pol.Versions["movie"].Indexers = []string{"sd"}
				e.pol.Versions["movie"].PreferSeaDex = prefer
				eng, err := decide.New(e.pol, decide.Options{})
				if err != nil {
					t.Fatal(err)
				}
				idx, err := indexer.New(e.pol, &searchGateway{}, indexer.Options{CacheTTL: -1})
				if err != nil {
					t.Fatal(err)
				}
				res, err := idx.Search(e.ctx, "sd", indexer.Query{Type: indexer.QueryMovie, AniList: 1, Title: "Movie", Year: 2024})
				if err != nil {
					t.Fatal(err)
				}
				if len(res.Releases) == 0 {
					return
				}
				rel := res.Releases[0]
				d, err := eng.Decide(e.ctx, decide.Release{Release: rel}, decide.Title{ID: 1, Kind: policy.KindMovie, Name: "Movie", Year: 2024, Runtime: 110},
					decide.Version{ID: 10, Key: "movie", Policy: e.pol.Versions["movie"]}, nil)
				if err != nil {
					t.Fatal(err)
				}
				var adds []string
				if d.Accepted {
					if _, err := e.r.Route(e.ctx, grabReq(Candidate{Release: rel, Quality: d.Quality, Score: d.Score})); err != nil {
						t.Fatal(err)
					}
					adds = e.ad["rd"].adds
				}
				t.Fatalf("unrelated expanded torrent offered as %q (accepted=%v, provider adds %v)", rel.Title, d.Accepted, adds)
			})
		}
	}
}
