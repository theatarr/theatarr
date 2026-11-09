package arrview

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// seedBlocklist adds router_blocklist rows: two of /radarr's versions (a: from a download of ours, b: blocklisted
// from an imported arr record, without a download), two of /sonarr's (c: from a download; d: oldest, its title gone). It returns the rows' ids (rowid) by hash prefix.
func (e *env) seedBlocklist() map[string]int64 {
	e.t.Helper()
	now := ms(testNow)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		score, size_bytes, episodes, provider, provider_torrent_id, progress, state, error, formats, created_at, updated_at)
		VALUES (51, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.WEB-DL.H265-BAD', 'torrentio', 'WEBDL-2160p', 40, 1000,
		 '[]', 'rd', 'T1', 100, 'failed', 'library: sample', '[{"name":"x265","score":10}]', ?, ?),
		(52, 10, 10, 'tv-2160p', ?, 'The.Show.S01E02.2160p.WEB-DL-BAD', 'dmm', 'WEBDL-2160p', 20, 1000,
		 '[102]', 'ad', 'T2', 100, 'failed', 'library: sample', NULL, ?, ?)`,
		strings.Repeat("a", 40), now, now, strings.Repeat("c", 40), now, now)
	e.exec(`INSERT INTO router_blocklist (version_key, info_hash, title_id, release_title, reason, source, download_id,
		created_at) VALUES
		('movie-web', ?, 1, 'Alpha.2020.2160p.WEB-DL.H265-BAD', 'sample guard: 40 MB', 'library', 51, ?),
		('movie-remux', ?, 1, 'Alpha.2020.2160p.BluRay.REMUX-OLD', 'marked failed by Ruddarr', 'history', NULL, ?),
		('tv-2160p', ?, 10, 'The.Show.S01E02.2160p.WEB-DL-BAD', 'sample guard: 20 MB', 'library', 52, ?),
		('anime-tv', ?, NULL, 'Other.S01E01-GRP', 'no video', 'library', NULL, ?)`,
		strings.Repeat("a", 40), now-3000, strings.Repeat("b", 40), now-2000, strings.Repeat("c", 40), now-1000,
		strings.Repeat("d", 40), now-5000)
	out := map[string]int64{}
	for _, h := range []string{"a", "b", "c", "d"} {
		out[h] = e.one(`SELECT rowid FROM router_blocklist WHERE info_hash = ?`, strings.Repeat(h, 40))
	}
	return out
}

// GET blocklist: a paged PagingResource<BlocklistResource> of the view's versions only, newest first, shaped as the
// arrs' BlocklistResource (Radarr @ a96bf7c Blocklist/BlocklistResource.cs: id, movieId, sourceTitle, languages,
// quality, customFormats, date, protocol, indexer, message, movie; Sonarr @ cab419a: seriesId, episodeIds, series).
func TestBlocklistList(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ids := e.seedBlocklist()
	var p struct {
		Page          int              `json:"page"`
		PageSize      int              `json:"pageSize"`
		SortKey       string           `json:"sortKey"`
		SortDirection string           `json:"sortDirection"`
		TotalRecords  int              `json:"totalRecords"`
		Records       []map[string]any `json:"records"`
	}
	rec := e.call("GET", "/radarr/api/v3/blocklist", nil, 200, &p)
	if p.TotalRecords != 2 || len(p.Records) != 2 || p.Page != 1 || p.PageSize != 10 || p.SortKey != "date" ||
		p.SortDirection != "descending" {
		t.Fatalf("radarr blocklist %s", rec.Body)
	}
	wantKeys(t, "radarr page", decode(t, rec.Body.Bytes()), fields("page pageSize sortKey sortDirection totalRecords records"))
	r0 := decode(t, rec.Body.Bytes()).(map[string]any)["records"].([]any)[0]
	wantKeys(t, "BlocklistResource (Radarr)", r0,
		fields("id movieId sourceTitle languages quality customFormats date protocol message movie"), "indexer")
	b := p.Records[0]
	if int64(b["id"].(float64)) != ids["b"] || b["movieId"].(float64) != IDBase+1 ||
		b["sourceTitle"] != "Alpha.2020.2160p.BluRay.REMUX-OLD" || b["protocol"] != "torrent" ||
		b["message"] != "marked failed by Ruddarr" || b["date"] != "2026-10-26T11:59:58Z" {
		t.Errorf("newest radarr row %v", b)
	}
	if q := b["quality"].(map[string]any)["quality"].(map[string]any); q["name"] != "Remux-2160p" {
		t.Errorf("parsed quality of a row without a download: %v", q)
	}
	a := p.Records[1]
	if int64(a["id"].(float64)) != ids["a"] || a["indexer"] != "torrentio" ||
		a["quality"].(map[string]any)["quality"].(map[string]any)["name"] != "WEBDL-2160p" {
		t.Errorf("download row %v", a)
	}
	if cf := a["customFormats"].([]any); len(cf) != 1 || cf[0].(map[string]any)["name"] != "x265" {
		t.Errorf("custom formats %v", a["customFormats"])
	}
	if m := a["movie"].(map[string]any); m["title"] != "Alpha" || m["id"].(float64) != IDBase+1 {
		t.Errorf("embedded movie %v", a["movie"])
	}

	// Paging and sorting.
	p.Records = nil
	e.call("GET", "/radarr/api/v3/blocklist?page=2&pageSize=1&sortKey=sourceTitle&sortDirection=ascending", nil, 200, &p)
	if p.TotalRecords != 2 || len(p.Records) != 1 || p.Records[0]["sourceTitle"] != "Alpha.2020.2160p.WEB-DL.H265-BAD" ||
		p.SortKey != "sourceTitle" {
		t.Errorf("page 2 by sourceTitle %+v", p)
	}
	// movieIds filter, protocols (usenet: none).
	p.Records = nil
	e.call("GET", "/radarr/api/v3/blocklist?movieIds="+strconv.Itoa(IDBase+2), nil, 200, &p)
	if p.TotalRecords != 0 {
		t.Errorf("movieIds filter %+v", p)
	}
	e.call("GET", "/radarr/api/v3/blocklist?protocols=1", nil, 200, &p)
	if p.TotalRecords != 0 {
		t.Errorf("usenet filter %+v", p)
	}
	// Radarr's per-movie list.
	var list []map[string]any
	e.call("GET", "/radarr/api/v3/blocklist/movie?movieId="+strconv.Itoa(IDBase+1), nil, 200, &list)
	if len(list) != 2 {
		t.Errorf("blocklist/movie %v", list)
	}

	// Sonarr: its own row only, with the series and the episodes of the download.
	p.Records = nil
	rec = e.call("GET", "/sonarr/api/v3/blocklist", nil, 200, &p)
	if p.TotalRecords != 2 {
		t.Fatalf("sonarr blocklist %s", rec.Body)
	}
	s0 := decode(t, rec.Body.Bytes()).(map[string]any)["records"].([]any)[0]
	wantKeys(t, "BlocklistResource (Sonarr)", s0,
		fields("id seriesId episodeIds sourceTitle languages quality customFormats date protocol indexer message series"))
	wantKeys(t, "BlocklistResource (Sonarr), no title", decode(t, rec.Body.Bytes()).(map[string]any)["records"].([]any)[1],
		fields("id seriesId episodeIds sourceTitle languages quality customFormats date protocol message"))
	s := p.Records[0]
	if int64(s["id"].(float64)) != ids["c"] || s["seriesId"].(float64) != IDBase+10 ||
		len(s["episodeIds"].([]any)) != 1 || s["episodeIds"].([]any)[0].(float64) != 102 {
		t.Errorf("sonarr row %v", s)
	}
	if d := p.Records[1]; int64(d["id"].(float64)) != ids["d"] || d["seriesId"].(float64) != 0 || d["series"] != nil {
		t.Errorf("row without a title %v", d)
	}
	// Sonarr has no blocklist/movie.
	if rec := e.do("GET", "/sonarr/api/v3/blocklist/movie?movieId=1", e.key, nil); rec.Code != http.StatusNotFound {
		t.Errorf("sonarr blocklist/movie: %d", rec.Code)
	}
}

// DELETE blocklist/{id} and blocklist/bulk: the web UI's audited unblock (router.Unblock with a journal action), for
// the view's own rows only; nothing else changes (the download stays failed; no provider is called: the views have
// none).
func TestBlocklistDelete(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ids := e.seedBlocklist()
	// Another view's row is not this view's to remove.
	if rec := e.do("DELETE", "/radarr/api/v3/blocklist/"+strconv.FormatInt(ids["c"], 10), e.key, nil); rec.Code != http.StatusNotFound {
		t.Errorf("radarr deleting sonarr's row: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("DELETE", "/radarr/api/v3/blocklist/999999", e.key, nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown id: %d", rec.Code)
	}
	e.call("DELETE", "/radarr/api/v3/blocklist/"+strconv.FormatInt(ids["a"], 10), nil, 200, nil)
	if n := e.one(`SELECT COUNT(*) FROM router_blocklist WHERE info_hash = ?`, strings.Repeat("a", 40)); n != 0 {
		t.Errorf("row a still blocklisted")
	}
	var by, detail, version string
	if err := e.st.Reader().QueryRow(`SELECT by, detail, version_key FROM journal_action WHERE action = 'unblock'
		AND info_hash = ?`, strings.Repeat("a", 40)).Scan(&by, &detail, &version); err != nil {
		t.Fatalf("no journal action: %v", err)
	}
	if by != "key:seerr" || version != "movie-web" || !strings.Contains(detail, "sample guard") {
		t.Errorf("journal action by %q version %q detail %q", by, version, detail)
	}
	if st := e.one(`SELECT COUNT(*) FROM download WHERE id = 51 AND state = 'failed'`); st != 1 {
		t.Error("the download changed")
	}
	// Bulk: the view's rows go, another view's stay; unknown ids are skipped (BlocklistService.Delete).
	var out map[string]any
	e.call("DELETE", "/sonarr/api/v3/blocklist/bulk", map[string]any{"ids": []int64{ids["b"], ids["c"], ids["d"], 999999}},
		200, &out)
	if len(out) != 0 {
		t.Errorf("bulk answer %v", out)
	}
	if n := e.one(`SELECT COUNT(*) FROM router_blocklist`); n != 1 {
		t.Errorf("%d rows left, want 1 (b of radarr)", n)
	}
	if n := e.one(`SELECT COUNT(*) FROM journal_action WHERE action = 'unblock'`); n != 3 {
		t.Errorf("%d journal actions", n)
	}
	// A per-version view sees and removes its version's row.
	if rec := e.do("DELETE", "/v/movie-web/radarr/api/v3/blocklist/"+strconv.FormatInt(ids["b"], 10), e.key, nil); rec.Code != http.StatusNotFound {
		t.Errorf("movie-web view deleting movie-remux's row: %d", rec.Code)
	}
	e.call("DELETE", "/v/movie-remux/radarr/api/v3/blocklist/"+strconv.FormatInt(ids["b"], 10), nil, 200, nil)
	if n := e.one(`SELECT COUNT(*) FROM router_blocklist`); n != 0 {
		t.Errorf("%d rows left", n)
	}
}
