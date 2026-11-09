package arrview

import (
	"context"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

func TestCalendarCutoffTagsIndexers(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	// Radarr calendar (Ruddarr: ISO 8601 bounds, unmonitored=true; LunaSea: plain dates, no unmonitored).
	var ms []map[string]any
	e.call("GET", "/radarr/api/v3/calendar?unmonitored=true&start=2020-04-30T00:00:00Z&end=2020-05-02T00:00:00Z", nil,
		200, &ms)
	if len(ms) != 1 || ms[0]["title"] != "Alpha" {
		t.Errorf("radarr calendar %v", ms)
	}
	ms = nil
	e.call("GET", "/radarr/api/v3/calendar?start=2020-07-31&end=2020-08-02", nil, 200, &ms)
	if len(ms) != 1 {
		t.Errorf("digital release %v", ms)
	}
	// Sonarr calendar with the series (LunaSea's dashboard dereferences series.title) and the episode file.
	e.seedFiles()
	var eps []map[string]any
	e.call("GET", "/sonarr/api/v3/calendar?start=2019-01-01&end=2019-01-31&includeSeries=true&includeEpisodeFile=true",
		nil, 200, &eps)
	if len(eps) != 2 || eps[0]["series"].(map[string]any)["title"] != "The Show" ||
		eps[0]["episodeFile"].(map[string]any)["id"].(float64) != 3 || eps[1]["episodeFile"] != nil {
		t.Fatalf("sonarr calendar %v", eps)
	}
	wantKeys(t, "calendar episode", eps[1], fields(`id seriesId tvdbId episodeFileId seasonNumber episodeNumber title
		airDate airDateUtc runtime hasFile monitored unverifiedSceneNumbering series`), "absoluteEpisodeNumber")

	// wanted/cutoff: Beta (movie-web, filled, unmonitored) is below cutoff; monitored=true hides it.
	var p struct {
		TotalRecords int
		Records      []map[string]any
	}
	e.call("GET", "/radarr/api/v3/wanted/cutoff?monitored=false", nil, 200, &p)
	if p.TotalRecords != 1 || p.Records[0]["title"] != "Beta" {
		t.Errorf("cutoff %v", p)
	}
	p.Records = nil
	e.call("GET", "/radarr/api/v3/wanted/cutoff", nil, 200, &p)
	if p.TotalRecords != 1 || p.Records[0]["title"] != "Alpha" { // movie-web filled by seedFiles
		t.Errorf("monitored cutoff %v", p)
	}
	// Sonarr (upgrade scripts read records[].{seriesId, seasonNumber}).
	e.exec(`UPDATE episode_version_state SET state = 'filled' WHERE episode_id = 101`)
	p.Records = nil
	e.call("GET", "/sonarr/api/v3/wanted/cutoff?pageSize=10000&monitored=true", nil, 200, &p)
	if p.TotalRecords != 1 || p.Records[0]["seriesId"].(float64) != IDBase+10 || p.Records[0]["seasonNumber"].(float64) != 1 {
		t.Errorf("sonarr cutoff %v", p)
	}

	// Tags and the editor (an upgrade script: POST tag, PUT movie/editor {movieIds, tags, applyTags},
	// GET tag/detail movieIds).
	var tag map[string]any
	e.call("POST", "/radarr/api/v3/tag", map[string]any{"label": "debrid-upgrade"}, 201, &tag)
	tid := tag["id"].(float64)
	var edited []map[string]any
	e.call("PUT", "/radarr/api/v3/movie/editor", map[string]any{"movieIds": []int{IDBase + 1, 9}, "tags": []float64{tid},
		"applyTags": "add"}, 202, &edited)
	if len(edited) != 1 || len(edited[0]["tags"].([]any)) != 1 {
		t.Errorf("editor add %v", edited)
	}
	var details []map[string]any
	e.call("GET", "/radarr/api/v3/tag/detail", nil, 200, &details)
	if len(details) != 1 || len(details[0]["movieIds"].([]any)) != 1 || details[0]["movieIds"].([]any)[0].(float64) != IDBase+1 {
		t.Errorf("tag detail %v", details)
	}
	wantKeys(t, "tag detail", details[0], fields(`id label delayProfileIds importListIds notificationIds
		releaseProfileIds indexerIds downloadClientIds autoTagIds movieIds`))
	var d1 map[string]any
	e.call("GET", "/radarr/api/v3/tag/detail/"+strconv.Itoa(int(tid)), nil, 200, &d1)
	e.call("PUT", "/radarr/api/v3/movie/editor", map[string]any{"movieIds": []int{IDBase + 1}, "tags": []float64{tid},
		"applyTags": "remove"}, 202, nil)
	// Monitoring through the editor ({movieIds, monitored}): only the view's versions.
	e.call("PUT", "/v/movie-web/radarr/api/v3/movie/editor", map[string]any{"movieIds": []int{IDBase + 1},
		"monitored": false}, 202, nil)
	if e.one(`SELECT monitored FROM version WHERE id = 2`) != 0 || e.one(`SELECT monitored FROM version WHERE id = 1`) != 1 {
		t.Error("editor monitored")
	}
	// Ruddarr's series editor (Series.swift L364-375) with applyTags replace.
	e.call("PUT", "/sonarr/api/v3/series/editor", map[string]any{"seriesIds": []int{IDBase + 10}, "monitored": true,
		"monitorNewItems": "all", "seriesType": "standard", "seasonFolder": true, "qualityProfileId": 999,
		"tags": []int{}, "applyTags": "replace"}, 400, nil)
	var series []map[string]any
	e.call("PUT", "/sonarr/api/v3/series/editor", map[string]any{"seriesIds": []int{IDBase + 10}, "monitored": true,
		"monitorNewItems": "none", "seriesType": "standard", "seasonFolder": true, "tags": []int{}, "applyTags": "replace"},
		202, &series)
	if len(series) != 1 || series[0]["monitorNewItems"] != "none" {
		t.Errorf("series editor %v", series)
	}

	// indexer (upgrade scripts read name, id, protocol): the view's versions' policy indexers, no URLs or keys.
	var ixs []map[string]any
	e.call("GET", "/v/movie-web/radarr/api/v3/indexer", nil, 200, &ixs)
	if len(ixs) == 0 || ixs[0]["protocol"] != "torrent" {
		t.Fatalf("indexers %v", ixs)
	}
	for _, ix := range ixs {
		b := string(mustJSON(t, ix))
		if strings.Contains(b, "http") || strings.Contains(b, "THEATARR_") {
			t.Errorf("indexer leaks config: %s", b)
		}
	}
}

// Ruddarr's webhook install (Models/Instances/InstanceWebhook.swift L25-117, InstanceNotification.swift): GET the
// list, POST its Webhook, PUT it enabled, DELETE it. Every entry of the list must decode as its model.
func TestNotificationsRuddarr(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	var list []map[string]any
	e.call("GET", "/radarr/api/v3/notification", nil, 200, &list)
	if len(list) != 0 {
		t.Fatalf("list %v", list)
	}
	body := map[string]any{"name": "Ruddarr", "implementation": "Webhook", "configContract": "WebhookSettings",
		"fields": []map[string]any{{"name": "url", "value": "https://notify.ruddarr.com/push/abc"},
			{"name": "method", "value": "1"}, {"name": "username", "value": ""}, {"name": "password", "value": "sig"}},
		"onGrab": false, "onDownload": false, "onUpgrade": false, "onMovieAdded": false, "onHealthIssue": false,
		"includeHealthWarnings": false, "onApplicationUpdate": false, "supportsOnGrab": false}
	var n map[string]any
	e.call("POST", "/radarr/api/v3/notification", body, 201, &n)
	id := n["id"].(float64)
	if n["supportsOnGrab"] != true || n["supportsOnMovieAdded"] != true || n["onGrab"] != false ||
		n["implementationName"] != "Webhook" {
		t.Errorf("created %v", n)
	}
	for _, k := range fields(`implementation configContract fields onGrab onDownload onUpgrade onHealthIssue
		includeHealthWarnings onApplicationUpdate`) {
		if _, ok := n[k]; !ok {
			t.Errorf("missing %s (Ruddarr requires it)", k)
		}
	}
	// The password is written masked; webhooks are off in the example policy, so a warning says it is not sent.
	if strings.Contains(string(mustJSON(t, n)), `"sig"`) {
		t.Error("password written back")
	}
	if msg, _ := n["message"].(map[string]any); msg == nil || !strings.Contains(msg["message"].(string), "notify.enabled") {
		t.Errorf("message %v", n["message"])
	}
	// enable(): the on* flags set to the supports* ones, sent back with the mask as the password.
	n["onGrab"], n["onDownload"], n["onUpgrade"] = true, true, true
	e.call("PUT", "/radarr/api/v3/notification/"+strconv.Itoa(int(id)), n, 202, &n)
	if n["onGrab"] != true {
		t.Errorf("updated %v", n)
	}
	var raw string
	if err := e.st.Reader().QueryRow(`SELECT resource FROM arrview_notification WHERE id = ?`, int64(id)).Scan(&raw); err != nil ||
		!strings.Contains(raw, `"sig"`) {
		t.Errorf("stored password lost: %s", raw)
	}
	// Enabled webhooks and an allowed host: PUT tests first (the arrs' Test), through the notifier.
	e.pol.Notify = &policy.Notify{Enabled: true, AllowHosts: []string{"notify.ruddarr.com"}}
	sent := n
	n = nil
	e.call("PUT", "/radarr/api/v3/notification/"+strconv.Itoa(int(id)), sent, 202, &n)
	if len(e.events.tests) != 1 || !strings.Contains(e.events.tests[0], `"sig"`) {
		t.Errorf("tests %v", e.events.tests)
	}
	if _, has := n["message"]; has {
		t.Errorf("message while sendable: %v", n["message"])
	}
	e.call("POST", "/radarr/api/v3/notification/test", n, 200, nil)
	// Another view does not see it; only Webhook is served.
	e.call("GET", "/sonarr/api/v3/notification", nil, 200, &list)
	if len(list) != 0 {
		t.Errorf("sonarr sees %v", list)
	}
	body["implementation"] = "Discord"
	e.call("POST", "/radarr/api/v3/notification", body, 400, nil)
	var schema []map[string]any
	e.call("GET", "/radarr/api/v3/notification/schema", nil, 200, &schema)
	if len(schema) != 1 || schema[0]["implementation"] != "Webhook" {
		t.Errorf("schema %v", schema)
	}
	e.call("DELETE", "/radarr/api/v3/notification/"+strconv.Itoa(int(id)), nil, 200, nil)
	e.call("DELETE", "/radarr/api/v3/notification/"+strconv.Itoa(int(id)), nil, 404, nil)
}

// A page or pageSize big enough to overflow (page-1)*pageSize must answer an empty page, not panic.
func TestPagingWindowOverflow(t *testing.T) {
	t.Parallel()
	for _, p := range []pagingResource{
		{Page: math.MaxInt/2 + 2, PageSize: 10},
		{Page: 3, PageSize: math.MaxInt},
		{Page: math.MaxInt, PageSize: math.MaxInt},
	} {
		lo, hi := p.window(5)
		if lo < 0 || hi < lo || hi > 5 {
			t.Errorf("page %d size %d: window %d..%d", p.Page, p.PageSize, lo, hi)
		}
	}
	if lo, hi := (pagingResource{Page: 2, PageSize: 2}).window(5); lo != 2 || hi != 4 {
		t.Errorf("page 2 of 2: %d..%d", lo, hi)
	}
}

func TestCalendarWindowBeforeResourceLoading(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// A second date does not move the canonical first release into this window.
	e.exec(`INSERT INTO title_release_date(title_id,source,country,type,date) VALUES
 (1,'tmdb-art','','digital','2026-10-30'),(1,'tmdb-art','','digital','2026-11-01'),
 (3,'tmdb-art','','digital','2026-11-01')`)
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	var view *View
	for _, v := range e.svc.views {
		if v.Flavour == Radarr && v.Key == "radarr" {
			view = v
		}
	}
	if view == nil {
		t.Fatal("radarr view missing")
	}
	titles, episodes, err := e.svc.calendarIDs(context.Background(), view, start, start.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(episodes) != 0 || slices.Contains(titles, int64(1)) || !slices.Contains(titles, int64(3)) {
		t.Fatalf("movie candidates: %v %v", titles, episodes)
	}
}
