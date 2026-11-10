package arrview

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// Interactive search, grab and push. movie-web (active) and movie-remux (shadow) both hold Alpha in /radarr.
func TestReleaseSearchAndGrab(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	var rs []map[string]any
	e.call("GET", "/radarr/api/v3/release?movieId="+strconv.Itoa(IDBase+1), nil, 200, &rs)
	if len(rs) != 2 {
		t.Fatalf("releases %v", rs)
	}
	good, bad := rs[0], rs[1]
	// Ruddarr's MovieRelease requires these (Movies/MovieReleases.swift:112-166); autobrr and decypharr read
	// approved/rejected/temporarilyRejected/rejections (strings).
	wantKeys(t, "radarr release", good, fields(`guid quality qualityWeight age ageHours ageMinutes size indexerId
		indexer releaseGroup title sceneSource languages approved temporarilyRejected rejected rejections publishDate
		downloadAllowed releaseWeight customFormats customFormatScore magnetUrl infoHash seeders leechers protocol
		movieTitles mappedMovieId tmdbId imdbId movieRequested indexerFlags`), "edition")
	if good["approved"] != true || good["rejected"] != false || good["downloadAllowed"] != true ||
		good["indexerId"].(float64) != 2 || good["guid"] != "theatarr:prowlarr-public:"+strings.Repeat("9", 40) ||
		good["protocol"] != "torrent" || good["customFormatScore"].(float64) != 50 || good["age"].(float64) != 1 ||
		good["mappedMovieId"].(float64) != IDBase+1 || good["leechers"].(float64) != 3 {
		t.Errorf("accepted release %v", good)
	}
	if flags, _ := good["indexerFlags"].([]any); len(flags) != 1 || flags[0] != "G_Freeleech" {
		t.Errorf("indexerFlags %v", good["indexerFlags"])
	}
	rej, _ := bad["rejections"].([]any)
	if bad["approved"] != false || bad["rejected"] != true || len(rej) != 2 ||
		rej[0] != "movie-remux: WEBDL-1080p is not wanted in profile" {
		t.Errorf("rejected release %v", bad)
	}
	if got := strings.Join(e.calls, "|"); got != "movie-remux Alpha|movie-web Alpha" {
		t.Errorf("searches %s", got)
	}
	// Ruddarr and upgrade scripts grab with guid and indexerId only. The release goes to the router for both versions that
	// accepted it: the active one is added, the shadow one is a dry-run plan (no write).
	e.call("POST", "/radarr/api/v3/release", map[string]any{"guid": good["guid"], "indexerId": 2}, 200, nil)
	e.drain()
	if adds := e.adds(); len(adds) != 1 || adds[0] != strings.Repeat("9", 40) {
		t.Errorf("adds %v", adds)
	}
	if n := e.one(`SELECT COUNT(*) FROM download WHERE version_key = 'movie-web' AND source = 'arrview:release:radarr'`); n != 1 {
		t.Errorf("download rows %d", n)
	}
	if n := e.one(`SELECT COUNT(*) FROM router_plan WHERE version_key = 'movie-remux' AND mode = 'shadow'`); n != 1 {
		t.Errorf("shadow plans %d", n)
	}
	if n := e.one(`SELECT COUNT(*) FROM download WHERE version_key = 'movie-remux'`); n != 0 {
		t.Errorf("shadow version got a download")
	}
	// The grab shows in history with our guid (decypharr's reconcile reads data.guid).
	var p struct{ Records []map[string]any }
	e.call("GET", "/radarr/api/v3/history?eventType=1", nil, 200, &p)
	found := false
	for _, h := range p.Records {
		if d, _ := h["data"].(map[string]any); d != nil && d["guid"] == good["guid"] && d["releaseSource"] == "InteractiveSearch" {
			found = true
		}
	}
	if !found {
		t.Errorf("grab not in history: %v", p.Records)
	}
	// decypharr posts the whole resource back; an unknown guid is the arrs' 404.
	e.call("POST", "/radarr/api/v3/release", bad, 200, nil)
	e.call("POST", "/radarr/api/v3/release", map[string]any{"guid": "nope", "indexerId": 2}, 404, nil)
	e.call("POST", "/radarr/api/v3/release", map[string]any{"guid": "nope"}, 400, nil)

	// Sonarr: an episode search; seasonNumber, fullSeason, episodeRequested and mappedEpisodeInfo are written.
	e.calls, rs = nil, nil
	e.call("GET", "/sonarr/api/v3/release?episodeId=102", nil, 200, &rs)
	if len(rs) != 2 || e.calls[0] != "tv-2160p The Show s1e2" {
		t.Fatalf("sonarr releases %v %v", e.calls, rs)
	}
	wantKeys(t, "sonarr release", rs[0], fields(`guid quality qualityWeight age ageHours ageMinutes size indexerId
		indexer releaseGroup title sceneSource languages approved temporarilyRejected rejected rejections publishDate
		downloadAllowed releaseWeight customFormats customFormatScore magnetUrl infoHash seeders leechers protocol
		fullSeason seasonNumber seriesTitle episodeNumbers mappedSeasonNumber mappedEpisodeNumbers mappedSeriesId
		mappedEpisodeInfo tvdbId tvRageId imdbId episodeRequested isDaily isAbsoluteNumbering isPossibleSpecialEpisode
		special indexerFlags`), "absoluteEpisodeNumbers")
	if mi, _ := rs[0]["mappedEpisodeInfo"].([]any); len(mi) != 1 || mi[0].(map[string]any)["id"].(float64) != 102 {
		t.Errorf("mappedEpisodeInfo %v", rs[0]["mappedEpisodeInfo"])
	}
	if rs[0]["indexerFlags"].(float64) != 1 {
		t.Errorf("sonarr indexerFlags %v", rs[0]["indexerFlags"])
	}
	// seriesId alone is the arrs' RSS answer: nothing searched.
	e.calls, rs = nil, nil
	e.call("GET", "/sonarr/api/v3/release?seriesId="+strconv.Itoa(IDBase+10), nil, 200, &rs)
	if len(rs) != 0 || len(e.calls) != 0 {
		t.Errorf("rss %v %v", rs, e.calls)
	}
}

func TestReleaseGrabUsesCachedSearchAcrossBudgetReload(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	var releases []map[string]any
	e.call("GET", "/radarr/api/v3/release?movieId="+strconv.Itoa(IDBase+1), nil, 200, &releases)
	if len(releases) == 0 {
		t.Fatal("search returned no cached release")
	}
	// A reload that only changes a budget keeps the release cache usable.
	next, err := e.pol.Clone()
	if err != nil {
		t.Fatal(err)
	}
	next.Budgets["dmm-key"].Requests++
	e.pol = next
	e.startDev(e.svc.decider)
	searches := len(e.calls)
	e.call("POST", "/radarr/api/v3/release", map[string]any{"guid": releases[0]["guid"], "indexerId": releases[0]["indexerId"]}, 200, nil)
	e.drain()
	if len(e.calls) != searches {
		t.Fatalf("budget reload discarded cached search: %v", e.calls)
	}

	// The cached release is refused clearly when its indexer disappeared from the new runtime.
	next, err = e.pol.Clone()
	if err != nil {
		t.Fatal(err)
	}
	delete(next.Indexers, "prowlarr-public")
	e.pol = next
	e.startDev(e.svc.decider)
	var refused map[string]any
	e.call("POST", "/radarr/api/v3/release", map[string]any{"guid": releases[0]["guid"], "indexerId": releases[0]["indexerId"]}, 409, &refused)
	if msg, _ := refused["message"].(string); !strings.Contains(msg, "release indexer is no longer enabled") {
		t.Fatalf("disabled cached indexer error %q", msg)
	}
}

// autobrr's push (pkg/arr/radarr/types.go L104-119, run_radarr.go L42-77) and an upgrade script's.
func TestReleasePush(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	e.pushClient("autobrr")
	push := func(path string, body map[string]any, want int) []map[string]any {
		t.Helper()
		var out []map[string]any
		e.call("POST", path, body, want, &out)
		return out
	}
	// autobrr, Radarr: imdbId as a number, both protocol fields, an indexer no configured one matches: rejected.
	out := push("/v/movie-web/radarr/api/v3/release/push", map[string]any{"title": "Alpha.2020.2160p.WEB-DL.H265-GRP",
		"downloadUrl": "https://tracker.example/dl/1?passkey=x", "size": 1 << 30, "indexer": "Tracker-A",
		"downloadProtocol": "torrent", "protocol": "torrent", "publishDate": "2026-10-26T11:00:00Z", "tmdbId": 101,
		"imdbId": 101}, 200)
	if len(out) != 1 || out[0]["rejected"] != true || out[0]["approved"] != false ||
		!strings.Contains(out[0]["rejections"].([]any)[0].(string), "Tracker-A") || out[0]["mappedMovieId"].(float64) != IDBase+1 {
		t.Fatalf("unmatched push %v", out)
	}
	// An upgrade script: a hash through a listed indexer by its Prowlarr name, imdbId a number, protocol only: approved and
	// routed.
	out = push("/v/movie-web/radarr/api/v3/release/push", map[string]any{"title": "Alpha.2020.2160p.WEB-DL.H265-GRP",
		"magnetUrl": "magnet:?xt=urn:btih:" + strings.Repeat("7", 40), "infoHash": strings.Repeat("7", 40),
		"protocol": "torrent", "indexer": "Public (Prowlarr)", "size": 1 << 30, "tmdbId": 0, "imdbId": 101,
		"publishDate": "2026-10-26T11:59:55Z"}, 200)
	if out[0]["approved"] != true || out[0]["rejected"] != false || out[0]["mappedMovieId"].(float64) != IDBase+1 ||
		out[0]["indexer"] != "prowlarr-public" {
		t.Fatalf("public push %v", out)
	}
	e.drain()
	if adds := e.adds(); len(adds) != 1 || adds[0] != strings.Repeat("7", 40) {
		t.Errorf("adds %v", adds)
	}
	// A .torrent link only: decided, but never routed (no hash).
	out = push("/v/movie-web/radarr/api/v3/release/push", map[string]any{"title": "Alpha.2020.2160p.WEB-DL.H265-GRP",
		"downloadUrl": "http://x/1.torrent", "protocol": "torrent", "indexer": "prowlarr-public",
		"publishDate": "2026-10-26T11:00:00Z"}, 200)
	if out[0]["approved"] != false || !strings.Contains(strings.Join(toStrings(out[0]["rejections"]), " "), "info hash") {
		t.Errorf("hash-less push %v", out)
	}
	// Mapping by name when no id is given; an unknown title is rejected, not a 400.
	out = push("/radarr/api/v3/release/push", map[string]any{"title": "Nobody.Knows.2019.2160p.WEB-DL-GRP",
		"magnetUrl": "magnet:?xt=urn:btih:" + strings.Repeat("6", 40), "protocol": "torrent",
		"indexer": "prowlarr-public", "publishDate": "2026-10-26T11:00:00Z"}, 200)
	if out[0]["rejected"] != true || out[0]["mappedMovieId"] != nil {
		t.Errorf("unknown title %v", out)
	}
	// Validation: autobrr decodes attemptedValue as a string.
	var bad []map[string]any
	e.call("POST", "/radarr/api/v3/release/push", map[string]any{"title": "", "protocol": "torrent"}, 400, &bad)
	if len(bad) != 1 || bad[0]["propertyName"] != "Title" {
		t.Errorf("validation %v", bad)
	}
	if _, ok := bad[0]["attemptedValue"].(string); !ok && bad[0]["attemptedValue"] != nil {
		t.Errorf("attemptedValue %T", bad[0]["attemptedValue"])
	}
	e.call("POST", "/radarr/api/v3/release/push", map[string]any{"title": "x", "magnetUrl": "magnet:?",
		"protocol": "unknown", "publishDate": "2026-10-26T11:00:00Z"}, 400, &bad)
	// The placeholder of a release added by hand is not a pushed indexer's name: it would skip the version's indexer
	// list. Refused, nothing decided or routed.
	e.call("POST", "/v/movie-web/radarr/api/v3/release/push", map[string]any{"title": "Alpha.2020.2160p.WEB-DL.H265-GRP",
		"magnetUrl": "magnet:?xt=urn:btih:" + strings.Repeat("3", 40), "protocol": "torrent", "size": 1 << 30,
		"indexer": " " + policy.ManualIndexer + " ", "publishDate": "2026-10-26T11:00:00Z", "tmdbId": 101}, 400, &bad)
	if len(bad) != 1 || bad[0]["propertyName"] != "Indexer" {
		t.Errorf("a push naming the manual add placeholder %v", bad)
	}
	e.drain()
	if adds := e.adds(); len(adds) != 1 {
		t.Errorf("adds after a push naming the manual add placeholder %v", adds)
	}
	if n := e.one(`SELECT COUNT(*) FROM download WHERE info_hash = ?`, strings.Repeat("3", 40)); n != 0 {
		t.Errorf("%d downloads for the refused push", n)
	}
	out = push("/sonarr/api/v3/release/push", map[string]any{"title": "The.Show.S01E02.2160p.WEB-DL-GRP",
		"magnetUrl": "magnet:?xt=urn:btih:" + strings.Repeat("5", 40), "protocol": "torrent",
		"indexer": "prowlarr-public", "publishDate": "2026-10-26T11:00:00Z", "tvdbId": 201, "imdbId": "tt0000201"}, 200)
	if out[0]["approved"] != true || out[0]["mappedSeriesId"].(float64) != IDBase+10 {
		t.Errorf("sonarr push %v", out)
	}
	// A shadow version never writes: the remux view.
	writes := e.rt.Writes()
	push("/v/movie-remux/radarr/api/v3/release/push", map[string]any{"title": "Alpha.2020.2160p.WEB-DL.H265-GRP",
		"magnetUrl": "magnet:?xt=urn:btih:" + strings.Repeat("4", 40), "protocol": "torrent", "indexer": "prowlarr-public",
		"publishDate": "2026-10-26T11:00:00Z", "tmdbId": 101}, 200)
	e.drain()
	if e.rt.Writes() != writes+1 { // the Sonarr push above, nothing for the remux view
		t.Errorf("writes %d, before %d", e.rt.Writes(), writes)
	}
	if n := e.one(`SELECT COUNT(*) FROM download WHERE version_key = 'movie-remux'`); n != 0 {
		t.Errorf("shadow downloads %d", n)
	}
}

// The API key is the gate; the pushed indexer is matched to a configured one by name (indexerId is not used), and
// the version's indexer list decides: an unmatched, unnamed or unlisted indexer is rejected, whatever the key.
func TestReleasePushIndexerMatchedByName(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	body := func(hash string, ix map[string]any) map[string]any {
		b := map[string]any{"title": "Alpha.2020.2160p.WEB-DL.H265-GRP", "magnetUrl": "magnet:?xt=urn:btih:" + hash,
			"infoHash": hash, "protocol": "torrent", "publishDate": "2026-10-26T11:00:00Z", "tmdbId": 101}
		for k, v := range ix {
			b[k] = v
		}
		return b
	}
	push := func(what, path, key string, b map[string]any) map[string]any {
		t.Helper()
		rec := e.do("POST", path, key, b)
		var out []map[string]any
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || len(out) != 1 {
			t.Fatalf("%s: %d %s", what, rec.Code, rec.Body.String())
		}
		return out[0]
	}
	rejected := func(what string, out map[string]any, reason string) {
		t.Helper()
		if out["approved"] != false || !strings.Contains(strings.Join(toStrings(out["rejections"]), " "), reason) {
			t.Errorf("%s: not rejected with %q: %v", what, reason, out)
		}
	}
	rejected("by id only", push("by id only", "/v/movie-web/radarr/api/v3/release/push", e.key,
		body(strings.Repeat("7", 40), map[string]any{"indexerId": 2})), "push")
	rejected("unknown name", push("unknown name", "/radarr/api/v3/release/push", radarrViewKey,
		body(strings.Repeat("6", 40), map[string]any{"indexer": "SomeTracker (Prowlarr)"})), "SomeTracker (Prowlarr)")
	// A configured indexer the version does not list: rejected, named by its key.
	rejected("unlisted", push("unlisted", "/v/movie-web/radarr/api/v3/release/push", e.key,
		body(strings.Repeat("5", 40), map[string]any{"indexer": "tracker-example"})), "tracker-example")
	e.drain()
	if adds := e.adds(); len(adds) != 0 {
		t.Errorf("rejected pushes reached a provider: %v", adds)
	}
	// Listed: the view's own key or a Settings key, matched by name.
	out := push("view key", "/radarr/api/v3/release/push", radarrViewKey,
		body(strings.Repeat("1", 40), map[string]any{"indexer": "Public (Prowlarr)"}))
	if out["approved"] != true || out["indexer"] != "prowlarr-public" {
		t.Fatalf("listed indexer: %v", out)
	}
	e.drain()
	if adds := e.adds(); len(adds) != 1 || adds[0] != strings.Repeat("1", 40) {
		t.Errorf("adds %v", adds)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

// GET parse: an upgrade script's scoring oracle (customFormatScore, customFormats[].name, quality, movie.id).
func TestParse(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	var p map[string]any
	e.call("GET", "/v/movie-web/radarr/api/v3/parse?title=Alpha.2020.2160p.WEB-DL.H265-GRP", nil, 200, &p)
	wantKeys(t, "parse", p, fields(`title parsedMovieInfo movie languages customFormats customFormatScore`))
	if p["customFormatScore"].(float64) != 50 || p["movie"].(map[string]any)["id"].(float64) != IDBase+1 {
		t.Errorf("parse %v", p)
	}
	q := p["parsedMovieInfo"].(map[string]any)["quality"].(map[string]any)["quality"].(map[string]any)
	if q["name"] != "WEBDL-2160p" {
		t.Errorf("parsed quality %v", q)
	}
	// Not one of the view's titles: the parse only.
	p = nil
	e.call("GET", "/radarr/api/v3/parse?title=Nobody.2019.1080p.WEB-DL-GRP", nil, 200, &p)
	if _, ok := p["movie"]; ok || p["parsedMovieInfo"] == nil {
		t.Errorf("unmapped %v", p)
	}
	rec := e.do("GET", "/radarr/api/v3/parse?title=", e.key, nil)
	if rec.Code != 204 {
		t.Errorf("blank title %d", rec.Code)
	}
	p = nil
	e.call("GET", "/sonarr/api/v3/parse?title=The.Show.S01E02.2160p.WEB-DL-GRP", nil, 200, &p)
	if p["series"].(map[string]any)["id"].(float64) != IDBase+10 || len(p["episodes"].([]any)) != 1 {
		t.Errorf("sonarr parse %v", p)
	}
}
