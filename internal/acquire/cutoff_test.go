package acquire

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/demo"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// router.wanted.cutoff_unmet picks the files the wanted loop searches an upgrade for. quality (the default) is the
// arrs' Cutoff Unmet search (Radarr MovieCutoffService, Sonarr EpisodeCutoffService): a file below the profile's
// quality cutoff. score takes every file below the full cutoff (state filled), also one at the cutoff quality but
// below the cutoff score. off takes none. Missing items are searched the same in every mode, and the states keep the
// full cutoff.
//
// Fixture: movie 3 holds a WEBDL-2160p file (the cutoff quality, score 0 of cutoff_score 100), movie 4 a WEBDL-1080p
// file; s01e02 a WEBDL-2160p file, s01e03 a WEBDL-1080p file. All four are filled.
func TestWantedCutoffMode(t *testing.T) {
	t.Parallel()
	const missing = "101:movie 102:movie 201:s00e01 201:s01e01 201:s02e01"
	for _, tc := range []struct {
		mode policy.CutoffMode
		want string
	}{
		{"", missing + " 104:movie 201:s01e03"}, // the default is quality
		{policy.CutoffQuality, missing + " 104:movie 201:s01e03"},
		{policy.CutoffScore, missing + " 103:movie 104:movie 201:s01"},
		{policy.CutoffOff, missing},
	} {
		e := newEnv(t, true, &policy.Wanted{PerHour: 100, CutoffUnmet: tc.mode})
		cutoffFixture(t, e)
		sum, err := e.a.Round(e.ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := e.runs(); got != sortWords(tc.want) {
			t.Errorf("cutoff_unmet %q: searched %s, want %s (%+v)", tc.mode, got, sortWords(tc.want), sum)
		}
	}
}

// The selection alone: a quality cutoff search over the movie and series versions, and the full cutoff the states
// keep (decide.FileCutoffNotMet: every file here is below it).
func TestQualityCutoffSelection(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	cutoffFixture(t, e)
	names := func(sel Selection) string {
		t.Helper()
		sel.Versions = []string{"movie", "series"}
		ts, err := e.a.targets(e.ctx, sel)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, x := range ts {
			got = append(got, x.key+":"+x.name)
		}
		return strings.Join(got, " ")
	}
	if got := names(Selection{Only: OnlyCutoff, QualityCutoff: true}); got != "movie:movie series:s01e03" {
		t.Errorf("quality cutoff: %s", got)
	}
	if got := names(Selection{Only: OnlyCutoff}); got != "movie:movie movie:movie series:s01" {
		t.Errorf("full cutoff: %s", got)
	}
	if got := names(Selection{Only: OnlyMissing, QualityCutoff: true}); got != "movie:movie movie:movie series:s00e01 series:s01e01 series:s02e01" {
		t.Errorf("missing: %s", got)
	}
	for _, vid := range []int64{103, 104} {
		f, err := decide.LoadExisting(e.ctx, e.st.Reader(), vid)
		if err != nil || f == nil {
			t.Fatal(f, err)
		}
		notMet, err := e.a.eng.FileCutoffNotMet(&decide.Title{}, decide.Version{ID: vid, Key: "movie"}, f)
		if err != nil || !notMet {
			t.Errorf("version %d: full cutoff not met = %v, %v; want true", vid, notMet, err)
		}
	}
	// Without a decision engine the profile is unknown: the state alone decides, as before.
	e.a.eng = nil
	if got := names(Selection{Only: OnlyCutoff, QualityCutoff: true}); got != "movie:movie movie:movie series:s01" {
		t.Errorf("no engine: %s", got)
	}
}

// cutoffFixture gives the movie and series versions a profile with a quality and a score cutoff, an engine, and the
// files of TestWantedCutoffMode.
func cutoffFixture(t *testing.T, e *env) {
	t.Helper()
	e.pol.Profiles = map[string]*policy.Profile{"p": {Qualities: []policy.QualityItem{{Name: "WEBDL-2160p"},
		{Name: "WEBDL-1080p"}}, Cutoff: "WEBDL-2160p", CutoffScore: 100}}
	e.pol.Versions["movie"].Profile = "p"
	e.pol.Versions["series"].Profile = "p"
	eng, err := decide.New(e.pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e.a.eng = eng
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, year, created_at, updated_at) VALUES (4, 'movie', 'Four', 2024, 0, 0)`,
		`INSERT INTO title_release_date (title_id, source, type, date) VALUES (4, 'fixture', 'digital', '2024-01-01')`,
		`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (104, 4, 'movie', 'filled', 0, 0)`,
		`INSERT INTO link (id, path, target, title_id, release_title, quality, file_metadata, created_at, updated_at) VALUES
			(9001, '/m/3.mkv', '/z/m/3.mkv', 3, 'Three.2024.2160p.WEB-DL-GRP', 'WEBDL-2160p', json_object('Quality', 'WEBDL-2160p', 'Revision', 1, 'SceneName', 'Three.2024.2160p.WEB-DL-GRP'), 0, 0),
			(9002, '/m/4.mkv', '/z/m/4.mkv', 4, 'Four.2024.1080p.WEB-DL-GRP', 'WEBDL-1080p', json_object('Quality', 'WEBDL-1080p', 'Revision', 1, 'SceneName', 'Four.2024.1080p.WEB-DL-GRP'), 0, 0),
			(9003, '/s/e2.mkv', '/z/s/e2.mkv', 10, 'Show.S01E02.2160p.WEB-DL-GRP', 'WEBDL-2160p', json_object('Quality', 'WEBDL-2160p', 'Revision', 1, 'SceneName', 'Show.S01E02.2160p.WEB-DL-GRP'), 0, 0),
			(9004, '/s/e3.mkv', '/z/s/e3.mkv', 10, 'Show.S01E03.1080p.WEB-DL-GRP', 'WEBDL-1080p', json_object('Quality', 'WEBDL-1080p', 'Revision', 1, 'SceneName', 'Show.S01E03.1080p.WEB-DL-GRP'), 0, 0)`,
		`INSERT INTO link_version (link_id, version_id) VALUES (9001, 103), (9002, 104), (9003, 201), (9004, 201)`,
		`INSERT INTO link_episode (link_id, episode_id) VALUES (9003, 1002), (9004, 1003)`,
		`UPDATE episode_version_state SET state = 'filled' WHERE version_id = 201 AND episode_id = 1003`,
	} {
		if _, err := e.st.Writer().ExecContext(e.ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	e.storeCutoffs()
}

// storeCutoffs stores every version's below-the-quality-cutoff flags (decide.StoreQualityCutoff), as the library
// does after its writes and when the SeaDex standing changes: the fixtures write files and standing by hand.
func (e *env) storeCutoffs() {
	e.t.Helper()
	rows, err := e.st.Reader().QueryContext(e.ctx, `SELECT id, title_id, policy_key FROM version ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	type v struct {
		id, title int64
		key       string
	}
	var vs []v
	for rows.Next() {
		var x v
		if err := rows.Scan(&x.id, &x.title, &x.key); err != nil {
			e.t.Fatal(err)
		}
		vs = append(vs, x)
	}
	rows.Close()
	for _, x := range vs {
		if e.pol.Versions[x.key] == nil {
			continue
		}
		if err := e.a.eng.StoreQualityCutoff(e.ctx, e.st.Reader(), e.st.Writer(), x.title, x.id, x.key); err != nil {
			e.t.Fatalf("version %d: %v", x.id, err)
		}
	}
}

// runs lists the recorded searches as "version_id:target", sorted.
func (e *env) runs() string {
	e.t.Helper()
	rows, err := e.st.Reader().QueryContext(e.ctx, `SELECT version_id, target FROM acquire_run`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, target string
		if err := rows.Scan(&id, &target); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, id+":"+target)
	}
	return sortWords(strings.Join(out, " "))
}

func sortWords(s string) string {
	w := strings.Fields(s)
	slices.Sort(w)
	return strings.Join(w, " ")
}

// Wanted > Cutoff unmet lists, and its Search all searches, the quality cutoff in every mode but an explicit score;
// off no longer falls through to score (it only stops the regular cutoff rounds). The season row of the mixed-quality
// season goes through roundSelection: in quality mode its asked search is the one episode below the quality cutoff,
// not the season. The stored states keep the full cutoff whatever the mode.
func TestWantedCutoffListFollowsMode(t *testing.T) {
	t.Parallel()
	const quality, score = "4/movie:movie 10/series:s01e03", "3/movie:movie 4/movie:movie 10/series:s01"
	for _, tc := range []struct {
		mode       policy.CutoffMode
		list, runs string
	}{
		{"", quality, "104:movie 201:s01e03"},
		{policy.CutoffQuality, quality, "104:movie 201:s01e03"},
		{policy.CutoffOff, quality, "104:movie 201:s01e03"},
		{policy.CutoffScore, score, "103:movie 104:movie 201:s01"},
	} {
		e := newEnv(t, true, &policy.Wanted{PerHour: 100, Missing: new(false), CutoffUnmet: tc.mode})
		cutoffFixture(t, e)
		l, err := e.a.WantedList(e.ctx, WantedQuery{Only: OnlyCutoff})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range l.Items {
			got = append(got, fmt.Sprintf("%d/%s:%s", r.TitleID, r.Version, strings.Join(r.Targets, ",")))
		}
		if g := sortWords(strings.Join(got, " ")); g != sortWords(tc.list) || l.Total != len(got) {
			t.Errorf("cutoff_unmet %q: lists %s (total %d), want %s", tc.mode, g, l.Total, sortWords(tc.list))
		}
		if _, err := e.a.RequestSearchNowAll(e.ctx, WantedQuery{Only: OnlyCutoff}); err != nil {
			t.Fatal(err)
		}
		if got := e.runs(); got != sortWords(tc.runs) {
			t.Errorf("cutoff_unmet %q: Search all searched %s, want %s", tc.mode, got, sortWords(tc.runs))
		}
		// The mixed season's actual episodes: only the one below the quality cutoff, unless the mode is score.
		want := "[1003]"
		if tc.mode == policy.CutoffScore {
			want = "[1002,1003]"
		}
		if got := e.runEpisodes(201); got != want {
			t.Errorf("cutoff_unmet %q: the season's run searched episodes %s, want %s", tc.mode, got, want)
		}
		var states string
		if err := e.st.Reader().QueryRowContext(e.ctx, `SELECT (SELECT group_concat(state) FROM (SELECT state FROM
			version WHERE id IN (103, 104) ORDER BY id)) || '/' || (SELECT group_concat(state) FROM (SELECT state FROM
			episode_version_state WHERE version_id = 201 AND episode_id IN (1002, 1003) ORDER BY episode_id))`).Scan(&states); err != nil {
			t.Fatal(err)
		}
		if states != "filled,filled/filled,filled" {
			t.Errorf("cutoff_unmet %q: states %s, want the full cutoff's filled", tc.mode, states)
		}
	}
}

// A re-search after a failure is not Wanted's: with cutoff_unmet off it keeps the full cutoff it always had, so a
// failed score-only upgrade (movie 3, at the cutoff quality, below the cutoff score) is searched again, and the mixed
// season's failed season search searches the same two episodes again.
func TestCutoffResearchKeepsFullCutoff(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 100, Missing: new(false), CutoffUnmet: policy.CutoffOff})
	cutoffFixture(t, e)
	e.row(3, "movie", OnlyCutoff, -1, t0, "stalled", 0)
	e.row(10, "series", OnlyCutoff, 1, t0, "stalled", 0)
	if _, err := e.a.Round(e.ctx, []int64{3, 10}); err != nil {
		t.Fatal(err)
	}
	if got := e.runs(); got != "103:movie 201:s01" {
		t.Errorf("re-searched %s, want 103:movie 201:s01", got)
	}
	if got := e.runEpisodes(201); got != "[1002,1003]" {
		t.Errorf("season re-search episodes %s, want [1002,1003]", got)
	}
}

// runEpisodes is the episodes of version id's newest run, as stored.
func (e *env) runEpisodes(versionID int64) string {
	e.t.Helper()
	var eps string
	if err := e.st.Reader().QueryRowContext(e.ctx, `SELECT episodes FROM acquire_run WHERE version_id = ? ORDER BY id DESC
		LIMIT 1`, versionID).Scan(&eps); err != nil {
		e.t.Fatal(err)
	}
	return eps
}

// The stored below-the-quality-cutoff flags (decide.StoreQualityCutoff, as the demo seed and the library store them)
// equal the judgement the cutoff selections made before they were stored (BelowQualityCutoff,
// EpisodesBelowQualityCutoff), for every movie and series library of the demo database, the prefer-SeaDex ones
// too; and the cutoff targets Wanted and the round search, in quality and score mode, are the same either way.
func TestStoredCutoffMatchesJudgement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("..", "..", "examples", "policy"))); err != nil {
		t.Fatal(err)
	}
	overlay, _ := filepath.Glob(filepath.Join("..", "..", "examples", "demo", "*.yaml"))
	for _, f := range overlay {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(f)), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pol, err := policy.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := demo.Seed(ctx, st, demo.Options{Policy: pol, Seed: 1, Now: now, Logger: log}); err != nil {
		t.Fatal(err)
	}
	// Every library searched, Watch only ones too, so each is judged.
	for _, vp := range pol.Versions {
		vp.Mode = policy.ModeActive
	}
	eng, err := decide.New(pol, decide.Options{TrashRoot: filepath.Join("..", "demo", "formats")})
	if err != nil {
		t.Fatal(err)
	}
	a := &Acquirer{st: st, pol: pol, eng: eng, set: pol.WantedSettings(), log: log, now: func() time.Time { return now }}
	rows, err := st.Reader().QueryContext(ctx, `SELECT v.id, v.title_id, v.policy_key, v.below_quality_cutoff
		FROM version v ORDER BY v.id`)
	if err != nil {
		t.Fatal(err)
	}
	type version struct {
		id, title int64
		key       string
		below     bool
	}
	var vs []version
	for rows.Next() {
		var v version
		if err := rows.Scan(&v.id, &v.title, &v.key, &v.below); err != nil {
			t.Fatal(err)
		}
		vs = append(vs, v)
	}
	rows.Close()
	judged := map[string]int{}
	for _, v := range vs {
		vp := pol.Versions[v.key]
		if vp == nil {
			continue
		}
		kind := string(vp.Kind)
		if vp.PreferSeaDex {
			kind += " seadex"
		}
		if vp.Kind != policy.KindSeries {
			want, err := a.BelowQualityCutoff(ctx, v.key, v.id, v.title)
			if err != nil {
				t.Fatal(err)
			}
			if v.below != want {
				t.Errorf("version %d (%s): stored %v, judged %v", v.id, v.key, v.below, want)
			}
			judged[fmt.Sprintf("%s %v", kind, want)]++
			continue
		}
		want, err := a.EpisodesBelowQualityCutoff(ctx, v.key, v.id, v.title)
		if err != nil {
			t.Fatal(err)
		}
		got := map[int64]bool{}
		er, err := st.Reader().QueryContext(ctx, `SELECT episode_id FROM episode_version_state WHERE version_id = ?
			AND below_quality_cutoff = 1`, v.id)
		if err != nil {
			t.Fatal(err)
		}
		for er.Next() {
			var id int64
			if err := er.Scan(&id); err != nil {
				t.Fatal(err)
			}
			got[id] = true
		}
		er.Close()
		if !maps.Equal(got, want) {
			t.Errorf("version %d (%s): stored %v, judged %v", v.id, v.key, got, want)
		}
		judged[fmt.Sprintf("%s %v", kind, len(want) > 0)]++
	}
	// The demo has files on both sides of the cutoff, in plain and prefer-SeaDex libraries of both kinds.
	for _, k := range []string{"movie true", "movie false", "series true", "series false", "series seadex true",
		"series seadex false", "movie seadex false"} {
		if judged[k] == 0 {
			t.Errorf("no %s library judged (%v)", k, judged)
		}
	}
	var keys []string
	for k := range pol.Versions {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, quality := range []bool{true, false} {
		sel := Selection{Versions: keys, Only: OnlyCutoff, QualityCutoff: quality}
		a.judgeCutoff = false
		stored, err := a.targets(ctx, sel)
		if err != nil {
			t.Fatal(err)
		}
		a.judgeCutoff = true
		before, err := a.targets(ctx, sel)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(stored, before) {
			t.Errorf("quality %v: targets from the flags\n%+v\nwant\n%+v", quality, stored, before)
		}
		if quality && len(stored) == 0 {
			t.Error("no cutoff target")
		}
	}
}
