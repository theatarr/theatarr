package library

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/library/probe"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/router/routertest"
)

// withFormats rebuilds the library with recording format functions.
func (e *env) withFormats(decided, matched *[]string) {
	e.lib = New(Options{Store: e.st, Policy: e.pol, Queue: e.queue, Now: func() time.Time { return e.now },
		Probe: func(p string, _ int64) (*probe.Result, error) {
			if r, ok := e.probes[p]; ok {
				return r, nil
			}
			return nil, probe.ErrUnknownFormat
		},
		Formats: func(_ context.Context, _ string, _ int64, release string) []string {
			*decided = append(*decided, release)
			return nil
		},
		Matched: func(_ context.Context, _ string, names []string) []string {
			*matched = append(*matched, strings.Join(names, ","))
			return names
		}})
}

// The router's matched formats name the file; a download without them decides the release title again.
func TestImportUsesStoredFormats(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	var decided, matched []string
	e.withFormats(&decided, &matched)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	e.exec(`UPDATE download SET formats = '[{"name":"HDR","score":500},{"name":"Remux Tier 01","score":1900}]' WHERE id = 1`)
	if _, err := e.lib.Import(e.ctx, 1, true); err != nil {
		t.Fatal(err)
	}
	if len(decided) != 0 || !slices.Equal(matched, []string{"HDR,Remux Tier 01"}) {
		t.Errorf("stored formats: decided %v, matched %v", decided, matched)
	}
	decided, matched = nil, nil
	e.exec(`UPDATE download SET formats = NULL WHERE id = 1`)
	if _, err := e.lib.Import(e.ctx, 1, true); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(decided, []string{movieRelease}) || len(matched) != 0 {
		t.Errorf("no stored formats: decided %v, matched %v", decided, matched)
	}
}

// A guard rejection blocklists the hash for the version, and the router skips it from then on: no add.
func TestRejectBlocklistsForRouter(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 40 * 60})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT reason || ' / ' || source FROM router_blocklist WHERE version_key = 'movie-remux' AND
		info_hash = ?`, hashA); !strings.Contains(got, "runtime") || !strings.HasSuffix(got, "/ library") {
		t.Fatalf("blocklist row %q", got)
	}
	// The router over the same database: the version active and debrid, writes on.
	rp := &policy.Policy{
		Indexers: map[string]*policy.Indexer{"idx": {Kind: "torznab", URL: "http://idx.test/api",
			Privacy: policy.PrivacyPublic}},
		Providers: map[string]*policy.Provider{"rd": {Kind: provider.KindRealDebrid, Order: 1,
			URL: "http://rd.test/rest/1.0", APIKeyEnv: "THEATARR_RD_KEY", Pacing: policy.Pacing{AddsPerMinute: 10}}},
		Versions: map[string]*policy.VersionPolicy{"movie-remux": {Mode: policy.ModeActive, Kind: policy.KindMovie,
			Storage: policy.StorageDebrid, Indexers: []string{"idx"}, Providers: []string{"rd"}}},
		Router: &policy.Router{Enabled: true},
	}
	rt, err := routertest.New(e.st, rp, nil, nil, func() time.Time { return e.now })
	if err != nil {
		t.Fatal(err)
	}
	res, err := rt.Route(e.ctx, router.Request{TitleID: 1, VersionID: 10, Version: "movie-remux",
		Candidates: []router.Candidate{{Release: indexer.Release{Indexer: "idx", Kind: "torznab", Title: movieRelease,
			InfoHash: hashA, Privacy: policy.PrivacyPublic}}}})
	if err != nil {
		t.Fatal(err)
	}
	if rt.Writes() != 0 || res.Outcome != router.OutcomeExhausted || len(res.Steps) != 1 ||
		res.Steps[0].Code != "blocklisted" {
		t.Errorf("route %+v, writes %d", res, rt.Writes())
	}
}
