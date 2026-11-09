package router

import (
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
)

// A settings-managed router reuses a torrent id from our own downloads only when it was recorded under the current
// provider epoch: after an account change the old account's ids (and legacy rows with no epoch) are not reused.
func TestHeldByIgnoresTorrentIDsFromAnotherProviderEpoch(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.r.settingsManaged, e.r.providerEpoch = true, 1
	e.exec(`INSERT INTO settings_runtime_state (id, last_published_generation, provider_epoch) VALUES (1, 1, 1)`)
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (3, 'movie', 'Other', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (30, 3, 'movie', 0, 0)`,
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (4, 'movie', 'Third', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (40, 4, 'movie', 0, 0)`,
	} {
		e.exec(q)
	}
	// Imported by the previous account (a row written before provider epochs were recorded).
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		provider_torrent_id, state, created_at, updated_at) VALUES (3, 30, 'movie', ?, 'A', 'pub', 'rd', 'rd-old-account',
		'imported', 0, 0)`, h("a"))
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "A")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Reused || res.TorrentID == "rd-old-account" || len(e.ad["rd"].adds) != 1 {
		t.Fatalf("route reused the previous account's torrent: %+v adds=%d (%s)", res, len(e.ad["rd"].adds), steps(res))
	}
	// A torrent this account added is reused by the next version that wants the hash (the reuse reads it first).
	e.pr["rd"].set(res.TorrentID, provider.Torrent{Hash: h("a"), Status: "downloading", State: provider.StateDownloading})
	next := Request{TitleID: 4, VersionID: 40, Version: "movie", Candidates: []Candidate{cand(h("a"), "A")}, Source: "test"}
	res2, err := e.r.Route(e.ctx, next)
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Reused || res2.TorrentID != res.TorrentID || len(e.ad["rd"].adds) != 1 {
		t.Fatalf("current-epoch torrent not reused: %+v adds=%d (%s)", res2, len(e.ad["rd"].adds), steps(res2))
	}
}

// A YAML router (--settings-source yaml) has no provider epoch and keeps reusing its own downloads' torrent ids.
func TestYAMLRouterReusesLegacyDownloadTorrentID(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.exec(`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (3, 'movie', 'Other', 0, 0)`)
	e.exec(`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (30, 3, 'movie', 0, 0)`)
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		provider_torrent_id, state, created_at, updated_at) VALUES (3, 30, 'movie', ?, 'A', 'pub', 'rd', 'rd-held',
		'imported', 0, 0)`, h("a"))
	e.pr["rd"].set("rd-held", provider.Torrent{Hash: h("a"), Status: "downloaded", State: provider.StateDownloaded})
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "A")))
	if err != nil || !res.Reused || res.TorrentID != "rd-held" || e.writes() != 0 {
		t.Fatalf("yaml reuse: %+v %v (%s)", res, err, steps(res))
	}
}
