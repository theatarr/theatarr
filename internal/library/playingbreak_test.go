package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/outcome"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
)

// playingEnv: a series release of three episodes (one torrent, hashA), each linked, the router on (Pause off).
func playingEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t, true)
	e.pol.Router = &policy.Router{Enabled: true}
	e.release("Other", map[string]float64{"a.mkv": 7000})
	e.release("Show.S01", map[string]float64{"e1.mkv": 2700, "e2.mkv": 2700, "e3.mkv": 2700})
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at) VALUES (203, 2, 1, 3, 'Three', 0, 0)`)
	e.exec(`INSERT INTO content (id, info_hash, name, mount_folder, first_seen_at, last_seen_at) VALUES (1, ?, 'Show.S01', 'Show.S01', 0, 0)`, hashA)
	for i, ep := range []int64{201, 202, 203} {
		name := fmt.Sprintf("e%d.mkv", i+1)
		e.exec(`INSERT INTO content_file (id, content_id, path, size_bytes) VALUES (?, 1, ?, 1)`, i+1, name)
		p := filepath.Join(e.shows, "Show (2020)", "Season 01", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(e.mount, "__all__", "Show.S01", name)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
		e.exec(`INSERT INTO link (id, path, target, content_file_id, title_id, release_title, quality, state, created_at, updated_at)
			VALUES (?, ?, ?, ?, 2, 'Show.S01.1080p', 'WEBDL-1080p', 'active', 0, 0)`, i+1, p, target, i+1)
		e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (?, 20)`, i+1)
		e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (?, ?)`, i+1, ep)
		e.exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state, cutoff_met, updated_at)
			VALUES (20, ?, 1, 'filled', 0, 0)`, ep)
	}
	e.exec(`UPDATE version SET state = 'filled' WHERE id = 20`)
	return e
}

func (e *env) playingJobs() []router.ResearchRequest {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT payload FROM job WHERE kind = ? ORDER BY id`, router.JobResearch)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []router.ResearchRequest
	for rows.Next() {
		var s string
		var p router.ResearchRequest
		if err := rows.Scan(&s); err != nil {
			e.t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(s), &p); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// The watched file is marked dead at once (no 30-minute rule, no second sweep), its episode wanted, one playing search
// queued for it, the release's miss written on the failed account, and the incident kept with the old copy's facts.
func TestMarkBrokenPlaying(t *testing.T) {
	e := playingEnv(t)
	inc := PlayingIncident{TitleID: 2, Version: "tv", Viewer: "ann", Player: "TV"}
	if hit, err := e.lib.PlayingBoundReached(e.ctx, 2, "tv"); err != nil || hit {
		t.Fatalf("bound before any incident: %v %v", hit, err)
	}
	id, err := e.lib.OpenPlayingIncident(e.ctx, inc, PlayingFile{LinkID: 2, Offset: 4096})
	if err != nil {
		t.Fatal(err)
	}
	ok, err := e.lib.MarkBrokenPlaying(e.ctx, id, PlayingFile{LinkID: 2, Target: e.linkTarget(2), Offset: 4096}, true, []string{"realdebrid"})
	if err != nil || !ok {
		t.Fatalf("marked %v: %v", ok, err)
	}
	if got := e.one(`SELECT state || ' ' || COALESCE(missing_since, 'null') FROM link WHERE id = 2`); got != "dead null" {
		t.Errorf("watched link %s", got)
	}
	if got := e.one(`SELECT state FROM episode_version_state WHERE episode_id = 202`); got != "wanted" {
		t.Errorf("its episode %s", got)
	}
	if got := e.one(`SELECT group_concat(state) FROM link WHERE id IN (1, 3)`); got != "active,active" {
		t.Errorf("the other files %s", got)
	}
	jobs := e.playingJobs()
	if len(jobs) != 1 || jobs[0].Reason != router.ReasonPlaying || len(jobs[0].Episodes) != 1 ||
		jobs[0].Episodes[0] != 202 || jobs[0].VersionID != 20 {
		t.Errorf("searches %+v", jobs)
	}
	if got := e.one(`SELECT state || ' ' || source FROM provider_cache WHERE info_hash = ? AND provider = 'realdebrid'`,
		hashA); got != "miss router:playing" {
		t.Errorf("miss %s", got)
	}
	if got := e.one(`SELECT incident || ' ' || watched || ' ' || info_hash || ' ' || inner_file || ' ' || read_offset ||
		' ' || release_title || ' ' || episodes || ' ' || searched || ' ' || viewer FROM playing_break WHERE link_id = 2`); got !=
		fmt.Sprintf("%d 1 %s e2.mkv 4096 Show.S01.1080p [202] 1 ann", id, hashA) {
		t.Errorf("incident row %s", got)
	}
	if got := e.one(`SELECT detail FROM library_action WHERE kind = 'dead' AND link_id = 2`); got !=
		"stopped working while playing: a read through the mount failed" {
		t.Errorf("history %s", got)
	}
	// The bound: one incident per title and version in 24 hours.
	if hit, _ := e.lib.PlayingBoundReached(e.ctx, 2, "tv"); !hit {
		t.Error("bound not reached after an incident")
	}
	if hit, _ := e.lib.PlayingBoundReached(e.ctx, 1, "movie-remux"); hit {
		t.Error("bound reached for another title")
	}
	e.now = e.now.Add(PlayingBound + 1)
	if hit, _ := e.lib.PlayingBoundReached(e.ctx, 2, "tv"); hit {
		t.Error("bound still reached a day later")
	}
}

// A release's other files are marked as they are found, without a search; QueuePlayingSearch queues one playing
// search for their episodes together.
func TestMarkBrokenPlayingSiblingsSearchedTogether(t *testing.T) {
	e := playingEnv(t)
	id, err := e.lib.OpenPlayingIncident(e.ctx, PlayingIncident{TitleID: 2, Version: "tv"}, PlayingFile{LinkID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.lib.MarkBrokenPlaying(e.ctx, id, PlayingFile{LinkID: 2, Target: e.linkTarget(2)}, true, nil); err != nil {
		t.Fatal(err)
	}
	for _, link := range []int64{1, 3} {
		if ok, err := e.lib.MarkBrokenPlaying(e.ctx, id, PlayingFile{LinkID: link, Target: e.linkTarget(link), Offset: 7}, false, nil); err != nil || !ok {
			t.Fatalf("sibling %d: %v %v", link, ok, err)
		}
	}
	if jobs := e.playingJobs(); len(jobs) != 1 {
		t.Fatalf("searches before the siblings' %+v", jobs)
	}
	if got := e.one(`SELECT COUNT(*) FROM playing_break WHERE incident = ? AND watched = 0 AND searched = 0`, id); got != "2" {
		t.Errorf("sibling rows %s", got)
	}
	if n, err := e.lib.QueuePlayingSearch(e.ctx, id); err != nil || n != 1 {
		t.Fatalf("queued %d: %v", n, err)
	}
	jobs := e.playingJobs()
	if len(jobs) != 2 || jobs[1].Reason != router.ReasonPlaying || fmt.Sprint(jobs[1].Episodes) != "[201 203]" {
		t.Errorf("searches %+v", jobs)
	}
	if n, _ := e.lib.QueuePlayingSearch(e.ctx, id); n != 0 {
		t.Errorf("queued again: %d", n)
	}
}

// Pause on, or a version whose library writes nothing: nothing is marked.
func TestMarkBrokenPlayingStopsWhilePaused(t *testing.T) {
	e := playingEnv(t)
	id, err := e.lib.OpenPlayingIncident(e.ctx, PlayingIncident{TitleID: 2, Version: "tv"}, PlayingFile{LinkID: 2})
	if err != nil {
		t.Fatal(err)
	}
	e.pol.Router.Enabled = false
	if _, err := e.lib.MarkBrokenPlaying(e.ctx, id, PlayingFile{LinkID: 2, Target: e.linkTarget(2)}, true, nil); !errors.Is(err, ErrPlayingStopped) {
		t.Errorf("paused: %v", err)
	}
	if got := e.one(`SELECT state FROM link WHERE id = 2`); got != "active" {
		t.Errorf("link %s", got)
	}
	if jobs := e.playingJobs(); len(jobs) != 0 {
		t.Errorf("searches %+v", jobs)
	}
}

// The import that brings a file for a broken watched file's film fixes its incident (fixed_by replaced, the new
// release) and sends the outcome replaced once, with the old and new releases; settling the incident later sends
// nothing more.
func TestPlayingReplacedByImport(t *testing.T) {
	e, _, _ := deadEnv(t, true)
	e.pol.Router = &policy.Router{Enabled: true}
	var got []outcome.Outcome
	e.lib.OnPlayingOutcome(func(o outcome.Outcome) { got = append(got, o) })
	id, err := e.lib.OpenPlayingIncident(e.ctx, PlayingIncident{TitleID: 1, Version: "movie-remux", Viewer: "ann"},
		PlayingFile{LinkID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := e.lib.MarkBrokenPlaying(e.ctx, id, PlayingFile{LinkID: 1, Target: e.linkTarget(1)}, true, nil); err != nil || !ok {
		t.Fatalf("marked %v: %v", ok, err)
	}
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT fixed_by || ' ' || new_release || ' ' || reported FROM playing_break WHERE id = ?`, id); got !=
		"replaced "+movieRelease+" replaced" {
		t.Errorf("incident %s", got)
	}
	if len(got) != 1 || got[0].Kind != outcome.Replaced || got[0].NewRelease != movieRelease || got[0].OldRelease != "Name" ||
		got[0].Title != "Amélie: The Movie (2001)" || got[0].Viewer != "ann" || len(got[0].Episodes) != 0 {
		t.Fatalf("outcomes %+v", got)
	}
	e.exec(`UPDATE job SET state = 'done'`)
	e.exec(`UPDATE download SET state = 'imported'`)
	if err := e.lib.SettlePlayingRuns(e.ctx); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("outcomes after settling %+v", got)
	}
}

// A broken file holding E01 and E02 of which an import brings only E01 is not replaced: its row stays open for E02
// and nothing is reported; the import that brings E02 fixes it.
func TestPlayingPartialImportNotReplaced(t *testing.T) {
	e := newEnv(t, true)
	e.pol.Router = &policy.Router{Enabled: true}
	var got []outcome.Outcome
	e.lib.OnPlayingOutcome(func(o outcome.Outcome) { got = append(got, o) })
	e.release("Old.S01", map[string]float64{"both.mkv": 5400})
	e.exec(`INSERT INTO link (id, path, target, title_id, release_title, quality, state, created_at, updated_at)
		VALUES (1, ?, ?, 2, 'Old.S01', 'WEBDL-1080p', 'active', 0, 0)`, filepath.Join(e.shows, "Show (2020)", "both.mkv"),
		filepath.Join(e.mount, "__all__", "Old.S01", "both.mkv"))
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 20)`)
	e.exec(`INSERT INTO link_episode (link_id, episode_id) VALUES (1, 201), (1, 202)`)
	id, err := e.lib.OpenPlayingIncident(e.ctx, PlayingIncident{TitleID: 2, Version: "tv"}, PlayingFile{LinkID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := e.lib.MarkBrokenPlaying(e.ctx, id, PlayingFile{LinkID: 1, Target: e.linkTarget(1)}, true, nil); err != nil || !ok {
		t.Fatalf("marked %v: %v", ok, err)
	}
	importEp := func(dl int64, hash, ep string, eps string) {
		rel := "Show." + ep + ".2160p.WEB-DL.DDP5.1.H.265-GRP"
		e.release(rel, map[string]float64{rel + ".mkv": 2700})
		e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
			score, episodes, provider, name, state, created_at, updated_at, ready_at)
			VALUES (?, 2, 20, 'tv', ?, ?, 'idx', 'Bluray-2160p Remux', 100, ?, 'rd', ?, 'ready', 0, 0, 0)`,
			dl, hash, rel, eps, rel)
		if _, err := e.lib.Import(e.ctx, dl, false); err != nil {
			t.Fatal(err)
		}
	}
	importEp(1, strings.Repeat("1", 40), "S01E01", "[201]")
	if row := e.one(`SELECT fixed_by || '|' || reported FROM playing_break WHERE id = ?`, id); row != "|" || len(got) != 0 {
		t.Fatalf("after E01 only: row %q, outcomes %+v", row, got)
	}
	importEp(2, strings.Repeat("2", 40), "S01E02", "[202]")
	if got := e.one(`SELECT fixed_by || '|' || reported FROM playing_break WHERE id = ?`, id); got != "replaced|replaced" {
		t.Errorf("after E02: row %q", got)
	}
	if len(got) != 1 || got[0].Kind != outcome.Replaced {
		t.Errorf("outcomes %+v", got)
	}
}

// The settle pass looks only at what belongs to the incident's episodes: another season's queued search does not hold
// the outcome or the siblings' search back.
func TestSettlePlayingIgnoresOtherSeasons(t *testing.T) {
	e := playingEnv(t)
	e.exec(`INSERT INTO episode (id, title_id, season, number, name, created_at, updated_at) VALUES (204, 2, 2, 1, 'S2', 0, 0)`)
	id, err := e.lib.OpenPlayingIncident(e.ctx, PlayingIncident{TitleID: 2, Version: "tv"}, PlayingFile{LinkID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.lib.MarkBrokenPlaying(e.ctx, id, PlayingFile{LinkID: 2, Target: e.linkTarget(2)}, true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.lib.MarkBrokenPlaying(e.ctx, id, PlayingFile{LinkID: 3, Target: e.linkTarget(3)}, false, nil); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE job SET state = 'done'`)
	e.exec(`INSERT INTO acquire_run (source, title_id, version_id, version_key, target, reason, season, episodes, state,
		created_at, updated_at) VALUES ('wanted', 2, 20, 'tv', 's02', 'missing', 2, '[204]', 'queued', ?, ?)`,
		e.now.UnixMilli(), e.now.UnixMilli())
	e.exec(`INSERT INTO search_request (title_id, version_key, reason, season, created_at, next_at, tries, why)
		VALUES (2, 'tv', 'missing', 2, 0, 0, 0, 'stalled')`)
	if err := e.lib.SettlePlayingRuns(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT reported FROM playing_break WHERE id = ?`, id); got != "unavailable" {
		t.Errorf("reported %q with only another season pending", got)
	}
	if jobs := e.playingJobs(); len(jobs) != 2 || fmt.Sprint(jobs[1].Episodes) != "[203]" {
		t.Errorf("searches %+v", jobs)
	}
}

func (e *env) linkTarget(id int64) string {
	e.t.Helper()
	return e.one(`SELECT target FROM link WHERE id = ?`, id)
}

// Only the copy the check read is marked: a link that points elsewhere now (a same-path import during the check) is
// left, with no miss, no search and no history written.
func TestMarkBrokenPlayingOnlyTheCheckedCopy(t *testing.T) {
	e := playingEnv(t)
	id, err := e.lib.OpenPlayingIncident(e.ctx, PlayingIncident{TitleID: 2, Version: "tv"}, PlayingFile{LinkID: 2})
	if err != nil {
		t.Fatal(err)
	}
	read := e.linkTarget(2)
	e.exec(`UPDATE link SET target = ? WHERE id = 2`, filepath.Join(e.mount, "__all__", "Other", "a.mkv"))
	ok, err := e.lib.MarkBrokenPlaying(e.ctx, id, PlayingFile{LinkID: 2, Target: read}, true, []string{"rd"})
	if err != nil || ok {
		t.Fatalf("marked %v: %v", ok, err)
	}
	if got := e.one(`SELECT state FROM link WHERE id = 2`) + " " + e.one(`SELECT COUNT(*) FROM provider_cache`) + " " +
		e.one(`SELECT COUNT(*) FROM library_action WHERE kind = 'dead'`); got != "active 0 0" {
		t.Errorf("after a changed copy: %s", got)
	}
	if jobs := e.playingJobs(); len(jobs) != 0 {
		t.Errorf("searches %+v", jobs)
	}
}

// A failed download of the incident's search whose re-search the router has not queued yet (its blocklist row not
// researched) still counts as pending: no outcome, no siblings' search before the retry.
func TestSettlePlayingWaitsForBlocklistRetry(t *testing.T) {
	e := playingEnv(t)
	id, err := e.lib.OpenPlayingIncident(e.ctx, PlayingIncident{TitleID: 2, Version: "tv"}, PlayingFile{LinkID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.lib.MarkBrokenPlaying(e.ctx, id, PlayingFile{LinkID: 2, Target: e.linkTarget(2)}, true, nil); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE job SET state = 'done'`)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		score, episodes, provider, name, state, created_at, updated_at) VALUES (7, 2, 20, 'tv', ?, 'Show.S01E02.X',
		'idx', 'WEBDL-1080p', 1, '[202]', 'rd', 'x', 'failed', 0, 0)`, strings.Repeat("7", 40))
	e.exec(`INSERT INTO router_blocklist (version_key, info_hash, title_id, reason, source, download_id, created_at)
		VALUES ('tv', ?, 2, 'stalled', 'stalled', 7, ?)`, strings.Repeat("7", 40), e.now.UnixMilli())
	if err := e.lib.SettlePlayingRuns(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT reported FROM playing_break WHERE id = ?`, id); got != "" {
		t.Errorf("reported %q before the router queued the retry", got)
	}
	e.exec(`UPDATE router_blocklist SET researched_at = ?`, e.now.UnixMilli())
	if err := e.lib.SettlePlayingRuns(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT reported FROM playing_break WHERE id = ?`, id); got != "unavailable" {
		t.Errorf("reported %q once nothing is pending", got)
	}
}

// While now playing's sibling pass holds an incident, the settle pass sends its outcome but does not queue the
// siblings' search (a sibling found later would miss it); once released, the next pass queues it.
func TestSettlePlayingWaitsForTheSiblingPass(t *testing.T) {
	e := playingEnv(t)
	id, err := e.lib.OpenPlayingIncident(e.ctx, PlayingIncident{TitleID: 2, Version: "tv"}, PlayingFile{LinkID: 2})
	if err != nil {
		t.Fatal(err)
	}
	release := e.lib.HoldPlayingSiblings(id)
	if ok, err := e.lib.MarkBrokenPlaying(e.ctx, id, PlayingFile{LinkID: 2, Target: e.linkTarget(2)}, true, nil); err != nil || !ok {
		t.Fatalf("marked %v: %v", ok, err)
	}
	if ok, err := e.lib.MarkBrokenPlaying(e.ctx, id, PlayingFile{LinkID: 1, Target: e.linkTarget(1)}, false, nil); err != nil || !ok {
		t.Fatalf("sibling %v: %v", ok, err)
	}
	e.exec(`UPDATE job SET state = 'done'`)
	if err := e.lib.SettlePlayingRuns(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT reported FROM playing_break WHERE id = ?`, id); got != outcome.Unavailable {
		t.Errorf("reported %q", got)
	}
	if jobs := e.playingJobs(); len(jobs) != 1 {
		t.Fatalf("the siblings' search queued while the pass holds the incident: %+v", jobs)
	}
	release()
	release() // a second call does nothing
	if err := e.lib.SettlePlayingRuns(e.ctx); err != nil {
		t.Fatal(err)
	}
	if jobs := e.playingJobs(); len(jobs) != 2 || fmt.Sprint(jobs[1].Episodes) != "[201]" {
		t.Errorf("after the release: %+v", jobs)
	}
}

// PlayingSearchable is the round's own eligibility for a file: a film whose title and version are monitored, or a
// series file with at least one episode the round would search (title and version monitored, the episode monitored
// in that version and not dropped), in a version whose library is in the settings.
func TestPlayingSearchable(t *testing.T) {
	for _, c := range []struct {
		name    string
		q       string
		link    int64
		version string
		want    bool
	}{
		{"monitored", ``, 2, "tv", true},
		{"episode unmonitored", `UPDATE episode_version_state SET monitored = 0 WHERE episode_id = 202`, 2, "tv", false},
		{"another episode unmonitored", `UPDATE episode_version_state SET monitored = 0 WHERE episode_id = 202`, 1, "tv", true},
		{"episode dropped", `UPDATE episode SET removed_at = 1 WHERE id = 202`, 2, "tv", false},
		{"version unmonitored", `UPDATE version SET monitored = 0 WHERE id = 20`, 2, "tv", false},
		{"title unmonitored", `UPDATE title SET monitored = 0 WHERE id = 2`, 2, "tv", false},
		{"removed from the arr view", `INSERT INTO arrview_removed_version (version_id, view, removed_at) VALUES (20, 'ui', 0)`, 2, "tv", false},
		{"no such library", ``, 2, "nope", false},
	} {
		e := playingEnv(t)
		if c.q != "" {
			e.exec(c.q)
		}
		if got, err := e.lib.PlayingSearchable(e.ctx, c.link, c.version); err != nil || got != c.want {
			t.Errorf("%s: %v %v, want %v", c.name, got, err, c.want)
		}
	}
	e, _, _ := deadEnv(t, true)
	if got, err := e.lib.PlayingSearchable(e.ctx, 1, "movie-remux"); err != nil || !got {
		t.Errorf("film: %v %v", got, err)
	}
	e.exec(`UPDATE version SET monitored = 0 WHERE id = 10`)
	if got, err := e.lib.PlayingSearchable(e.ctx, 1, "movie-remux"); err != nil || got {
		t.Errorf("film unmonitored: %v %v", got, err)
	}
}

// At start, an incident cut off before its file was marked or searched (a restart during step 1), whose link is
// still the active copy it read, is dropped: Now playing does not list it and the day's bound does not count it.
// A marked incident (and its siblings waiting for their search), or one settled, stays.
func TestDropUnfinishedPlaying(t *testing.T) {
	e := playingEnv(t)
	cut, err := e.lib.OpenPlayingIncident(e.ctx, PlayingIncident{TitleID: 2, Version: "tv"}, PlayingFile{LinkID: 2})
	if err != nil {
		t.Fatal(err)
	}
	marked, err := e.lib.OpenPlayingIncident(e.ctx, PlayingIncident{TitleID: 2, Version: "tv"}, PlayingFile{LinkID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := e.lib.MarkBrokenPlaying(e.ctx, marked, PlayingFile{LinkID: 1, Target: e.linkTarget(1)}, true, nil); err != nil || !ok {
		t.Fatalf("marked %v: %v", ok, err)
	}
	if ok, err := e.lib.MarkBrokenPlaying(e.ctx, marked, PlayingFile{LinkID: 3, Target: e.linkTarget(3)}, false, nil); err != nil || !ok {
		t.Fatalf("sibling %v: %v", ok, err)
	}
	copied, err := e.lib.OpenPlayingIncident(e.ctx, PlayingIncident{TitleID: 2, Version: "tv"}, PlayingFile{LinkID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.lib.SettlePlayingIncident(e.ctx, copied, "copy", ""); err != nil {
		t.Fatal(err)
	}
	if n, err := e.lib.DropUnfinishedPlaying(e.ctx); err != nil || n != 1 {
		t.Fatalf("dropped %d: %v", n, err)
	}
	if got := e.one(`SELECT group_concat(incident || ':' || link_id) FROM (SELECT incident, link_id FROM playing_break ORDER BY id)`); got !=
		fmt.Sprintf("%d:1,%d:3,%d:2", marked, marked, copied) {
		t.Errorf("rows left %s (cut %d)", got, cut)
	}
}
