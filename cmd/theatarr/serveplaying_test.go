package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/notify"
	"github.com/theatarr/theatarr/internal/outcome"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

type noDoer struct{}

func (noDoer) Do(context.Context, gateway.Target, *http.Request) (*http.Response, error) {
	return nil, errors.New("not sent in this test")
}

// Each kind of outcome of a file that broke while playing reaches a notification target once, and the title's
// History, through the hook serve gives every library: replaced, readable again and nothing available as the library
// reports them (a second report sends nothing), restored as an outcome made by hand.
func TestPlayingOutcomesReachNotifyAndHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pol := &policy.Policy{Versions: map[string]*policy.VersionPolicy{"movie-web": {Kind: policy.KindMovie}},
		Notify: &policy.Notify{Enabled: true, Targets: map[string]*policy.NotifyTarget{
			"hook": {URL: "http://192.168.1.50:8080/hook", Native: true}}}}
	nt, err := notify.New(notify.Config{Store: st, Policy: pol, Doer: noDoer{}, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.Writer().Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	now := store.Millis(time.Now())
	exec(`INSERT INTO title (id, kind, name, year, tmdb_id, created_at, updated_at) VALUES (1, 'movie', 'Alpha', 2020,
		101, ?, ?)`, now, now)
	for id := 1; id <= 4; id++ {
		exec(`INSERT INTO playing_break (id, incident, watched, link_id, title_id, version_key, path, old_target,
			release_title, episodes, broken_at, viewer, player, updated_at)
			VALUES (?, ?, 1, ?, 1, 'movie-web', '/media/Alpha.mkv', '/mnt/old/Alpha.mkv', 'Alpha.2020.1080p-OLD', '[]',
			?, 'ann', 'TV', ?)`, id, id, 10+id, now, now)
	}
	lib := library.New(library.Options{Store: st, Policy: pol, Logger: log})
	playing := &playingOutcomes{st: st}
	playing.attach(lib)
	playing.nt.Store(nt)
	for id, kind := range map[int64]string{1: outcome.Replaced, 2: outcome.SameRelease, 3: outcome.Unavailable} {
		for range 2 {
			if err := lib.ReportPlaying(ctx, id, kind); err != nil {
				t.Fatal(err)
			}
		}
	}
	playing.report(outcome.Outcome{Incident: 4, Kind: outcome.Restored, TitleID: 1, VersionKey: "movie-web",
		Title: "Alpha (2020)", OldRelease: "Alpha.2020.1080p-OLD", At: time.Now()})
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := st.Reader().QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for ev, want := range map[string]int{"theatarr.playing_replaced": 2, "theatarr.playing_unavailable": 1,
		"theatarr.playing_restored": 1} {
		if n := count(`SELECT COUNT(*) FROM notify_delivery WHERE target = 'policy:hook' AND event = ?`, ev); n != want {
			t.Errorf("%s: %d deliveries, want %d", ev, n, want)
		}
	}
	for ref, kind := range map[string]string{"pb:1:replaced": outcome.Replaced, "pb:2:same_release": outcome.SameRelease,
		"pb:3:unavailable": outcome.Unavailable, "pb:4:restored": outcome.Restored} {
		if n := count(`SELECT COUNT(*) FROM history WHERE ref = ? AND event = 'playing' AND source = ? AND title_id = 1
			AND version_key = 'movie-web' AND release = 'Alpha.2020.1080p-OLD'`, ref, kind); n != 1 {
			t.Errorf("history %s: %d rows", ref, n)
		}
	}
	if n := count(`SELECT COUNT(*) FROM history WHERE event = 'playing'`); n != 4 {
		t.Errorf("%d History rows, want 4", n)
	}
}

// library.plex.stop_with_message follows the settings in force, so a save turns stopping a broken stream on or off
// without restarting the tracker.
func TestStopWithMessageFollowsSettings(t *testing.T) {
	t.Parallel()
	off := false
	start := (&policy.Policy{Library: &policy.Library{Plex: &policy.Plex{URL: "http://plex:32400"}}}).LibrarySettings()
	saved := (&policy.Policy{Library: &policy.Library{Plex: &policy.Plex{URL: "http://plex:32400",
		StopWithMessage: &off}}}).LibrarySettings()
	if !stopWithMessage(nil, start) {
		t.Error("default off")
	}
	if stopWithMessage(&serveRuntime{Library: &serveLibrary{set: saved}}, start) {
		t.Error("a save that turned it off is not followed")
	}
	if !stopWithMessage(&serveRuntime{Library: &serveLibrary{set: start}}, saved) {
		t.Error("a save that turned it on is not followed")
	}
}
