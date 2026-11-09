package library

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// Lifecycle tests for "which file is which episode": the import maps a release's files as the decision mapped the
// release (decide.ResolveFile), keeps what it linked, and honours a client's explicit choice.

// links is the plan's links as "<path under shows> <- <file> (<how>)", sorted.
func (e *env) links(p *Plan) string {
	var out []string
	for _, l := range p.Links {
		out = append(out, strings.TrimPrefix(l.Path, e.shows+"/")+" <- "+l.File+" ("+l.How+")")
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

func (e *env) seriesDownload(id, title, version int64, hash, release, episodes string, epMap any) {
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality, score,
		episodes, episode_map, provider, name, state, created_at, updated_at, ready_at)
		VALUES (?, ?, ?, 'tv', ?, ?, 'idx', 'WEBDL-1080p', 0, ?, ?, 'rd', ?, 'ready', 0, 0, 0)`,
		id, title, version, hash, release, episodes, epMap, release)
}

// isekai is an anime series whose franchise has two entries: "Isekai Story" (S01, absolute 1-12) and "Isekai Story
// 2nd Season" (S02E01-E02, absolute 13-14), both reviewed.
func (e *env) isekai() {
	e.exec(`INSERT INTO title (id, kind, name, year, tvdb_id, series_type, runtime_minutes, created_at, updated_at)
		VALUES (3, 'series', 'Isekai Story', 2021, 2002, 'anime', 24, 0, 0)`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (30, 3, 'tv', 0, 0)`)
	for n := 1; n <= 14; n++ {
		s, num := 1, n
		if n > 12 {
			s, num = 2, n-12
		}
		e.exec(`INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at) VALUES (?, 3, ?, ?, 'Episode', 0, 0)`,
			300+n, s, num)
		e.exec(`INSERT INTO episode_number (episode_id, title_id, scheme, number, source) VALUES (?, 3, 'absolute_tvdb', ?, 'tvdb')`, 300+n, n)
	}
	e.exec(`INSERT INTO anime_franchise (slug, name, seed_anilist, created_at, updated_at) VALUES ('isekai', 'Isekai', 1, 0, 0)`)
	e.exec(`INSERT INTO anime_entry (franchise, key, origin, title, body) VALUES ('isekai', 'al:1', 'anilist', 'Isekai Story', '{}'),
		('isekai', 'al:2', 'anilist', 'Isekai Story 2nd Season', '{}')`)
	e.exec(`INSERT INTO anime_mapping (franchise, entry_key, status, tvdb_series, episodes, reason, updated_at) VALUES
		('isekai', 'al:1', 'auto', 2002, '[[1,1],[1,2],[1,3],[1,4],[1,5],[1,6],[1,7],[1,8],[1,9],[1,10],[1,11],[1,12]]', '', 0),
		('isekai', 'al:2', 'auto', 2002, '[[2,1],[2,2]]', '', 0)`)
}

// TestImportFranchiseNumbering: files numbered by their franchise entry ("2nd Season - 01" is canonical S02E01,
// absolute 13) link to the episodes the decision accepted; a file named by an alias the entry does not list maps
// through the decision's stored mapping; the identity is kept for the download.
func TestImportFranchiseNumbering(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.isekai()
	rel := "[Group] Isekai Story 2nd Season (BD 1080p)"
	e.release(rel, map[string]float64{"[Group] Isekai Story 2nd Season - 01 (BD 1080p).mkv": 1440,
		"[Group] Isekai Story 2nd Season - 02 (BD 1080p).mkv": 1440})
	e.videoSize(rel, 1920, 1080)
	e.seriesDownload(1, 3, 30, hashA, rel, "[313, 314]", nil)
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	got := e.links(plan)
	for _, w := range []string{"S02E01 - Episode Bluray-1080p.mkv <- [Group] Isekai Story 2nd Season - 01 (BD 1080p).mkv (file+franchise:al:2)",
		"S02E02 - Episode Bluray-1080p.mkv <- [Group] Isekai Story 2nd Season - 02 (BD 1080p).mkv (file+franchise:al:2)"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %s in\n%s", w, got)
		}
	}
	if len(plan.Links) != 2 || len(plan.Unmatched) != 0 {
		t.Fatalf("links\n%s\nunmatched %v", got, plan.Unmatched)
	}
	if got := e.one(`SELECT group_concat(rel || '=' || episodes || ':' || source, ' | ') FROM download_file WHERE download_id = 1`); !strings.Contains(got, "- 01 (BD 1080p).mkv=[313]:import") {
		t.Errorf("recorded identity %s", got)
	}

	// The decision mapped "Isekai Monogatari" through entry al:2 (download.episode_map); the files name no entry.
	rel2 := "[Other] Isekai Monogatari Season 2 (BD 1080p)"
	hashB := strings.Repeat("b", 40)
	e.release(rel2, map[string]float64{"[Other] Isekai Monogatari - 01 (BD 1080p).mkv": 1440,
		"[Other] Isekai Monogatari - 02 (BD 1080p).mkv": 1440})
	e.videoSize(rel2, 1920, 1080)
	e.seriesDownload(2, 3, 30, hashB, rel2, "[313, 314]", `{"how":"franchise","entry":"al:2"}`)
	// Over the first download's links the equal files are not upgrades (TestImportUpgradeCheck); plan them fresh.
	e.exec(`UPDATE link SET state = 'unlinked'`)
	plan, err = e.lib.Import(e.ctx, 2, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.links(plan); !strings.Contains(got, "S02E01 - Episode Bluray-1080p.mkv <- [Other] Isekai Monogatari - 01 (BD 1080p).mkv (file+franchise:al:2)") ||
		len(plan.Links) != 2 {
		t.Errorf("stored mapping: links\n%s\nunmatched %v", got, plan.Unmatched)
	}
	// Without it the absolute numbers are S01E01-E02, which the download was not grabbed for.
	e.exec(`UPDATE download SET episode_map = NULL WHERE id = 2`)
	if plan, err = e.lib.Import(e.ctx, 2, true); err != nil || len(plan.Links) != 0 || len(plan.Unmatched) != 2 {
		t.Errorf("no stored mapping: %+v %v", plan, err)
	}
}

// TestImportMixedPackSpecialsDailyScene: a two-season pack with a special named by its title, a daily episode
// named by its air date, and a file in scene numbering each link to their episode; the canonical numbers alone
// would match only two of them.
func TestImportMixedPackSpecialsDailyScene(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at) VALUES (203, 2, 0, 1, 'The Making Of', 0, 0)`)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at) VALUES (204, 2, 2, 1, 'Return', 0, 0)`)
	rel := "Show.S01-S02.2160p.WEB-DL.DDP5.1.H.265-GRP"
	e.release(rel, map[string]float64{
		"Season 1/Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv":      2700,
		"Season 1/Show.S01E02.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv":      2700,
		"Season 2/Show.S02E01.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv":      2700,
		"Extras/Show.The.Making.Of.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 1200,
		"Show.S01E01.sample.mkv":                                      30}) // E01 is taken by the larger file first

	e.seriesDownload(1, 2, 20, hashA, rel, "[]", nil)
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	got := e.links(plan)
	for _, w := range []string{"Season 01/Show (2020) - S01E01 - Pilot", "Season 01/Show (2020) - S01E02 - Second Part",
		"Season 02/Show (2020) - S02E01 - Return", "S00E01 - The Making Of"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %s in\n%s", w, got)
		}
	}
	if len(plan.Links) != 4 || len(plan.Unmatched) != 1 || !strings.Contains(got, "(special)") {
		t.Errorf("links\n%s\nrejected %+v unmatched %v", got, plan.Rejected, plan.Unmatched)
	}

	// Daily: the file is named by its air date.
	e.exec(`INSERT INTO title (id, kind, name, year, tvdb_id, series_type, runtime_minutes, created_at, updated_at)
		VALUES (4, 'series', 'Late Show', 2015, 7, 'daily', 45, 0, 0)`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (40, 4, 'tv', 0, 0)`)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, air_date, created_at, updated_at) VALUES
		(401, 4, 2026, 100, 'Guest One', '2026-10-20', 0, 0), (402, 4, 2026, 101, 'Guest Two', '2026-10-21', 0, 0)`)
	daily := "Late.Show.2026.10.21.Guest.Two.1080p.WEB-DL-GRP"
	e.release(daily, map[string]float64{daily + ".mkv": 2700})
	e.seriesDownload(2, 4, 40, strings.Repeat("c", 40), daily, "[402]", nil)
	if plan, err = e.lib.Import(e.ctx, 2, true); err != nil || len(plan.Links) != 1 || plan.Links[0].Episodes[0] != 402 {
		t.Errorf("daily: %+v %v", plan, err)
	}

	// Scene numbering: scene S02E01 is TVDB S01E03.
	e.exec(`INSERT INTO title (id, kind, name, year, tvdb_id, series_type, use_scene_numbering, runtime_minutes, created_at, updated_at)
		VALUES (5, 'series', 'Scene Show', 2019, 8, 'standard', 1, 45, 0, 0)`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (50, 5, 'tv', 0, 0)`)
	for n := 1; n <= 3; n++ {
		e.exec(`INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at) VALUES (?, 5, 1, ?, 'Ep', 0, 0)`, 500+n, n)
	}
	e.exec(`INSERT INTO episode_number (episode_id, title_id, scheme, season, number, source) VALUES (503, 5, 'scene', 2, 1, 'xem')`)
	scene := "Scene.Show.S02E01.1080p.WEB-DL-GRP"
	e.release(scene, map[string]float64{scene + ".mkv": 2700})
	e.seriesDownload(3, 5, 50, strings.Repeat("d", 40), scene, "[503]", nil)
	if plan, err = e.lib.Import(e.ctx, 3, true); err != nil || len(plan.Links) != 1 || plan.Links[0].Episodes[0] != 503 ||
		!strings.Contains(plan.Links[0].Path, "S01E03") {
		t.Errorf("scene: %+v %v", plan, err)
	}
}

// TestImportManualFiles: a client's explicit choice imports only the chosen files, each with the chosen episodes,
// even against the file's name; a path import of one file in a pack imports that file only.
func TestImportManualFiles(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	rel := "Show.S01.2160p.WEB-DL.DDP5.1.H.265-GRP"
	e.release(rel, map[string]float64{"Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2700,
		"Show.S01E02.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2650})
	e.seriesDownload(1, 2, 20, hashA, rel, "[201, 202]", nil)
	// The file named E01 is really E02; E02's file is not chosen; a chosen file is missing.
	e.exec(`INSERT INTO download_file (download_id, rel, episodes, source, updated_at) VALUES
		(1, 'Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv', '[202]', 'manual', 0), (1, 'gone.mkv', '[201]', 'manual', 0)`)
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.links(plan); got != "Show (2020)/Season 01/Show (2020) - S01E02 - Second Part [WEBDL-2160p]-GRP.mkv <- Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv (manual)" ||
		strings.Join(plan.Unmatched, ",") != "gone.mkv" {
		t.Errorf("manual: links\n%s\nunmatched %v", got, plan.Unmatched)
	}
	if got := e.one(`SELECT count(*) FROM download_file WHERE source = 'import'`); got != "0" {
		t.Errorf("a manual file was recorded as resolved: %s", got)
	}

	// A path import names one file of the pack.
	e.exec(`UPDATE download SET state = 'ready' WHERE id = 1`)
	e.exec(`DELETE FROM download_file`)
	e.exec(`UPDATE link SET state = 'unlinked'`) // an equal file over the manual link is not an upgrade
	e.exec(`UPDATE download SET source_path = ? WHERE id = 1`, e.mount+"/__all__/"+rel+"/Show.S01E02.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv")
	if plan, err = e.lib.Import(e.ctx, 1, true); err != nil || len(plan.Files) != 1 || len(plan.Links) != 1 ||
		plan.Links[0].Episodes[0] != 202 {
		t.Errorf("path import: %+v %v", plan, err)
	}
	// A path off the mount is refused.
	e.exec(`UPDATE download SET source_path = ? WHERE id = 1`, e.outside+"/x.mkv")
	if _, err := e.lib.Import(e.ctx, 1, true); err == nil || !strings.Contains(err.Error(), "not on the debrid mount") {
		t.Errorf("off the mount: %v", err)
	}
}

// TestManualChoiceIsAuthoritative: two files chosen for the same episode. The larger takes it; the other's choice
// cannot be applied, so it stays unmatched and is never linked by the episode its name says.
func TestManualChoiceIsAuthoritative(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	rel := "Show.S01.1080p.WEB-DL-GRP"
	e.release(rel, map[string]float64{"Show.S01E01.mkv": 2700, "Show.S01E02.mkv": 2700})
	e.videoSize(rel, 1920, 1080)
	// The E02 file is the larger: files are taken largest first.
	if err := os.WriteFile(filepath.Join(e.mount, "__all__", rel, "Show.S01E02.mkv"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	e.seriesDownload(1, 2, 20, hashA, rel, "[]", nil)
	e.exec(`INSERT INTO download_file (download_id, rel, episodes, source, how, updated_at) VALUES
		(1, 'Show.S01E01.mkv', '[202]', 'manual', 'manual', 0), (1, 'Show.S01E02.mkv', '[202]', 'manual', 'manual', 0)`)
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.links(plan); got != "Show (2020)/Season 01/Show (2020) - S01E02 - Second Part [WEBDL-1080p]-GRP.mkv <- Show.S01E02.mkv (manual)" {
		t.Errorf("links\n%s", got)
	}
	if len(plan.Unmatched) != 1 || plan.Unmatched[0] != "Show.S01E01.mkv" {
		t.Errorf("unmatched %v", plan.Unmatched)
	}
	if got := e.one(`SELECT COUNT(*) FROM link_episode WHERE episode_id = 201`); got != "0" {
		t.Errorf("E01 linked from a file the client chose for E02")
	}
}

// TestResolverAnswerIsNotOverridden: XEM swaps canonical E01 and E02 (scene S01E01 is canonical E02). A
// download for canonical E01 holding the file named S01E01 must not link it as E01 by its canonical reading: the
// resolver's answer (E02) is outside the download, so the file stays unmatched with the reason. A download for both
// links each file to its real episode, and a client's explicit choice still applies.
func TestResolverAnswerIsNotOverridden(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.exec(`INSERT INTO title (id, kind, name, year, tvdb_id, series_type, use_scene_numbering, runtime_minutes, created_at, updated_at)
		VALUES (5, 'series', 'Scene Show', 2019, 8, 'standard', 1, 45, 0, 0)`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (50, 5, 'tv', 0, 0)`)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at) VALUES
		(501, 5, 1, 1, 'One', 0, 0), (502, 5, 1, 2, 'Two', 0, 0)`)
	e.exec(`INSERT INTO episode_number (episode_id, title_id, scheme, season, number, source) VALUES
		(501, 5, 'scene', 1, 2, 'xem'), (502, 5, 'scene', 1, 1, 'xem')`)
	rel := "Scene.Show.S01E01.1080p.WEB-DL-GRP"
	e.release(rel, map[string]float64{rel + ".mkv": 2700})
	e.videoSize(rel, 1920, 1080)
	e.seriesDownload(1, 5, 50, hashA, rel, "[501]", nil)
	plan, err := e.lib.Import(e.ctx, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Links) != 0 || len(plan.Unmatched) != 1 ||
		!strings.HasPrefix(plan.Unresolved[rel+".mkv"], "out of scope: maps to S01E02") {
		t.Fatalf("links\n%s\nunmatched %v unresolved %v", e.links(plan), plan.Unmatched, plan.Unresolved)
	}

	// The download for both: each file to its real episode.
	pack := "Scene.Show.S01.1080p.WEB-DL-GRP"
	hashB := strings.Repeat("b", 40)
	e.release(pack, map[string]float64{"Scene.Show.S01E01.1080p.WEB-DL-GRP.mkv": 2700, "Scene.Show.S01E02.1080p.WEB-DL-GRP.mkv": 2700})
	e.videoSize(pack, 1920, 1080)
	e.seriesDownload(2, 5, 50, hashB, pack, "[501, 502]", nil)
	if plan, err = e.lib.Import(e.ctx, 2, true); err != nil {
		t.Fatal(err)
	}
	if got := e.links(plan); !strings.Contains(got, "S01E02 - Two [WEBDL-1080p]-GRP.mkv <- Scene.Show.S01E01.1080p.WEB-DL-GRP.mkv") ||
		!strings.Contains(got, "S01E01 - One [WEBDL-1080p]-GRP.mkv <- Scene.Show.S01E02.1080p.WEB-DL-GRP.mkv") {
		t.Errorf("pack links\n%s", got)
	}

	// A client's choice is authoritative.
	e.exec(`INSERT INTO download_file (download_id, rel, episodes, source, updated_at) VALUES (1, ?, '[501]', 'manual', 0)`, rel+".mkv")
	if plan, err = e.lib.Import(e.ctx, 1, true); err != nil || len(plan.Links) != 1 || plan.Links[0].Episodes[0] != 501 {
		t.Errorf("manual: %+v %v", plan, err)
	}
}
