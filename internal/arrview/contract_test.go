package arrview

// Contract tests: the request sequences Seerr, Homepage and decypharr send to an arr, replayed against the views.
// Sources (read-only clones in ref/):
//   - Seerr c57dca9: server/api/servarr/base.ts (apikey as a query parameter on every call, L96-107),
//     radarr.ts (addMovie L118-216, removeMovie L237-268), sonarr.ts (addSeries L208-330, removeSeries L436-466),
//     server/subscriber/MediaRequestSubscriber.ts (tags L322-365, options L367-378 and L708-726),
//     server/routes/settings/radarr.ts L44-66 and sonarr.ts L40-60 (the settings test),
//     server/lib/downloadtracker.ts (RefreshMonitoredDownloads, then queue), scanners and availabilitySync.ts.
//   - Homepage 20bfd1c: src/widgets/radarr/widget.js L5-35, src/widgets/sonarr/widget.js L74-100.
//   - decypharr 917cfa2: pkg/arr/client.go L140-160, media.go L44-220, content.go L60-140.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// client sends requests the way a caller does: Seerr and Homepage put the key in the apikey query parameter,
// decypharr in X-Api-Key.
type client struct {
	t      *testing.T
	e      *env
	base   string // e.g. /radarr/api/v3
	header bool
	calls  []string
}

func (c *client) do(method, path string, query url.Values, body any, out any) int {
	c.t.Helper()
	if query == nil {
		query = url.Values{}
	}
	if !c.header {
		query.Set("apikey", c.e.key)
	}
	u := c.base + path
	if q := query.Encode(); q != "" {
		u += "?" + q
	}
	var rd *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	} else {
		rd = strings.NewReader("")
	}
	r := httptest.NewRequest(method, u, rd)
	r.Header.Set("Content-Type", "application/json")
	if c.header {
		r.Header.Set("X-Api-Key", c.e.key)
	}
	rec := httptest.NewRecorder()
	c.e.mux.ServeHTTP(rec, r)
	c.calls = append(c.calls, method+" "+path+" "+strconv.Itoa(rec.Code))
	if out != nil && rec.Code < 300 && rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			c.t.Fatalf("%s %s: %v\n%s", method, path, err, rec.Body.String())
		}
	}
	return rec.Code
}

func (c *client) must(method, path string, query url.Values, body any, want int, out any) {
	c.t.Helper()
	if got := c.do(method, path, query, body, out); got != want {
		c.t.Fatalf("%s %s: %d, want %d (calls: %v)", method, path, got, want, c.calls)
	}
}

// seerrSettingsTest is the settings "test" Seerr runs when a server is saved (routes/settings/radarr.ts L44-66):
// system/status for version and urlBase, then profiles, root folders and tags.
func seerrSettingsTest(t *testing.T, c *client, wantMajor string) (profiles []QualityProfileRef, roots []RootRef) {
	var st struct {
		Version string `json:"version"`
		URLBase string `json:"urlBase"`
	}
	c.must("GET", "/system/status", nil, nil, 200, &st)
	if !strings.HasPrefix(st.Version, wantMajor+".") {
		t.Fatalf("version %q: Seerr parses the major", st.Version)
	}
	c.must("GET", "/qualityProfile", nil, nil, 200, &profiles)
	c.must("GET", "/rootfolder", nil, nil, 200, &roots)
	var tags []tagResource
	c.must("GET", "/tag", nil, nil, 200, &tags)
	if len(profiles) == 0 || len(roots) == 0 {
		t.Fatalf("profiles %v roots %v", profiles, roots)
	}
	return profiles, roots
}

type QualityProfileRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type RootRef struct {
	ID   int64  `json:"id"`
	Path string `json:"path"`
}

func profileNamed(t *testing.T, ps []QualityProfileRef, name string) int64 {
	for _, p := range ps {
		if p.Name == name {
			return p.ID
		}
	}
	t.Fatalf("no profile %q in %v", name, ps)
	return 0
}

// seerrUserTag is MediaRequestSubscriber's tagRequests block: find "<userId>-" among the tags, else create it.
func seerrUserTag(c *client, userID int, name string) int64 {
	var tags []tagResource
	c.must("GET", "/tag", nil, nil, 200, &tags)
	for _, tg := range tags {
		if strings.HasPrefix(tg.Label, strconv.Itoa(userID)+"-") {
			return tg.ID
		}
	}
	var tg tagResource
	c.must("POST", "/tag", nil, map[string]any{"label": strconv.Itoa(userID) + "-" + name}, 201, &tg)
	return tg.ID
}

// seerrAddMovie is RadarrAPI.addMovie (radarr.ts L118-216): lookup by TMDb id; skip if hasFile; PUT if it exists
// unmonitored; return it if it exists monitored (search if not available... asked); else POST. Returns the movie
// Seerr stores (its id becomes externalServiceId).
func seerrAddMovie(t *testing.T, c *client, opts map[string]any, searchNow bool) map[string]any {
	t.Helper()
	tmdb := opts["tmdbId"].(int)
	var lookup []map[string]any
	c.must("GET", "/movie/lookup", url.Values{"term": {"tmdb:" + strconv.Itoa(tmdb)}}, nil, 200, &lookup)
	if len(lookup) == 0 {
		t.Fatal("Movie not found: Seerr fails the request on an empty lookup")
	}
	movie := lookup[0]
	if movie["hasFile"] == true {
		return movie
	}
	if movie["id"] != nil && movie["monitored"] == false {
		body := map[string]any{}
		for k, v := range movie {
			body[k] = v
		}
		for k, v := range opts {
			body[k] = v
		}
		body["titleSlug"] = strconv.Itoa(tmdb)
		body["addOptions"] = map[string]any{"searchForMovie": searchNow}
		var out map[string]any
		c.must("PUT", "/movie", nil, body, 202, &out)
		if out["monitored"] != true {
			t.Fatal("Failed to update existing movie in Radarr")
		}
		if searchNow && out["isAvailable"] == true {
			c.must("POST", "/command", nil, map[string]any{"name": "MoviesSearch", "movieIds": []any{out["id"]}}, 201, nil)
		}
		return out
	}
	if movie["id"] != nil {
		if searchNow && movie["hasFile"] != true && movie["isAvailable"] == true {
			c.must("POST", "/command", nil, map[string]any{"name": "MoviesSearch", "movieIds": []any{movie["id"]}},
				201, nil)
		}
		return movie
	}
	body := map[string]any{"titleSlug": strconv.Itoa(tmdb), "addOptions": map[string]any{"searchForMovie": searchNow}}
	for k, v := range opts {
		body[k] = v
	}
	var out map[string]any
	c.must("POST", "/movie", nil, body, 201, &out)
	if out["id"] == nil {
		t.Fatal("Failed to add movie to Radarr: no id")
	}
	return out
}

func TestSeerrRadarrContract(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	c := &client{t: t, e: e, base: "/radarr/api/v3"}
	profiles, roots := seerrSettingsTest(t, c, "6")
	def := profileNamed(t, profiles, "Rules default")

	// A request for a new movie (TMDb 120), tagRequests on.
	tag := seerrUserTag(c, 1, "alice")
	opts := map[string]any{"profileId": def, "qualityProfileId": def, "rootFolderPath": roots[0].Path,
		"minimumAvailability": "released", "title": "Kappa", "tmdbId": 120, "year": 2025, "monitored": true,
		"tags": []int64{tag}}
	movie := seerrAddMovie(t, c, opts, true)
	ext := int64(movie["id"].(float64))
	if movie["titleSlug"] != "120" || movie["monitored"] != true {
		t.Errorf("added %v", movie)
	}
	// The add's searchForMovie was recorded and did nothing.
	var cmds []map[string]any
	c.must("GET", "/command", nil, nil, 200, &cmds)
	if len(cmds) != 1 || cmds[0]["name"] != "MoviesSearch" || cmds[0]["status"] != "completed" {
		t.Errorf("commands %v", cmds)
	}
	// A second request user tag lookup finds the tag (no second POST).
	if again := seerrUserTag(c, 1, "alice"); again != tag {
		t.Errorf("user tag %d, then %d", tag, again)
	}

	// Download tracker: RefreshMonitoredDownloads, then the first queue page.
	c.must("POST", "/command", nil, map[string]any{"name": "RefreshMonitoredDownloads"}, 201, nil)
	var q struct {
		Records []any `json:"records"`
	}
	c.must("GET", "/queue", url.Values{"includeEpisode": {"true"}}, nil, 200, &q)

	// Scanner: the full list, matched by tmdbId; processing = monitored && !hasFile.
	var all []map[string]any
	c.must("GET", "/movie", nil, nil, 200, &all)
	found := false
	for _, m := range all {
		if m["tmdbId"].(float64) == 120 {
			found = true
			if m["id"].(float64) != float64(ext) || m["monitored"] != true || m["hasFile"] != false {
				t.Errorf("scanner row %v", m)
			}
			if tags := m["tags"].([]any); len(tags) != 1 || tags[0].(float64) != float64(tag) {
				t.Errorf("tags passed through: %v", tags)
			}
		}
	}
	if !found {
		t.Fatal("scanner: added movie not listed")
	}
	// Availability sync: by the stored id.
	var byID map[string]any
	c.must("GET", "/movie/"+strconv.FormatInt(ext, 10), nil, nil, 200, &byID)

	// A request for a movie the view already has with a file (Alpha): Seerr returns early.
	alpha := seerrAddMovie(t, c, map[string]any{"tmdbId": 101, "qualityProfileId": def}, true)
	if alpha["hasFile"] != true {
		t.Errorf("Alpha %v", alpha["hasFile"])
	}
	// Exists with a file but unmonitored (Beta): Seerr still returns early on hasFile.
	if beta := seerrAddMovie(t, c, map[string]any{"tmdbId": 102, "qualityProfileId": def}, true); beta["monitored"] != false {
		t.Errorf("Beta was changed: %v", beta["monitored"])
	}
	// Exists without a file, unmonitored (Gamma, unmonitored here): the lookup object goes back with PUT.
	e.exec(`UPDATE version SET monitored = 0 WHERE id = 4`)
	animeP := profileNamed(t, profiles, "Anime")
	gamma := seerrAddMovie(t, c, map[string]any{"tmdbId": 103, "qualityProfileId": animeP, "profileId": animeP,
		"title": "Gamma", "year": 2027, "monitored": true, "tags": []int64{tag}, "rootFolderPath": roots[2].Path,
		"minimumAvailability": "released"}, true)
	if gamma["monitored"] != true || len(gamma["tags"].([]any)) != 1 {
		t.Errorf("Gamma %v %v", gamma["monitored"], gamma["tags"])
	}
	if got := e.versionsOf(3); len(got) != 1 || !got["anime-movie"] {
		t.Errorf("Gamma versions %v", got)
	}
	// Seerr's stored id from the replaced instance still answers (alias table).
	c.must("GET", "/movie/7", nil, nil, 200, &byID)
	if byID["tmdbId"].(float64) != 101 {
		t.Errorf("alias 7: %v", byID["tmdbId"])
	}

	// Remove (radarr.ts removeMovie): lookup, then DELETE by its id; afterwards 404, which Seerr reads as gone.
	var lk []map[string]any
	c.must("GET", "/movie/lookup", url.Values{"term": {"tmdb:120"}}, nil, 200, &lk)
	id := strconv.FormatInt(int64(lk[0]["id"].(float64)), 10)
	c.must("DELETE", "/movie/"+id, url.Values{"deleteFiles": {"true"}, "addImportExclusion": {"false"}}, nil, 200, nil)
	c.must("GET", "/movie/"+id, nil, nil, 404, nil)
	lk = nil
	c.must("GET", "/movie/lookup", url.Values{"term": {"tmdb:120"}}, nil, 200, &lk)
	if lk[0]["id"] != nil {
		t.Error("removed movie still has an id in lookup: Seerr would not re-add it")
	}
	// Requested again after removal: added back.
	again := seerrAddMovie(t, c, opts, false)
	if again["id"].(float64) != float64(ext) {
		t.Errorf("re-add id %v, want %d", again["id"], ext)
	}
	t.Logf("Seerr Radarr calls: %v", c.calls)
}

// seerrAddSeries is SonarrAPI.addSeries (sonarr.ts L208-330).
func seerrAddSeries(t *testing.T, c *client, opts map[string]any, seasons []int, searchNow bool) map[string]any {
	t.Helper()
	tvdb := opts["tvdbId"].(int)
	var lookup []map[string]any
	c.must("GET", "/series/lookup", url.Values{"term": {"tvdb:" + strconv.Itoa(tvdb)}}, nil, 200, &lookup)
	if len(lookup) == 0 {
		t.Fatal("Series not found")
	}
	series := lookup[0]
	want := map[int]bool{}
	for _, s := range seasons {
		want[s] = true
	}
	if series["id"] != nil {
		series["monitored"] = opts["monitored"]
		tags := series["tags"].([]any)
		for _, tg := range opts["tags"].([]int64) {
			tags = append(tags, tg)
		}
		series["tags"] = tags
		for _, s := range series["seasons"].([]any) {
			sm := s.(map[string]any)
			if want[int(sm["seasonNumber"].(float64))] {
				sm["monitored"] = true
			}
		}
		var out map[string]any
		c.must("PUT", "/series", nil, series, 202, &out)
		if out["id"] == nil {
			t.Fatal("Failed to update series in Sonarr")
		}
		var eps []map[string]any
		c.must("GET", "/episode", url.Values{"seriesId": {strconv.Itoa(int(out["id"].(float64)))}}, nil, 200, &eps)
		var ids []any
		for _, ep := range eps {
			if want[int(ep["seasonNumber"].(float64))] && ep["monitored"] == false {
				ids = append(ids, ep["id"])
			}
		}
		if len(ids) > 0 {
			c.must("PUT", "/episode/monitor", nil, map[string]any{"episodeIds": ids, "monitored": true}, 202, nil)
		}
		if searchNow {
			c.must("POST", "/command", nil, map[string]any{"name": "MissingEpisodeSearch", "seriesId": out["id"]}, 201, nil)
		}
		return out
	}
	var built []map[string]any
	for _, s := range series["seasons"].([]any) {
		n := int(s.(map[string]any)["seasonNumber"].(float64))
		built = append(built, map[string]any{"seasonNumber": n, "monitored": want[n]})
	}
	body := map[string]any{"seasons": built, "addOptions": map[string]any{"ignoreEpisodesWithFiles": true,
		"searchForMissingEpisodes": searchNow}}
	for k, v := range opts {
		body[k] = v
	}
	var out map[string]any
	c.must("POST", "/series", nil, body, 201, &out)
	if out["id"] == nil {
		t.Fatal("Failed to add series to Sonarr")
	}
	return out
}

func TestSeerrSonarrContract(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	c := &client{t: t, e: e, base: "/sonarr/api/v3"}
	profiles, roots := seerrSettingsTest(t, c, "4")
	def := profileNamed(t, profiles, "Rules default")
	anime := profileNamed(t, profiles, "Anime")
	// Sonarr 4: Seerr asks for no language profiles (major > 3).
	tag := seerrUserTag(c, 1, "alice")

	// New anime series with Seerr's anime settings (activeAnimeProfileId, activeAnimeDirectory, animeSeriesType).
	opts := map[string]any{"tvdbId": 401, "title": "Mecha", "qualityProfileId": anime, "languageProfileId": 1,
		"tags": []int64{tag}, "seasonFolder": true, "monitored": true, "monitorNewItems": "all",
		"rootFolderPath": roots[1].Path, "seriesType": "anime"}
	s := seerrAddSeries(t, c, opts, []int{1}, true)
	mid := int64(s["id"].(float64)) - IDBase
	if got := e.versionsOf(mid); len(got) != 1 || !got["anime-tv"] {
		t.Errorf("anime series versions %v", got)
	}

	// Existing series (The Show): PUT with the requested seasons monitored, then episode monitor and a search.
	e.call("PUT", "/sonarr/api/v3/episode/monitor", map[string]any{"episodeIds": []int{102}, "monitored": false}, 202, nil)
	show := seerrAddSeries(t, c, map[string]any{"tvdbId": 201, "qualityProfileId": def, "monitored": true,
		"tags": []int64{tag}}, []int{1}, true)
	var eps []map[string]any
	c.must("GET", "/episode", url.Values{"seriesId": {strconv.Itoa(int(show["id"].(float64)))},
		"seasonNumber": {"1"}}, nil, 200, &eps)
	for _, ep := range eps {
		if ep["monitored"] != true {
			t.Errorf("episode %v not monitored after the request", ep["id"])
		}
	}
	var cmds []map[string]any
	c.must("GET", "/command", nil, nil, 200, &cmds)
	n := 0
	for _, cm := range cmds {
		if cm["name"] == "MissingEpisodeSearch" {
			n++
			if cm["status"] != "completed" {
				t.Errorf("search %v", cm)
			}
		}
	}
	if n != 2 {
		t.Errorf("%d MissingEpisodeSearch commands, want 2 (the add's and the request's)", n)
	}

	// Scanner and availability sync.
	var all []map[string]any
	c.must("GET", "/series", nil, nil, 200, &all)
	if len(all) != 2 {
		t.Errorf("series %d", len(all))
	}
	c.must("GET", "/series/3", nil, nil, 200, nil) // the replaced instance's id

	// Remove.
	var lk []map[string]any
	c.must("GET", "/series/lookup", url.Values{"term": {"tvdb:401"}}, nil, 200, &lk)
	id := strconv.FormatInt(int64(lk[0]["id"].(float64)), 10)
	c.must("DELETE", "/series/"+id, url.Values{"deleteFiles": {"true"}, "addImportExclusion": {"false"}}, nil, 200, nil)
	c.must("GET", "/series/"+id, nil, nil, 404, nil)
	t.Logf("Seerr Sonarr calls: %v", c.calls)
}

func TestHomepageContract(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// Radarr widget: movie, queue/status, queue/details, always all three.
	r := &client{t: t, e: e, base: "/radarr/api/v3"}
	var movies []struct {
		ID          int64  `json:"id"`
		Title       string `json:"title"`
		Monitored   bool   `json:"monitored"`
		HasFile     bool   `json:"hasFile"`
		IsAvailable bool   `json:"isAvailable"`
	}
	r.must("GET", "/movie", nil, nil, 200, &movies)
	wanted, have, missing := 0, 0, 0
	for _, m := range movies {
		if m.Monitored && !m.HasFile && m.IsAvailable {
			wanted++
		}
		if m.HasFile {
			have++
		}
		if m.Monitored && !m.HasFile {
			missing++
		}
	}
	if wanted != 0 || have != 2 || missing != 1 {
		t.Errorf("radarr widget: wanted %d have %d missing %d", wanted, have, missing)
	}
	var qs map[string]any
	r.must("GET", "/queue/status", nil, nil, 200, &qs)
	if _, ok := qs["totalCount"]; !ok {
		t.Error("queue/status: no totalCount (widget validates it)")
	}
	var details []any
	r.must("GET", "/queue/details", nil, nil, 200, &details)

	// Sonarr widget: wanted/missing and queue (totalRecords), series, queue/details.
	s := &client{t: t, e: e, base: "/sonarr/api/v3"}
	for _, p := range []string{"/wanted/missing", "/queue"} {
		var pg map[string]any
		s.must("GET", p, nil, nil, 200, &pg)
		if _, ok := pg["totalRecords"]; !ok {
			t.Errorf("%s: no totalRecords", p)
		}
	}
	var series []struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
	}
	s.must("GET", "/series", nil, nil, 200, &series)
	if len(series) != 1 || series[0].Title != "The Show" {
		t.Errorf("series %v", series)
	}
	s.must("GET", "/queue/details", nil, nil, 200, &details)
}

func TestSeerrMovieStatusUsesResolutionForLinkedFiles(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedFiles()
	c := &client{t: t, e: e, base: "/v/movie-web/radarr/api/v3"}
	type mediaInfo struct {
		Resolution            string   `json:"resolution"`
		AudioBitrate          *int     `json:"audioBitrate"`
		AudioChannels         *float64 `json:"audioChannels"`
		AudioStreamCount      *int     `json:"audioStreamCount"`
		VideoBitDepth         *int     `json:"videoBitDepth"`
		VideoBitrate          *int     `json:"videoBitrate"`
		VideoFps              *float64 `json:"videoFps"`
		VideoDynamicRangeType string   `json:"videoDynamicRangeType"`
	}
	type movieStatus struct {
		HasFile   bool `json:"hasFile"`
		MovieFile *struct {
			MediaInfo *mediaInfo `json:"mediaInfo"`
		} `json:"movieFile"`
	}
	checkMediaInfo := func(where string, info *mediaInfo, wantResolution string) {
		t.Helper()
		if info == nil || info.Resolution != wantResolution || info.AudioBitrate == nil || info.AudioChannels == nil ||
			info.AudioStreamCount == nil || info.VideoBitDepth == nil || info.VideoBitrate == nil || info.VideoFps == nil {
			t.Fatalf("%s mediaInfo is incomplete for Seerr/Ruddarr: %+v", where, info)
		}
	}
	var movie movieStatus
	c.must("GET", "/movie/"+strconv.FormatInt(IDBase+1, 10), nil, nil, 200, &movie)
	if !movie.HasFile || movie.MovieFile == nil {
		t.Fatalf("Seerr status lookup for linked movie: %+v", movie)
	}
	checkMediaInfo("Seerr movie status", movie.MovieFile.MediaInfo, "3840x2160")
	if width, _, ok := strings.Cut(movie.MovieFile.MediaInfo.Resolution, "x"); !ok || width != "3840" {
		t.Errorf("Seerr cannot classify the linked file as 4K: %q", movie.MovieFile.MediaInfo.Resolution)
	}
	var files []struct {
		MediaInfo *mediaInfo `json:"mediaInfo"`
	}
	c.must("GET", "/moviefile", url.Values{"movieId": {strconv.FormatInt(IDBase+1, 10)}}, nil, 200, &files)
	if len(files) != 1 {
		t.Fatalf("Ruddarr movie-file list: %d files", len(files))
	}
	checkMediaInfo("Ruddarr movie-file list", files[0].MediaInfo, "3840x2160")

	// A probed value wins even when it conflicts with the parsed release quality.
	e.exec(`UPDATE version_file SET media_info = '{"resolution":"1280x720","audioBitrate":null,"videoDynamicRangeType":"HDR10"}' WHERE id = 1`)
	movie = movieStatus{}
	main := &client{t: t, e: e, base: "/radarr/api/v3"}
	main.must("GET", "/movie/"+strconv.FormatInt(IDBase+1, 10), nil, nil, 200, &movie)
	checkMediaInfo("probed movie", movie.MovieFile.MediaInfo, "1280x720")
	if *movie.MovieFile.MediaInfo.AudioBitrate != 0 || movie.MovieFile.MediaInfo.VideoDynamicRangeType != "HDR10" {
		t.Errorf("probe metadata was lost: %+v", movie.MovieFile.MediaInfo)
	}
}

func TestQualityResolutionText(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		resolution int
		want       string
	}{{2160, "3840x2160"}, {1080, "1920x1080"}, {720, "1280x720"}, {576, "720x576"}, {480, "720x480"}, {0, ""}} {
		if got := qualityResolutionText(tt.resolution); got != tt.want {
			t.Errorf("qualityResolutionText(%d) = %q, want %q", tt.resolution, got, tt.want)
		}
	}
}

func TestDecypharrContract(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for _, fl := range []string{"radarr", "sonarr"} {
		c := &client{t: t, e: e, base: "/" + fl + "/api/v3", header: true}
		var st struct {
			AppName string `json:"appName"`
		}
		c.must("GET", "/system/status", nil, nil, 200, &st)
		if strings.ToLower(st.AppName) != fl {
			t.Errorf("appName %q", st.AppName)
		}
		var cfg struct {
			ID                              int  `json:"id"`
			EnableCompletedDownloadHandling bool `json:"enableCompletedDownloadHandling"`
		}
		c.must("GET", "/config/downloadclient", nil, nil, 200, &cfg)
		if cfg.ID != 1 || !cfg.EnableCompletedDownloadHandling {
			t.Errorf("config %+v", cfg)
		}
		c.must("POST", "/command", nil, map[string]any{"name": "RefreshMonitoredDownloads"}, 201, nil)
	}
	// Movies: by TMDb id, by id; search a movie and read the command back.
	c := &client{t: t, e: e, base: "/radarr/api/v3", header: true}
	var ms []struct {
		ID        int64  `json:"id"`
		Path      string `json:"path"`
		MovieFile *struct {
			ID   int64  `json:"id"`
			Path string `json:"path"`
		} `json:"movieFile"`
	}
	c.must("GET", "/movie", url.Values{"tmdbId": {"101"}}, nil, 200, &ms)
	if len(ms) != 1 || ms[0].MovieFile == nil || ms[0].MovieFile.Path == "" {
		t.Fatalf("movie by tmdbId %+v", ms)
	}
	var cmd struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
		Body   struct {
			MovieIDs []int64 `json:"movieIds"`
		} `json:"body"`
	}
	c.must("POST", "/command", nil, map[string]any{"name": "MoviesSearch", "movieIds": []int64{ms[0].ID}}, 201, &cmd)
	if cmd.Status != "completed" || len(cmd.Body.MovieIDs) != 1 || cmd.Body.MovieIDs[0] != ms[0].ID {
		t.Errorf("command %+v", cmd)
	}
	var list []struct{ ID int64 }
	c.must("GET", "/command", nil, nil, 200, &list)
	if len(list) == 0 || list[0].ID != cmd.ID {
		t.Errorf("command list %+v", list)
	}
	// Series: by TVDB id, episodes by series.
	s := &client{t: t, e: e, base: "/sonarr/api/v3", header: true}
	var ss []struct{ ID int64 }
	s.must("GET", "/series", url.Values{"tvdbId": {"201"}}, nil, 200, &ss)
	var eps []struct {
		ID            int64 `json:"id"`
		EpisodeFileID int64 `json:"episodeFileId"`
		SeasonNumber  int   `json:"seasonNumber"`
		EpisodeNumber int   `json:"episodeNumber"`
	}
	s.must("GET", "/episode", url.Values{"seriesId": {strconv.FormatInt(ss[0].ID, 10)}}, nil, 200, &eps)
	if len(eps) != 4 {
		t.Errorf("episodes %+v", eps)
	}
	s.must("POST", "/command", nil, map[string]any{"name": "EpisodeSearch", "episodeIds": []int64{102}}, 201, nil)
	if code := s.do("GET", "/command/1", nil, nil, nil); code != http.StatusOK {
		t.Errorf("command/1: %d", code)
	}
}
