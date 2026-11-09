package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
)

// Three equally ranked releases; the search-time TorBox check finds the last one cached. The check's evidence feeds
// the cache tie-break: the search ranks again, so the order shown (Ranked), the candidates and the release the route
// tries first agree, and it is the cached one.
func TestCheckTopEvidenceReranks(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Query().Get("t") == "caps" {
			fmt.Fprint(w, `<caps><limits default="100" max="100"/><searching><movie-search available="yes" supportedParams="imdbid,tmdbid"/></searching><categories><category id="2000" name="Movies"/></categories></caps>`)
			return
		}
		fmt.Fprint(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><torznab:response offset="0" total="3"/>`)
		for _, c := range []string{"a", "b", "c"} {
			fmt.Fprintf(w, `<item><title>Arrival.2016.2160p.WEB-DL-GRP</title><guid>%s</guid><size>20000000000</size><category>2000</category>`+
				`<torznab:attr name="infohash" value="%s"/><torznab:attr name="seeders" value="50"/></item>`, c, h(c))
		}
		fmt.Fprint(w, `</channel></rss>`)
	}))
	defer srv.Close()
	e := newEnv(t, false, policy.ModeActive)
	e.pol.Indexers["pub"].URL = srv.URL + "/api"
	e.pol.Profiles = map[string]*policy.Profile{"web": {Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}}, Cutoff: "WEBDL-2160p"}}
	e.pol.Versions["movie"].Profile = "web"
	e.pr["tb"].cache = map[string]bool{h("c"): true}
	eng, err := decide.New(e.pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := indexer.New(e.pol, &searchGateway{}, indexer.Options{CacheTTL: -1})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetEvidence(indexer.NewEvidence(indexer.EvidenceConfig{Store: e.st, Policy: e.pol,
		Checkers: map[string]provider.Provider{"tb": e.pr["tb"]}, Now: func() time.Time { return e.clock }}))
	title := decide.Title{ID: 1, Kind: policy.KindMovie, Name: "Arrival", Year: 2016, IMDb: "tt2543164", TMDb: 329865}
	got, err := SearchRequest(e.ctx, eng, svc, title, decide.Version{ID: 10, Key: "movie", Policy: e.pol.Versions["movie"]}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.pr["tb"].checked) != 1 {
		t.Fatalf("TorBox checks %+v", e.pr["tb"].checked)
	}
	var shown, tried []string
	for _, d := range got.Ranked {
		if d.Accepted {
			shown = append(shown, got.Releases[d].InfoHash)
		}
	}
	for _, c := range got.Request.Candidates {
		tried = append(tried, c.Release.InfoHash)
	}
	if want := []string{h("c"), h("a"), h("b")}; !slices.Equal(shown, want) || !slices.Equal(tried, want) {
		t.Fatalf("shown %v, candidates %v, want %v", shown, tried, want)
	}
	res, err := e.r.Plan(e.ctx, got.Request)
	if err != nil {
		t.Fatal(err)
	}
	if res.Hash != h("c") || res.Provider != "tb" {
		t.Errorf("route tries %s on %s first, want %s on tb: %s", res.Hash, res.Provider, h("c"), steps(res))
	}
}
