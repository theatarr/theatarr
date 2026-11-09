package shadow

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSonarr is an importarr.Getter holding one Sonarr instance in memory.
type fakeSonarr struct {
	mu       sync.Mutex
	series   []map[string]any
	episodes []map[string]any
	files    []map[string]any
	history  []map[string]any
}

func (f *fakeSonarr) GetJSON(ctx context.Context, ep string, q url.Values, v any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out any
	switch ep {
	case "blocklist":
		out = map[string]any{"totalRecords": 0, "records": []any{}}
	case "system/status":
		out = map[string]any{"appName": "Sonarr", "version": "4.0.20.3014"}
	case "rootfolder":
		out = []map[string]any{{"id": 1, "path": "/media/shows"}}
	case "config/mediamanagement", "config/indexer", "config/naming":
		out = map[string]any{}
	case "tag", "delayprofile", "releaseprofile", "customformat", "qualitydefinition":
		out = []map[string]any{}
	case "qualityprofile":
		out = []map[string]any{{"id": 7, "name": "4k Preferred", "upgradeAllowed": true, "cutoff": 18,
			"items": []map[string]any{{"quality": map[string]any{"id": 18, "name": "WEBDL-2160p"}}}}}
	case "series":
		out = f.series
	case "episode":
		out = append([]map[string]any{}, f.episodes...)
	case "episodefile":
		out = append([]map[string]any{}, f.files...)
	case "history":
		recs := append([]map[string]any(nil), f.history...)
		for i, j := 0, len(recs)-1; i < j; i, j = i+1, j-1 {
			recs[i], recs[j] = recs[j], recs[i]
		}
		out = map[string]any{"page": 1, "pageSize": 250, "totalRecords": len(recs), "records": recs}
	default:
		return fmt.Errorf("fake sonarr: no %s", ep)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func (f *fakeSonarr) add(h ...map[string]any) {
	f.mu.Lock()
	f.history = append(f.history, h...)
	f.mu.Unlock()
}

const (
	hShowPack = "7777777777777777777777777777777777777777"
	hShowE03  = "8888888888888888888888888888888888888888"
	showPack  = "The.Show.S01.2160p.WEB-DL.DDP5.1.H.265-GRP"
)

// newShow: "The Show" (tvdb 1001), S01E01 with an HDTV file imported before, S01E02 and S01E03 aired, no file.
func newShow(f *fakeSonarr) {
	f.series = []map[string]any{{"id": 5, "title": "The Show", "sortTitle": "show", "year": 2020, "tvdbId": 1001,
		"imdbId": "tt1001001", "monitored": true, "qualityProfileId": 7, "status": "continuing",
		"path": "/media/shows/The Show", "rootFolderPath": "/media/shows", "tags": []int{},
		"added": "2026-01-01T00:00:00Z", "seriesType": "standard", "runtime": 45,
		"alternateTitles": []map[string]any{{"title": "Show Alt", "sceneSeasonNumber": -1}}}}
	ep := func(id, n, file int) map[string]any {
		return map[string]any{"id": id, "seriesId": 5, "seasonNumber": 1, "episodeNumber": n, "title": fmt.Sprint("Ep ", n),
			"airDate": fmt.Sprintf("2026-09-%02d", n), "airDateUtc": fmt.Sprintf("2026-09-%02dT02:00:00Z", n),
			"runtime": 45, "monitored": true, "episodeFileId": file, "hasFile": file != 0}
	}
	f.episodes = []map[string]any{ep(51, 1, 90), ep(52, 2, 0), ep(53, 3, 0)}
	f.files = []map[string]any{{"id": 90, "seriesId": 5, "seasonNumber": 1, "relativePath": "Season 01/The Show S01E01.mkv",
		"size": 8000000000, "dateAdded": "2026-10-01T00:00:00Z", "sceneName": "The.Show.S01E01.2160p.HDTV-GRP",
		"releaseGroup": "GRP", "quality": quality("HDTV-2160p", 16), "releaseType": "singleEpisode",
		"qualityCutoffNotMet": true}}
	f.history = []map[string]any{{"id": 1, "seriesId": 5, "episodeId": 51, "eventType": "downloadFolderImported",
		"date": "2026-10-01T00:00:00Z", "sourceTitle": "The.Show.S01E01.2160p.HDTV-GRP",
		"quality": quality("HDTV-2160p", 16), "data": map[string]any{"fileId": "90"}}}
}

func showGrab(id, ep int, at time.Time, title, hash, source string) map[string]any {
	return map[string]any{"id": id, "seriesId": 5, "episodeId": ep, "eventType": "grabbed", "date": at.Format(time.RFC3339),
		"downloadId": strings.ToUpper(hash), "sourceTitle": title, "quality": quality("WEBDL-2160p", 18),
		"languages": []map[string]any{{"id": 1, "name": "English"}},
		"data": map[string]any{"indexer": "Example (Prowlarr)", "protocol": "2", "size": "16000000000",
			"torrentInfoHash": hash, "customFormatScore": "0", "releaseSource": source, "releaseType": "SeasonPack",
			"tvdbId": "1001", "seriesMatchType": "Title"}}
}

// A Sonarr pack grab (one history row per episode, a second apart) becomes one shadow grab with its episodes; our
// season search finds the same pack and a single episode the existing file makes no upgrade; the diff row is of kind
// series and counts in the series agreement. An RSS item in a TV category becomes a series pick with its episodes.
func TestRunnerSeries(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	newShow(e.tv)
	if _, err := e.r.Import(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.prow.shows["1001:1"] = []item{
		{title: showPack, hash: hShowPack, size: 16e9, pub: t0.Add(-48 * time.Hour), tv: true},
		{title: "The.Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-OTHER", hash: "9999999999999999999999999999999999999999",
			size: 8e9, pub: t0.Add(-72 * time.Hour), tv: true},
	}
	at := t0.Add(-30 * time.Minute)
	e.tv.add(showGrab(2, 52, at, showPack, hShowPack, "Search"), showGrab(3, 53, at.Add(time.Second), showPack, hShowPack, "Search"))
	sum, err := e.r.Import(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var tvi *ImportInstance
	for i := range sum.Instances {
		if sum.Instances[i].Instance == "sonarr_tv" {
			tvi = &sum.Instances[i]
		}
	}
	if tvi == nil || tvi.Grabs != 1 || tvi.Cursor != 3 {
		t.Fatalf("import %+v", sum.Instances)
	}
	if s := one[string](t, e.st, `SELECT history_ids || ' ' || episodes || ' ' || existing_source FROM shadow_grab
		WHERE instance = 'sonarr_tv'`); !strings.HasPrefix(s, "[2,3] [") || !strings.HasSuffix(s, " history") {
		t.Errorf("grab %s", s)
	}
	rows, _, err := ListDiffs(e.ctx, e.st.Reader(), DiffFilter{Version: "tv"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("diffs %v %v", rows, err)
	}
	d := rows[0]
	if d.Bucket != BucketAgree || d.Reason != ReasonSameRelease || !d.Searched {
		t.Errorf("diff %s/%s searched %v: %s", d.Bucket, d.Reason, d.Searched, d.Detail)
	}
	if k := one[string](t, e.st, `SELECT kind FROM shadow_diff WHERE id = ?`, d.ID); k != "series" {
		t.Errorf("kind %s", k)
	}
	full, err := LoadDiff(e.ctx, e.st.Reader(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if full.Grab.ResultCount != 2 || full.Grab.AcceptedCount != 1 || !full.Grab.FoundArr {
		t.Errorf("grab detail %+v", full.Grab)
	}
	if reqs := e.net.requests("prowlarr.test/1/api"); !containsReq(reqs, "t=tvsearch") || !containsReq(reqs, "tvdbid=1001") {
		t.Errorf("searches %v", reqs)
	}
	s, err := LoadSummary(e.ctx, e.st.Reader(), t0)
	if err != nil {
		t.Fatal(err)
	}
	var series *KindSummary
	for i := range s.Kinds {
		if s.Kinds[i].Kind == "series" {
			series = &s.Kinds[i]
		}
	}
	if series == nil || series.Grabs != 1 || series.Agree != 1 || !series.MeetsBar {
		t.Errorf("kinds %+v", s.Kinds)
	}

	// The same grab run again by its second history row is the same shadow grab.
	res, err := e.r.GrabByID(e.ctx, "sonarr_tv", 3, true)
	if err != nil || res.History != 2 {
		t.Errorf("grab by second row: %+v %v", res, err)
	}

	// RSS: a TV item for the wanted E03 by a scene mapping name.
	e.prow.setRSS(item{title: "Show.Alt.S01E03.2160p.WEB-DL-GRP", hash: hShowE03, size: 8e9, pub: t0, tv: true})
	rs, err := e.r.RSS(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Picks != 1 {
		t.Fatalf("rss %+v", rs)
	}
	if s := one[string](t, e.st, `SELECT version_key || ' ' || json_array_length(episodes) FROM shadow_rss_pick
		WHERE info_hash = ?`, hShowE03); s != "tv 1" {
		t.Errorf("pick %s", s)
	}
	if reqs := e.net.requests("prowlarr.test/1/api"); !containsReq(reqs, "cat=2000%2C5000") {
		t.Errorf("rss categories %v", reqs)
	}
}

func containsReq(reqs []string, part string) bool {
	for _, r := range reqs {
		if strings.Contains(r, part) {
			return true
		}
	}
	return false
}

func TestRSSCategoriesIncludeAnimeForConfiguredAnimeVersion(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	anime := true
	e.r.pol.Versions["tv"].Anime = &anime
	if got := e.r.rssCategories("prowlarr"); fmt.Sprint(got) != "[2000 5000 5070]" {
		t.Fatalf("RSS categories %v", got)
	}
}
