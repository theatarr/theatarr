package library

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/metadata/anime/animestore"
)

// TestAnimeUnreviewedImports: an episode only a conflict mapping covers is not held; it imports through Sonarr's
// numbering like the others, its review item stays. Accepting the mapping later re-links only files the mapping
// places elsewhere (TestAnimeRelinkAfterReview); this one's range agrees with Sonarr's S01E01, so nothing moves.
func TestAnimeUnreviewedImports(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
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
			e := newEnv(t, true)
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
			e := newEnv(t, true)
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
	e := newEnv(t, true)
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
// build runs. When the build agrees with Sonarr, the download imports at its episode. When the build maps its file
// elsewhere, the download stored no entry to follow, so it is not re-scoped: the file is held as out of scope, with
// the download ready and its episodes kept (a known limit, docs/design/anime.md).
func TestAnimeFirstBuildBeforeImport(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		last int
		held bool
	}{{"agrees", 1, false}, {"disagrees", 2, true}} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, true)
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
			if c.held {
				e.held(plan, 1, "[313]", "", "")
				if why := plan.Unresolved[rel+".mkv"]; !strings.Contains(why, "maps to S02E02") {
					t.Errorf("held as %q", why)
				}
				return
			}
			if got := e.links(plan); len(plan.Links) != 1 || !strings.Contains(got, "S02E01") {
				t.Fatalf("links\n%s\nunresolved %v", got, plan.Unresolved)
			}
			if got := e.one(`SELECT state || ' ' || episodes FROM download WHERE id = 1`); got != "imported [313]" {
				t.Errorf("download %s", got)
			}
		})
	}
}

// A file outside the download's episodes (an explicit SxxEyy) does not re-scope a download grabbed through Sonarr's
// numbering when the first build runs before its import: the files in scope import, the other is held as before.
func TestAnimeRescopeIgnoresExplicitFile(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
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
			e := newEnv(t, true)
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
// the release names al:2. The file name outranks the stored identity: review accepting al:2 before the import leaves
// the files at al:1's S01E01-E02 (inside the download's episodes, so not held), and accepting it after the import
// re-links nothing.
func TestAnimeFallbackFilesNameOtherEntry(t *testing.T) {
	t.Parallel()
	for _, before := range []bool{true, false} {
		t.Run(map[bool]string{true: "before", false: "after"}[before], func(t *testing.T) {
			e := newEnv(t, true)
			e.isekai()
			e.exec(`UPDATE anime_mapping SET status = 'conflict' WHERE entry_key = 'al:2'`)
			rel := "[Group] Isekai Story 2nd Season - 01-02 (1080p WEB-DL)"
			e.release(rel, map[string]float64{"[Group] Isekai Story - 01 (1080p WEB-DL).mkv": 1440,
				"[Group] Isekai Story - 02 (1080p WEB-DL).mkv": 1440})
			e.videoSize(rel, 1920, 1080)
			e.seriesDownload(1, 3, 30, hashA, rel, "[301,302]",
				e.decidedMap(rel, `{"how":"franchise","entry":"al:2","fallback":true}`))
			if before {
				e.exec(`UPDATE anime_mapping SET status = 'override' WHERE entry_key = 'al:2'`)
			}
			plan, err := e.lib.Import(e.ctx, 1, false)
			if err != nil {
				t.Fatal(err)
			}
			if got := e.links(plan); len(plan.Links) != 2 || !strings.Contains(got, "S01E01") || !strings.Contains(got, "S01E02") {
				t.Fatalf("links\n%s\nunresolved %v", got, plan.Unresolved)
			}
			if !before {
				e.exec(`UPDATE anime_mapping SET status = 'override' WHERE entry_key = 'al:2'`)
				res, err := e.lib.RelinkAnime(e.ctx, 2002)
				if err != nil || len(res.Moved)+len(res.Planned)+len(res.Occupied) != 0 {
					t.Fatalf("relink %+v %v", res, err)
				}
			}
			if got := e.one(`SELECT group_concat(episode_id, ' ') FROM (SELECT le.episode_id FROM link l
				JOIN link_episode le ON le.link_id = l.id WHERE l.download_id = 1 AND l.state = 'active' ORDER BY 1)`); got != "301 302" {
				t.Errorf("links hold %s", got)
			}
		})
	}
}

// kaiju is an anime series of three seasons (3, 3 and 4 episodes; ids 6s0n, absolute 1 to 10) with version 60.
func (e *env) kaiju() {
	e.exec(`INSERT INTO title (id, kind, name, year, tvdb_id, series_type, runtime_minutes, created_at, updated_at)
		VALUES (6, 'series', 'Kaiju Tale', 2019, 6006, 'anime', 24, 0, 0)`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (60, 6, 'tv', 0, 0)`)
	abs := 0
	for s, n := range []int{3, 3, 4} {
		for i := 1; i <= n; i++ {
			abs++
			id := 600 + 10*(s+1) + i
			e.exec(`INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at) VALUES (?, 6, ?, ?, 'Episode', 0, 0)`,
				id, s+1, i)
			e.exec(`INSERT INTO episode_number (episode_id, title_id, scheme, number, source) VALUES (?, 6, 'absolute_tvdb', ?, 'tvdb')`, id, abs)
		}
	}
}

// kaijuIDs are kaiju's episode ids of the seasons (all when none).
func kaijuIDs(seasons ...int) []int64 {
	var out []int64
	for s, n := range []int{3, 3, 4} {
		if len(seasons) > 0 && !slices.Contains(seasons, s+1) {
			continue
		}
		for i := 1; i <= n; i++ {
			out = append(out, int64(600+10*(s+1)+i))
		}
	}
	return out
}

// kaijuClaim is the stored mapping of a pack of kaiju's three seasons: named (seasons) or absolute 1 to 10.
func kaijuClaim(mode string) string {
	c := decide.PackClaim{Mode: mode, Seasons: []int{1, 2, 3}, Episodes: kaijuIDs()}
	if mode == decide.PackAbsolute {
		for i, id := range c.Episodes {
			c.Numbers = append(c.Numbers, decide.PackNumber{N: i + 1, Episode: id})
		}
	}
	b, _ := json.Marshal(decide.EpisodeMap{Pack: &c})
	return string(b)
}

// kaijuFiles names kaiju's ten episodes: "S01E01" style, "Season N/... - 0n" style, or absolute "- 0n".
func kaijuFiles(style string) map[string]float64 {
	out := map[string]float64{}
	abs := 0
	for s, n := range []int{3, 3, 4} {
		for i := 1; i <= n; i++ {
			abs++
			switch style {
			case "SxxEyy":
				out[fmt.Sprintf("Kaiju.Tale.S%02dE%02d.1080p.BluRay.x264-GRP.mkv", s+1, i)] = 1440
			case "folders":
				out[fmt.Sprintf("Season %d/[GRP] Kaiju Tale - %02d [BD 1080p].mkv", s+1, i)] = 1440
			case "absolute":
				out[fmt.Sprintf("[GRP] Kaiju Tale - %02d [BD 1080p].mkv", abs)] = 1440
			}
		}
	}
	return out
}

// importKaiju imports a pack of kaiju's seasons for download 1 with the files and the stored mapping, the download
// given episodes eps.
func (e *env) importKaiju(release string, files map[string]float64, epMap string, eps []int64) *Plan {
	e.t.Helper()
	e.release(release, files)
	e.videoSize(release, 1920, 1080)
	js, _ := json.Marshal(eps)
	e.seriesDownload(1, 6, 60, hashA, release, string(js), epMap)
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		e.t.Fatal(err)
	}
	return plan
}

// linkedTo is every link's file and its episode, as "S01E01 <- file".
func (e *env) linkedTo() []string {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT printf('S%02dE%02d', ep.season, ep.number), cf.path FROM link l
		JOIN link_episode le ON le.link_id = l.id JOIN episode ep ON ep.id = le.episode_id
		JOIN content_file cf ON cf.id = l.content_file_id WHERE l.title_id = 6 ORDER BY ep.season, ep.number`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var se, f string
		if err := rows.Scan(&se, &f); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, se+" <- "+f)
	}
	return out
}

// An anime pack of several seasons imports only when its files match its stored claim one to one; each file then
// links to its own episode, by its own season and number, by its season folder, or by its absolute number.
func TestAnimePackProved(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, release, style, mode string }{
		{"named, per-season files", "[GRP] Kaiju Tale S01-S03 [BD 1080p]", "SxxEyy", decide.PackSeasons},
		{"named, season folders", "[GRP] Kaiju Tale Seasons 1-3 [BD 1080p]", "folders", decide.PackSeasons},
		{"absolute batch", "[GRP] Kaiju Tale (01-10) [BD 1080p]", "absolute", decide.PackAbsolute},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, true)
			e.kaiju()
			plan := e.importKaiju(c.release, kaijuFiles(c.style), kaijuClaim(c.mode), kaijuIDs())
			if len(plan.Links) != 10 {
				t.Fatalf("links %d, unresolved %v, why %q", len(plan.Links), plan.Unresolved, plan.Why)
			}
			got := e.linkedTo()
			abs := 0
			for s, n := range []int{3, 3, 4} {
				for i := 1; i <= n; i++ {
					abs++
					want := map[string]string{"SxxEyy": fmt.Sprintf("S%02dE%02d.", s+1, i),
						"folders":  fmt.Sprintf("Season %d/[GRP] Kaiju Tale - %02d ", s+1, i),
						"absolute": fmt.Sprintf("Kaiju Tale - %02d ", abs)}[c.style]
					line := got[abs-1]
					if !strings.HasPrefix(line, fmt.Sprintf("S%02dE%02d <- ", s+1, i)) || !strings.Contains(line, want) {
						t.Errorf("link %d: %s", abs, line)
					}
				}
			}
			if s := e.one(`SELECT state FROM download WHERE id = 1`); s != "imported" {
				t.Errorf("download %s", s)
			}
		})
	}
}

// A pack whose numbering cannot be trusted is rejected whole: nothing linked, the download failed, the release
// blocklisted for the library (its seasons are then searched one by one), never held for review.
func TestAnimePackProofFails(t *testing.T) {
	t.Parallel()
	dup := kaijuFiles("SxxEyy")
	dup["Kaiju.Tale.S02E01.1080p.BluRay.x264-OTHER.mkv"] = 1440
	restart := map[string]float64{}
	for s, n := range []int{3, 3, 4} {
		for i := 1; i <= n; i++ {
			restart[fmt.Sprintf("Season %d/[GRP] Kaiju Tale - %02d [BD 1080p].mkv", s+1, i)] = 1440
		}
	}
	missing := kaijuFiles("SxxEyy")
	delete(missing, "Kaiju.Tale.S03E04.1080p.BluRay.x264-GRP.mkv")
	outside := kaijuFiles("absolute")
	outside["[GRP] Kaiju Tale - 11 [BD 1080p].mkv"] = 1440
	recap := kaijuFiles("folders")
	delete(recap, "Season 1/[GRP] Kaiju Tale - 01 [BD 1080p].mkv")
	recap["Season 1/[GRP] Kaiju Tale - Recap 01 2160p [WEB].mkv"] = 1440
	seasonVideo := kaijuFiles("SxxEyy")
	seasonVideo["Kaiju Tale S02 1080p.mkv"] = 1440
	for _, c := range []struct {
		name, release, mode string
		files               map[string]float64
		why                 string
	}{
		{"a recap in place of an episode", "[GRP] Kaiju Tale Seasons 1-3 [BD 1080p]", decide.PackSeasons, recap, "no file for S01E01"},
		{"a whole-season video without a number", "[GRP] Kaiju Tale S01-S03 [BD 1080p]", decide.PackSeasons, seasonVideo,
			"reads as no episode: Kaiju Tale S02 1080p.mkv"},
		{"two files on one episode", "[GRP] Kaiju Tale S01-S03 [BD 1080p]", decide.PackSeasons, dup, "two files on S02E01"},
		{"restarting files under an absolute name", "[GRP] Kaiju Tale (01-10) [BD 1080p]", decide.PackAbsolute, restart,
			"a file numbered 1 sits in a season 2 folder"},
		{"absolute files under a season name", "[GRP] Kaiju Tale S01-S03 [BD 1080p]", decide.PackSeasons,
			kaijuFiles("absolute"), "absolute-numbered file under a season name"},
		{"a claimed episode without a file", "[GRP] Kaiju Tale S01-S03 [BD 1080p]", decide.PackSeasons, missing,
			"no file for S03E04"},
		{"a regular file outside the claim", "[GRP] Kaiju Tale (01-10) [BD 1080p]", decide.PackAbsolute, outside,
			"outside the pack's numbers (11)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, true)
			e.kaiju()
			plan := e.importKaiju(c.release, c.files, kaijuClaim(c.mode), kaijuIDs())
			if len(plan.Links) != 0 || len(e.linkedTo()) != 0 {
				t.Fatalf("linked %v", e.linkedTo())
			}
			if !strings.HasPrefix(plan.Why, "pack numbering: ") || !strings.Contains(plan.Why, c.why) {
				t.Errorf("why %q, want %q", plan.Why, c.why)
			}
			if got := e.one(`SELECT d.state || ' ' || li.state || ' ' || (SELECT COUNT(*) FROM router_blocklist b WHERE b.info_hash = d.info_hash
				AND b.source = 'library' AND b.version_key = 'tv') FROM download d JOIN library_import li ON li.download_id = d.id
				WHERE d.id = 1`); got != "failed rejected 1" {
				t.Errorf("download, import, blocklist: %s", got)
			}
		})
	}
}

// Extras beside the episodes are set aside before the proof and never linked: OVA, SP01, NCOP1, NCED, recap, a
// decimal "12.5", an Extras folder. "5.1" audio and a series title with "Special" in it are not extras.
func TestAnimePackExtras(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.kaiju()
	files := kaijuFiles("absolute")
	for _, x := range []string{"[GRP] Kaiju Tale - OVA [BD 1080p].mkv", "[GRP] Kaiju Tale - SP01 [BD 1080p].mkv",
		"[GRP] Kaiju Tale - NCOP1 [BD 1080p].mkv", "[GRP] Kaiju Tale - NCED [BD 1080p].mkv",
		"[GRP] Kaiju Tale - Recap [BD 1080p].mkv", "[GRP] Kaiju Tale - 03.5 [BD 1080p].mkv",
		"Extras/[GRP] Kaiju Tale - 01 Making Of [BD 1080p].mkv"} {
		files[x] = 1440
	}
	delete(files, "[GRP] Kaiju Tale - 02 [BD 1080p].mkv")
	files["[GRP] Kaiju Tale - 02 [BD 1080p AAC 5.1].mkv"] = 1440
	plan := e.importKaiju("[GRP] Kaiju Tale (01-10) [BD 1080p]", files, kaijuClaim(decide.PackAbsolute), kaijuIDs())
	if len(plan.Links) != 10 {
		t.Fatalf("links %d, why %q, unresolved %v", len(plan.Links), plan.Why, plan.Unresolved)
	}
	for _, l := range plan.Links {
		if strings.Contains(l.File, "OVA") || strings.Contains(l.File, "SP01") || strings.Contains(l.File, "NC") ||
			strings.Contains(l.File, "Recap") || strings.Contains(l.File, "03.5") || strings.HasPrefix(l.File, "Extras/") {
			t.Errorf("an extra is linked: %s", l.File)
		}
	}
	// Rule S: a marker anywhere after the library title sets a file aside ("Special Forces" is not this title).
	for name, want := range map[string]bool{"[GRP] Special Forces - 05 [BD 1080p].mkv": true,
		"Special.Forces.S01E05.1080p.DDP5.1.mkv": true, "[GRP] Kaiju Tale - 05 [BD 1080p AAC 5.1].mkv": false,
		"[GRP] Kaiju Tale - NCED2 [BD].mkv": true, "Specials/[GRP] Kaiju Tale - 01.mkv": true, "Kaiju.Tale.NCOP1.1080p.mkv": true,
		"[GRP] Kaiju Tale - 12.5 [BD].mkv": true, "Season 1/[GRP] Kaiju Tale - Recap 01 2160p [WEB].mkv": true,
		"[GRP] Kaiju Tale OVA 01.mkv": true, "[GRP] Kaiju Tale NCOP 01.mkv": true, "[GRP] Kaiju Tale - OVA - 01.mkv": true,
		"Kaiju Tale S02 1080p.mkv": false, "[GRP] Kaiju Tale - 05 [ED].mkv": true,
		"[GRP] Kaiju Tale - 05 - Special Training [BD 1080p].mkv": true, "Kaiju.Tale.S01E05.Special.1080p.mkv": true,
		"[GRP] Kaiju Tale - S01E05 - Preview [1080p].mkv": true, "Extras/NCED.mkv": true, "NCOP1.mkv": true} {
		stem := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
		if got := packExtra(name, sonarr.ParseTitle(stem), indexer.NewTitleMatcher("Kaiju Tale"), newPackDirs()); got != want {
			t.Errorf("%s: extra %v, want %v", name, got, want)
		}
	}
}

// A narrowed pack (the download given seasons 1 and 2) is proved over the whole pack; only its own seasons are
// linked and season 3's files stay out of scope. A proved pack with nothing it may link is rejected, not held.
func TestAnimePackNarrowed(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.kaiju()
	plan := e.importKaiju("[GRP] Kaiju Tale S01-S03 [BD 1080p]", kaijuFiles("SxxEyy"), kaijuClaim(decide.PackSeasons),
		kaijuIDs(1, 2))
	if len(plan.Links) != 6 || len(plan.Unresolved) != 4 {
		t.Fatalf("links %d, unresolved %v, why %q", len(plan.Links), plan.Unresolved, plan.Why)
	}
	for f, why := range plan.Unresolved {
		if !strings.Contains(f, "S03E") || !strings.HasPrefix(why, "out of scope") {
			t.Errorf("%s: %s", f, why)
		}
	}

	// Nothing to place: a proved pack of seasons 1 and 2 given only season 3's episodes is rejected, not held.
	e2 := newEnv(t, true)
	e2.kaiju()
	claim := decide.PackClaim{Mode: decide.PackAbsolute, Seasons: []int{1, 2}, Episodes: kaijuIDs(1, 2)}
	for i, id := range claim.Episodes {
		claim.Numbers = append(claim.Numbers, decide.PackNumber{N: i + 1, Episode: id})
	}
	b, _ := json.Marshal(decide.EpisodeMap{Pack: &claim})
	files := map[string]float64{}
	for n := 1; n <= 6; n++ {
		files[fmt.Sprintf("[GRP] Kaiju Tale - %02d [BD 1080p].mkv", n)] = 1440
	}
	plan = e2.importKaiju("[GRP] Kaiju Tale (01-06) [BD 1080p]", files, string(b), kaijuIDs(3))
	if len(plan.Links) != 0 || !strings.HasPrefix(plan.Why, "pack numbering: no file of the pack is for the episodes") {
		t.Fatalf("links %d, why %q", len(plan.Links), plan.Why)
	}
	if s := e2.one(`SELECT state FROM download WHERE id = 1`); s != "failed" {
		t.Errorf("download %s", s)
	}
}

// A narrowed pack whose only file for its episode also holds an episode it was not given has nothing it can place:
// rejected, not held.
func TestAnimePackOverlapOnlyIsRejected(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.kaiju()
	files := map[string]float64{"[GRP] Kaiju Tale - 01-02 [BD 1080p].mkv": 2880, "[GRP] Kaiju Tale - 03-04 [BD 1080p].mkv": 2880,
		"[GRP] Kaiju Tale - 05-06 [BD 1080p].mkv": 2880, "[GRP] Kaiju Tale - 07-08 [BD 1080p].mkv": 2880,
		"[GRP] Kaiju Tale - 09-10 [BD 1080p].mkv": 2880}
	plan := e.importKaiju("[GRP] Kaiju Tale (01-10) [BD 1080p]", files, kaijuClaim(decide.PackAbsolute), []int64{621})
	if len(plan.Links) != 0 || !strings.HasPrefix(plan.Why, "pack numbering: no file of the pack is for the episodes") {
		t.Fatalf("links %d, why %q", len(plan.Links), plan.Why)
	}
	if s := e.one(`SELECT state FROM download WHERE id = 1`); s != "failed" {
		t.Errorf("download %s", s)
	}
}

// A proved pack that is merely not an upgrade ends as any other download does: not imported, not blocklisted, no
// numbering reason (section 13 point 7).
func TestAnimePackNotAnUpgradeEndsAsToday(t *testing.T) {
	t.Parallel()
	e := importUpEnv(t, "")
	e.kaiju()
	for i, id := range kaijuIDs() {
		release := fmt.Sprintf("Kaiju.Tale.E%02d.2160p.BluRay.REMUX.HEVC-OLD", i+1)
		p := filepath.Join(e.shows, "Kaiju Tale (2019)", release+".mkv")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(e.mount, "__all__", "Held", release+".mkv")
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
		e.exec(`INSERT INTO link (id, path, target, kind, title_id, release_title, quality, state, created_at, updated_at)
			VALUES (?, ?, ?, 'symlink', 6, ?, 'Bluray-2160p Remux', 'active', 0, 0)`, 100+i, p, target, release)
		e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (?, 60)`, 100+i)
		e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (?, ?)`, 100+i, id)
	}
	plan := e.importKaiju("[GRP] Kaiju Tale S01-S03 [BD 1080p]", kaijuFiles("SxxEyy"), kaijuClaim(decide.PackSeasons), kaijuIDs())
	if len(plan.Links) != 0 || plan.Why != "not an upgrade for the existing files" {
		t.Fatalf("links %d, why %q", len(plan.Links), plan.Why)
	}
	if got := e.one(`SELECT COUNT(*) FROM router_blocklist`) + "/" + e.one(`SELECT state FROM download WHERE id = 1`); got != "0/ready" {
		t.Errorf("blocklist rows/download state %s", got)
	}
}

// A season-folder pack proved onto its episodes is left alone by the relink after a franchise mapping is accepted:
// "Season 2/[GRP] Kaiju Tale - 01" reads there as number 1 of the entry named "Kaiju Tale".
func TestAnimePackRelinkLeavesItAlone(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.kaiju()
	plan := e.importKaiju("[GRP] Kaiju Tale Seasons 1-3 [BD 1080p]", kaijuFiles("folders"), kaijuClaim(decide.PackSeasons), kaijuIDs())
	if len(plan.Links) != 10 {
		t.Fatalf("links %d, why %q", len(plan.Links), plan.Why)
	}
	before := e.linkedTo()
	e.exec(`INSERT INTO anime_franchise (slug, name, seed_anilist, created_at, updated_at) VALUES ('kaiju', 'Kaiju', 1, 0, 0)`)
	e.exec(`INSERT INTO anime_entry (franchise, key, origin, title, body) VALUES ('kaiju', 'al:1', 'anilist', 'Kaiju Tale', '{}')`)
	e.exec(`INSERT INTO anime_mapping (franchise, entry_key, status, tvdb_series, episodes, reason, updated_at) VALUES
		('kaiju', 'al:1', 'override', 6006, '[[1,1],[1,2],[1,3],[2,1],[2,2],[2,3],[3,1],[3,2],[3,3],[3,4]]', '', 0)`)
	if res, err := e.lib.RelinkAnime(e.ctx, 6006); err != nil || len(res.Moved)+len(res.Planned)+len(res.Occupied) != 0 {
		t.Fatalf("relink %+v %v", res, err)
	}
	if after := e.linkedTo(); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("links moved:\n%s", strings.Join(after, "\n"))
	}
}

// A listing the mount has not confirmed waits as today before the proof counts a file missing: the import neither
// rejects nor blocklists.
func TestAnimePackUnconfirmedListingWaits(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.kaiju()
	release := "[GRP] Kaiju Tale S01-S03 [BD 1080p]"
	files := kaijuFiles("SxxEyy")
	srv := newZurgServer(t)
	srv.torrents = []ZurgTorrent{{Name: release, Hash: hashA, Size: 50}}
	for f := range files {
		size := int64(5)
		if strings.Contains(f, "S03E04") {
			size = 6 // the mount shows 5 bytes: not confirmed
		}
		srv.entries[davPath(release, f)] = ZurgEntry{Hash: hashA, Name: filepath.Base(f), Size: size}
	}
	e.withZurg(srv)
	plan := e.importKaiju(release, files, kaijuClaim(decide.PackSeasons), kaijuIDs())
	if len(plan.Links) != 0 || !plan.Waiting {
		t.Fatalf("links %d, waiting %v, why %q", len(plan.Links), plan.Waiting, plan.Why)
	}
	if got := e.one(`SELECT state || ' ' || (SELECT COUNT(*) FROM router_blocklist) FROM download WHERE id = 1`); got != "ready 0" {
		t.Errorf("download, blocklist: %s", got)
	}
}

// Section 9: an anime absolute batch across a season break taken by a one-season search or RSS claims its numbers
// with no span; well numbered it imports as on main, with a duplicate number it is rejected.
func TestAnimeBatchAcrossBreakIsProved(t *testing.T) {
	t.Parallel()
	claim := decide.PackClaim{Mode: decide.PackAbsolute, Episodes: []int64{612, 613, 621, 622}}
	for i, id := range claim.Episodes {
		claim.Numbers = append(claim.Numbers, decide.PackNumber{N: i + 2, Episode: id})
	}
	b, _ := json.Marshal(decide.EpisodeMap{Pack: &claim})
	files := map[string]float64{}
	for n := 2; n <= 5; n++ {
		files[fmt.Sprintf("[GRP] Kaiju Tale - %02d [BD 1080p].mkv", n)] = 1440
	}
	e := newEnv(t, true)
	e.kaiju()
	plan := e.importKaiju("[GRP] Kaiju Tale - 02-05 [BD 1080p]", files, string(b), claim.Episodes)
	if len(plan.Links) != 4 {
		t.Fatalf("well numbered: links %d, why %q", len(plan.Links), plan.Why)
	}
	if got := strings.Join(e.linkedTo(), "\n"); !strings.Contains(got, "S02E01 <- [GRP] Kaiju Tale - 04") {
		t.Errorf("links\n%s", got)
	}
	files["[GRP] Kaiju Tale - 04 [BD 1080p] (v2).mkv"] = 1440
	e2 := newEnv(t, true)
	e2.kaiju()
	plan = e2.importKaiju("[GRP] Kaiju Tale - 02-05 [BD 1080p]", files, string(b), claim.Episodes)
	if len(plan.Links) != 0 || !strings.HasPrefix(plan.Why, "pack numbering: two files on S02E01") {
		t.Fatalf("duplicate: links %d, why %q", len(plan.Links), plan.Why)
	}
}

// A span carries no SeaDex standing as a release (section 13 point 3). On a library that does not prefer SeaDex, a
// span whose stored release carries a SeaDex feed row's marks (listed for season 1 only) stores no SeaDex label on any
// file; on a library that prefers SeaDex the import still stores each proved file's own standing.
func TestAnimeSpanImportSeaDex(t *testing.T) {
	t.Parallel()
	for _, prefer := range []bool{false, true} {
		e := newEnv(t, true)
		e.kaiju()
		e.pol.Versions["tv"].PreferSeaDex = prefer
		e.exec(`INSERT INTO title_external_id VALUES(6,'anilist','11')`)
		e.exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','11',200,?,0,253402214400000)`,
			fmt.Sprintf(`{"anilist":11,"torrents":[{"info_hash":"%s","preference":2,"is_best":true,"facts":{"isBest":true}}]}`, hashA))
		release := "[GRP] Kaiju Tale S01-S03 [WEB-DL 1080p]"
		files := map[string]float64{}
		for f := range kaijuFiles("SxxEyy") {
			files[strings.Replace(f, "BluRay.x264", "WEB-DL.H264", 1)] = 1440
		}
		e.release(release, files)
		e.videoSize(release, 1920, 1080)
		js, _ := json.Marshal(kaijuIDs())
		e.seriesDownload(1, 6, 60, hashA, release, string(js), kaijuClaim(decide.PackSeasons))
		raw, _ := json.Marshal(indexer.Release{InfoHash: hashA, Flags: []string{indexer.FlagSeaDex, indexer.FlagSeaDexBest},
			Extras: map[string]string{"anilist": "11"}})
		e.exec(`UPDATE download SET release_data = ? WHERE id = 1`, string(raw))
		plan, err := e.lib.Import(e.ctx, 1, false)
		if err != nil || len(plan.Links) != 10 {
			t.Fatalf("prefer=%v: links %d %v (%q)", prefer, len(plan.Links), err, plan.Why)
		}
		labelled := e.one(`SELECT COUNT(*) FROM link WHERE title_id = 6 AND state = 'active' AND
			(COALESCE(json_extract(file_metadata,'$.SeaDexPreference'), 0) > 0 OR COALESCE(json_extract(file_metadata,'$.SeaDex'), 0) > 0)`)
		if !prefer && labelled != "0" {
			t.Errorf("not prefer: %s files carry a SeaDex label: %s", labelled, e.one(`SELECT file_metadata FROM link WHERE title_id = 6 LIMIT 1`))
		}
		if prefer && labelled == "0" {
			t.Errorf("prefer: no proved file stored its own SeaDex standing")
		}
	}
}

// A release with explicit season and episode numbers across a season break ("S01E03-S02E01") claims nothing and
// imports as on main (section 9).
func TestAnimeExplicitAcrossBreakImportsAsBefore(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	e.kaiju()
	release := "Kaiju.Tale.S01E03-S02E01.1080p.BluRay.x264-GRP"
	e.release(release, map[string]float64{"Kaiju.Tale.S01E03.1080p.BluRay.x264-GRP.mkv": 1440,
		"Kaiju.Tale.S02E01.1080p.BluRay.x264-GRP.mkv": 1440})
	e.videoSize(release, 1920, 1080)
	e.seriesDownload(1, 6, 60, hashA, release, "[613, 621]", nil)
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil || len(plan.Links) != 2 {
		t.Fatalf("links %d %v (%q)", len(plan.Links), err, plan.Why)
	}
	if got := strings.Join(e.linkedTo(), "\n"); !strings.Contains(got, "S01E03 <- Kaiju.Tale.S01E03") || !strings.Contains(got, "S02E01 <- Kaiju.Tale.S02E01") {
		t.Errorf("links\n%s", got)
	}
}

// proofTitle is a two-season anime series of 12 episodes each (ids 100*season+number, absolute 1 to 24) named name,
// with the claim of a pack of both seasons: by its seasons or as an absolute batch.
func proofTitle(name, mode string) (*decide.Title, *decide.PackClaim) {
	t := &decide.Title{ID: 9, Kind: "series", Name: name, SeriesType: "anime"}
	c := &decide.PackClaim{Mode: mode, Seasons: []int{1, 2}}
	for s := 1; s <= 2; s++ {
		for n := 1; n <= 12; n++ {
			id := int64(100*s + n)
			t.Episodes = append(t.Episodes, decide.Episode{ID: id, Season: s, Number: n, Absolute: 12*(s-1) + n})
			c.Episodes = append(c.Episodes, id)
			if mode == decide.PackAbsolute {
				c.Numbers = append(c.Numbers, decide.PackNumber{N: 12*(s-1) + n, Episode: id})
			}
		}
	}
	return t, c
}

// proofFiles names a pack's files: pat takes the season and the number, each file of both seasons, less skip
// (100*season+number).
func proofFiles(skip map[int]bool, pat func(season, number int) string) []string {
	var out []string
	for s := 1; s <= 2; s++ {
		for n := 1; n <= 12; n++ {
			if !skip[100*s+n] {
				out = append(out, pat(s, n))
			}
		}
	}
	return out
}

// absFiles names an absolute batch's files 1 to 24, less skip.
func absFiles(skip map[int]bool, pat string) []string {
	var out []string
	for n := 1; n <= 24; n++ {
		if !skip[n] {
			out = append(out, fmt.Sprintf(pat, n))
		}
	}
	return out
}

// Pack layouts the proof takes or rejects (the coordinator's comparison table): every valid layout whose season stays
// explicit is taken and its watched file placed on its own episode; every layout whose numbering cannot be trusted,
// and bare-number stems (no proof of what they number; docs/not-supported.md), are rejected.
func TestAnimePackLayouts(t *testing.T) {
	t.Parallel()
	no := map[int]bool{}
	sf := func(pat string) func(int, int) string {
		return func(s, n int) string { return fmt.Sprintf(pat, s, n) }
	}
	first12 := map[int]bool{}
	for n := 13; n <= 24; n++ {
		first12[n] = true
	}
	season2 := map[int]bool{}
	for n := 1; n <= 12; n++ {
		season2[200+n] = true
	}
	for _, c := range []struct {
		name, series, mode string
		ok                 bool
		files              []string
		watch              string
		at                 int64
	}{
		{"01 plain SxxEyy", "Show", decide.PackSeasons, true, proofFiles(no, sf("Show S%02dE%02d.mkv")), "Show S02E03.mkv", 203},
		{"02 season folders, absolute-style names", "Show", decide.PackSeasons, true, proofFiles(no, sf("Season %d/Show - %02d.mkv")), "Season 2/Show - 03.mkv", 203},
		{"03 season folders, bare stems", "Show", decide.PackSeasons, false, proofFiles(no, sf("Season %d/%02d.mkv")), "", 0},
		{"04 range folder outside", "Show", decide.PackSeasons, true, proofFiles(no, sf("Show Season 1 ~ 2/Show S%02dE%02d.mkv")), "Show Season 1 ~ 2/Show S02E03.mkv", 203},
		{"04b range folder, no season of its own", "Show", decide.PackSeasons, false, proofFiles(no, func(s, n int) string { return fmt.Sprintf("Show Season 1 ~ 2/Show - %02d.mkv", 12*(s-1)+n) }), "", 0},
		{"05 range folder + season folders", "Show", decide.PackSeasons, true, proofFiles(no, sf("Show Season 1 ~ 2/Season %d/Show - %02d.mkv")), "Show Season 1 ~ 2/Season 2/Show - 03.mkv", 203},
		{"06 'S1-S2' folder outside", "Show", decide.PackSeasons, true, proofFiles(no, sf("[Grp] Show S1-S2 [BD 1080p]/Show S%02dE%02d.mkv")), "[Grp] Show S1-S2 [BD 1080p]/Show S02E03.mkv", 203},
		{"07 numeric prefix vs explicit token", "Show", decide.PackSeasons, false, append(proofFiles(map[int]bool{101: true, 102: true, 201: true, 202: true},
			func(s, n int) string { return fmt.Sprintf("Season %d/Show S%02dE%02d.mkv", s, s, n) }), "Season 1/01-02 - Show S02E01-E02.mkv", "Season 2/Show S02E01-E02.mkv"), "", 0},
		{"08 numeric title, unnumbered file", "12 Kingdoms", decide.PackSeasons, false, append(proofFiles(map[int]bool{112: true}, sf("Season %d/12 Kingdoms - %02d.mkv")), "Season 1/12 Kingdoms - Digest.mkv"), "", 0},
		{"09b numeric title 'Brave 10'", "Brave 10", decide.PackSeasons, true, proofFiles(no, sf("Season %d/Brave 10 - %02d.mkv")), "Season 1/Brave 10 - 11.mkv", 111},
		{"09c 'Brave 10' without episode 10's file", "Brave 10", decide.PackSeasons, false, proofFiles(map[int]bool{110: true}, sf("Season %d/Brave 10 - %02d.mkv")), "", 0},
		{"09 numeric title, normal files", "12 Kingdoms", decide.PackSeasons, true, proofFiles(no, sf("Season %d/12 Kingdoms - %02d.mkv")), "Season 1/12 Kingdoms - 05.mkv", 105},
		{"10 extras beside episodes", "Show", decide.PackSeasons, true, append(proofFiles(no, sf("Show S%02dE%02d.mkv")), "Show - NCOP1.mkv", "Show - OVA 01.mkv", "Extras/Show - 01.mkv"), "Show S01E01.mkv", 101},
		{"11 dotted creditless opening", "Show", decide.PackSeasons, true, append(proofFiles(no, sf("Show.S%02dE%02d.1080p.mkv")), "Show.NC.OP.1080p.mkv"), "Show.S02E03.1080p.mkv", 203},
		// Rule S: an episode titled with a marker word is set aside, its episode has no file: rejected.
		{"12 episode title 'Special Training'", "Show", decide.PackSeasons, false, append(proofFiles(map[int]bool{105: true}, sf("Show S%02dE%02d.mkv")), "Show S01E05 - Special Training.mkv"), "", 0},
		{"12b extra '(NCOP)' in place of episode 5", "Show", decide.PackSeasons, false, append(proofFiles(map[int]bool{105: true}, sf("[Grp] Show S%dE%02d [1080p].mkv")), "[Grp] Show - 05 (NCOP) [1080p].mkv"), "", 0},
		{"12c 'OVA 01 - S01E01' in place of episode 1", "Show", decide.PackSeasons, false, append(proofFiles(map[int]bool{101: true}, sf("Show S%02dE%02d.mkv")), "Show - OVA 01 - S01E01.mkv"), "", 0},
		{"12d 'NC-OP 01' in place of episode 1", "Show", decide.PackSeasons, false, append(proofFiles(map[int]bool{101: true}, sf("Season %d/Show - %02d.mkv")), "Season 1/Show - NC-OP 01.mkv"), "", 0},
		{"12e those extras beside a full pack", "Show", decide.PackSeasons, true, append(proofFiles(no, sf("Season %d/Show - %02d.mkv")), "[Grp] Show - 05 (NCOP) [1080p].mkv",
			"Show - OVA 01 - S01E01.mkv", "Season 1/Show - NC-OP 01.mkv"), "Season 1/Show - 05.mkv", 105},
		{"13 two files on one episode", "Show", decide.PackSeasons, false, append(proofFiles(no, sf("Show S%02dE%02d.mkv")), "Show S01E01 v2.mkv"), "", 0},
		{"14 one episode missing", "Show", decide.PackSeasons, false, proofFiles(map[int]bool{207: true}, sf("Show S%02dE%02d.mkv")), "", 0},
		{"15 season name, absolute files", "Show", decide.PackSeasons, false, absFiles(nil, "Show - %02d.mkv"), "", 0},
		{"16 file of a third season", "Show", decide.PackSeasons, false, append(proofFiles(no, sf("Show S%02dE%02d.mkv")), "Show S03E01.mkv"), "", 0},
		{"17 S01 file in Season 2 folder", "Show", decide.PackSeasons, false, append(proofFiles(map[int]bool{201: true},
			func(s, n int) string { return fmt.Sprintf("Season %d/Show S%02dE%02d.mkv", s, s, n) }), "Season 2/Show S01E01.mkv"), "", 0},
		{"18 'Show S2 - 05' names", "Show", decide.PackSeasons, true, proofFiles(no, sf("[Grp] Show S%d - %02d [1080p].mkv")), "[Grp] Show S2 - 03 [1080p].mkv", 203},
		{"20 absolute plain", "Show", decide.PackAbsolute, true, absFiles(nil, "[Grp] Show - %02d [1080p].mkv"), "[Grp] Show - 15 [1080p].mkv", 203},
		{"21 absolute bare stems", "Show", decide.PackAbsolute, false, absFiles(nil, "%03d.mkv"), "", 0},
		{"22 absolute, numeric title + NCOP", "Mob Psycho 100", decide.PackAbsolute, true, append(absFiles(nil, "Mob Psycho 100 - %02d.mkv"), "Mob Psycho 100 NCOP1.mkv"), "Mob Psycho 100 - 15.mkv", 203},
		{"23 absolute name, season files", "Show", decide.PackAbsolute, false, proofFiles(no, sf("Show S%02dE%02d.mkv")), "", 0},
		{"24 absolute name, numbers restart in folders", "Show", decide.PackAbsolute, false, proofFiles(no, sf("Season %d/Show - %02d.mkv")), "", 0},
		{"24b absolute batch in season folders", "Show", decide.PackAbsolute, true, proofFiles(no, func(s, n int) string { return fmt.Sprintf("Season %d/Show - %02d.mkv", s, 12*(s-1)+n) }), "Season 2/Show - 15.mkv", 203},
		{"25 absolute, one number beyond the batch", "Show", decide.PackAbsolute, false, append(absFiles(nil, "Show - %02d.mkv"), "Show - 25.mkv"), "", 0},
		{"26 absolute with v2 and episode titles", "Show", decide.PackAbsolute, false, append(absFiles(map[int]bool{7: true, 9: true}, "Show - %02d.mkv"), "Show - 07v2.mkv", "Show - 09 - Opening Night.mkv"), "", 0},
		{"26b absolute with v2", "Show", decide.PackAbsolute, true, append(absFiles(map[int]bool{7: true}, "Show - %02d.mkv"), "Show - 07v2.mkv"), "Show - 07v2.mkv", 107},
		{"30 wrapper 'Show (Seasons 1-2) [BD]'", "Show", decide.PackSeasons, true, proofFiles(no, sf("Show (Seasons 1-2) [BD]/Show S%02dE%02d.mkv")), "Show (Seasons 1-2) [BD]/Show S02E03.mkv", 203},
		{"31 wrapper 'Show S01-S02' + season folders", "Show", decide.PackSeasons, true, proofFiles(no, sf("Show S01-S02/Season %d/Show - %02d.mkv")), "Show S01-S02/Season 2/Show - 03.mkv", 203},
		{"32 wrapper 'Show S01+S02'", "Show", decide.PackSeasons, true, proofFiles(no, sf("Show S01+S02/Show S%02dE%02d.mkv")), "Show S01+S02/Show S02E03.mkv", 203},
		{"33 folders 'Show S1' / 'Show S2'", "Show", decide.PackSeasons, true, proofFiles(no, sf("Show S%d/Show - %02d.mkv")), "Show S2/Show - 03.mkv", 203},
		{"34 folders 'S01' / 'S02'", "Show", decide.PackSeasons, true, proofFiles(no, sf("S%02d/Show - %02d.mkv")), "S02/Show - 03.mkv", 203},
		{"35 folders 'Season 01' with SxxEyy files", "Show", decide.PackSeasons, true, proofFiles(no, func(s, n int) string { return fmt.Sprintf("Season %02d/Show S%02dE%02d.mkv", s, s, n) }), "Season 02/Show S02E03.mkv", 203},
		{"36 absolute batch inside one wrapper folder", "Show", decide.PackAbsolute, true, absFiles(nil, "Show (01-24) [1080p]/Show - %02d.mkv"), "Show (01-24) [1080p]/Show - 15.mkv", 203},
		{"37b '2nd Season [BD 1080p]' folder", "Show", decide.PackSeasons, true, append(proofFiles(season2, sf("Season %d/Show - %02d.mkv")),
			proofFiles(map[int]bool{101: true, 102: true, 103: true, 104: true, 105: true, 106: true, 107: true, 108: true, 109: true, 110: true, 111: true, 112: true},
				func(_, n int) string { return fmt.Sprintf("Show 2nd Season [BD 1080p]/Show - %02d.mkv", n) })...), "Show 2nd Season [BD 1080p]/Show - 02.mkv", 202},
		{"37c 'Show 1st & 2nd Season' wrapper names no season", "Show", decide.PackSeasons, false, absFiles(nil, "Show 1st & 2nd Season/Show - %02d.mkv"), "", 0},
		{"37 '2nd Season' folder", "Show", decide.PackSeasons, true, append(proofFiles(season2, sf("Season %d/Show - %02d.mkv")),
			absFiles(first12, "Show 2nd Season/Show 2nd Season - %02d.mkv")...), "Show 2nd Season/Show 2nd Season - 03.mkv", 203},
	} {
		title, claim := proofTitle(c.series, c.mode)
		var files []PlannedFile
		for _, f := range c.files {
			files = append(files, PlannedFile{Rel: f})
		}
		proof, why := provePack(title, claim, files)
		if (why == "") != c.ok {
			t.Errorf("%s: proof holds %v (%s), want %v", c.name, why == "", why, c.ok)
			continue
		}
		if c.ok && c.watch != "" && !slices.Equal(proof.files[c.watch], []int64{c.at}) {
			t.Errorf("%s: %s placed on %v, want [%d]", c.name, c.watch, proof.files[c.watch], c.at)
		}
	}
}

// File names the proof sets aside as extras, and names that are regular episodes, under rule S (the coordinator's
// table): a marker anywhere after the library title sets a file aside, so an episode titled with a marker word is set
// aside too (its episode then has no file and the pack is rejected).
func TestAnimePackExtraNames(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		series, rel string
		extra       bool
	}{
		{"Show", "Show - NCOP1.mkv", true},
		{"Show", "Show - NCED 01.mkv", true},
		{"Show", "Show.NC.OP.1080p.mkv", true},
		{"Show", "Show - NC-OP 01.mkv", true},
		{"Show", "Show - NC-OP.mkv", true},
		{"Mob Psycho 100", "Mob Psycho 100 NCOP1.mkv", true},
		{"Show", "Show NCED2.mkv", true},
		{"Show", "Show 01 NCOP1a.mkv", true},
		{"Show", "Show - 02 (NCED2b).mkv", true},
		{"Show", "[Group] Show - NCOP [1080p].mkv", true},
		{"Show", "[Grp] Show - 05 (NCOP) [1080p].mkv", true},
		{"Show", "[Grp] Show - 05 [NCOP].mkv", true},
		{"Show", "[Grp] Show (OVA) - 05 [1080p].mkv", true},
		{"Show", "Kaiju Tale - OVA 01 - S01E01 [BD 1080p].mkv", true},
		{"Show", "Show - 13 (OVA).mkv", true},
		{"Show", "Show - OVA 01.mkv", true},
		{"Show", "Show - OAV 01.mkv", true},
		{"Show", "Show - OVA.mkv", true},
		{"Show", "Show - Special 1.mkv", true},
		{"Show", "Show - SP1.mkv", true},
		{"Show", "Show - Recap.mkv", true},
		{"Show", "Extras/Show - 01.mkv", true},
		{"Show", "Specials/Show - 01.mkv", true},
		{"Show", "Season 0/Show - 01.mkv", true},
		{"Show", "Show - Creditless OP.mkv", true},
		{"Show", "Show - 01 - Creditless OP.mkv", true},
		{"Show", "Show - Textless OP 01.mkv", true},
		{"Show", "Show - Opening 01.mkv", true},
		{"Show", "Show - Ending 02.mkv", true},
		{"Show", "Show - Omake 01.mkv", true},
		{"Show", "Show - Extra 01.mkv", true},
		{"Show", "Show - Bonus 01.mkv", true},
		{"Show", "Show - 05 (Commentary).mkv", true},
		{"Show", "Show - 05 Preview.mkv", true},
		{"Show", "Show - PV1.mkv", true},
		{"Show", "Show - Menu 1.mkv", true},
		{"Show", "Show S00E01.mkv", true},
		{"Show", "[Group] Show - Creditless Ending 2 [BD 1080p].mkv", true},
		{"Show", "Show - OP1.mkv", true},
		{"Show", "Show - ED2 [1080p].mkv", true},
		{"Show", "Show S01E01 - Special Training.mkv", true},
		{"Show", "Show - 05 - Special Training.mkv", true},
		{"Show", "Show [S02E05] - Special.mkv", true},
		{"Show", "Kaiju Tale 05 Special [BD 1080p].mkv", true},
		{"Show", "Show.05.Recap.1080p.mkv", true},
		{"Show", "Show S02E03 - Recap of Love.mkv", true},
		{"Show", "Show - 07 - Opening Night.mkv", true},
		{"Show", "Show - 12 - The Menu.mkv", true},
		{"Show", "Show S01E12 - The Special.mkv", true},
		{"Show", "Show - 01v2 [NCED fixed].mkv", true},
		{"Special 7", "Special 7 - 05.mkv", false},
		{"Special 7", "Special.7.S01E05.1080p.mkv", false},
		{"OVA Girls", "OVA Girls - 03.mkv", false},
		{"Show", "Show - 05 [5.1 AAC].mkv", false},
		{"Show", "Show - 05 [BD 1080p FLAC 5.1].mkv", false},
		{"Show", "Show - 05 (AAC 2.0).mkv", false},
		{"Show", "[NC-Raws] Show - 05 (B-Global 1920x1080 HEVC AAC MKV).mkv", false},
		{"Show", "[Group] Show - 01 [BD 1080p][OPUS].mkv", false},
		{"Show", "Show - 05 - Ed and Al.mkv", false},
		{"Show", "Show.S01E05.The.Speed.Run.1080p.CR.WEB-DL.mkv", false},
		{"Mob Psycho 100", "Mob Psycho 100 - 05.mkv", false},
		{"Show", "Season 2/Show - 05.mkv", false},
		{"Show", "Show - 05v2 (Uncensored).mkv", false},
		{"Show", "Show - Movie.mkv", true}, {"Show", "Movies/Show Movie 1.mkv", true}, {"Show", "OVAs/Show - 01.mkv", true},
		{"Show", "Show - Movie 01.mkv", true}, {"Show", "Show - Op 01.mkv", true}, {"Show", "Show - ed2.mkv", true},
		{"Show", "[Grp] Show - 05 [SPA][ENG].mkv", false}, {"Show", "Show - 05 - Spy Games.mkv", false}, {"Show", "Show - 05 - PvP.mkv", false},
	} {
		m := indexer.NewTitleMatcher(c.series)
		stem := strings.TrimSuffix(filepath.Base(c.rel), filepath.Ext(c.rel))
		p, _ := parseFile(stem, m)
		if got := packExtra(c.rel, p, m, newPackDirs()); got != c.extra {
			t.Errorf("%s (%s): extra %v, want %v", c.rel, c.series, got, c.extra)
		}
	}
}

// Layouts whose title or claim differs from the plain two-season "Show" (the closing round's rows): a scene name that
// carries a season is never dropped, a folder marker sets its files aside, numbers split by a title separator are no
// multi-episode file, and the guards a removed check would let through.
func TestAnimePackLayoutsByTitle(t *testing.T) {
	t.Parallel()
	no := map[int]bool{}
	sf := func(pat string) func(int, int) string {
		return func(s, n int) string { return fmt.Sprintf(pat, s, n) }
	}
	ip := func(n int) *int { return &n }
	scene := func(name string, season int) func(*decide.Title, *decide.PackClaim) {
		return func(t *decide.Title, _ *decide.PackClaim) {
			t.SceneMappings = append(t.SceneMappings, decide.SceneMapping{Title: name, SeasonNumber: ip(season)})
		}
	}
	for _, c := range []struct {
		name, series, mode string
		mod                func(*decide.Title, *decide.PackClaim)
		ok                 bool
		files              []string
		watch              string
		at                 int64
	}{
		{"scene title with season under an absolute claim", "Show", decide.PackAbsolute, scene("Show 2nd Season", 2), false,
			append(absFiles(map[int]bool{1: true}, "Show - %02d.mkv"), "Show 2nd Season - 01.mkv"), "", 0},
		{"ordinal titles against their folders", "Show", decide.PackSeasons, scene("Show 2nd Season", 2), false,
			proofFiles(no, func(s, n int) string {
				return fmt.Sprintf("Season %d/Show %s Season - %02d.mkv", s, map[int]string{1: "2nd", 2: "1st"}[s], n)
			}), "", 0},
		{"a folder marked OVA", "Show", decide.PackSeasons, nil, false,
			append(proofFiles(map[int]bool{101: true}, sf("Show S%02dE%02d.mkv")), "Season 1 [OVA]/Show - 01.mkv"), "", 0},
		{"numbers split by a title separator", "Brave Ten", decide.PackAbsolute, func(_ *decide.Title, c *decide.PackClaim) {
			c.Episodes, c.Numbers = []int64{110, 111, 112, 201}, []decide.PackNumber{{N: 10, Episode: 110}, {N: 11, Episode: 111},
				{N: 12, Episode: 112}, {N: 13, Episode: 201}}
		}, false, []string{"Brave 10 - 13.mkv"}, "", 0},
		{"numbers split by a title separator, a tag with a dash", "Brave Ten", decide.PackAbsolute, func(_ *decide.Title, c *decide.PackClaim) {
			c.Episodes, c.Numbers = []int64{110, 111, 112, 201}, []decide.PackNumber{{N: 10, Episode: 110}, {N: 11, Episode: 111},
				{N: 12, Episode: 112}, {N: 13, Episode: 201}}
		}, false, []string{"Brave 10 - 13 (2012-2013).mkv"}, "", 0},
		{"numbers split by a title separator, an unbracketed codec tag", "Brave Ten", decide.PackAbsolute, func(_ *decide.Title, c *decide.PackClaim) {
			c.Episodes, c.Numbers = []int64{110, 111, 112, 201}, []decide.PackNumber{{N: 10, Episode: 110}, {N: 11, Episode: 111},
				{N: 12, Episode: 112}, {N: 13, Episode: 201}}
		}, false, []string{"Brave.10.-.13.1080p.BluRay.x264-10bit-GRP.mkv"}, "", 0},
		{"numbers split by a title separator, an unbracketed audio tag", "Brave Ten", decide.PackAbsolute, func(_ *decide.Title, c *decide.PackClaim) {
			c.Episodes, c.Numbers = []int64{110, 111, 112, 201}, []decide.PackNumber{{N: 10, Episode: 110}, {N: 11, Episode: 111},
				{N: 12, Episode: 112}, {N: 13, Episode: 201}}
		}, false, []string{"Brave 10 - 13 1080p DDP2.0-10bit.mkv"}, "", 0},
		{"numbers split by a title separator, an unbracketed H265 tag", "Brave Ten", decide.PackAbsolute, func(_ *decide.Title, c *decide.PackClaim) {
			c.Episodes, c.Numbers = []int64{110, 111, 112, 201}, []decide.PackNumber{{N: 10, Episode: 110}, {N: 11, Episode: 111},
				{N: 12, Episode: 112}, {N: 13, Episode: 201}}
		}, false, []string{"Brave 10 - 13 H265-10bit.mkv"}, "", 0},
		{"flat pack with a scene title of season 2", "Isekai Story", decide.PackSeasons, scene("Isekai Story S2", 2), true,
			proofFiles(no, sf("[Grp] Isekai Story S%d - %02d.mkv")), "[Grp] Isekai Story S2 - 05.mkv", 205},
		{"title punctuation", "Show: 10", decide.PackAbsolute, nil, true, absFiles(nil, "Show 10 - %02d.mkv"), "Show 10 - 15.mkv", 203},
		{"a season of its own under an absolute claim", "Show", decide.PackAbsolute, nil, false,
			append(absFiles(map[int]bool{17: true}, "Show - %02d.mkv"), "[Grp] Show S2 - 05.mkv"), "", 0},
		{"a third season outside the claim", "Show", decide.PackSeasons, func(t *decide.Title, _ *decide.PackClaim) {
			t.Episodes = append(t.Episodes, decide.Episode{ID: 301, Season: 3, Number: 1, Absolute: 25})
		}, false, append(proofFiles(no, sf("Show S%02dE%02d.mkv")), "Show S03E01.mkv"), "", 0},
		{"season 1's file alone in a season 2 folder", "Show", decide.PackSeasons, nil, false,
			append(proofFiles(map[int]bool{101: true}, func(s, n int) string { return fmt.Sprintf("Season %d/Show S%02dE%02d.mkv", s, s, n) }),
				"Season 2/Show S01E01.mkv"), "", 0},
	} {
		title, claim := proofTitle(c.series, c.mode)
		if c.mod != nil {
			c.mod(title, claim)
		}
		var files []PlannedFile
		for _, f := range c.files {
			files = append(files, PlannedFile{Rel: f})
		}
		proof, why := provePack(title, claim, files)
		if (why == "") != c.ok {
			t.Errorf("%s: proof holds %v (%s), want %v", c.name, why == "", why, c.ok)
			continue
		}
		if c.ok && !slices.Equal(proof.files[c.watch], []int64{c.at}) {
			t.Errorf("%s: %s placed on %v, want [%d]", c.name, c.watch, proof.files[c.watch], c.at)
		}
		if !c.ok {
			t.Logf("%s: %s", c.name, why)
		}
	}
}

// The end check's rows: a folder name is read whole for markers (a season-range wrapper excepted), the parser's own
// season counts under an absolute claim, and movie, Op/Ed with a number set a file aside. Each extra stands in for
// S01E01's (number 01's) missing file, so a missed marker would link it: every such row rejects.
func TestAnimePackLayoutsEnd(t *testing.T) {
	t.Parallel()
	no := map[int]bool{}
	first := map[int]bool{101: true}
	sf := func(pat string) func(int, int) string {
		return func(s, n int) string { return fmt.Sprintf(pat, s, n) }
	}
	abs := func(extra string) []string { return append(absFiles(map[int]bool{1: true}, "Show - %02d.mkv"), extra) }
	seasons := func(extra string) []string {
		return append(proofFiles(first, sf("Season %d/Show - %02d.mkv")), extra)
	}
	wrapper := proofFiles(no, sf("Show (Season 1+2+OVA)/Season %d/Show - %02d.mkv"))
	for _, c := range []struct {
		name, mode string
		ok         bool
		files      []string
		aside      string
	}{
		{"(Season 2) under an absolute claim", decide.PackAbsolute, false, abs("[Grp] Show (Season 2) - 01.mkv"), ""},
		{"(S2) under an absolute claim", decide.PackAbsolute, false, abs("[Grp] Show (S2) - 01.mkv"), ""},
		{"Disc.OVA folder", decide.PackSeasons, false, append(proofFiles(first, sf("Show S%02dE%02d.mkv")), "Disc.OVA/Show S01E01.mkv"), ""},
		{"[OVA] folder", decide.PackSeasons, false, append(proofFiles(first, sf("Show S%02dE%02d.mkv")), "[OVA]/Show S01E01.mkv"), ""},
		{"Show.S01.OVA folder", decide.PackSeasons, false, seasons("Show.S01.OVA/Show - 01.mkv"), ""},
		{"Show.S01.Extras folder", decide.PackSeasons, false, seasons("Show.S01.Extras/Show - 01.mkv"), ""},
		{"Movie 01", decide.PackSeasons, false, seasons("Season 1/Show - Movie 01.mkv"), ""},
		{"Op 01", decide.PackSeasons, false, seasons("Season 1/Show - Op 01.mkv"), ""},
		{"Movies/Movie 01", decide.PackAbsolute, false, abs("Movies/Show - Movie 01.mkv"), ""},
		{"op 01", decide.PackAbsolute, false, abs("Show - op 01.mkv"), ""},
		{"OVAs folder", decide.PackSeasons, false, seasons("OVAs/Show - 01.mkv"), ""},
		{"season-range wrapper", decide.PackSeasons, true, wrapper, ""},
		{"season-range wrapper with an OVA folder", decide.PackSeasons, true,
			append(slices.Clone(wrapper), "Show (Season 1+2+OVA)/OVA/Show - 01.mkv"), "Show (Season 1+2+OVA)/OVA/Show - 01.mkv"},
	} {
		title, claim := proofTitle("Show", c.mode)
		var files []PlannedFile
		for _, f := range c.files {
			files = append(files, PlannedFile{Rel: f})
		}
		proof, why := provePack(title, claim, files)
		if (why == "") != c.ok {
			t.Errorf("%s: proof holds %v (%s), want %v", c.name, why == "", why, c.ok)
			continue
		}
		if c.ok && !slices.Equal(proof.files["Show (Season 1+2+OVA)/Season 2/Show - 03.mkv"], []int64{203}) {
			t.Errorf("%s: season 2's file placed on %v", c.name, proof.files["Show (Season 1+2+OVA)/Season 2/Show - 03.mkv"])
		}
		if c.aside != "" && !proof.extras[c.aside] {
			t.Errorf("%s: %s not set aside", c.name, c.aside)
		}
		if !c.ok {
			t.Logf("%s: %s", c.name, why)
		}
	}
}

// The joined forms the parser reads as several episodes prove a multi-episode file of a pack (round 1): an episode
// range after SxxEyy, a chain of numbers in a season folder and an NxNN range, each placed on its three episodes.
func TestAnimePackJoinedForms(t *testing.T) {
	t.Parallel()
	first3 := map[int]bool{101: true, 102: true, 103: true}
	for joined, pat := range map[string]string{
		"Show S01E01-03.mkv":               "Show S%02dE%02d.mkv",
		"Season 1/Show - 01-02-03.mkv":     "Season %d/Show - %02d.mkv",
		"Show 1x01-1x03.mkv":               "Show %dx%02d.mkv",
		"Show.S01E01-E03-1080p_x265.mkv":   "Show.S%02dE%02d-1080p_x265.mkv",
		"Show_S01E01-E03-1080p_WEB-DL.mkv": "Show_S%02dE%02d-1080p_WEB-DL.mkv",
		"Show.S01E01-E03-1080p.mkv":        "Show.S%02dE%02d-1080p.mkv",
		"Season 1/Show - 01-03-720p.mkv":   "Season %d/Show - %02d.mkv",
	} {
		title, claim := proofTitle("Show", decide.PackSeasons)
		var files []PlannedFile
		for _, f := range append(proofFiles(first3, func(s, n int) string { return fmt.Sprintf(pat, s, n) }), joined) {
			files = append(files, PlannedFile{Rel: f})
		}
		proof, why := provePack(title, claim, files)
		if why != "" {
			t.Errorf("%s: %s", joined, why)
			continue
		}
		if got := proof.files[joined]; !slices.Equal(got, []int64{101, 102, 103}) {
			t.Errorf("%s: placed on %v", joined, got)
		}
	}
}
