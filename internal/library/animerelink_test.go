package library

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/errinbox"
	"github.com/theatarr/theatarr/internal/library/probe"
	"github.com/theatarr/theatarr/internal/metadata/anime/animemap"
	"github.com/theatarr/theatarr/internal/metadata/anime/animestore"
	"github.com/theatarr/theatarr/internal/policy"
)

// relinkEnv is the isekai series with its second season's mapping (al:2) in a conflict and a two-file release named
// by that entry imported through Sonarr's numbering (S01E01-E02), plus an open review item for the mapping.
func relinkEnv(t *testing.T) (*env, []string) {
	e := newEnv(t, true, policy.ModeActive)
	e.isekai()
	e.exec(`UPDATE anime_mapping SET status = 'conflict' WHERE entry_key = 'al:2'`)
	e.exec(`INSERT INTO anime_review (id, franchise, entry_key, kind, state, summary, created_at)
		VALUES (7, 'isekai', 'al:2', 'conflict', 'open', 'al:2', 0)`)
	rel := "[Group] Isekai Story 2nd Season (BD 1080p)"
	e.release(rel, map[string]float64{"[Group] Isekai Story 2nd Season - 01 (BD 1080p).mkv": 1440,
		"[Group] Isekai Story 2nd Season - 02 (BD 1080p).mkv": 1440})
	e.videoSize(rel, 1920, 1080)
	e.seriesDownload(1, 3, 30, hashA, rel, "[301, 302]", nil)
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	got := e.links(plan)
	if len(plan.Links) != 2 || !strings.Contains(got, "S01E01") || !strings.Contains(got, "S01E02") {
		t.Fatalf("conflict import: links\n%s\nunmatched %v rejected %+v", got, plan.Unmatched, plan.Rejected)
	}
	var paths []string
	for _, l := range plan.Links {
		paths = append(paths, l.Path)
	}
	return e, paths
}

// withLibrary rebuilds the env's library on the same store with a changed policy.
func (e *env) withLibrary(change func(*policy.Policy)) {
	change(e.pol)
	engine, err := decide.New(e.pol, decide.Options{Now: func() time.Time { return e.now }})
	if err != nil {
		e.t.Fatal(err)
	}
	e.lib = New(Options{Store: e.st, Policy: e.pol, Engine: engine, Queue: e.queue, Now: func() time.Time { return e.now },
		Probe:   func(string, int64) (*probe.Result, error) { return nil, probe.ErrUnknownFormat },
		Formats: func(context.Context, string, int64, string) []string { return nil }})
	e.t.Cleanup(e.lib.mount.closeHelpers)
}

func (e *env) inbox() *[]errinbox.Event {
	var got []errinbox.Event
	e.lib.inbox = func(ev errinbox.Event) { got = append(got, ev) }
	return &got
}

// Accepting the mapping in review queues the series' re-link; the job moves both files to the episodes the entry
// gives them, with the version's naming, records the episodes on the download's file identity, leaves a journal
// entry for the decision and queues the Plex scans. A second run finds nothing to do.
func TestAnimeRelinkAfterReview(t *testing.T) {
	t.Parallel()
	e, before := relinkEnv(t)
	e.withLibrary(func(p *policy.Policy) { p.Library.Versions["tv"].PlexSection = 19 })
	as := &animestore.Store{DB: e.st, Now: func() time.Time { return e.now }}
	if err := as.Resolve(e.ctx, 7, true, ""); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT count(*) || ' ' || payload FROM job WHERE kind = ? AND dedupe_key = ? AND state = 'queued'`,
		animemap.RelinkJobKind, animemap.RelinkDedupeKey(2002)); got != `1 {"tvdb_id":2002}` {
		t.Fatalf("relink job %s", got)
	}
	res, err := e.lib.RelinkAnime(e.ctx, 2002)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Moved) != 2 || len(res.Planned)+len(res.Occupied) != 0 {
		t.Fatalf("relink %+v", res)
	}
	got := e.one(`SELECT group_concat(l.path || '=' || e.season || 'x' || e.number, ' | ') FROM link l
		JOIN link_episode le ON le.link_id = l.id JOIN episode e ON e.id = le.episode_id WHERE l.state = 'active'`)
	for _, w := range []string{"Season 02/Isekai Story - S02E01 - Episode Bluray-1080p.mkv=2x1",
		"Season 02/Isekai Story - S02E02 - Episode Bluray-1080p.mkv=2x2"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %s in %s", w, got)
		}
	}
	for _, p := range before {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("old link %s still there", p)
		}
	}
	if got := e.one(`SELECT group_concat(mode || ' ' || download_id || ' ' || detail, ' | ') FROM library_action
		WHERE kind = 'link' AND detail LIKE 'relinked%'`); !strings.Contains(got, "applied 1 relinked after review: S01E01 → S02E01") ||
		!strings.Contains(got, "applied 1 relinked after review: S01E02 → S02E02") {
		t.Errorf("journal actions %s", got)
	}
	if got := e.one(`SELECT group_concat(episodes || ' ' || how, ' | ') FROM download_file WHERE download_id = 1`); !strings.Contains(got, "[313] file+franchise:al:2") {
		t.Errorf("file identity %s", got)
	}
	if got := e.one(`SELECT count(*) FROM job WHERE kind = ?`, ScanJobKind); got == "0" {
		t.Error("no plex scan queued")
	}
	snap := tree(t, e.dir)
	if res, err := e.lib.RelinkAnime(e.ctx, 2002); err != nil || len(res.Moved)+len(res.Planned)+len(res.Occupied) != 0 {
		t.Errorf("second run %+v %v", res, err)
	}
	if strings.Join(tree(t, e.dir), "\n") != strings.Join(snap, "\n") {
		t.Error("the second run changed the tree")
	}
}

// A target episode that already has another file (Theatarr's or the arr's) is skipped: both stay, and the error inbox
// names the two paths. The other file moves.
func TestAnimeRelinkOccupiedTarget(t *testing.T) {
	t.Parallel()
	e, _ := relinkEnv(t)
	e.exec(`INSERT INTO version_file (id, version_id, title_id, instance, arr_file_id, path) VALUES (1, 30, 3, 'sonarr', 1, '/arr/Isekai S02E02.mkv')`)
	e.exec(`INSERT INTO version_file_episode (version_file_id, episode_id) VALUES (1, 314)`)
	e.exec(`UPDATE anime_mapping SET status = 'override' WHERE entry_key = 'al:2'`)
	got := e.inbox()
	snap := tree(t, e.dir)
	res, err := e.lib.RelinkAnime(e.ctx, 2002)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Moved) != 1 || len(res.Occupied) != 1 || !strings.Contains(res.Moved[0], "S01E01 → S02E01") {
		t.Fatalf("relink %+v", res)
	}
	if len(*got) != 1 || !strings.Contains((*got)[0].Message, "/arr/Isekai S02E02.mkv") ||
		!strings.Contains((*got)[0].Message, "S01E02 - Episode") || (*got)[0].Source != errinbox.SourceLog {
		t.Errorf("inbox %+v", *got)
	}
	if strings.Join(tree(t, e.dir), "\n") == strings.Join(snap, "\n") {
		t.Error("nothing moved")
	}
	if got := e.one(`SELECT count(*) FROM link l JOIN link_episode le ON le.link_id = l.id WHERE le.episode_id = 302
		AND l.state = 'active'`); got != "1" {
		t.Errorf("the occupied file moved: %s", got)
	}
}

// With the gate closed (library off, or the version followed or in shadow) the job records the plan and changes
// nothing. A mapping that agrees with where Sonarr's numbering put the file, an adopted link and a file whose episodes
// a person chose move nothing either.
func TestAnimeRelinkPlanOnly(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		change func(*policy.Policy)
	}{
		{"library off", func(p *policy.Policy) { p.Library.Enabled = false }},
		{"shadow version", func(p *policy.Policy) { p.Versions["tv"].Mode = policy.ModeShadow }},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e, _ := relinkEnv(t)
			e.exec(`UPDATE anime_mapping SET status = 'override' WHERE entry_key = 'al:2'`)
			e.withLibrary(c.change)
			snap := tree(t, e.dir)
			res, err := e.lib.RelinkAnime(e.ctx, 2002)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Planned) != 2 || len(res.Moved) != 0 {
				t.Fatalf("relink %+v", res)
			}
			if strings.Join(tree(t, e.dir), "\n") != strings.Join(snap, "\n") {
				t.Error("a plan changed the tree")
			}
			if got := e.one(`SELECT count(*) FROM library_action WHERE mode = 'dry_run' AND download_id = 1
				AND detail LIKE 'relinked after review: S01E01 → S02E01 (%'`); got != "1" {
				t.Errorf("planned actions %s", got)
			}
		})
	}

	t.Run("nothing to move", func(t *testing.T) {
		t.Parallel()
		e, _ := relinkEnv(t)
		e.exec(`UPDATE anime_mapping SET status = 'override' WHERE entry_key = 'al:2'`)
		// one file adopted (no download), the other chosen by hand
		e.exec(`UPDATE link SET download_id = NULL WHERE id = (SELECT link_id FROM link_episode WHERE episode_id = 301)`)
		e.exec(`UPDATE download_file SET how = 'manual' WHERE download_id = 1`)
		snap := tree(t, e.dir)
		if res, err := e.lib.RelinkAnime(e.ctx, 2002); err != nil || len(res.Moved)+len(res.Planned)+len(res.Occupied) != 0 {
			t.Fatalf("relink %+v %v", res, err)
		}
		if strings.Join(tree(t, e.dir), "\n") != strings.Join(snap, "\n") {
			t.Error("the tree changed")
		}
	})
}
