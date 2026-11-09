package router

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
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

// A route queued under an older policy revision is decided again with the cache rows written while it waited: of
// two equally scored releases queued uncached-first, the one now stored cached is tried first.
func TestRedecideReadsStoredCacheEvidence(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	freshMovie(e)
	e.exec(`UPDATE title SET year=2024,runtime_minutes=110 WHERE id=1`)
	eng, err := decide.New(e.pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e.r.SetProfiles(eng.Profile)
	e.r.SetDecider(eng.Decide, eng.DecideSearch)
	a, b := cand(h("a"), "Movie.2024.2160p.WEB-DL-GRP"), cand(h("b"), "Movie.2024.2160p.WEB-DL-GRP")
	a.Release.Seeders, b.Release.Seeders = 10, 10
	e.exec(`INSERT INTO provider_cache (info_hash, provider, state, source, checked_at, expires_at)
		VALUES (?, 'tb', 'cached', 'torbox:check', ?, ?)`, h("b"), e.clock.UnixMilli(), e.clock.Add(time.Hour).UnixMilli())
	req := Request{TitleID: 1, VersionID: 10, Version: "movie", Source: "rss", Candidates: []Candidate{a, b},
		DecisionRev: "rev1"}
	e.pol.Revision = "rev2"
	res, err := e.r.Route(e.ctx, req)
	if err != nil || res.Outcome != OutcomeAdded {
		t.Fatalf("route %+v %v", res, err)
	}
	if got := e.ad["tb"].adds; len(got) != 1 || got[0] != h("b") || len(e.ad["rd"].adds) != 0 || len(e.ad["ad"].adds) != 0 {
		t.Fatalf("adds rd %v ad %v tb %v, want the cached %s on tb only", e.ad["rd"].adds, e.ad["ad"].adds, got, h("b"))
	}
}

// The stored evidence a re-decision ranks by does not reorder the route's provider accounts: with two AllDebrid
// accounts and a cached row on the second only, the route still tries that account first, as it did when queued.
func TestRedecideKeepsAccountOrder(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	freshMovie(e)
	e.exec(`UPDATE title SET year=2024,runtime_minutes=110 WHERE id=1`)
	e.pol.Providers["ad2"] = &policy.Provider{Kind: provider.KindAllDebrid, Order: 4, URL: "http://ad2.test/v4",
		APIKeyEnv: "THEATARR_AD2_KEY", Pacing: policy.Pacing{AddsPerMinute: 10, BackoffMax: policy.Duration(2 * time.Hour)}}
	e.pol.Versions["movie"].Providers = []string{"ad", "ad2"}
	adders := map[string]adder.Adder{}
	provs := map[string]provider.Provider{}
	for k, pv := range e.pol.Providers {
		e.ad[k] = &fakeAdder{key: k, kind: pv.Kind}
		e.pr[k] = &fakeProvider{key: k, kind: pv.Kind}
		adders[k], provs[k] = e.ad[k], e.pr[k]
	}
	var err error
	e.r, err = New(Config{Store: e.st, Policy: e.pol, Adders: adders, Providers: provs,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return e.clock }})
	if err != nil {
		t.Fatal(err)
	}
	e.r.sleep = func(context.Context, time.Duration) error { return nil }
	eng, err := decide.New(e.pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e.r.SetProfiles(eng.Profile)
	e.r.SetDecider(eng.Decide, eng.DecideSearch)
	a := cand(h("a"), "Movie.2024.2160p.WEB-DL-GRP")
	a.Release.Seeders = 10
	e.exec(`INSERT INTO provider_cache (info_hash, provider, state, source, checked_at, expires_at)
		VALUES (?, 'ad2', 'cached', 'router:add', ?, ?)`, h("a"), e.clock.UnixMilli(), e.clock.Add(time.Hour).UnixMilli())
	e.pol.Revision = "rev2"
	res, err := e.r.Route(e.ctx, Request{TitleID: 1, VersionID: 10, Version: "movie", Source: "rss",
		Candidates: []Candidate{a}, DecisionRev: "rev1"})
	if err != nil || res.Outcome != OutcomeAdded {
		t.Fatalf("route %+v %v", res, err)
	}
	if got := e.ad["ad2"].adds; len(got) != 1 || len(e.ad["ad"].adds) != 0 {
		t.Fatalf("adds ad %v ad2 %v, want the cached account ad2 first", e.ad["ad"].adds, got)
	}
}
