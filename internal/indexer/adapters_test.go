package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
)

func serveProwlarr(n *testNet, rec *recorder, caps string, feed func(r *http.Request) string) {
	n.serve("prowlarr.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		w.Header().Set("Content-Type", "application/rss+xml")
		if r.URL.Query().Get("t") == "caps" {
			w.Write(fixtureBytes(caps))
			return
		}
		fmt.Fprint(w, feed(r))
	}))
}

var fixtures = map[string][]byte{}

func fixtureBytes(name string) []byte { return fixtures[name] }

func loadFixtures(t *testing.T, names ...string) {
	for _, n := range names {
		fixtures[n] = fixture(t, n)
	}
}

func search(t *testing.T, h *harness, key string, q Query) *Result {
	t.Helper()
	res, err := h.svc.Search(context.Background(), key, q)
	if err != nil {
		t.Fatalf("search %s: %v", key, err)
	}
	return res
}

func TestTorznabMovie(t *testing.T) {
	loadFixtures(t, "prowlarr_caps.xml", "prowlarr_movie.xml")
	n := newNet(t)
	rec := &recorder{}
	serveProwlarr(n, rec, "prowlarr_caps.xml", func(*http.Request) string { return string(fixtureBytes("prowlarr_movie.xml")) })
	h := newHarness(t, n, testPolicy(), false)

	res := search(t, h, "prowlarr", Query{Type: QueryMovie, IMDb: "tt0133093", TMDb: 603, Title: "The Matrix", Year: 1999})
	reqs := rec.all()
	if len(reqs) != 2 || res.Requests != 2 {
		t.Fatalf("requests %d (result says %d), want caps + one page", len(reqs), res.Requests)
	}
	q := reqs[1].URL.Query()
	want := map[string]string{"t": "movie", "imdbid": "0133093", "cat": "2000", "limit": "100", "apikey": prowlarrKey}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("param %s = %q, want %q", k, q.Get(k), v)
		}
	}
	if q.Has("q") || q.Has("offset") {
		t.Errorf("unexpected params %v", q)
	}
	if reqs[1].URL.Path != "/1/api" {
		t.Errorf("path %s", reqs[1].URL.Path)
	}
	if len(res.Releases) != 2 {
		t.Fatalf("%d releases", len(res.Releases))
	}
	a, b := res.Releases[0], res.Releases[1]
	if a.Title != "The.Matrix.1999.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.7.1-GROUP" || a.InfoHash != "0123456789abcdef0123456789abcdef01234567" ||
		a.Size != 68719476736 || a.Seeders != 42 || a.Peers != 45 || a.IMDb != "tt0133093" || a.TMDb != 603 ||
		a.Indexer != "prowlarr" || a.Kind != "torznab" || a.Privacy != policy.PrivacyPublic {
		t.Errorf("first release: %+v", a)
	}
	if fmt.Sprint(a.Categories) != "[2000 2045]" || fmt.Sprint(a.Flags) != "[freeleech internal]" {
		t.Errorf("categories %v flags %v", a.Categories, a.Flags)
	}
	// Language attributes, else <language> elements (TorznabRssParser.GetLanguages).
	if fmt.Sprint(a.Languages) != "[English French]" || fmt.Sprint(b.Languages) != "[German]" {
		t.Errorf("languages %v %v", a.Languages, b.Languages)
	}
	if a.Extras["downloadvolumefactor"] != "0" || a.Extras["grabs"] != "7" {
		t.Errorf("extras %v", a.Extras)
	}
	if !a.PublishDate.Equal(time.Date(2026, 10, 24, 18, 3, 11, 0, time.UTC)) {
		t.Errorf("date %v", a.PublishDate)
	}
	if a.Link.Empty() || !a.Link.Keyed() || a.Magnet.IsMagnet() {
		t.Errorf("link: empty %v keyed %v", a.Link.Empty(), a.Link.Keyed())
	}
	// The second item says semi-private: private, though the policy says public.
	if b.Privacy != policy.PrivacySemiPrivate {
		t.Errorf("semi-private item: privacy %q", b.Privacy)
	}
	if b.InfoHash != "0102030405060708090a0b0c0d0e0f1011121314" || !b.Magnet.IsMagnet() || b.Seeders != 3 {
		t.Errorf("magnet item: %+v", b)
	}

	// Links never reach JSON, fmt or logs.
	js, _ := json.Marshal(res.Releases)
	if strings.Contains(string(js), "PLACEHOLDER") || strings.Contains(string(js), "magnet:") || strings.Contains(string(js), "tracker.test") {
		t.Errorf("JSON leaks a link: %s", js)
	}
	if s := fmt.Sprintf("%v %+v %#v", a, a, a.Link); strings.Contains(s, "PLACEHOLDER") {
		t.Errorf("fmt leaks the link: %s", s)
	}
	if strings.Contains(n.logs.String(), prowlarrKey) {
		t.Error("the API key reached the logs")
	}

	// The caps are cached: a second, different search asks for the feed only.
	search(t, h, "prowlarr", Query{Type: QuerySeries, TVDB: 81189, IMDb: "tt0903747", Season: 2, Episode: 3})
	reqs = rec.all()
	if len(reqs) != 3 {
		t.Fatalf("%d requests after the second search", len(reqs))
	}
	q = reqs[2].URL.Query()
	if q.Get("t") != "tvsearch" || q.Get("tvdbid") != "81189" || q.Get("season") != "2" || q.Get("ep") != "3" ||
		q.Has("imdbid") || q.Get("cat") != "5000" {
		t.Errorf("tv params %v", q)
	}
}

func TestTorznabTextOnlyCaps(t *testing.T) {
	loadFixtures(t, "prowlarr_caps_textonly.xml", "prowlarr_movie.xml")
	n := newNet(t)
	rec := &recorder{}
	serveProwlarr(n, rec, "prowlarr_caps_textonly.xml", func(*http.Request) string { return string(fixtureBytes("prowlarr_movie.xml")) })
	h := newHarness(t, n, testPolicy(), false)
	search(t, h, "tpb", Query{Type: QueryMovie, IMDb: "tt0133093", Title: "The Matrix", Year: 1999})
	search(t, h, "tpb", Query{Type: QuerySeries, Title: "Show", Season: 1, Anime: true})
	reqs := rec.all()
	if q := reqs[1].URL.Query(); q.Get("t") != "search" || q.Get("q") != "The Matrix 1999" || q.Get("limit") != "50" {
		t.Errorf("movie fallback %v", q)
	}
	if q := reqs[2].URL.Query(); q.Get("t") != "search" || q.Get("q") != "Show S01" || q.Has("cat") {
		t.Errorf("series fallback %v", q)
	}
	// No title and no usable id: nothing is sent.
	_, err := h.svc.Search(context.Background(), "tpb", Query{Type: QueryMovie, IMDb: "tt0133093"})
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("id-only search on a text-only feed: %v", err)
	}
}

// feedOf builds a Torznab page of count items numbered from start, with an optional total.
func feedOf(start, count, total int, titleSuffix string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>`)
	if total > 0 {
		fmt.Fprintf(&b, `<torznab:response offset="%d" total="%d"/>`, start, total)
	}
	for i := start; i < start+count; i++ {
		fmt.Fprintf(&b, `<item><title>Release.%d.2160p%s</title><guid>g%d</guid><size>%d</size>`+
			`<torznab:attr name="infohash" value="%040x"/><torznab:attr name="seeders" value="1"/></item>`,
			i, titleSuffix, i, 1000+i, i+1)
	}
	b.WriteString(`</channel></rss>`)
	return b.String()
}

func TestTorznabPagingStopsAtPageCap(t *testing.T) {
	loadFixtures(t, "prowlarr_caps.xml")
	n := newNet(t)
	rec := &recorder{}
	serveProwlarr(n, rec, "prowlarr_caps.xml", func(r *http.Request) string {
		off := atoiOr(r.URL.Query().Get("offset"), 0)
		lim := atoiOr(r.URL.Query().Get("limit"), 0)
		return feedOf(off, lim, 0, "")
	})
	p := testPolicy()
	p.Indexers["prowlarr"].PageCap = 250
	h := newHarness(t, n, p, false)
	res := search(t, h, "prowlarr", Query{Type: QueryText, Title: "release"})
	if len(res.Releases) != 250 || !res.Truncated || res.Requests != 4 {
		t.Fatalf("releases %d truncated %v requests %d", len(res.Releases), res.Truncated, res.Requests)
	}
	if !res.Capped {
		t.Fatal("our own page_cap is not marked Capped (it would count as a failed search)")
	}
	reqs := rec.all()
	last := reqs[len(reqs)-1].URL.Query()
	if last.Get("offset") != "200" || last.Get("limit") != "50" {
		t.Errorf("last page asked %v", last)
	}
}

func TestDMM(t *testing.T) {
	n := newNet(t)
	rec := &recorder{}
	// DMM answers ten a page whatever limit it is sent, biggest first, with a total.
	n.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		off := atoiOr(r.URL.Query().Get("offset"), 0)
		fmt.Fprint(w, feedOf(off, min(10, 35-off), 35, " {imdb-tt0133093}"))
	}))
	h := newHarness(t, n, testPolicy(), false)
	res := search(t, h, "dmm", Query{Type: QueryMovie, IMDb: "tt0133093", Title: "The Matrix"})
	reqs := rec.all()
	if len(reqs) != 4 || len(res.Releases) != 35 || res.Truncated {
		t.Fatalf("requests %d releases %d truncated %v", len(reqs), len(res.Releases), res.Truncated)
	}
	for i, want := range []string{"", "10", "20", "30"} {
		if got := reqs[i].URL.Query().Get("offset"); got != want {
			t.Errorf("page %d offset %q, want %q", i, got, want)
		}
	}
	q := reqs[0].URL.Query()
	if reqs[0].URL.Path != "/api/torznab/rd/cached/api" || q.Get("t") != "movie" || q.Get("imdbid") != "0133093" || q.Get("apikey") != dmmKey {
		t.Errorf("first request %s %v", reqs[0].URL.Path, q)
	}
	r := res.Releases[0]
	if r.Title != "Release.0.2160p" || r.IMDb != "tt0133093" || r.Extras["cached_on"] != "rd" || r.Kind != "dmm" {
		t.Errorf("release %+v", r)
	}
	if strings.Contains(n.logs.String(), dmmKey) {
		t.Error("the DMM key reached the logs")
	}
	// Unsupported: DMM needs an IMDb id.
	if _, err := h.svc.Search(context.Background(), "dmm", Query{Type: QueryMovie, Title: "x"}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("no IMDb: %v", err)
	}
}

func TestDMMBudgetEndsPaging(t *testing.T) {
	n := newNet(t)
	n.serve("dmm.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		off := atoiOr(r.URL.Query().Get("offset"), 0)
		fmt.Fprint(w, feedOf(off, 10, 200, ""))
	}))
	p := testPolicy()
	p.Budgets["dmm-key"] = &policy.Budget{Requests: 2, Per: policy.Duration(time.Hour), Burst: 2}
	h := newHarness(t, n, p, false, func(g *gateway.Options, _ *Options) { g.MaxWait = 20 * time.Millisecond })
	res := search(t, h, "dmm", Query{Type: QuerySeries, IMDb: "tt0903747", Season: 1})
	if len(res.Releases) != 20 || !res.Truncated || len(res.Notes) == 0 || !strings.Contains(res.Notes[0], "budget") {
		t.Fatalf("releases %d truncated %v notes %v", len(res.Releases), res.Truncated, res.Notes)
	}
	if res.Capped {
		t.Fatal("paging stopped by a spent budget is marked Capped")
	}
	// A fresh search with the budget gone fails outright: there is no first page.
	_, err := h.svc.Search(context.Background(), "dmm", Query{Type: QuerySeries, IMDb: "tt0903747", Season: 2})
	if !errors.Is(err, gateway.ErrBudget) {
		t.Errorf("first page without budget: %v", err)
	}
}

func TestDMMURL(t *testing.T) {
	for url, ok := range map[string]bool{
		"https://debridmediamanager.com/api/torznab/cached":        true,
		"https://debridmediamanager.com/api/torznab/tb/cached":     true,
		"https://debridmediamanager.com/api/torznab/ad/cached/api": true,
		"https://debridmediamanager.com":                           false,
		"https://debridmediamanager.com/api/torznab/xx/cached":     false,
	} {
		ix := &policy.Indexer{Kind: "dmm", URL: url, Privacy: policy.PrivacyPublic}
		_, err := newTorznab(base{key: "dmm", kind: "dmm", ix: ix}, true, time.Now)
		if (err == nil) != ok {
			t.Errorf("%s: %v", url, err)
		}
	}
}

func TestTorznabErrors(t *testing.T) {
	loadFixtures(t, "prowlarr_caps.xml", "torznab_error.xml")
	n := newNet(t)
	var fail atomic.Int32
	n.serve("prowlarr.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") == "caps" {
			w.Write(fixtureBytes("prowlarr_caps.xml"))
			return
		}
		if fail.Load() == 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Write(fixtureBytes("torznab_error.xml"))
	}))
	h := newHarness(t, n, testPolicy(), false, func(_ *gateway.Options, o *Options) { o.CacheTTL = -1 })
	_, err := h.svc.Search(context.Background(), "prowlarr", Query{Type: QueryText, Title: "x"})
	var te *TorznabError
	if !errors.As(err, &te) || te.Code != "100" {
		t.Fatalf("error answer: %v", err)
	}
	fail.Store(1)
	_, err = h.svc.Search(context.Background(), "prowlarr", Query{Type: QueryText, Title: "y"})
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 500 {
		t.Fatalf("HTTP 500: %v", err)
	}
	if strings.Contains(err.Error(), prowlarrKey) || strings.Contains(err.Error(), "apikey") {
		t.Errorf("error carries the URL: %v", err)
	}
	if strings.Contains(n.logs.String(), prowlarrKey) {
		t.Error("key in logs")
	}
}

func TestPrivateIndexerReleases(t *testing.T) {
	loadFixtures(t, "prowlarr_caps.xml", "prowlarr_movie.xml")
	n := newNet(t)
	serveProwlarr(n, &recorder{}, "prowlarr_caps.xml", func(*http.Request) string { return string(fixtureBytes("prowlarr_movie.xml")) })
	h := newHarness(t, n, testPolicy(), false)
	res := search(t, h, "tracker", Query{Type: QueryMovie, IMDb: "tt0133093"})
	for _, r := range res.Releases {
		// The first item says public, but the indexer is private in the policy: the label shows the stricter.
		if !r.Privacy.IsPrivate() {
			t.Errorf("%s: privacy %q", r.Title, r.Privacy)
		}
	}
	// An indexer missing from the policy's privacy counts as private.
	p := testPolicy()
	p.Indexers["prowlarr"].Privacy = policy.PrivacyUnknown
	h2 := newHarness(t, n, p, false)
	for _, r := range search(t, h2, "prowlarr", Query{Type: QueryMovie, IMDb: "tt0133093"}).Releases {
		if !r.Privacy.IsPrivate() {
			t.Errorf("unknown privacy: %s is public", r.Title)
		}
	}
}

func TestVPNRequired(t *testing.T) {
	n := newNet(t)
	n.serve("torrentio.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"streams":[]}`) }))
	h := newHarness(t, n, testPolicy(), true) // no egress check yet
	_, err := h.svc.Search(context.Background(), "torrentio", Query{Type: QueryMovie, IMDb: "tt0133093"})
	if !errors.Is(err, gateway.ErrVPNRequired) {
		t.Fatalf("without a VPN check: %v", err)
	}
	if n.dialCount("torrentio.test:80") != 0 {
		t.Error("dialed the indexer without a VPN check")
	}
}

func TestTorrentioMovie(t *testing.T) {
	loadFixtures(t, "torrentio_movie.json")
	n := newNet(t)
	rec := &recorder{}
	n.serve("torrentio.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		w.Write(fixtureBytes("torrentio_movie.json"))
	}))
	h := newHarness(t, n, testPolicy(), false)
	res := search(t, h, "torrentio", Query{Type: QueryMovie, IMDb: "tt0133093"})
	req := rec.all()[0]
	if req.URL.EscapedPath() != "/providers=yts,eztv%7Csort=qualitysize/stream/movie/tt0133093.json" {
		t.Errorf("path %s", req.URL.EscapedPath())
	}
	if len(res.Releases) != 3 {
		t.Fatalf("%d releases", len(res.Releases))
	}
	a := res.Releases[0]
	if a.Title != "The.Matrix.1999.2160p.UHD.BluRay.REMUX.DV.HDR.HEVC.TrueHD.7.1-GROUP" || a.Seeders != 57 ||
		a.Size != tioSize(61.4, "GB") || a.Extras["source"] != "ThePirateBay" || a.Extras["file_idx"] != "0" ||
		a.Extras["torrentio_quality"] != "4k DV | HDR" || a.Magnet.URL() != "magnet:?xt=urn:btih:1111111111111111111111111111111111111111" ||
		a.Privacy.IsPrivate() || a.IMDb != "tt0133093" || a.Extras["pack"] != "" {
		t.Errorf("first: %+v", a)
	}
	if c := res.Releases[2]; c.Title != "The.Matrix.1999.720p.WEB-DL" || c.Size != 900<<20 {
		t.Errorf("description form: %+v", c)
	}
}

func TestTorrentioSeasonSearch(t *testing.T) {
	loadFixtures(t, "torrentio_series.json")
	n := newNet(t)
	rec := &recorder{}
	n.serve("torrentio.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		w.Write(fixtureBytes("torrentio_series.json"))
	}))
	h := newHarness(t, n, testPolicy(), false)
	q := Query{Type: QuerySeries, IMDb: "tt0000001", Season: 2, EpisodeCounts: map[int]int{0: 3, 1: 8, 2: 10, 3: 6}}
	res := search(t, h, "torrentio", q)
	if p := rec.all()[0].URL.Path; !strings.HasSuffix(p, "/stream/series/tt0000001:2:1.json") {
		t.Errorf("path %s", p)
	}
	if len(res.Releases) != 2 {
		t.Fatalf("season search kept %d, want the 2 packs", len(res.Releases))
	}
	s2, multi := res.Releases[0], res.Releases[1]
	if s2.Size != 10*4<<30 || s2.Extras["size_scaled_by"] != "10" || s2.Extras["file"] != "Show.S02E01.2160p.WEB-DL.DDP5.1.H.265-NTb.mkv" {
		t.Errorf("season pack: size %d extras %v", s2.Size, s2.Extras)
	}
	if multi.Size != 24*2<<30 || multi.Extras["size_scaled_by"] != "24" {
		t.Errorf("S01-S03 pack: size %d extras %v", multi.Size, multi.Extras)
	}

	// An episode search keeps everything; without counts a pack keeps the file size and says so.
	res = search(t, h, "torrentio", Query{Type: QuerySeries, IMDb: "tt0000001", Season: 2, Episode: 1})
	if len(res.Releases) != 3 || res.Releases[0].Extras["size_is_file"] != "true" || res.Releases[0].Size != 4<<30 {
		t.Fatalf("episode search: %d releases, first %+v", len(res.Releases), res.Releases[0])
	}
	if res.Releases[2].Extras["pack"] != "" {
		t.Error("a single episode marked as a pack")
	}
}

func TestTorrentioRefusesDebridConfig(t *testing.T) {
	for url, ok := range map[string]bool{
		"https://torrentio.strem.fun":                                     true,
		"https://torrentio.strem.fun/sort=qualitysize|qualityfilter=480p": true,
		"https://torrentio.strem.fun/realdebrid=SECRET":                   false,
		"https://torrentio.strem.fun/providers=yts|torbox=SECRET":         false,
		"https://torrentio.strem.fun/a/b":                                 false,
		"https://torrentio.strem.fun/?realdebrid=SECRET":                  false,
	} {
		ix := &policy.Indexer{Kind: "torrentio", URL: url, Privacy: policy.PrivacyPublic}
		_, err := newTorrentio(base{key: "torrentio", kind: "torrentio", ix: ix})
		if (err == nil) != ok {
			t.Errorf("%s: %v", url, err)
		}
		if err != nil && strings.Contains(err.Error(), "SECRET") {
			t.Errorf("error echoes the key: %v", err)
		}
	}
}

func TestPackEpisodeCount(t *testing.T) {
	seasons := map[int]int{0: 2, 1: 10, 2: 12, 3: 8}
	for _, c := range []struct {
		name   string
		season int
		want   int
	}{
		{"Show.S02.1080p", 2, 12},
		{"Show S01-S03 1080p", 2, 30},
		{"Show Season 1 - Season 2", 2, 22},
		{"Show S01-S02", 3, 8}, // range doesn't hold the season: that season's count
		{"Show Complete Series 1080p", 1, 30},
		{"Show.S05.1080p", 5, 0},
	} {
		if got := packEpisodeCount(c.name, c.season, seasons); got != c.want {
			t.Errorf("%q S%d: %d, want %d", c.name, c.season, got, c.want)
		}
	}
}

func TestZileanMerge(t *testing.T) {
	loadFixtures(t, "zilean_imdb.json", "zilean_title.json")
	n := newNet(t)
	rec := &recorder{}
	n.serve("zilean.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		if r.URL.Query().Has("ImdbId") {
			w.Write(fixtureBytes("zilean_imdb.json"))
		} else {
			w.Write(fixtureBytes("zilean_title.json"))
		}
	}))
	h := newHarness(t, n, testPolicy(), false)
	res := search(t, h, "zilean", Query{Type: QuerySeries, IMDb: "tt0000001", Title: "Show", Season: 1})
	reqs := rec.all()
	if len(reqs) != 2 || reqs[0].URL.Path != "/dmm/filtered" {
		t.Fatalf("%d requests, path %s", len(reqs), reqs[0].URL.Path)
	}
	if q := reqs[0].URL.Query(); q.Get("ImdbId") != "tt0000001" || q.Get("Season") != "1" || q.Has("Query") {
		t.Errorf("IMDb search %v", q)
	}
	if q := reqs[1].URL.Query(); q.Get("Query") != "Show" || q.Get("Season") != "1" || q.Has("ImdbId") {
		t.Errorf("title search %v", q)
	}
	// 2 by IMDb, plus 1 new by title (the duplicate, the other IMDb id and the untitled one are dropped).
	if len(res.Releases) != 3 {
		t.Fatalf("%d releases: %+v", len(res.Releases), res.Releases)
	}
	if r := res.Releases[0]; r.Size != 53687091200 || r.InfoHash != strings.Repeat("7", 40) || r.PublishDate.IsZero() || r.Seeders != -1 {
		t.Errorf("first: %+v", r)
	}
	if r := res.Releases[1]; r.Size != 2147483648 {
		t.Errorf("numeric size: %d", r.Size)
	}
	if r := res.Releases[2]; r.Title != "Show S01 1080p BluRay REMUX AVC-EXAMPLE" || !r.Magnet.IsMagnet() {
		t.Errorf("merged: %+v", r)
	}
	if len(res.Notes) == 0 || !strings.Contains(res.Notes[0], "added 1, dropped 1") {
		t.Errorf("notes %v", res.Notes)
	}
}

func TestSeadex(t *testing.T) {
	loadFixtures(t, "seadex_entry.json", "seadex_movie.json")
	n := newNet(t)
	rec := &recorder{}
	n.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		if r.URL.Query().Get("filter") == "alID=47" {
			w.Write(fixtureBytes("seadex_movie.json"))
			return
		}
		w.Write(fixtureBytes("seadex_entry.json"))
	}))
	h := newHarness(t, n, testPolicy(), false)
	res := search(t, h, "seadex", Query{Type: QuerySeries, AniList: 154587, Title: "Frieren", Season: 1, Anime: true})
	q := rec.all()[0].URL
	if q.Path != "/api/collections/entries/records" || q.Query().Get("filter") != "alID=154587" || q.Query().Get("expand") != "trs" {
		t.Errorf("request %s %v", q.Path, q.Query())
	}
	if len(res.Releases) != 1 {
		t.Fatalf("%d releases, want the one public complete Nyaa entry", len(res.Releases))
	}
	r := res.Releases[0]
	if r.Title != "Frieren S01 Bluray 1080p (abc123def456ghi)" || r.Size != 5910000000 ||
		fmt.Sprint(r.Flags) != "[seadex internal scene seadex-best seadex-preferred]" || r.Seeders != 1000 ||
		r.Extras["downloadvolumefactor"] != "0.75" || r.Extras["uploadvolumefactor"] != "2" ||
		r.Extras["dual_audio"] != "true" || r.Extras["features"] != "2" || r.Extras["source"] != "https://nyaa.si/view/1234567" ||
		r.Link.URL() != "https://nyaa.si/download/1234567.torrent" || r.Link.Keyed() || r.Privacy.IsPrivate() {
		t.Errorf("release %+v link %s", r, r.Link.URL())
	}
	if len(res.Notes) == 0 {
		t.Error("no note about the dropped entries")
	}
	res = search(t, h, "seadex", Query{Type: QueryMovie, AniList: 47, Title: "Akira", Year: 1988})
	if r := res.Releases[0]; r.Title != "Akira (1988) Bluray 1080p Remux (mov1)" || fmt.Sprint(r.Categories) != "[2000]" {
		t.Errorf("movie: %+v", r)
	}
	// RSS: the recent feed.
	search(t, h, "seadex", Query{Type: QueryRSS})
	if q := rec.all()[2].URL.Query(); q.Get("sort") != "-updated" || q.Has("filter") {
		t.Errorf("rss %v", q)
	}
}

func TestUnknownKindAndBadQuery(t *testing.T) {
	n := newNet(t)
	h := newHarness(t, n, testPolicy(), false)
	if _, err := h.svc.Search(context.Background(), "nope", Query{Type: QueryText, Title: "x"}); err == nil {
		t.Error("unknown indexer accepted")
	}
	if _, err := h.svc.Search(context.Background(), "zilean", Query{Type: QueryMovie, IMDb: "nm123"}); !errors.Is(err, ErrBadQuery) {
		t.Errorf("bad IMDb id: %v", err)
	}
	p := testPolicy()
	p.Indexers["x"] = &policy.Indexer{Kind: "newznab", URL: "http://x.test"}
	if _, err := New(p, h.gw, Options{}); err == nil {
		t.Error("unknown kind accepted")
	}
}

func tioSize(f float64, unit string) int64 { return int64(f * tioUnits[unit]) }
