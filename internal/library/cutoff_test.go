package library

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
)

// scoreFormat gives both profiles a "Grp" format (release title contains GRP) scored score, with cutoff as the
// cutoff score, and rebuilds the engine.
func (e *env) scoreFormat(score, cutoff int) {
	e.t.Helper()
	body := `{"name":"Grp","specifications":[{"name":"g","implementation":"ReleaseTitleSpecification","negate":false,
		"required":true,"fields":{"value":"GRP"}}]}`
	if err := os.WriteFile(filepath.Join(e.dir, "grp.json"), []byte(body), 0o644); err != nil {
		e.t.Fatal(err)
	}
	e.pol.Dir = e.dir
	e.pol.Formats = map[string]*policy.FormatRef{"Grp": {File: "grp.json"}}
	for _, p := range e.pol.Profiles {
		p.Formats = map[string]int{"Grp": score}
		p.CutoffScore = cutoff
	}
	engine, err := decide.New(e.pol, decide.Options{})
	if err != nil {
		e.t.Fatal(err)
	}
	e.lib.engine = engine
}

// An import at the cutoff quality whose format score is below the cutoff score leaves the version filled
// (UpgradableSpecification.CutoffNotMet), so RSS and the cutoff sweep keep looking for a format upgrade.
func TestImportStateCountsFormatCutoff(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		score, cutoff int
		want          string
	}{{"score below cutoff", 100, 10000, "filled"}, {"score at cutoff", 100, 100, "cutoff_met"}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			e.scoreFormat(tc.score, tc.cutoff)
			e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
			e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
			if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
				t.Fatal(err)
			}
			if got := e.one(`SELECT state FROM version WHERE id = 10`); got != tc.want {
				t.Fatalf("movie state=%s, want %s", got, tc.want)
			}
			rel := "Show.S01E01.2160p.BluRay.REMUX.HEVC-GRP"
			e.release(rel, map[string]float64{"Show.S01E01.2160p.BluRay.REMUX.HEVC-GRP.mkv": 2700})
			e.seriesDownload(2, 2, 20, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", rel, "[201]", nil)
			e.exec(`UPDATE download SET quality = 'Bluray-2160p Remux' WHERE id = 2`)
			if _, err := e.lib.Import(e.ctx, 2, false); err != nil {
				t.Fatal(err)
			}
			if got := e.one(`SELECT state || '/' || cutoff_met FROM episode_version_state
				WHERE version_id = 20 AND episode_id = 201`); got != tc.want+"/"+map[bool]string{true: "1", false: "0"}[tc.want == "cutoff_met"] {
				t.Fatalf("episode state=%s, want %s", got, tc.want)
			}
		})
	}
}

// Links imported before format scores counted were judged on quality alone: the first import pass recomputes them once, for active
// versions only, moving only between filled and cutoff_met.
func TestRecomputeLinkStates(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	rel := "Show.S01.2160p.BluRay.REMUX.HEVC-GRP"
	e.release(rel, map[string]float64{"Show.S01E01.2160p.BluRay.REMUX.HEVC-GRP.mkv": 2700,
		"Show.S01E02.2160p.BluRay.REMUX.HEVC-GRP.mkv": 2700})
	e.seriesDownload(2, 2, 20, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", rel, "[201, 202]", nil)
	e.exec(`UPDATE download SET quality = 'Bluray-2160p Remux' WHERE id = 2`)
	for _, id := range []int64{1, 2} {
		if _, err := e.lib.Import(e.ctx, id, false); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.one(`SELECT state FROM version WHERE id = 10`) + e.one(`SELECT state FROM version WHERE id = 20`); got != "cutoff_metcutoff_met" {
		t.Fatalf("before: %s", got)
	}
	// The profiles gain a format cutoff the files do not reach; an episode with no file stays wanted.
	e.scoreFormat(100, 10000)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at) VALUES (203, 2, 1, 3, 'Third', 0, 0)`)
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at) VALUES (20, 203, 1, 'wanted', 0)`)
	n, err := e.lib.RecomputeLinkStates(e.ctx)
	if err != nil || n != 2 {
		t.Fatalf("recomputed %d, err %v", n, err)
	}
	if got := e.one(`SELECT state FROM version WHERE id = 10`); got != "filled" {
		t.Errorf("movie state=%s, want filled", got)
	}
	if got := e.one(`SELECT group_concat(episode_id || ':' || state || ':' || cutoff_met, ' ') FROM
		(SELECT * FROM episode_version_state WHERE version_id = 20 ORDER BY episode_id)`); got != "201:filled:0 202:filled:0 203:wanted:0" {
		t.Errorf("episode states %s", got)
	}
	if got := e.one(`SELECT state FROM version WHERE id = 20`); got != "filled" {
		t.Errorf("series state=%s, want filled", got)
	}
	// Idempotent; a wanted version is not touched.
	e.exec(`UPDATE version SET state = 'wanted' WHERE id = 10`)
	if n, err := e.lib.RecomputeLinkStates(e.ctx); err != nil || n != 0 {
		t.Fatalf("second run %d, %v", n, err)
	}
	if got := e.one(`SELECT state FROM version WHERE id = 10`); got != "wanted" {
		t.Errorf("wanted movie moved to %s", got)
	}

	// Run does it once per process.
	e.exec(`UPDATE version SET state = 'cutoff_met' WHERE id = 10`)
	if _, err := e.lib.Run(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT state FROM version WHERE id = 10`); got != "filled" {
		t.Errorf("after Run: %s", got)
	}
	e.exec(`UPDATE version SET state = 'cutoff_met' WHERE id = 10`)
	if _, err := e.lib.Run(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT state FROM version WHERE id = 10`); got != "cutoff_met" {
		t.Errorf("second Run recomputed again: %s", got)
	}

	// A shadow version is never touched.
	e.pol.Versions["movie-remux"].Mode = policy.ModeShadow
	if n, err := e.lib.RecomputeLinkStates(e.ctx); err != nil || n != 0 {
		t.Fatalf("shadow run %d, %v", n, err)
	}
	if got := e.one(`SELECT state FROM version WHERE id = 10`); got != "cutoff_met" {
		t.Errorf("shadow version moved to %s", got)
	}
}
