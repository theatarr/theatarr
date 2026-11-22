package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func TestNowPlayingStartupMasksURLPath(t *testing.T) {
	const token = "abc123TOKEN456secret789"
	ref := policy.SettingsSecretPrefix + "PLEX"
	pol := &policy.Policy{Library: &policy.Library{Plex: &policy.Plex{URL: "http://127.0.0.1:1/" + token + "/plex", TokenEnv: ref}}}
	pol.WithSettingsSecrets(map[string]string{ref: "invented-plex"})
	pol.Library.Mount.ZurgAPI = "http://127.0.0.1:1/" + token + "/zurg"
	gw, err := gateway.New(context.Background(), pol, gateway.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	rt := &serveRuntime{Policy: pol, Gateway: gw, Library: &serveLibrary{pol: pol, set: pol.LibrarySettings()}}
	var logs bytes.Buffer
	loops := serveLoops{log: slog.New(slog.NewTextHandler(&logs, nil)),
		current: func(ctx context.Context) (context.Context, *serveRuntime) { return ctx, rt }}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Log startup, then stop without making a request.
	if err := loops.runNowPlaying(ctx, rt); err != nil {
		t.Fatal(err)
	}
	if text := logs.String(); strings.Contains(text, token) || !strings.Contains(text, "127.0.0.1:1") ||
		!strings.Contains(text, "following Plex") {
		t.Fatalf("startup URL not safely logged: %s", text)
	}
}

// When the tracker starts, an incident a stop cut off before its file was marked or searched (link still active) is
// dropped.
func TestNowPlayingStartDropsUnfinishedIncident(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(dir, "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	ref := policy.SettingsSecretPrefix + "PLEX"
	root, mount := filepath.Join(dir, "movies"), filepath.Join(dir, "zurg")
	pol := &policy.Policy{
		Versions: map[string]*policy.VersionPolicy{"movie": {Kind: policy.KindMovie, Library: root}},
		Library: &policy.Library{Enabled: true, Mount: policy.LibraryMount{Path: mount},
			Plex:     &policy.Plex{URL: "http://127.0.0.1:1", TokenEnv: ref},
			Versions: map[string]*policy.LibraryVersion{"movie": {Roots: []string{root}}}}}
	pol.WithSettingsSecrets(map[string]string{ref: "invented-plex"})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	gw, err := gateway.New(ctx, pol, gateway.Options{Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(mount, "__all__", "A", "a.mkv")
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, year, created_at, updated_at) VALUES (1, 'movie', 'Film', 2002, 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (10, 1, 'movie', 0, 0)`,
	} {
		if _, err := st.Writer().ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Writer().ExecContext(ctx, `INSERT INTO link (id, path, target, title_id, state, created_at, updated_at)
		VALUES (1, ?, ?, 1, 'active', 0, 0)`, filepath.Join(root, "a.mkv"), target); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().ExecContext(ctx, `INSERT INTO playing_break (id, incident, watched, link_id, title_id,
		version_key, path, old_target, broken_at, updated_at) VALUES (1, 1, 1, 1, 1, 'movie', ?, ?, 0, 0)`,
		filepath.Join(root, "a.mkv"), target); err != nil {
		t.Fatal(err)
	}
	lb := &serveLibrary{pol: pol, st: st, log: log, set: pol.LibrarySettings(),
		lib: library.New(library.Options{Store: st, Policy: pol, Logger: log})}
	rt := &serveRuntime{Policy: pol, Gateway: gw, Library: lb}
	loops := serveLoops{log: log, current: func(ctx context.Context) (context.Context, *serveRuntime) { return ctx, rt }}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- loops.runNowPlaying(runCtx, rt) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(10 * time.Second)
	for {
		var n int
		if err := st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM playing_break`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the unfinished incident was not dropped at start")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
