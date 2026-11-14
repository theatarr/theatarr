package library

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
)

// The import's upgrade check (Sonarr's and Radarr's import UpgradeSpecification): a file is linked over the
// version's current files only when it upgrades every one it would replace.

const packRelease = "Show.S01.2160p.WEB-DL.DDP5.1.H.265-GRP"

// importUpEnv is a series version with four episodes and a profile Remux > WEB 2160p > WEB 1080p, a "Good" format
// (release title says GOOD) scored 100, and the given propers setting.
func importUpEnv(t *testing.T, mode policy.Mode, propers string) *env {
	t.Helper()
	e := newEnv(t, true, mode)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at) VALUES
		(203, 2, 1, 3, 'Third', 0, 0), (204, 2, 1, 4, 'Fourth', 0, 0)`)
	body := `{"name":"Good","specifications":[{"name":"g","implementation":"ReleaseTitleSpecification","negate":false,
		"required":true,"fields":{"value":"\\bGOOD\\b"}}]}`
	if err := os.WriteFile(filepath.Join(e.dir, "good.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	e.pol.Dir = e.dir
	e.pol.Formats = map[string]*policy.FormatRef{"Good": {File: "good.json"}}
	e.pol.Profiles["tv-state"] = &policy.Profile{Qualities: []policy.QualityItem{{Name: "Bluray-2160p Remux"},
		{Name: "WEBDL-2160p"}, {Name: "WEBDL-1080p"}}, Cutoff: "Bluray-2160p Remux", CutoffScore: 1000,
		Formats: map[string]int{"Good": 100}, Propers: propers}
	e.pol.Profiles["movie-state"].Formats = map[string]int{"Good": 100}
	engine, err := decide.New(e.pol, decide.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e.lib.engine = engine
	return e
}

// heldLink is a current file of the tv version as Theatarr's link: its release title gives its formats and revision.
func (e *env) heldLink(id int64, quality, release string, eps ...int64) string {
	e.t.Helper()
	p := filepath.Join(e.shows, "Show (2020)", "Season 01", release+".mkv")
	target := filepath.Join(e.mount, "__all__", "Held", release+".mkv")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		e.t.Fatal(err)
	}
	e.exec(`INSERT INTO link (id, path, target, kind, title_id, release_title, quality, state, created_at, updated_at)
		VALUES (?, ?, ?, 'symlink', 2, ?, ?, 'active', 0, 0)`, id, p, target, release, quality)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (?, 20)`, id)
	for _, ep := range eps {
		e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (?, ?)`, id, ep)
	}
	return p
}

// heldArrFile is a current file of the tv version as the arr's (shadow).
func (e *env) heldArrFile(id int64, quality, release string, eps ...int64) {
	e.t.Helper()
	e.exec(`INSERT INTO version_file (id, version_id, title_id, instance, arr_file_id, path, quality, scene_name, relative_path)
		VALUES (?, 20, 2, 'sonarr', ?, ?, ?, ?, ?)`, id, id, "/arr/Show/"+release+".mkv", quality, release, release+".mkv")
	for _, ep := range eps {
		e.exec(`INSERT INTO version_file_episode (version_file_id, episode_id) VALUES (?, ?)`, id, ep)
	}
}

func (e *env) pack(id int64, quality string) {
	e.release(packRelease, map[string]float64{
		"Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2700, "Show.S01E02.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2700,
		"Show.S01E03.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2700, "Show.S01E04.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2700})
	e.seriesDownload(id, 2, 20, hashA, packRelease, "[201, 202, 203, 204]", nil)
	e.exec(`UPDATE download SET quality = ? WHERE id = ?`, quality, id)
}

// skipped is the plan's not-upgrade files by name, with their reasons.
func skipped(p *Plan) map[string]string {
	out := map[string]string{}
	for _, f := range p.Rejected {
		if f.NotUpgrade {
			out[f.Rel] = f.Reason
		}
	}
	return out
}

func linkedEpisodes(p *Plan) []int64 {
	var out []int64
	for _, l := range p.Links {
		out = append(out, l.Episodes...)
	}
	return out
}

func checkMixedPlan(t *testing.T, p *Plan) {
	t.Helper()
	if got := linkedEpisodes(p); len(got) != 2 || got[0] != 202 || got[1] != 203 {
		t.Errorf("linked episodes %v, want [202 203] (the worse E02 upgraded, the missing E03 filled)", got)
	}
	s := skipped(p)
	if len(s) != 2 || !strings.Contains(s["Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv"],
		"Not an upgrade for existing file(s). Existing quality: Bluray-2160p Remux. New Quality WEBDL-2160p.") ||
		!strings.Contains(s["Show.S01E04.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv"],
			"Not a Custom Format upgrade for existing file(s). New: [] (0) do not improve on Existing: [Good] (100)") {
		t.Errorf("skipped %v", s)
	}
}

// The live blocker: a WEB pack accepted because an episode is missing (packs: any_upgradable) fills that episode and
// upgrades the worse file, but leaves the better remux and the higher-scored file of the same quality alone.
func TestImportUpgradeCheck(t *testing.T) {
	t.Parallel()
	e := importUpEnv(t, policy.ModeActive, "")
	remux := e.heldLink(1, "Bluray-2160p Remux", "Show.S01E01.2160p.BluRay.REMUX.HEVC-OLD", 201)
	worse := e.heldLink(2, "WEBDL-1080p", "Show.S01E02.1080p.WEB-DL.H.264-OLD", 202)
	good := e.heldLink(4, "WEBDL-2160p", "Show.S01E04.2160p.WEB-DL.GOOD.H.265-OLD", 204)
	e.pack(1, "WEBDL-2160p")

	plan, err := e.lib.Import(e.ctx, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	checkMixedPlan(t, plan)

	plan, err = e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	checkMixedPlan(t, plan)
	for _, p := range []string{remux, good} {
		if _, err := os.Readlink(p); err != nil {
			t.Errorf("a better file left the library: %v", err)
		}
	}
	if _, err := os.Lstat(worse); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the worse file was not moved aside: %v", err)
	}
	if got := e.one(`SELECT COUNT(*) FROM link_trash`); got != "1" {
		t.Errorf("trash rows %s, want 1", got)
	}
	if got := e.one(`SELECT state FROM download WHERE id = 1`) + "/" + e.one(`SELECT state FROM library_import WHERE download_id = 1`); got != "imported/linked" {
		t.Errorf("download/import %s", got)
	}
	// The skipped files are not the episodes' files: E01 and E04 keep their links.
	if got := e.one(`SELECT group_concat(episode_id || ':' || link_id, ' ') FROM
		(SELECT * FROM link_episode le JOIN link l ON l.id = le.link_id WHERE l.state = 'active' ORDER BY episode_id)`); !strings.HasPrefix(got, "201:1 ") || !strings.HasSuffix(got, " 204:4") {
		t.Errorf("episode links %s", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM download_file WHERE download_id = 1`); got != "2" {
		t.Errorf("recorded identities %s, want only the 2 linked files", got)
	}
	// A retry of the same download finds its own links in place: no check against itself.
	e.exec(`UPDATE download SET state = 'ready' WHERE id = 1`)
	if plan, err = e.lib.Import(e.ctx, 1, true); err != nil {
		t.Fatal(err)
	}
	if got := linkedEpisodes(plan); len(got) != 2 || !plan.Links[0].Exists {
		t.Errorf("retry links %v %+v", got, plan.Links)
	}
}

// Shadow: the dry run against the arr's files shows the same skips, and records them as dry-run rejections.
func TestImportUpgradeCheckShadow(t *testing.T) {
	t.Parallel()
	e := importUpEnv(t, policy.ModeShadow, "")
	e.heldArrFile(1, "Bluray-2160p Remux", "Show.S01E01.2160p.BluRay.REMUX.HEVC-OLD", 201)
	e.heldArrFile(2, "WEBDL-1080p", "Show.S01E02.1080p.WEB-DL.H.264-OLD", 202)
	e.heldArrFile(4, "WEBDL-2160p", "Show.S01E04.2160p.WEB-DL.GOOD.H.265-OLD", 204)
	e.pack(1, "WEBDL-2160p")
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.DryRun {
		t.Fatal("shadow import was not a dry run")
	}
	checkMixedPlan(t, plan)
	if got := e.one(`SELECT COUNT(*) FROM library_action WHERE kind = 'reject' AND mode = 'dry_run' AND detail LIKE 'Not a%'`); got != "2" {
		t.Errorf("dry-run rejections %s", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM library_action WHERE kind = 'replace' AND mode = 'dry_run'`); got != "1" {
		t.Errorf("dry-run replacements %s, want only E02's", got)
	}
}

// Equal quality and score is accepted by the arr import specification.
func TestImportEqualIsNotAnUpgrade(t *testing.T) {
	t.Parallel()
	e := importUpEnv(t, policy.ModeActive, "")
	held := e.heldLink(1, "WEBDL-2160p", "Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-OTHER", 201)
	rel := "Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP"
	e.release(rel, map[string]float64{rel + ".mkv": 2700})
	e.seriesDownload(1, 2, 20, hashA, rel, "[201]", nil)
	e.exec(`UPDATE download SET quality = 'WEBDL-2160p' WHERE id = 1`)
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Links) != 1 || skipped(plan)[rel+".mkv"] != "" {
		t.Fatalf("plan %+v", plan)
	}
	if tg, _ := os.Readlink(held); strings.Contains(tg, "Held") {
		t.Errorf("the current file was moved aside: %q", tg)
	}
	if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "imported" {
		t.Errorf("download outcome %s", got)
	}
	if got := e.one(`SELECT state || '/' || COALESCE(last_error, '') FROM library_import WHERE download_id = 1`); got != "linked/" {
		t.Errorf("import outcome %s", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM router_blocklist WHERE download_id = 1`); got != "0" {
		t.Errorf("blocklist rows %s", got)
	}
}

// A pack whose release title scores higher can still tie on each file's parsed name; equal files are imported.
func TestImportEqualPackIsImported(t *testing.T) {
	t.Parallel()
	e := importUpEnv(t, policy.ModeActive, policy.PropersDoNotPrefer)
	e.heldLink(1, "WEBDL-2160p", "Show.S01E01.2160p.WEB-DL.H.265-OLD", 201)
	e.heldLink(2, "WEBDL-2160p", "Show.S01E02.2160p.WEB-DL.H.265-OLD", 202)
	rel := "Show.S01.2160p.WEB-DL.GOOD.H.265-GRP"
	e.release(rel, map[string]float64{"Show S01E01.mkv": 2700, "Show S01E02.mkv": 2700})
	e.seriesDownload(1, 2, 20, hashA, rel, "[201, 202]", nil)
	e.exec(`UPDATE download SET quality = 'WEBDL-2160p' WHERE id = 1`)
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Links) != 2 {
		t.Fatalf("links=%d skipped=%v", len(plan.Links), skipped(plan))
	}
	if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "imported" {
		t.Errorf("download state %s", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM router_blocklist WHERE download_id = 1`); got != "0" {
		t.Errorf("blocklist rows %s", got)
	}
}

func TestImportAllUnmatchedIsHeld(t *testing.T) {
	t.Parallel()
	e := importUpEnv(t, policy.ModeActive, "")
	rel := "Show.S01E99.2160p.WEB-DL.H.265-GRP"
	e.release(rel, map[string]float64{rel + ".mkv": 2700})
	e.seriesDownload(1, 2, 20, hashA, rel, "[201]", nil)
	e.exec(`UPDATE download SET quality = 'WEBDL-2160p' WHERE id = 1`)
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Links) != 0 || len(plan.Unmatched) == 0 {
		t.Fatalf("plan %+v", plan)
	}
	if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "ready" {
		t.Errorf("download state %s", got)
	}
	if got := e.one(`SELECT state || '/' || last_error FROM library_import WHERE download_id = 1`); got != "rejected/no file matched the download's episodes" {
		t.Errorf("import state %s", got)
	}
	if got := e.one(`SELECT COUNT(*) FROM router_blocklist WHERE download_id = 1`); got != "0" {
		t.Errorf("blocklist rows %s", got)
	}
	actions := e.one(`SELECT COUNT(*) FROM library_action WHERE download_id = 1 AND kind = 'reject'`)
	if done, err := e.lib.Run(e.ctx); err != nil || done != 0 {
		t.Fatalf("periodic import retried held download: done=%d err=%v", done, err)
	}
	if got := e.one(`SELECT COUNT(*) FROM library_action WHERE download_id = 1 AND kind = 'reject'`); got != actions {
		t.Errorf("held import repeated its action: before=%s after=%s", actions, got)
	}

	// A manual choice resolves the held row even though the file's own episode number was unmatched.
	e.exec(`INSERT INTO download_file (download_id, rel, episodes, source, updated_at) VALUES (1, ?, '[201]', 'manual', 0)`, rel+".mkv")
	plan, err = e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Links) != 1 || plan.Links[0].How != HowManual {
		t.Fatalf("manual resolution plan %+v", plan)
	}
	if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "imported" {
		t.Errorf("manually resolved download state %s", got)
	}
}

// A proper of the same quality replaces only under prefer_and_upgrade; a lower revision never does.
func TestImportUpgradePropers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		propers, held, release, want string
	}{
		{"", "Show.S01E01.2160p.WEB-DL.H.265-OLD", "Show.S01E01.PROPER.2160p.WEB-DL.H.265-GRP", ""},
		{policy.PropersPreferAndUpgrade, "Show.S01E01.2160p.WEB-DL.H.265-OLD", "Show.S01E01.REPACK.2160p.WEB-DL.H.265-GRP", ""},
		{policy.PropersDoNotUpgrade, "Show.S01E01.2160p.WEB-DL.H.265-OLD", "Show.S01E01.PROPER.2160p.WEB-DL.H.265-GRP", "the same quality"},
		{policy.PropersDoNotPrefer, "Show.S01E01.2160p.WEB-DL.H.265-OLD", "Show.S01E01.PROPER.2160p.WEB-DL.H.265-GRP", "the same quality"},
		{"", "Show.S01E01.PROPER.2160p.WEB-DL.H.265-OLD", "Show.S01E01.2160p.WEB-DL.H.265-GRP", "Not a quality revision upgrade"},
		{policy.PropersDoNotUpgrade, "Show.S01E01.PROPER.2160p.WEB-DL.H.265-OLD", "Show.S01E01.2160p.WEB-DL.H.265-GRP", "Not a quality revision upgrade"},
		{policy.PropersDoNotPrefer, "Show.S01E01.PROPER.2160p.WEB-DL.H.265-OLD", "Show.S01E01.GOOD.2160p.WEB-DL.H.265-GRP", ""},
	} {
		t.Run(tc.propers+"/"+tc.release, func(t *testing.T) {
			e := importUpEnv(t, policy.ModeActive, tc.propers)
			e.heldLink(1, "WEBDL-2160p", tc.held, 201)
			e.release(tc.release, map[string]float64{tc.release + ".mkv": 2700})
			e.seriesDownload(1, 2, 20, hashA, tc.release, "[201]", nil)
			e.exec(`UPDATE download SET quality = 'WEBDL-2160p' WHERE id = 1`)
			plan, err := e.lib.Import(e.ctx, 1, true)
			if err != nil {
				t.Fatal(err)
			}
			why := skipped(plan)[tc.release+".mkv"]
			if tc.want == "" && (len(plan.Links) != 1 || why != "") || tc.want != "" && (len(plan.Links) != 0 || !strings.Contains(why, tc.want)) {
				t.Errorf("links %d, skipped %q; want %q", len(plan.Links), why, tc.want)
			}
		})
	}
}

// A multi-episode file must upgrade every current file it would replace (Sonarr checks each episode's file).
func TestImportUpgradeMultiEpisode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		first, other string
		linked       bool
	}{
		{"one better", "WEBDL-1080p", "Bluray-2160p Remux", false},
		{"first better", "Bluray-2160p Remux", "WEBDL-1080p", false},
		{"both worse", "WEBDL-1080p", "WEBDL-1080p", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := importUpEnv(t, policy.ModeActive, "")
			e.heldLink(1, tc.first, "Show.S01E01.held-OLD", 201)
			e.heldLink(2, tc.other, "Show.S01E02.held-OLD", 202)
			rel := "Show.S01E01E02.2160p.WEB-DL.DDP5.1.H.265-GRP"
			e.release(rel, map[string]float64{rel + ".mkv": 5400})
			e.seriesDownload(1, 2, 20, hashA, rel, "[201, 202]", nil)
			e.exec(`UPDATE download SET quality = 'WEBDL-2160p' WHERE id = 1`)
			plan, err := e.lib.Import(e.ctx, 1, true)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(plan.Links) == 1; got != tc.linked {
				t.Fatalf("linked %v, want %v: %+v", got, tc.linked, plan)
			}
			if tc.linked {
				if eps := linkedEpisodes(plan); len(eps) != 2 || len(plan.Links[0].Replaces) != 2 {
					t.Errorf("episodes %v replaces %v", eps, plan.Links[0].Replaces)
				}
				if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
					t.Fatal(err)
				}
				if got := e.one(`SELECT COUNT(*) FROM link_trash`); got != "2" {
					t.Errorf("trash rows %s, want both single files", got)
				}
			} else if why := skipped(plan)[rel+".mkv"]; !strings.Contains(why, "Existing quality: Bluray-2160p Remux") {
				t.Errorf("skipped %q", why)
			}
		})
	}
}

// Sonarr's SameEpisodesImportSpecification refuses a partial upgrade rather than importing alongside the old
// multi-episode file. The accepted batch may cover it jointly, but rejected files cannot supply coverage.
func TestImportUpgradeSplitMultiEpisode(t *testing.T) {
	t.Parallel()
	const first = "Show.S01E01.2160p.WEB-DL.H.265-GRP.mkv"
	const second = "Show.S01E02.2160p.BluRay.REMUX.HEVC-GRP.mkv"
	for _, tc := range []struct {
		name, quality, held string
		files               map[string]float64
		replaces            bool
	}{
		{"E01 only", "WEBDL-1080p", "Show.S01E01E02.1080p.WEB-DL.H.264-OLD",
			map[string]float64{first: 2700}, false},
		{"both accepted", "WEBDL-1080p", "Show.S01E01E02.1080p.WEB-DL.H.264-OLD",
			map[string]float64{first: 2700, second: 2700}, true},
		{"only E02 passes upgrade", "WEBDL-2160p", "Show.S01E01E02.2160p.WEB-DL.GOOD.H.265-OLD",
			map[string]float64{first: 2700, second: 2700}, false},
		{"E02 sample", "WEBDL-1080p", "Show.S01E01E02.1080p.WEB-DL.H.264-OLD",
			map[string]float64{first: 2700, second: 10}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := importUpEnv(t, policy.ModeActive, "")
			old := e.heldLink(1, tc.quality, tc.held, 201, 202)
			e.exec(`INSERT INTO episode_version_state (version_id, episode_id, state, updated_at)
				VALUES (20, 201, 'filled', 0), (20, 202, 'filled', 0)`)
			e.release(packRelease, tc.files)
			e.seriesDownload(1, 2, 20, hashA, packRelease, "[201, 202]", nil)
			e.exec(`UPDATE download SET quality = 'WEBDL-2160p' WHERE id = 1`)
			for _, dryRun := range []bool{true, false} {
				plan, err := e.lib.Import(e.ctx, 1, dryRun)
				if err != nil {
					t.Fatal(err)
				}
				if tc.replaces {
					if len(plan.Links) != 2 || len(plan.Links[0].Replaces) != 0 ||
						len(plan.Links[1].Replaces) != 1 || plan.Links[1].Replaces[0] != old {
						t.Fatalf("replacement must be planned once after both files: %+v", plan)
					}
				} else {
					if len(plan.Links) != 0 || len(plan.Rejected) != len(tc.files) {
						t.Fatalf("partial replacement plan %+v", plan)
					}
					partial := first
					if tc.name == "only E02 passes upgrade" {
						partial = second
						if why := skipped(plan)[first]; !strings.Contains(why, "Not a Custom Format upgrade") {
							t.Errorf("E01 rejection %q", why)
						}
					}
					if why := skipped(plan)[partial]; !strings.Contains(why, "Episode file on disk contains more episodes than this file contains") || !strings.Contains(why, old) {
						t.Errorf("partial replacement reason %q", why)
					}
				}
			}
			wantState, wantTrash, wantLinks, wantDownload := "active", "0", "1", "ready"
			if tc.replaces {
				wantState, wantTrash, wantLinks, wantDownload = "unlinked", "1", "2", "imported"
			} else if tc.name == "E02 sample" {
				wantDownload = "failed"
			}
			if got := e.one(`SELECT state FROM link WHERE id = 1`); got != wantState {
				t.Errorf("old link %s, want %s", got, wantState)
			}
			if _, err := os.Readlink(old); tc.replaces && !errors.Is(err, os.ErrNotExist) || !tc.replaces && err != nil {
				t.Errorf("old symlink: %v", err)
			}
			if got := e.one(`SELECT COUNT(*) FROM link_trash`); got != wantTrash {
				t.Errorf("trash rows %s, want %s", got, wantTrash)
			}
			if got := e.one(`SELECT COUNT(*) FROM link WHERE state = 'active'`); got != wantLinks {
				t.Errorf("active links %s, want %s", got, wantLinks)
			}
			if got := e.one(`SELECT COUNT(DISTINCT le.episode_id) FROM link_episode le JOIN link l ON l.id = le.link_id
				WHERE l.state = 'active' AND le.episode_id IN (201, 202)`); got != "2" {
				t.Errorf("covered episodes %s, want 2", got)
			}
			if got := e.one(`SELECT COUNT(*) FROM episode_version_state WHERE version_id = 20 AND state = 'wanted'`); got != "0" {
				t.Errorf("wanted episodes %s, want 0", got)
			}
			if got := e.one(`SELECT state FROM download WHERE id = 1`); got != wantDownload {
				t.Errorf("download %s, want %s", got, wantDownload)
			}
		})
	}
}

func TestImportUpgradeMultiEpisodeRemainingLink(t *testing.T) {
	t.Parallel()
	for _, active := range []bool{true, false} {
		t.Run(map[bool]string{true: "active link", false: "arr file"}[active], func(t *testing.T) {
			e := importUpEnv(t, policy.ModeActive, "")
			old := e.heldLink(1, "WEBDL-1080p", "Show.S01E01E02.1080p.WEB-DL.H.264-OLD", 201, 202)
			other := ""
			if active {
				other = e.heldLink(2, "WEBDL-1080p", "Show.S01E02.1080p.WEB-DL.H.264-OTHER", 202)
			} else {
				e.heldArrFile(2, "WEBDL-1080p", "Show.S01E02.1080p.WEB-DL.H.264-OTHER", 202)
			}
			rel := "Show.S01E01.2160p.WEB-DL.H.265-GRP"
			e.release(rel, map[string]float64{rel + ".mkv": 2700})
			e.seriesDownload(1, 2, 20, hashA, rel, "[201]", nil)
			for _, dryRun := range []bool{true, false} {
				plan, err := e.lib.Import(e.ctx, 1, dryRun)
				if err != nil {
					t.Fatal(err)
				}
				if active {
					if len(plan.Links) != 1 || len(plan.Links[0].Replaces) != 1 || plan.Links[0].Replaces[0] != old {
						t.Fatalf("replacement plan %+v", plan)
					}
				} else if len(plan.Links) != 0 || !strings.Contains(skipped(plan)[rel+".mkv"], "Episode file on disk contains more episodes") {
					t.Fatalf("arr file supplied active coverage: %+v", plan)
				}
			}
			if active {
				if _, err := os.Readlink(other); err != nil {
					t.Errorf("remaining link removed: %v", err)
				}
				if got := e.one(`SELECT COUNT(DISTINCT le.episode_id) FROM link_episode le JOIN link l ON l.id = le.link_id
					WHERE l.state = 'active' AND le.episode_id IN (201, 202)`); got != "2" {
					t.Errorf("covered episodes %s, want 2", got)
				}
			} else if _, err := os.Readlink(old); err != nil {
				t.Errorf("old link removed: %v", err)
			}
		})
	}
}

func TestPlanSeriesReplacementsSamePath(t *testing.T) {
	t.Parallel()
	p := &Plan{Files: []PlannedFile{{Rel: "first"}},
		Links: []PlannedLink{{Path: "same", File: "first", Episodes: []int64{201}}}}
	planSeriesReplacements(p, []existingFile{{LinkID: 1, Path: "same", Episodes: []int64{201, 202}}})
	if len(p.Links) != 0 || len(p.Rejected) != 1 || !p.Files[0].NotUpgrade ||
		!strings.Contains(p.Files[0].Reason, "Episode file on disk contains more episodes") {
		t.Fatalf("partial in-place replacement accepted: %+v", p)
	}
}

func TestImportUpgradeSplitMultiEpisodeRetry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name             string
		blocked, inPlace int
	}{
		{"E01 fails", 0, -1},
		{"E02 fails", 1, -1},
		{"E01 fails with E02 in place", 0, 1},
		{"E02 fails with E01 in place", 1, 0},
		{"E01 in place", -1, 0},
		{"E02 in place", -1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := importUpEnv(t, policy.ModeActive, "")
			old := e.heldLink(1, "WEBDL-1080p", "Show.S01E01E02.1080p.WEB-DL.H.264-OLD", 201, 202)
			e.exec(`INSERT INTO episode_version_state (version_id, episode_id, state, updated_at)
				VALUES (20, 201, 'filled', 0), (20, 202, 'filled', 0)`)
			e.release(packRelease, map[string]float64{"Show.S01E01.2160p.WEB-DL.H.265-GRP.mkv": 2700,
				"Show.S01E02.2160p.WEB-DL.H.265-GRP.mkv": 2700})
			e.seriesDownload(1, 2, 20, hashA, packRelease, "[201, 202]", nil)
			plan, err := e.lib.Import(e.ctx, 1, true)
			if err != nil || len(plan.Links) != 2 {
				t.Fatalf("plan %+v, err %v", plan, err)
			}
			if tc.inPlace >= 0 {
				path := plan.Links[tc.inPlace].Path
				if err := os.Rename(old, path); err != nil {
					t.Fatal(err)
				}
				e.exec(`UPDATE link SET path = ? WHERE id = 1`, path)
				old = path
			}
			if tc.blocked < 0 {
				if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
					t.Fatal(err)
				}
				if got := e.one(`SELECT COUNT(*) FROM link_trash`) + "/" +
					e.one(`SELECT COUNT(*) FROM link WHERE state = 'active'`) + "/" +
					e.one(`SELECT state FROM download WHERE id = 1`); got != "1/2/imported" {
					t.Errorf("in-place trash/links/download %s", got)
				}
				return
			}
			blocker := plan.Links[tc.blocked].Path
			if err := os.WriteFile(blocker, []byte("block"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := e.lib.Import(e.ctx, 1, false); err == nil {
				t.Fatal("partial import reported success")
			}
			if target, err := os.Readlink(old); err != nil || !strings.Contains(target, "Held") {
				t.Errorf("old file changed after a failed link: target=%q err=%v", target, err)
			}
			if got := e.one(`SELECT state FROM link WHERE id = 1`) + "/" +
				e.one(`SELECT COUNT(*) FROM link_trash`) + "/" +
				e.one(`SELECT COUNT(*) FROM episode_version_state WHERE version_id = 20 AND state = 'wanted'`); got != "active/0/0" {
				t.Errorf("old link/trash/wanted %s", got)
			}
			if err := os.Remove(blocker); err != nil {
				t.Fatal(err)
			}
			if tc.inPlace >= 0 {
				if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "failed" {
					t.Errorf("download with no new links %s, want failed", got)
				}
				e.exec(`UPDATE download SET state = 'ready' WHERE id = 1`)
			}
			plan, err = e.lib.Import(e.ctx, 1, false)
			if err != nil {
				t.Fatal(err)
			}
			if tc.inPlace < 0 && !plan.Links[1-tc.blocked].Exists {
				t.Error("retry did not find the successful link")
			}
			wantState := "unlinked"
			if tc.inPlace >= 0 {
				wantState = "active"
			}
			if got := e.one(`SELECT state FROM link WHERE id = 1`) + "/" +
				e.one(`SELECT COUNT(*) FROM link_trash`) + "/" +
				e.one(`SELECT state FROM download WHERE id = 1`); got != wantState+"/1/imported" {
				t.Errorf("retry old link/trash/download %s", got)
			}
		})
	}
}

// Movies: Radarr's MovieImport UpgradeSpecification is the same check against the movie's file.
func TestImportUpgradeMovie(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T, held string) (*env, string) {
		e := importUpEnv(t, policy.ModeActive, "")
		folder := filepath.Join(e.movies, "Amelie The Movie (2001)")
		p := filepath.Join(folder, "held.mkv")
		target := filepath.Join(e.mount, "__all__", "Held", "held.mkv")
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
		e.exec(`INSERT INTO link (id, path, target, kind, title_id, release_title, quality, state, created_at, updated_at)
			VALUES (1, ?, ?, 'symlink', 1, 'Amelie.2001.held-OLD', ?, 'active', 0, 0)`, p, target, held)
		e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 10)`)
		return e, p
	}
	t.Run("worse", func(t *testing.T) {
		e, held := setup(t, "Remux-2160p")
		rel := "Amelie.2001.1080p.WEB-DL.DDP5.1.H.264-GRP"
		e.release(rel, map[string]float64{"movie.mkv": 7000})
		e.videoSize(rel, 1920, 1080)
		e.download(1, "movie-remux", 1, 10, rel, rel, "[]")
		e.exec(`UPDATE download SET quality = 'WEBDL-1080p' WHERE id = 1`)
		plan, err := e.lib.Import(e.ctx, 1, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Links) != 0 || !strings.Contains(skipped(plan)["movie.mkv"], "Existing quality: Remux-2160p. New Quality WEBDL-1080p.") {
			t.Fatalf("plan %+v", plan)
		}
		if _, err := os.Readlink(held); err != nil {
			t.Errorf("the remux left the library: %v", err)
		}
		if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "ready" {
			t.Errorf("download %s", got)
		}
		if got := e.one(`SELECT state || '/' || last_error FROM library_import WHERE download_id = 1`); !strings.HasPrefix(got, "rejected/not an upgrade") {
			t.Errorf("import %s", got)
		}
	})
	t.Run("better", func(t *testing.T) {
		e, held := setup(t, "WEBDL-1080p")
		e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
		e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
		plan, err := e.lib.Import(e.ctx, 1, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Links) != 1 || len(plan.Links[0].Replaces) != 1 {
			t.Fatalf("plan %+v", plan)
		}
		if _, err := os.Lstat(held); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the worse file stayed: %v", err)
		}
		if got := e.one(`SELECT release_title || '/' || quality FROM library_action WHERE kind = 'replace'`); got != "Amelie.2001.held-OLD/WEBDL-1080p" {
			t.Errorf("replace action facts %q", got)
		}
	})
}

// A client's explicit files are imported as chosen, as the arrs' ManualImport does, even over a better file.
func TestManualImportSkipsUpgradeCheck(t *testing.T) {
	t.Parallel()
	e := importUpEnv(t, policy.ModeActive, "")
	e.heldLink(1, "Bluray-2160p Remux", "Show.S01E01.2160p.BluRay.REMUX.HEVC-OLD", 201)
	rel := "Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP"
	e.release(rel, map[string]float64{rel + ".mkv": 2700})
	e.seriesDownload(1, 2, 20, hashA, rel, "[201]", nil)
	e.exec(`INSERT INTO download_file (download_id, rel, episodes, source, updated_at) VALUES (1, ?, '[201]', 'manual', 0)`, rel+".mkv")
	plan, err := e.lib.Import(e.ctx, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Links) != 1 || len(plan.Links[0].Replaces) != 1 {
		t.Errorf("plan %+v", plan)
	}
}
