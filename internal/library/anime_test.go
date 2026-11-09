package library

import (
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// TestAnimeHold: episodes only a conflict mapping covers are not imported; the others are. A download of held
// episodes only stays ready, is recorded as a dry run, and imports once the mapping is reviewed.
func TestAnimeHold(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.exec(`INSERT INTO anime_franchise (slug, name, seed_anilist, created_at, updated_at) VALUES ('show', 'Show', 1, 0, 0)`)
	e.exec(`INSERT INTO anime_mapping (franchise, entry_key, status, tvdb_series, episodes, reason, updated_at) VALUES
		('show', 'al:1', 'conflict', 5, '[[1,1]]', 'the lists disagree', 0), ('show', 'al:2', 'auto', 5, '[[1,2]]', '', 0)`)
	rel := "Show.S01.2160p.WEB-DL.DDP5.1.H.265-GRP"
	e.release(rel, map[string]float64{"Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2700,
		"Show.S01E02.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2650})
	e.download(1, "tv", 2, 20, rel, rel, "[201, 202]")
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Links) != 1 || !strings.Contains(plan.Links[0].Path, "S01E02") || len(plan.Held) != 1 ||
		!strings.Contains(plan.Held[0], "S01E01 is in al:1") {
		t.Fatalf("pack: links %+v held %v", plan.Links, plan.Held)
	}
	if got := e.one(`SELECT count(*) FROM link_episode WHERE episode_id = 201`); got != "0" {
		t.Error("held episode linked")
	}

	// only the held episode: nothing is read or written, the download stays ready
	rel2 := "Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-OTHER"
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality, score,
		episodes, provider, name, state, created_at, updated_at, ready_at)
		VALUES (2, 2, 20, 'tv', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', ?, 'idx', 'Bluray-2160p Remux', 100, '[201]', 'rd', ?, 'ready', 0, 0, 0)`, rel2, rel2)
	before := tree(t, e.dir)
	plan, err = e.lib.Import(e.ctx, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.DryRun || !strings.HasPrefix(plan.Refused, ReasonMappingNotReviewed) || plan.Folder != "" {
		t.Errorf("held import: %+v", plan)
	}
	if strings.Join(tree(t, e.dir), "\n") != strings.Join(before, "\n") {
		t.Error("a held import changed the tree")
	}
	if got := e.one(`SELECT state FROM download WHERE id = 2`); got != "ready" {
		t.Errorf("download %s", got)
	}
	if got := e.one(`SELECT state || ' ' || last_error FROM library_import WHERE download_id = 2`); !strings.HasPrefix(got, "dry_run S01E01") {
		t.Errorf("library_import %s", got)
	}
	if got := e.one(`SELECT count(*) FROM router_blocklist`); got != "0" {
		t.Error("a held import blocklisted the hash")
	}
	// reviewed: the next pass imports it
	e.exec(`UPDATE anime_mapping SET status = 'override' WHERE entry_key = 'al:1'`)
	e.release(rel2, map[string]float64{"Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-OTHER.mkv": 2700})
	if _, err := e.lib.Run(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT state FROM download WHERE id = 2`); got != "imported" {
		t.Errorf("after review: download %s", got)
	}
}
