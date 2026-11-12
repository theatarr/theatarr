package indexer

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer/cardigann"
	"github.com/theatarr/theatarr/internal/policy"
)

// keepNamed is a Query.Related that keeps rows whose title starts with prefix ("" keeps nothing).
func keepNamed(prefix string) func(Release) bool {
	return func(r Release) bool { return prefix != "" && strings.HasPrefix(r.Title, prefix) }
}

// An id answer whose rows Related refuses counts as empty: the title tier is asked, and its rows are kept unasked.
// When one id row is kept, the title tier is not asked.
func TestTorznabIDPaddingFallsBackToTitle(t *testing.T) {
	loadFixtures(t, "prowlarr_caps.xml")
	for _, tc := range []struct {
		keep          string
		requests      int
		want          string
		wantUnrelated int
	}{
		{"", 2, "Release.5.2160p", 2},
		{"Release.1.", 1, "Release.1.2160p", 1},
	} {
		n := newNet(t)
		rec := &recorder{}
		serveProwlarr(n, rec, "prowlarr_caps.xml", func(r *http.Request) string {
			if r.URL.Query().Has("imdbid") {
				return feedOf(0, 2, 0, "")
			}
			return feedOf(5, 1, 0, "")
		})
		h := newHarness(t, n, testPolicy(), false)
		res := search(t, h, "prowlarr", Query{Type: QueryMovie, IMDb: "tt0133093", Title: "The Matrix", Year: 1999,
			Related: keepNamed(tc.keep)})
		if got := len(rec.all()) - 1; got != tc.requests { // minus the caps request
			t.Errorf("keep %q: %d search requests, want %d", tc.keep, got, tc.requests)
		}
		if len(res.Releases) != 1 || res.Releases[0].Title != tc.want || res.unrelated != tc.wantUnrelated {
			t.Errorf("keep %q: releases %v, unrelated %d", tc.keep, res.Releases, res.unrelated)
		}
	}
}

func TestCardigannIDPaddingFallsBackToTitle(t *testing.T) {
	q := Query{Type: QueryMovie, Title: "The Matrix", Year: 1999, IMDb: "tt0133093"}
	for _, tc := range []struct {
		keep          string
		requests      int
		want          string
		wantUnrelated int
	}{
		{"", 2, "The.Matrix.1999.1080p", 2},
		{"Other.B", 1, "Other.B.2019.1080p", 1},
	} {
		h, asked := idRowsSite(t, func(v url.Values) string {
			if v.Get("q") == "tt0133093" {
				return `{"name":"Other.A.2018.1080p","hash":"` + strings.Repeat("a", 40) + `"},` +
					`{"name":"Other.B.2019.1080p","hash":"` + strings.Repeat("b", 40) + `"}`
			}
			return `{"name":"The.Matrix.1999.1080p","hash":"` + strings.Repeat("c", 40) + `"}`
		})
		qq := q
		qq.Related = keepNamed(tc.keep)
		res := search(t, h, "site", qq)
		if got := asked(); len(got) != tc.requests {
			t.Errorf("keep %q: requests %v", tc.keep, got)
		}
		if len(res.Releases) != 1 || res.Releases[0].Title != tc.want || res.unrelated != tc.wantUnrelated {
			t.Errorf("keep %q: releases %v, unrelated %d", tc.keep, res.Releases, res.unrelated)
		}
	}
}

// idRowsSite serves idSiteDef like idSite, answering each request with the JSON rows rows gives.
func idRowsSite(t *testing.T, rows func(url.Values) string) (*harness, func() []url.Values) {
	t.Helper()
	n := newNet(t)
	var (
		mu    sync.Mutex
		asked []url.Values
	)
	n.serve("site.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Query())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"results":[%s]}`, rows(r.URL.Query()))
	}))
	p := testPolicy()
	p.Indexers["site"] = &policy.Indexer{Kind: "cardigann", Definition: "site", URL: "http://site.test/",
		Privacy: policy.PrivacyPublic, Budget: "site"}
	p.Budgets["site"] = &policy.Budget{Requests: 60, Per: policy.Duration(time.Minute), Burst: 10}
	m := cardigann.NewManager(testDefs{"site": idSiteDef}, nil)
	m.DefaultDelay = time.Millisecond
	h := newHarness(t, n, p, false, func(_ *gateway.Options, o *Options) { o.Cardigann = m })
	return h, func() []url.Values {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(asked)
	}
}
