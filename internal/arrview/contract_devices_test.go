package arrview

// Contract tests for the phone apps, autobrr, decypharr's repair and upgrade scripts: each caller's request sequence,
// replayed from its source against the views, with the response fields it decodes. Sources (read-only, ref/):
//   - Ruddarr d07829d: Ruddarr/Dependencies/API/API+Live.swift (every request, L11-341), models for required keys
//     (Movies/Movie.swift L4-91, Movies/MovieReleases.swift L112-166, Series/SeriesRelease.swift L3-53,
//     Series/Episode.swift L3-41, Queue/QueueItem.swift L4-94, MediaHistory.swift L3-34, MediaFile.swift L3-35,
//     Queue/ImportableFile.swift L3-48, Instances/Instance.swift L198-287, Instances/InstanceNotification.swift),
//     commands Instances/InstanceCommand.swift L1-106, webhook Instances/InstanceWebhook.swift L25-143.
//   - LunaSea 6ee0bf9: lunasea/lib/api/radarr/commands/*, lunasea/lib/api/sonarr/controllers/* (apikey as a query
//     parameter, radarr.dart L54-73), modules/dashboard/core/api/api.dart L84-186 (calendars).
//   - autobrr 58bad4f: pkg/arr/radarr/radarr.go L75-170 and types.go L104-138, pkg/arr/sonarr/sonarr.go L76-185
//     and types.go L14-52, internal/action/run_radarr.go L18-87, run_sonarr.go L16-75, internal/list.
//   - decypharr 917cfa2: pkg/arr/queue.go L24-220, history.go L21-228, release.go L15-112, import.go L12-125,
//     media.go L35-131, content.go L204-238, library.go L27-186.
//   - an upgrade script (a client that grabs debrid upgrades through a per-version view): parse, release, release/push,
//     tags and the editor, moviefile/episodefile and manualimport.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/notify"
)

// has fails unless obj has every key (the fields a Swift model declares non-optional).
func has(t *testing.T, what string, obj any, keys string) {
	t.Helper()
	m, ok := obj.(map[string]any)
	if !ok {
		t.Fatalf("%s: not an object: %T", what, obj)
	}
	for _, k := range strings.Fields(keys) {
		if v, ok := m[k]; !ok || v == nil {
			t.Errorf("%s: %s missing or null", what, k)
		}
	}
}

func devClient(t *testing.T, e *devEnv, base string, header bool) *client {
	return &client{t: t, e: e.env, base: base + "/api/v3", header: header}
}

func TestRuddarrRadarrContract(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	e.seedDownloads()
	e.seedFiles()
	c := devClient(t, e, "/radarr", true) // Ruddarr sends X-Api-Key (API.swift L149-161)
	var st map[string]any
	c.must("GET", "/system/status", nil, nil, 200, &st) // L248-252
	if st["appName"] != "Radarr" {
		t.Fatalf("appName %v (Ruddarr compares it with the instance type)", st["appName"])
	}
	has(t, "status", st, "appName instanceName version")
	var roots, profiles, disks, tags []any
	c.must("GET", "/rootfolder", nil, nil, 200, &roots) // L253-257
	for _, r := range roots {
		has(t, "rootfolder", r, "id accessible")
	}
	c.must("GET", "/qualityprofile", nil, nil, 200, &profiles) // L258-261
	c.must("GET", "/diskspace", nil, nil, 200, &disks)         // L262-266
	c.must("GET", "/tag", nil, nil, 200, &tags)                // L267-271
	var movies []any
	c.must("GET", "/movie", nil, nil, 200, &movies) // L11-17
	for _, m := range movies {
		has(t, "movie", m, `tmdbId title sortTitle year runtime alternateTitles genres status isAvailable
			minimumAvailability monitored qualityProfileId added tags images`)
	}
	id := strconv.Itoa(IDBase + 1)
	var m any
	c.must("GET", "/movie/"+id, nil, nil, 200, &m)                                   // L30-37
	c.must("GET", "/history/movie", url.Values{"movieId": {id}}, nil, 200, &[]any{}) // L38-43
	var files []any
	c.must("GET", "/moviefile", url.Values{"movieId": {id}}, nil, 200, &files) // L44-49
	for _, f := range files {
		has(t, "moviefile", f, "id size dateAdded quality")
	}
	c.must("GET", "/extrafile", url.Values{"movieId": {id}}, nil, 200, &[]any{}) // L50-55
	var rels []any
	c.must("GET", "/release", url.Values{"movieId": {id}}, nil, 200, &rels) // L24-29
	for _, r := range rels {
		has(t, "release", r, `guid title size age ageMinutes rejected downloadAllowed customFormatScore protocol
			indexerId quality languages rejections qualityWeight releaseWeight`)
	}
	// Grab (DownloadReleaseCommand, InstanceCommand.swift L74-106): guid and indexerId; a forced grab adds movieId.
	r0 := rels[0].(map[string]any)
	c.must("POST", "/release", nil, map[string]any{"guid": r0["guid"], "indexerId": r0["indexerId"]}, 200, nil)
	c.must("POST", "/release", nil, map[string]any{"guid": r0["guid"], "indexerId": r0["indexerId"],
		"movieId": IDBase + 1}, 200, nil)
	var cal []any
	c.must("GET", "/calendar", url.Values{"unmonitored": {"true"}, "start": {"2020-04-01T07:00:00Z"},
		"end": {"2020-09-01T07:00:00Z"}}, nil, 200, &cal) // L93-105
	if len(cal) != 1 {
		t.Errorf("calendar %v", cal)
	}
	var q map[string]any
	c.must("GET", "/queue", url.Values{"includeMovie": {"true"}, "includeSeries": {"true"},
		"includeEpisode": {"true"}, "pageSize": {"250"}}, nil, 200, &q) // L272-284
	has(t, "queue", q, "page pageSize totalRecords records")
	recs := q["records"].([]any)
	if len(recs) == 0 {
		t.Fatal("queue empty")
	}
	for _, r := range recs {
		has(t, "queue item", r, "id protocol size sizeleft quality movie")
	}
	qid := strconv.Itoa(int(recs[0].(map[string]any)["id"].(float64)))
	var imp []any
	c.must("GET", "/manualimport", url.Values{"downloadId": {strings.Repeat("A", 40)},
		"filterExistingFiles": {"false"}}, nil, 200, &imp) // L296-304
	c.must("DELETE", "/queue/"+qid, url.Values{"removeFromClient": {"true"}, "blocklist": {"false"},
		"skipRedownload": {"true"}}, nil, 200, nil) // L285-295
	c.must("POST", "/command", nil, map[string]any{"name": "RefreshMonitoredDownloads"}, 201, nil) // Queue.swift L171
	var hist map[string]any
	c.must("GET", "/history", url.Values{"page": {"1"}, "pageSize": {"25"}, "eventType": {"1"}}, nil, 200, &hist) // L305-319
	for _, h := range hist["records"].([]any) {
		has(t, "history", h, "id eventType date quality")
	}
	for _, name := range []string{"RefreshMovie", "MoviesSearch"} {
		c.must("POST", "/command", nil, map[string]any{"name": name, "movieIds": []int{IDBase + 1}}, 201, nil)
	}
	// Edit (Movie.swift L315-324) and delete.
	c.must("PUT", "/movie/editor", nil, map[string]any{"movieIds": []int{IDBase + 1}, "monitored": true,
		"qualityProfileId": profiles[0].(map[string]any)["id"], "minimumAvailability": "released",
		"rootFolderPath": roots[0].(map[string]any)["path"], "tags": []int{}, "applyTags": "replace"}, 202, nil)
	if code := c.do("DELETE", "/moviefile/"+strconv.Itoa(LinkFileBase+7), nil, nil, nil); code != 409 {
		t.Errorf("moviefile delete with the library gate closed: %d", code)
	}
	e.pol.Import = nil // a version an arr still owns is refused (409); after cutover none is
	c.must("DELETE", "/movie/"+strconv.Itoa(IDBase+2), url.Values{"deleteFiles": {"true"},
		"addImportExclusion": {"false"}}, nil, 200, nil) // L77-86
}

func TestRuddarrSonarrContract(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	e.seedDownloads()
	e.seedFiles()
	c := devClient(t, e, "/sonarr", true)
	sid := strconv.Itoa(IDBase + 10)
	var series []any
	c.must("GET", "/series", nil, nil, 200, &series) // L111-117
	for _, s := range series {
		has(t, "series", s, `title sortTitle tvdbId status seriesType year runtime ended seasonFolder
			useSceneNumbering added monitored seasons tags genres images`)
	}
	var eps []any
	c.must("GET", "/episode", url.Values{"seriesId": {sid}, "includeEpisodeFile": {"true"}}, nil, 200, &eps) // L118-128
	for _, ep := range eps {
		has(t, "episode", ep, `id seriesId episodeFileId tvdbId seasonNumber episodeNumber hasFile monitored
			unverifiedSceneNumbering`)
	}
	if f, _ := eps[1].(map[string]any)["episodeFile"].(map[string]any); f == nil || f["id"].(float64) != 3 {
		t.Errorf("episode file of S01E01: %v", eps[1])
	}
	var rels []any
	c.must("GET", "/release", url.Values{"episodeId": {"102"}}, nil, 200, &rels) // L135-145
	c.must("GET", "/release", url.Values{"seriesId": {sid}, "seasonNumber": {"1"}}, nil, 200, &rels)
	for _, r := range rels {
		has(t, "series release", r, `guid title size age ageMinutes rejected downloadAllowed protocol indexerId
			quality rejections qualityWeight releaseWeight fullSeason episodeRequested special
			isPossibleSpecialEpisode seasonNumber`)
	}
	// Season pack grab adds seriesId and seasonNumber (SeriesReleaseSheet.swift L228-235).
	r0 := rels[0].(map[string]any)
	c.must("POST", "/release", nil, map[string]any{"guid": r0["guid"], "indexerId": r0["indexerId"],
		"seriesId": IDBase + 10, "seasonNumber": 1}, 200, nil)
	var s map[string]any
	c.must("GET", "/series/"+sid, nil, nil, 200, &s)
	c.must("PUT", "/series/"+sid, nil, s, 202, nil) // season monitoring, L159-164
	c.must("PUT", "/series/editor", nil, map[string]any{"seriesIds": []int{IDBase + 10}, "monitored": true,
		"monitorNewItems": "all", "seriesType": "standard", "seasonFolder": true, "tags": []int{},
		"applyTags": "replace"}, 202, nil) // L165-182
	c.must("PUT", "/episode/monitor", nil, map[string]any{"episodeIds": []int{102}, "monitored": true}, 202, nil)
	var h map[string]any
	c.must("GET", "/history", url.Values{"episodeId": {"102"}}, nil, 200, &h) // L200-205
	var cal []any
	c.must("GET", "/calendar", url.Values{"unmonitored": {"true"}, "includeSeries": {"true"},
		"start": {"2019-01-01T00:00:00Z"}, "end": {"2019-02-01T00:00:00Z"}}, nil, 200, &cal) // L219-232
	if len(cal) != 2 {
		t.Errorf("calendar %v", cal)
	}
	for _, b := range []map[string]any{{"name": "RefreshSeries", "seriesId": IDBase + 10},
		{"name": "SeriesSearch", "seriesId": IDBase + 10}, {"name": "SeasonSearch", "seriesId": IDBase + 10,
			"seasonNumber": 1}, {"name": "EpisodeSearch", "episodeIds": []int{102}}} {
		c.must("POST", "/command", nil, b, 201, nil)
	}
	// File deletes go through the library gate (closed here): 409, nothing removed.
	if code := c.do("DELETE", "/episodefile/3", nil, nil, nil); code != 409 {
		t.Errorf("episodefile delete: %d", code)
	}
	if code := c.do("DELETE", "/episodefile/bulk", nil, map[string]any{"episodeFileIds": []int{3}}, nil); code != 409 {
		t.Errorf("episodefile bulk: %d", code)
	}
	e.pol.Import = nil // a version an arr still owns is refused (409); after cutover none is
	c.must("DELETE", "/series/"+sid, url.Values{"deleteFiles": {"false"}, "addImportListExclusion": {"false"}}, nil,
		200, nil) // L183-192
}

func TestLunaSeaContract(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	e.seedDownloads()
	e.seedFiles()
	r := devClient(t, e, "/radarr", false) // apikey query parameter (radarr.dart L54-73)
	id := strconv.Itoa(IDBase + 1)
	var cal []map[string]any
	r.must("GET", "/calendar", url.Values{"start": {"2020-04-01"}, "end": {"2020-09-01"}}, nil, 200, &cal) // dashboard api.dart L84-138
	if len(cal) != 1 || cal[0]["hasFile"] != true {
		t.Errorf("radarr calendar %v", cal)
	}
	var q map[string]any
	r.must("GET", "/queue", url.Values{"page": {"1"}, "pageSize": {"50"}, "sortDirection": {"descending"},
		"sortKey": {"progress"}, "includeUnknownMovieItems": {"false"}}, nil, 200, &q) // queue/get_queue.dart L11-17
	r.must("GET", "/queue/status", nil, nil, 200, nil)
	// delete_queue.dart L9-12 sends the old "blacklist" name.
	r.must("DELETE", "/queue/1", url.Values{"removeFromClient": {"false"}, "blacklist": {"true"}}, nil, 200, nil)
	if e.one(`SELECT COUNT(*) FROM router_blocklist WHERE source = 'queue'`) != 1 {
		t.Error("blacklist=true did not blocklist")
	}
	r.must("GET", "/history", url.Values{"page": {"1"}, "pageSize": {"25"}, "sortDirection": {"descending"},
		"sortKey": {"date"}}, nil, 200, nil)
	r.must("GET", "/history/movie", url.Values{"movieId": {id}}, nil, 200, nil)
	r.must("GET", "/release", url.Values{"movieId": {id}}, nil, 200, nil)
	r.must("GET", "/moviefile", url.Values{"movieId": {id}}, nil, 200, nil)
	for _, p := range []string{"/extraFile", "/credit", "/exclusions", "/qualitydefinition", "/language",
		"/filesystem", "/health", "/diskspace", "/importlist/movie"} {
		r.must("GET", p, url.Values{"movieId": {id}, "path": {"/"}}, nil, 200, nil)
	}
	// Manual import: list a folder, reprocess, import (update_manual_import.dart, command/manual_import.dart).
	r.must("GET", "/manualimport", url.Values{"folder": {t.TempDir()}, "filterExistingFiles": {"true"}}, nil, 200, nil)
	r.must("POST", "/command", nil, map[string]any{"name": "ManualImport", "importMode": "copy",
		"files": []map[string]any{{"path": "/x/a.mkv", "movieId": IDBase + 1}}}, 201, nil)
	s := devClient(t, e, "/sonarr", false)
	sid := strconv.Itoa(IDBase + 10)
	var scal []map[string]any
	s.must("GET", "/calendar", url.Values{"start": {"2019-01-01"}, "end": {"2019-02-01"}, "includeSeries": {"true"},
		"includeEpisodeFile": {"true"}}, nil, 200, &scal) // api.dart L140-186 dereferences series.title
	for _, x := range scal {
		if x["series"].(map[string]any)["title"] != "The Show" {
			t.Errorf("calendar series %v", x)
		}
	}
	s.must("GET", "/queue", url.Values{"includeEpisode": {"true"}, "includeSeries": {"true"}, "pageSize": {"50"}},
		nil, 200, nil)
	var qd []map[string]any
	s.must("GET", "/queue/details", url.Values{"seriesId": {sid}, "includeEpisode": {"true"}}, nil, 200, &qd)
	if len(qd) == 0 || qd[0]["episode"] == nil {
		t.Errorf("queue/details %v", qd)
	}
	s.must("GET", "/history/series", url.Values{"seriesId": {sid}, "includeEpisode": {"true"}}, nil, 200, nil)
	s.must("GET", "/episodefile", url.Values{"seriesId": {sid}}, nil, 200, nil)
	s.must("GET", "/wanted/missing", url.Values{"pageSize": {"50"}, "sortDirection": {"descending"},
		"sortKey": {"airDateUtc"}}, nil, 200, nil)
	s.must("GET", "/episode", url.Values{"seriesId": {sid}, "seasonNumber": {"1"}}, nil, 200, nil)
	s.must("GET", "/languageprofile", nil, nil, 200, nil)
	s.must("GET", "/importlistexclusion", nil, nil, 200, nil)
	s.must("POST", "/command", nil, map[string]any{"name": "SeasonSearch", "seriesId": IDBase + 10,
		"seasonNumber": 1}, 201, nil)
}

// autobrr: the client test (system/status, version), the push, and the arr lists (movie/series, tag).
func TestAutobrrContract(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	e.pushClient("autobrr")
	for _, fl := range []string{"radarr", "sonarr"} {
		c := devClient(t, e, "/v/movie-web/radarr", true)
		if fl == "sonarr" {
			c = devClient(t, e, "/v/tv-2160p/sonarr", true)
		}
		var st struct {
			Version string `json:"version"`
		}
		c.must("GET", "/system/status", nil, nil, 200, &st)
		if st.Version == "" {
			t.Error("version")
		}
		// ReleasePushResponse: approved/rejected/temporarilyRejected bools and rejections as strings, in an array.
		var resp []struct {
			Approved            bool     `json:"approved"`
			Rejected            bool     `json:"rejected"`
			TemporarilyRejected bool     `json:"temporarilyRejected"`
			Rejections          []string `json:"rejections"`
		}
		body := map[string]any{"title": "Alpha.2020.2160p.WEB-DL.H265-GRP", "infoUrl": "https://t/1",
			"downloadUrl": "https://t/dl/1", "magnetUrl": "magnet:?xt=urn:btih:" + strings.Repeat("3", 40),
			"size": 1 << 30, "indexer": "prowlarr-public", "downloadProtocol": "torrent", "protocol": "torrent",
			"publishDate": "2026-10-26T11:00:00Z", "indexerFlags": 1, "tmdbId": 101, "imdbId": 101}
		if fl == "sonarr" {
			body["title"], body["tvdbId"], body["imdbId"] = "The.Show.S01E02.2160p.WEB-DL-GRP", 201, "tt0000201"
			delete(body, "tmdbId")
		}
		c.must("POST", "/release/push", nil, body, 200, &resp)
		if len(resp) != 1 || !resp[0].Approved || resp[0].Rejected {
			t.Errorf("%s push %+v", fl, resp)
		}
		// Lists.
		c.must("GET", map[string]string{"radarr": "/movie", "sonarr": "/series"}[fl], nil, nil, 200, &[]struct {
			Title     string  `json:"title"`
			Monitored bool    `json:"monitored"`
			Tags      []int   `json:"tags"`
			ID        int64   `json:"id"`
			SizeOn    int64   `json:"sizeOnDisk"`
			Alt       []any   `json:"alternateTitles"`
			Ratings   any     `json:"ratings"`
			Year      int     `json:"year"`
			Runtime   int     `json:"runtime"`
			Pop       float64 `json:"popularity"`
		}{})
		c.must("GET", "/tag", nil, nil, 200, &[]struct {
			ID    int
			Label string
		}{})
	}
}

// decypharr's repair and queue flows, decoded into its own types (pkg/arr): status codes must be 2xx and the
// bodies must decode with its field types.
func TestDecypharrRepairContract(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	e.seedDownloads()
	e.seedFiles()
	c := devClient(t, e, "/radarr", true)
	var q struct {
		TotalRecords int `json:"totalRecords"`
		Records      []struct {
			ID                    int    `json:"id"`
			Title                 string `json:"title"`
			Status                string `json:"status"`
			TrackedDownloadStatus string `json:"trackedDownloadStatus"`
			TrackedDownloadState  string `json:"trackedDownloadState"`
			DownloadID            string `json:"downloadId"`
			Protocol              string `json:"protocol"`
			StatusMessages        []struct {
				Title    string   `json:"title"`
				Messages []string `json:"messages"`
			} `json:"statusMessages"`
		} `json:"records"`
	}
	c.must("GET", "/queue", url.Values{"page": {"1"}, "pageSize": {"200"}}, nil, 200, &q) // queue.go L88-111
	if q.TotalRecords == 0 {
		t.Fatal("queue empty")
	}
	// History by downloadId, then mark the grab failed (history.go L55-94, L195-216).
	var h struct {
		Records []struct {
			ID         int               `json:"id"`
			DownloadID string            `json:"downloadId"`
			EventType  string            `json:"eventType"`
			MovieID    int               `json:"movieId"`
			Data       map[string]string `json:"data"`
		} `json:"records"`
	}
	c.must("GET", "/history", url.Values{"page": {"1"}, "pageSize": {"100"}, "sortKey": {"date"},
		"sortDirection": {"descending"}, "downloadId": {strings.Repeat("A", 40)}}, nil, 200, &h)
	if len(h.Records) != 1 || h.Records[0].EventType != "grabbed" || h.Records[0].Data["indexer"] != "dmm" {
		t.Fatalf("history %+v", h)
	}
	c.must("POST", "/history/failed/"+strconv.Itoa(h.Records[0].ID), nil, nil, 200, nil)
	// queue/bulk with decypharr's flags (queue.go L197-220).
	c.must("DELETE", "/queue/bulk", url.Values{"removeFromClient": {"true"}, "blocklist": {"true"},
		"skipRedownload": {"false"}, "changeCategory": {"false"}}, map[string]any{"ids": []int{4}}, 200, nil)
	// Files (media.go L35-131): read one, then delete it (the library gate is closed: not a success, so decypharr
	// does not go on to search as if it were gone).
	var mf struct {
		ID      int    `json:"id"`
		Path    string `json:"path"`
		Size    int64  `json:"size"`
		MovieID int    `json:"movieId"`
	}
	c.must("GET", "/moviefile/1", nil, nil, 200, &mf)
	if code := c.do("DELETE", "/moviefile/1", nil, nil, nil); code < 300 {
		t.Errorf("moviefile delete %d with the gate closed", code)
	}
	// Interactive re-acquire (release.go L28-100): GET release, POST the element verbatim.
	var rels []map[string]any
	c.must("GET", "/release", url.Values{"movieId": {strconv.Itoa(IDBase + 1)}}, nil, 200, &rels)
	var typed []struct {
		GUID                string   `json:"guid"`
		Title               string   `json:"title"`
		Indexer             string   `json:"indexer"`
		IndexerID           int      `json:"indexerId"`
		Approved            bool     `json:"approved"`
		Rejected            bool     `json:"rejected"`
		TemporarilyRejected bool     `json:"temporarilyRejected"`
		DownloadAllowed     bool     `json:"downloadAllowed"`
		Rejections          []string `json:"rejections"`
	}
	c.must("GET", "/release", url.Values{"movieId": {strconv.Itoa(IDBase + 1)}}, nil, 200, &typed)
	c.must("POST", "/release", nil, rels[0], 200, nil)
	// Theatarr's cachedOn extension: decypharr's Release type (sonic, unknown keys skipped) decodes past it, and the
	// element carrying it goes back verbatim.
	withCached := 0
	for i, x := range rels {
		if _, ok := x["cachedOn"]; ok {
			withCached++
			if typed[i].GUID == "" || typed[i].Title == "" {
				t.Errorf("typed decode with cachedOn %+v", typed[i])
			}
			c.must("POST", "/release", nil, x, 200, nil)
		}
	}
	if withCached == 0 {
		t.Error("no release with cachedOn")
	}
	// manualimport by downloadId (import.go L71-89), rejections as {reason, type}.
	var imp []struct {
		Path       string           `json:"path"`
		Movie      struct{ ID int } `json:"movie"`
		Rejections []struct {
			Reason string `json:"reason"`
			Type   string `json:"type"`
		} `json:"rejections"`
	}
	c.must("GET", "/manualimport", url.Values{"downloadId": {strings.Repeat("B", 40)}}, nil, 200, &imp)
	c.must("POST", "/command", nil, map[string]any{"name": "ManualImport", "importMode": "copy",
		"files": []map[string]any{{"path": "/x", "downloadId": strings.Repeat("B", 40), "movieId": IDBase + 1,
			"quality": map[string]any{}, "languages": []any{}}}}, 201, nil)
	// Sonarr library mapping: series, episodefile?seriesId, episode?seriesId (library.go L27-168).
	s := devClient(t, e, "/sonarr", true)
	var efs []struct {
		ID           int    `json:"id"`
		SeriesID     int    `json:"seriesId"`
		SeasonNumber int    `json:"seasonNumber"`
		Path         string `json:"path"`
		Size         int64  `json:"size"`
	}
	s.must("GET", "/episodefile", url.Values{"seriesId": {strconv.Itoa(IDBase + 10)}}, nil, 200, &efs)
	if len(efs) != 1 || efs[0].Path == "" {
		t.Errorf("episode files %+v", efs)
	}
}

// An upgrade script against a per-version view (it points each "instance" at /v/<version>/radarr|sonarr).
func TestUpgradeScriptContract(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	e.pushClient("upgrader")
	e.seedFiles()
	c := devClient(t, e, "/v/movie-web/radarr", true)
	id := strconv.Itoa(IDBase + 1)
	var parse map[string]any
	c.must("GET", "/parse", url.Values{"title": {"Alpha.2020.2160p.WEB-DL.H265-GRP"}}, nil, 200, &parse) // L196-203
	if parse["customFormatScore"].(float64) != 50 || parse["movie"].(map[string]any)["id"].(float64) != IDBase+1 {
		t.Errorf("parse %v", parse)
	}
	var profiles []map[string]any
	c.must("GET", "/qualityprofile", nil, nil, 200, &profiles) // L206-213: items[].{allowed, quality.{id,name}}
	if items, _ := profiles[0]["items"].([]any); len(items) == 0 {
		t.Error("profile items")
	}
	c.must("GET", "/moviefile", url.Values{"movieFileIds": {strconv.Itoa(LinkFileBase + 7)}}, nil, 200, nil) // L224-229
	c.must("GET", "/moviefile", url.Values{"movieId": {id}}, nil, 200, nil)
	c.must("GET", "/movie", nil, nil, 200, nil)
	c.must("GET", "/movie/"+id, nil, nil, 200, nil)
	c.must("GET", "/release", url.Values{"movieId": {id}}, nil, 200, nil) // L530-543
	var tags []map[string]any
	c.must("POST", "/tag", nil, map[string]any{"label": "usenet-upgrade"}, 201, nil) // L682-686
	c.must("GET", "/tag", nil, nil, 200, &tags)
	c.must("PUT", "/movie/editor", nil, map[string]any{"movieIds": []int{IDBase + 1}, "tags": []any{tags[0]["id"]},
		"applyTags": "add"}, 202, nil) // L691-699
	var details []map[string]any
	c.must("GET", "/tag/detail", nil, nil, 200, &details)
	if len(details[0]["movieIds"].([]any)) != 1 {
		t.Errorf("tag detail %v", details)
	}
	c.must("PUT", "/movie/editor", nil, map[string]any{"movieIds": []int{IDBase + 1}, "monitored": true}, 202, nil) // L1528
	var health []map[string]any
	c.must("GET", "/health", nil, nil, 200, &health) // L764-765
	var ixs []map[string]any
	c.must("GET", "/indexer", nil, nil, 200, &ixs) // L766: name, id, protocol
	var ix map[string]any
	for _, x := range ixs {
		if x["name"] == "prowlarr-public" {
			ix = x
		}
	}
	// Push a hash through the chosen indexer. The script names it by indexerId only; a
	// push's indexer is matched by name (the positional id is not used), so that push is rejected with the reason,
	// and the same push naming the indexer is accepted: rejected false, mappedMovieId the movie.
	var push []map[string]any
	body := map[string]any{"title": "Alpha.2020.2160p.WEB-DL.H265-GRP",
		"magnetUrl": "magnet:?xt=urn:btih:" + strings.Repeat("2", 40), "infoHash": strings.Repeat("2", 40),
		"protocol": "torrent", "indexerId": ix["id"], "size": 5 << 30, "tmdbId": 101, "imdbId": 101,
		"publishDate": "2026-10-26T11:59:55Z"}
	c.must("POST", "/release/push", nil, body, 200, &push)
	if push[0]["rejected"] != true || !strings.Contains(fmt.Sprint(push[0]["rejections"]), "Indexer push is not one of") {
		t.Errorf("id-only push %v", push)
	}
	body["indexer"] = ix["name"]
	c.must("POST", "/release/push", nil, body, 200, &push)
	if push[0]["rejected"] != false || push[0]["mappedMovieId"].(float64) != IDBase+1 {
		t.Errorf("push %v", push)
	}
	e.drain()
	// Confirmation by grab history (L790-791): downloadId lower-cased equals the hash.
	var hs []map[string]any
	c.must("GET", "/history/movie", url.Values{"movieId": {id}, "eventType": {"1"}}, nil, 200, &hs)
	found := false
	for _, h := range hs {
		if strings.ToLower(h["downloadId"].(string)) == strings.Repeat("2", 40) {
			found = true
		}
	}
	if !found {
		t.Errorf("pushed grab not in history %v", hs)
	}
	c.must("GET", "/queue", url.Values{"pageSize": {"200"}}, nil, 200, nil) // L793-797
	// Grab without a hash: a fresh release search, then POST {guid, indexerId} (L804-817).
	var rels []map[string]any
	c.must("GET", "/release", url.Values{"movieId": {id}}, nil, 200, &rels)
	c.must("POST", "/release", nil, map[string]any{"guid": rels[0]["guid"], "indexerId": rels[0]["indexerId"]}, 200, nil)
	// Sonarr side: series, episodefile, episode by season, wanted/cutoff (L302-391, L464-465).
	s := devClient(t, e, "/v/tv-2160p/sonarr", true)
	sid := strconv.Itoa(IDBase + 10)
	s.must("GET", "/series", nil, nil, 200, nil)
	s.must("GET", "/series/"+sid, nil, nil, 200, nil)
	s.must("GET", "/episodefile", url.Values{"seriesId": {sid}}, nil, 200, nil)
	s.must("GET", "/episode", url.Values{"seriesId": {sid}, "seasonNumber": {"1"}}, nil, 200, nil)
	s.must("GET", "/wanted/cutoff", url.Values{"pageSize": {"10000"}, "monitored": {"true"}}, nil, 200, nil)
	// The Usenet job's import (L2376-2413): manualimport?folder, then ManualImport (move), then poll the command.
	dir := t.TempDir()
	var imp []map[string]any
	c.must("GET", "/manualimport", url.Values{"folder": {dir}, "filterExistingFiles": {"false"}}, nil, 200, &imp)
	var cmd map[string]any
	c.must("POST", "/command", nil, map[string]any{"name": "ManualImport", "importMode": "move",
		"files": []map[string]any{{"path": dir + "/a.mkv", "movieId": IDBase + 1, "quality": map[string]any{},
			"languages": []any{}, "releaseGroup": "GRP", "indexerFlags": 0}}}, 201, &cmd)
	var got map[string]any
	c.must("GET", "/command/"+strconv.Itoa(int(cmd["id"].(float64))), nil, nil, 200, &got)
	if got["status"] != "failed" || !strings.Contains(got["message"].(string), "not on the debrid mount") {
		t.Errorf("ManualImport of a file not of ours: %v %v", got["status"], got["message"])
	}
}

// What an upgrade script needs beyond the arr API: a push the router fails before any provider takes it shows in
// history as downloadFailed with the reason; router.enabled off is a health warning; GET release carries cachedOn.
func TestUpgradeScriptViewGaps(t *testing.T) {
	t.Parallel()
	e := newDevEnvWith(t, false) // no provider has its API key set
	e.pushClient("upgrader")
	c := devClient(t, e, "/v/movie-web/radarr", true)
	id := strconv.Itoa(IDBase + 1)
	hash := strings.Repeat("3", 40)
	var push []map[string]any
	c.must("POST", "/release/push", nil, map[string]any{"title": "Alpha.2020.2160p.WEB-DL.H265-GRP",
		"magnetUrl": "magnet:?xt=urn:btih:" + hash, "infoHash": hash, "protocol": "torrent", "indexer": "prowlarr-public",
		"size": 5 << 30, "tmdbId": 101, "publishDate": "2026-10-26T11:59:55Z"}, 200, &push)
	if push[0]["approved"] != true {
		t.Fatalf("push %v", push)
	}
	e.drain()
	// The grab poll (eventType 1) finds nothing: no provider took it.
	var hs []map[string]any
	c.must("GET", "/history/movie", url.Values{"movieId": {id}, "eventType": {"1"}}, nil, 200, &hs)
	if len(hs) != 0 {
		t.Errorf("grabbed records for a failed route: %v", hs)
	}
	// eventType 4: the failure, with the router's reason as the arrs' data.message.
	var failed []struct {
		ID          int64             `json:"id"`
		MovieID     int64             `json:"movieId"`
		EventType   string            `json:"eventType"`
		DownloadID  string            `json:"downloadId"`
		SourceTitle string            `json:"sourceTitle"`
		Date        string            `json:"date"`
		Data        map[string]string `json:"data"`
	}
	c.must("GET", "/history/movie", url.Values{"movieId": {id}, "eventType": {"4"}}, nil, 200, &failed)
	if len(failed) != 1 {
		t.Fatalf("failed records %+v", failed)
	}
	f := failed[0]
	if f.EventType != "downloadFailed" || strings.ToLower(f.DownloadID) != hash || f.MovieID != IDBase+1 ||
		f.Data["message"] != "no provider of the version has an API key set" || f.SourceTitle == "" ||
		f.Data["indexer"] != "prowlarr-public" || f.Data["releaseGroup"] != "GRP" || f.Data["size"] != "5368709120" ||
		f.Data["downloadClient"] != "theatarr router" || f.ID%10 != 4 {
		t.Errorf("failed record %+v", f)
	}
	// The same by name and through GET history?downloadId (paged), as decypharr asks.
	c.must("GET", "/history/movie", url.Values{"movieId": {id}, "eventType": {"downloadFailed"}}, nil, 200, &failed)
	var page struct {
		Records []map[string]any `json:"records"`
	}
	c.must("GET", "/history", url.Values{"downloadId": {strings.ToUpper(hash)}, "eventType": {"4"}}, nil, 200, &page)
	if len(failed) != 1 || len(page.Records) != 1 {
		t.Errorf("by name %d, paged %d", len(failed), len(page.Records))
	}
	// Sonarr: history/series?seriesId=&eventType=4 and history?episodeId= (Sonarr's DownloadFailed has no indexer).
	s := devClient(t, e, "/v/tv-2160p/sonarr", true)
	shash := strings.Repeat("1", 40)
	s.must("POST", "/release/push", nil, map[string]any{"title": "The.Show.S01E02.2160p.WEB-DL-GRP",
		"magnetUrl": "magnet:?xt=urn:btih:" + shash, "protocol": "torrent", "indexer": "prowlarr-public",
		"publishDate": "2026-10-26T11:00:00Z", "tvdbId": 201}, 200, nil)
	e.drain()
	var sh []map[string]any
	s.must("GET", "/history/series", url.Values{"seriesId": {strconv.Itoa(IDBase + 10)}, "eventType": {"4"}}, nil, 200, &sh)
	if len(sh) != 1 || sh[0]["episodeId"].(float64) != 102 || strings.ToLower(sh[0]["downloadId"].(string)) != shash {
		t.Fatalf("sonarr failed %v", sh)
	}
	if d := sh[0]["data"].(map[string]any); d["message"] != "no provider of the version has an API key set" ||
		d["indexer"] != nil {
		t.Errorf("sonarr data %v", d)
	}
	s.must("GET", "/history", url.Values{"episodeId": {"102"}, "eventType": {"4"}}, nil, 200, &page)
	if len(page.Records) != 1 {
		t.Errorf("history?episodeId %v", page.Records)
	}

	// Health: router.enabled off while the view's version is active is a warning; shadow versions stay a notice.
	var health []map[string]any
	c.must("GET", "/health", nil, nil, 200, &health)
	if len(health) != 0 {
		t.Errorf("health with the router on %v", health)
	}
	e.pol.Router.Enabled = false
	c.must("GET", "/health", nil, nil, 200, &health)
	if len(health) != 1 || health[0]["source"] != "RouterDisabledCheck" || health[0]["type"] != "warning" ||
		health[0]["message"] != "Router disabled: grabs are dry runs: movie-web" || health[0]["wikiUrl"] != "" {
		t.Errorf("router disabled %v", health)
	}
	r := devClient(t, e, "/radarr", true)
	r.must("GET", "/health", nil, nil, 200, &health)
	if len(health) != 2 || health[0]["source"] != "ShadowModeCheck" ||
		health[0]["message"] != "Watch only libraries download nothing: movie-remux, anime-movie" ||
		health[1]["message"] != "Router disabled: grabs are dry runs: movie-web" {
		t.Errorf("radarr view health %v", health)
	}
	if items := e.svc.HealthItems(t.Context()); len(items) != 2 || items[1].Source != "RouterDisabledCheck" ||
		items[1].Message != "Router disabled: grabs are dry runs: movie-web, tv-2160p" {
		t.Errorf("webhook health %v", items)
	}
	e.pol.Router = nil
	c.must("GET", "/health", nil, nil, 200, &health)
	if len(health) != 1 || health[0]["source"] != "RouterDisabledCheck" {
		t.Errorf("no router section %v", health)
	}
	// The service's own failing checks follow the view's; each changed check gets its own Health webhook.
	e.svc.SetHealth(fakeHealth{{Source: "BackupCheck", Type: "warning", Message: "The newest backup is 2d old"},
		{Source: "MountCheck", Type: "error", Message: "The mount at /m is empty: is it mounted?"}})
	c.must("GET", "/health", nil, nil, 200, &health)
	if len(health) != 3 || health[1]["source"] != "BackupCheck" || health[2]["type"] != "error" {
		t.Errorf("service health %v", health)
	}
	items := e.svc.HealthItems(t.Context())
	for _, item := range items {
		ps, err := e.svc.Payloads(t.Context(), notify.Event{Kind: notify.Health, Health: []notify.HealthItem{item}})
		if err != nil || len(ps) == 0 {
			t.Fatalf("health payloads %v %v", ps, err)
		}
		b, _ := json.Marshal(ps[0].Body)
		if !strings.Contains(string(b), `"type":"`+item.Source+`"`) ||
			!strings.Contains(string(b), `"level":"`+item.Type+`"`) {
			t.Errorf("health webhook %s", b)
		}
	}
	e.svc.SetHealth(nil)

	// cachedOn (claims_of): DMM's cached_on on the release it lists; left out where the source says nothing.
	var rels []map[string]any
	c.must("GET", "/release", url.Values{"movieId": {id}}, nil, 200, &rels)
	seen := 0
	for _, x := range rels {
		switch x["indexer"] {
		case "dmm":
			if on, _ := x["cachedOn"].([]any); len(on) != 2 || on[0] != "rd" || on[1] != "ad" {
				t.Errorf("dmm cachedOn %v", x["cachedOn"])
			}
			seen++
		default:
			if _, ok := x["cachedOn"]; ok {
				t.Errorf("cachedOn without a source: %v", x["cachedOn"])
			}
		}
	}
	if seen == 0 {
		t.Errorf("no dmm release in %v", rels)
	}
}

type fakeHealth []health.Result

func (f fakeHealth) Issues(context.Context) []health.Result { return f }
