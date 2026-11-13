package web

import (
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
)

// userBody is GET path as a caller without "see files" (manageEnv.userMux).
func (e *manageEnv) userBody(path string) string {
	e.t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set(auth.APIKeyHeader, e.key)
	rec := httptest.NewRecorder()
	e.userMux().ServeHTTP(rec, r)
	if rec.Code != 200 {
		e.t.Fatalf("GET %s as user: %d %s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// seedHeld adds content id with hash and its provider rows: rd holds it complete, tb lists it still downloading, ad
// held it once (absent now).
func seedHeld(e *manageEnv, id int64, hash string) {
	e.exec(`INSERT INTO content (id, info_hash, name, state, first_seen_at, last_seen_at) VALUES (?, ?, 'c', 'present', 0, 0)`,
		id, hash)
	e.exec(`INSERT INTO content_provider (content_id, provider, torrent_id, seen_at, state, status, progress) VALUES
		(?, 'rd', 'R1', 0, 'present', 'downloaded', 100), (?, 'tb', 'T1', 0, 'present', 'downloading', 40),
		(?, 'ad', 'A1', 0, 'absent', 'Ready', 100)`, id, id, id)
}

// TestTitleCachedOnAdoptedLink: an adopted link (no content file, no download) is resolved through the hash it
// stores: its file metadata's InfoHash is empty, so the adopted source's hash is used. Only rd, which holds the
// content complete, is shown; tb (still downloading) and ad (gone) are not. The list row, the provider filter and
// the provider facet agree with the title page.
func TestTitleCachedOnAdoptedLink(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	e.exec(`DELETE FROM version_file WHERE version_id = 1`) // a Theatarr library: links only
	hash := strings.Repeat("ab", 20)
	seedHeld(e, 1, hash)
	e.exec(`INSERT INTO link (id, path, target, title_id, quality, file_metadata, adopted_from, adopted_source, state,
		created_at, updated_at) VALUES (1, '/theatarr/Alpha (2020)/Alpha.mkv', '/mount/Alpha.mkv', 1, 'Remux-2160p',
		'{"InfoHash":"","Size":1000}', 'radarr /movies/Alpha.mkv', ?, 'active', 0, 0)`,
		`{"from":"radarr /movies/Alpha.mkv","hash":"`+strings.ToUpper(hash)+`"}`)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 1)`)

	var d TitleDetail
	e.call("GET", APIBase+"/titles/1", nil, 200, &d)
	if v := d.Versions[0]; !slices.Equal(v.CachedOn, []string{"rd"}) || !slices.Equal(v.Providers, []string{"rd"}) {
		t.Errorf("title page: cached_on %v providers %v, want [rd]", v.CachedOn, v.Providers)
	}
	var p TitlePage
	e.call("GET", APIBase+"/titles?kind=movie&q=Alpha", nil, 200, &p)
	if len(p.Items) != 1 || !slices.Equal(p.Items[0].Versions[0].Providers, []string{"rd"}) {
		t.Errorf("list row: %+v", p.Items)
	}
	for q, want := range map[string]int{"provider=rd": 1, "provider=tb": 0, "provider=ad": 0} {
		p = TitlePage{}
		e.call("GET", APIBase+"/titles?kind=movie&"+q, nil, 200, &p)
		if p.Total != want {
			t.Errorf("%s: %d titles, want %d", q, p.Total, want)
		}
	}
	var f TitleFacets
	e.call("GET", APIBase+"/titles/facets?kind=movie", nil, 200, &f)
	if len(f.Providers) != 1 || f.Providers[0] != (FacetCount{Value: "rd", Count: 1}) {
		t.Errorf("provider facet: %+v", f.Providers)
	}
	// Nothing about providers for a caller without "see files".
	if body := e.userBody(APIBase + "/titles/1"); strings.Contains(body, `"cached_on"`) || strings.Contains(body, `"providers"`) {
		t.Errorf("user title page: %s", body)
	}
}

// TestTitleCurrentFromLink: a movie's current file takes group, languages, edition and formats from its Theatarr
// link's file metadata; an adopted link, which stores no formats, shows no score either. A caller without "see
// files" keeps the languages and edition only.
func TestTitleCurrentFromLink(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	e.exec(`DELETE FROM version_file WHERE version_id = 1`)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, size_bytes,
		state, created_at, updated_at) VALUES (1, 1, 1, 'movie-remux', 'aa', 'Alpha.2020.IMAX.2160p.WEB-DL-GRP', 'dmm',
		1000, 'imported', 0, 0)`)
	e.exec(`INSERT INTO link (id, path, target, title_id, download_id, release_title, quality, score, file_metadata,
		state, created_at, updated_at) VALUES (1, '/movies/Alpha (2020)/Alpha.mkv', '/mount/Alpha.mkv', 1, 1,
		'Alpha.2020.IMAX.2160p.WEB-DL-GRP', 'WEBDL-2160p', 1500, '{"Formats":[{"name":"DV HDR10","score":1500}],
		"Score":1500,"ReleaseGroup":"GRP","Edition":"IMAX","Languages":["English","French"],"Size":1000}', 'active', 0, 0)`)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 1)`)
	var d TitleDetail
	e.call("GET", APIBase+"/titles/1", nil, 200, &d)
	c := d.Versions[0].Current
	if u := d.Versions[0].Upgrade; u == nil || u.Score == nil || *u.Score != 1500 {
		t.Errorf("upgrade: want the score: %+v", u)
	}
	if c == nil || c.ReleaseGroup != "GRP" || c.Edition != "IMAX" || !slices.Equal(c.Languages, []string{"English", "French"}) ||
		len(c.Formats) != 1 || c.Formats[0].Name != "DV HDR10" || c.Formats[0].Score != 1500 || c.Score == nil || *c.Score != 1500 {
		t.Fatalf("current %+v", c)
	}
	body := e.userBody(APIBase + "/titles/1")
	for _, s := range []string{`"formats"`, "DV HDR10", "GRP", `"score"`} {
		if strings.Contains(body, s) {
			t.Errorf("user title page shows %s: %s", s, body)
		}
	}
	if !strings.Contains(body, `"languages":["English","French"]`) || !strings.Contains(body, `"edition":"IMAX"`) {
		t.Errorf("user title page: want languages and edition: %s", body)
	}

	// Adopted: a stored score but no formats.
	e.exec(`UPDATE link SET download_id = NULL, file_metadata = '{"Score":900,"Size":1000}', score = 900 WHERE id = 1`)
	d = TitleDetail{}
	e.call("GET", APIBase+"/titles/1", nil, 200, &d)
	if c := d.Versions[0].Current; c == nil || c.Score != nil || c.Formats != nil {
		t.Errorf("adopted current: %+v", c)
	}
	if u := d.Versions[0].Upgrade; u == nil || u.Score != nil {
		t.Errorf("adopted upgrade: want no score: %+v", u)
	}
}

// TestTitleRemovedCopy: a removed copy is marked removed on the title page and counts in none of its rollups; the
// list row has no pill for it and its availability and size leave it out, as the availability and provider filters
// and the facets do; the episode list has no column for a removed series copy.
func TestTitleRemovedCopy(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	// Alpha: movie-remux filled (9 GB arr file); movie-web wanted with a 4 GB link rd holds, removed.
	hash := strings.Repeat("ef", 20)
	seedHeld(e, 1, hash)
	e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at) VALUES
		(2, 1, 'movie-web', 0, 'wanted', 0, 0)`)
	e.exec(`INSERT INTO link (id, path, target, title_id, quality, file_metadata, state, created_at, updated_at) VALUES
		(1, '/web/Alpha.mkv', '/mount/Alpha.mkv', 1, 'WEBDL-2160p', ?, 'active', 0, 99999999999999)`,
		`{"Size":4000000000,"InfoHash":"`+hash+`"}`)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 2)`)
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (2, 'web', 0)`)

	var d TitleDetail
	e.call("GET", APIBase+"/titles/1", nil, 200, &d)
	byKey := map[string]Version{}
	for _, v := range d.Versions {
		byKey[v.Key] = v
	}
	if !byKey["movie-web"].Removed || byKey["movie-remux"].Removed {
		t.Errorf("removed marks: %+v", d.Versions)
	}
	if d.Availability != "upgrade" || d.SizeBytes == nil || *d.SizeBytes != 9000000000 || d.AddedAt == nil ||
		d.AddedAt.UnixMilli() == 99999999999999 {
		t.Errorf("title rollups: availability %q size %v added %v", d.Availability, d.SizeBytes, d.AddedAt)
	}
	if body := e.userBody(APIBase + "/titles/1"); !strings.Contains(body, `"removed":true`) {
		t.Errorf("user title page: want removed: %s", body)
	}

	var p TitlePage
	e.call("GET", APIBase+"/titles?kind=movie&q=Alpha", nil, 200, &p)
	if len(p.Items) != 1 || len(p.Items[0].Versions) != 1 || p.Items[0].Versions[0].Key != "movie-remux" ||
		p.Items[0].Availability != "upgrade" || *p.Items[0].SizeBytes != 9000000000 {
		t.Errorf("list row: %+v", p.Items)
	}
	for q, want := range map[string]int{"availability=upgrade": 1, "availability=partial": 0, "provider=rd": 0} {
		p = TitlePage{}
		e.call("GET", APIBase+"/titles?kind=movie&"+q, nil, 200, &p)
		if p.Total != want {
			t.Errorf("%s: %d titles, want %d", q, p.Total, want)
		}
	}
	var f TitleFacets
	e.call("GET", APIBase+"/titles/facets?kind=movie", nil, 200, &f)
	if len(f.Providers) != 0 || f.Availability["upgrade"] != 1 || f.Availability["partial"] != 0 {
		t.Errorf("facets: providers %+v availability %v", f.Providers, f.Availability)
	}

	// The Show: tv-2160p removed, anime-tv kept.
	e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at) VALUES
		(11, 10, 'anime-tv', 1, 'wanted', 0, 0)`)
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (10, 'sonarr', 0)`)
	d = TitleDetail{}
	e.call("GET", APIBase+"/titles/10", nil, 200, &d)
	for _, v := range d.Versions {
		if v.Removed != (v.Key == "tv-2160p") {
			t.Errorf("series removed mark: %s %v", v.Key, v.Removed)
		}
	}
	var el EpisodeList
	e.call("GET", APIBase+"/titles/10/episodes", nil, 200, &el)
	if len(el.Versions) != 1 || el.Versions[0].Key != "anime-tv" {
		t.Errorf("episode versions: %+v", el.Versions)
	}
	for _, ep := range el.Episodes {
		if _, ok := ep.Versions["tv-2160p"]; ok {
			t.Errorf("episode %d has a removed copy's cell", ep.ID)
		}
	}
	for _, s := range el.Seasons {
		if _, ok := s.Versions["tv-2160p"]; ok {
			t.Errorf("season %d counts a removed copy", s.Season)
		}
	}
}

// TestEpisodeCellFromLink: an episode cell held by a Theatarr link carries its release title, the release group its
// file metadata keeps and the providers holding its content now; so does the episode route's file.
func TestEpisodeCellFromLink(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	hash := strings.Repeat("cd", 20)
	seedHeld(e, 1, hash)
	e.exec(`INSERT INTO link (id, path, target, title_id, release_title, quality, file_metadata, state, created_at,
		updated_at) VALUES (1, '/tv/Show/S01E01.theatarr.mkv', '/mount/S01E01.mkv', 10, 'Show.S01E01.1080p.WEB-DL-NTb',
		'WEBDL-1080p', ?, 'active', 0, 0)`, `{"InfoHash":"`+hash+`","ReleaseGroup":"NTb","Size":1000}`)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 10)`)
	e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (1, 101)`)
	var el EpisodeList
	e.call("GET", APIBase+"/titles/10/episodes?season=1", nil, 200, &el)
	var cell *EpisodeCell
	for _, ep := range el.Episodes {
		if ep.ID == 101 {
			c := ep.Versions["tv-2160p"]
			cell = &c
		}
	}
	if cell == nil || cell.ReleaseTitle != "Show.S01E01.1080p.WEB-DL-NTb" || cell.ReleaseGroup != "NTb" ||
		!slices.Equal(cell.CachedOn, []string{"rd"}) {
		t.Fatalf("cell %+v", cell)
	}
	var ep EpisodeDetail
	e.call("GET", APIBase+"/titles/10/episodes/101", nil, 200, &ep)
	if f := ep.Versions[0].File; f == nil || f.LinkID != 1 || !slices.Equal(f.CachedOn, []string{"rd"}) {
		t.Fatalf("episode file %+v", ep.Versions[0].File)
	}
	for _, path := range []string{"/titles/10/episodes?season=1", "/titles/10/episodes/101"} {
		body := e.userBody(APIBase + path)
		for _, s := range []string{`"cached_on"`, "NTb", `"release_title"`} {
			if strings.Contains(body, s) {
				t.Errorf("%s as user shows %s", path, s)
			}
		}
	}
}

// TestRemovedCopySortsAndFilters: a removed copy's files count in no sort, filter or facet of the list. Alpha shows
// 9 GB (its removed WEB copy holds a newer 20 GB 720p link), Beta 10 GB: the size and added sorts follow what the
// rows show, the library and quality filters agree with the facets and the rows.
func TestRemovedCopySortsAndFilters(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	e.exec(`INSERT INTO title (id, kind, name, sort_name, year, created_at, updated_at) VALUES
		(2, 'movie', 'Beta', 'beta', 2021, 0, 0)`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at) VALUES
		(2, 1, 'movie-web', 0, 'wanted', 0, 0), (3, 2, 'movie-remux', 1, 'filled', 0, 0)`)
	e.exec(`INSERT INTO link (id, path, target, title_id, quality, file_metadata, state, created_at, updated_at) VALUES
		(1, '/web/Alpha.mkv', '/mount/Alpha.mkv', 1, 'WEBDL-720p', '{"Size":20000000000}', 'active', 9000, 0),
		(2, '/remux/Beta.mkv', '/mount/Beta.mkv', 2, 'Remux-1080p', '{"Size":10000000000}', 'active', 5000, 0)`)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 2), (2, 3)`)
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (2, 'web', 0)`)

	list := func(q string) TitlePage {
		var p TitlePage
		e.call("GET", APIBase+"/titles?kind=movie&"+q, nil, 200, &p)
		return p
	}
	if p := list("sort=size&order=desc"); names(p) != "Beta,Alpha" || *p.Items[0].SizeBytes != 10000000000 ||
		*p.Items[1].SizeBytes != 9000000000 {
		t.Errorf("size sort: %s", names(p))
	}
	if p := list("sort=added&order=desc"); names(p) != "Beta,Alpha" {
		t.Errorf("added sort: %s", names(p))
	}
	var f TitleFacets
	e.call("GET", APIBase+"/titles/facets?kind=movie", nil, 200, &f)
	web := 0
	for _, v := range f.Versions {
		if v.Key == "movie-web" {
			web = v.WithFile
		}
	}
	for q, want := range map[string]int{"with=movie-web": web, "without=movie-web": 2, "state=wanted": 0,
		"quality=WEBDL-720p": 0, "quality=720p": 0, "quality=Remux-1080p": 1} {
		if p := list(q); p.Total != want || len(p.Items) != want {
			t.Errorf("%s: total %d, rows %d, want %d", q, p.Total, len(p.Items), want)
		}
	}
	for _, q := range f.Qualities {
		if q.Value == "WEBDL-720p" {
			t.Errorf("quality facet counts a removed copy: %+v", f.Qualities)
		}
	}
	var picks struct {
		Items []TitleRow `json:"items"`
	}
	e.call("GET", APIBase+"/discover/recent", nil, 200, &picks)
	if len(picks.Items) == 0 || picks.Items[0].Name != "Beta" {
		t.Errorf("recent: %+v", picks.Items)
	}
}

// TestCaughtUpSeriesIsAvailable: a series whose aired monitored episodes all have files is available, though its
// copy stays wanted for an episode still to air: in the row, the availability filters, the facets and the title page,
// for managers and for accounts without "see files". With that episode aired and missing it is partial again.
func TestCaughtUpSeriesIsAvailable(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	e.exec(`UPDATE episode_version_state SET state = 'cutoff_met' WHERE episode_id IN (101, 102)`)
	e.exec(`UPDATE episode SET air_date = '2099-01-01', air_date_utc_at = NULL WHERE id = 103`)
	check := func(want, watch string) {
		t.Helper()
		var p TitlePage
		e.call("GET", APIBase+"/titles?kind=series", nil, 200, &p)
		if len(p.Items) != 1 || p.Items[0].Availability != want {
			t.Errorf("row: %+v, want %s", p.Items, want)
		}
		for _, a := range []string{"partial", "available"} {
			p = TitlePage{}
			e.call("GET", APIBase+"/titles?kind=series&availability="+a, nil, 200, &p)
			if (p.Total == 1) != (a == want) {
				t.Errorf("availability=%s: %d titles (want %s)", a, p.Total, want)
			}
		}
		var f TitleFacets
		e.call("GET", APIBase+"/titles/facets?kind=series", nil, 200, &f)
		if f.Availability[want] != 1 {
			t.Errorf("facets %v, want %s", f.Availability, want)
		}
		var d TitleDetail
		e.call("GET", APIBase+"/titles/10", nil, 200, &d)
		if d.Availability != want {
			t.Errorf("title page %q, want %s", d.Availability, want)
		}
		for _, path := range []string{"/titles/10", "/titles?kind=series", "/titles/facets?kind=series"} {
			body := e.userBody(APIBase + path)
			if strings.Contains(path, "facets") {
				if !strings.Contains(body, `"`+watch+`":1`) {
					t.Errorf("user %s: want %s: %s", path, watch, body)
				}
			} else if !strings.Contains(body, `"availability":"`+watch+`"`) {
				t.Errorf("user %s: want %s: %s", path, watch, body)
			}
		}
		if body := e.userBody(APIBase + "/titles?kind=series&availability=" + watch); !strings.Contains(body, `"total":1`) {
			t.Errorf("user filter %s: %s", watch, body)
		}
	}
	check("available", "available")
	e.exec(`UPDATE episode SET air_date = '2020-01-01', air_date_utc_at = 1577836800000 WHERE id = 103`)
	check("partial", "partial")
}

// TestHistoryNotStarted: a download that ended because downloads were off, never tried at a provider, is not_started
// in the title's and the episode's history (Activity's words), not failed; one that was tried and failed stays failed.
func TestHistoryNotStarted(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	const paused = "route ended: router: Theatarr is paused"
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, size_bytes,
		state, error, episodes, created_at, updated_at) VALUES
		(1, 1, 1, 'movie-remux', 'aa', 'Alpha.Untried', 'dmm', 1, 'failed', ?, '[]', 10, 20),
		(2, 1, 1, 'movie-remux', 'bb', 'Alpha.Tried', 'dmm', 1, 'failed', ?, '[]', 30, 40),
		(3, 10, 10, 'tv-2160p', 'cc', 'Show.S01E01.Untried', 'dmm', 1, 'failed', ?, '[101]', 50, 60)`, paused, paused, paused)
	e.exec(`INSERT INTO download_attempt (download_id, provider, outcome, attempted_at) VALUES (2, 'rd', 'error', 35)`)
	ends := func(hs []HistoryEvent) map[string]string {
		out := map[string]string{}
		for _, h := range hs {
			if h.Event != "grabbed" && h.Event != "searched" {
				out[h.ReleaseTitle] = h.Event + ":" + h.Reason
			}
		}
		return out
	}
	var d TitleDetail
	e.call("GET", APIBase+"/titles/1", nil, 200, &d)
	got := ends(d.Versions[0].History)
	if got["Alpha.Untried"] != "not_started:Not started: downloads were off" || !strings.HasPrefix(got["Alpha.Tried"], "failed:") {
		t.Errorf("title history: %v", got)
	}
	var ep EpisodeDetail
	e.call("GET", APIBase+"/titles/10/episodes/101", nil, 200, &ep)
	if got := ends(ep.Versions[0].History); got["Show.S01E01.Untried"] != "not_started:Not started: downloads were off" {
		t.Errorf("episode history: %v", got)
	}
}
