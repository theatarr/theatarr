package indexer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer/cardigann"
	"github.com/theatarr/theatarr/internal/policy"
)

// rssCompareSite is one site of the RSS comparison: its definition, the site pages its search without keywords
// fetches, and the Torznab feed Prowlarr gives for the same items (testdata/cardigann_rss).
type rssCompareSite struct {
	def      string            // definition id (and file name)
	host     string            // the site
	hosts    []string          // further hosts the definition reaches
	settings map[string]string // non-default settings
	pages    map[string]string // request path (and query) -> fixture
	torznab  string            // Prowlarr's feed for the same items
	requests int               // requests one RSS read makes
	custom   bool              // loaded from the catalog's custom folder (not in Prowlarr/Indexers)
}

var rssCompareSites = []rssCompareSite{
	{def: "thepiratebay", host: "tpb.test", hosts: []string{"apibay.test"}, settings: map[string]string{"apiurl": "apibay.test"},
		pages:   map[string]string{"apibay.test/precompiled/data_top100_recent.json": "tpb_recent.json"},
		torznab: "prowlarr_tpb_rss.xml", requests: 1},
	{def: "therarbg", host: "therarbg.test", custom: true,
		pages:   map[string]string{"therarbg.test/get-posts/": "therarbg_recent.json"},
		torznab: "prowlarr_therarbg_rss.xml", requests: 1},
	{def: "nyaasi", host: "nyaa.test",
		pages:   map[string]string{"nyaa.test/?p=2": "nyaa_p2.html", "nyaa.test/": "nyaa_p1.html"},
		torznab: "prowlarr_nyaa_rss.xml", requests: 2},
}

// rssDefs reads the definitions from dir. The test network speaks plain HTTP, so an https:// a definition spells out
// (TPB's API host) is read as http:// (only with THEATARR_CARDIGANN_DEFS: the hand-made ones use http).
type rssDefs string

func (d rssDefs) Load(_ context.Context, id string) (*cardigann.Definition, string, error) {
	data, err := os.ReadFile(filepath.Join(string(d), id+".yml"))
	if err != nil {
		return nil, "", fmt.Errorf("definition %q: %w", id, cardigann.ErrNoDefinition)
	}
	def, err := cardigann.ParseDefinition([]byte(strings.ReplaceAll(string(data), "https://", "http://")))
	if err != nil {
		return nil, "", err
	}
	return def, id, def.Validate()
}

// customDefs is a catalog whose custom folder holds the definition id from src, as an operator drops it there.
func customDefs(t *testing.T, src, id string) cardigann.Definitions {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(src, id+".yml"))
	if err != nil {
		t.Fatal(err)
	}
	store := &cardigann.Store{Dir: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(store.Dir, "custom"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir, "custom", id+".yml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if sum, ok := store.Summary(id); !ok || !sum.Custom {
		t.Fatalf("%s is not a custom definition: %+v", id, sum)
	}
	return store
}

// TestCardigannRSSMatchesTorznab is the dry comparison asked for before moving the Prowlarr feeds (TPB, TheRARBG,
// Nyaa) to native indexers: for the same site pages, the native RSS read (the definition's search without keywords)
// yields the items Prowlarr's Torznab feed listed: same titles, hashes, details links, sizes, dates, categories, IMDb
// ids and seeders, so the RSS sync decides them the same way. Torznab-only fields (Prowlarr's download link, peers)
// are not compared. Nothing leaves the test network.
//
// By default it runs on hand-made definitions shaped like the real ones (testdata/cardigann_rss; Prowlarr/Indexers
// has no licence, so the real ones are not committed). To check the real definitions, point THEATARR_CARDIGANN_DEFS at a
// local checkout's definitions/v11 (a definition missing there is reported and skipped). TheRARBG is not there
// (Prowlarr/Indexers blocklists it): it runs from the catalog's custom folder, read from THEATARR_CARDIGANN_CUSTOM when set:
//
//	THEATARR_CARDIGANN_DEFS=/path/to/definitions/v11 THEATARR_CARDIGANN_CUSTOM=/path/to/custom go test ./internal/indexer -run CardigannRSSMatchesTorznab -v
func TestCardigannRSSMatchesTorznab(t *testing.T) {
	dir, customDir := os.Getenv("THEATARR_CARDIGANN_DEFS"), os.Getenv("THEATARR_CARDIGANN_CUSTOM")
	if dir == "" {
		dir = filepath.Join("testdata", "cardigann_rss")
	}
	if customDir == "" {
		customDir = filepath.Join("testdata", "cardigann_rss")
	}
	loadFixtures(t, "prowlarr_caps.xml")
	for i, s := range rssCompareSites {
		t.Run(s.def, func(t *testing.T) {
			src := dir
			if s.custom {
				src = customDir
			}
			if _, err := os.Stat(filepath.Join(src, s.def+".yml")); err != nil {
				t.Skipf("%s is not in %s", s.def, src)
			}
			n := newNet(t)
			var (
				mu    sync.Mutex
				asked []string
			)
			serve := func(host string) {
				n.serve(host, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					target := host + r.URL.Path
					mu.Lock()
					asked = append(asked, target+"?"+r.URL.RawQuery)
					mu.Unlock()
					// A page key is a path prefix, with "?p=N" for a numbered page (a key without it serves the first).
					for key, file := range s.pages {
						path, page, _ := strings.Cut(key, "?p=")
						if strings.HasPrefix(target, path) && r.URL.Query().Get("p") == page {
							w.Write(fixture(t, filepath.Join("cardigann_rss", file)))
							return
						}
					}
					http.NotFound(w, r)
				}))
			}
			serve(s.host)
			for _, h := range s.hosts {
				serve(h)
			}
			torznabFeed := string(fixture(t, filepath.Join("cardigann_rss", s.torznab)))
			serveProwlarr(n, &recorder{}, "prowlarr_caps.xml", func(r *http.Request) string {
				if r.URL.Query().Get("offset") != "" {
					return `<?xml version="1.0"?><rss><channel></channel></rss>`
				}
				return torznabFeed
			})
			p := testPolicy()
			p.Indexers["native"] = &policy.Indexer{Kind: "cardigann", Definition: s.def, URL: "http://" + s.host + "/",
				Privacy: policy.PrivacyPublic, Budget: "native", Hosts: s.hosts, Settings: s.settings}
			p.Indexers["feed"] = &policy.Indexer{Kind: "torznab", URL: fmt.Sprintf("http://prowlarr.test/%d/api", i+5),
				APIKeyEnv: prowlarrKeyEnv, Privacy: policy.PrivacyPublic, Budget: "prowlarr"}
			p.Budgets["native"] = &policy.Budget{Requests: 60, Per: policy.Duration(time.Minute), Burst: 10}
			var defs cardigann.Definitions = rssDefs(dir)
			if s.custom {
				defs = customDefs(t, src, s.def)
			}
			m := cardigann.NewManager(defs, nil)
			m.DefaultDelay = time.Millisecond
			h := newHarness(t, n, p, false, func(_ *gateway.Options, o *Options) { o.Cardigann = m })

			if why := h.svc.RSSProblem(context.Background(), "native"); why != "" {
				t.Fatalf("RSSProblem: %s", why)
			}
			q := Query{Type: QueryRSS, Categories: []int{2000, 5000, 5070}, RSSLimit: 100}
			native := search(t, h, "native", q)
			feed := search(t, h, "feed", q)
			if native.Requests != s.requests {
				t.Errorf("native requests %d, want %d: %v", native.Requests, s.requests, asked)
			}
			for _, a := range asked {
				if strings.Contains(a, "q=test") || strings.Contains(a, ":keywords:") || strings.Contains(a, "q.php") {
					t.Errorf("an RSS read searched by keyword: %s", a)
				}
			}
			byHash := map[string]Release{}
			for _, r := range native.Releases {
				byHash[r.InfoHash] = r
			}
			if len(feed.Releases) == 0 || len(native.Releases) != len(feed.Releases) {
				t.Errorf("native %d items, Torznab %d: %v", len(native.Releases), len(feed.Releases), titles(native.Releases))
			}
			for _, f := range feed.Releases {
				r, ok := byHash[f.InfoHash]
				if !ok {
					t.Errorf("%s: missing from the native read", f.Title)
					continue
				}
				for _, d := range rssDiff(f, r) {
					t.Errorf("%s: %s", f.Title, d)
				}
			}
			if !t.Failed() {
				t.Logf("%s: %d items, %d requests, same as the Torznab feed", s.def, len(native.Releases), native.Requests)
			}

			// The next page: a definition has none, so nothing is asked.
			before := len(asked)
			q.RSSOffset = 100
			if next := search(t, h, "native", q); len(next.Releases) != 0 || next.Requests != 0 || len(asked) != before {
				t.Errorf("page 2: %d releases, %d requests", len(next.Releases), next.Requests)
			}
		})
	}
}

// rssDiff lists what differs between a Torznab item and the native one, in the fields the RSS sync decides on.
// Sizes may differ by rounding (an HTML site shows "1.4 GiB").
func rssDiff(f, r Release) []string {
	var out []string
	if f.Title != r.Title {
		out = append(out, fmt.Sprintf("title %q, native %q", f.Title, r.Title))
	}
	if f.GUID != r.GUID {
		out = append(out, fmt.Sprintf("details %q, native %q", f.GUID, r.GUID))
	}
	if d := f.Size - r.Size; d > f.Size/200 || -d > f.Size/200 {
		out = append(out, fmt.Sprintf("size %d, native %d", f.Size, r.Size))
	}
	if d := f.PublishDate.Sub(r.PublishDate); d > time.Minute || -d > time.Minute {
		out = append(out, fmt.Sprintf("published %s, native %s", f.PublishDate, r.PublishDate))
	}
	family := func(cats []int) []int {
		var out []int
		for _, c := range cats {
			if c == 5070 {
				out = append(out, c)
			} else {
				out = append(out, c/1000*1000)
			}
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	if !slices.Equal(family(f.Categories), family(r.Categories)) {
		out = append(out, fmt.Sprintf("categories %v, native %v", f.Categories, r.Categories))
	}
	if f.IMDb != r.IMDb {
		out = append(out, fmt.Sprintf("IMDb %q, native %q", f.IMDb, r.IMDb))
	}
	factor := func(r Release, k string) float64 {
		v, err := strconv.ParseFloat(r.Extras[k], 64)
		if err != nil {
			return 1
		}
		return v
	}
	for _, k := range []string{"downloadvolumefactor", "uploadvolumefactor"} {
		if factor(f, k) != factor(r, k) {
			out = append(out, fmt.Sprintf("%s %q, native %q", k, f.Extras[k], r.Extras[k]))
		}
	}
	if f.TMDb != r.TMDb || f.TVDB != r.TVDB {
		out = append(out, fmt.Sprintf("TMDb/TVDB %d/%d, native %d/%d", f.TMDb, f.TVDB, r.TMDb, r.TVDB))
	}
	if f.Seeders != r.Seeders {
		out = append(out, fmt.Sprintf("seeders %d, native %d", f.Seeders, r.Seeders))
	}
	if f.Privacy.IsPrivate() || r.Privacy.IsPrivate() || f.Privacy != r.Privacy {
		out = append(out, fmt.Sprintf("privacy %s, native %s", f.Privacy, r.Privacy))
	}
	if !r.Magnet.IsMagnet() && r.Link.Empty() {
		out = append(out, "native item has no magnet or link")
	}
	return out
}

// An RSS read of a catalog source is checked like any search: without a fresh VPN check nothing is sent, and a
// definition that cannot feed RSS is refused before any request, with the reason Service.RSSProblem gives.
func TestCardigannRSSRefusals(t *testing.T) {
	n := newNet(t)
	rec := &recorder{}
	serveSite(n, rec)
	h := cardigannHarness(t, n, "public", nil, nil, true)
	_, err := h.svc.Search(context.Background(), "site", Query{Type: QueryRSS, Categories: []int{2000}})
	if !errors.Is(err, gateway.ErrVPNRequired) || n.dialCount("site.test:80") != 0 {
		t.Fatalf("without a VPN check: %v, %d dials", err, n.dialCount("site.test:80"))
	}

	// A site with no movie or TV category.
	p := testPolicy()
	p.Indexers["music"] = &policy.Indexer{Kind: "cardigann", Definition: "music", URL: "http://site.test/",
		Privacy: policy.PrivacyPublic, Budget: "music"}
	p.Budgets["music"] = &policy.Budget{Requests: 60, Per: policy.Duration(time.Minute), Burst: 10}
	music := strings.Replace(fmt.Sprintf(siteDef, "public"), "    - {id: 1, cat: Movies, desc: \"Movies\"}\n    - {id: 2, cat: TV, desc: \"TV\"}",
		"    - {id: 9, cat: Audio, desc: \"Music\"}", 1)
	m := cardigann.NewManager(testDefs{"music": music}, nil)
	h = newHarness(t, n, p, false, func(_ *gateway.Options, o *Options) { o.Cardigann = m })
	if why := h.svc.RSSProblem(context.Background(), "music"); !strings.Contains(why, "no movie or TV category") {
		t.Errorf("RSSProblem = %q", why)
	}
	_, err = h.svc.Search(context.Background(), "music", Query{Type: QueryRSS, Categories: []int{2000}})
	if !errors.Is(err, ErrUnsupported) || len(rec.all()) != 0 {
		t.Errorf("music RSS: %v, %d requests", err, len(rec.all()))
	}
	// A definition the catalog does not have, and no catalog at all.
	p.Indexers["gone"] = &policy.Indexer{Kind: "cardigann", Definition: "gone", URL: "http://site.test/",
		Privacy: policy.PrivacyPublic, Budget: "music"}
	h = newHarness(t, n, p, false, func(_ *gateway.Options, o *Options) { o.Cardigann = m })
	if why := h.svc.RSSProblem(context.Background(), "gone"); !strings.Contains(why, "definition gone") {
		t.Errorf("missing definition: %q", why)
	}
	h = newHarness(t, n, p, false)
	if why := h.svc.RSSProblem(context.Background(), "music"); !strings.Contains(why, "catalog is not running") {
		t.Errorf("no catalog: %q", why)
	}
	// Other kinds: a Torznab feed has RSS, Torrentio none.
	if why := h.svc.RSSProblem(context.Background(), "prowlarr"); why != "" {
		t.Errorf("torznab: %q", why)
	}
	if why := h.svc.RSSProblem(context.Background(), "torrentio"); why == "" {
		t.Error("torrentio has RSS")
	}
}
