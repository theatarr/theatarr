package acquire

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/router"
)

// purposeGateway sends to the local fake indexer and records the purpose each request carries to the gateway.
type purposeGateway struct {
	mu  sync.Mutex
	got []gateway.Purpose
}

func (g *purposeGateway) Do(ctx context.Context, _ gateway.Target, req *http.Request) (*http.Response, error) {
	g.mu.Lock()
	g.got = append(g.got, gateway.PurposeOf(ctx))
	g.mu.Unlock()
	return http.DefaultClient.Do(req.WithContext(ctx))
}

func (*purposeGateway) PrivateFor(context.Context, gateway.Target) bool { return false }

// take returns the distinct purposes recorded since the last take.
func (g *purposeGateway) take() []gateway.Purpose {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := slices.Compact(slices.Sorted(slices.Values(g.got)))
	g.got = nil
	return out
}

// Each acquire search path tags its indexer requests: the releases search is interactive, a missing run wanted, a
// cutoff run and a search of a movie with a file upgrade (docs/design/gateway.md).
func TestSearchPurposes(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	cutoffFixture(t, e)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Query().Get("t") == "caps" {
			fmt.Fprint(w, `<caps><limits default="100" max="100"/><searching><search available="yes" supportedParams="q"/><movie-search available="yes" supportedParams="q,imdbid"/><tv-search available="yes" supportedParams="q,season,ep,tvdbid"/></searching></caps>`)
			return
		}
		fmt.Fprint(w, `<rss version="2.0"><channel></channel></rss>`)
	}))
	defer srv.Close()
	e.pol.Indexers["pub"].URL = srv.URL + "/api"
	gw := &purposeGateway{}
	idx, err := indexer.New(e.pol, gw, indexer.Options{CacheTTL: -1})
	if err != nil {
		t.Fatal(err)
	}
	e.a.search = func(ctx context.Context, title decide.Title, v decide.Version, existing *decide.ExistingFile, ss *decide.SeriesSearch) (*router.Searched, error) {
		return router.SearchRequest(decide.WithSearch(ctx), e.a.eng, idx, title, v, existing, ss)
	}
	want := func(step string, p gateway.Purpose) {
		t.Helper()
		if got := gw.take(); !slices.Equal(got, []gateway.Purpose{p}) {
			t.Fatalf("%s: purposes %v, want %s", step, got, p)
		}
	}

	if _, err := e.a.Interactive(e.ctx, 1, "movie", nil, nil); err != nil {
		t.Fatal(err)
	}
	want("interactive", gateway.PurposeInteractive)

	submit := func(sel Selection) {
		t.Helper()
		if n, _, err := e.a.Submit(e.ctx, 0, sel); err != nil || n == 0 {
			t.Fatalf("submit %+v: %d %v", sel, n, err)
		}
		e.drain()
	}
	submit(Selection{Versions: []string{"movie"}, Only: OnlyMissing})
	want("missing", gateway.PurposeWanted)
	submit(Selection{Versions: []string{"movie", "series"}, Only: OnlyCutoff, QualityCutoff: true})
	want("cutoff", gateway.PurposeUpgrade)
	submit(Selection{Versions: []string{"movie"}, TitleIDs: []int64{4}})
	want("search of a movie with a file", gateway.PurposeUpgrade)
	submit(Selection{Versions: []string{"movie"}, TitleIDs: []int64{1}})
	want("search of a movie without one", gateway.PurposeWanted)
	// A series target is held when each of its episodes has a file (1002 and 1003 have, 1001 has not).
	submit(Selection{Versions: []string{"series"}, TitleIDs: []int64{10}, EpisodeIDs: []int64{1003}})
	want("search of an episode with a file", gateway.PurposeUpgrade)
	submit(Selection{Versions: []string{"series"}, TitleIDs: []int64{10}, EpisodeIDs: []int64{1001}})
	want("search of an episode without one", gateway.PurposeWanted)
	season := 1
	submit(Selection{Versions: []string{"series"}, TitleIDs: []int64{10}, Season: &season})
	want("search of a season with and without files", gateway.PurposeWanted)
}

func TestSearchPurpose(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		reason string
		held   bool
		want   gateway.Purpose
	}{
		{"", false, gateway.PurposeWanted},
		{OnlyMissing, false, gateway.PurposeWanted},
		{OnlyMissing, true, gateway.PurposeWanted},
		{OnlyCutoff, false, gateway.PurposeUpgrade},
		{"", true, gateway.PurposeUpgrade},
	} {
		if got := searchPurpose(c.reason, c.held); got != c.want {
			t.Errorf("searchPurpose(%q, held %v) = %s, want %s", c.reason, c.held, got, c.want)
		}
	}
}
