package library

import (
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// importSeries imports the two-episode series pack into version 20 (cutoff_met under the default profile).
func (e *env) importSeries() {
	e.t.Helper()
	rel := "Show.S01.2160p.BluRay.REMUX.HEVC-GRP"
	e.release(rel, map[string]float64{"Show.S01E01.2160p.BluRay.REMUX.HEVC-GRP.mkv": 2700,
		"Show.S01E02.2160p.BluRay.REMUX.HEVC-GRP.mkv": 2700})
	e.seriesDownload(2, 2, 20, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", rel, "[201, 202]", nil)
	e.exec(`UPDATE download SET quality = 'Bluray-2160p Remux' WHERE id = 2`)
	if _, err := e.lib.Import(e.ctx, 2, false); err != nil {
		e.t.Fatal(err)
	}
}

// A series version with one aired episode missing is wanted; its other episodes' states are still
// judged when the cutoff rises.
func TestRecomputeWantedSeriesVersion(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.importSeries()
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, air_date_utc_at, created_at, updated_at)
		VALUES (203, 2, 1, 3, 'Third', 1000, 0, 0)`)
	e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, updated_at) VALUES (20, 203, 1, 'wanted', 0)`)
	e.exec(`UPDATE version SET state = 'wanted' WHERE id = 20`)
	e.scoreFormat(100, 10000)
	if _, err := e.lib.RecomputeLinkStates(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT group_concat(episode_id || ':' || state, ' ') FROM
		(SELECT * FROM episode_version_state WHERE version_id = 20 ORDER BY episode_id)`); got != "201:filled 202:filled 203:wanted" {
		t.Fatalf("episode states %s", got)
	}
}

// A version an arr still manages keeps the arr's verdict on its files, but Theatarr's own links in it are
// judged.
func TestRecomputeManagedVersionLinks(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.importSeries()
	e.exec(`INSERT INTO version_source (version_id, instance, arr_id) VALUES (20, 'sonarr_old', 7)`)
	e.pol.Import = &policy.Import{Arr: map[string]*policy.ArrSource{"sonarr_old": {}}}
	e.scoreFormat(100, 10000)
	if n, err := e.lib.RecomputeLinkStates(e.ctx); err != nil || n != 1 {
		t.Fatalf("recomputed %d, %v", n, err)
	}
	if got := e.one(`SELECT state FROM episode_version_state WHERE version_id = 20 AND episode_id = 201`); got != "filled" {
		t.Fatalf("managed version's link stays %s", got)
	}
}

// After a settings save only the versions whose judging settings changed are recomputed.
func TestRecomputeAfterSaveOnlyChangedVersions(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.lib.Run(e.ctx); err != nil {
		t.Fatal(err)
	}
	// A save that changes nothing a state is judged under: a stale state is left alone.
	e.exec(`UPDATE version SET state = 'filled' WHERE id = 10`)
	e.pol.Revision = "other"
	if _, err := e.lib.Run(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT state FROM version WHERE id = 10`); got != "filled" {
		t.Fatalf("unchanged version recomputed: %s", got)
	}
	// Its profile changes: it is judged again.
	e.scoreFormat(100, 100)
	e.pol.Revision = "raised"
	if _, err := e.lib.Run(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT state FROM version WHERE id = 10`); got != "cutoff_met" {
		t.Fatalf("changed version not recomputed: %s", got)
	}
}
