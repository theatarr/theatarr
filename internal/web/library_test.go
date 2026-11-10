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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
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
	e.exec(`UPDATE version_file SET added_at = ? WHERE arr_file_id = 501`, now-10*day)
	e.exec(`UPDATE version_file SET added_at = ? WHERE arr_file_id = 502`, now-1*day)
	e.exec(`UPDATE arr_history SET version_id = 1`)
	e.exec(`INSERT INTO arr_history (instance, arr_id, event_type, date_at, title_id, version_id, info_hash, source_title,
		quality) VALUES ('radarr_4k_debrid', 2, 'downloadFolderImported', ?, 1, 1, ?, 'Alpha.Old', 'Remux-2160p'),
		('radarr_4k_debrid', 3, 'downloadFolderImported', ?, 1, 1, ?, 'Alpha.2020.2160p.BluRay.REMUX-GRP', 'Remux-2160p')`,
		now-900, hashA, now-500, hashA)
	e.exec(`INSERT INTO content_provider (content_id, provider, torrent_id, seen_at) VALUES (1, 'rd', 'T1', ?)`, now)
	e.exec(`INSERT INTO title_release_date (title_id, source, country, type, date) VALUES
		(1, 'tmdb', 'US', 'digital', '2020-08-01'), (1, 'tmdb', 'US', 'digital', '2026-11-01'),
		(2, 'tmdb', 'US', 'digital', '2026-11-05'),
		(2, 'tmdb', 'FR', 'digital', '2026-10-28')`)
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
	e.exec(`INSERT INTO version_file (id, version_id, title_id, instance, arr_file_id, path, size_bytes, quality, added_at,
		release_group) VALUES (90, 4, 4, 'sonarr_anime_debrid', 601, '/anime/Delta/S01E01.mkv', 1000000000, 'Bluray-1080p', ?, 'SubsPlease'),
		(91, 4, 4, 'sonarr_anime_debrid', 602, '/anime/Delta/S01E02.mkv', 1000000000, 'Bluray-1080p', ?, 'SubsPlease')`,
		now-5*day, now-6*day)
	e.exec(`INSERT INTO version_file_episode (version_file_id, episode_id) VALUES (90, 41), (91, 42)`)
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
	for q, want := range map[string]string{
		"":                             "Alpha,Beta,Delta,Gamma",
		"sort=name&order=desc":         "Gamma,Delta,Beta,Alpha",
		"sort=added":                   "Beta,Delta,Alpha,Gamma",
		"sort=added&order=asc":         "Alpha,Delta,Beta,Gamma",
		"sort=rating":                  "Alpha,Beta,Delta,Gamma",
		"sort=rating&order=asc":        "Beta,Alpha,Delta,Gamma",
		"sort=year":                    "Delta,Beta,Alpha,Gamma",
		"sort=size":                    "Alpha,Beta,Delta,Gamma",
		"sort=release":                 "Delta,Beta,Alpha,Gamma",
		"kind=movie&sort=added":        "Beta,Alpha",
		"anime=true":                   "Delta",
		"anime=false":                  "Alpha,Beta,Gamma",
		"kind=series&anime=false":      "Gamma",
		"genre=Drama":                  "Alpha,Gamma",
		"genre=Comedy,Action":          "Alpha,Beta",
		"genre=Comedy&genre=Action":    "Alpha,Beta",
		"quality=WEBDL-2160p":          "Beta",
		"quality=Bluray-1080p":         "Delta",
		"provider=rd":                  "Alpha",
		"year_from=2021":               "Beta,Delta",
		"year_to=2020":                 "Alpha,Gamma",
		"year_from=2020&year_to=2021":  "Alpha,Beta",
		"monitored=false":              "Gamma",
		"availability=partial":         "Alpha",
		"availability=upgrade":         "Beta,Delta",
		"availability=none":            "Gamma",
		"availability=missing":         "",
		"availability=available":       "",
		"sort=added&limit=2&offset=1":  "Delta,Alpha",
		"without=movie-web&sort=added": "Delta,Alpha,Gamma",
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
	for _, bad := range []string{"sort=x", "order=up", "availability=x", "anime=maybe", "year_from=-1"} {
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
	if v := p.Items[0].Versions[0]; v.Label != "Anime" || v.Files != 2 || v.Quality != "Bluray-1080p" {
		t.Errorf("Delta version: %+v", v)
	}
}

func TestAvailabilityOf(t *testing.T) {
	t.Parallel()
	w, f := versionState{"wanted", false}, versionState{"filled", true}
	part, done := versionState{"wanted", true}, versionState{"cutoff_met", true}
	for want, vs := range map[string][]versionState{
		"none": nil, "missing": {w, w}, "partial": {w, done}, "upgrade": {f, done}, "available": {done, done},
	} {
		if got := availabilityOf(vs); got != want {
			t.Errorf("%v: %s, want %s", vs, got, want)
		}
	}
	// A series part-way: wanted, with some episode files.
	if got := availabilityOf([]versionState{part}); got != "partial" {
		t.Errorf("series part-way: %s", got)
	}
}

func TestVersionLabels(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ key, profile, want string }{
		{"movie-remux", "remux-2160p", "4K Remux"},
		{"movie-web", "web-2160p", "4K WEB"},
		{"tv-2160p", "tv-2160p", "4K"},
		{"anime-tv", "anime-tv", "Anime"},
		{"anime-movie", "anime-remux-1080p-movie", "Anime"},
		{"custom", "", "custom"},
	} {
		if got := labelFor(c.key, c.profile); got != c.want {
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
	if f.Total != 2 || f.Monitored != 2 || f.Availability["partial"] != 1 || f.Availability["upgrade"] != 1 ||
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
		for av, n := range f.Availability {
			var p TitlePage
			e.getKey(APIBase+"/titles?availability="+av+"&"+scope, key, &p)
			if p.Total != n {
				t.Errorf("%s availability=%s: facet %d, titles %d", scope, av, n, p.Total)
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
	e.exec(`INSERT INTO version_file_episode (version_file_id, episode_id) VALUES (91, 44)`)
	var held EpisodeList
	e.getKey(APIBase+"/titles/4/episodes", key, &held)
	for _, s := range held.Seasons {
		if c := s.Versions["anime-tv"]; s.Season == 2 && (s.Aired != 1 || c.Have != 1 || c.Total != 2 || c.Missing != 1) {
			t.Errorf("season 2 with an unaired episode held: aired %d, %+v", s.Aired, c)
		}
	}
	e.exec(`UPDATE episode_version_state SET state = 'wanted' WHERE episode_id = 44`)
	e.exec(`DELETE FROM version_file_episode WHERE episode_id = 44`)
	var d TitleDetail
	if e.getKey(APIBase+"/titles/4", key, &d); d.Versions[0].Episodes == nil ||
		*d.Versions[0].Episodes != (EpisodeCounts{Total: 4, Have: 2, Missing: 2}) {
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
		"missing":  {d.Missing, "Delta|1 episode missing|2026-01-15; Alpha|Missing 4K WEB|2020-08-01"},
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
		row(page) != "Alpha|Missing 4K WEB|2020-08-01" {
		t.Errorf("discover/missing page 2: %d %+v", code, page)
	}
	if code := e.getKey(APIBase+"/discover/nope", key, nil); code != 404 {
		t.Errorf("discover/nope: %d", code)
	}
	// The upgrades row lists a title once a better release was found for its file...
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
	// ...or while one is held for a manager's approval.
	e.exec(`INSERT INTO upgrade_hold (title_id, version_id, version_key, info_hash, release_title, candidate, created_at, seen_at)
		VALUES (4, 4, 'anime-tv', 'h', 'Delta.S01.Remux', '{}', ?, ?)`, store.Millis(testNow), store.Millis(testNow))
	if got := upgrades(); got != "Beta|4K WEB: upgrade found|; Delta|Anime: upgrade found|" {
		t.Errorf("upgrades with a hold: %q", got)
	}
	e.exec(`UPDATE upgrade_hold SET state = 'declined'`)
	if got := upgrades(); got != "Beta|4K WEB: upgrade found|" {
		t.Errorf("upgrades after the hold was declined: %q", got)
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
	if got := strings.Join(events, ","); got != "upgraded,imported,grabbed" {
		t.Errorf("history: %s", got)
	}
	if d.Versions[1].Current != nil || d.Availability != "partial" || d.AddedAt == nil {
		t.Errorf("web version: %+v; availability %q", d.Versions[1], d.Availability)
	}
	d = TitleDetail{}
	if e.getKey(APIBase+"/titles/4", key, &d); d.Versions[0].Episodes == nil || *d.Versions[0].Episodes != (EpisodeCounts{Total: 3, Have: 2, Missing: 1}) {
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
		`"custom_format_score"`, `"rd"`}
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
	// An admin sees them.
	var adm map[string]any
	if e.getKey(APIBase+"/auth/me", key, &adm); !strings.Contains(fmt.Sprint(adm["permissions"]), "see_files") {
		t.Errorf("admin me: %v", adm)
	}
}

func TestTitleInfersSharedFileHashOnlyWhenUnique(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "shared-hash.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := store.Millis(testNow)
	sharedTarget := "/debrid/content/Alpha.mkv"
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.Writer().Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES
		(1, 'movie', 'Alpha', ?, ?), (2, 'movie', 'Beta', ?, ?)`, now, now, now, now)
	exec(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES
		(1, 1, 'movie-remux', 'cutoff_met', ?, ?), (2, 1, 'movie-web', 'filled', ?, ?),
		(3, 2, 'movie-web', 'filled', ?, ?)`, now, now, now, now, now, now)
	exec(`INSERT INTO content (id, info_hash, name, state, first_seen_at, last_seen_at) VALUES
		(1, ?, 'Alpha.Release', 'present', ?, ?)`, hashA, now, now)
	exec(`INSERT INTO version_file (id, version_id, title_id, instance, arr_file_id, path, size_bytes, quality,
		content_id) VALUES
		(501, 1, 1, 'radarr_4k_debrid', 501, '/movies/Alpha (2020)/Alpha.mkv', 50000000000, 'Remux-2160p', 1),
		(503, 2, 1, 'radarr_4k_web', 503, '/movies/Alpha Web/Alpha.mkv', 50000000000, 'WEBDL-2160p', NULL)`)
	exec(`INSERT INTO content_file (id, content_id, path, size_bytes) VALUES (1, 1, 'Alpha.mkv', 50000000000)`)
	exec(`INSERT INTO link (id, path, target, content_file_id, title_id, created_at, updated_at) VALUES
		(1, '/movies/Alpha (2020)/Alpha.mkv', ?, 1, 1, ?, ?),
		(2, '/movies/Alpha Web/Alpha.mkv', ?, NULL, 1, ?, ?)`, sharedTarget, now, now, sharedTarget, now, now)
	exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 1), (2, 2)`)

	w := &Web{st: st}
	versions, err := w.versions(ctx, "v.title_id = ?", []any{1}, 0, 0)
	if err != nil || len(versions) != 2 {
		t.Fatalf("title versions: %+v, %v", versions, err)
	}
	versions[1].Current = currentOf(&versions[1], VersionFiles{Files: 1})
	web := versions[1]
	if len(web.Files) != 1 || web.Files[0].InfoHash != hashA || web.Current == nil || web.Current.InfoHash != hashA ||
		len(web.Links) != 1 || web.Links[0].InfoHash != hashA {
		t.Fatalf("shared target did not supply its unique hash: files %+v, current %+v", web.Files, web.Current)
	}

	const hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	exec(`INSERT INTO content (id, info_hash, name, state, first_seen_at, last_seen_at) VALUES
		(2, ?, 'Other.Release', 'present', ?, ?)`, hashB, now, now)
	exec(`INSERT INTO version_file (id, version_id, title_id, instance, arr_file_id, path, size_bytes, quality,
		content_id) VALUES (504, 3, 2, 'radarr_4k_beta', 504, '/movies/Beta/Beta.mkv', 9000000000, 'WEBDL-2160p', 2)`)
	exec(`INSERT INTO content_file (id, content_id, path, size_bytes) VALUES (2, 2, 'Beta.mkv', 9000000000)`)
	exec(`INSERT INTO link (id, path, target, content_file_id, title_id, created_at, updated_at)
		VALUES (3, '/movies/Beta/Beta.mkv', ?, 2, 2, ?, ?)`, sharedTarget, now, now)
	exec(`INSERT INTO link_version (link_id, version_id) VALUES (3, 3)`)
	versions, err = w.versions(ctx, "v.title_id = ?", []any{1}, 0, 0)
	if err != nil || len(versions) != 2 {
		t.Fatalf("title versions with ambiguous target: %+v, %v", versions, err)
	}
	versions[1].Current = currentOf(&versions[1], VersionFiles{Files: 1})
	web = versions[1]
	if web.Files[0].InfoHash != "" || web.Current == nil || web.Current.InfoHash != "" {
		t.Errorf("ambiguous target supplied a hash: files %+v, current %+v", web.Files, web.Current)
	}

	detail := &TitleDetail{Versions: versions}
	redactTitle(detail)
	body, err := json.Marshal(detail)
	if err != nil || strings.Contains(string(body), hashA) || strings.Contains(string(body), hashB) || strings.Contains(string(body), `"info_hash"`) {
		t.Errorf("title hash inference bypassed see_files redaction: %s, %v", body, err)
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

// A library's own name wins over the guessed label; an empty or blank one falls back, and two libraries with the
// same name are told apart by key like any other colliding labels.
func TestVersionLabelName(t *testing.T) {
	t.Parallel()
	w := &Web{pol: &policy.Policy{Versions: map[string]*policy.VersionPolicy{
		"movie-web":   {Profile: "web-2160p", Name: " Films "},
		"movie-remux": {Profile: "remux-2160p", Name: "Films"},
		"tv-2160p":    {Profile: "tv-2160p"},
		"anime-tv":    {Profile: "anime-tv", Name: "  "},
	}}}
	for key, want := range map[string]string{"movie-web": "Films", "tv-2160p": "4K", "anime-tv": "Anime", "gone": "gone"} {
		if got := w.versionLabel(key); got != want {
			t.Errorf("%s: %q, want %q", key, got, want)
		}
	}
	got := w.keyLabels([]string{"movie-web", "movie-remux", "tv-2160p"})
	want := map[string]string{"movie-web": "Films · movie-web", "movie-remux": "Films · movie-remux", "tv-2160p": "4K"}
	if !maps.Equal(got, want) {
		t.Errorf("labels %v, want %v", got, want)
	}
}
