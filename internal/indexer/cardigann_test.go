package indexer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer/cardigann"
	"github.com/theatarr/theatarr/internal/policy"
)

// testDefs serves definitions from memory.
type testDefs map[string]string

func (d testDefs) Load(_ context.Context, id string) (*cardigann.Definition, string, error) {
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

// A JSON site invented for the tests. type is filled in per test.
const siteDef = `---
id: site
name: Site
type: %s
links:
  - http://site.test/
caps:
  categorymappings:
    - {id: 1, cat: Movies, desc: "Movies"}
    - {id: 2, cat: TV, desc: "TV"}
  modes:
    search: [q]
settings:
  - name: username
    type: text
    label: Username
  - name: password
    type: password
    label: Password
  - name: sort
    type: text
    label: Sort
search:
  paths:
    - path: api/search
      response:
        type: json
  headers:
    X-User: ["{{ .Config.username }}:{{ .Config.password }}"]
  inputs:
    q: "{{ .Keywords }}"
    cat: "{{ join .Categories \",\" }}"
    sort: "{{ .Config.sort }}"
  rows:
    selector: results
  fields:
    title:
      selector: name
    category:
      selector: cat
    infohash:
      selector: hash
    details:
      text: "{{ .Config.sitelink }}t/{{ .Result.title }}"
    size:
      selector: size
    seeders:
      selector: seeders
    leechers:
      selector: leechers
    downloadvolumefactor:
      text: 1
    uploadvolumefactor:
      text: 1
`

func cardigannHarness(t *testing.T, n *testNet, defType string, mut func(ix *policy.Indexer), creds map[string]string,
	skipEgress bool) *harness {
	t.Helper()
	p := testPolicy()
	ix := &policy.Indexer{Kind: "cardigann", Definition: "site", URL: "http://site.test/", Privacy: policy.PrivacyPublic,
		Budget: "site", Settings: map[string]string{"sort": "seeders"}}
	if mut != nil {
		mut(ix)
	}
	p.Indexers["site"] = ix
	p.Budgets["site"] = &policy.Budget{Requests: 60, Per: policy.Duration(time.Minute), Burst: 10}
	m := cardigann.NewManager(testDefs{"site": fmt.Sprintf(siteDef, defType)}, nil)
	m.DefaultDelay = time.Millisecond
	return newHarness(t, n, p, skipEgress, func(_ *gateway.Options, o *Options) {
		o.Cardigann = m
		if creds != nil {
			o.APIKeys = creds
		}
	})
}

const siteHash = "abcdef0123456789abcdef0123456789abcdef01"

func serveSite(n *testNet, rec *recorder) {
	n.serve("site.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"results":[{"name":"Show.S01E02.1080p.WEB","cat":2,"hash":"%s","size":1073741824,"seeders":7,"leechers":3}]}`,
			strings.ToUpper(siteHash))
	}))
}

// A public site: the query becomes keywords and a category, the credential's secret settings reach the site, and
// the release comes back public with its hash, through the gateway with the engine's User-Agent.
func TestCardigannSearch(t *testing.T) {
	n := newNet(t)
	rec := &recorder{}
	serveSite(n, rec)
	h := cardigannHarness(t, n, "public", nil, map[string]string{"site": `{"username":"alice","password":"pw-1"}`}, false)
	res := search(t, h, "site", Query{Type: QuerySeries, Title: "Show", Season: 1, Episode: 2})
	if len(res.Releases) != 1 || res.Requests != 1 {
		t.Fatalf("releases %+v requests %d", res.Releases, res.Requests)
	}
	r := res.Releases[0]
	if r.Kind != "cardigann" || r.Indexer != "site" || r.InfoHash != siteHash || r.Size != 1<<30 || r.Seeders != 7 ||
		r.Peers != 10 || r.Privacy != policy.PrivacyPublic {
		t.Errorf("release %+v", r)
	}
	if len(r.Categories) != 1 || r.Categories[0] != 5000 {
		t.Errorf("categories %v", r.Categories)
	}
	if r.Extras["downloadvolumefactor"] != "1" {
		t.Errorf("extras %v", r.Extras)
	}
	req := rec.all()[0]
	if got := req.URL.Query().Get("q"); got != "Show S01E02" {
		t.Errorf("keywords %q", got)
	}
	if req.URL.Query().Get("cat") != "2" || req.URL.Query().Get("sort") != "seeders" {
		t.Errorf("query %s", req.URL.RawQuery)
	}
	if req.Header.Get("X-User") != "alice:pw-1" {
		t.Errorf("credential header %q", req.Header.Get("X-User"))
	}
	if ua := req.Header.Get("User-Agent"); !strings.HasPrefix(ua, "Mozilla/") {
		t.Errorf("User-Agent %q", ua)
	}
}

// The definition's type labels its releases (semi-private and untyped show as private); privacy is a label only, so
// an indexer configured public still searches.
func TestCardigannPrivacyFromDefinition(t *testing.T) {
	n := newNet(t)
	serveSite(n, &recorder{})
	h := cardigannHarness(t, n, "semi-private", nil, nil, false)
	res := search(t, h, "site", Query{Type: QueryText, Title: "show"})
	if len(res.Releases) != 1 || res.Releases[0].Privacy != policy.PrivacySemiPrivate {
		t.Fatalf("releases %+v", res.Releases)
	}
	h = cardigannHarness(t, n, `""`, nil, nil, false)
	res = search(t, h, "site", Query{Type: QueryText, Title: "show"})
	if len(res.Releases) != 1 || !res.Releases[0].Privacy.IsPrivate() {
		t.Fatalf("untyped definition: %+v", res.Releases)
	}
}

// Every request goes through the gateway's VPN check and fails closed without one.
func TestCardigannVPNRequired(t *testing.T) {
	n := newNet(t)
	serveSite(n, &recorder{})
	h := cardigannHarness(t, n, "public", nil, nil, true)
	_, err := h.svc.Search(context.Background(), "site", Query{Type: QueryText, Title: "show"})
	if !errors.Is(err, gateway.ErrVPNRequired) {
		t.Fatalf("without a VPN check: %v", err)
	}
	if n.dialCount("site.test:80") != 0 {
		t.Error("dialed the site without a VPN check")
	}
}

// A secret in plain settings, a credential that is not a JSON object, and no catalog are each refused.
func TestCardigannRefusals(t *testing.T) {
	n := newNet(t)
	rec := &recorder{}
	serveSite(n, rec)
	q := Query{Type: QueryText, Title: "show"}
	h := cardigannHarness(t, n, "public", func(ix *policy.Indexer) { ix.Settings["password"] = "pw" }, nil, false)
	if _, err := h.svc.Search(context.Background(), "site", q); err == nil || !strings.Contains(err.Error(), "settings.password holds a credential") {
		t.Errorf("secret in settings: %v", err)
	}
	h = cardigannHarness(t, n, "public", nil, map[string]string{"site": "pw-plain"}, false)
	if _, err := h.svc.Search(context.Background(), "site", q); err == nil || strings.Contains(err.Error(), "pw-plain") ||
		!strings.Contains(err.Error(), "JSON object") {
		t.Errorf("bad credential: %v", err)
	}
	p := testPolicy()
	p.Indexers["site"] = &policy.Indexer{Kind: "cardigann", Definition: "site", URL: "http://site.test/", Privacy: policy.PrivacyPublic}
	h = newHarness(t, n, p, false)
	if _, err := h.svc.Search(context.Background(), "site", q); !errors.Is(err, ErrNoCatalog) {
		t.Errorf("no catalog: %v", err)
	}
	if len(rec.all()) != 0 {
		t.Error("a refused search sent a request")
	}
}

func TestCardigannKeywords(t *testing.T) {
	for q, want := range map[*Query]string{
		{Type: QueryMovie, Title: "Film", Year: 1999}:             "Film 1999",
		{Type: QueryMovie, Title: "Film"}:                         "Film",
		{Type: QuerySeries, Title: "Show", Season: 2}:             "Show S02",
		{Type: QuerySeries, Title: "Show", Season: 2, Episode: 3}: "Show S02E03",
		{Type: QuerySeries, Title: "Show"}:                        "Show",
		{Type: QueryRSS}:                                          "",
	} {
		if got := cardigannKeywords(*q); got != want {
			t.Errorf("%+v: %q, want %q", *q, got, want)
		}
	}
	if (&cardigannSearcher{}).Supports(Query{Type: QueryMovie, IMDb: "tt0133093"}) {
		t.Error("an id-only movie search is not a keyword search")
	}
}
