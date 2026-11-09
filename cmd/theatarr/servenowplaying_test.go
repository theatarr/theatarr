package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
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
