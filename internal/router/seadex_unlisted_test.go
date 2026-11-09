package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// Search -> decide -> route: a SeaDex answer whose entry lists one torrent but expands
// another (an unrelated film) offers only the listed torrent, so the router can never add the unlisted one,
// whichever way the version prefers SeaDex.
func TestUnlistedSeaDexTorrentNeverRouted(t *testing.T) {
	t.Parallel()
	listed, unlisted := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for _, prefer := range []bool{false, true} {
		t.Run(fmt.Sprintf("prefer=%v", prefer), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[{"alID":1,"trs":["listed"],"expand":{"trs":[`+
					`{"id":"unrelated","infoHash":"`+unlisted+`","tracker":"Nyaa","isBest":true,"dualAudio":true,"files":[{"name":"Other Film BD 1080p.mkv","length":6000000000}]},`+
					`{"id":"listed","infoHash":"`+listed+`","tracker":"Nyaa","isBest":true,"files":[{"name":"Movie 2024 BD 1080p.mkv","length":5000000000}]}]}}]}`)
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
			var cs []Candidate
			for _, rel := range res.Releases {
				if rel.InfoHash == unlisted {
					t.Fatalf("search offered the unlisted torrent as %q", rel.Title)
				}
				d, err := eng.Decide(e.ctx, decide.Release{Release: rel}, decide.Title{ID: 1, Kind: policy.KindMovie, Name: "Movie", Year: 2024, Runtime: 110},
					decide.Version{ID: 10, Key: "movie", Policy: e.pol.Versions["movie"]}, nil)
				if err != nil {
					t.Fatal(err)
				}
				if d.Accepted {
					cs = append(cs, Candidate{Release: rel, Quality: d.Quality, Score: d.Score})
				}
			}
			if len(cs) != 1 {
				t.Fatalf("want the listed torrent accepted, got %d of %d releases", len(cs), len(res.Releases))
			}
			if _, err := e.r.Route(e.ctx, grabReq(cs...)); err != nil {
				t.Fatal(err)
			}
			for _, a := range e.ad["rd"].adds {
				if a == unlisted {
					t.Fatalf("unlisted torrent added: %v", e.ad["rd"].adds)
				}
			}
		})
	}
}
