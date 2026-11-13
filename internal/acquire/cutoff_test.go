package acquire

import (
	"slices"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
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
		`INSERT INTO version_file (id, version_id, title_id, instance, arr_file_id, path, quality, scene_name)
			VALUES (1, 103, 3, 'radarr', 1, '/m/3.mkv', 'WEBDL-2160p', 'Three.2024.2160p.WEB-DL-GRP'),
			(2, 104, 4, 'radarr', 2, '/m/4.mkv', 'WEBDL-1080p', 'Four.2024.1080p.WEB-DL-GRP'),
			(3, 201, 10, 'sonarr', 3, '/s/e2.mkv', 'WEBDL-2160p', 'Show.S01E02.2160p.WEB-DL-GRP'),
			(4, 201, 10, 'sonarr', 4, '/s/e3.mkv', 'WEBDL-1080p', 'Show.S01E03.1080p.WEB-DL-GRP')`,
		`INSERT INTO version_file_episode (version_file_id, episode_id) VALUES (3, 1002), (4, 1003)`,
		`UPDATE episode_version_state SET state = 'filled' WHERE version_id = 201 AND episode_id = 1003`,
	} {
		if _, err := e.st.Writer().ExecContext(e.ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
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
