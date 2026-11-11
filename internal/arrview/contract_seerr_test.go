package arrview

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/metadata/tvdb"
	"github.com/theatarr/theatarr/internal/metadata/tvrefresh"
)

// seerrTVDB serves a title absent from Theatarr through the real TVDB client, without network access.
type seerrTVDB struct {
	t          *testing.T
	calls      int
	fail       bool
	detailFail bool
	missing    bool
	query      string
	extra      bool
	slow       bool // the search waits out its deadline
	remote     bool // the search result carries New Show's TMDb id (5000)
	many       bool // the search returns eight series, with overview and image
	extended   int  // /extended requests
}

func (f *seerrTVDB) Do(ctx context.Context, target gateway.Target, r *http.Request) (*http.Response, error) {
	f.calls++
	if target.Class != gateway.ClassMetadata || target.Key != "tvdb" {
		f.t.Fatalf("metadata target: %+v", target)
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
		// Refreshes below have their own bounded context too.
		f.t.Fatal("metadata request has no bounded timeout")
	}
	code, body := 200, ""
	switch {
	case strings.HasSuffix(r.URL.Path, "/login"):
		body = `{"status":"success","data":{"token":"fixture-token"}}`
	case f.missing:
		code, body = 404, `{"status":"failure"}`
	case f.fail || f.detailFail && strings.HasSuffix(r.URL.Path, "/extended"):
		code, body = 500, `{"status":"failure","message":"InternalServerError"}`
	case f.slow && strings.HasSuffix(r.URL.Path, "/search"):
		<-ctx.Done()
		return nil, ctx.Err()
	case f.many && strings.HasSuffix(r.URL.Path, "/search"):
		var hits []map[string]any
		for i := 1; i <= 8; i++ {
			hits = append(hits, map[string]any{"tvdb_id": strconv.Itoa(900100 + i), "type": "series",
				"name": fmt.Sprintf("原題 %d", i), "translations": map[string]string{"eng": fmt.Sprintf("Many %d", i)}, "year": "2024", "overview": "原文", "overviews": map[string]string{"eng": fmt.Sprintf("About %d", i)},
				"image_url": fmt.Sprintf("https://artworks.thetvdb.com/banners/%d.jpg", i)})
		}
		b, _ := json.Marshal(map[string]any{"status": "success", "data": hits})
		body = string(b)
	case f.remote && strings.HasSuffix(r.URL.Path, "/search"):
		body = `{"status":"success","data":[{"tvdb_id":"900001","type":"series","name":"New Show","year":"2024",
			"remote_ids":[{"id":"5000","type":12,"sourceName":"TheMovieDB.com"}]}]}`
	case strings.HasSuffix(r.URL.Path, "/search"):
		f.query = r.URL.Query().Get("query")
		if r.URL.Query().Get("type") != "series" {
			f.t.Fatal("search is not restricted to series")
		}
		body = `{"status":"success","data":[{"tvdb_id":"900001","type":"series","name":"New Show","year":"2024"},
			{"tvdb_id":"900001","type":"series","name":"Duplicate"},
			{"tvdb_id":"bad","type":"series"},{"tvdb_id":"900002","type":"movie"}]}`
	case strings.HasSuffix(r.URL.Path, "/extended"):
		f.extended++
		body = `{"status":"success","data":{"id":900001,"name":"New Show","year":"2024",
			"originalLanguage":"eng","status":{"name":"Continuing"},"averageRuntime":45,"defaultSeasonType":1,
			"seasons":[{"number":0,"type":{"id":1}},{"number":1,"type":{"id":1}},
			{"number":2,"type":{"id":1}},{"number":99,"type":{"id":2}}],
			"remoteIds":[{"sourceName":"IMDB","id":"tt900001"},{"sourceName":"TheMovieDB.com","id":"5000"}]}}`
	case strings.Contains(r.URL.Path, "/episodes/default"):
		eps := []map[string]any{}
		for _, season := range []int{0, 1, 2} {
			eps = append(eps, map[string]any{"id": 900010 + season, "seasonNumber": season, "number": 1, "name": "First"})
			if f.extra {
				eps = append(eps, map[string]any{"id": 900020 + season, "seasonNumber": season, "number": 2, "name": "Next"})
			}
		}
		if f.extra {
			eps = append(eps, map[string]any{"id": 900030, "seasonNumber": 3, "number": 1, "name": "New season"})
		}
		b, _ := json.Marshal(map[string]any{"status": "success", "data": map[string]any{"episodes": eps}})
		body = string(b)
	default:
		f.t.Fatalf("unexpected TVDB request %s", r.URL.Path)
	}
	return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func seerrMetadata(t *testing.T, e *env, f *seerrTVDB) *tvrefresh.Service {
	t.Helper()
	delete(e.pol.Metadata.Sources, "thexem")
	delete(e.pol.Metadata.Sources, "sonarr")
	c, err := tvdb.New(tvdb.Options{BaseURL: "https://tvdb.test/v4", APIKey: "fixture-key", Doer: f,
		MinInterval: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	s, err := tvrefresh.New(tvrefresh.Config{Store: e.st, Policy: e.pol, TVDB: c, Now: func() time.Time { return e.now }})
	if err != nil {
		t.Fatal(err)
	}
	e.svc.tvmeta = s
	return s
}

// Both snapshots' addSeries build seasons exclusively from lookup, forcing unselected seasons false. Seerr
// additionally sends monitorNewItems; Overseerr leaves Sonarr's default. No title is seeded for TVDB 900001.
func TestSeerrUncataloguedSeriesSeasons(t *testing.T) {
	t.Parallel()
	for _, clientName := range []string{"seerr", "overseerr"} {
		t.Run(clientName, func(t *testing.T) {
			e := newEnv(t)
			f := &seerrTVDB{t: t}
			meta := seerrMetadata(t, e, f)
			var lookup []seriesResource
			e.call("GET", "/sonarr/api/v3/series/lookup?term=tvdb:900001", nil, 200, &lookup)
			if len(lookup) != 1 {
				t.Fatalf("lookup: %+v", lookup)
			}
			sr := lookup[0]
			if sr.ID != 0 || sr.Title != "New Show" || sr.Year != 2024 || sr.Runtime != 45 ||
				sr.ImdbID != "tt900001" || sr.TmdbID != 5000 || sr.TitleSlug != "new-show" ||
				!reflect.DeepEqual(sr.Seasons, []seasonResource{{SeasonNumber: 0}, {SeasonNumber: 1, Monitored: true}, {SeasonNumber: 2, Monitored: true}}) {
				t.Fatalf("lookup: %+v", sr)
			}
			calls := f.calls
			var profiles []QualityProfileRef
			e.call("GET", "/sonarr/api/v3/qualityprofile", nil, 200, &profiles)
			opts := map[string]any{"tvdbId": 900001, "title": "New Show", "qualityProfileId": profileNamed(t, profiles, "Rules default"),
				"tags": []int64{}, "seasonFolder": true, "monitored": true, "rootFolderPath": "/media/tv", "seriesType": "standard"}
			wantNew := "all"
			if clientName == "seerr" {
				opts["monitorNewItems"], wantNew = "none", "none"
			}
			c := &client{t: t, e: e, base: "/sonarr/api/v3"}
			out := seerrAddSeries(t, c, opts, []int{2}, false)
			if f.calls != calls {
				t.Fatal("lookup cache missed")
			}
			titleID := int64(out["id"].(float64)) - IDBase
			versionID := e.one(`SELECT id FROM version WHERE title_id = ?`, titleID)
			checkIntent := func() {
				t.Helper()
				m, err := catalog.LoadMonitor(context.Background(), e.st.Reader(), versionID)
				if err != nil {
					t.Fatal(err)
				}
				if m.New != wantNew || m.Seasons[0] || m.Seasons[1] || !m.Seasons[2] || len(m.Seasons) < 3 {
					t.Fatalf("stored monitoring: %+v", m)
				}
			}
			checkIntent()
			refresh := func() {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if _, err := meta.RefreshTitle(ctx, titleID, false); err != nil {
					t.Fatal(err)
				}
			}
			refresh()
			f.extra = true
			refresh()
			checkIntent()
			var eps []episodeResource
			e.call("GET", "/sonarr/api/v3/episode?seriesId="+strconv.FormatInt(titleID+IDBase, 10), nil, 200, &eps)
			if len(eps) != 7 {
				t.Fatalf("episodes: %+v", eps)
			}
			for _, ep := range eps {
				want := ep.SeasonNumber == 2 || ep.SeasonNumber == 3 && wantNew == "all"
				if ep.Monitored != want {
					t.Errorf("episode %+v: monitored want %v", ep, want)
				}
			}
		})
	}
}

// Metadata failures keep the local id stub and never add a catalog title.
func TestSeerrLookupMetadataFailure(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	f := &seerrTVDB{t: t, fail: true}
	seerrMetadata(t, e, f)
	var body []seriesResource
	e.call("GET", "/sonarr/api/v3/series/lookup?term=tvdb:900001", nil, 200, &body)
	if len(body) != 1 || body[0].TvdbID != 900001 || body[0].ID != 0 || body[0].Title != "" {
		t.Fatalf("failed metadata lookup: %+v", body)
	}
	calls := f.calls
	e.call("GET", "/sonarr/api/v3/series/lookup?term=tvdb:900001", nil, 200, nil)
	if f.calls != calls {
		t.Fatal("failure cache missed")
	}
	e.now = e.now.Add(31 * time.Second)
	e.call("GET", "/sonarr/api/v3/series/lookup?term=tvdb:900001", nil, 200, nil)
	if f.calls == calls {
		t.Fatal("failure cache did not expire")
	}
	var n int
	if err := e.st.Reader().QueryRow(`SELECT COUNT(*) FROM title WHERE tvdb_id = 900001`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("titles %d, %v", n, err)
	}
}

func TestSeriesLookupName(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"success", "search failure", "details failure", "not found"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t)
			f := &seerrTVDB{t: t, fail: mode == "search failure", detailFail: mode == "details failure", missing: mode == "not found"}
			seerrMetadata(t, e, f)
			var out []seriesResource
			// The catalog's match comes first; TVDB's search adds what the catalog lacks, or nothing when it fails.
			e.call("GET", "/sonarr/api/v3/series/lookup?term=The%20Show", nil, 200, &out)
			want := 2
			if mode == "search failure" || mode == "not found" {
				want = 1
			}
			if len(out) != want || out[0].ID != IDBase+10 || f.calls == 0 ||
				want == 2 && (out[1].ID != 0 || out[1].TvdbID != 900001 || f.query != "The Show") {
				t.Fatalf("local lookup: %+v, calls %d", out, f.calls)
			}
			out = nil
			e.call("GET", "/sonarr/api/v3/series/lookup?term=New%20%26%20Show", nil, 200, &out)
			if mode == "search failure" || mode == "not found" {
				if len(out) != 0 {
					t.Fatalf("search failure: %+v", out)
				}
			} else if len(out) != 1 || out[0].ID != 0 || out[0].TvdbID != 900001 || out[0].Title != "New Show" || out[0].Year != 2024 || f.query != "New & Show" {
				t.Fatalf("name lookup: %+v, query %q", out, f.query)
			} else if mode == "success" && len(out[0].Seasons) != 3 {
				t.Fatalf("metadata seasons: %+v", out[0])
			}
			if mode == "not found" {
				out = nil
				e.call("GET", "/sonarr/api/v3/series/lookup?term=tvdb:900001", nil, 200, &out)
				if len(out) != 1 || out[0].TvdbID != 900001 || out[0].ID != 0 {
					t.Fatalf("missing id fallback: %+v", out)
				}
			}
			if n := e.one(`SELECT COUNT(*) FROM title WHERE tvdb_id = 900001`); n != 0 {
				t.Fatalf("lookup created %d titles", n)
			}
		})
	}
}

// A TVDB search that times out leaves the catalog's matches as the answer.
func TestSeriesLookupNameSearchTimeout(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	seerrMetadata(t, e, &seerrTVDB{t: t, slow: true})
	var out []seriesResource
	e.call("GET", "/sonarr/api/v3/series/lookup?term=The%20Show", nil, 200, &out)
	if len(out) != 1 || out[0].ID != IDBase+10 {
		t.Fatalf("lookup: %+v", out)
	}
}

// TVDB's results for series the catalog holds come back as those series, once: one known only by its TMDb id that the
// name matched, and one under another name that the name did not match.
func TestSeriesLookupNameOutsideHeld(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, update, term string
		remote             bool
	}{
		{"tmdb only, matched by name", `UPDATE title SET tvdb_id = NULL, tmdb_id = 5000 WHERE id = 10`, "The%20Show", true},
		{"tvdb id, other name", `UPDATE title SET tvdb_id = 900001 WHERE id = 10`, "New%20Show", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			f := &seerrTVDB{t: t, remote: c.remote}
			seerrMetadata(t, e, f)
			e.exec(c.update)
			var out []seriesResource
			e.call("GET", "/sonarr/api/v3/series/lookup?term="+c.term, nil, 200, &out)
			if len(out) != 1 || out[0].ID != IDBase+10 || out[0].Title != "The Show" || f.calls == 0 {
				t.Fatalf("lookup: %+v, calls %d", out, f.calls)
			}
		})
	}
}

// A name lookup asks TVDB for the details (seasons) of only the first few series the catalog lacks, in TVDB's order;
// every hit carries the search result's overview and poster.
func TestSeriesLookupNameHydratesFirstHits(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	f := &seerrTVDB{t: t, many: true}
	seerrMetadata(t, e, f)
	var out []seriesResource
	e.call("GET", "/sonarr/api/v3/series/lookup?term=Many", nil, 200, &out)
	if len(out) != 8 || f.extended != outsideHydrate {
		t.Fatalf("lookup: %d results, %d detail requests", len(out), f.extended)
	}
	for i, sr := range out {
		want := fmt.Sprintf("https://artworks.thetvdb.com/banners/%d.jpg", i+1)
		if sr.TvdbID != 900101+i || sr.Overview != fmt.Sprintf("About %d", i+1) || sr.RemotePoster != want ||
			len(sr.Images) != 1 {
			t.Errorf("hit %d: %+v", i, sr)
		}
		if i >= outsideHydrate && sr.Title != fmt.Sprintf("Many %d", i+1) {
			t.Errorf("hit %d: title %q", i, sr.Title)
		}
		if hydrated := len(sr.Seasons) > 0; hydrated != (i < outsideHydrate) {
			t.Errorf("hit %d: seasons %v", i, sr.Seasons)
		}
	}
}
