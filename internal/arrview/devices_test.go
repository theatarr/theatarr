package arrview

import (
	"strconv"
	"strings"
	"testing"
)

// seedDownloads adds router downloads: 1 movie-web downloading (title 1), 2 movie-remux imported (title 1),
// 3 tv-2160p downloading (episodes 101, 102), 4 movie-web queued, never added.
func (e *env) seedDownloads() {
	e.t.Helper()
	at := ms(testNow.Add(-3600e9))
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		score, size_bytes, episodes, provider, provider_torrent_id, progress, state, source, created_at, updated_at,
		added_at, imported_at) VALUES
		(1, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.WEB.H265-GRP', 'dmm', 'WEBDL-2160p', 1700, 10000, '[]', 'rd',
		 'T1', 40, 'downloading', 'acquire:1', ?, ?, ?, NULL),
		(2, 1, 1, 'movie-remux', ?, 'Alpha.2020.2160p.BluRay.REMUX-GRP', 'dmm', 'Remux-2160p', 3200, 60000, '[]', 'rd',
		 'T2', 100, 'imported', 'acquire:2', ?, ?, ?, ?),
		(3, 10, 10, 'tv-2160p', ?, 'The.Show.S01.2160p.WEB-DL-GRP', 'dmm', 'WEBDL-2160p', 0, 2000, '[101, 102]',
		 'ad', 'A1', 50, 'downloading', 'rss', ?, ?, ?, NULL),
		(4, 1, 2, 'movie-web', ?, 'Alpha.Queued', 'dmm', 'WEBDL-2160p', 0, 1, '[]', NULL, NULL, 0, 'queued', 'rss',
		 ?, ?, NULL, NULL)`,
		strings.Repeat("a", 40), at, at, at, strings.Repeat("b", 40), at, at, at, at+60000,
		strings.Repeat("c", 40), at, at, at, strings.Repeat("d", 40), at, at)
}

func TestQueueDelete(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedDownloads()
	// Another view's download: 404, nothing changes.
	e.call("DELETE", "/v/movie-remux/radarr/api/v3/queue/1", nil, 404, nil)
	e.call("DELETE", "/radarr/api/v3/queue/1?removeFromClient=true&blocklist=false", nil, 200, nil)
	if st := e.str(`SELECT state FROM download WHERE id = 1`); st != "failed" {
		t.Errorf("state %s", st)
	}
	if n := e.one(`SELECT COUNT(*) FROM router_blocklist`); n != 0 {
		t.Errorf("blocklisted without blocklist=true")
	}
	// Bulk with blocklist and skipRedownload: blocklisted, no search command.
	e.call("DELETE", "/sonarr/api/v3/queue/bulk?removeFromClient=true&blocklist=true&skipRedownload=true", map[string]any{
		"ids": []int{3}}, 200, nil)
	if n := e.one(`SELECT COUNT(*) FROM router_blocklist WHERE version_key = 'tv-2160p' AND source = 'queue'`); n != 1 {
		t.Errorf("blocklist rows %d", n)
	}
	if n := e.one(`SELECT COUNT(*) FROM arrview_command`); n != 0 {
		t.Errorf("commands %d", n)
	}
	// blocklist without skipRedownload: a search command (all shadow here: recorded completed, nothing searched).
	e.call("DELETE", "/radarr/api/v3/queue/4?blocklist=true", nil, 200, nil)
	if n := e.one(`SELECT COUNT(*) FROM arrview_command WHERE name = 'MoviesSearch'`); n != 1 {
		t.Errorf("redownload command %d", n)
	}
}

func TestQueueDeleteHeldImport(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
		state, episodes, created_at, updated_at) VALUES (5, 1, 2, 'movie-web', ?, 'Alpha.Held', 'dmm', 'ready', '[]', ?, ?)`,
		strings.Repeat("5", 40), ms(testNow), ms(testNow))
	e.exec(`INSERT INTO library_import (download_id, state, last_error, created_at, updated_at)
		VALUES (5, 'rejected', 'no file matched', ?, ?)`, ms(testNow), ms(testNow))

	e.call("DELETE", "/radarr/api/v3/queue/5?removeFromClient=true&blocklist=true&skipRedownload=true", nil, 200, nil)
	if got := e.str(`SELECT state FROM download WHERE id = 5`); got != "failed" {
		t.Errorf("held download state %s", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM router_blocklist WHERE version_key = 'movie-web' AND info_hash = ? AND source = 'queue'`,
		strings.Repeat("5", 40)); got != 1 {
		t.Errorf("held download blocklist rows %d", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM arrview_command`); got != 0 {
		t.Errorf("held queue removal unexpectedly searched: %d", got)
	}
}

func TestHistoryFromDownloadsAndFailed(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedDownloads()
	var p struct {
		TotalRecords int
		Records      []map[string]any
	}
	e.call("GET", "/radarr/api/v3/history?page=1&pageSize=100&sortKey=date&sortDirection=descending&downloadId="+
		strings.Repeat("A", 40), nil, 200, &p)
	if p.TotalRecords != 1 || p.Records[0]["eventType"] != "grabbed" ||
		p.Records[0]["id"].(float64) != HistoryBase+11 {
		t.Fatalf("history by downloadId %v", p.Records)
	}
	data := p.Records[0]["data"].(map[string]any)
	if data["indexer"] != "dmm" || data["guid"] != "theatarr:dmm:"+strings.Repeat("a", 40) {
		t.Errorf("data %v", data)
	}
	var hs []map[string]any
	e.call("GET", "/radarr/api/v3/history/movie?movieId="+strconv.Itoa(IDBase+1)+"&eventType=3", nil, 200, &hs)
	if len(hs) != 2 { // the arr's import and ours
		t.Errorf("imports %v", hs)
	}
	e.call("GET", "/sonarr/api/v3/history?episodeId=101", nil, 200, &p)
	if p.TotalRecords != 1 || p.Records[0]["episodeId"].(float64) != 101 {
		t.Errorf("by episode %v", p.Records)
	}
	// A pack grab (download 3: episodes 101, 102) has one record per episode with its own id; the first keeps the
	// plain id. Paging, hydration and history/failed agree on the second episode's id.
	e.call("GET", "/sonarr/api/v3/history?episodeId=102&eventType=1&includeEpisode=true", nil, 200, &p)
	second := int64(HistoryBase + 31 + historyEpisodeStride)
	if p.TotalRecords != 1 || p.Records[0]["id"].(float64) != float64(second) || p.Records[0]["episodeId"].(float64) != 102 ||
		p.Records[0]["episode"].(map[string]any)["id"].(float64) != 102 {
		t.Errorf("pack episode %v", p.Records)
	}
	e.call("GET", "/sonarr/api/v3/history?episodeId=101&eventType=1", nil, 200, &p)
	if p.TotalRecords != 1 || p.Records[0]["id"].(float64) != HistoryBase+31 {
		t.Errorf("pack first episode %v", p.Records)
	}
	e.call("GET", "/sonarr/api/v3/history?pageSize=1&page=2&downloadId="+strings.Repeat("c", 40), nil, 200, &p)
	if p.TotalRecords != 2 || len(p.Records) != 1 {
		t.Errorf("pack paging %v", p.Records)
	}
	e.call("POST", "/sonarr/api/v3/history/failed/"+strconv.FormatInt(HistoryBase+31+2*historyEpisodeStride, 10), nil, 404, nil)
	e.call("POST", "/radarr/api/v3/history/failed/"+strconv.FormatInt(HistoryBase+11+historyEpisodeStride, 10), nil, 404, nil)
	e.call("POST", "/sonarr/api/v3/history/failed/"+strconv.FormatInt(second, 10), nil, 200, nil)
	if e.str(`SELECT state FROM download WHERE id = 3`) != "failed" {
		t.Error("history/failed on a pack episode's record")
	}
	e.exec(`DELETE FROM router_blocklist`)
	e.exec(`DELETE FROM arrview_command`)
	// Mark the grab failed: the download fails, the hash is blocklisted, a search is recorded.
	e.call("POST", "/radarr/api/v3/history/failed/"+strconv.Itoa(HistoryBase+11), nil, 200, nil)
	if e.str(`SELECT state FROM download WHERE id = 1`) != "failed" ||
		e.one(`SELECT COUNT(*) FROM router_blocklist WHERE source = 'history'`) != 1 ||
		e.one(`SELECT COUNT(*) FROM arrview_command`) != 1 {
		t.Error("history/failed on a download of ours")
	}
	e.call("POST", "/radarr/api/v3/history/failed/999999", nil, 404, nil)
	// An imported arr record: its hash is blocklisted for its version.
	rowid := e.one(`SELECT rowid FROM arr_history WHERE arr_id = 40`)
	e.call("POST", "/radarr/api/v3/history/failed/"+strconv.FormatInt(rowid, 10), nil, 200, nil)
	if e.one(`SELECT COUNT(*) FROM router_blocklist WHERE version_key = 'movie-remux'`) != 1 {
		t.Error("arr history failed")
	}
}

func (e *env) str(q string, args ...any) string {
	e.t.Helper()
	var s string
	if err := e.st.Reader().QueryRow(q, args...).Scan(&s); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return s
}
