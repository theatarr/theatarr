package library

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// The franchise mapping of a series changes between a grab and its import (docs/design/anime.md, "Import"). The
// acceptable failure is a visible hold with every file intact: never a link moved aside at an episode the grab did
// not cover, a pack widened past the episodes the router kept, or a failed (blocklisted) download.

// decided is the decision on an isekai release (title 3, version 30) now.
func (e *env) decided(rel indexer.Release) *decide.Decision {
	e.t.Helper()
	t, err := decide.LoadTitle(e.ctx, e.st.Reader(), 3)
	if err == nil {
		err = decide.LoadSeriesVersion(e.ctx, e.st.Reader(), &t, 30)
	}
	if err != nil {
		e.t.Fatal(err)
	}
	d, err := e.lib.engine.Decide(e.ctx, decide.Release{Release: rel}, t,
		decide.Version{ID: 30, Key: "tv", Policy: e.pol.Versions["tv"]}, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return d
}

// linkedOld imports download 1: a 1080p file of episode id (SxxEyy by name), and returns its link's path and target.
func (e *env) linkedOld(name string, id int64) (path, target string) {
	e.t.Helper()
	e.release(name, map[string]float64{name + ".mkv": 1440})
	e.videoSize(name, 1920, 1080)
	e.seriesDownload(1, 3, 30, hashA, name, fmt.Sprintf("[%d]", id), nil)
	first, err := e.lib.Import(e.ctx, 1, false)
	if err != nil || len(first.Links) != 1 {
		e.t.Fatalf("import of the old file: %+v %v", first, err)
	}
	path = first.Links[0].Path
	if target, err = os.Readlink(path); err != nil {
		e.t.Fatal(err)
	}
	return path, target
}

// held checks download dl was held with nothing changed: no link planned, the download ready with its episodes, the
// old link in place, nothing in the trash and nothing blocklisted.
func (e *env) held(plan *Plan, dl int64, episodes, oldPath, oldTarget string) {
	e.t.Helper()
	if len(plan.Links) != 0 || len(plan.Unresolved) == 0 {
		e.t.Errorf("links\n%s\nunresolved %v", e.links(plan), plan.Unresolved)
	}
	for _, why := range plan.Unresolved {
		if !strings.HasPrefix(why, "out of scope: ") {
			e.t.Errorf("held as %q", why)
		}
	}
	if got := e.one(`SELECT state || ' ' || episodes FROM download WHERE id = ?`, dl); got != "ready "+episodes {
		e.t.Errorf("download %s, want ready %s", got, episodes)
	}
	if oldPath != "" {
		if got, err := os.Readlink(oldPath); err != nil || got != oldTarget {
			e.t.Errorf("the link the grab did not cover was displaced: %s -> %s (%v), was %s", oldPath, got, err, oldTarget)
		}
		if got := e.one(`SELECT state FROM link WHERE download_id = 1`); got != "active" {
			e.t.Errorf("old link %s", got)
		}
	}
	if got := e.one(`SELECT (SELECT COUNT(*) FROM link_trash) || ' ' || (SELECT COUNT(*) FROM router_blocklist)`); got != "0 0" {
		e.t.Errorf("trash and blocklist rows: %s", got)
	}
}

// A grab for S02E01 through Sonarr's numbering (no franchise mapping yet) waits while the series' first build maps
// its file to S02E02, which holds a 1080p link: the incoming remux is held, and the link stays.
func TestAnimeFirstBuildKeepsOtherEpisodesLink(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.isekai()
	e.exec(`DELETE FROM anime_mapping`)
	e.exec(`UPDATE episode SET air_date_utc_at = 1600000000000 WHERE title_id = 3`)
	oldPath, oldTarget := e.linkedOld("Isekai.Story.S02E02.1080p.WEB-DL-OLD", 314)

	rel := "[Group] Isekai Story - 13 (2160p BluRay REMUX)"
	e.release(rel, map[string]float64{rel + ".mkv": 1440})
	dec := e.decided(indexer.Release{Title: rel, Size: 2000000000})
	if !dec.Accepted || len(dec.EpisodeIDs) != 1 || dec.EpisodeIDs[0] != 313 || dec.EpisodeMapping() != nil {
		t.Fatalf("not an accepted S02E01 grab: %+v", dec)
	}
	e.seriesDownload(2, 3, 30, strings.Repeat("b", 40), rel, "[313]", nil)
	e.exec(`UPDATE download SET quality = 'Bluray-2160p Remux' WHERE id = 2`)
	e.firstBuild(2)
	plan, err := e.lib.Import(e.ctx, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	e.held(plan, 2, "[313]", oldPath, oldTarget)
}

// The same on the two paths that do re-scope. A grab through an entry review then rejects (al:2, S02E02) maps
// through Sonarr's numbering to S01E02; a grab through Sonarr's numbering for an entry in conflict (S01E02) maps to
// the entry's S02E02 once review accepts it. The new episode holds a 1080p link the grab did not cover: the download
// is not re-scoped, its remux is held, and the link stays.
func TestAnimeRescopeKeepsOtherEpisodesLink(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, old                   string
		oldEp                       int64
		grabStatus, status, grabbed string
		epMap                       string
	}{
		{"rejected", "Isekai.Story.S01E02.1080p.WEB-DL-OLD", 302, "auto", "rejected", "[314]", `{"how":"franchise","entry":"al:2"}`},
		{"accepted", "Isekai.Story.S02E02.1080p.WEB-DL-OLD", 314, "conflict", "override", "[302]", `{"how":"franchise","entry":"al:2","fallback":true}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, true, policy.ModeActive)
			e.isekai()
			e.exec(`UPDATE episode SET air_date_utc_at = 1600000000000 WHERE title_id = 3`)
			e.exec(`UPDATE anime_mapping SET status = ? WHERE entry_key = 'al:2'`, c.grabStatus)
			oldPath, oldTarget := e.linkedOld(c.old, c.oldEp)

			rel := "[Group] Isekai Story 2nd Season - 02 (2160p BluRay REMUX)"
			e.release(rel, map[string]float64{rel + ".mkv": 1440})
			dec := e.decided(indexer.Release{Title: rel, Size: 2000000000})
			ids, _ := json.Marshal(dec.EpisodeIDs)
			m, _ := json.Marshal(dec.EpisodeMapping())
			if !dec.Accepted || string(ids) != c.grabbed || string(m) != c.epMap {
				t.Fatalf("grab: accepted %v, episodes %s, mapping %s (%v)", dec.Accepted, ids, m, dec.Rejections)
			}
			e.seriesDownload(2, 3, 30, strings.Repeat("b", 40), rel, c.grabbed, c.epMap)
			e.exec(`UPDATE download SET quality = 'Bluray-2160p Remux' WHERE id = 2`)
			e.exec(`UPDATE anime_mapping SET status = ? WHERE entry_key = 'al:2'`, c.status)
			plan, err := e.lib.Import(e.ctx, 2, false)
			if err != nil {
				t.Fatal(err)
			}
			e.held(plan, 2, c.grabbed, oldPath, oldTarget)
		})
	}
}

// A season 1 pack matched by its TVDB id (its title names no alias) and grabbed through Sonarr's numbering waits
// while the first build maps its "- 01" file to S02E01: the pack keeps season 1, so neither that file nor the
// special beside it is linked outside it.
func TestAnimeFirstBuildKeepsIdMatchedPack(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.isekai()
	e.exec(`DELETE FROM anime_mapping`)
	e.exec(`UPDATE episode SET air_date_utc_at = 1600000000000 WHERE title_id = 3`)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at) VALUES (315, 3, 0, 1, 'Bonus', 0, 0)`)
	rel := "Another Name S01 1080p WEB-DL"
	release := indexer.Release{Title: rel, Size: 2000000000, TVDB: 2002}
	dec := e.decided(release)
	if !dec.Accepted || len(dec.EpisodeIDs) != 12 || dec.EpisodeMapping() != nil {
		t.Fatalf("not an accepted season 1 grab through Sonarr's numbering: %+v", dec)
	}
	eps, _ := json.Marshal(dec.EpisodeIDs)
	e.release(rel, map[string]float64{"[Group] Isekai Story - 01 (1080p WEB-DL).mkv": 1440,
		"Isekai.Story.S00E01.1080p.WEB-DL.mkv": 1440})
	e.videoSize(rel, 1920, 1080)
	e.seriesDownload(1, 3, 30, hashA, rel, string(eps), nil)
	body, _ := json.Marshal(release)
	e.exec(`UPDATE download SET release_data = ? WHERE id = 1`, string(body))
	e.exec(`INSERT INTO anime_mapping (franchise, entry_key, status, tvdb_series, episodes, reason, updated_at)
		VALUES ('isekai', 'al:1', 'auto', 2002, '[[2,1]]', '', 0)`)
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	e.held(plan, 1, string(eps), "", "")
}

// A two-episode grab the router narrowed to S02E01 (S02E02 turned unmonitored while it waited) keeps that scope when
// the first build agrees with Sonarr's numbering: S02E02 is not linked.
func TestAnimeFirstBuildKeepsNarrowedPack(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.isekai()
	e.exec(`DELETE FROM anime_mapping`)
	e.exec(`UPDATE episode SET air_date_utc_at = 1600000000000 WHERE title_id = 3`)
	rel := "[Group] Isekai Story - 13-14 (1080p WEB-DL)"
	dec := e.decided(indexer.Release{Title: rel, Size: 2000000000})
	if !dec.Accepted || len(dec.EpisodeIDs) != 2 || dec.EpisodeIDs[0] != 313 || dec.EpisodeIDs[1] != 314 || dec.EpisodeMapping() != nil {
		t.Fatalf("not an accepted two-episode grab: %+v", dec)
	}
	e.release(rel, map[string]float64{"[Group] Isekai Story - 13 (1080p WEB-DL).mkv": 1440,
		"[Group] Isekai Story - 14 (1080p WEB-DL).mkv": 1440})
	e.videoSize(rel, 1920, 1080)
	e.exec(`UPDATE episode SET monitored = 0 WHERE id = 314`)
	e.seriesDownload(1, 3, 30, hashA, rel, "[313]", nil)
	e.firstBuild(1)
	e.exec(`UPDATE anime_mapping SET episodes = json_insert(episodes, '$[#]', json('[2,2]'))`)
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.links(plan); len(plan.Links) != 1 || !strings.Contains(got, "S02E01") || strings.Contains(got, "S02E02") {
		t.Errorf("links\n%s\nunresolved %v", got, plan.Unresolved)
	}
	if got := e.one(`SELECT episodes FROM download WHERE id = 1`); got != "[313]" {
		t.Errorf("download episodes %s", got)
	}
}

// The same narrowing on the path that does re-scope: a pack named for an entry in conflict, grabbed through Sonarr's
// numbering (S01E01-E02) and narrowed to S01E01, follows the entry once review accepts it only for the file the
// router kept: S02E01 is linked, S02E02 is not.
func TestAnimeAcceptKeepsNarrowedPack(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.isekai()
	e.exec(`UPDATE anime_mapping SET status = 'conflict' WHERE entry_key = 'al:2'`)
	rel := "[Group] Isekai Story 2nd Season - 01-02 (BD 1080p)"
	e.release(rel, map[string]float64{"[Group] Isekai Story 2nd Season - 01 (BD 1080p).mkv": 1440,
		"[Group] Isekai Story 2nd Season - 02 (BD 1080p).mkv": 1440})
	e.videoSize(rel, 1920, 1080)
	e.seriesDownload(1, 3, 30, hashA, rel, "[301]", e.decidedMap(rel, `{"how":"franchise","entry":"al:2","fallback":true}`))
	e.exec(`UPDATE anime_mapping SET status = 'override' WHERE entry_key = 'al:2'`)
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.links(plan); len(plan.Links) != 1 || !strings.Contains(got, "S02E01") || strings.Contains(got, "S02E02") {
		t.Errorf("links\n%s\nunresolved %v", got, plan.Unresolved)
	}
	if got := e.one(`SELECT episodes FROM download WHERE id = 1`); got != "[313]" {
		t.Errorf("download episodes %s", got)
	}
}

// A sample cannot move the download's episodes. The first build maps the absolute-numbered sample of an explicit
// S02E01 grab to S02E02; an accepted entry maps the sample of a grab made through Sonarr's numbering to its own
// episode. Either way the main file imports where it was grabbed, and nothing is failed or blocklisted.
func TestAnimeSampleDoesNotRescope(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, rel, main, sample, grabbed string
		fallback                         bool
	}{
		{"first build", "[Group] Isekai Story - 13 (1080p WEB-DL)", "Isekai.Story.S02E01.1080p.WEB-DL.mkv",
			"[Group] Isekai Story - 13 (1080p WEB-DL).sample.mkv", "[313]", false},
		{"accepted", "[Group] Isekai Story 2nd Season - 02 (1080p WEB-DL)", "Isekai.Story.S01E02.1080p.WEB-DL.mkv",
			"[Group] Isekai Story 2nd Season - 02 (1080p WEB-DL).sample.mkv", "[302]", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, true, policy.ModeActive)
			e.isekai()
			e.exec(`UPDATE episode SET air_date_utc_at = 1600000000000 WHERE title_id = 3`)
			var epMap any
			if c.fallback {
				e.exec(`UPDATE anime_mapping SET status = 'conflict' WHERE entry_key = 'al:2'`)
				epMap = e.decidedMap(c.rel, `{"how":"franchise","entry":"al:2","fallback":true}`)
			} else {
				e.exec(`DELETE FROM anime_mapping`)
				e.decidedMap(c.rel, "null")
			}
			e.release(c.rel, map[string]float64{c.main: 1440, c.sample: 30})
			e.videoSize(c.rel, 1920, 1080)
			e.seriesDownload(1, 3, 30, hashA, c.rel, c.grabbed, epMap)
			if c.fallback {
				e.exec(`UPDATE anime_mapping SET status = 'override' WHERE entry_key = 'al:2'`)
			} else {
				e.firstBuild(2)
			}
			plan, err := e.lib.Import(e.ctx, 1, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Links) != 1 || plan.Links[0].File != c.main {
				t.Errorf("links\n%s\nrejected %v unresolved %v", e.links(plan), plan.Rejected, plan.Unresolved)
			}
			if got := e.one(`SELECT state || ' ' || episodes || ' ' || (SELECT COUNT(*) FROM router_blocklist) FROM download
				WHERE id = 1`); got != "imported "+c.grabbed+" 0" {
				t.Errorf("download and blocklist rows: %s", got)
			}
		})
	}
}

// A SeaDex pack matched by its TVDB id and AniList entry (al:2, S02E01-E02; its title names no alias) whose mapping
// review then rejects is decided again with the release as the download stored it: Sonarr's season 2, not the
// episodes of whatever files the pack holds. The special beside the episodes is not linked.
func TestAnimeRejectedMappingKeepsReleaseIdentity(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.isekai()
	e.exec(`UPDATE episode SET air_date_utc_at = 1600000000000 WHERE title_id = 3`)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at) VALUES (315, 3, 0, 1, 'Bonus', 0, 0)`)
	rel := "Another Name S02 1080p WEB-DL"
	release := indexer.Release{Title: rel, Size: 2000000000, TVDB: 2002, Kind: "seadex", Extras: map[string]string{"anilist": "2"}}
	dec := e.decided(release)
	m, _ := json.Marshal(dec.EpisodeMapping())
	if !dec.Accepted || len(dec.EpisodeIDs) != 2 || string(m) != `{"how":"franchise","entry":"al:2"}` {
		t.Fatalf("not an accepted grab through al:2: accepted %v, episodes %v, mapping %s (%v)", dec.Accepted,
			dec.EpisodeIDs, m, dec.Rejections)
	}
	e.release(rel, map[string]float64{"Isekai.Story.S02E01.1080p.WEB-DL.mkv": 1440,
		"Isekai.Story.S02E02.1080p.WEB-DL.mkv": 1440, "Isekai.Story.S00E01.1080p.WEB-DL.mkv": 1440})
	e.videoSize(rel, 1920, 1080)
	e.seriesDownload(1, 3, 30, hashA, rel, "[313,314]", string(m))
	body, _ := json.Marshal(release)
	e.exec(`UPDATE download SET release_data = ? WHERE id = 1`, string(body))
	e.exec(`UPDATE anime_mapping SET status = 'rejected' WHERE entry_key = 'al:2'`)
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.links(plan); len(plan.Links) != 2 || !strings.Contains(got, "S02E01") || !strings.Contains(got, "S02E02") ||
		strings.Contains(got, "S00E01") {
		t.Errorf("links\n%s\nunresolved %v", got, plan.Unresolved)
	}
	if got := e.one(`SELECT episodes FROM download WHERE id = 1`); got != "[313,314]" {
		t.Errorf("download episodes %s", got)
	}
}
