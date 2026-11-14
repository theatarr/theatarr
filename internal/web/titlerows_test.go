package web

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/policy"
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
	// The file's hash comes from the same content as Cached on (the admin's Torrent link needs it).
	if c := d.Versions[0].Current; c == nil || c.InfoHash != hash {
		t.Errorf("title page: current %+v, want info_hash %s", c, hash)
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
	if body := e.userBody(APIBase + "/titles/1"); strings.Contains(body, `"cached_on"`) || strings.Contains(body, `"providers"`) ||
		strings.Contains(body, hash) {
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

	// Adopted: a score its adoption calculated but no formats (adoption stores none): the score is evidence, and a
	// calculated 0 stays 0.
	// Score evidence: a non-zero stored score, or stored formats (an empty list too, for a calculated 0).
	for meta, score := range map[string]int{`{"Score":900,"Size":1000}`: 900, `{"Score":0,"Formats":[],"Size":1000}`: 0} {
		e.exec(`UPDATE link SET download_id = NULL, file_metadata = ?, score = ? WHERE id = 1`, meta, score)
		d = TitleDetail{}
		e.call("GET", APIBase+"/titles/1", nil, 200, &d)
		if c := d.Versions[0].Current; c == nil || c.Score == nil || *c.Score != int64(score) || len(c.Formats) != 0 {
			t.Errorf("adopted current, score %d: %+v", score, c)
		}
		if u := d.Versions[0].Upgrade; u == nil || u.Score == nil || *u.Score != int64(score) {
			t.Errorf("adopted upgrade, score %d: want the score: %+v", score, u)
		}
	}
	// A link that stored neither formats nor a non-zero score shows none, though the column holds 0: a 0 backfilled
	// without formats (migration 00038) is no evidence.
	for _, meta := range []string{`{"Size":1000}`, `{"Score":0,"Size":1000}`} {
		e.exec(`UPDATE link SET file_metadata = ?, score = 0 WHERE id = 1`, meta)
		d = TitleDetail{}
		e.call("GET", APIBase+"/titles/1", nil, 200, &d)
		if c := d.Versions[0].Current; c == nil || c.Score != nil {
			t.Errorf("unscored current %s: %+v", meta, c)
		}
	}
	d = TitleDetail{}
	e.call("GET", APIBase+"/titles/1", nil, 200, &d)
	if c := d.Versions[0].Current; c == nil || c.Score != nil {
		t.Errorf("unscored current: %+v", c)
	}
	if u := d.Versions[0].Upgrade; u == nil || u.Score != nil {
		t.Errorf("unscored upgrade: want no score: %+v", u)
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
	if d.Availability != "available" || d.SizeBytes == nil || *d.SizeBytes != 9000000000 || d.AddedAt == nil ||
		d.AddedAt.UnixMilli() == 99999999999999 {
		t.Errorf("title rollups: availability %q size %v added %v", d.Availability, d.SizeBytes, d.AddedAt)
	}
	if body := e.userBody(APIBase + "/titles/1"); !strings.Contains(body, `"removed":true`) {
		t.Errorf("user title page: want removed: %s", body)
	}

	var p TitlePage
	e.call("GET", APIBase+"/titles?kind=movie&q=Alpha", nil, 200, &p)
	if len(p.Items) != 1 || len(p.Items[0].Versions) != 1 || p.Items[0].Versions[0].Key != "movie-remux" ||
		p.Items[0].Availability != "available" || *p.Items[0].SizeBytes != 9000000000 {
		t.Errorf("list row: %+v", p.Items)
	}
	for q, want := range map[string]int{"availability=available": 1, "availability=partial": 0, "provider=rd": 0} {
		p = TitlePage{}
		e.call("GET", APIBase+"/titles?kind=movie&"+q, nil, 200, &p)
		if p.Total != want {
			t.Errorf("%s: %d titles, want %d", q, p.Total, want)
		}
	}
	var f TitleFacets
	e.call("GET", APIBase+"/titles/facets?kind=movie", nil, 200, &f)
	if len(f.Providers) != 0 || f.Availability["available"] != 1 || f.Availability["partial"] != 0 {
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

// TestLibraryLabelsWithoutKeys: two libraries with the same label are told apart by number for every caller (no
// library key in the labels).
func TestLibraryLabelsWithoutKeys(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, func(p *policy.Policy) {
		v := *p.Versions["tv-2160p"]
		p.Versions["tv-2160p-theatarr"] = &v
	})
	labels := func(o AddOptions) map[string]string {
		out := map[string]string{}
		for _, v := range o.Versions {
			out[v.Key] = v.Label
		}
		return out
	}
	var o AddOptions
	e.call("GET", APIBase+"/add/options?kind=series&title_id=10", nil, 200, &o)
	if got := labels(o); got["tv-2160p"] != "4K" || got["tv-2160p-theatarr"] != "4K (2)" {
		t.Errorf("admin labels: %v", got)
	}
	o = AddOptions{}
	if err := json.Unmarshal([]byte(e.userBody(APIBase+"/add/options?kind=series&title_id=10")), &o); err != nil {
		t.Fatal(err)
	}
	if got := labels(o); got["tv-2160p"] != "4K" || got["tv-2160p-theatarr"] != "4K (2)" {
		t.Errorf("user labels: %v", got)
	}
	// A request for the second library alone names it as the options do.
	e.exec(`INSERT INTO request (user_id, kind, tmdb_id, title_id, name, versions, status, created_at, updated_at)
		VALUES (1, 'series', 201, 10, 'The Show', '["tv-2160p-theatarr"]', 'approved', 0, 0)`)
	body := e.userBody(APIBase + "/requests")
	if !strings.Contains(body, `"label":"4K (2)"`) || strings.Contains(body, "· tv-2160p") {
		t.Errorf("user request labels: %s", body)
	}
}

// TestCaughtUpSeriesOneAnswerWithoutSeeFiles: for an account without "see files", a series holding every aired
// episode reads the same everywhere the server answers: the title's availability, its library's episode counts (what
// the hero and the request box read: have > 0, missing 0) and the episode cells (aired ones held; the one to come is
// not aired and not missing).
func TestCaughtUpSeriesOneAnswerWithoutSeeFiles(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	e.exec(`UPDATE episode_version_state SET state = 'cutoff_met' WHERE episode_id IN (101, 102)`)
	e.exec(`UPDATE episode SET air_date = '2099-01-01', air_date_utc_at = NULL WHERE id = 103`)
	var d TitleDetail
	if err := json.Unmarshal([]byte(e.userBody(APIBase+"/titles/10")), &d); err != nil {
		t.Fatal(err)
	}
	if d.Availability != "available" || len(d.Versions) != 1 {
		t.Fatalf("title: %q %+v", d.Availability, d.Versions)
	}
	if c := d.Versions[0].Episodes; c == nil || c.Have != 2 || c.Missing != 0 || !d.Versions[0].HasFile {
		t.Errorf("episode counts: %+v", c)
	}
	var el EpisodeList
	if err := json.Unmarshal([]byte(e.userBody(APIBase+"/titles/10/episodes")), &el); err != nil {
		t.Fatal(err)
	}
	for _, ep := range el.Episodes {
		c := ep.Versions["tv-2160p"]
		switch {
		case ep.Season == 0:
		case ep.Aired && !c.HasFile, !ep.Aired && c.HasFile, ep.ID == 103 && ep.Aired:
			t.Errorf("episode %d: aired %v cell %+v", ep.ID, ep.Aired, c)
		}
	}
	var p TitlePage
	if err := json.Unmarshal([]byte(e.userBody(APIBase+"/titles?kind=series")), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 || p.Items[0].Availability != "available" {
		t.Errorf("row: %+v", p.Items)
	}
	if c := p.Items[0].Versions[0].Coverage; c == nil || c.Missing != 0 || c.Have != 2 {
		t.Errorf("row coverage: %+v", c)
	}
}

// TestStaleCompleteSeriesIsPartial: a series copy stored as cutoff_met whose next episode has aired and is missing
// (its state not yet updated, as while paused) is partial in the row, the filters, the facets and the title page.
func TestStaleCompleteSeriesIsPartial(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	e.exec(`UPDATE version SET state = 'cutoff_met' WHERE id = 10`)
	e.exec(`UPDATE episode_version_state SET state = 'cutoff_met' WHERE episode_id IN (101, 102)`)
	var p TitlePage
	e.call("GET", APIBase+"/titles?kind=series", nil, 200, &p)
	if len(p.Items) != 1 || p.Items[0].Availability != "partial" {
		t.Errorf("row: %+v", p.Items)
	}
	for q, want := range map[string]int{"availability=available": 0, "availability=partial": 1} {
		p = TitlePage{}
		e.call("GET", APIBase+"/titles?kind=series&"+q, nil, 200, &p)
		if p.Total != want {
			t.Errorf("%s: %d, want %d", q, p.Total, want)
		}
	}
	var f TitleFacets
	e.call("GET", APIBase+"/titles/facets?kind=series", nil, 200, &f)
	if f.Availability["partial"] != 1 || f.Availability["available"] != 0 {
		t.Errorf("facets: %v", f.Availability)
	}
	var d TitleDetail
	e.call("GET", APIBase+"/titles/10", nil, 200, &d)
	if d.Availability != "partial" {
		t.Errorf("title page: %q", d.Availability)
	}
	if body := e.userBody(APIBase + "/titles?kind=series&availability=partial"); !strings.Contains(body, `"total":1`) {
		t.Errorf("user partial filter: %s", body)
	}
}

// TestMoviePartlyAvailable: a movie held in one library and missing in a second, monitored library is partial in the
// row, the filters, the facets and the title page. Unmonitored, or removed, the empty library does not count and the
// held file decides: available (whatever its quality). A caller without "see files" reads available throughout.
func TestMoviePartlyAvailable(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at) VALUES
		(2, 1, 'movie-web', 1, 'wanted', 0, 0)`)
	check := func(name, want string) {
		t.Helper()
		var p TitlePage
		e.call("GET", APIBase+"/titles?kind=movie&q=Alpha", nil, 200, &p)
		if len(p.Items) != 1 || p.Items[0].Availability != want {
			t.Errorf("%s: row %+v, want %s", name, p.Items, want)
		}
		for _, a := range []string{"partial", "available"} {
			p = TitlePage{}
			e.call("GET", APIBase+"/titles?kind=movie&q=Alpha&availability="+a, nil, 200, &p)
			if (p.Total == 1) != (a == want) {
				t.Errorf("%s: availability=%s lists %d", name, a, p.Total)
			}
		}
		var f TitleFacets
		e.call("GET", APIBase+"/titles/facets?kind=movie&q=Alpha", nil, 200, &f)
		if f.Availability[want] != 1 || f.Availability["partial"]+f.Availability["available"] != 1 {
			t.Errorf("%s: facets %v", name, f.Availability)
		}
		var d TitleDetail
		e.call("GET", APIBase+"/titles/1", nil, 200, &d)
		if d.Availability != want {
			t.Errorf("%s: title page %q", name, d.Availability)
		}
		if body := e.userBody(APIBase + "/titles?kind=movie&q=Alpha"); !strings.Contains(body, `"availability":"available"`) {
			t.Errorf("%s: user row %s", name, body)
		}
		if body := e.userBody(APIBase + "/titles?kind=movie&q=Alpha&availability=available"); !strings.Contains(body, `"total":1`) {
			t.Errorf("%s: user available filter %s", name, body)
		}
		if body := e.userBody(APIBase + "/titles/1"); !strings.Contains(body, `"availability":"available"`) {
			t.Errorf("%s: user title page %s", name, body)
		}
	}
	check("monitored", "partial")
	e.exec(`UPDATE version SET monitored = 0 WHERE id = 2`)
	check("unmonitored", "available")
	e.exec(`UPDATE version SET monitored = 1 WHERE id = 2`)
	e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (2, 'web', 0)`)
	check("removed", "available")
}

// Titles with the same sort name order by their full name: "Alien" before "Alien³" whatever their ids.
func TestNameSortTieBreak(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	e.exec(`INSERT INTO title (id, kind, name, sort_name, year, created_at, updated_at) VALUES
		(5, 'movie', 'Alien³', 'alien', 1992, 0, 0), (6, 'movie', 'Alien', 'alien', 1979, 0, 0)`)
	var p TitlePage
	e.call("GET", APIBase+"/titles?kind=movie&sort=name&order=asc", nil, 200, &p)
	if got := names(p); !strings.Contains(got, "Alien,Alien³") {
		t.Errorf("name order: %s", got)
	}
}

// TestTitleOnDiskCountsSharedFileOnce: two libraries linking the same file (one target) add it to On disk once, and
// each link carries its size for the admin's Files table.
func TestTitleOnDiskCountsSharedFileOnce(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	e.exec(`DELETE FROM version_file WHERE title_id = 1`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (98, 1, 'movie-web', 'filled', 0, 0)`)
	e.exec(`INSERT INTO link (id, path, target, title_id, quality, file_metadata, state, created_at, updated_at) VALUES
		(1, '/remux/Alpha.mkv', '/mount/Alpha.mkv', 1, 'Remux-2160p', '{"Size":1000}', 'active', 0, 0),
		(2, '/web/Alpha.mkv', '/mount/Alpha.mkv', 1, 'Remux-2160p', '{"Size":1000}', 'active', 0, 0)`)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 1), (2, 98)`)

	var d TitleDetail
	e.call("GET", APIBase+"/titles/1", nil, 200, &d)
	if d.SizeBytes == nil || *d.SizeBytes != 1000 {
		t.Errorf("on disk %v, want 1000", d.SizeBytes)
	}
	for _, v := range d.Versions {
		for _, l := range v.Links {
			if l.SizeBytes == nil || *l.SizeBytes != 1000 {
				t.Errorf("%s link %d size %v, want 1000", v.Key, l.ID, l.SizeBytes)
			}
		}
	}
}
