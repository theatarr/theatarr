package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

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
	n, err := e.lib.MarkBrokenPlaying(e.ctx, id, []PlayingFile{{LinkID: 2, Offset: 4096}}, true, []string{"realdebrid"})
	if err != nil || n != 1 {
		t.Fatalf("marked %d: %v", n, err)
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
	if _, err := e.lib.MarkBrokenPlaying(e.ctx, id, []PlayingFile{{LinkID: 2}}, true, nil); err != nil {
		t.Fatal(err)
	}
	for _, link := range []int64{1, 3} {
		if n, err := e.lib.MarkBrokenPlaying(e.ctx, id, []PlayingFile{{LinkID: link, Offset: 7}}, false, nil); err != nil || n != 1 {
			t.Fatalf("sibling %d: %d %v", link, n, err)
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
	if _, err := e.lib.MarkBrokenPlaying(e.ctx, id, []PlayingFile{{LinkID: 2}}, true, nil); !errors.Is(err, ErrPlayingStopped) {
		t.Errorf("paused: %v", err)
	}
	if got := e.one(`SELECT state FROM link WHERE id = 2`); got != "active" {
		t.Errorf("link %s", got)
	}
	if jobs := e.playingJobs(); len(jobs) != 0 {
		t.Errorf("searches %+v", jobs)
	}
}
