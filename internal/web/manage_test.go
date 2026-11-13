package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/policy"
)

func TestJudgeUpgrade(t *testing.T) {
	t.Parallel()
	up := func(rel string, score int, eps ...string) evCand {
		return evCand{Release: rel, Accepted: true, Upgrade: true, Score: score, UpgradeWhy: "better", Episodes: eps}
	}
	movie := evItem{fileAt: 1000}
	cases := []struct {
		name  string
		it    evItem
		recs  []evRecord
		want  string // the release seen, "" for none
		last  int64
		multi bool
	}{
		{name: "nothing stored", it: movie},
		{name: "a search before the file judged an older file", it: movie,
			recs: []evRecord{{at: 900, search: true, source: "search", cands: []evCand{up("Old.Find", 9000)}}}, last: 900},
		{name: "a search after the file found an upgrade", it: movie,
			recs: []evRecord{{at: 2000, search: true, source: "search", cands: []evCand{up("New.Find", 5000)}}},
			want: "New.Find", last: 2000},
		{name: "accepted but not an upgrade, or an upgrade that was rejected", it: movie,
			recs: []evRecord{{at: 2000, search: true, cands: []evCand{{Release: "Same", Accepted: true, Score: 5000},
				{Release: "Rejected", Upgrade: true, Score: 9000}}}}, last: 2000},
		{name: "a newer search that found nothing stands for the older one", it: movie,
			recs: []evRecord{{at: 2000, search: true, cands: []evCand{up("New.Find", 5000)}}, {at: 3000, search: true}},
			last: 3000},
		{name: "RSS after the newest search adds; RSS before it does not", it: movie,
			recs: []evRecord{{at: 2000, search: true, cands: []evCand{up("Searched", 5000)}}, {at: 3000, search: true},
				{at: 2500, source: "rss", target: "rss", cands: []evCand{up("Early.RSS", 9000)}},
				{at: 3500, source: "rss", target: "rss", cands: []evCand{up("Late.RSS", 6000)}}},
			want: "Late.RSS", last: 3000},
		{name: "the best score wins", it: movie,
			recs: []evRecord{{at: 2000, search: true, cands: []evCand{up("A", 5000), up("B", 7000)}},
				{at: 2100, target: "rss", cands: []evCand{up("C", 6000)}}}, want: "B", last: 2000},
		{name: "a season search covers its episodes", it: evItem{episodeID: 7, season: 1, number: 2, fileAt: 1000},
			recs: []evRecord{{at: 2000, search: true, target: "s01", cands: []evCand{up("S01.Pack", 5000)}}},
			want: "S01.Pack", last: 2000},
		{name: "another season's search does not", it: evItem{episodeID: 7, season: 1, number: 2, fileAt: 1000},
			recs: []evRecord{{at: 2000, search: true, target: "s02", cands: []evCand{up("S02.Pack", 5000)}}}},
		{name: "a candidate for other episodes does not count", it: evItem{episodeID: 7, season: 1, number: 2, fileAt: 1000},
			recs: []evRecord{{at: 2000, search: true, target: "s01", cands: []evCand{up("S01E03", 5000, "S01E03"),
				up("S01E02", 4000, "S01E02")}}}, want: "S01E02", last: 2000},
		{name: "first search wins a timestamp tie even when empty", it: movie,
			recs: []evRecord{{at: 2000, search: true}, {at: 2000, search: true, cands: []evCand{up("Tied", 5000)}}},
			last: 2000},
		{name: "file and search floors are strict for evidence", it: movie,
			recs: []evRecord{{at: 1000, search: true, cands: []evCand{up("At.File", 5000)}},
				{at: 1000, target: "rss", cands: []evCand{up("RSS.At.File", 6000)}}}, last: 1000},
		{name: "RSS at the search timestamp is excluded", it: movie,
			recs: []evRecord{{at: 2000, search: true}, {at: 2000, target: "rss", cands: []evCand{up("Tied.RSS", 5000)}}},
			last: 2000},
		{name: "newer candidate wins a score tie", it: movie,
			recs: []evRecord{{at: 2000, search: true, cands: []evCand{up("Search", 5000)}},
				{at: 2001, target: "rss", cands: []evCand{up("Newer.RSS", 5000)}}}, want: "Newer.RSS", last: 2000},
	}
	for _, c := range cases {
		next, last := judgeUpgrade(c.it, c.recs)
		got := ""
		if next != nil {
			got = next.Release
		}
		if got != c.want || last != c.last {
			t.Errorf("%s: got %q (last search %d), want %q (%d)", c.name, got, last, c.want, c.last)
		}
	}
}

// manageEnv is addEnv plus the auth service, so a user-view Web can share the database.
type manageEnv struct {
	*addEnv
	au *auth.Service
}

func newManageEnv(t *testing.T, mutate func(*policy.Policy)) *manageEnv {
	t.Helper()
	e := newAddEnv(t, mutate)
	au, _, err := auth.New(context.Background(), e.st, auth.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return addNow }})
	if err != nil {
		t.Fatal(err)
	}
	m := &manageEnv{addEnv: e, au: au}
	m.seedManage()
	return m
}

// seedManage: The Show's tv-2160p was imported from sonarr_4k_debrid;
// S01E01 and S01E02 have files below the cutoff; an RSS read upgrades S01E01.
// Alpha's movie-remux has a file below the cutoff, and a search after it found an upgrade.
func (e *manageEnv) seedManage() {
	e.exec(`INSERT INTO version_source (version_id, instance, arr_id) VALUES (10, 'sonarr_4k_debrid', 77)`)
	e.exec(`UPDATE episode SET air_date = '2020-01-01'`)
	airAtMidnight(e.exec)
	e.exec(`UPDATE episode_version_state SET state = 'filled' WHERE episode_id IN (101, 102)`)
	e.exec(`INSERT INTO version_file (id, version_id, title_id, instance, arr_file_id, path, size_bytes, quality,
		custom_format_score, added_at, release_title, release_group, media_info) VALUES
		(900, 10, 10, 'sonarr_4k_debrid', 1, '/tv/Show/S01E01.mkv', 2000000000, 'WEBDL-1080p', 1700, 1000,
		 'The.Show.S01E01.1080p.WEB-DL-GRP', 'GRP', '{"resolution":"1920x1080","videoCodec":"x265","audioCodec":"EAC3"}'),
		(901, 10, 10, 'sonarr_4k_debrid', 2, '/tv/Show/S01E02.mkv', 2000000000, 'WEBDL-1080p', 1700, 1000,
		 'The.Show.S01E02.1080p.WEB-DL-GRP', 'GRP', '{}')`)
	e.exec(`INSERT INTO version_file_episode (version_file_id, episode_id) VALUES (900, 101), (901, 102)`)
	e.exec(`INSERT INTO journal_decision (source, title_id, version_key, target, results, accepted, candidates,
		created_at) VALUES ('rss', 10, 'tv-2160p', 'rss', 1, 1,
		'[{"release":"The.Show.S01E01.2160p.WEB-DL-BETTER","indexer":"dmm","accepted":true,"upgrade":true,
		  "upgrade_reason":"better quality: WEBDL-2160p over WEBDL-1080p","quality":"WEBDL-2160p","score":3400,
		  "episodes":["S01E01"]}]', 5000)`)

	e.exec(`UPDATE version SET state = 'filled' WHERE id = 1`)
	e.exec(`INSERT INTO version_file (id, version_id, title_id, instance, arr_file_id, path, size_bytes, quality,
		custom_format_score, added_at, release_title) VALUES (902, 1, 1, 'radarr_4k_debrid', 3, '/movies/Alpha.mkv',
		9000000000, 'WEBDL-2160p', 1500, 1000, 'Alpha.2020.2160p.WEB-DL-GRP')`)
	e.exec(`INSERT INTO journal_decision (source, title_id, version_key, target, results, accepted, candidates,
		created_at) VALUES ('acquire:9', 1, 'movie-remux', 'movie', 1, 1,
		'[{"release":"Alpha.2020.2160p.BluRay.REMUX-BEST","accepted":true,"upgrade":true,"quality":"Remux-2160p",
		   "score":3500,"upgrade_reason":"better quality: Remux-2160p over WEBDL-2160p"}]', 2000)`)
}

// userMux is the API as a caller without see_files gets it.
func (e *manageEnv) userMux() *http.ServeMux {
	e.t.Helper()
	w, err := New(Config{Store: e.st, Auth: e.au, Policy: e.pol, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return addNow }, UI: testUI, Add: AddConfig{Acquirer: e.acq},
		Perms: func(*auth.Identity) Perms { return PermsOf(auth.PermBrowse | auth.PermRequest) }})
	if err != nil {
		e.t.Fatal(err)
	}
	mux := http.NewServeMux()
	w.Mount(mux)
	return mux
}

func (e *manageEnv) state() string {
	e.t.Helper()
	var b strings.Builder
	rows, err := e.st.Reader().Query(`SELECT 'e', id, monitored, '' FROM episode UNION ALL
		SELECT 's', episode_id * 1000 + version_id, monitored, state FROM episode_version_state UNION ALL
		SELECT 'v', id, monitored, state FROM version ORDER BY 1, 2`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, s string
		var id, m int64
		if err := rows.Scan(&k, &id, &m, &s); err != nil {
			e.t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s%d=%d%s ", k, id, m, s)
	}
	return b.String()
}

func TestUpgradeEvidence(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	var d TitleDetail
	e.call("GET", APIBase+"/titles/10", nil, 200, &d)
	u := d.Versions[0].Upgrade
	if u == nil || u.State != "upgrade_available" || u.Below != 2 || u.Seen != 1 || u.Quality != "WEBDL-1080p" ||
		u.Score == nil || *u.Score != 1700 || u.Next == nil || u.Next.Release != "The.Show.S01E01.2160p.WEB-DL-BETTER" ||
		u.Next.Source != "rss" || !strings.Contains(u.Next.Reason, "better quality") {
		t.Fatalf("series upgrade: %+v next %+v", u, u.Next)
	}
	if c := u.Cutoff; c == nil || c.Quality != "Bluray-2160p Remux" || c.Score == nil || *c.Score != 10000 ||
		c.From != "policy" || c.Profile != "tv-2160p" {
		t.Fatalf("cutoff from the library's profile: %+v", u.Cutoff)
	}
	var m TitleDetail
	e.call("GET", APIBase+"/titles/1", nil, 200, &m)
	mu := m.Versions[0].Upgrade
	if mu == nil || mu.State != "upgrade_available" || mu.Next.Release != "Alpha.2020.2160p.BluRay.REMUX-BEST" ||
		mu.Next.Source != "search" || mu.Cutoff.Quality != "Remux-2160p" || *mu.Cutoff.Score != 10000 ||
		mu.Cutoff.From != "policy" {
		t.Fatalf("movie upgrade: %+v %+v", mu, mu.Cutoff)
	}

	var l EpisodeList
	e.call("GET", APIBase+"/titles/10/episodes", nil, 200, &l)
	cells := map[int64]EpisodeCell{}
	for _, ep := range l.Episodes {
		cells[ep.ID] = ep.Versions["tv-2160p"]
	}
	if !cells[101].UpgradeSeen || cells[102].UpgradeSeen {
		t.Errorf("episode cells: %+v %+v", cells[101], cells[102])
	}
	var s1 SeasonCounts
	for _, s := range l.Seasons {
		if s.Season == 1 {
			s1 = s.Versions["tv-2160p"]
		}
	}
	if s1.BelowCutoff != 2 || s1.UpgradeSeen != 1 || s1.Monitored != 2 || s1.Quality != "WEBDL-1080p" ||
		s1.Score == nil || *s1.Score != 1700 || s1.SizeBytes == nil || *s1.SizeBytes != 4000000000 {
		t.Errorf("season 1: %+v", s1)
	}
	if v := l.Versions[0]; v.Cutoff == nil || v.Cutoff.Quality != "Bluray-2160p Remux" || v.Control == nil ||
		!v.Control.CanEdit {
		t.Errorf("episode list version: %+v", v)
	}

	var ep EpisodeDetail
	e.call("GET", APIBase+"/titles/10/episodes/101", nil, 200, &ep)
	ev := ep.Versions[0]
	if ev.Upgrade.State != "upgrade_available" || ev.File == nil || ev.File.ReleaseTitle != "The.Show.S01E01.1080p.WEB-DL-GRP" ||
		ev.File.Media == nil || ev.File.Media.VideoCodec != "x265" || len(ev.History) != 0 {
		t.Fatalf("episode 101: %+v file %+v history %+v", ev.Upgrade, ev.File, ev.History)
	}
	ep = EpisodeDetail{}
	e.call("GET", APIBase+"/titles/10/episodes/102", nil, 200, &ep)
	if ep.Versions[0].Upgrade.State != "below_cutoff" || ep.Versions[0].Upgrade.Next != nil {
		t.Errorf("episode 102: %+v", ep.Versions[0].Upgrade)
	}

	var page TitlePage
	e.call("GET", APIBase+"/titles?sort=name", nil, 200, &page)
	for _, it := range page.Items {
		if (it.ID == 1 || it.ID == 10) != it.UpgradeSeen {
			t.Errorf("row %s: upgrade_seen %v", it.Name, it.UpgradeSeen)
		}
	}

	// Theatarr's own search of season 1 after the RSS pick, finding nothing better: it stands for everything before it.
	e.exec(`INSERT INTO journal_decision (source, title_id, version_key, target, results, accepted, candidates,
		rejections, created_at) VALUES ('acquire:1', 10, 'tv-2160p', 's01', 3, 1,
		'[{"rank":1,"release":"The.Show.S01.1080p.WEB-DL-GRP","accepted":true,"upgrade":false,"score":1700}]', '{}', 6000)`)
	d = TitleDetail{}
	e.call("GET", APIBase+"/titles/10", nil, 200, &d)
	if u := d.Versions[0].Upgrade; u.State != "below_cutoff" || u.Seen != 0 || u.Next != nil || u.LastSearch == nil {
		t.Errorf("after a search that found nothing better: %+v", u)
	}
	// A file newer than the evidence: the evidence judged the old file.
	e.exec(`UPDATE version_file SET added_at = 9000 WHERE id = 902`)
	m = TitleDetail{}
	e.call("GET", APIBase+"/titles/1", nil, 200, &m)
	if mu := m.Versions[0].Upgrade; mu.State != "below_cutoff" || mu.Next != nil {
		t.Errorf("movie after a newer file: %+v", mu)
	}
}

// shadow versions get no searches from these controls; their monitoring is editable, even when an arr imports them.
func TestManageShadowNoWrites(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	before := e.state()
	var d TitleDetail
	e.call("GET", APIBase+"/titles/10", nil, 200, &d)
	if c := d.Versions[0].Control; c == nil || !c.CanEdit || c.CanSearch || c.EditReason != "" ||
		!strings.Contains(c.SearchReason, "Watch only") {
		t.Fatalf("control: %+v", d.Versions[0].Control)
	}
	var so SearchOutcome
	e.call("POST", APIBase+"/titles/10/seasons/1/search", nil, 200, &so)
	if so.Started || !strings.Contains(so.Message, "Watch only") {
		t.Errorf("season search: %+v", so)
	}
	e.call("POST", APIBase+"/titles/10/episodes/search", map[string]any{"episode_ids": []int64{101, 102}}, 200, &so)
	if so.Started {
		t.Errorf("episode search: %+v", so)
	}
	var r Releases
	e.call("GET", APIBase+"/titles/10/episodes/101/releases?live=1", nil, 200, &r)
	if r.LiveAllowed || !strings.Contains(r.Message, "Watch only") {
		t.Errorf("episode releases: %+v", r)
	}
	if s, i := e.acq.counts(); s != 0 || i != 0 {
		t.Fatalf("shadow controls searched: %d acquisitions, %d interactive", s, i)
	}
	if after := e.state(); after != before {
		t.Fatalf("shadow controls wrote:\n%s\n%s", before, after)
	}
}

func TestManageActive(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, func(p *policy.Policy) {
		p.Versions["tv-2160p"].Mode = policy.ModeActive
		p.Router = &policy.Router{Enabled: true}
	})
	var d TitleDetail
	e.call("GET", APIBase+"/titles/10", nil, 200, &d)
	if c := d.Versions[0].Control; !c.CanEdit || !c.CanSearch || !c.LiveReleases {
		t.Fatalf("active control: %+v", c)
	}
	var out ManageOutcome
	e.call("PUT", APIBase+"/titles/10/seasons/1/monitor", map[string]any{"monitored": false}, 200, &out)
	if out.Episodes != 2 || !slices.Equal(out.Versions, []string{"tv-2160p"}) {
		t.Errorf("season unmonitor: %+v", out)
	}
	var mon int
	e.st.Reader().QueryRow(`SELECT SUM(monitored) FROM episode WHERE season = 1`).Scan(&mon)
	if mon != 0 {
		t.Errorf("episode.monitored not synced: %d", mon)
	}
	e.call("PUT", APIBase+"/titles/10/episodes/monitor", map[string]any{"monitored": true, "episode_ids": []int64{102}}, 200, &out)
	e.st.Reader().QueryRow(`SELECT monitored FROM episode_version_state WHERE episode_id = 102`).Scan(&mon)
	if mon != 1 || out.Episodes != 1 {
		t.Errorf("episode monitor: %d %+v", mon, out)
	}
	e.call("PUT", APIBase+"/titles/10/episodes/monitor", map[string]any{"monitored": true, "episode_ids": []int64{1}}, 400, nil)
	e.call("PUT", APIBase+"/titles/10/seasons/9/monitor", map[string]any{"monitored": true}, 404, nil)
	e.call("PUT", APIBase+"/titles/1/seasons/1/monitor", map[string]any{"monitored": true}, 400, nil)

	var so SearchOutcome
	e.call("POST", APIBase+"/titles/10/seasons/1/search", map[string]any{}, 200, &so)
	e.call("POST", APIBase+"/titles/10/episodes/search", map[string]any{"episode_ids": []int64{101, 102}}, 200, &so)
	calls := searchCalls(e.addEnv)
	if len(calls) != 2 || calls[0].Season == nil || *calls[0].Season != 1 || !slices.Equal(calls[0].Versions, []string{"tv-2160p"}) ||
		!slices.Equal(calls[1].EpisodeIDs, []int64{101, 102}) || !so.Started {
		t.Fatalf("searches: %+v %+v", calls, so)
	}
	var r Releases
	e.call("GET", APIBase+"/titles/10/episodes/101/releases?live=1", nil, 200, &r)
	if r.Source != "live" || r.Target != "s01e01" {
		t.Errorf("live episode releases: %+v", r)
	}
	if _, i := e.acq.counts(); i != 1 {
		t.Errorf("interactive searches: %d", i)
	}
}

// The title page's next search: a movie copy's earliest row on its control, a series' per season on the episodes
// route. A missing row of a film with a file and a regular row whose switch is off are left out; nothing while paused
// or for an unmonitored copy.
func TestManageNextSearch(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, func(p *policy.Policy) {
		p.Versions["tv-2160p"].Mode = policy.ModeActive
		p.Versions["movie-remux"].Mode = policy.ModeActive
		p.Router = &policy.Router{Enabled: true, Wanted: &policy.Wanted{}}
	})
	ms := func(d time.Duration) int64 { return addNow.Add(d).UnixMilli() }
	e.exec(`INSERT INTO search_request (title_id, version_key, reason, season, created_at, next_at, tries, why) VALUES
		(1, 'movie-remux', 'cutoff', -1, 0, ?, 0, NULL), (1, 'movie-remux', 'missing', -1, 0, ?, 0, 'asked'),
		(10, 'tv-2160p', 'cutoff', 1, 0, ?, 0, 'stalled'), (10, 'tv-2160p', 'missing', 1, 0, ?, 0, NULL)`,
		ms(3*time.Hour), ms(-time.Minute), ms(-5*time.Minute), ms(-time.Hour))
	check := func(name string, movieAt *time.Duration, movieWhy string, seasonAt *time.Duration, seasonWhy string) {
		t.Helper()
		var d TitleDetail
		e.call("GET", APIBase+"/titles/1", nil, 200, &d)
		var c *VersionControl
		for _, v := range d.Versions {
			if v.Key == "movie-remux" {
				c = v.Control
			}
		}
		same := func(got *time.Time, want *time.Duration) bool {
			return (got == nil) == (want == nil) && (got == nil || got.Equal(addNow.Add(*want)))
		}
		if c == nil || !same(c.NextSearchAt, movieAt) || c.NextSearchWhy != movieWhy {
			t.Errorf("%s: movie control %+v", name, c)
		}
		var l EpisodeList
		e.call("GET", APIBase+"/titles/10/episodes", nil, 200, &l)
		for _, se := range l.Seasons {
			sc := se.Versions["tv-2160p"]
			if se.Season == 1 && (!same(sc.NextSearchAt, seasonAt) || sc.NextSearchWhy != seasonWhy) ||
				se.Season != 1 && sc.NextSearchAt != nil {
				t.Errorf("%s: season %d %+v", name, se.Season, sc)
			}
		}
	}
	d := func(x time.Duration) *time.Duration { return &x }
	// Season 1 has no missing episode, so only its cutoff row counts.
	check("on", d(3*time.Hour), "", d(-5*time.Minute), "stalled")
	e.pol.Router.Wanted.CutoffUnmet = policy.CutoffOff
	check("upgrades off", nil, "", d(-5*time.Minute), "stalled")
	e.pol.Router.Wanted.CutoffUnmet = ""
	e.exec(`UPDATE version SET monitored = 0`)
	check("unmonitored", nil, "", nil, "")
	e.exec(`UPDATE version SET monitored = 1`)
	e.pol.Router.Enabled = false
	check("paused", nil, "", nil, "")
}

// Users (availability only) get no scores, release names, sizes or controls.
func TestManageNoScoresForUsers(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	mux := e.userMux()
	do := func(method, path string, body any) (int, string) {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		r := httptest.NewRequest(method, path, rd)
		r.Header.Set(auth.APIKeyHeader, e.key)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec.Code, rec.Body.String()
	}
	secret := []string{`"score"`, `"next"`, `"size_bytes"`, `"release_title"`, `"path"`, "BETTER", "REMUX-BEST",
		"WEB-DL-GRP", "4k Preferred", "sonarr_4k_debrid", `"last_search"`, `"journal"`, `"media"`, "10000"}
	for _, p := range []string{"/titles/10", "/titles/1", "/titles/10/episodes", "/titles/10/episodes/101", "/titles",
		"/discover"} {
		code, body := do("GET", APIBase+p, nil)
		if code != 200 {
			t.Errorf("%s: %d", p, code)
			continue
		}
		for _, s := range secret {
			if strings.Contains(body, s) {
				t.Errorf("%s shows %s to a user", p, s)
			}
		}
	}
	// Availability stays (has a file, in which resolution); the upgrade state and the cutoff go.
	if _, body := do("GET", APIBase+"/titles/10", nil); !strings.Contains(body, `"has_file":true`) ||
		!strings.Contains(body, `"quality":"1080p"`) || strings.Contains(body, `"upgrade"`) ||
		strings.Contains(body, "Bluray-2160p Remux") {
		t.Errorf("user: want availability only: %s", body)
	}
	before := e.state()
	for _, c := range []struct{ method, path string }{{"PUT", "/titles/10/seasons/1/monitor"}, {"PUT", "/titles/10/episodes/monitor"},
		{"POST", "/titles/10/seasons/1/search"}, {"POST", "/titles/10/episodes/search"}, {"GET", "/titles/10/episodes/101/releases"}} {
		if code, _ := do(c.method, APIBase+c.path, map[string]any{"monitored": false, "episode_ids": []int64{101}}); code != 403 {
			t.Errorf("%s %s as a user: %d", c.method, c.path, code)
		}
	}
	if e.state() != before {
		t.Fatal("a user changed monitoring")
	}
}
