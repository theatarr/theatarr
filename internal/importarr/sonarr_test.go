package importarr

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// fakeSonarr serves the endpoints the importer reads for a Sonarr instance.
type fakeSonarr struct {
	mu        sync.Mutex
	series    []map[string]any
	episodes  map[int][]map[string]any // by series id
	files     map[int][]map[string]any // by series id
	history   []map[string]any
	blocklist []map[string]any
	requests  []string
	root      string
	// delays and releaseProfiles are the selection profiles; nil delays serve the arr's default delay profile.
	delays          []map[string]any
	releaseProfiles []map[string]any
}

func (f *fakeSonarr) delayProfiles() []map[string]any { return orDefaultDelay(f.delays) }

func (f *fakeSonarr) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("X-Api-Key") != testKey || r.Method != http.MethodGet {
		http.Error(w, "no", http.StatusUnauthorized)
		return
	}
	ep := strings.TrimPrefix(r.URL.Path, "/api/v3/")
	f.requests = append(f.requests, ep+"?"+r.URL.RawQuery)
	sid, _ := strconv.Atoi(r.URL.Query().Get("seriesId"))
	var out any
	switch ep {
	case "blocklist":
		q := r.URL.Query()
		page, _ := strconv.Atoi(q.Get("page"))
		size, _ := strconv.Atoi(q.Get("pageSize"))
		lo, hi := min((page-1)*size, len(f.blocklist)), min(page*size, len(f.blocklist))
		out = map[string]any{"totalRecords": len(f.blocklist), "records": f.blocklist[lo:hi]}
	case "system/status":
		out = map[string]any{"appName": "Sonarr", "version": "4.0.20.3014"}
	case "customformat", "qualitydefinition":
		out = []any{}
	case "config/mediamanagement", "config/indexer", "config/naming":
		out = map[string]any{}
	case "rootfolder":
		out = []map[string]any{{"id": 1, "path": f.root}}
	case "tag":
		out = []map[string]any{{"id": 3, "label": "anime"}}
	case "delayprofile":
		out = f.delayProfiles()
	case "releaseprofile":
		out = f.releaseProfiles
	case "qualityprofile":
		out = []map[string]any{{"id": 7, "name": "4k Preferred", "upgradeAllowed": true, "cutoff": 19,
			"minFormatScore": 0, "cutoffFormatScore": 10000,
			"items": []map[string]any{{"quality": map[string]any{"id": 19, "name": "Bluray-2160p"}}}}}
	case "series":
		out = f.series
	case "episode":
		if sid == 0 {
			http.Error(w, "seriesId required", 400)
			return
		}
		out = nonNil(f.episodes[sid])
	case "episodefile":
		if sid == 0 {
			http.Error(w, "seriesId required", 400)
			return
		}
		out = nonNil(f.files[sid])
	case "history":
		q := r.URL.Query()
		page, _ := strconv.Atoi(q.Get("page"))
		size, _ := strconv.Atoi(q.Get("pageSize"))
		recs := append([]map[string]any(nil), f.history...)
		sort.Slice(recs, func(i, j int) bool { return recs[i]["id"].(int) > recs[j]["id"].(int) })
		lo, hi := min((page-1)*size, len(recs)), min(page*size, len(recs))
		out = map[string]any{"page": page, "pageSize": size, "totalRecords": len(recs), "records": recs[lo:hi]}
	default:
		http.Error(w, "not found", 404)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

const (
	hashS1 = "1111111111111111111111111111111111111111"
	hashS2 = "2222222222222222222222222222222222222222"
)

func sonarrGrab(id, sid, eid int, date, dl, title string) map[string]any {
	return map[string]any{"id": id, "seriesId": sid, "episodeId": eid, "eventType": "grabbed", "date": date,
		"downloadId": dl, "sourceTitle": title, "quality": quality("Bluray-2160p", 1), "customFormatScore": 600,
		"data": map[string]any{"indexer": "Torrentio (Prowlarr)", "protocol": "2", "size": "20000000000",
			"torrentInfoHash": strings.ToLower(dl), "releaseType": "SeasonPack", "seriesMatchType": "Title",
			"downloadUrl": "https://tracker.example/dl?passkey=x", "guid": "https://tracker.example/1"}}
}
func sonarrImport(root string, id, sid, eid int, date, dl, title string, file int) map[string]any {
	return map[string]any{"id": id, "seriesId": sid, "episodeId": eid, "eventType": "downloadFolderImported",
		"date": date, "downloadId": dl, "sourceTitle": title, "quality": quality("Bluray-2160p", 1),
		"data": map[string]any{"fileId": strconv.Itoa(file), "importedPath": root + "/Show/Season 01/x.mkv"}}
}

// newFakeSonarr has two series:
//   - 1 "Show" (tvdb 1001, standard): S01E01-E02 in one season-pack file (hashS1, grabbed and imported), S01E03 aired
//     with no file, S00E01 unmonitored with no file;
//   - 2 "Anime" (tvdb 2002, anime, scene numbering): three scene mappings; E01 with its own file (manual import),
//     E02 not aired yet.
func newFakeSonarr(root string) *fakeSonarr {
	f := &fakeSonarr{root: root, episodes: map[int][]map[string]any{}, files: map[int][]map[string]any{}}
	series := func(id, tvdb int, title, typ string, scene bool) map[string]any {
		return map[string]any{"id": id, "title": title, "sortTitle": strings.ToLower(title), "year": 2020, "tvdbId": tvdb,
			"tmdbId": tvdb + 50000, "imdbId": "tt" + strconv.Itoa(9000000+id), "monitored": true, "qualityProfileId": 7,
			"status": "continuing", "path": root + "/" + title, "rootFolderPath": root, "tags": []int{3},
			"added": "2024-01-02T03:04:05Z", "firstAired": "2020-04-01T00:00:00Z", "seriesType": typ,
			"useSceneNumbering": scene, "runtime": 45, "originalLanguage": map[string]any{"id": 1, "name": "English"}}
	}
	f.series = []map[string]any{series(1, 1001, "Show", "standard", false), series(2, 2002, "Anime", "anime", true)}
	f.series[1]["originalLanguage"] = map[string]any{"id": 8, "name": "Japanese"}
	f.series[1]["alternateTitles"] = []map[string]any{
		{"title": "Anime Japanese Name", "sceneSeasonNumber": -1},
		{"title": "Anime S2", "seasonNumber": 2, "sceneSeasonNumber": 2},
		{"title": "Anime Season Two", "sceneSeasonNumber": 2, "sceneOrigin": "unknown:tvdb", "comment": "Limited matching"},
	}
	ep := func(id, sid, season, num int, aired string, fileID int, monitored bool) map[string]any {
		m := map[string]any{"id": id, "seriesId": sid, "seasonNumber": season, "episodeNumber": num,
			"title": "Episode " + strconv.Itoa(num), "runtime": 45, "monitored": monitored, "episodeFileId": fileID,
			"hasFile": fileID != 0}
		if aired != "" {
			m["airDate"] = aired[:10]
			m["airDateUtc"] = aired
		}
		return m
	}
	f.episodes[1] = []map[string]any{
		ep(101, 1, 1, 1, "2020-04-01T01:00:00Z", 501, true),
		ep(102, 1, 1, 2, "2020-04-08T01:00:00Z", 501, true),
		ep(103, 1, 1, 3, "2020-04-15T01:00:00Z", 0, true),
		ep(100, 1, 0, 1, "2020-03-01T01:00:00Z", 0, false),
	}
	a1 := ep(201, 2, 1, 1, "2021-01-10T15:00:00Z", 601, true)
	a1["absoluteEpisodeNumber"], a1["sceneSeasonNumber"], a1["sceneEpisodeNumber"], a1["sceneAbsoluteEpisodeNumber"] = 1, 1, 1, 1
	a2 := ep(202, 2, 1, 2, "2027-01-17T15:00:00Z", 0, true)
	a2["absoluteEpisodeNumber"] = 2
	f.episodes[2] = []map[string]any{a1, a2}
	f.files[1] = []map[string]any{{"id": 501, "seriesId": 1, "seasonNumber": 1,
		"relativePath": "Season 01/Show - S01E01-E02 [Bluray-2160p]-GRP.mkv", "path": root + "/Show/Season 01/x.mkv",
		"size": 20000000000, "dateAdded": "2024-02-01T00:00:00Z", "releaseGroup": "GRP",
		"languages": []map[string]any{{"id": 1, "name": "English"}}, "quality": quality("Bluray-2160p", 1),
		"customFormatScore": 600, "indexerFlags": 0, "releaseType": "seasonPack", "qualityCutoffNotMet": true}}
	f.files[2] = []map[string]any{{"id": 601, "seriesId": 2, "seasonNumber": 1,
		"relativePath": "Season 01/Anime - S01E01 - 001 [Bluray-1080p]-SUB.mkv", "size": 3000000000,
		"dateAdded": "2024-03-01T00:00:00Z", "releaseGroup": "SUB", "quality": quality("Bluray-1080p", 1),
		"customFormatScore": 6000, "indexerFlags": 92, "releaseType": "singleEpisode", "qualityCutoffNotMet": false}}
	pack := "Show.S01.2160p.UHD.BluRay.x265-GRP"
	f.history = []map[string]any{
		sonarrGrab(1, 1, 101, "2024-01-31T10:00:00Z", strings.ToUpper(hashS1), pack),
		sonarrGrab(2, 1, 102, "2024-01-31T10:00:00Z", strings.ToUpper(hashS1), pack),
		sonarrImport(root, 3, 1, 101, "2024-02-01T00:00:00Z", strings.ToUpper(hashS1), pack, 501),
		sonarrImport(root, 4, 1, 102, "2024-02-01T00:00:00Z", strings.ToUpper(hashS1), pack, 501),
		{"id": 5, "seriesId": 2, "episodeId": 201, "eventType": "downloadFolderImported", "date": "2024-03-01T00:00:00Z",
			"sourceTitle": "[SUB] Anime - 01 (BD 1080p)", "quality": quality("Bluray-1080p", 1),
			"data": map[string]any{"fileId": "601"}},
	}
	return f
}

func TestImportSonarr(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	fake := newFakeSonarr("/mnt/shows")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	im := &Importer{Store: st, Now: fixedNow}
	src := Source{Instance: "sonarr_a", Flavour: "sonarr", Version: "tv-2160p", Library: "/mnt/shows"}

	sum := runImport(t, im, src, srv)
	if sum.Titles.Imported != 2 || sum.Titles.Unmapped != 0 {
		t.Errorf("titles %+v", sum.Titles)
	}
	if sum.Titles.Wanted != 1 || sum.Titles.CutoffMet != 1 {
		t.Errorf("states %+v (Show wanted: S01E03 aired without a file; Anime: E02 not aired)", sum.Titles)
	}
	if e := sum.Episodes; e.Total != 6 || e.Monitored != 5 || e.WithFile != 3 {
		t.Errorf("episodes %+v", e)
	}
	if f := sum.Files; f.Total != 2 || f.WithImport != 2 || f.WithHash != 1 || f.ManualImport != 1 ||
		f.WithReleaseTitle != 1 || f.ReleaseTitleFromImport != 1 {
		t.Errorf("files %+v", f)
	}
	if sum.Registry.Hashes != 1 || sum.Registry.Imported != 1 {
		t.Errorf("registry %+v", sum.Registry)
	}
	if len(sum.Warnings) != 0 {
		t.Errorf("warnings %v", sum.Warnings)
	}

	if n := one[int](t, st, `SELECT COUNT(*) FROM title WHERE kind = 'series' AND tvdb_id IN (1001, 2002)`); n != 2 {
		t.Errorf("series titles %d", n)
	}
	if s := one[string](t, st, `SELECT series_type || '/' || use_scene_numbering || '/' || original_language || '/' ||
		runtime_minutes FROM title WHERE tvdb_id = 2002`); s != "anime/1/Japanese/45" {
		t.Errorf("anime title %s", s)
	}
	if s := one[string](t, st, `SELECT series_type FROM arr_title WHERE instance = 'sonarr_a' AND arr_id = 2`); s != "anime" {
		t.Errorf("arr_title series type %q", s)
	}
	if n := one[int](t, st, `SELECT COUNT(*) FROM title_scene_mapping WHERE source = 'sonarr_a'`); n != 3 {
		t.Errorf("scene mappings %d", n)
	}
	if s := one[string](t, st, `SELECT scene_origin || '|' || comment FROM title_scene_mapping WHERE name = 'Anime Season Two'`); s != "unknown:tvdb|Limited matching" {
		t.Errorf("scene mapping %s", s)
	}
	if n := one[int](t, st, `SELECT COUNT(*) FROM episode`); n != 6 {
		t.Errorf("episodes %d", n)
	}
	if n := one[int](t, st, `SELECT COUNT(*) FROM episode_number`); n != 4 {
		t.Errorf("episode numbers %d (absolute 1 and 2, scene, scene absolute)", n)
	}
	if s := one[string](t, st, `SELECT GROUP_CONCAT(scheme || ':' || COALESCE(season, '-') || 'x' || number, ' ') FROM
		(SELECT * FROM episode_number n JOIN episode e ON e.id = n.episode_id WHERE e.season = 1 AND e.number = 1
		AND e.title_id = (SELECT id FROM title WHERE tvdb_id = 2002) ORDER BY scheme)`); s != "absolute_tvdb:-x1 scene:1x1 scene_absolute:-x1" {
		t.Errorf("anime E01 numbers %q", s)
	}
	if s := one[string](t, st, `SELECT GROUP_CONCAT(e.season || 'x' || e.number || '=' || s.state, ' ') FROM
		(SELECT * FROM episode_version_state ORDER BY episode_id) s JOIN episode e ON e.id = s.episode_id
		WHERE e.title_id = (SELECT id FROM title WHERE tvdb_id = 1001)`); s != "0x1=wanted 1x1=filled 1x2=filled 1x3=wanted" {
		t.Errorf("Show episode states %q", s)
	}
	if n := one[int](t, st, `SELECT current_score FROM episode_version_state s JOIN episode e ON e.id = s.episode_id
		WHERE e.title_id = (SELECT id FROM title WHERE tvdb_id = 2002) AND e.number = 1`); n != 6000 {
		t.Errorf("anime E01 score %d", n)
	}
	if s := one[string](t, st, `SELECT release_title || '|' || release_type FROM version_file WHERE arr_file_id = 501`); s != "Show.S01.2160p.UHD.BluRay.x265-GRP|seasonPack" {
		t.Errorf("pack file %q", s)
	}
	if n := one[int](t, st, `SELECT COUNT(*) FROM version_file_episode fe JOIN version_file f ON f.id = fe.version_file_id
		WHERE f.arr_file_id = 501`); n != 2 {
		t.Errorf("pack file episodes %d", n)
	}
	if n := one[int](t, st, `SELECT COUNT(*) FROM arr_alias WHERE instance = 'sonarr_a'`); n != 10 {
		t.Errorf("aliases %d (2 series, 6 episodes, 2 files)", n)
	}
	if n := one[int](t, st, `SELECT COUNT(*) FROM arr_history WHERE arr_episode_id IS NOT NULL AND title_id IS NOT NULL`); n != 5 {
		t.Errorf("history with episodes %d", n)
	}
	if n := one[int](t, st, `SELECT COUNT(*) FROM content_version cv JOIN content c ON c.id = cv.content_id
		WHERE c.info_hash = ? AND cv.imported_at IS NOT NULL`, hashS1); n != 1 {
		t.Errorf("registry pack %d", n)
	}
	var leak int
	st.Reader().QueryRow(`SELECT COUNT(*) FROM arr_history WHERE data LIKE '%passkey%' OR data LIKE '%://%'`).Scan(&leak)
	if leak != 0 {
		t.Errorf("%d history rows kept a URL", leak)
	}

	// A second run changes nothing.
	sum2 := runImport(t, im, src, srv)
	if sum2.Changes.Total() != 0 {
		t.Errorf("second run changed rows: %s", sum2.Changes)
	}

	// Upstream: S01E03 gets its own file via a new grab, the special leaves TVDB, a scene mapping goes, and the pack
	// is replaced by a single-episode file for E01 (E02 loses its file).
	fake.mu.Lock()
	fake.episodes[1] = []map[string]any{fake.episodes[1][0], fake.episodes[1][1], fake.episodes[1][2]}
	fake.episodes[1][0]["episodeFileId"] = 502
	fake.episodes[1][1]["episodeFileId"], fake.episodes[1][1]["hasFile"] = 0, false
	fake.episodes[1][2]["episodeFileId"], fake.episodes[1][2]["hasFile"] = 503, true
	fake.files[1] = []map[string]any{
		{"id": 502, "seriesId": 1, "seasonNumber": 1, "relativePath": "Season 01/Show - S01E01.mkv", "size": 9000000000,
			"dateAdded": "2024-05-01T00:00:00Z", "quality": quality("Bluray-2160p", 1), "releaseType": "singleEpisode",
			"customFormatScore": 700},
		{"id": 503, "seriesId": 1, "seasonNumber": 1, "relativePath": "Season 01/Show - S01E03.mkv", "size": 9000000000,
			"dateAdded": "2024-05-02T00:00:00Z", "quality": quality("Bluray-2160p", 1), "releaseType": "singleEpisode",
			"customFormatScore": 700},
	}
	fake.series[1]["alternateTitles"] = fake.series[1]["alternateTitles"].([]map[string]any)[:2]
	fake.history = append(fake.history,
		sonarrGrab(6, 1, 103, "2024-05-02T00:00:00Z", strings.ToUpper(hashS2), "Show.S01E03.2160p.BluRay-GRP"),
		sonarrImport("/mnt/shows", 7, 1, 103, "2024-05-02T01:00:00Z", strings.ToUpper(hashS2), "Show.S01E03.2160p.BluRay-GRP", 503))
	fake.mu.Unlock()
	// An episode the series metadata refresh stored (TVDB has it, the arr not yet) is not the import's to delete.
	if _, err := st.Writer().Exec(`INSERT INTO episode (title_id, season, number, name, tvdb_episode_id, created_at,
		updated_at) SELECT id, 1, 9, 'From TVDB', 555, 0, 0 FROM title WHERE tvdb_id = 1001`); err != nil {
		t.Fatal(err)
	}
	sum3 := runImport(t, im, src, srv)
	c := sum3.Changes
	if sum3.History.New != 2 || c["version_file"] == nil || c["version_file"].Inserted != 2 || c["version_file"].Deleted != 1 {
		t.Errorf("third run: history %+v, changes %s", sum3.History, c)
	}
	if c["episode"] == nil || c["episode"].Deleted != 1 || c["title_scene_mapping"].Deleted != 1 {
		t.Errorf("third run episode/mapping changes: %s", c)
	}
	if n := one[int](t, st, `SELECT COUNT(*) FROM episode WHERE tvdb_episode_id = 555`); n != 1 {
		t.Errorf("the import deleted the refresh's episode")
	}
	if s := one[string](t, st, `SELECT release_title FROM version_file WHERE arr_file_id = 503`); s != "Show.S01E03.2160p.BluRay-GRP" {
		t.Errorf("E03 file release title %q", s)
	}
	if s := one[string](t, st, `SELECT v.state FROM version v JOIN title t ON t.id = v.title_id WHERE t.tvdb_id = 1001`); s != "wanted" {
		t.Errorf("Show state %s (E02 lost its file)", s)
	}
	if n := one[int](t, st, `SELECT COUNT(*) FROM version_file_episode`); n != 3 {
		t.Errorf("file episodes %d", n)
	}
	if sum4 := runImport(t, im, src, srv); sum4.Changes.Total() != 0 {
		t.Errorf("fourth run changed rows: %s", sum4.Changes)
	}

	// A second instance with the same series in another version: one title, two versions; its episode states are its
	// own, and the first instance's scene mappings stay.
	fake2 := newFakeSonarr("/mnt/anime")
	fake2.series = fake2.series[1:]
	fake2.series[0]["seriesType"] = "standard"
	delete(fake2.episodes, 1)
	delete(fake2.files, 1)
	fake2.history = fake2.history[4:]
	srv2 := httptest.NewServer(fake2)
	defer srv2.Close()
	runImport(t, im, Source{Instance: "sonarr_b", Flavour: "sonarr", Version: "anime-tv", Library: "/mnt/anime"}, srv2)
	if n := one[int](t, st, `SELECT COUNT(*) FROM title WHERE tvdb_id = 2002`); n != 1 {
		t.Errorf("anime titles %d", n)
	}
	if n := one[int](t, st, `SELECT COUNT(*) FROM version v JOIN title t ON t.id = v.title_id WHERE t.tvdb_id = 2002`); n != 2 {
		t.Errorf("anime versions %d", n)
	}
	if s := one[string](t, st, `SELECT series_type FROM title WHERE tvdb_id = 2002`); s != "anime" {
		t.Errorf("title series type %q: the first import's stays", s)
	}
	if s := one[string](t, st, `SELECT series_type FROM arr_title WHERE instance = 'sonarr_b'`); s != "standard" {
		t.Errorf("sonarr_b series type %q", s)
	}
	if n := one[int](t, st, `SELECT COUNT(*) FROM episode_version_state s JOIN version v ON v.id = s.version_id
		WHERE v.policy_key = 'anime-tv'`); n != 2 {
		t.Errorf("anime-tv episode states %d", n)
	}
	if n := one[int](t, st, `SELECT COUNT(*) FROM episode e JOIN title t ON t.id = e.title_id WHERE t.tvdb_id = 2002`); n != 2 {
		t.Errorf("shared episodes %d", n)
	}
	if n := one[int](t, st, `SELECT COUNT(*) FROM title_scene_mapping`); n != 5 {
		t.Errorf("scene mappings %d (2 from sonarr_a, 3 from sonarr_b)", n)
	}
}

func TestPruneRuns(t *testing.T) {
	st := openStore(t)
	ctx := t.Context()
	now := fixedNow()
	day := int64(24 * time.Hour / time.Millisecond)
	at := store.Millis(now)
	for _, r := range []struct {
		inst string
		age  int64 // days
	}{{"a", 30}, {"a", 9}, {"a", 1}, {"b", 20}, {"b", 10}, {"c", 3}} {
		if _, err := st.Writer().ExecContext(ctx, `INSERT INTO import_run (instance, started_at, finished_at) VALUES (?, ?, ?)`,
			r.inst, at-r.age*day, at-r.age*day); err != nil {
			t.Fatal(err)
		}
	}
	n, err := PruneRuns(ctx, st, now, RunKeep)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("pruned %d, want 3 (a 30 and 9 days, b 20 days)", n)
	}
	if s := one[string](t, st, `SELECT GROUP_CONCAT(instance, '') FROM (SELECT instance FROM import_run ORDER BY id)`); s != "abc" {
		t.Errorf("left %q", s)
	}
}

// arrview's episode monitoring writes episode_version_state rows (wanted, monitored) between imports; the import
// takes them over: the arr owns the version during shadow, so its monitored flag and file state win.
func TestImportSonarrAfterArrviewWrites(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	srv := httptest.NewServer(newFakeSonarr("/mnt/shows"))
	defer srv.Close()
	im := &Importer{Store: st, Now: fixedNow}
	src := Source{Instance: "sonarr_a", Flavour: "sonarr", Version: "tv-2160p", Library: "/mnt/shows"}
	runImport(t, im, src, srv)
	ctx := t.Context()
	if _, err := st.Writer().ExecContext(ctx, `DELETE FROM episode_version_state WHERE episode_id =
		(SELECT id FROM episode WHERE season = 1 AND number = 1 AND title_id = (SELECT id FROM title WHERE tvdb_id = 1001))`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().ExecContext(ctx, `INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at)
		SELECT v.id, e.id, 0, 'wanted', 0 FROM episode e JOIN version v ON v.title_id = e.title_id
		WHERE e.season = 1 AND e.number = 1 AND v.policy_key = 'tv-2160p' AND e.title_id = (SELECT id FROM title WHERE tvdb_id = 1001)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().ExecContext(ctx, `UPDATE episode_version_state SET monitored = 0`); err != nil {
		t.Fatal(err)
	}
	sum := runImport(t, im, src, srv)
	if c := sum.Changes["episode_version_state"]; c == nil || c.Updated != 5 || c.Inserted != 0 {
		t.Errorf("changes %s", sum.Changes)
	}
	if s := one[string](t, st, `SELECT s.state || '/' || s.monitored FROM episode_version_state s JOIN episode e ON e.id = s.episode_id
		WHERE e.season = 1 AND e.number = 1 AND e.title_id = (SELECT id FROM title WHERE tvdb_id = 1001)`); s != "filled/1" {
		t.Errorf("S01E01 %s", s)
	}
}
