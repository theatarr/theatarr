package arrview

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
)

func isRefused(err error) bool {
	var r *Refused
	return errors.As(err, &r)
}

// TestGrabReleaseByHand: the web UI's grab decides the release again and routes it through the router; a rejected
// release needs override; a private-labelled release is routed like any other (privacy is a label), and a release
// without a hash is refused.
func TestGrabReleaseByHand(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	ctx := context.Background()
	good := indexer.Release{Indexer: "prowlarr-public", Kind: "torznab", Privacy: policy.PrivacyPublic,
		Title: "Alpha.2020.2160p.WEB-DL.H265-GRP", InfoHash: strings.Repeat("9", 40), Size: 10 << 30}
	g, err := e.svc.GrabRelease(ctx, 1, "movie-web", good, false, "ui:grab:test")
	if err != nil || !g.Decision.Accepted || g.Overridden || g.Job == 0 {
		t.Fatalf("grab: %+v %v", g, err)
	}
	e.drain()
	if !slices.Contains(e.adds(), good.InfoHash) {
		t.Fatalf("adds %v", e.adds())
	}
	if src := e.str(`SELECT source FROM download WHERE info_hash = ?`, good.InfoHash); src != "ui:grab:test" {
		t.Errorf("download source %q", src)
	}

	bad := good
	bad.Title, bad.InfoHash = "Alpha.2020.1080p.WEB-DL-GRP", strings.Repeat("8", 40)
	g, err = e.svc.GrabRelease(ctx, 1, "movie-web", bad, false, "ui:grab:test")
	if !isRefused(err) || g == nil || g.Decision.Accepted || !strings.Contains(err.Error(), "not wanted") {
		t.Fatalf("rejected without override: %+v %v", g, err)
	}
	if n := e.str(`SELECT COUNT(*) FROM job WHERE kind = 'router.route' AND state = 'queued'`); n != "0" {
		t.Errorf("a refused grab queued %s routes", n)
	}
	g, err = e.svc.GrabRelease(ctx, 1, "movie-web", bad, true, "ui:grab:test")
	if err != nil || !g.Overridden {
		t.Fatalf("override: %+v %v", g, err)
	}
	e.drain()
	if !slices.Contains(e.adds(), bad.InfoHash) {
		t.Errorf("overridden grab not added: %v", e.adds())
	}

	private := good
	private.Title, private.InfoHash = "Alpha.2020.2160p.WEB-DL-PRIV", strings.Repeat("6", 40)
	private.Privacy = policy.PrivacyPrivate
	if _, err := e.svc.GrabRelease(ctx, 1, "movie-web", private, false, "ui:grab:test"); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if !slices.Contains(e.adds(), private.InfoHash) {
		t.Fatal("a private-labelled release of a listed indexer was not added")
	}

	nohash := good
	nohash.InfoHash = ""
	if _, err := e.svc.GrabRelease(ctx, 1, "movie-web", nohash, true, "ui:grab:test"); !isRefused(err) {
		t.Errorf("no hash: %v", err)
	}
	e.pol.Router.Enabled = false
	if _, err := e.svc.GrabRelease(ctx, 1, "movie-web", good, true, "ui:grab:test"); !isRefused(err) ||
		!strings.Contains(err.Error(), "router.enabled is false") {
		t.Errorf("disabled grab: %v", err)
	}
	e.pol.Router.Enabled = true
	e.pol.Versions["movie-web"].Mode = policy.ModeShadow
	if _, err := e.svc.GrabRelease(ctx, 1, "movie-web", good, true, "ui:grab:test"); !isRefused(err) ||
		!strings.Contains(err.Error(), "shadow mode") {
		t.Errorf("shadow grab: %v", err)
	}
}

// TestAddReleaseByHand: a release added by its hash has no indexer (it carries "manual") and takes the grab's route: decided now, refused
// when rejected unless override, added through the router with its download row written; GrabRelease still refuses
// a release without a configured indexer; a failed manual add can be retried.
func TestAddReleaseByHand(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	ctx := context.Background()
	good := indexer.Release{Title: "Alpha.2020.2160p.WEB-DL.H265-GRP", InfoHash: strings.Repeat("9", 40), Seeders: -1,
		Peers: -1}
	if _, err := e.svc.GrabRelease(ctx, 1, "movie-web", good, true, "ui:grab:test"); !isRefused(err) ||
		!strings.Contains(err.Error(), "no longer enabled") {
		t.Fatalf("grab without an indexer: %v", err)
	}
	// An indexer the caller names is not kept: a manual add never claims one.
	named := good
	named.Indexer = "prowlarr-public"
	g, err := e.svc.AddRelease(ctx, 1, "movie-web", named, false, "ui:grab:test")
	if err != nil || !g.Decision.Accepted || g.Overridden || g.Job == 0 {
		t.Fatalf("add: %+v %v", g, err)
	}
	e.drain()
	if !slices.Contains(e.adds(), good.InfoHash) {
		t.Fatalf("adds %v", e.adds())
	}
	if got := e.str(`SELECT source || '|' || indexer || '|' || release_title FROM download WHERE info_hash = ?`,
		good.InfoHash); got != "ui:grab:test|manual|"+good.Title {
		t.Errorf("download row %q", got)
	}
	bad := good
	bad.Title, bad.InfoHash = "Alpha.2020.1080p.WEB-DL-GRP", strings.Repeat("8", 40)
	g, err = e.svc.AddRelease(ctx, 1, "movie-web", bad, false, "ui:grab:test")
	if !isRefused(err) || g == nil || g.Decision.Accepted || !strings.Contains(err.Error(), "not wanted") {
		t.Fatalf("rejected without override: %+v %v", g, err)
	}
	e.drain()
	if slices.Contains(e.adds(), bad.InfoHash) {
		t.Fatal("a rejected add reached the provider")
	}
	if g, err = e.svc.AddRelease(ctx, 1, "movie-web", bad, true, "ui:grab:test"); err != nil || !g.Overridden {
		t.Fatalf("override: %+v %v", g, err)
	}
	e.drain()
	if !slices.Contains(e.adds(), bad.InfoHash) {
		t.Errorf("overridden add not added: %v", e.adds())
	}

	// A failed manual add is retried like any failed download: no indexer is not "an indexer the version dropped".
	now, failed := ms(testNow), strings.Repeat("2", 40)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		score, episodes, state, error, created_at, updated_at) VALUES (60, 1, 2, 'movie-web', ?,
		'Alpha.2020.2160p.WEB-DL-MANUAL', 'manual', 'WEBDL-2160p', 50, '[]', 'failed', 'stalled', ?, ?)`, failed, now, now)
	if r, err := e.svc.RetryDownload(ctx, 60, "ui:retry:60"); err != nil || r.What != "routed again" {
		t.Fatalf("retry of a manual add: %+v %v", r, err)
	}
	e.drain()
	if !slices.Contains(e.adds(), failed) {
		t.Errorf("retried manual add not added: %v", e.adds())
	}

	nohash := good
	nohash.InfoHash = ""
	if _, err := e.svc.AddRelease(ctx, 1, "movie-web", nohash, true, "ui:grab:test"); !isRefused(err) {
		t.Errorf("no hash: %v", err)
	}
	e.pol.Router.Enabled = false
	if _, err := e.svc.AddRelease(ctx, 1, "movie-web", good, true, "ui:grab:test"); !isRefused(err) ||
		!strings.Contains(err.Error(), "router.enabled is false") {
		t.Errorf("disabled add: %v", err)
	}
	e.pol.Router.Enabled = true
	e.pol.Versions["movie-web"].Mode = policy.ModeShadow
	if _, err := e.svc.AddRelease(ctx, 1, "movie-web", good, true, "ui:grab:test"); !isRefused(err) ||
		!strings.Contains(err.Error(), "shadow mode") {
		t.Errorf("shadow add: %v", err)
	}
}

// TestRetryDownload: a failed download's release is routed again (a new download row, never a link), refused while
// blocklisted; a finished download not yet linked is imported now; a download on its way is refused; a release of an
// indexer the version no longer lists is refused.
func TestRetryDownload(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	ctx := context.Background()
	now := ms(testNow)
	hash := strings.Repeat("7", 40)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		score, episodes, state, error, created_at, updated_at) VALUES
		(50, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.WEB-DL-RETRY', 'prowlarr-public', 'WEBDL-2160p', 50, '[]', 'failed',
		 'stalled', ?, ?),
		(51, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.WEB-DL-GONE', 'old-indexer', 'WEBDL-2160p', 50, '[]', 'failed', 'x', ?, ?),
		(52, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.WEB-DL-DL', 'prowlarr-public', 'WEBDL-2160p', 50, '[]', 'downloading',
		 NULL, ?, ?),
		(53, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.WEB-DL-READY', 'prowlarr-public', 'WEBDL-2160p', 50, '[]', 'ready',
		 NULL, ?, ?)`, hash, now, now, strings.Repeat("5", 40), now, now, strings.Repeat("4", 40), now, now,
		strings.Repeat("3", 40), now, now)

	e.exec(`INSERT INTO router_blocklist (version_key, info_hash, title_id, release_title, reason, source, created_at)
		VALUES ('movie-web', ?, 1, 'x', 'stalled at the provider', 'stalled', ?)`, hash, now)
	if _, err := e.svc.RetryDownload(ctx, 50, "ui:retry:50"); !isRefused(err) || !strings.Contains(err.Error(), "unblock") {
		t.Fatalf("blocklisted retry: %v", err)
	}
	e.exec(`DELETE FROM router_blocklist`)
	r, err := e.svc.RetryDownload(ctx, 50, "ui:retry:50")
	if err != nil || r.What != "routed again" || r.Version != "movie-web" || r.Job == 0 {
		t.Fatalf("retry: %+v %v", r, err)
	}
	e.drain()
	if !slices.Contains(e.adds(), hash) || e.str(`SELECT COUNT(*) FROM download WHERE info_hash = ?`, hash) != "2" {
		t.Errorf("retry did not open a new download: adds %v", e.adds())
	}

	if _, err := e.svc.RetryDownload(ctx, 51, "ui:retry:51"); !isRefused(err) || !strings.Contains(err.Error(), "old-indexer") {
		t.Fatalf("unlisted indexer: %v", err)
	}
	e.drain()
	if slices.Contains(e.adds(), strings.Repeat("5", 40)) {
		t.Error("a release from an unknown indexer reached debrid")
	}

	if _, err := e.svc.RetryDownload(ctx, 52, "ui:retry:52"); !isRefused(err) {
		t.Errorf("in hand: %v", err)
	}
	r, err = e.svc.RetryDownload(ctx, 53, "ui:retry:53")
	if err != nil || r.What != "imported" || len(e.lib.imports) != 1 || e.lib.imports[0] != "53" {
		t.Errorf("ready: %+v %v %v", r, err, e.lib.imports)
	}
	if _, err := e.svc.RetryDownload(ctx, 999, "ui:retry:999"); err == nil {
		t.Error("unknown download")
	}
	e.pol.Router.Enabled = false
	if _, err := e.svc.RetryDownload(ctx, 50, "ui:retry:50"); !isRefused(err) ||
		!strings.Contains(err.Error(), "router.enabled is false") {
		t.Errorf("disabled retry: %v", err)
	}
	e.pol.Router.Enabled = true
	e.pol.Versions["movie-web"].Mode = policy.ModeShadow
	if _, err := e.svc.RetryDownload(ctx, 50, "ui:retry:50"); !isRefused(err) ||
		!strings.Contains(err.Error(), "shadow mode") {
		t.Errorf("shadow retry: %v", err)
	}
}

// TestUnlinkLink: one of our symlinks leaves the library through the gate (nothing changes while it refuses), the
// version is wanted again with no other file; a hardlink or copy is never touched.
func TestUnlinkLink(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	e.seedFiles()
	ctx := context.Background()
	if _, err := e.svc.UnlinkLink(ctx, 7, "test"); !isRefused(err) {
		t.Fatalf("gated: %v", err)
	}
	if len(e.lib.unlinks) != 0 || e.str(`SELECT state FROM link WHERE id = 7`) != "active" {
		t.Fatal("a refused unlink changed something")
	}
	e.pol.Library = &policy.Library{Enabled: true, Versions: map[string]*policy.LibraryVersion{
		"movie-web": {Roots: []string{"/media/movies-web"}}}}
	e.lib.gate = library.NewGate(e.pol)
	u, err := e.svc.UnlinkLink(ctx, 7, "test")
	if err != nil || u.TitleID != 1 || !slices.Equal(u.Versions, []string{"movie-web"}) {
		t.Fatalf("unlink: %+v %v", u, err)
	}
	if len(e.lib.unlinks) != 1 || e.str(`SELECT state FROM link WHERE id = 7`) != "unlinked" ||
		e.str(`SELECT state FROM version WHERE id = 2`) != "wanted" {
		t.Errorf("after unlink: %v link %s version %s", e.lib.unlinks, e.str(`SELECT state FROM link WHERE id = 7`),
			e.str(`SELECT state FROM version WHERE id = 2`))
	}
	if _, err := e.svc.UnlinkLink(ctx, 7, "test"); !isRefused(err) {
		t.Errorf("second unlink: %v", err)
	}
	e.exec(`INSERT INTO link (id, path, target, kind, title_id, state, created_at, updated_at) VALUES
		(9, '/media/movies-web/Alpha (2020)/hard.mkv', '/data/hard.mkv', 'hardlink', 1, 'active', 0, 0)`)
	e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (9, 2)`)
	if _, err := e.svc.UnlinkLink(ctx, 9, "test"); !isRefused(err) || len(e.lib.unlinks) != 1 {
		t.Errorf("hardlink: %v %v", err, e.lib.unlinks)
	}
	if _, err := e.svc.UnlinkLink(ctx, 99, "test"); err == nil || isRefused(err) {
		t.Errorf("unknown link: %v", err)
	}
}
