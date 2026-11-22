package library

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"
)

// The put-back (putback.go): a replaced copy that reads again on two sweeps comes back in place of its replacement
// only when it wins strictly under today's profile and nothing stops it; else it is kept aside as before.

// restored checks a put-back: the old copy linked again at its path, the replacement gone from the library, the row
// settled restored with its miss lifted, one outcome restored, one Plex scan, no provider call.
func (c *coveredEnv) restored(t *testing.T) {
	t.Helper()
	if c.settled() != "restored" || !c.missLifted() {
		t.Errorf("settled %q, miss lifted %v", c.settled(), c.missLifted())
	}
	if got, _ := os.Readlink(c.oldPath); got != c.oldTgt {
		t.Errorf("old path points at %q", got)
	}
	if c.newPath != c.oldPath {
		if _, err := os.Lstat(c.newPath); err == nil {
			t.Errorf("the replacement stayed in the library")
		}
	}
	if got := c.one(`SELECT state || ' ' || quality FROM link WHERE id = 1`); got != "active Remux-2160p" {
		t.Errorf("old row %s", got)
	}
	if got := c.kinds(); !slices.Equal(got, []string{"restored"}) {
		t.Errorf("outcomes %v", got)
	}
	if got := c.one(`SELECT COUNT(*) FROM job WHERE kind = ?`, ScanJobKind); got != "1" {
		t.Errorf("scans %s", got)
	}
	// The incident's own search (acquire.research) was queued when it broke; nothing else, no provider job.
	if got := c.one(`SELECT COALESCE(group_concat(kind), '') FROM job WHERE kind NOT IN (?, 'acquire.research')`,
		ScanJobKind); got != "" {
		t.Errorf("other jobs %s", got)
	}
	if got := c.one(`SELECT COUNT(*) FROM history WHERE event IN ('removed', 'undone')`); got != "0" {
		t.Errorf("history rows %s", got)
	}
	if got := c.one(`SELECT COUNT(*) FROM router_blocklist`); got != "0" {
		t.Errorf("blocklist rows %s", got)
	}
}

func TestPutBack(t *testing.T) {
	t.Run("better, same path", func(t *testing.T) {
		c := newCoveredEnv(t, "Remux-2160p", "WEBDL-1080p", true)
		c.twoReads()
		c.restored(t)
		// Once: a later pass changes nothing and sends nothing.
		c.twoReads()
		if got := c.kinds(); len(got) != 1 {
			t.Errorf("outcomes %v", got)
		}
	})
	t.Run("better, other path", func(t *testing.T) {
		c := newCoveredEnv(t, "Remux-2160p", "WEBDL-1080p", false)
		c.twoReads()
		c.restored(t)
		if got := c.one(`SELECT state FROM link WHERE id = 2`); got != "unlinked" {
			t.Errorf("replacement %s", got)
		}
	})
	aside := func(t *testing.T, c *coveredEnv) {
		t.Helper()
		c.twoReads()
		if c.settled() != "aside" {
			t.Errorf("settled %q", c.settled())
		}
		if got, _ := os.Readlink(c.newPath); got != c.newTgt {
			t.Errorf("replacement %q", got)
		}
		if len(c.kinds()) != 0 {
			t.Errorf("outcomes %v", c.kinds())
		}
	}
	t.Run("tie", func(t *testing.T) { aside(t, newCoveredEnv(t, "WEBDL-1080p", "WEBDL-1080p", false)) })
	t.Run("worse", func(t *testing.T) { aside(t, newCoveredEnv(t, "WEBDL-1080p", "Remux-2160p", false)) })
	t.Run("unknown", func(t *testing.T) { aside(t, newCoveredEnv(t, "Unknown", "WEBDL-1080p", false)) })
	t.Run("current unknown", func(t *testing.T) { aside(t, newCoveredEnv(t, "Remux-2160p", "Unknown", false)) })
	t.Run("SeaDex library", func(t *testing.T) {
		c := newCoveredEnv(t, "Remux-2160p", "WEBDL-1080p", false)
		c.pol.Versions["movie-remux"].PreferSeaDex = true
		aside(t, c)
	})
	t.Run("profile no longer allows it", func(t *testing.T) {
		c := newCoveredEnv(t, "Remux-2160p", "WEBDL-1080p", false)
		c.pol.Profiles["movie-state"].MinScore = 100
		aside(t, c)
	})
	t.Run("wrong audio", func(t *testing.T) {
		c := newCoveredEnv(t, "Remux-2160p", "WEBDL-1080p", false)
		c.pol.Profiles["movie-state"].Language = "English"
		c.exec(`UPDATE content_file SET probe = '{"container":"matroska","audio":[{"codec":"aac","language":"fre","tagged":true}]}'
			WHERE id = 1`)
		aside(t, c)
	})
	t.Run("audio inconclusive", func(t *testing.T) {
		// As at import: an untagged track makes the rule say nothing, so the better copy comes back.
		c := newCoveredEnv(t, "Remux-2160p", "WEBDL-1080p", false)
		c.pol.Profiles["movie-state"].Language = "English"
		c.exec(`UPDATE content_file SET probe = '{"container":"matroska","audio":[{"codec":"aac","language":"fre","tagged":true},
			{"codec":"aac","language":"und"}]}' WHERE id = 1`)
		c.twoReads()
		c.restored(t)
	})
	wait := func(t *testing.T, c *coveredEnv, stop func(), go_ func()) {
		t.Helper()
		stop()
		c.twoReads()
		if c.settled() != "" || len(c.kinds()) != 0 {
			t.Fatalf("while stopped: settled %q, outcomes %v", c.settled(), c.kinds())
		}
		if got, _ := os.Readlink(c.newPath); got != c.newTgt {
			t.Errorf("replacement %q", got)
		}
		go_()
		if err := c.lib.ComeBack(c.ctx); err != nil {
			t.Fatal(err)
		}
		c.restored(t)
	}
	t.Run("paused", func(t *testing.T) {
		c := newCoveredEnv(t, "Remux-2160p", "WEBDL-1080p", false)
		wait(t, c, func() { c.pol.Router.Enabled = false }, func() { c.pol.Router.Enabled = true })
	})
	t.Run("being played", func(t *testing.T) {
		c := newCoveredEnv(t, "Remux-2160p", "WEBDL-1080p", false)
		wait(t, c, func() { c.playing = []int64{2} }, func() { c.playing = nil })
	})
	t.Run("path busy", func(t *testing.T) {
		// A passing refusal (another run holds the path's lease) leaves the incident open for the next sweep.
		c := newCoveredEnv(t, "Remux-2160p", "WEBDL-1080p", false)
		if err := c.lib.ComeBack(c.ctx); err != nil {
			t.Fatal(err)
		}
		c.now = c.now.Add(deadConfirm + time.Minute)
		_, unlock, err := c.lib.lockLinks(context.Background(), c.oldPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.lib.ComeBack(c.ctx); err != nil {
			t.Fatal(err)
		}
		if c.settled() != "" || len(c.kinds()) != 0 {
			t.Fatalf("while busy: settled %q", c.settled())
		}
		unlock()
		if err := c.lib.ComeBack(c.ctx); err != nil {
			t.Fatal(err)
		}
		c.restored(t)
	})
	t.Run("search open", func(t *testing.T) {
		c := newCoveredEnv(t, "Remux-2160p", "WEBDL-1080p", false)
		wait(t, c, func() { c.exec(`UPDATE playing_break SET searched = 1, reported = '' WHERE id = ?`, c.id) },
			func() { c.exec(`UPDATE playing_break SET reported = 'replaced' WHERE id = ?`, c.id) })
	})
	t.Run("unclear session", func(t *testing.T) {
		c := newCoveredEnv(t, "Remux-2160p", "WEBDL-1080p", false)
		wait(t, c, func() { c.unclear = []int64{1} }, func() { c.unclear = []int64{2} })
	})
	t.Run("Plex silent", func(t *testing.T) {
		c := newCoveredEnv(t, "Remux-2160p", "WEBDL-1080p", false)
		wait(t, c, func() { c.silent = true }, func() { c.silent = false })
	})
}
