package router

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
)

// DMM's availability listing a Prowlarr release on AllDebrid puts AllDebrid first for it, in the search (CachedOn)
// and from the stored row; Real-Debrid is still tried after it. A release DMM does not list keeps the version order.
func TestDMMAvailabilityOrdersProviders(t *testing.T) {
	e := newEnv(t, true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Service string   `json:"service"`
			Hashes  []string `json:"hashes"`
		}
		if r.URL.Path != "/api/sponsor/availability" || json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var items []string
		for _, x := range body.Hashes {
			if body.Service == "ad" && (x == h("a") || x == h("c")) {
				items = append(items, fmt.Sprintf(`{"hash": %q}`, x))
			}
		}
		fmt.Fprintf(w, `{"available": [%s]}`, strings.Join(items, ","))
	}))
	defer srv.Close()
	t.Setenv("THEATARR_TEST_DMM_KEY", "dmm-test-key")
	e.pol.Indexers["dmm"] = &policy.Indexer{Kind: "dmm", URL: srv.URL + "/api/torznab/rd/cached", APIKeyEnv: "THEATARR_TEST_DMM_KEY",
		Privacy: policy.PrivacyPublic}
	idx, err := indexer.New(e.pol, &searchGateway{}, indexer.Options{CacheTTL: -1, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	idx.SetEvidence(indexer.NewEvidence(indexer.EvidenceConfig{Store: e.st, Policy: e.pol, Now: func() time.Time { return e.clock },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}))
	listed, unknown := cand(h("a"), "Movie.2023.2160p.web-dl"), cand(h("b"), "Movie.2023.2160p.web-dl")
	rels := []indexer.Release{listed.Release, unknown.Release, cand(h("c"), "Other.2023.2160p.web-dl").Release}
	idx.ApplyCachedOn(e.ctx, rels)
	if n := idx.CheckAvailability(e.ctx, []*policy.VersionPolicy{e.pol.Versions["movie"]}, rels); n != 2 {
		t.Fatalf("gained %d", n)
	}
	listed.Release, unknown.Release = rels[0], rels[1]

	res, err := e.r.Route(e.ctx, grabReq(unknown))
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != "rd" || orderedStep(res) != nil {
		t.Fatalf("unknown: %q, steps %s", res.Provider, steps(res))
	}
	res, err = e.r.Route(e.ctx, grabReq(listed))
	if err != nil {
		t.Fatal(err)
	}
	if st := orderedStep(res); res.Provider != "ad" || st == nil || !strings.Contains(st.Message, "ad (feed:dmm/availability)") {
		t.Fatalf("listed on ad: %q, steps %s", res.Provider, steps(res))
	}

	// The stored row orders a candidate that carries no evidence itself (a later search); RD is still tried when
	// AllDebrid turns it down.
	e.ad["ad"].add = refuse(adder.CodeNotCached)
	res, err = e.r.Route(e.ctx, grabReq(cand(h("c"), "Other.2023.2160p.web-dl")))
	if err != nil {
		t.Fatal(err)
	}
	if got := providersTried(res); len(got) < 2 || !strings.HasPrefix(got[0], "ad:") || res.Provider != "rd" {
		t.Fatalf("stored evidence: tried %v, provider %q, steps %s", got, res.Provider, steps(res))
	}
}

// availSearch is one server for a search with DMM's availability: a Prowlarr-like feed for "pub" (items) and DMM's
// availability, which records what it was asked and lists the hashes in listed for their service.
type availSearch struct {
	srv    *httptest.Server
	mu     sync.Mutex
	items  []string            // title + "|" + hash, in feed order
	listed map[string][]string // service -> hashes
	asked  []availCall
}

type availCall struct {
	service string
	hashes  []string
}

func newAvailSearch(t *testing.T, e *env) *availSearch {
	s := &availSearch{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.URL.Path == "/api/sponsor/availability" {
			var body struct {
				Service string   `json:"service"`
				Hashes  []string `json:"hashes"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("availability body: %v", err)
			}
			s.asked = append(s.asked, availCall{body.Service, body.Hashes})
			var items []string
			for _, x := range body.Hashes {
				if slices.Contains(s.listed[body.Service], x) {
					items = append(items, fmt.Sprintf(`{"hash": %q}`, x))
				}
			}
			fmt.Fprintf(w, `{"available": [%s]}`, strings.Join(items, ","))
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Query().Get("t") == "caps" {
			fmt.Fprint(w, `<caps><limits default="500" max="500"/><searching><movie-search available="yes" supportedParams="imdbid,tmdbid"/></searching><categories><category id="2000" name="Movies"/></categories></caps>`)
			return
		}
		fmt.Fprintf(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><torznab:response offset="0" total="%d"/>`, len(s.items))
		for i, it := range s.items {
			title, hash, _ := strings.Cut(it, "|")
			fmt.Fprintf(w, `<item><title>%s</title><guid>g%d</guid><size>20000000000</size><category>2000</category>`+
				`<torznab:attr name="infohash" value="%s"/><torznab:attr name="seeders" value="50"/></item>`, title, i, hash)
		}
		fmt.Fprint(w, `</channel></rss>`)
	}))
	t.Cleanup(s.srv.Close)
	t.Setenv("THEATARR_TEST_DMM_KEY", "dmm-test-key")
	e.pol.Indexers["pub"].URL = s.srv.URL + "/api"
	e.pol.Indexers["dmm"] = &policy.Indexer{Kind: "dmm", URL: s.srv.URL + "/api/torznab/rd/cached", APIKeyEnv: "THEATARR_TEST_DMM_KEY",
		Privacy: policy.PrivacyPublic}
	e.pol.Profiles = map[string]*policy.Profile{"web": {Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"}}, Cutoff: "WEBDL-2160p"}}
	e.pol.Versions["movie"].Profile = "web"
	return s
}

func (s *availSearch) search(t *testing.T, e *env, svc *indexer.Service) *Searched {
	t.Helper()
	eng, err := decide.New(e.pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	title := decide.Title{ID: 1, Kind: policy.KindMovie, Name: "Arrival", Year: 2016, IMDb: "tt2543164", TMDb: 329865}
	got, err := SearchRequest(e.ctx, eng, svc, title, decide.Version{ID: 10, Key: "movie", Policy: e.pol.Versions["movie"]}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (s *availSearch) calls() []availCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.asked)
}

func rankedHashes(got *Searched) []string {
	var out []string
	for _, d := range got.Ranked {
		out = append(out, got.Releases[d].InfoHash)
	}
	return out
}

// DMM's availability runs after the first ranking: with 250 releases whose accepted ones arrive last, it asks about
// the first 100 hashes in rank order (the accepted ones), once on rd and once on ad. A second search asks about the
// next 100 (the first were just asked); a search with nothing accepted still asks about its top rejected rows.
func TestDMMAvailabilityAsksRankedTop(t *testing.T) {
	e := newEnv(t, false)
	s := newAvailSearch(t, e)
	var accepted []string
	for i := range 250 {
		x := fmt.Sprintf("%040x", i+1)
		if i < 150 {
			s.items = append(s.items, "Arrival.2016.720p.HDTV.x264-GRP|"+x)
		} else {
			s.items = append(s.items, "Arrival.2016.2160p.WEB-DL-GRP|"+x)
			accepted = append(accepted, x)
		}
	}
	svc, err := indexer.New(e.pol, &searchGateway{}, indexer.Options{CacheTTL: -1})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetEvidence(indexer.NewEvidence(indexer.EvidenceConfig{Store: e.st, Policy: e.pol, Now: func() time.Time { return e.clock }}))
	got := s.search(t, e, svc)
	ranked := rankedHashes(got)
	if len(ranked) != 250 || !slices.Equal(slices.Sorted(slices.Values(ranked[:100])), accepted) {
		t.Fatalf("%d ranked; the first 100 are not the accepted ones", len(ranked))
	}
	calls := s.calls()
	if len(calls) != 2 || calls[0].service != "rd" || calls[1].service != "ad" ||
		!slices.Equal(calls[0].hashes, ranked[:100]) || !slices.Equal(calls[1].hashes, ranked[:100]) {
		t.Fatalf("%d calls; want rd and ad on the first 100 in rank order", len(calls))
	}

	// The same search again: the first 100 were just asked, so the next 100 in rank order are.
	got = s.search(t, e, svc)
	ranked = rankedHashes(got)
	calls = s.calls()[2:]
	if len(calls) != 2 || !slices.Equal(calls[0].hashes, ranked[100:200]) || !slices.Equal(calls[1].hashes, ranked[100:200]) {
		t.Fatalf("second search: %d calls; want the next 100 in rank order", len(calls))
	}

	// Nothing accepted: the top rejected rows are still asked about.
	s.mu.Lock()
	s.items = nil
	for i := range 3 {
		s.items = append(s.items, fmt.Sprintf("Arrival.2016.720p.HDTV.x264-GRP|%040x", 5000+i))
	}
	s.mu.Unlock()
	got = s.search(t, e, svc)
	calls = s.calls()[4:]
	if len(calls) != 2 || len(calls[0].hashes) != 3 || got.Ranked[0].Accepted {
		t.Fatalf("nothing accepted: %d calls %+v", len(calls), calls)
	}
}

// A positive DMM answer marks every release of the hash, a rejected copy too, and the search ranks again before the
// TorBox check, which then checks the release DMM listed (top 1) rather than the one ranked first without it.
func TestDMMAvailabilityRanksBeforeTorBox(t *testing.T) {
	e := newEnv(t, false)
	s := newAvailSearch(t, e)
	for _, c := range []string{"a", "b", "c", "d"} {
		s.items = append(s.items, "Arrival.2016.2160p.WEB-DL-GRP|"+h(c))
	}
	s.items = append(s.items, "Arrival.2016.720p.HDTV.x264-GRP|"+h("d"))
	s.listed = map[string][]string{"rd": {h("d")}}
	e.pol.Router.CacheEvidence = &policy.CacheEvidence{TorBoxCheck: &policy.TorBoxCheck{Top: 1}}
	e.pr["tb"].cache = map[string]bool{}
	svc, err := indexer.New(e.pol, &searchGateway{}, indexer.Options{CacheTTL: -1})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetEvidence(indexer.NewEvidence(indexer.EvidenceConfig{Store: e.st, Policy: e.pol,
		Checkers: map[string]provider.Provider{"tb": e.pr["tb"]}, Now: func() time.Time { return e.clock }}))
	got := s.search(t, e, svc)
	if checked := e.pr["tb"].checked; len(checked) != 1 || !slices.Equal(checked[0].Hashes, []string{h("d")}) {
		t.Fatalf("TorBox checked %+v, want %s alone", checked, h("d"))
	}
	if ranked := rankedHashes(got); !slices.Equal(ranked[:4], []string{h("d"), h("a"), h("b"), h("c")}) {
		t.Fatalf("ranked %v", ranked)
	}
	marked := 0
	for d, rel := range got.Releases {
		if rel.InfoHash == h("d") {
			if !slices.Contains(rel.CachedOn, "rd") {
				t.Errorf("%s (accepted %v) has no rd mark: %v", rel.Title, d.Accepted, rel.CachedOn)
			}
			marked++
		}
	}
	if marked != 2 || got.Request.Candidates[0].Release.InfoHash != h("d") {
		t.Fatalf("%d releases of d; first candidate %s", marked, got.Request.Candidates[0].Release.InfoHash)
	}
}
