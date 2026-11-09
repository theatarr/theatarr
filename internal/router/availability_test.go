package router

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider/adder"
)

// DMM's availability listing a Prowlarr release on AllDebrid puts AllDebrid first for it, in the search (CachedOn)
// and from the stored row; Real-Debrid is still tried after it. A release DMM does not list keeps the version order.
func TestDMMAvailabilityOrdersProviders(t *testing.T) {
	e := newEnv(t, true, policy.ModeActive)
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
