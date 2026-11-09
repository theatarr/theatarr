package library

import (
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/metadata/anime/animestore"
	"github.com/theatarr/theatarr/internal/policy"
)

// TestAnimeUnreviewedImports: an episode only a conflict mapping covers is not held; it imports through Sonarr's
// numbering like the others, its review item stays. Accepting the mapping later re-links only files the mapping
// places elsewhere (TestAnimeRelinkAfterReview); this one's range agrees with Sonarr's S01E01, so nothing moves.
func TestAnimeUnreviewedImports(t *testing.T) {
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
	if plan.DryRun || len(plan.Links) != 2 {
		t.Fatalf("pack: dry run %v (%s), links %+v", plan.DryRun, plan.Refused, plan.Links)
	}
	if got := e.one(`SELECT count(*) FROM link_episode WHERE episode_id = 201`); got != "1" {
		t.Error("the conflict episode was not linked")
	}

	// only the conflict episode: it imports too, nothing waits
	rel2 := "Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-OTHER"
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality, score,
		episodes, provider, name, state, created_at, updated_at, ready_at)
		VALUES (2, 2, 20, 'tv', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', ?, 'idx', 'Bluray-2160p Remux', 100, '[201]', 'rd', ?, 'ready', 0, 0, 0)`, rel2, rel2)
	e.release(rel2, map[string]float64{"Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-OTHER.mkv": 2700})
	plan, err = e.lib.Import(e.ctx, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.DryRun || len(plan.Links) != 1 || !strings.Contains(plan.Links[0].Path, "S01E01") {
		t.Fatalf("conflict-only import: dry run %v (%s), links %+v", plan.DryRun, plan.Refused, plan.Links)
	}
	if got := e.one(`SELECT state FROM download WHERE id = 2`); got != "imported" {
		t.Errorf("download %s", got)
	}
	if got := e.one(`SELECT count(*) FROM library_import WHERE state = 'dry_run'`); got != "0" {
		t.Errorf("%s dry-run imports", got)
	}
	// reviewed later: the mapping agrees with Sonarr's placement, so the re-link and the library run move nothing
	before := tree(t, e.dir)
	e.exec(`UPDATE anime_mapping SET status = 'override' WHERE entry_key = 'al:1'`)
	if res, err := e.lib.RelinkAnime(e.ctx, 5); err != nil || len(res.Moved)+len(res.Planned)+len(res.Occupied) != 0 {
		t.Fatalf("relink %+v %v", res, err)
	}
	if _, err := e.lib.Run(e.ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Join(tree(t, e.dir), "\n") != strings.Join(before, "\n") {
		t.Error("accepting the mapping moved a link")
	}
}

// A download grabbed through a franchise entry (al:2, S02E01-E02) whose mapping review then rejects imports through
// Sonarr's numbering: its episodes are reconciled to the ones Sonarr gives the release title (an episode), or its
// files (a pack whose title Sonarr cannot parse), a dry run writes nothing, and the import links the files there and
// records the new episodes on the download.
func TestAnimeRejectedMappingImports(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, release, grabbed, episodes string
		files                            []string
	}{
		{"pack", "[Group] Isekai Story 2nd Season (BD 1080p)", "[313, 314]", "[301,302]",
			[]string{"[Group] Isekai Story 2nd Season - 01 (BD 1080p).mkv", "[Group] Isekai Story 2nd Season - 02 (BD 1080p).mkv"}},
		{"episode", "[Group] Isekai Story 2nd Season - 02 (BD 1080p)", "[314]", "[302]",
			[]string{"[Group] Isekai Story 2nd Season - 02 (BD 1080p).mkv"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, true, policy.ModeActive)
			e.isekai()
			e.exec(`UPDATE anime_mapping SET status = 'heuristic' WHERE entry_key = 'al:2'`)
			e.exec(`INSERT INTO anime_review (id, franchise, entry_key, kind, state, summary, created_at)
				VALUES (8, 'isekai', 'al:2', 'heuristic', 'open', 'al:2', 0)`)
			files := map[string]float64{}
			for _, f := range c.files {
				files[f] = 1440
			}
			e.release(c.release, files)
			e.videoSize(c.release, 1920, 1080)
			e.seriesDownload(1, 3, 30, hashA, c.release, c.grabbed, `{"how":"franchise","entry":"al:2"}`)
			plan, err := e.lib.Import(e.ctx, 1, true)
			if err != nil {
				t.Fatal(err)
			}
			if got := e.links(plan); len(plan.Links) != len(c.files) || !strings.Contains(got, "S02E02") {
				t.Fatalf("grabbed under al:2: links\n%s\nunresolved %v", got, plan.Unresolved)
			}

			as := &animestore.Store{DB: e.st, Now: func() time.Time { return e.now }}
			if err := as.Resolve(e.ctx, 8, false, ""); err != nil {
				t.Fatal(err)
			}
			if plan, err = e.lib.Import(e.ctx, 1, true); err != nil || len(plan.Links) != len(c.files) {
				t.Fatalf("dry run after the reject: %+v %v", plan, err)
			}
			if got := e.one(`SELECT episodes FROM download WHERE id = 1`); got != c.grabbed {
				t.Errorf("a dry run wrote the episodes: %s", got)
			}
			if plan, err = e.lib.Import(e.ctx, 1, false); err != nil {
				t.Fatal(err)
			}
			got := e.links(plan)
			if len(plan.Links) != len(c.files) || !strings.Contains(got,
				"S01E02 - Episode Bluray-1080p.mkv <- [Group] Isekai Story 2nd Season - 02 (BD 1080p).mkv") ||
				strings.Contains(got, "S02E") {
				t.Errorf("links\n%s\nunresolved %v", got, plan.Unresolved)
			}
			want := `imported ` + c.episodes + ` {"how":"franchise","entry":"al:2"}`
			if got := e.one(`SELECT state || ' ' || episodes || ' ' || episode_map FROM download WHERE id = 1`); got != want {
				t.Errorf("download %s, want %s", got, want)
			}
		})
	}
}
