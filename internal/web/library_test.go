package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// seedLibrary adds, on top of seed: TMDb metadata for Alpha, Beta and Gamma; file times; a provider for Alpha's
// remux; an import and an upgrade in Alpha's history; release dates; and Delta (4), an anime series with episodes.
func (e *env) seedLibrary() {
	now := store.Millis(testNow)
	day := int64(24 * time.Hour / time.Millisecond)
	e.exec(`INSERT INTO title_meta (title_id, genres, rating, rating_votes, certification, release_date, refreshed_at,
		attempted_at, refresh_after) VALUES (1, '["Drama","Action"]', 7.5, 100, 'R', '2020-05-01', ?, ?, ?),
		(2, '["Comedy"]', 6.0, 50, 'PG', '2021-03-01', ?, ?, ?), (3, '["Drama"]', NULL, 0, '', '', ?, ?, ?)`,
		now, now, now, now, now, now, now, now, now)
	e.exec(`UPDATE link SET created_at = ? WHERE id = 901`, now-10*day)
	e.exec(`UPDATE link SET created_at = ? WHERE id = 902`, now-1*day)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality, state,
		episodes, created_at, updated_at, added_at, imported_at) VALUES
		(80, 1, 1, 'movie-remux', ?, 'Alpha.Old', 'dmm', 'Remux-2160p', 'imported', '[]', ?, ?, ?, ?),
		(81, 1, 1, 'movie-remux', ?, 'Alpha.2020.2160p.BluRay.REMUX-GRP', 'dmm', 'Remux-2160p', 'imported', '[]', ?, ?, ?, ?)`,
		strings.Repeat("8", 40), now-1000, now-900, now-1000, now-900, hashA, now-600, now-500, now-600, now-500)
	e.exec(`INSERT INTO provider_torrent (provider, torrent_id, info_hash, status, state, progress)
		SELECT 'rd', 'T1', info_hash, 'downloaded', 'downloaded', 100 FROM content WHERE id = 1`)
	e.exec(`INSERT INTO title_release_date (title_id, source, country, type, date) VALUES
		(1, 'tmdb', 'US', 'digital', '2020-08-01'), (1, 'tmdb', 'US', 'digital', '2026-11-01'),
		(2, 'tmdb', 'US', 'digital', '2026-11-05')`)
	e.exec(`UPDATE title SET monitored = 0 WHERE id = 3`)

	e.exec(`INSERT INTO title (id, kind, name, year, tvdb_id, anime, created_at, updated_at) VALUES
		(4, 'series', 'Delta', 2022, 404, 1, ?, ?)`, now, now)
	e.exec(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (4, 4, 'anime-tv', 'filled', ?, ?)`,
		now, now)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, air_date, created_at, updated_at) VALUES
		(41, 4, 1, 1, 'One', '2026-01-01', 0, 0), (42, 4, 1, 2, 'Two', '2026-01-08', 0, 0),
		(43, 4, 1, 3, 'Three', '2026-01-15', 0, 0), (40, 4, 0, 1, 'Special', '2026-02-01', 0, 0),
		(44, 4, 2, 1, 'Return', '2026-10-31', 0, 0)`)
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, state, updated_at) VALUES
		(4, 41, 'cutoff_met', 0), (4, 42, 'filled', 0), (4, 43, 'wanted', 0), (4, 40, 'wanted', 0), (4, 44, 'wanted', 0)`)
	e.exec(`INSERT INTO link (id, path, target, title_id, quality, file_metadata, adopted, created_at, updated_at) VALUES
		(90, '/anime/Delta/S01E01.mkv', '/zurg/Delta/S01E01.mkv', 4, 'Bluray-1080p',
		 '{"ReleaseGroup":"SubsPlease","Size":1000000000}', 1, ?, ?),
		(91, '/anime/Delta/S01E02.mkv', '/zurg/Delta/S01E02.mkv', 4, 'Bluray-1080p',
		 '{"ReleaseGroup":"SubsPlease","Size":1000000000}', 1, ?, ?)`, now-5*day, now-5*day, now-6*day, now-6*day)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (90, 4), (91, 4)`)
	e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (90, 41), (91, 42)`)
	airAtMidnight(e.exec)
}

func names(p TitlePage) string {
	var s []string
	for _, it := range p.Items {
		s = append(s, it.Name)
	}
	return strings.Join(s, ",")
}

func TestTitlesSortAndFilters(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedLibrary()
	key := e.apiKey()
	// For the created, runtime and network sorts: Gamma's empty runtime and Alpha's empty network count as none.
	now := store.Millis(testNow)
	day := int64(24 * time.Hour / time.Millisecond)
	e.exec(`UPDATE title SET created_at = CASE id WHEN 1 THEN ? WHEN 2 THEN ? WHEN 3 THEN ? ELSE created_at END`,
		now-3*day, now-day, now-2*day)
	e.exec(`UPDATE title_meta SET runtime_minutes = CASE title_id WHEN 1 THEN 120 WHEN 2 THEN 95 ELSE 0 END,
		network = CASE title_id WHEN 2 THEN 'apple' WHEN 3 THEN 'Zed' ELSE '' END`)
	for q, want := range map[string]string{
		"sort=created":                  "Delta,Beta,Gamma,Alpha",
		"sort=created&order=asc":        "Alpha,Gamma,Beta,Delta",
		"sort=runtime":                  "Alpha,Beta,Delta,Gamma",
		"sort=runtime&order=asc":        "Beta,Alpha,Delta,Gamma",
		"sort=certification":            "Beta,Alpha,Delta,Gamma",
		"sort=certification&order=desc": "Alpha,Beta,Delta,Gamma",
		"sort=network":                  "Beta,Gamma,Alpha,Delta",
		"sort=network&order=desc":       "Gamma,Beta,Alpha,Delta",
		"":                              "Alpha,Beta,Delta,Gamma",
		"sort=name&order=desc":          "Gamma,Delta,Beta,Alpha",
		"sort=added":                    "Beta,Delta,Alpha,Gamma",
		"sort=added&order=asc":          "Alpha,Delta,Beta,Gamma",
		"sort=rating":                   "Alpha,Beta,Delta,Gamma",
		"sort=rating&order=asc":         "Beta,Alpha,Delta,Gamma",
		"sort=year":                     "Delta,Beta,Alpha,Gamma",
		"sort=size":                     "Alpha,Beta,Delta,Gamma",
		"sort=release":                  "Beta,Alpha,Delta,Gamma", // undated titles last, as their cell shows "—"
		"kind=movie&sort=added":         "Beta,Alpha",
		"anime=true":                    "Delta",
		"anime=false":                   "Alpha,Beta,Gamma",
		"kind=series&anime=false":       "Gamma",
		"genre=Drama":                   "Alpha,Gamma",
		"genre=Comedy,Action":           "Alpha,Beta",
		"genre=Comedy&genre=Action":     "Alpha,Beta",
		"quality=WEBDL-2160p":           "Beta",
		"quality=Bluray-1080p":          "Delta",
		"provider=rd":                   "Alpha",
		"year_from=2021":                "Beta,Delta",
		"year_to=2020":                  "Alpha,Gamma",
		"year_from=2020&year_to=2021":   "Alpha,Beta",
		"monitored=false":               "Gamma",
		"q=ha++alp":                     "Alpha", // every word, in any order
		"q=alp+zzz":                     "",
		// Delta's copy is stored filled, but its aired S01E03 is missing: partial (availStateSQL).
		"availability=partial":         "Alpha,Delta",
		"availability=available":       "Beta",
		"availability=none":            "Gamma",
		"availability=missing":         "",
		"availability=upcoming":        "",
		"availability=unmonitored":     "",
		"sort=added&limit=2&offset=1":  "Delta,Alpha",
		"without=movie-web&sort=added": "Alpha", // the series (Delta, Gamma) never lack a movie library
	} {
		var p TitlePage
		if code := e.getKey(APIBase+"/titles?"+q, key, &p); code != 200 {
			t.Errorf("%s: %d", q, code)
			continue
		}
		if got := names(p); got != want {
			t.Errorf("%s: %q, want %q", q, got, want)
		}
		// The total counts the same filter.
		if n := len(strings.Split(want, ",")); want != "" && !strings.Contains(q, "limit") && p.Total != n {
			t.Errorf("%s: total %d, want %d", q, p.Total, n)
		}
	}
	for _, bad := range []string{"sort=x", "order=up", "availability=x", "availability=upgrade", "anime=maybe",
		"year_from=-1"} {
		if code := e.getKey(APIBase+"/titles?"+bad, key, nil); code != 400 {
			t.Errorf("%s: %d, want 400", bad, code)
		}
	}

	// A row's facts and current files.
	var p TitlePage
	e.getKey(APIBase+"/titles?q=Alpha", key, &p)
	a := p.Items[0]
	if a.Availability != "partial" || a.Rating == nil || *a.Rating != 7.5 || a.Certification != "R" ||
		strings.Join(a.Genres, ",") != "Drama,Action" || a.AddedAt == nil || a.SizeBytes == nil || *a.SizeBytes != 50000000000 {
		t.Errorf("Alpha row: %+v", a.TitleFacts)
	}
	r := a.Versions[0]
	if r.Label != "4K Remux" || r.Quality != "Remux-2160p" || r.Files != 1 || r.SizeBytes == nil ||
		strings.Join(r.Providers, ",") != "rd" || a.Versions[1].Label != "4K WEB" || a.Versions[1].Files != 0 {
		t.Errorf("Alpha versions: %+v", a.Versions)
	}
	e.getKey(APIBase+"/titles?q=Delta", key, &p)
	if v := p.Items[0].Versions[0]; v.Label != "Anime series" || v.Files != 2 || v.Quality != "Bluray-1080p" {
		t.Errorf("Delta version: %+v", v)
	}
}

// Series status: the title metadata's (TMDb) status, else the series metadata's (TVDB); series only.
func TestSeriesStatus(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedLibrary()
	key := e.apiKey()
	e.exec(`UPDATE title_meta SET status = CASE title_id WHEN 3 THEN 'Returning Series' WHEN 1 THEN 'Released' ELSE status END`)
	e.exec(`INSERT INTO series_meta (title_id, status) VALUES (4, 'Ended')`)
	for q, want := range map[string]string{
		"series_status=continuing":            "Gamma",
		"series_status=ended":                 "Delta",
		"kind=series&series_status=ended":     "Delta",
		"anime=true&series_status=continuing": "",
	} {
		var p TitlePage
		if code := e.getKey(APIBase+"/titles?"+q, key, &p); code != 200 {
			t.Errorf("%s: %d", q, code)
			continue
		}
		if got := names(p); got != want {
			t.Errorf("%s: %q, want %q", q, got, want)
		}
	}
	// The row shows the status the filter reads: Delta has no TMDb status, so its series metadata's.
	var row TitlePage
	if e.getKey(APIBase+"/titles?q=Delta", key, &row); len(row.Items) != 1 || row.Items[0].Status != "Ended" {
		t.Errorf("Delta's row status: %+v", row.Items)
	}
	// A TMDb Canceled counts as Ended, and wins over the series metadata's status.
	e.exec(`INSERT INTO title_meta (title_id, status, refreshed_at, attempted_at, refresh_after) VALUES (4, 'Canceled', 0, 0, 0)`)
	e.exec(`UPDATE series_meta SET status = 'Continuing' WHERE title_id = 4`)
	var p TitlePage
	if e.getKey(APIBase+"/titles?series_status=ended", key, &p); names(p) != "Delta" {
		t.Errorf("Canceled: %q", names(p))
	}
	for _, bad := range []string{"series_status=x", "kind=movie&series_status=ended"} {
		if code := e.getKey(APIBase+"/titles?"+bad, key, nil); code != 400 {
			t.Errorf("%s: %d, want 400", bad, code)
		}
	}
	var f TitleFacets
	if e.getKey(APIBase+"/titles/facets?kind=movie", key, &f); f.SeriesStatus != nil {
		t.Errorf("movie facets carry a series status: %v", f.SeriesStatus)
	}
	for scope, want := range map[string]map[string]int{
		"":                        {"continuing": 1, "upcoming": 0, "ended": 1},
		"kind=series&anime=false": {"continuing": 1, "upcoming": 0, "ended": 0},
		"anime=true":              {"continuing": 0, "upcoming": 0, "ended": 1},
	} {
		f = TitleFacets{}
		if e.getKey(APIBase+"/titles/facets?"+scope, key, &f); !maps.Equal(f.SeriesStatus, want) {
			t.Errorf("%q: series status %v, want %v", scope, f.SeriesStatus, want)
		}
	}
	// TMDb's In Production, Planned and Pilot are Upcoming, as the Series status column calls them, in the filter and
	// its counts.
	for _, st := range []string{"In Production", "Planned", "Pilot"} {
		e.exec(`UPDATE title_meta SET status = ? WHERE title_id = 4`, st)
		p = TitlePage{}
		if e.getKey(APIBase+"/titles?series_status=upcoming", key, &p); names(p) != "Delta" {
			t.Errorf("%s: %q", st, names(p))
		}
		f = TitleFacets{}
		if e.getKey(APIBase+"/titles/facets", key, &f); !maps.Equal(f.SeriesStatus,
			map[string]int{"continuing": 1, "upcoming": 1, "ended": 0}) {
			t.Errorf("%s: series status %v", st, f.SeriesStatus)
		}
	}
}

// Tags: the labels on the title's copies in the library, shown, counted and filtered for callers who see files only.
func TestTitleTags(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedLibrary()
	key := e.apiKey()
	e.exec(`INSERT INTO arrview_tag (id, label, created_at) VALUES (1, 'kids', 0), (2, 'debrid-upgrade', 0), (3, 'unused', 0)`)
	// Alpha: one label per copy; Beta: kids; Gamma: a label on a library it has no copy in, which counts nowhere.
	e.exec(`INSERT INTO arrview_title_tag (version, title_id, tag_id) VALUES ('movie-remux', 1, 1), ('movie-web', 1, 2),
		('movie-remux', 1, 2), ('movie-web', 2, 1), ('tv-2160p', 3, 2)`)
	var p TitlePage
	if e.getKey(APIBase+"/titles?q=Alpha", key, &p); strings.Join(p.Items[0].Tags, ",") != "debrid-upgrade,kids" {
		t.Errorf("Alpha's tags: %v", p.Items[0].Tags)
	}
	for q, want := range map[string]string{
		"tag=kids":                       "Alpha,Beta",
		"tag=KIDS":                       "Alpha,Beta",
		"tag=debrid-upgrade":             "Alpha",
		"tag=unused":                     "",
		"tag=unused,kids":                "Alpha,Beta",
		"kind=series&tag=debrid-upgrade": "",
	} {
		if code := e.getKey(APIBase+"/titles?"+q, key, &p); code != 200 || names(p) != want {
			t.Errorf("%s: %d %q, want %q", q, code, names(p), want)
		}
	}
	var d TitleDetail
	if e.getKey(APIBase+"/titles/1", key, &d); strings.Join(d.Tags, ",") != "debrid-upgrade,kids" {
		t.Errorf("Alpha's page tags: %v", d.Tags)
	}
	var f TitleFacets
	e.getKey(APIBase+"/titles/facets?kind=movie", key, &f)
	if got := fmt.Sprint(f.Tags); got != "[{kids 2} {debrid-upgrade 1}]" {
		t.Errorf("movie tag facet: %s", got)
	}
	f = TitleFacets{}
	if e.getKey(APIBase+"/titles/facets?kind=series", key, &f); len(f.Tags) != 0 {
		t.Errorf("series tag facet: %v", f.Tags)
	}

	// A household account: no tags anywhere, and the filter is refused.
	uid := e.account("household", auth.RoleUser, auth.StatusActive, true)
	ukey, _, err := e.auth.CreateKey(context.Background(), "k-household", uid)
	if err != nil {
		t.Fatal(err)
	}
	if code := e.getKey(APIBase+"/titles?tag=kids", ukey, nil); code != http.StatusForbidden {
		t.Errorf("tag filter as user: %d, want 403", code)
	}
	for _, path := range []string{"/titles", "/titles/1", "/titles/facets?kind=movie"} {
		var raw json.RawMessage
		if code := e.getKey(APIBase+path, ukey, &raw); code != 200 || strings.Contains(string(raw), `"tags"`) || strings.Contains(string(raw), "kids") {
			t.Errorf("%s as user: %d %s", path, code, raw)
		}
	}
}

func TestVersionLabels(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		key, profile string
		kind         policy.Kind
		want         string
	}{
		{"movie-remux", "remux-2160p", policy.KindMovie, "4K Remux"},
		{"movie-web", "web-2160p", policy.KindMovie, "4K WEB"},
		{"tv-2160p", "tv-2160p", policy.KindSeries, "4K"},
		{"anime-tv", "anime-tv", policy.KindSeries, "Anime series"},
		{"anime-movie", "anime-remux-1080p-movie", policy.KindMovie, "Anime movies"},
		// A key no longer in the settings: the kind comes from the key.
		{"anime-tv-mm", "", "", "Anime series"},
		{"anime-movie-mm", "", "", "Anime movies"},
		{"custom", "", "", "custom"},
	} {
		if got := labelFor(c.key, c.profile, c.kind); got != c.want {
			t.Errorf("%s/%s: %q, want %q", c.key, c.profile, got, c.want)
		}
	}
}

func TestTitleFacets(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedLibrary()
	key := e.apiKey()
	var f TitleFacets
	if code := e.getKey(APIBase+"/titles/facets?kind=movie", key, &f); code != 200 {
		t.Fatalf("facets: %d", code)
	}
	if f.Total != 2 || f.Monitored != 2 || f.Availability["partial"] != 1 || f.Availability["available"] != 1 ||
		f.Availability["none"] != 0 || *f.YearMin != 2020 || *f.YearMax != 2021 {
		t.Errorf("movie facets: %+v", f)
	}
	vs := map[string]VersionFacet{}
	for _, v := range f.Versions {
		vs[v.Key] = v
	}
	if v := vs["movie-web"]; v.Titles != 2 || v.WithFile != 1 || v.Wanted != 1 || v.Filled != 1 || v.Label != "4K WEB" {
		t.Errorf("movie-web facet: %+v", v)
	}
	if v := vs["movie-remux"]; v.Titles != 1 || v.WithFile != 1 || v.CutoffMet != 1 {
		t.Errorf("movie-remux facet: %+v", v)
	}
	// Without see files a library facet keeps its titles, not its file counts.
	uid := e.account("household", auth.RoleUser, auth.StatusActive, true)
	ukey, _, err := e.auth.CreateKey(context.Background(), "k-household", uid)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Versions []map[string]any `json:"versions"`
	}
	if code := e.getKey(APIBase+"/titles/facets?kind=movie", ukey, &raw); code != 200 || len(raw.Versions) != 2 {
		t.Fatalf("facets as user: %d %+v", code, raw)
	}
	for _, v := range raw.Versions {
		for _, k := range []string{"with_file", "wanted", "filled", "cutoff_met"} {
			if _, ok := v[k]; ok {
				t.Errorf("facet %v as user carries %s", v["key"], k)
			}
		}
		if v["titles"] == nil {
			t.Errorf("facet %v as user lost its titles", v["key"])
		}
	}
	counts := func(cs []FacetCount) string {
		var s []string
		for _, c := range cs {
			s = append(s, c.Value+"="+itoa(int64(c.Count)))
		}
		return strings.Join(s, ",")
	}
	if got := counts(f.Qualities); got != "Remux-2160p=1,WEBDL-2160p=1" {
		t.Errorf("qualities: %s", got)
	}
	if got := counts(f.Genres); got != "Action=1,Comedy=1,Drama=1" {
		t.Errorf("genres: %s", got)
	}
	if got := counts(f.Providers); got != "rd=1" {
		t.Errorf("providers: %s", got)
	}
	if got := counts(f.Certifications); got != "PG=1,R=1" {
		t.Errorf("certifications: %s", got)
	}
	// Every grid's facet counts agree with the titles they would filter.
	for _, scope := range []string{"", "kind=movie", "kind=series&anime=false", "anime=true"} {
		e.getKey(APIBase+"/titles/facets?"+scope, key, &f)
		sum := 0
		for av, n := range f.Availability {
			var p TitlePage
			e.getKey(APIBase+"/titles?availability="+av+"&"+scope, key, &p)
			if p.Total != n {
				t.Errorf("%s availability=%s: facet %d, titles %d", scope, av, n, p.Total)
			}
			sum += n
		}
		// Every title has one status word.
		if sum != f.Total {
			t.Errorf("%s: availability facets add up to %d of %d titles", scope, sum, f.Total)
		}
		for up, n := range f.Upgrades {
			var p TitlePage
			e.getKey(APIBase+"/titles?upgrade="+up+"&"+scope, key, &p)
			if p.Total != n {
				t.Errorf("%s upgrade=%s: facet %d, titles %d", scope, up, n, p.Total)
			}
		}
		for _, g := range f.Genres {
			var p TitlePage
			e.getKey(APIBase+"/titles?genre="+g.Value+"&"+scope, key, &p)
			if p.Total != g.Count {
				t.Errorf("%s genre=%s: facet %d, titles %d", scope, g.Value, g.Count, p.Total)
			}
		}
		for _, q := range f.Qualities {
			var p TitlePage
			e.getKey(APIBase+"/titles?quality="+q.Value+"&"+scope, key, &p)
			if p.Total != q.Count {
				t.Errorf("%s quality=%s: facet %d, titles %d", scope, q.Value, q.Count, p.Total)
			}
		}
	}
	if code := e.getKey(APIBase+"/titles/facets?kind=x", key, nil); code != 400 {
		t.Errorf("bad kind: %d", code)
	}
}

func TestTitleEpisodes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedLibrary()
	key := e.apiKey()
	var l EpisodeList
	if code := e.getKey(APIBase+"/titles/4/episodes", key, &l); code != 200 {
		t.Fatalf("episodes: %d", code)
	}
	if len(l.Versions) != 1 || l.Versions[0].Key != "anime-tv" || len(l.Episodes) != 5 {
		t.Fatalf("episodes: %+v", l)
	}
	// Every episode has a cell for the version, with its state and file.
	want := map[string]string{"S01E01": "cutoff_met/Bluray-1080p", "S01E02": "filled/Bluray-1080p", "S01E03": "wanted/",
		"S00E01": "wanted/", "S02E01": "wanted/"}
	for _, ep := range l.Episodes {
		c, ok := ep.Versions["anime-tv"]
		code := episodeCode(ep.Season, ep.Number)
		if !ok || c.State+"/"+c.Quality != want[code] || c.HasFile != (c.Quality != "") {
			t.Errorf("%s: %+v", code, c)
		}
		if code == "S01E01" && c.Path != "/anime/Delta/S01E01.mkv" {
			t.Errorf("S01E01 path: %q", c.Path)
		}
	}
	var seasons []string
	for _, s := range l.Seasons {
		c := s.Versions["anime-tv"]
		seasons = append(seasons, itoa(s.Season)+":"+itoa(int64(s.Episodes))+"/"+itoa(int64(s.Aired))+"/"+
			itoa(int64(c.Have))+"/"+itoa(int64(c.Missing)))
	}
	if got := strings.Join(seasons, " "); got != "1:3/3/2/1 2:1/0/0/0 0:1/1/0/1" {
		t.Errorf("seasons: %s", got)
	}
	if e.getKey(APIBase+"/titles/4/episodes?season=1", key, &l); len(l.Episodes) != 3 || len(l.Seasons) != 3 {
		t.Errorf("season 1: %d episodes, %d seasons", len(l.Episodes), len(l.Seasons))
	}
	if code := e.getKey(APIBase+"/titles/1/episodes", key, &l); code != 200 || len(l.Episodes) != 0 {
		t.Errorf("movie: %d %+v", code, l)
	}
	if code := e.getKey(APIBase+"/titles/99/episodes", key, nil); code != 404 {
		t.Errorf("missing title: %d", code)
	}
	if code := e.getKey(APIBase+"/titles/4/episodes?season=x", key, nil); code != 400 {
		t.Errorf("bad season: %d", code)
	}
	// Absolute numbers: the refresh writes absolute_tvdb (S01E01 has only that); the legacy absolute scheme is the
	// fallback (S01E02), and absolute_tvdb wins where both exist (S01E03).
	e.exec(`INSERT INTO episode_number (episode_id, title_id, scheme, season, number, source) VALUES
		(41, 4, 'absolute_tvdb', NULL, 101, 'tvdb'), (42, 4, 'absolute', NULL, 102, 'tvdb'),
		(43, 4, 'absolute_tvdb', NULL, 103, 'tvdb'), (43, 4, 'absolute', NULL, 999, 'tvdb')`)
	e.getKey(APIBase+"/titles/4/episodes?season=1", key, &l)
	var abs []string
	for _, ep := range l.Episodes {
		a := "-"
		if ep.Absolute != nil {
			a = itoa(*ep.Absolute)
		}
		abs = append(abs, episodeCode(ep.Season, ep.Number)+"="+a)
	}
	if got := strings.Join(abs, " "); got != "S01E01=101 S01E02=102 S01E03=103" {
		t.Errorf("absolute numbers: %s", got)
	}
}

// An episode has aired once its air instant has passed, as the engine and Wanted have it; the UTC day of its air
// date does not decide. S01E03 airs "today" by date but hours from now (a US evening); S02E01 is dated tomorrow and
// aired an hour ago (a New Zealand night). S01E03 is unmonitored, so only S02E01 can be missing.
func TestAiredByInstant(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedLibrary()
	key := e.apiKey()
	e.exec(`UPDATE episode_version_state SET monitored = 0 WHERE episode_id = 43`)
	e.exec(`UPDATE episode SET air_date = '2026-10-26', air_date_utc_at = ? WHERE id = 43`,
		store.Millis(time.Date(2026, 10, 27, 1, 0, 0, 0, time.UTC)))
	e.exec(`UPDATE episode SET air_date = '2026-10-27', air_date_utc_at = ? WHERE id = 44`,
		store.Millis(time.Date(2026, 10, 26, 11, 30, 0, 0, time.UTC)))
	// An instant without a date (not something a refresh writes) is not a next episode: there is no day to show.
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, air_date_utc_at, created_at, updated_at) VALUES
		(45, 4, 2, 2, 'Undated', ?, 0, 0)`, store.Millis(time.Date(2026, 10, 27, 1, 0, 0, 0, time.UTC)))
	var l EpisodeList
	if code := e.getKey(APIBase+"/titles/4/episodes", key, &l); code != 200 {
		t.Fatalf("episodes: %d", code)
	}
	aired := map[string]bool{}
	for _, ep := range l.Episodes {
		aired[episodeCode(ep.Season, ep.Number)] = ep.Aired
	}
	if !aired["S01E01"] || aired["S01E03"] || !aired["S02E01"] {
		t.Errorf("aired: %v", aired)
	}
	var seasons []string
	for _, s := range l.Seasons {
		c := s.Versions["anime-tv"]
		seasons = append(seasons, itoa(s.Season)+":"+itoa(int64(s.Aired))+"/"+itoa(int64(c.Missing)))
	}
	if got := strings.Join(seasons, " "); got != "1:2/0 2:1/1 0:1/1" {
		t.Errorf("seasons (aired/missing): %s", got)
	}
	var d TitleDetail
	if e.getKey(APIBase+"/titles/4", key, &d); d.Versions[0].Episodes == nil || d.Versions[0].Episodes.Missing != 1 {
		t.Errorf("title page counts: %+v", d.Versions[0].Episodes)
	}
	var page TitlePage
	if code := e.getKey(APIBase+"/titles?anime=true", key, &page); code != 200 {
		t.Fatalf("titles: %d", code)
	}
	row := rowOf(t, page, 4)
	if c := copyOf(t, row, "anime-tv").Coverage; c == nil || c.Aired != 3 || c.Upcoming != 2 || c.Missing != 1 {
		t.Errorf("list coverage: %+v", c)
	}
	if n := row.Next; n == nil || n.Date != "2026-10-26" || *n.Season != 1 || *n.Number != 3 {
		t.Errorf("next episode: %+v", n)
	}
	var dv Discover
	if code := e.getKey(APIBase+"/discover", key, &dv); code != 200 {
		t.Fatalf("discover: %d", code)
	}
	note := func(r DiscoverRow) string {
		for _, it := range r.Items {
			if it.ID == 4 {
				return it.Note + "|" + it.Date
			}
		}
		return ""
	}
	if got := note(dv.Missing); got != "1 episode missing|2026-10-27" {
		t.Errorf("Home missing: %q", got)
	}
	if got := note(dv.Upcoming); got != "S01E03 · Three|2026-10-26" {
		t.Errorf("Home upcoming: %q", got)
	}
}

// An episode with an air date and no instant (an import can leave it so, and no refresh has filled it) is aired from
// the day of its date, as before instants were read; one with an instant and no date goes by the instant, and Home
// takes its day from the instant. The fixtures are written here, after seedLibrary's airAtMidnight: S01E03 (past) and
// S02E01 (five days ahead) keep their dates and lose their instants; S02E02 has an instant an hour old and no date.
func TestAiredWithoutInstantOrDate(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedLibrary()
	key := e.apiKey()
	e.exec(`UPDATE episode SET air_date_utc_at = NULL WHERE id IN (43, 44)`)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, air_date_utc_at, created_at, updated_at) VALUES
		(45, 4, 2, 2, 'Undated', ?, 0, 0)`, store.Millis(testNow.Add(-time.Hour)))
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, state, updated_at) VALUES (4, 45, 'wanted', 0)`)
	var l EpisodeList
	if code := e.getKey(APIBase+"/titles/4/episodes", key, &l); code != 200 {
		t.Fatalf("episodes: %d", code)
	}
	aired := map[string]bool{}
	for _, ep := range l.Episodes {
		aired[episodeCode(ep.Season, ep.Number)] = ep.Aired
	}
	if !aired["S01E03"] || aired["S02E01"] || !aired["S02E02"] {
		t.Errorf("aired: %v", aired)
	}
	var seasons []string
	for _, s := range l.Seasons {
		c := s.Versions["anime-tv"]
		seasons = append(seasons, itoa(s.Season)+":"+itoa(int64(s.Aired))+"/"+itoa(int64(c.Missing)))
	}
	if got := strings.Join(seasons, " "); got != "1:3/1 2:1/1 0:1/1" {
		t.Errorf("seasons (aired/missing): %s", got)
	}
	// S02E01 held before it airs: season 2 has two episodes aired or held in the copy, one of them held.
	e.exec(`UPDATE episode_version_state SET state = 'filled' WHERE episode_id = 44`)
	e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (91, 44)`)
	var held EpisodeList
	e.getKey(APIBase+"/titles/4/episodes", key, &held)
	for _, s := range held.Seasons {
		if c := s.Versions["anime-tv"]; s.Season == 2 && (s.Aired != 1 || c.Have != 1 || c.Total != 2 || c.Missing != 1) {
			t.Errorf("season 2 with an unaired episode held: aired %d, %+v", s.Aired, c)
		}
	}
	e.exec(`UPDATE episode_version_state SET state = 'wanted' WHERE episode_id = 44`)
	e.exec(`DELETE FROM link_episode WHERE episode_id = 44`)
	var d TitleDetail
	if e.getKey(APIBase+"/titles/4", key, &d); d.Versions[0].Episodes == nil ||
		!sameCounts(*d.Versions[0].Episodes, 4, 2, 2, 4, 2) {
		t.Errorf("title page counts: %+v", d.Versions[0].Episodes)
	}
	var page TitlePage
	if code := e.getKey(APIBase+"/titles?anime=true", key, &page); code != 200 {
		t.Fatalf("titles: %d", code)
	}
	row := rowOf(t, page, 4)
	if c := copyOf(t, row, "anime-tv").Coverage; c == nil || c.Aired != 4 || c.Upcoming != 1 || c.Missing != 2 {
		t.Errorf("list coverage: %+v", c)
	}
	if n := row.Next; n == nil || n.Date != "2026-10-31" || *n.Season != 2 || *n.Number != 1 {
		t.Errorf("next episode: %+v", n)
	}
	note := func(r DiscoverRow) string {
		for _, it := range r.Items {
			if it.ID == 4 {
				return it.Note + "|" + it.Date
			}
		}
		return ""
	}
	var dv Discover
	if code := e.getKey(APIBase+"/discover", key, &dv); code != 200 {
		t.Fatalf("discover: %d", code)
	}
	if got := note(dv.Missing); got != "2 episodes missing|2026-01-15" {
		t.Errorf("Home missing: %q", got)
	}
	if got := note(dv.Upcoming); got != "S02E01 · Return|2026-10-31" {
		t.Errorf("Home upcoming: %q", got)
	}
	// The only missing episode left has no air date: Home still answers, with the UTC day of its instant.
	e.exec(`UPDATE episode_version_state SET monitored = 0 WHERE episode_id = 43`)
	dv = Discover{}
	if code := e.getKey(APIBase+"/discover", key, &dv); code != 200 {
		t.Fatalf("discover with an undated missing episode: %d", code)
	}
	if got := note(dv.Missing); got != "1 episode missing|2026-10-26" {
		t.Errorf("Home missing, undated: %q", got)
	}
}

func TestDiscover(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedLibrary()
	key := e.apiKey()
	var d Discover
	if code := e.getKey(APIBase+"/discover", key, &d); code != 200 {
		t.Fatalf("discover: %d", code)
	}
	row := func(r DiscoverRow) string {
		var s []string
		for _, it := range r.Items {
			s = append(s, it.Name+"|"+it.Note+"|"+it.Date)
		}
		return strings.Join(s, "; ")
	}
	for name, c := range map[string]struct {
		r    DiscoverRow
		want string
	}{
		"recent":   {d.Recent, "Beta||2026-10-25; Delta||2026-10-21; Alpha||2026-10-16"},
		"upcoming": {d.Upcoming, "Delta|S02E01 · Return|2026-10-31; Beta|Digital release|2026-11-05"},
		"missing":  {d.Missing, "Delta|1 episode missing|2026-01-15; Alpha|4K WEB|2020-08-01"},
		// Beta and Delta have files below their cutoff, and nothing better was found: not in the row.
		"upgrades": {d.Upgrades, ""},
	} {
		if got := row(c.r); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
		if c.r.Total != len(c.r.Items) || c.r.More == "" {
			t.Errorf("%s: total %d, more %q", name, c.r.Total, c.r.More)
		}
	}
	if len(d.Recent.Items) > 0 && d.Recent.Items[0].Poster == "" {
		t.Error("discover items lack art")
	}
	// "See all": every row's more link is its own page across kinds, with the same items in the same order.
	for name, r := range map[string]DiscoverRow{"recent": d.Recent, "upcoming": d.Upcoming, "missing": d.Missing,
		"upgrades": d.Upgrades} {
		if r.More != "/discover/"+name {
			t.Errorf("%s: more %q", name, r.More)
		}
		var full DiscoverRow
		if code := e.getKey(APIBase+"/discover/"+name, key, &full); code != 200 {
			t.Fatalf("discover/%s: %d", name, code)
		}
		if row(full) != row(r) || full.Total != r.Total {
			t.Errorf("discover/%s: %q, want %q", name, row(full), row(r))
		}
		// Recently added carries the moment on Home and on its "See all" page; the browser picks the day.
		for i, it := range full.Items {
			if name != "recent" {
				if it.At != nil || r.Items[i].At != nil {
					t.Errorf("discover/%s: %s has a moment", name, it.Name)
				}
			} else if it.At == nil || r.Items[i].At == nil || !it.At.Equal(*r.Items[i].At) {
				t.Errorf("discover/recent: %s at %v, Home %v", it.Name, it.At, r.Items[i].At)
			}
		}
	}
	var page DiscoverRow
	if code := e.getKey(APIBase+"/discover/missing?limit=1&offset=1", key, &page); code != 200 || page.Total != 2 ||
		row(page) != "Alpha|4K WEB|2020-08-01" {
		t.Errorf("discover/missing page 2: %d %+v", code, page)
	}
	if code := e.getKey(APIBase+"/discover/nope", key, nil); code != 404 {
		t.Errorf("discover/nope: %d", code)
	}
	// The upgrades row lists a title once a better release was found for its file.
	upgrades := func() string {
		t.Helper()
		var r DiscoverRow
		if code := e.getKey(APIBase+"/discover/upgrades", key, &r); code != 200 || r.Total != len(r.Items) {
			t.Fatalf("discover/upgrades: %d %+v", code, r)
		}
		var home Discover
		if code := e.getKey(APIBase+"/discover", key, &home); code != 200 || row(home.Upgrades) != row(r) {
			t.Fatalf("discover upgrades: %d %q, row page %q", code, row(home.Upgrades), row(r))
		}
		return row(r)
	}
	e.exec(`INSERT INTO journal_decision(source,title_id,version_key,target,results,accepted,candidates,rejections,notes,created_at)
		VALUES('test',2,'movie-web','movie',1,1,'[{"release":"Beta.Remux","accepted":true,"upgrade":true,"score":9000}]','{}','',?)`,
		store.Millis(testNow))
	if got := upgrades(); got != "Beta|4K WEB: upgrade found|" {
		t.Errorf("upgrades with a release found: %q", got)
	}
	// A later search that found nothing drops it.
	e.exec(`INSERT INTO journal_decision(source,title_id,version_key,target,results,accepted,candidates,rejections,notes,created_at)
		VALUES('test',2,'movie-web','movie',0,0,'[]','{}','',?)`, store.Millis(testNow)+1000)
	if got := upgrades(); got != "" {
		t.Errorf("upgrades after an empty newer search: %q", got)
	}
}

func TestTitlePage(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedLibrary()
	e.seedAnime()
	key := e.apiKey()
	var d TitleDetail
	if code := e.getKey(APIBase+"/titles/1", key, &d); code != 200 {
		t.Fatalf("title: %d", code)
	}
	v := d.Versions[0]
	if v.Label != "4K Remux" || v.Current == nil || v.Current.Quality != "Remux-2160p" || v.Current.InfoHash != hashA ||
		v.Current.Path == "" || !v.Current.CutoffMet || strings.Join(v.Providers, ",") != "rd" {
		t.Errorf("remux: %+v %+v", v, v.Current)
	}
	var events []string
	for _, h := range v.History {
		events = append(events, h.Event)
	}
	// The second import recorded no replaced file: an import, not an upgrade.
	if got := strings.Join(events, ","); got != "imported,grabbed,imported,grabbed" {
		t.Errorf("history: %s", got)
	}
	if d.Versions[1].Current != nil || d.Availability != "partial" || d.AddedAt == nil {
		t.Errorf("web version: %+v; availability %q", d.Versions[1], d.Availability)
	}
	d = TitleDetail{}
	if e.getKey(APIBase+"/titles/4", key, &d); d.Versions[0].Episodes == nil || !sameCounts(*d.Versions[0].Episodes, 3, 2, 1, 3, 2) {
		// S02E01 has not aired and is not held: "2 of 3 episodes ready", as the episode list counts.
		t.Errorf("Delta episodes: %+v", d.Versions[0].Episodes)
	}
	if c := d.Versions[0].Current; c == nil || c.Files != 2 || c.Path != "" || c.SizeBytes == nil || *c.SizeBytes != 2000000000 {
		t.Errorf("Delta current: %+v", c)
	}
	d = TitleDetail{}
	if e.getKey(APIBase+"/titles/9", key, &d); len(d.Franchises) != 1 || d.Franchises[0].Slug != "mushoku-tensei" {
		t.Errorf("franchises: %+v", d.Franchises)
	}
}

// The title page's facts for "why and what next" (J5): a failed miss names the providers it missed on, each library
// carries its last targeted search (or none) and a search under way, the title whether Theatarr searches by itself,
// and a series library its monitored counts; a caller without see_files gets none of them.
func TestTitlePageSearchFacts(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedLibrary()
	key := e.apiKey()
	now := store.Millis(testNow)
	miss := strings.Repeat("7", 40)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		state, error, source, created_at, updated_at) VALUES (85, 1, 1, 'movie-remux', ?, 'Alpha.2020.Remux-NEW', 'dmm',
		'rd', 'failed', 'miss on every provider', 'rss', ?, ?)`, strings.ToUpper(miss), now-300, now-200)
	e.exec(`INSERT INTO download_attempt (download_id, provider, outcome, code, message, attempted_at) VALUES
		(85, 'rd', 'skipped', 'miss', 'not cached', ?), (85, 'tb', 'skipped', 'rd_name_rule', 'blocked name', ?)`, now-200, now-200)
	// The hash-wide cache says TB missed it too; only the attempt's own outcomes count (TB refused the name).
	e.exec(`INSERT INTO provider_cache (info_hash, provider, state, source, checked_at) VALUES (?, 'rd', 'miss', 'miss', ?),
		(?, 'tb', 'miss', 'miss', ?)`, miss, now-200, miss, now-200)
	e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, state, results, accepted,
		message, created_at, updated_at) VALUES (51, 'wanted', 1, 2, 'movie-web', 'movie', 'nothing', 12, 0, '', ?, ?),
		(52, 'command', 1, 2, 'movie-web', 'movie', 'queued', 0, 0, '', ?, ?)`, now-7200, now-7200, now-60, now-60)
	var d TitleDetail
	if code := e.getKey(APIBase+"/titles/1", key, &d); code != 200 {
		t.Fatalf("title: %d", code)
	}
	var failed *HistoryEvent
	for i, h := range d.Versions[0].History {
		if h.Event == "failed" {
			failed = &d.Versions[0].History[i]
		}
		if h.Event == "grabbed" && len(h.MissedOn) > 0 {
			t.Errorf("a grabbed event carries missed_on: %+v", h)
		}
	}
	if failed == nil || strings.Join(failed.MissedOn, ",") != "rd" || failed.DownloadID != 85 || !failed.Upgrade {
		t.Errorf("failed miss: %+v", failed)
	}
	// The server's reason names the providers too (the page shows it as it is).
	if failed != nil && failed.Reason != "Not cached on RD" {
		t.Errorf("failed miss reason: %q, want %q", failed.Reason, "Not cached on RD")
	}
	if v := d.Versions[0]; v.LastSearch != nil || v.Searching != "" {
		t.Errorf("remux was never searched: %+v %q", v.LastSearch, v.Searching)
	}
	if v := d.Versions[1]; v.LastSearch == nil || v.LastSearch.RunID != 52 || v.Searching != "queued" {
		t.Errorf("web: %+v %q", v.LastSearch, v.Searching)
	}
	if a := d.AutoSearch; a == nil || !a.Missing {
		t.Errorf("auto search: %+v", a)
	}
	if d.Collection != nil {
		t.Errorf("no stored collection: %+v", d.Collection)
	}
	// The stored collection the film is part of, for its page.
	e.exec(`INSERT INTO title_external_id (title_id, source, external_id) VALUES (1, 'tmdb_collection', '8091')`)
	e.exec(`INSERT INTO movie_collection (tmdb_id, name, versions, movies, updated_at) VALUES (8091, 'Alpha Collection',
		'[]', '[]', 0)`)
	d = TitleDetail{}
	if e.getKey(APIBase+"/titles/1", key, &d); d.Collection == nil || d.Collection.TmdbID != 8091 ||
		d.Collection.Name != "Alpha Collection" {
		t.Errorf("collection: %+v", d.Collection)
	}
	// A run left queued for more than an hour is not under way; the last search is still the newest run.
	e.exec(`UPDATE acquire_run SET created_at = ? WHERE id = 52`, now-2*3600*1000)
	d = TitleDetail{}
	if e.getKey(APIBase+"/titles/1", key, &d); d.Versions[1].Searching != "" || d.Versions[1].LastSearch.RunID != 51 {
		t.Errorf("stale queued run: %+v %q", d.Versions[1].LastSearch, d.Versions[1].Searching)
	}
	// Search now's due row is a queued search.
	e.exec(`INSERT INTO search_request (title_id, version_key, reason, season, created_at, next_at, tries, why) VALUES
		(1, 'movie-remux', 'missing', -1, ?, ?, 0, 'asked')`, now-10, now-10)
	d = TitleDetail{}
	if e.getKey(APIBase+"/titles/1", key, &d); d.Versions[0].Searching != "queued" {
		t.Errorf("asked row: %q", d.Versions[0].Searching)
	}
	// A list addition's due row is queued too, with both backlog switches off (it is searched whatever they say).
	off := false
	e.web.pol.Router.Wanted = &policy.Wanted{Missing: &off, CutoffUnmet: policy.CutoffOff}
	e.exec(`UPDATE search_request SET why = 'added' WHERE title_id = 1`)
	d = TitleDetail{}
	if e.getKey(APIBase+"/titles/1", key, &d); d.Versions[0].Searching != "queued" || d.AutoSearch.Missing ||
		d.AutoSearch.Upgrades {
		t.Errorf("added row: %q %+v", d.Versions[0].Searching, d.AutoSearch)
	}
	// Episode monitoring: S01E03 unmonitored leaves 2 of 2 monitored held; Have and Missing keep their rules.
	e.exec(`UPDATE episode_version_state SET monitored = 0 WHERE episode_id = 43`)
	d = TitleDetail{}
	if e.getKey(APIBase+"/titles/4", key, &d); !sameCounts(*d.Versions[0].Episodes, 3, 2, 0, 2, 2) {
		t.Errorf("Delta counts: %+v", d.Versions[0].Episodes)
	}

	// A failed first download of an episode is no upgrade because another episode is held; one of a held episode is.
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state, error,
		episodes, source, created_at, updated_at) VALUES
		(86, 4, 4, 'anime-tv', ?, 'Delta.S01E03', 'dmm', 'failed', 'stalled: no progress', '[43]', 'rss', ?, ?),
		(87, 4, 4, 'anime-tv', ?, 'Delta.S01E01.v2', 'dmm', 'failed', 'stalled: no progress', '[41]', 'rss', ?, ?)`,
		strings.Repeat("6", 40), now-100, now-90, strings.Repeat("5", 40), now-80, now-70)
	d = TitleDetail{}
	e.getKey(APIBase+"/titles/4", key, &d)
	ups := map[int64]bool{}
	for _, h := range d.Versions[0].History {
		if h.Event == "failed" {
			ups[h.DownloadID] = h.Upgrade
		}
	}
	// An earlier import is no proof: the film's 4K WEB library holds no file now, so its failure is no upgrade.
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state, error,
		episodes, source, created_at, updated_at, imported_at) VALUES
		(88, 1, 2, 'movie-web', ?, 'Alpha.WEB.Old', 'dmm', 'imported', NULL, '[]', 'rss', ?, ?, ?),
		(89, 1, 2, 'movie-web', ?, 'Alpha.WEB.New', 'dmm', 'failed', 'stalled: no progress', '[]', 'rss', ?, ?, NULL)`,
		strings.Repeat("4", 40), now-500, now-400, now-400, strings.Repeat("3", 40), now-60, now-50)
	d1 := TitleDetail{}
	e.getKey(APIBase+"/titles/1", key, &d1)
	seen89 := false
	for _, v := range d1.Versions {
		for _, h := range v.History {
			if h.Event == "failed" && h.DownloadID == 89 {
				seen89 = true
				if h.Upgrade {
					t.Errorf("a failed download of an empty library reads as an upgrade: %+v", h)
				}
			}
		}
	}
	if !seen89 {
		t.Error("download 89's failure is not in the history")
	}
	if up, ok := ups[86]; !ok || up {
		t.Errorf("first download of S01E03 reads as an upgrade: %v", ups)
	}
	if !ups[87] {
		t.Errorf("a held episode's failed download is an upgrade attempt: %v", ups)
	}

	// The episode window: only the runs that searched the episode count, with counts only for a run of it alone.
	e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, season, episodes, state,
		results, accepted, message, created_at, updated_at) VALUES
		(61, 'command', 4, 4, 'anime-tv', 's01e01', 1, '[41]', 'nothing', 7, 0, '', ?, ?),
		(62, 'command', 4, 4, 'anime-tv', 's01e02', 1, '[42]', 'failed', 0, 0, 'search failed', ?, ?),
		(63, 'wanted', 4, 4, 'anime-tv', 's02', 2, '[]', 'routed', 30, 2, '', ?, ?)`,
		now-3000, now-3000, now-2000, now-2000, now-1000, now-1000)
	ep := func(id int) *EpisodeVersionDetail {
		var out EpisodeDetail
		if code := e.getKey(fmt.Sprintf("%s/titles/4/episodes/%d", APIBase, id), key, &out); code != 200 || len(out.Versions) == 0 {
			t.Fatalf("episode %d: %d", id, code)
		}
		return &out.Versions[0]
	}
	if l := ep(41).LastSearch; l == nil || l.RunID != 61 || l.Results == nil || *l.Results != 7 {
		t.Errorf("S01E01 last search: %+v", l)
	}
	if l := ep(42).LastSearch; l == nil || l.RunID != 62 || l.State != "failed" {
		t.Errorf("S01E02 last search: %+v", l)
	}
	if l := ep(43).LastSearch; l != nil {
		t.Errorf("S01E03 was never searched, not by another episode's run: %+v", l)
	}
	if l := ep(44).LastSearch; l == nil || l.RunID != 63 || l.Results != nil {
		t.Errorf("S02E01 is covered by the season run, without its counts: %+v", l)
	}

	mux := e.userMux()
	for _, p := range []string{"/titles/1", "/titles/4", "/titles/4/episodes/43"} {
		r := httptest.NewRequest("GET", APIBase+p, nil)
		r.Header.Set(auth.APIKeyHeader, key)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		for _, f := range []string{`"last_search"`, `"searching"`, `"auto_search"`, `"missed_on"`, `"monitored_have"`,
			`"collection"`, `"rss"`} {
			if strings.Contains(rec.Body.String(), f) {
				t.Errorf("%s shows %s to a caller without see_files", p, f)
			}
		}
	}
}

// The RSS sentence follows the RSS reader's own rule (acquire/rss Runner.Covers, tested there); without a reader
// it is never said.
type coversOnly map[string]bool

func (c coversOnly) Covers(_ context.Context, v string) bool { return c[v] }

func TestReadsRSS(t *testing.T) {
	ctx := context.Background()
	w := &Web{}
	if w.readsRSS(ctx, "movie-web") {
		t.Error("no reader: RSS said")
	}
	w.rss = coversOnly{"movie-web": true}
	if !w.readsRSS(ctx, "movie-web") || w.readsRSS(ctx, "movie-remux") {
		t.Error("readsRSS does not follow the reader")
	}
}

// sameCounts compares a title page's episode counts, the monitored ones included.
func sameCounts(c EpisodeCounts, total, have, missing, monitored, monitoredHave int) bool {
	return c.Total == total && c.Have == have && c.Missing == missing && c.Monitored != nil && *c.Monitored == monitored &&
		c.MonitoredHave != nil && *c.MonitoredHave == monitoredHave
}

// userMux serves the same store as e with a caller who may not see files.
func (e *env) userMux() *http.ServeMux {
	e.t.Helper()
	w, err := New(Config{Store: e.st, Auth: e.auth, Policy: e.web.pol, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return testNow }, UI: testUI, Perms: func(*auth.Identity) Perms { return PermsOf(auth.PermBrowse | auth.PermRequest) }})
	if err != nil {
		e.t.Fatal(err)
	}
	mux := http.NewServeMux()
	w.Mount(mux)
	return mux
}

func TestNoFilesWithoutSeeFiles(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedLibrary()
	key := e.apiKey()
	mux := e.userMux()
	get := func(path string) (int, string) {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set(auth.APIKeyHeader, key)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec.Code, rec.Body.String()
	}
	secret := []string{hashA, "/movies/", "/anime/", ".mkv", "Alpha.2020.2160p", "Alpha.Old", "radarr_4k_debrid",
		"SubsPlease", `"size_bytes"`, `"providers"`, `"score"`, `"path"`, `"info_hash"`, `"release_title"`,
		`"custom_format_score"`, `"rd"`, `"cached_on"`, `"formats"`}
	for _, p := range []string{"/titles", "/titles/1", "/titles/2", "/titles/4", "/titles/4/episodes",
		"/titles/facets", "/discover", "/discover/upgrades", "/discover/recent", "/versions", "/versions/1"} {
		code, body := get(APIBase + p)
		if code != 200 {
			t.Errorf("%s: %d", p, code)
			continue
		}
		for _, s := range secret {
			if strings.Contains(body, s) {
				t.Errorf("%s shows %s to a caller without see_files", p, s)
			}
		}
	}
	// "Recently available" (an account without see_files) carries the moment too, on Home and on "See all".
	for _, p := range []string{"/discover", "/discover/recent"} {
		if _, body := get(APIBase + p); strings.Count(body, `"at":"2026-10-`) != 3 {
			t.Errorf("%s: want the moment of each of the 3 recent titles: %s", p, body)
		}
	}
	// Availability stays, as a resolution; the source and the history go.
	if _, body := get(APIBase + "/titles/1"); !strings.Contains(body, `"quality":"2160p"`) ||
		strings.Contains(body, "Remux-2160p") || strings.Contains(body, `"history"`) {
		t.Errorf("title page: want the resolution only and no history: %s", body)
	}
	if code, _ := get(APIBase + "/titles?provider=rd"); code != 403 {
		t.Errorf("provider filter: %d, want 403", code)
	}
	var me map[string]any
	_, body := get(APIBase + "/auth/me")
	if json.Unmarshal([]byte(body), &me); fmt.Sprint(me["permissions"]) != "[browse request]" {
		t.Errorf("me: %v", me)
	}
	// Library names for the SPA: every library, named, and no name holds its key.
	var who Me
	if err := json.Unmarshal([]byte(body), &who); err != nil || len(who.Libraries) == 0 || len(who.Libraries) != len(e.web.pol.Versions) {
		t.Errorf("me libraries: %v %+v", err, who.Libraries)
	}
	if len(who.Providers) != 0 {
		t.Errorf("me providers for a caller without see_files: %+v", who.Providers)
	}
	for _, l := range who.Libraries {
		if l.Label == "" || strings.Contains(l.Label, l.Key) {
			t.Errorf("me library %s: name %q", l.Key, l.Label)
		}
	}
	// An admin sees them.
	var adm map[string]any
	if e.getKey(APIBase+"/auth/me", key, &adm); !strings.Contains(fmt.Sprint(adm["permissions"]), "see_files") {
		t.Errorf("admin me: %v", adm)
	}
	// Accounts that see files get each provider account's cache code.
	var admMe Me
	e.getKey(APIBase+"/auth/me", key, &admMe)
	if !slices.Contains(admMe.Providers, ProviderCode{Key: "rd", Code: "rd"}) {
		t.Errorf("admin me providers: %+v", admMe.Providers)
	}

	// List rows carry no library profile, availability rule or folder; an admin's do.
	e.exec(`INSERT INTO link (id, path, target, title_id, quality, state, created_at, updated_at) VALUES
		(70, '/media/movies-web/Alpha (2020)/Alpha.mkv', '/zurg/__all__/Alpha/a.mkv', 1, 'WEBDL-2160p', 'active', 1, 1)`)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (70, 2)`)
	for _, p := range []string{"/titles", "/discover", "/discover/upgrades", "/discover/recent"} {
		_, body := get(APIBase + p)
		for _, s := range []string{`"profile"`, `"minimum_availability"`, `"folder"`} {
			if strings.Contains(body, s) {
				t.Errorf("%s shows %s to a caller without see_files", p, s)
			}
		}
	}
	var ap TitlePage
	e.getKey(APIBase+"/titles?q=Alpha", key, &ap)
	if v := ap.Items[0].Versions[1]; v.Profile == "" || v.MinimumAvailability == "" || v.Folder != "/media/movies-web/Alpha (2020)" {
		t.Errorf("admin's Alpha copy: %+v", v)
	}
}

// TestMissingRowHomeRelease: Home's Missing row counts a movie released on its home release, as list rows'
// home_release, so no movie there reads Upcoming; without canonical dates, the old rule (its US or worldwide
// digital or disc date, else TMDb's release date plus 90 days).
func TestMissingRowHomeRelease(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedLibrary()
	now := store.Millis(testNow)
	e.exec(`INSERT INTO title (id, kind, name, year, tmdb_id, monitored, created_at, updated_at) VALUES
		(40, 'movie', 'US only', 2026, 540, 1, ?, ?), (41, 'movie', 'Refreshed', 2026, 541, 1, ?, ?),
		(42, 'movie', 'Out', 2026, 542, 1, ?, ?), (43, 'movie', 'Undated', 2026, 543, 1, ?, ?)`,
		now, now, now, now, now, now, now, now)
	e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at) VALUES
		(40, 40, 'movie-web', 1, 'wanted', ?, ?), (41, 41, 'movie-web', 1, 'wanted', ?, ?),
		(42, 42, 'movie-web', 1, 'wanted', ?, ?), (43, 43, 'movie-web', 1, 'wanted', ?, ?)`,
		now, now, now, now, now, now, now, now)
	// US only: a past US digital date, no canonical one (no home_release, so its poster uses TMDb's release date).
	// Refreshed: a past imported digital date, the TMDb refresh still ahead. Out: a past canonical digital date.
	// Undated: no dates, TMDb's release date 2026-06-01 (plus 90 is past).
	e.exec(`INSERT INTO title_meta (title_id, release_date, movie_dates_at, attempted_at, refresh_after) VALUES
		(40, '2026-10-01', NULL, 1, 1), (41, '2026-08-01', 1, 1, 1), (42, '2026-08-01', NULL, 1, 1),
		(43, '2026-06-01', NULL, 1, 1)`)
	e.exec(`INSERT INTO title_release_date (title_id, source, country, type, date) VALUES
		(40, 'tmdb', 'US', 'digital', '2026-09-01'),
		(41, 'radarr_4k_debrid', '', 'digital', '2026-09-01'), (41, 'tmdb-art', '', 'digital', '2026-12-01'),
		(42, 'tmdb', '', 'digital', '2026-09-15')`)
	var r DiscoverRow
	if code := e.getKey(APIBase+"/discover/missing", e.apiKey(), &r); code != 200 {
		t.Fatalf("discover/missing: %d", code)
	}
	got := map[string]string{}
	today := testNow.Format(time.DateOnly)
	for _, it := range r.Items {
		got[it.Name] = it.Date
		if it.HomeRelease > today {
			t.Errorf("%s is missing with home_release %s", it.Name, it.HomeRelease)
		}
	}
	if got["Out"] != "2026-09-15" || got["Undated"] != "2026-08-30" || got["US only"] != "2026-09-01" {
		t.Errorf("missing row %v: Out on its digital date, Undated on TMDb's date plus 90 days, US only on its US date", got)
	}
	if _, ok := got["Refreshed"]; ok {
		t.Errorf("Refreshed is not out at home yet but in the missing row %v", got)
	}
}

// libraryNames: a library's own name wins over the default; an empty or blank one falls back; the two anime
// defaults differ by kind; libraries that would share a name are told apart by kind, then by number, never by key.
func TestVersionLabelName(t *testing.T) {
	t.Parallel()
	w := &Web{pol: &policy.Policy{Versions: map[string]*policy.VersionPolicy{
		"movie-web":      {Kind: policy.KindMovie, Profile: "web-2160p", Name: " Films "},
		"movie-remux":    {Kind: policy.KindMovie, Profile: "remux-2160p", Name: "Films"},
		"movie-2160p":    {Kind: policy.KindMovie, Profile: "remux-2160p", Name: "4K"},
		"tv-2160p":       {Kind: policy.KindSeries, Profile: "tv-2160p"},
		"anime-tv-mm":    {Kind: policy.KindSeries, Profile: "anime-tv", Name: "  "},
		"anime-movie-mm": {Kind: policy.KindMovie, Profile: "anime-remux-1080p-movie"},
	}}}
	for key, want := range map[string]string{
		"movie-remux": "Films", "movie-web": "Films (2)", // same kind: numbered in key order
		"movie-2160p": "4K movies", "tv-2160p": "4K series", // kinds differ: the kind tells them apart
		"anime-tv-mm": "Anime series", "anime-movie-mm": "Anime movies",
		"gone": "gone",
	} {
		if got := w.versionLabel(key); got != want {
			t.Errorf("%s: %q, want %q", key, got, want)
		}
	}
	for k, l := range libraryNames(w.pol) {
		if strings.Contains(l, k) {
			t.Errorf("%s: name %q holds the key", k, l)
		}
	}
	got := w.keyLabels([]string{"movie-web", "movie-remux", "tv-2160p"})
	want := map[string]string{"movie-web": "Films (2)", "movie-remux": "Films", "tv-2160p": "4K series"}
	if !maps.Equal(got, want) {
		t.Errorf("labels %v, want %v", got, want)
	}
}

// TestAdoptedLinkSize: a link with no content file (adopted or imported from Sonarr/Radarr) counts the size its
// stored file metadata keeps, in the title's size and its version's.
func TestAdoptedLinkSize(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	now := store.Millis(testNow)
	e.exec(`INSERT INTO link (id, path, target, title_id, quality, file_metadata, adopted_from, state, created_at, updated_at)
		VALUES (7, '/web/Alpha (2020)/Alpha.mkv', '/zurg/__all__/Alpha/a.mkv', 1, 'WEBDL-2160p', '{"Size":4000000000}',
		'radarr@r:7878 /movies/Alpha.mkv', 'active', ?, ?)`, now, now)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (7, 2)`)
	var p TitlePage
	if code := e.getKey(APIBase+"/titles?q=Alpha", e.apiKey(), &p); code != 200 || len(p.Items) != 1 {
		t.Fatalf("titles: %d %+v", code, p)
	}
	a := p.Items[0]
	if a.SizeBytes == nil || *a.SizeBytes != 54000000000 {
		t.Errorf("title size %v", a.SizeBytes)
	}
	for _, v := range a.Versions {
		if v.Label == "4K WEB" && (v.SizeBytes == nil || *v.SizeBytes != 4000000000) {
			t.Errorf("version size %+v", v)
		}
	}
}

// TestAdoptedLinkFileSizes: the per-file readers (episode list, episode file panel, upgrade items) give a link with no
// content file the size its file metadata keeps.
func TestAdoptedLinkFileSizes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedLibrary()
	now := store.Millis(testNow)
	e.exec(`INSERT INTO link (id, path, target, title_id, quality, file_metadata, adopted_from, state, created_at, updated_at)
		VALUES (8, '/anime/Delta/S01E03.mkv', '/zurg/__all__/Delta/e3.mkv', 4, 'Bluray-1080p', '{"Size":3000000000}',
		'sonarr@s:8989 /anime/Delta/S01E03.mkv', 'active', ?, ?)`, now, now)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (8, 4)`)
	// The link holds S01E02-E03 (it replaced E02's adopted file).
	e.exec(`DELETE FROM link WHERE id = 91`)
	e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (8, 42), (8, 43)`)
	const want = 3000000000
	list, err := e.web.titleEpisodes(context.Background(), 4, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ep := range list.Episodes {
		if ep.ID == 43 {
			found = true
			if c := ep.Versions["anime-tv"]; c.SizeBytes == nil || *c.SizeBytes != want {
				t.Errorf("episode list size %+v", c)
			}
		}
	}
	if !found {
		t.Fatalf("episode 43 not listed: %+v", list.Episodes)
	}
	// The season counts the two-episode link once: E01's adopted file (1 GB) and the link (3 GB).
	for _, se := range list.Seasons {
		if c := se.Versions["anime-tv"]; se.Season == 1 && (c.SizeBytes == nil || *c.SizeBytes != 4000000000) {
			t.Errorf("season 1 size %+v", c.SizeBytes)
		}
	}
	if f, err := e.web.episodeFile(context.Background(), e.st.Reader(), 4, 43, func([]string) []string { return nil }); err != nil || f == nil || f.SizeBytes == nil || *f.SizeBytes != want {
		t.Errorf("episode file %+v %v", f, err)
	}
	files, err := loadItemFiles(context.Background(), e.st.Reader(), []any{int64(4)}, true)
	if err != nil || files[4][43].size != want {
		t.Errorf("upgrade item files %+v %v", files[4], err)
	}
}

// The Episodes table's cell carries the torrent hash its episode window reads (an arr file's content, a link's
// content file), so "Cached on" there gets the same Torrent link; never for a caller without see_files.
func TestEpisodeCellInfoHash(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedLibrary()
	e.exec(`INSERT INTO content (id, info_hash, name, state, first_seen_at, last_seen_at) VALUES
		(11, 'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee', 'a', 'present', 0, 0),
		(12, 'ffffffffffffffffffffffffffffffffffffffff', 'b', 'present', 0, 0)`)
	e.exec(`UPDATE link SET file_metadata = json_set(file_metadata, '$.InfoHash', 'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee')
		WHERE id = 90`)
	e.exec(`DELETE FROM link WHERE id = 91`) // S01E02's file is link 51 below
	e.exec(`INSERT INTO content_file (id, content_id, path, size_bytes) VALUES (21, 12, 'S01E02.mkv', 1)`)
	e.exec(`INSERT INTO link (id, path, target, content_file_id, title_id, state, created_at, updated_at)
		VALUES (51, '/anime/Delta/S01E02.mkv', '/mnt/b/S01E02.mkv', 21, 4, 'active', 0, 0)`)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (51, 4)`)
	e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (51, 42)`)
	key := e.apiKey()
	var l EpisodeList
	if code := e.getKey(APIBase+"/titles/4/episodes", key, &l); code != 200 {
		t.Fatalf("episodes: %d", code)
	}
	got := map[int64]string{}
	for _, ep := range l.Episodes {
		got[ep.ID] = ep.Versions["anime-tv"].InfoHash
	}
	for _, eid := range []int64{41, 42} {
		var d EpisodeDetail
		if code := e.getKey(fmt.Sprintf("%s/titles/4/episodes/%d", APIBase, eid), key, &d); code != 200 || len(d.Versions) != 1 ||
			d.Versions[0].File == nil {
			t.Fatalf("episode %d window: %d %+v", eid, code, d)
		}
		if w := d.Versions[0].File.InfoHash; w == "" || got[eid] != w {
			t.Errorf("episode %d: cell hash %q, window %q", eid, got[eid], w)
		}
	}
	r := httptest.NewRequest("GET", APIBase+"/titles/4/episodes", nil)
	r.Header.Set(auth.APIKeyHeader, key)
	rec := httptest.NewRecorder()
	e.userMux().ServeHTTP(rec, r)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `"info_hash"`) {
		t.Errorf("a caller without see_files: %d %s", rec.Code, rec.Body.String())
	}
}

// TestStatusWords: one rule for a title's status word (titleAvailabilitySQL), the same in the rows, the filters, the
// facets and the title page. Available means a file is held, whatever its quality against the cutoff; a gap in a
// library that is not monitored is no gap, for a series as for a movie.
func TestStatusWords(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()
	e.exec(`INSERT INTO title (id, kind, name, year, created_at, updated_at) VALUES
		(10, 'movie', 'M Partial', 2020, 0, 0), (11, 'movie', 'M Off Gap', 2020, 0, 0), (12, 'movie', 'M Missing', 2020, 0, 0),
		(13, 'movie', 'M Upcoming', 2027, 0, 0), (14, 'movie', 'M Digital', 2026, 0, 0), (15, 'movie', 'M Unmonitored', 2020, 0, 0),
		(20, 'series', 'S Full', 2020, 0, 0), (21, 'series', 'S Gap', 2020, 0, 0), (22, 'series', 'S Off Gap', 2020, 0, 0),
		(23, 'series', 'S Not Aired', 2027, 0, 0), (24, 'series', 'S None', 2020, 0, 0)`)
	e.exec(`INSERT INTO title_meta (title_id, release_date, status, attempted_at, refresh_after) VALUES
		(12, '2020-01-01', '', 0, 0), (13, '2027-01-01', '', 0, 0), (14, '2026-01-01', '', 0, 0),
		(23, '2027-02-01', 'In Production', 0, 0)`)
	// M Digital: TMDb's cinema release has passed, its canonical digital release is still to come.
	e.exec(`INSERT INTO title_release_date (title_id, source, country, type, date) VALUES (14, 'tmdb-art', '', 'digital', '2026-12-01')`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, state, monitored, created_at, updated_at) VALUES
		(101, 10, 'movie-remux', 'filled', 1, 0, 0), (102, 10, 'movie-web', 'wanted', 1, 0, 0),
		(111, 11, 'movie-remux', 'filled', 1, 0, 0), (112, 11, 'movie-web', 'wanted', 0, 0, 0),
		(121, 12, 'movie-web', 'wanted', 1, 0, 0), (131, 13, 'movie-web', 'wanted', 1, 0, 0),
		(141, 14, 'movie-web', 'wanted', 1, 0, 0), (151, 15, 'movie-web', 'wanted', 0, 0, 0),
		(201, 20, 'tv-2160p', 'filled', 1, 0, 0), (211, 21, 'tv-2160p', 'wanted', 1, 0, 0),
		(221, 22, 'tv-2160p', 'filled', 1, 0, 0), (222, 22, 'anime-tv', 'wanted', 0, 0, 0),
		(231, 23, 'tv-2160p', 'wanted', 1, 0, 0)`)
	e.exec(`INSERT INTO link (id, path, target, title_id, quality, file_metadata, created_at, updated_at) VALUES
		(7901, '/m/10.mkv', '/z/10.mkv', 10, 'WEBDL-1080p', '{"Size":1}', 0, 0),
		(7902, '/m/11.mkv', '/z/11.mkv', 11, 'WEBDL-1080p', '{"Size":1}', 0, 0)`)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (7901, 101), (7902, 111)`)
	// Episodes: two aired and one to come for each series; Not Aired has only one to come.
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, air_date, created_at, updated_at) VALUES
		(2001, 20, 1, 1, 'a', '2026-01-01', 0, 0), (2002, 20, 1, 2, 'b', '2026-01-08', 0, 0), (2003, 20, 1, 3, 'c', '2026-12-01', 0, 0),
		(2101, 21, 1, 1, 'a', '2026-01-01', 0, 0), (2102, 21, 1, 2, 'b', '2026-01-08', 0, 0),
		(2201, 22, 1, 1, 'a', '2026-01-01', 0, 0), (2202, 22, 1, 2, 'b', '2026-01-08', 0, 0),
		(2301, 23, 1, 1, 'a', '2027-02-01', 0, 0)`)
	airAtMidnight(e.exec)
	// S Tonight premieres today, later than now: its air instant is known and still to come.
	e.exec(`INSERT INTO title (id, kind, name, year, created_at, updated_at) VALUES (25, 'series', 'S Tonight', 2026, 0, 0)`)
	e.exec(`INSERT INTO title_meta (title_id, release_date, status, attempted_at, refresh_after) VALUES (25, '2026-10-26', 'Returning Series', 0, 0)`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, state, monitored, created_at, updated_at) VALUES (251, 25, 'tv-2160p', 'wanted', 1, 0, 0)`)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, air_date, air_date_utc_at, created_at, updated_at) VALUES
		(2501, 25, 1, 1, 'a', '2026-10-26', ?, 0, 0)`, store.Millis(testNow.Add(3*time.Hour)))
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, state, updated_at) VALUES
		(201, 2001, 'filled', 0), (201, 2002, 'filled', 0), (201, 2003, 'wanted', 0),
		(211, 2101, 'filled', 0), (211, 2102, 'wanted', 0),
		(221, 2201, 'filled', 0), (221, 2202, 'filled', 0), (222, 2201, 'wanted', 0), (222, 2202, 'wanted', 0),
		(231, 2301, 'wanted', 0)`)
	e.exec(`INSERT INTO link (id, path, target, title_id, quality, file_metadata, created_at, updated_at) VALUES
		(7920, '/t/20.mkv', '/z/20.mkv', 20, 'WEBDL-2160p', '{"Size":1}', 0, 0),
		(7921, '/t/21.mkv', '/z/21.mkv', 21, 'WEBDL-2160p', '{"Size":1}', 0, 0),
		(7922, '/t/22.mkv', '/z/22.mkv', 22, 'WEBDL-2160p', '{"Size":1}', 0, 0)`)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (7920, 201), (7921, 211), (7922, 221)`)
	e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (7920, 2001), (7920, 2002), (7921, 2101),
		(7922, 2201), (7922, 2202)`)
	// M Announced has no date at all and TMDb says Post Production: Upcoming, date unknown. M Undated has no date
	// and no such status: Missing.
	// M Dated Elsewhere is announced but has a (non-canonical) dated release: not undated, so Missing. M No Meta was
	// just added: no metadata, no date: Upcoming, date unknown. M Release Date Only is refreshed and announced, and
	// keeps no release date rows (its primary country has none), but TMDb's release date dates it: Missing.
	e.exec(`INSERT INTO title (id, kind, name, year, created_at, updated_at) VALUES (16, 'movie', 'M Announced', NULL, 0, 0),
		(17, 'movie', 'M Undated', NULL, 0, 0), (18, 'movie', 'M Dated Elsewhere', NULL, 0, 0), (19, 'movie', 'M No Meta', NULL, 0, 0),
		(60, 'movie', 'M Release Date Only', NULL, 0, 0)`)
	e.exec(`INSERT INTO title_meta (title_id, release_date, status, movie_dates_at, attempted_at, refresh_after) VALUES
		(16, '', 'Post Production', NULL, 0, 0), (17, '', 'Released', NULL, 0, 0), (18, '', 'announced', NULL, 0, 0),
		(60, '2020-05-01', 'announced', 1, 0, 0)`)
	e.exec(`INSERT INTO title_release_date (title_id, source, country, type, date) VALUES (18, 'tmdb', 'FR', 'theatrical', '2020-01-01')`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, state, monitored, created_at, updated_at) VALUES
		(161, 16, 'movie-web', 'wanted', 1, 0, 0), (171, 17, 'movie-web', 'wanted', 1, 0, 0), (181, 18, 'movie-web', 'wanted', 1, 0, 0),
		(191, 19, 'movie-web', 'wanted', 1, 0, 0), (601, 60, 'movie-web', 'wanted', 1, 0, 0)`)
	want := map[string]string{
		"M Release Date Only": "missing", "M Announced": "upcoming", "M Undated": "missing", "M Dated Elsewhere": "missing", "M No Meta": "upcoming",
		"M Partial": "partial", "M Off Gap": "available", "M Missing": "missing", "M Upcoming": "upcoming",
		"M Digital": "upcoming", "M Unmonitored": "unmonitored", "S Full": "available", "S Gap": "partial",
		"S Off Gap": "available", "S Not Aired": "upcoming", "S None": "none", "S Tonight": "upcoming",
		// newEnv's: Alpha held in one library and wanted in another, Beta below its cutoff, Gamma in none.
		"Alpha": "partial", "Beta": "available", "Gamma": "none",
	}
	var p TitlePage
	e.getKey(APIBase+"/titles?limit=500", key, &p)
	for _, it := range p.Items {
		if it.Availability != want[it.Name] {
			t.Errorf("%s: row %q, want %q", it.Name, it.Availability, want[it.Name])
		}
		var d TitleDetail
		e.getKey(APIBase+"/titles/"+itoa(it.ID), key, &d)
		if d.Availability != it.Availability {
			t.Errorf("%s: title page %q, row %q", it.Name, d.Availability, it.Availability)
		}
		// S Off Gap's unmonitored copy counts no missing episode, on the row's coverage and the title page.
		if it.Name == "S Off Gap" {
			for _, v := range it.Versions {
				if v.Coverage == nil || v.Coverage.Missing != 0 {
					t.Errorf("S Off Gap %s coverage: %+v", v.Key, v.Coverage)
				}
			}
			for _, v := range d.Versions {
				if v.Episodes == nil || v.Episodes.Missing != 0 {
					t.Errorf("S Off Gap %s episodes: %+v", v.Key, v.Episodes)
				}
			}
			var l EpisodeList
			e.getKey(APIBase+"/titles/22/episodes", key, &l)
			for _, s := range l.Seasons {
				if c := s.Versions["anime-tv"]; c.Missing != 0 {
					t.Errorf("S Off Gap season %d unmonitored copy: %+v", s.Season, c)
				}
			}
		}
	}
	if len(p.Items) != len(want) {
		t.Fatalf("%d rows, want %d", len(p.Items), len(want))
	}
	// A household account (no "see files") keeps watchability: S Off Gap's unmonitored copy still lacks two aired
	// episodes, on the title page and the season counts.
	uid := e.account("household", auth.RoleUser, auth.StatusActive, true)
	ukey, _, err := e.auth.CreateKey(context.Background(), "k-household", uid)
	if err != nil {
		t.Fatal(err)
	}
	var hd TitleDetail
	e.getKey(APIBase+"/titles/22", ukey, &hd)
	for _, v := range hd.Versions {
		if want := map[string]int{"tv-2160p": 0, "anime-tv": 2}[v.Key]; v.Episodes == nil || v.Episodes.Missing != want {
			t.Errorf("household S Off Gap %s episodes: %+v, want %d missing", v.Key, v.Episodes, want)
		}
	}
	var hl EpisodeList
	e.getKey(APIBase+"/titles/22/episodes", ukey, &hl)
	for _, s := range hl.Seasons {
		if c := s.Versions["anime-tv"]; s.Season == 1 && c.Missing != 2 {
			t.Errorf("household S Off Gap season %d unmonitored copy: %+v", s.Season, c)
		}
	}
	// Each filter lists exactly the titles with that word, and its facet counts them; the facets add up.
	for _, scope := range []string{"kind=movie", "kind=series"} {
		var f TitleFacets
		e.getKey(APIBase+"/titles/facets?"+scope, key, &f)
		sum := 0
		for _, av := range titleAvailabilities {
			var p TitlePage
			e.getKey(APIBase+"/titles?availability="+av+"&limit=500&"+scope, key, &p)
			for _, it := range p.Items {
				if want[it.Name] != av {
					t.Errorf("%s availability=%s lists %s (%s)", scope, av, it.Name, want[it.Name])
				}
			}
			if p.Total != f.Availability[av] {
				t.Errorf("%s availability=%s: %d titles, facet %d", scope, av, p.Total, f.Availability[av])
			}
			sum += f.Availability[av]
		}
		if sum != f.Total {
			t.Errorf("%s: facets add up to %d of %d", scope, sum, f.Total)
		}
	}
}

// TestEpisodeScoreEvidence: an episode's score shows only when the link stored scoring evidence: formats (an empty
// list too, so a calculated 0 stays 0) or a non-zero score; a stored 0 without formats shows none, in the Episodes
// list and the episode window.
func TestEpisodeScoreEvidence(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedLibrary()
	key := e.apiKey()
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, air_date, created_at, updated_at) VALUES
		(45, 4, 1, 4, 'Four', '2026-01-22', 0, 0), (46, 4, 1, 5, 'Five', '2026-01-29', 0, 0)`)
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, state, updated_at) VALUES (4, 45, 'filled', 0), (4, 46, 'filled', 0),
		(4, 43, 'filled', 0) ON CONFLICT DO UPDATE SET state = 'filled'`)
	e.exec(`INSERT INTO link (id, path, target, title_id, release_title, quality, score, file_metadata, state, created_at, updated_at) VALUES
		(71, '/a/S01E03.mkv', '/m/3', 4, 'Delta.S01E03', 'WEBDL-1080p', 0, '{"Score":0,"Formats":[]}', 'active', 0, 0),
		(72, '/a/S01E04.mkv', '/m/4', 4, 'Delta.S01E04', 'WEBDL-1080p', 0, '{"Score":0}', 'active', 0, 0),
		(73, '/a/S01E05.mkv', '/m/5', 4, 'Delta.S01E05', 'WEBDL-1080p', 250, '{"Formats":[{"name":"x","score":250}]}', 'active', 0, 0)`)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (71, 4), (72, 4), (73, 4)`)
	e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (71, 43), (72, 45), (73, 46)`)
	want := map[int64]string{43: "0", 45: "none", 46: "250"}
	score := func(s *int64) string {
		if s == nil {
			return "none"
		}
		return itoa(*s)
	}
	var l EpisodeList
	e.getKey(APIBase+"/titles/4/episodes", key, &l)
	for _, ep := range l.Episodes {
		w, ok := want[ep.ID]
		if !ok {
			continue
		}
		if got := score(ep.Versions["anime-tv"].Score); got != w {
			t.Errorf("episode %d list score %s, want %s", ep.ID, got, w)
		}
		var d EpisodeDetail
		e.getKey(APIBase+"/titles/4/episodes/"+itoa(ep.ID), key, &d)
		if len(d.Versions) != 1 || d.Versions[0].File == nil || score(d.Versions[0].File.Score) != w {
			t.Errorf("episode %d window: %+v", ep.ID, d.Versions)
		}
		if !d.Versions[0].LibraryMonitored {
			t.Errorf("episode %d window: library monitored false", ep.ID)
		}
	}
}

// TestReleasedEarliestDate: a film's general release (the Released cell, its sort, About) is its earliest stored
// cinema date of any kind or TMDb's date, so a 2026 wide re-release of a 1988 film reads and sorts as 1988; a film
// with only TMDb's date keeps it; a series keeps its first air date; in_cinemas and release_date keep their meaning.
func TestReleasedEarliestDate(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedLibrary()
	key := e.apiKey()
	now := store.Millis(testNow)
	e.exec(`INSERT INTO title (id, kind, name, year, tmdb_id, monitored, created_at, updated_at) VALUES
		(60, 'movie', 'Akira', 1988, 660, 1, ?, ?), (61, 'movie', 'Plain', 2010, 661, 1, ?, ?)`, now, now, now, now)
	// Akira as on live: the release-date choice (in_cinemas) and TMDb's date are the US wide re-release.
	e.exec(`INSERT INTO title_meta (title_id, release_date, movie_dates_at, attempted_at, refresh_after) VALUES
		(60, '2026-09-04', 1, 1, 1), (61, '2010-02-02', NULL, 1, 1), (4, '2026-01-01', NULL, 1, 1)`)
	e.exec(`INSERT INTO title_release_date (title_id, source, country, type, date) VALUES
		(60, 'tmdb-art', '', 'theatrical', '1988-07-16'), (60, 'tmdb', 'US', 'theatrical_limited', '1989-12-25'),
		(60, 'tmdb', 'US', 'theatrical', '2026-09-04'), (60, 'tmdb', 'US', 'digital', '2027-01-01')`)
	var p TitlePage
	if code := e.getKey(APIBase+"/titles?sort=release&order=asc", key, &p); code != 200 {
		t.Fatalf("titles: %d", code)
	}
	got := map[string]TitleRow{}
	for _, it := range p.Items {
		got[it.Name] = it
	}
	if r := got["Akira"]; r.Released != "1988-07-16" || r.ReleaseDate != "2026-09-04" || r.InCinemas != "2026-09-04" {
		t.Errorf("Akira released %q, release_date %q, in_cinemas %q", r.Released, r.ReleaseDate, r.InCinemas)
	}
	if r := got["Plain"]; r.Released != "2010-02-02" {
		t.Errorf("Plain released %q, want TMDb's date", r.Released)
	}
	if r := got["Delta"]; r.Released != "2026-01-01" {
		t.Errorf("Delta (series) released %q, want its first air date", r.Released)
	}
	if n := names(p); !strings.HasPrefix(n, "Akira,Plain,Alpha,Beta,Delta") {
		t.Errorf("sort=release asc: %s", n)
	}
	var d TitleDetail
	if e.getKey(APIBase+"/titles/60", key, &d); d.Released != "1988-07-16" || !d.ReleasedCinema {
		t.Errorf("title page released %q, from a cinema date %v", d.Released, d.ReleasedCinema)
	}
	if got["Plain"].ReleasedCinema || got["Delta"].ReleasedCinema {
		t.Errorf("released_cinema without a cinema date: Plain %v, Delta %v", got["Plain"].ReleasedCinema, got["Delta"].ReleasedCinema)
	}
}

// TestReleasedCinema: released_cinema says a stored cinema date supplied the release day: true for a limited opening
// before the wide date, false for a digital-only film whose TMDb date is its release day.
func TestReleasedCinema(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedLibrary()
	key := e.apiKey()
	now := store.Millis(testNow)
	e.exec(`INSERT INTO title (id, kind, name, year, tmdb_id, monitored, created_at, updated_at) VALUES
		(62, 'movie', 'Limited first', 2027, 662, 1, ?, ?), (63, 'movie', 'Digital only', 2027, 663, 1, ?, ?)`, now, now, now, now)
	e.exec(`INSERT INTO title_meta (title_id, release_date, movie_dates_at, attempted_at, refresh_after) VALUES
		(62, '2027-02-01', 1, 1, 1), (63, '2027-03-01', 1, 1, 1)`)
	e.exec(`INSERT INTO title_release_date (title_id, source, country, type, date) VALUES
		(62, 'tmdb', 'US', 'theatrical_limited', '2027-01-10'), (62, 'tmdb', 'US', 'theatrical', '2027-02-01'),
		(63, 'tmdb', 'US', 'digital', '2027-03-01')`)
	for id, want := range map[int]struct {
		day    string
		cinema bool
	}{62: {"2027-01-10", true}, 63: {"2027-03-01", false}} {
		var d TitleDetail
		if code := e.getKey(fmt.Sprintf("%s/titles/%d", APIBase, id), key, &d); code != 200 {
			t.Fatalf("title %d: %d", id, code)
		}
		if d.Released != want.day || d.ReleasedCinema != want.cinema {
			t.Errorf("title %d: released %q cinema %v, want %q %v", id, d.Released, d.ReleasedCinema, want.day, want.cinema)
		}
	}
}

// TestLacksSameKind: Lacks a library (without=) counts and lists only titles of that library's media kind, so the
// mixed Anime list's "Lacks Anime movies" holds no series; the Movies and Series lists return what they did.
func TestLacksSameKind(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.seedLibrary()
	key := e.apiKey()
	now := store.Millis(testNow)
	e.exec(`INSERT INTO title (id, kind, name, year, tmdb_id, anime, monitored, created_at, updated_at) VALUES
		(50, 'movie', 'Epsilon', 2020, 650, 1, 1, ?, ?)`, now, now)
	e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at) VALUES
		(50, 50, 'anime-movie', 1, 'wanted', ?, ?)`, now, now)
	for q, want := range map[string]string{
		"anime=true":                                "Delta,Epsilon",
		"anime=true&without=anime-movie":            "Epsilon", // not Delta, a series
		"anime=true&without=anime-tv":               "",        // not Epsilon, a movie
		"kind=movie&without=movie-web":              "Alpha,Epsilon",
		"kind=series&without=tv-2160p":              "Delta,Gamma",
		"kind=movie&anime=true&without=anime-movie": "Epsilon",
	} {
		var p TitlePage
		if code := e.getKey(APIBase+"/titles?"+q, key, &p); code != 200 {
			t.Errorf("%s: %d", q, code)
			continue
		}
		if got := names(p); got != want {
			t.Errorf("%s: %q, want %q", q, got, want)
		}
	}
	lacks := func(scope string) map[string]int {
		var f TitleFacets
		if code := e.getKey(APIBase+"/titles/facets?"+scope, key, &f); code != 200 {
			t.Fatalf("facets %s: %d", scope, code)
		}
		out := map[string]int{}
		for _, v := range f.Versions {
			out[v.Key] = v.Lacks
		}
		return out
	}
	if got := lacks("anime=true"); got["anime-movie"] != 1 || got["anime-tv"] != 0 {
		t.Errorf("anime lacks %v, want anime-movie 1 (Epsilon), anime-tv 0", got)
	}
	if got := lacks("kind=movie"); got["movie-web"] != 2 || got["movie-remux"] != 2 {
		t.Errorf("movie lacks %v, want movie-web 2 (Alpha, Epsilon), movie-remux 2 (Beta, Epsilon)", got)
	}
}
