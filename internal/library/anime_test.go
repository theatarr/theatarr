package library

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
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

// A SeaDex pack grabbed for an AniList entry split into parts (al:3#1, al:3#2: S01E01-E02) keeps its episodes while
// the parts stay importable, whether the download stored the shared entry (al:3) or no key (grabbed before the key was
// kept): S01E03 in the pack is not imported. Once a part is in conflict the pack imports through Sonarr's numbering.
func TestAnimeSplitPackScope(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, epMap, status string
		links               int
	}{
		{"shared entry", `{"how":"franchise","entry":"al:3"}`, "auto", 2},
		{"no key", `{"how":"franchise"}`, "auto", 2},
		{"part in conflict", `{"how":"franchise","entry":"al:3"}`, "conflict", 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, true, policy.ModeActive)
			e.isekai()
			e.exec(`INSERT INTO anime_entry (franchise, key, origin, title, body) VALUES
				('isekai', 'al:3#1', 'anilist', 'Other Story', '{}'), ('isekai', 'al:3#2', 'anilist', 'Other Story', '{}')`)
			e.exec(`INSERT INTO anime_mapping (franchise, entry_key, status, tvdb_series, episodes, reason, updated_at) VALUES
				('isekai', 'al:3#1', 'auto', 2002, '[[1,1]]', '', 0), ('isekai', 'al:3#2', ?, 2002, '[[1,2]]', '', 0)`, c.status)
			rel := "Isekai Story S01 1080p WEB-DL"
			e.release(rel, map[string]float64{"Isekai.Story.S01E01.1080p.WEB-DL.mkv": 1440,
				"Isekai.Story.S01E02.1080p.WEB-DL.mkv": 1440, "Isekai.Story.S01E03.1080p.WEB-DL.mkv": 1440})
			e.videoSize(rel, 1920, 1080)
			e.seriesDownload(1, 3, 30, hashA, rel, "[301,302]", c.epMap)
			plan, err := e.lib.Import(e.ctx, 1, false)
			if err != nil {
				t.Fatal(err)
			}
			if got := e.links(plan); len(plan.Links) != c.links || (c.links == 2) == strings.Contains(got, "S01E03") {
				t.Errorf("links\n%s\nunresolved %v", got, plan.Unresolved)
			}
		})
	}
}

// Review accepts the mapping of an entry in conflict while a download grabbed through Sonarr's numbering for it (a
// fallback: S01E01-E02) waits: the import re-scopes the download to the entry's episodes and links its files there,
// instead of holding them as out of scope.
func TestAnimeAcceptBeforeImport(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.isekai()
	e.exec(`UPDATE anime_mapping SET status = 'conflict' WHERE entry_key = 'al:2'`)
	e.exec(`INSERT INTO anime_review (id, franchise, entry_key, kind, state, summary, created_at)
		VALUES (8, 'isekai', 'al:2', 'conflict', 'open', 'al:2', 0)`)
	rel := "[Group] Isekai Story 2nd Season - 01-02 (BD 1080p)"
	e.release(rel, map[string]float64{"[Group] Isekai Story 2nd Season - 01 (BD 1080p).mkv": 1440,
		"[Group] Isekai Story 2nd Season - 02 (BD 1080p).mkv": 1440})
	e.videoSize(rel, 1920, 1080)
	e.seriesDownload(1, 3, 30, hashA, rel, "[301,302]", e.decidedMap(rel, `{"how":"franchise","entry":"al:2","fallback":true}`))
	as := &animestore.Store{DB: e.st, Now: func() time.Time { return e.now }}
	if err := as.Resolve(e.ctx, 8, true, ""); err != nil {
		t.Fatal(err)
	}
	if res, err := e.lib.RelinkAnime(e.ctx, 2002); err != nil || len(res.Moved)+len(res.Planned)+len(res.Occupied) != 0 {
		t.Fatalf("relink before the import %+v %v", res, err)
	}
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	got := e.links(plan)
	if len(plan.Links) != 2 || !strings.Contains(got, "S02E01") || !strings.Contains(got, "S02E02") {
		t.Fatalf("links\n%s\nunresolved %v", got, plan.Unresolved)
	}
	if got := e.one(`SELECT state || ' ' || episodes FROM download WHERE id = 1`); got != "imported [313,314]" {
		t.Errorf("download %s", got)
	}
}

// decidedMap is the download.episode_map the router stores for the decision on an isekai release (title 3, version
// 30) now; want pins it.
func (e *env) decidedMap(release, want string) string {
	e.t.Helper()
	t, err := decide.LoadTitle(e.ctx, e.st.Reader(), 3)
	if err == nil {
		err = decide.LoadSeriesVersion(e.ctx, e.st.Reader(), &t, 30)
	}
	if err != nil {
		e.t.Fatal(err)
	}
	d, err := e.lib.engine.Decide(e.ctx, decide.Release{Release: indexer.Release{Title: release}}, t,
		decide.Version{ID: 30, Key: "tv"}, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	b, _ := json.Marshal(d.EpisodeMapping())
	if string(b) != want {
		e.t.Fatalf("decision on %s: accepted %v, mapping %s, want %s", release, d.Accepted, b, want)
	}
	return string(b)
}

// firstBuild stands in for the series' first automatic build after a grab made with no franchise mapping: al:1 maps
// S01E01-E12 and then its 13th episode to S02E<last>.
func (e *env) firstBuild(last int) {
	e.t.Helper()
	e.exec(`INSERT INTO anime_mapping (franchise, entry_key, status, tvdb_series, episodes, reason, updated_at) VALUES
		('isekai', 'al:1', 'auto', 2002, ?, '', 0)`,
		fmt.Sprintf(`[[1,1],[1,2],[1,3],[1,4],[1,5],[1,6],[1,7],[1,8],[1,9],[1,10],[1,11],[1,12],[2,%d]]`, last))
}

// A download grabbed through Sonarr's numbering before the series had a franchise mapping waits while the first
// build maps its file elsewhere: the import re-scopes it to the build's episode instead of holding it out of scope.
// When the build agrees with Sonarr, the download keeps its episodes.
func TestAnimeFirstBuildBeforeImport(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		last          int
		link, episode string
	}{{2, "S02E02", "[314]"}, {1, "S02E01", "[313]"}} {
		t.Run(c.link, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			e.isekai()
			e.exec(`DELETE FROM anime_mapping`)
			rel := "[SubsPlease] Isekai Story - 13 (1080p WEB-DL)"
			e.release(rel, map[string]float64{rel + ".mkv": 1440})
			e.videoSize(rel, 1920, 1080)
			e.decidedMap(rel, "null")
			e.seriesDownload(1, 3, 30, hashA, rel, "[313]", nil)
			e.firstBuild(c.last)
			plan, err := e.lib.Import(e.ctx, 1, false)
			if err != nil {
				t.Fatal(err)
			}
			if got := e.links(plan); len(plan.Links) != 1 || !strings.Contains(got, c.link) {
				t.Fatalf("links\n%s\nunresolved %v", got, plan.Unresolved)
			}
			if got := e.one(`SELECT state || ' ' || episodes FROM download WHERE id = 1`); got != "imported "+c.episode {
				t.Errorf("download %s", got)
			}
		})
	}
}

// A file outside the download's episodes that the franchise does not place (an explicit SxxEyy) does not re-scope a
// download grabbed through Sonarr's numbering: the files in scope import, the other is held as before.
func TestAnimeRescopeIgnoresExplicitFile(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.isekai()
	e.exec(`DELETE FROM anime_mapping`)
	rel := "[Group] Isekai Story - 01-02 (1080p WEB-DL)"
	e.release(rel, map[string]float64{"[Group] Isekai Story - 01 (1080p WEB-DL).mkv": 1440,
		"[Group] Isekai Story - 02 (1080p WEB-DL).mkv": 1440, "Isekai Story S01E03 1080p WEB-DL.mkv": 1440})
	e.videoSize(rel, 1920, 1080)
	e.decidedMap(rel, "null")
	e.seriesDownload(1, 3, 30, hashA, rel, "[301,302]", nil)
	e.firstBuild(1)
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.links(plan); len(plan.Links) != 2 || !strings.Contains(got, "S01E01") || !strings.Contains(got, "S01E02") {
		t.Fatalf("links\n%s\nunresolved %v", got, plan.Unresolved)
	}
	if got := e.one(`SELECT episodes FROM download WHERE id = 1`); got != "[301,302]" {
		t.Errorf("download episodes %s", got)
	}
}

// A release names an entry in conflict whose proposed candidate (S02E01-E02) cannot hold its number: the grab places
// it through Sonarr's numbering (S01E05) and remembers the entry. Review then picks a candidate before the import:
// one that holds the number re-scopes the download to it (S02E05); one that does not leaves it at S01E05.
func TestAnimeCandidateBeforeImport(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		candidate, link, episodes string
	}{
		{"[[2,1],[2,2],[2,3],[2,4],[2,5]]", "S02E05", "[317]"},
		{"[[2,1],[2,2]]", "S01E05", "[305]"},
	} {
		t.Run(c.link, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			e.isekai()
			e.exec(`UPDATE anime_mapping SET status = 'conflict' WHERE entry_key = 'al:2'`)
			for n := 3; n <= 5; n++ {
				e.exec(`INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at)
					VALUES (?, 3, 2, ?, 'Episode', 0, 0)`, 312+n, n)
				e.exec(`INSERT INTO episode_number (episode_id, title_id, scheme, number, source)
					VALUES (?, 3, 'absolute_tvdb', ?, 'tvdb')`, 312+n, 12+n)
			}
			e.exec(`INSERT INTO anime_review (id, franchise, entry_key, kind, state, summary, detail, created_at)
				VALUES (8, 'isekai', 'al:2', 'conflict', 'open', 'al:2', ?, 0)`,
				`{"candidates":[{"source":"scudlee","series_id":2002,"episodes":`+c.candidate+`}]}`)
			rel := "[Group] Isekai Story 2nd Season - 05 (1080p WEB-DL)"
			e.release(rel, map[string]float64{rel + ".mkv": 1440})
			e.videoSize(rel, 1920, 1080)
			e.seriesDownload(1, 3, 30, hashA, rel, "[305]",
				e.decidedMap(rel, `{"how":"franchise","entry":"al:2","fallback":true}`))
			as := &animestore.Store{DB: e.st, Now: func() time.Time { return e.now }}
			if err := as.ResolveCandidate(e.ctx, 8, 0, ""); err != nil {
				t.Fatal(err)
			}
			plan, err := e.lib.Import(e.ctx, 1, false)
			if err != nil {
				t.Fatal(err)
			}
			if got := e.links(plan); len(plan.Links) != 1 || !strings.Contains(got, c.link) {
				t.Fatalf("links\n%s\nunresolved %v", got, plan.Unresolved)
			}
			if got := e.one(`SELECT state || ' ' || episodes FROM download WHERE id = 1`); got != "imported "+c.episodes {
				t.Errorf("download %s", got)
			}
		})
	}
}

// Known limit: a fallback pack whose files carry the name of another entry (the franchise's base title, al:1) while
// the release names al:2. Review accepts al:2 before the import: the files name al:1, which places them inside the
// download's episodes, so they import at S01E01-E02 (the file name outranks the stored identity) rather than being
// held.
func TestAnimeFallbackFilesNameOtherEntry(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.isekai()
	e.exec(`UPDATE anime_mapping SET status = 'conflict' WHERE entry_key = 'al:2'`)
	rel := "[Group] Isekai Story 2nd Season - 01-02 (1080p WEB-DL)"
	e.release(rel, map[string]float64{"[Group] Isekai Story - 01 (1080p WEB-DL).mkv": 1440,
		"[Group] Isekai Story - 02 (1080p WEB-DL).mkv": 1440})
	e.videoSize(rel, 1920, 1080)
	e.seriesDownload(1, 3, 30, hashA, rel, "[301,302]",
		e.decidedMap(rel, `{"how":"franchise","entry":"al:2","fallback":true}`))
	e.exec(`UPDATE anime_mapping SET status = 'override' WHERE entry_key = 'al:2'`)
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.links(plan); len(plan.Links) != 2 || !strings.Contains(got, "S01E01") || !strings.Contains(got, "S01E02") {
		t.Fatalf("links\n%s\nunresolved %v", got, plan.Unresolved)
	}
}
