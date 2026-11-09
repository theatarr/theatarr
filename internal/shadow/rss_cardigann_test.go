package shadow

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/indexer/cardigann"
	"github.com/theatarr/theatarr/internal/policy"
)

// nativeDefs serves catalog definitions from memory.
type nativeDefs map[string]string

func (d nativeDefs) Load(_ context.Context, id string) (*cardigann.Definition, string, error) {
	text, ok := d[id]
	if !ok {
		return nil, "", fmt.Errorf("definition %q: %w", id, cardigann.ErrNoDefinition)
	}
	def, err := cardigann.ParseDefinition([]byte(text))
	if err != nil {
		return nil, "", err
	}
	return def, id, def.Validate()
}

// nativeSite is a JSON site invented for the tests: its search without keywords lists the latest releases ("recent").
// type and the category mappings are filled in per definition.
const nativeSite = `---
id: %s
name: Native
type: %s
links:
  - http://native.test/
caps:
  categorymappings:
%s
  modes:
    search: [q]
search:
  paths:
    - path: "{{ if .Keywords }}search{{ else }}recent{{ end }}"
      response:
        type: json
  inputs:
    q: "{{ .Keywords }}"
  rows:
    selector: results
  fields:
    title:
      selector: name
    category:
      selector: cat
    infohash:
      selector: hash
    imdbid:
      selector: imdb
      optional: true
    date:
      selector: added
    size:
      selector: size
    seeders:
      selector: seeders
    leechers:
      selector: leechers
    downloadvolumefactor:
      text: 0
    uploadvolumefactor:
      text: 1
`

const nativeCats = `    - {id: 1, cat: Movies, desc: "Movies"}
    - {id: 2, cat: TV, desc: "TV"}`

// nativeFeed is the site's latest list, newest first.
type nativeFeed struct {
	mu    sync.Mutex
	items []item
}

func (f *nativeFeed) set(items ...item) {
	f.mu.Lock()
	f.items = items
	f.mu.Unlock()
}

func (f *nativeFeed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	type row struct {
		Name     string `json:"name"`
		Cat      int    `json:"cat"`
		Hash     string `json:"hash"`
		IMDb     string `json:"imdb,omitempty"`
		Added    int64  `json:"added"`
		Size     int64  `json:"size"`
		Seeders  int    `json:"seeders"`
		Leechers int    `json:"leechers"`
	}
	rows := []row{}
	if r.URL.Path == "/recent" {
		for _, it := range f.items {
			cat := 1
			if it.tv {
				cat = 2
			}
			rows = append(rows, row{it.title, cat, it.hash, it.imdb, it.pub.Unix(), it.size, 10, 2})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"results": rows})
}

// withNative adds catalog sources to the policy and rebuilds the gateway, the indexer service and the runner:
// "native" (public, listed by movie-web), "nativepriv" (a private definition, listed by movie-web) and "nativemusic"
// (a site without movie or TV categories, listed by movie-web). The movie-web version lists only these and prowlarr.
func (e *env) withNative(t *testing.T) *nativeFeed {
	t.Helper()
	f := &nativeFeed{}
	e.net.serve("native.test", f)
	defs := nativeDefs{
		"native":      fmt.Sprintf(nativeSite, "native", "public", nativeCats),
		"nativepriv":  fmt.Sprintf(nativeSite, "nativepriv", "private", nativeCats),
		"nativemusic": fmt.Sprintf(nativeSite, "nativemusic", "public", `    - {id: 9, cat: Audio, desc: "Music"}`),
	}
	for k, privacy := range map[string]policy.Privacy{"native": policy.PrivacyPublic, "nativepriv": policy.PrivacyPrivate,
		"nativemusic": policy.PrivacyPublic} {
		e.pol.Indexers[k] = &policy.Indexer{Kind: "cardigann", Definition: k, URL: "http://native.test/", Privacy: privacy,
			Budget: k}
		e.pol.Budgets[k] = &policy.Budget{Requests: 600, Per: policy.Duration(time.Minute), Burst: 100}
	}
	e.pol.Versions["movie-web"].Indexers = []string{"prowlarr", "native", "nativepriv", "nativemusic"}
	gw, err := gateway.New(e.ctx, e.pol, gateway.Options{Logger: e.log, Version: "test", DialContext: e.net.dial,
		Timeout: 5 * time.Second, MaxWait: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	gw.CheckEgress(e.ctx)
	m := cardigann.NewManager(defs, nil)
	m.DefaultDelay = time.Millisecond
	svc, err := indexer.New(e.pol, gw, indexer.Options{Logger: e.log, CacheTTL: -1, Cardigann: m})
	if err != nil {
		t.Fatal(err)
	}
	e.gw, e.svc = gw, svc
	e.build(nil)
	return f
}

func rssIndexer(sum *RSSSummary, key string) *RSSIndexer {
	for i := range sum.Indexers {
		if sum.Indexers[i].Indexer == key {
			return &sum.Indexers[i]
		}
	}
	return nil
}

// A catalog source a shadowed version lists is read by the RSS sync like a Torznab feed: its search without keywords
// is the feed (one request, no offset paging even with a full page), its items are decided and picked, seen marks
// and the cursor work as for any feed, and the sources that cannot be read are listed with the reason.
func TestRSSReadsCatalogSources(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	f := e.withNative(t)
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.prow.setRSS()
	// A full page (more than rssPageSize items): the Torznab loop would ask for the next page; a definition has none.
	items := append([]item{{title: alienWebRel, hash: hAlienWeb, imdb: alien.imdb, size: 20e9, pub: t0}},
		filler(rssPageSize+20, 1)...)
	f.set(items...)
	sum, err := e.r.RSS(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	native := rssIndexer(sum, "native")
	if native == nil || native.Items != len(items) || native.New != len(items) || native.Requests != 1 ||
		native.Error != "" || native.Skipped != "" {
		t.Fatalf("native feed %+v (all %+v)", native, sum.Indexers)
	}
	// The private-labelled source is read like any other (privacy is a label): two feeds on the one test host.
	if reqs := e.net.requests("native.test/"); len(reqs) != 2 || !strings.HasPrefix(reqs[0], "native.test/recent?") {
		t.Fatalf("requests %v", reqs)
	}
	if sum.Picks != 1 {
		t.Fatalf("picks %+v", sum)
	}
	got := one[string](t, e.st, `SELECT indexer || ' | ' || release_title || ' | ' || version_key FROM shadow_rss_pick
		WHERE info_hash = ?`, hAlienWeb)
	if want := "native | " + alienWebRel + " | movie-web"; got != want {
		t.Errorf("pick %q, want %q", got, want)
	}
	if p := rssIndexer(sum, "nativepriv"); p == nil || p.Skipped != "" || p.Error != "" || p.Requests != 1 {
		t.Errorf("private source: %+v", p)
	}
	if p := rssIndexer(sum, "nativemusic"); p == nil || !strings.Contains(p.Skipped, "no movie or TV category") {
		t.Errorf("music source: %+v", p)
	}
	if reqs := e.net.requests("native.test/"); len(reqs) != 2 {
		t.Errorf("a skipped source was asked: %v", reqs)
	}
	if c := one[string](t, e.st, `SELECT item_key FROM shadow_rss_cursor WHERE indexer = 'native'`); c != "h:"+hAlienWeb {
		t.Errorf("cursor %q", c)
	}

	// The next poll: one new item above the head. The picked item is seen; items of titles we don't know are decided
	// again (as from any feed).
	e.advance(15 * time.Minute)
	newer := filler(1, 9000)[0]
	newer.pub = t0.Add(10 * time.Minute)
	f.set(append([]item{newer}, items...)...)
	if sum, err = e.r.RSS(e.ctx); err != nil {
		t.Fatal(err)
	}
	if native = rssIndexer(sum, "native"); native == nil || native.Items != len(items)+1 || native.New != len(items) ||
		native.Gap != "" || native.Requests != 1 || sum.Picks != 0 {
		t.Fatalf("second poll %+v picks %d", native, sum.Picks)
	}
	if c := one[string](t, e.st, `SELECT item_key FROM shadow_rss_cursor WHERE indexer = 'native'`); c != "h:"+newer.hash {
		t.Errorf("cursor %q", c)
	}

	// The previous head fell off the site's list: reported as a gap, as for a Torznab feed.
	e.advance(15 * time.Minute)
	later := filler(5, 20000)
	for i := range later {
		later[i].pub = t0.Add(time.Duration(40-i) * time.Minute)
	}
	f.set(later...)
	if sum, err = e.r.RSS(e.ctx); err != nil {
		t.Fatal(err)
	}
	if native = rssIndexer(sum, "native"); native == nil || native.Gap == "" {
		t.Fatalf("gap not reported: %+v", native)
	}
}
