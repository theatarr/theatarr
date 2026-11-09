package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/web"
)

const dmm102Message = "DMM: no key linked for a provider in this path (error 102); link the TorBox key in DMM or remove tb from the path"

// A DMM source is tested through its configured search path, not the reduced rd/ad endpoint its RSS reads: with no
// TorBox key linked at DMM, rd/ad/tb/cached answers error 102 while rd/ad/cached answers, and the test fails with a
// fixed message naming the cause, in Settings Test and in preflight.
func TestDMMSettingsTestUsesSearchPath(t *testing.T) {
	t.Parallel()
	live := newProbeServer(t)
	a := newTesterAPI(t, live, &web.SettingsTestLimits{Cooldown: -1}, "tmdb")
	code, out := a.post("/indexers/test", map[string]any{"id": a.dmm, "config": dmmConfig(live.URL + "/api/torznab/rd/ad/tb/cached")})
	if code != http.StatusOK || out["status"] != "failed" || out["category"] != "dmm_provider_key" || out["message"] != dmm102Message {
		t.Fatalf("rd/ad/tb/cached without a TorBox key at DMM: %d %v", code, out)
	}
	// Preflight runs the same test.
	pol := *a.manager.Current().Policy
	ix := *pol.Indexers["dmm"]
	ix.URL = live.URL + "/api/torznab/rd/ad/tb/cached"
	pol.Indexers = map[string]*policy.Indexer{"dmm": &ix}
	srcs := offlineSources(context.Background(), &pol, a.gw, "indexers",
		func(string, string, string) string { return "live-key" }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if len(srcs) != 1 || srcs[0].Status != "failed" || srcs[0].Category != "dmm_provider_key" || srcs[0].Message != dmm102Message {
		t.Fatalf("preflight: %+v", srcs)
	}
	// The same source on rd/ad answers.
	code, out = a.post("/indexers/test", map[string]any{"id": a.dmm, "config": dmmConfig(live.URL + "/api/torznab/rd/ad/cached")})
	if code != http.StatusOK || out["status"] != "ok" {
		t.Fatalf("rd/ad/cached: %d %v", code, out)
	}
}

// The DMM test is an IMDb movie search only: when it fails, a text search that would answer does not make it pass.
func TestDMMSettingsTestIsIMDbOnly(t *testing.T) {
	t.Parallel()
	live := newProbeServer(t)
	a := newTesterAPI(t, live, &web.SettingsTestLimits{Cooldown: -1}, "tmdb")
	code, out := a.post("/indexers/test", map[string]any{"id": a.dmm, "config": dmmConfig(live.URL + "/api/torznab/ad/tb/cached")})
	if code != http.StatusOK || out["status"] != "failed" {
		t.Fatalf("movie search error 900, text search empty: %d %v", code, out)
	}
}
