package router

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/qbit"
	"github.com/theatarr/theatarr/internal/qbit/qbittest"
	"github.com/theatarr/theatarr/internal/store"
)

const (
	pathAdd  = "/api/v2/torrents/add"
	pathInfo = "/api/v2/torrents/info"
)

// clientEnv is newEnv with the movie version sent to a decypharr client "dc" (a fake qBittorrent API) instead of
// the providers.
func clientEnv(t *testing.T, enabled bool, mode policy.Mode) (*env, *qbittest.Fake) {
	t.Helper()
	e := newEnv(t, enabled, mode)
	f := qbittest.New()
	t.Cleanup(f.Close)
	f.User, f.Pass = "http://theatarr.test/radarr", "view-key"
	e.pol.Clients = map[string]*policy.Client{"dc": {Kind: "decypharr", URL: f.URL(), Category: "theatarr",
		UsernameEnv: "DC_USER", PasswordEnv: "DC_PASS"}}
	v := e.pol.Versions["movie"]
	v.AddVia, v.Client, v.Providers = policy.AddViaClient, "dc", nil
	env := map[string]string{"DC_USER": f.User, "DC_PASS": f.Pass}
	e.r.clients = BuildClients(e.pol, qbittest.Doer{}, func(k string) string { return env[k] }, e.r.log)
	return e, f
}

// held makes the fake list hash under our category, as decypharr does once it has the torrent.
func held(state string, progress float64, contentPath string) map[string]any {
	return map[string]any{"name": "Movie.2024.2160p.WEB.H265-GRP", "state": state, "progress": progress,
		"category": "theatarr", "content_path": contentPath}
}

func (e *env) dl(hash string) (id int64, state, client string, provider sql.NullString) {
	e.t.Helper()
	err := e.st.Reader().QueryRowContext(e.ctx, `SELECT id, state, COALESCE(client, ''), provider FROM download
		WHERE info_hash = ? ORDER BY id DESC LIMIT 1`, hash).Scan(&id, &state, &client, &provider)
	if err != nil {
		e.t.Fatal(err)
	}
	return id, state, client, provider
}

func TestClientRouteAddsToTheClientOnly(t *testing.T) {
	t.Parallel()
	e, f := clientEnv(t, true, policy.ModeActive)
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || res.Client != "dc" || res.Provider != "" || res.Reused {
		t.Fatalf("result %+v: %s", res, steps(res))
	}
	if e.writes() != 0 {
		t.Fatalf("%d provider writes", e.writes())
	}
	if len(f.Adds) != 1 || f.Adds[0] != "magnet:?xt=urn:btih:"+h("a") {
		t.Fatalf("client adds %v", f.Adds)
	}
	_, state, client, pk := e.dl(h("a"))
	if state != StateAdded || client != "dc" || pk.Valid {
		t.Fatalf("row %s client %q provider %v", state, client, pk)
	}
	if n := e.count(`SELECT COUNT(*) FROM download_attempt WHERE provider = 'client:dc' AND code = ?`,
		codeClientAdded); n != 1 {
		t.Errorf("%d client attempts recorded", n)
	}
	// A dry run names the client and sends nothing.
	n := len(f.AllCalls())
	res, err = e.r.Plan(e.ctx, grabReq(cand(h("b"), "Movie.2024.1080p.WEB.H264-GRP")))
	if err != nil || res.Outcome != OutcomeWouldAdd || res.Client != "dc" {
		t.Fatalf("dry run %+v, %v", res, err)
	}
	if len(f.AllCalls()) != n {
		t.Errorf("a dry run called the client: %v", f.AllCalls()[n:])
	}
}

func TestClientRouteClosedGatesAddNothing(t *testing.T) {
	t.Parallel()
	for name, mk := range map[string]func(*testing.T) (*env, *qbittest.Fake){
		"router off": func(t *testing.T) (*env, *qbittest.Fake) { return clientEnv(t, false, policy.ModeActive) },
		"shadow":     func(t *testing.T) (*env, *qbittest.Fake) { return clientEnv(t, true, policy.ModeShadow) },
		"grab off": func(t *testing.T) (*env, *qbittest.Fake) {
			e, f := clientEnv(t, true, policy.ModeActive)
			e.pol.Versions["movie"].Grab = false
			return e, f
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e, f := mk(t)
			if _, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"))); err != nil {
				t.Fatal(err)
			}
			if f.CallCount(pathAdd) != 0 || e.writes() != 0 {
				t.Fatalf("closed gate: client calls %v, provider writes %d", f.AllCalls(), e.writes())
			}
		})
	}
}

func TestClientRouteSkipsIndexerAllowlistReleases(t *testing.T) {
	t.Parallel()
	e, f := clientEnv(t, true, policy.ModeActive)
	e.pol.Indexers["priv"].Providers = []string{"rd"}
	c := cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")
	c.Release.Indexer, c.Release.Privacy = "priv", policy.PrivacyPrivate
	res, err := e.r.Route(e.ctx, grabReq(c, cand(h("b"), "Movie.2024.1080p.WEB.H264-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if !hasStep(res, codeClientIndexer) || res.Hash != h("b") {
		t.Fatalf("result %+v: %s", res, steps(res))
	}
	if len(f.Adds) != 1 || !strings.HasSuffix(f.Adds[0], h("b")) {
		t.Fatalf("client adds %v", f.Adds)
	}
}

func TestClientRouteNotCachedMovesOn(t *testing.T) {
	t.Parallel()
	e, f := clientEnv(t, true, policy.ModeActive)
	f.AddErr[h("a")] = []qbittest.AddError{{Status: 404, Code: "torrent_not_cached", Retryable: true}}
	res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"),
		cand(h("b"), "Movie.2024.1080p.WEB.H264-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || res.Hash != h("b") || !hasStep(res, codeClientMiss) {
		t.Fatalf("result %+v: %s", res, steps(res))
	}
	if _, state, _, _ := e.dl(h("a")); state != StateFailed {
		t.Errorf("the missed download is %s", state)
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist WHERE info_hash = ?`, h("a")); n != 0 {
		t.Errorf("a cache miss was blocklisted")
	}
	// Within miss_ttl it is not sent again (and opens no download).
	e.clock = e.clock.Add(time.Minute)
	adds := len(f.Adds)
	res, err = e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || !hasStep(res, "known_miss") || len(f.Adds) != adds {
		t.Fatalf("known miss: %+v %v; adds %v", res, err, f.Adds)
	}
}

func TestClientRouteRefusedIsBlocklisted(t *testing.T) {
	t.Parallel()
	e, f := clientEnv(t, true, policy.ModeActive)
	f.AddErr[h("a")] = []qbittest.AddError{{Status: 451, Code: "torrent_blocked", Permanent: true}}
	res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"),
		cand(h("b"), "Movie.2024.1080p.WEB.H264-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Hash != h("b") || !hasStep(res, codeClientRefused) {
		t.Fatalf("result %+v: %s", res, steps(res))
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist WHERE info_hash = ? AND source = ?`, h("a"),
		BlocklistClient); n != 1 {
		t.Errorf("%d blocklist rows for the refused release", n)
	}
}

func TestClientRouteDeferredStaysQueued(t *testing.T) {
	t.Parallel()
	e, f := clientEnv(t, true, policy.ModeActive)
	withQueue(e)
	f.AddErr[h("a")] = []qbittest.AddError{{Status: 509, Code: "too_many_active_downloads", Retryable: true}}
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeDeferred || res.RetryAt.IsZero() {
		t.Fatalf("result %+v: %s", res, steps(res))
	}
	id, state, client, _ := e.dl(h("a"))
	if state != StateQueued || client != "" {
		t.Fatalf("row %s client %q", state, client)
	}
	if len(routeJobs(e, id)) != 1 {
		t.Errorf("no continuation queued")
	}
}

func TestClientRouteAdoptsAHeldHash(t *testing.T) {
	t.Parallel()
	e, f := clientEnv(t, true, policy.ModeActive)
	f.Set(h("a"), held("downloading", 0.5, ""))
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || !res.Reused || !hasStep(res, codeClientAdopted) {
		t.Fatalf("result %+v: %s", res, steps(res))
	}
	if f.CallCount(pathAdd) != 0 {
		t.Errorf("added a hash the client holds")
	}
	if _, state, client, _ := e.dl(h("a")); state != StateAdded || client != "dc" {
		t.Errorf("row %s %q", state, client)
	}
}

func TestClientPollMapsStates(t *testing.T) {
	t.Parallel()
	e, f := clientEnv(t, true, policy.ModeActive)
	withQueue(e)
	if _, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"))); err != nil {
		t.Fatal(err)
	}
	id, _, _, _ := e.dl(h("a"))
	var woken atomic.Int32
	var seen atomic.Value
	e.r.SetReadyNotify(func() {
		woken.Add(1)
		var state string
		_ = e.st.Reader().QueryRow(`SELECT state FROM download WHERE id = ?`, id).Scan(&state)
		seen.Store(state)
	})
	f.Set(h("a"), held("downloading", 0.4, ""))
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	var state, name string
	var progress float64
	var source sql.NullString
	read := func() {
		t.Helper()
		if err := e.st.Reader().QueryRow(`SELECT state, name, progress, source_path FROM download WHERE id = ?`, id).
			Scan(&state, &name, &progress, &source); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if state != StateDownloading || progress != 40 || name != "Movie.2024.2160p.WEB.H265-GRP" {
		t.Fatalf("downloading: %s %v %q", state, progress, name)
	}
	// Ready, mount mode: the name is what the library matches; no source path.
	f.Set(h("a"), held("pausedUP", 1, "/data/symlinks/theatarr/Movie.2024.2160p.WEB.H265-GRP"))
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	read()
	if state != StateReady || progress != 100 || source.Valid {
		t.Fatalf("ready: %s %v %v", state, progress, source)
	}
	// The ready write calls the notifier once, after it committed; downloading did not.
	if n := woken.Load(); n != 1 || seen.Load() != StateReady {
		t.Errorf("ready notified %d times, the notifier read %v", n, seen.Load())
	}
	if f.CallCount(pathInfo) != 3 { // the pre-add check and one read per poll
		t.Errorf("info reads %d", f.CallCount(pathInfo))
	}
}

func TestClientPollDownloadFolderSourcePath(t *testing.T) {
	t.Parallel()
	e, f := clientEnv(t, true, policy.ModeActive)
	c := e.pol.Clients["dc"]
	c.ImportFrom, c.RemotePath, c.LocalPath = policy.ImportFromDownloadFolder, "/data/symlinks", "/mnt/decypharr"
	if _, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"))); err != nil {
		t.Fatal(err)
	}
	id, _, _, _ := e.dl(h("a"))
	// Complete but no content path reported yet: it waits.
	f.Set(h("a"), held("pausedUP", 1, ""))
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if _, state, _, _ := e.dl(h("a")); state != StateDownloading {
		t.Fatalf("no content path yet: %s", state)
	}
	f.Set(h("a"), held("pausedUP", 1, "/data/symlinks/theatarr/Movie.2024.2160p.WEB.H265-GRP"))
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	var source string
	if err := e.st.Reader().QueryRow(`SELECT COALESCE(source_path, '') FROM download WHERE id = ? AND state = 'ready'`,
		id).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if source != "/mnt/decypharr/theatarr/Movie.2024.2160p.WEB.H265-GRP" {
		t.Errorf("source path %q", source)
	}
}

func TestClientPollFailedAndMissing(t *testing.T) {
	t.Parallel()
	e, f := clientEnv(t, true, policy.ModeActive)
	withQueue(e)
	for _, c := range []Candidate{cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"),
		cand(h("b"), "Movie.2024.1080p.WEB.H264-GRP")} {
		if _, err := e.r.Route(e.ctx, grabReq(c)); err != nil {
			t.Fatal(err)
		}
	}
	f.Set(h("a"), held("error", 0.1, ""))
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if _, state, _, _ := e.dl(h("a")); state != StateFailed {
		t.Fatalf("errored torrent: %s", state)
	}
	// h("b") is not listed: missing, failed only after the grace.
	if _, state, _, _ := e.dl(h("b")); state != StateAdded {
		t.Fatalf("missing once: %s", state)
	}
	e.clock = e.clock.Add(missingGrace + time.Minute)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if _, state, _, _ := e.dl(h("b")); state != StateFailed {
		t.Fatalf("missing past the grace: %s", state)
	}
	for _, hash := range []string{h("a"), h("b")} {
		if n := e.count(`SELECT COUNT(*) FROM router_blocklist WHERE info_hash = ? AND source = ?`, hash,
			BlocklistProvider); n != 1 {
			t.Errorf("%s: %d blocklist rows", hash[:4], n)
		}
	}
	if len(researchJobs(e)) == 0 {
		t.Error("no re-search after the failures")
	}
	if f.CallCount("/api/v2/torrents/delete") != 0 {
		t.Error("deleted from the client")
	}
}

func TestClientPollOutageFailsNothing(t *testing.T) {
	t.Parallel()
	e, f := clientEnv(t, true, policy.ModeActive)
	var logs bytes.Buffer
	e.r.log = slog.New(slog.NewTextHandler(&logs, nil))
	if _, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"))); err != nil {
		t.Fatal(err)
	}
	f.Mu.Lock()
	f.Down = true
	f.Mu.Unlock()
	for i := 0; i < 5; i++ {
		e.clock = e.clock.Add(time.Hour)
		if _, err := e.r.Poll(e.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, state, _, _ := e.dl(h("a")); state != StateAdded {
		t.Fatalf("outage: %s", state)
	}
	if n := strings.Count(logs.String(), "download client does not answer"); n != 1 {
		t.Errorf("%d outage errors logged:\n%s", n, logs.String())
	}
	f.Mu.Lock()
	f.Down = false
	f.Mu.Unlock()
	f.Set(h("a"), held("downloading", 0.5, ""))
	e.clock = e.clock.Add(time.Hour)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if _, state, _, _ := e.dl(h("a")); state != StateDownloading {
		t.Fatalf("after the outage: %s", state)
	}
	if !strings.Contains(logs.String(), "download client answers again") {
		t.Error("recovery not logged")
	}
}

func TestClientDismissMakesNoClientCall(t *testing.T) {
	t.Parallel()
	e, f := clientEnv(t, true, policy.ModeActive)
	if _, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"))); err != nil {
		t.Fatal(err)
	}
	id, _, _, _ := e.dl(h("a"))
	n := len(f.AllCalls())
	if _, err := Dismiss(e.ctx, e.st, id, "removed in Activity", true, "", store.Millis(e.clock)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if calls := f.AllCalls()[n:]; len(calls) != 0 {
		t.Errorf("ending a client download called the client: %v", calls)
	}
}

func TestClientAddingSettledFromTheClient(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		listed bool
		want   string
	}{"landed": {true, StateAdded}, "never landed": {false, StateQueued}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e, f := clientEnv(t, true, policy.ModeActive)
			e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
				client, add_attempt, state, created_at, updated_at) VALUES (7, 1, 10, 'movie', ?, 'Movie', 'pub', 'dc',
				'a1', 'adding', 0, ?)`, h("a"), store.Millis(e.clock))
			if tc.listed {
				f.Set(h("a"), held("downloading", 0.1, ""))
			}
			e.clock = e.clock.Add(reconcileAfter + time.Minute)
			sum, err := e.r.Poll(e.ctx)
			if err != nil {
				t.Fatal(err)
			}
			_, state, client, pk := e.dl(h("a"))
			if state != tc.want || sum.Reconciled != 1 || pk.Valid {
				t.Fatalf("row %s (client %q, provider %v), summary %+v", state, client, pk, sum)
			}
			if f.CallCount(pathAdd) != 0 || e.writes() != 0 {
				t.Errorf("settling wrote: client %v, providers %d", f.AllCalls(), e.writes())
			}
		})
	}
}

func TestClientAddingSettledByTheRoute(t *testing.T) {
	t.Parallel()
	e, f := clientEnv(t, true, policy.ModeActive)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
		client, add_attempt, state, created_at, updated_at) VALUES (7, 1, 10, 'movie', ?, 'Movie', 'pub', 'dc',
		'a1', 'adding', 0, ?)`, h("a"), store.Millis(e.clock))
	f.Set(h("a"), held("downloading", 0.1, ""))
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || res.Client != "dc" || res.DownloadID != 7 || !hasStep(res, codeReconciled) {
		t.Fatalf("result %+v: %s", res, steps(res))
	}
	if f.CallCount(pathAdd) != 0 {
		t.Error("added again")
	}
	if _, state, _, _ := e.dl(h("a")); state != StateAdded {
		t.Errorf("row %s", state)
	}
}

func TestBuildClientsKeepsDebridClientsOnly(t *testing.T) {
	t.Parallel()
	pol := &policy.Policy{Clients: map[string]*policy.Client{
		"dc":   {Kind: "decypharr", URL: "http://dc.test:8282"},
		"seed": {Kind: "qbittorrent", URL: "http://seed.test"},
		"bad":  {Kind: "decypharr", URL: "::"},
	}}
	got := BuildClients(pol, qbittest.Doer{}, func(string) string { return "" },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if len(got) != 1 || got["dc"] == nil {
		t.Fatalf("clients %v", got)
	}
	var _ *qbit.Client = got["dc"]
}

// decypharr 2.7.1 reads each hashes value as one hash (pkg/server/qbit/context.go hashesContext): two open
// downloads are read one hash at a time, or neither is found.
func TestClientPollReadsEachHash(t *testing.T) {
	t.Parallel()
	e, f := clientEnv(t, true, policy.ModeActive)
	for _, x := range []string{"a", "b"} {
		if _, err := e.r.Route(e.ctx, grabReq(cand(h(x), "Movie.2024.2160p.WEB.H265-GRP"))); err != nil {
			t.Fatal(err)
		}
		f.Set(h(x), held("pausedUP", 1, ""))
	}
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	for _, x := range []string{"a", "b"} {
		if _, state, _, _ := e.dl(h(x)); state != StateReady {
			t.Errorf("%s: %s", x, state)
		}
	}
}

// A hash the client holds under another category (an arr's, before the handover) is adopted as it is and polled.
func TestClientRouteAdoptsAHashUnderAnotherCategory(t *testing.T) {
	t.Parallel()
	e, f := clientEnv(t, true, policy.ModeActive)
	other := held("downloading", 0.5, "")
	other["category"] = "radarr"
	f.Set(h("a"), other)
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || !hasStep(res, codeClientAdopted) || f.CallCount(pathAdd) != 0 {
		t.Fatalf("result %+v: %s; adds %d", res, steps(res), f.CallCount(pathAdd))
	}
	other = held("pausedUP", 1, "")
	other["category"] = "radarr"
	f.Set(h("a"), other)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if _, state, _, _ := e.dl(h("a")); state != StateReady {
		t.Errorf("adopted download %s", state)
	}
}

// Ready only on a completed state: progress 1 while decypharr still says downloading (its links not made yet) is
// downloading, and its interim content path is not kept.
func TestClientPollReadyOnlyWhenCompleted(t *testing.T) {
	t.Parallel()
	e, f := clientEnv(t, true, policy.ModeActive)
	c := e.pol.Clients["dc"]
	c.ImportFrom = policy.ImportFromDownloadFolder
	if _, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"))); err != nil {
		t.Fatal(err)
	}
	f.Set(h("a"), held("downloading", 1, "/data/symlinks/theatarr"))
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE info_hash = ? AND state = 'downloading'
		AND source_path IS NULL`, h("a")); n != 1 {
		_, state, _, _ := e.dl(h("a"))
		t.Fatalf("progress 1 while downloading: %s", state)
	}
}

func TestClientRouteAdoptedRowIsReused(t *testing.T) {
	t.Parallel()
	e, f := clientEnv(t, true, policy.ModeActive)
	f.Set(h("a"), held("downloading", 0.5, ""))
	if _, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"))); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE info_hash = ? AND reused = 1`, h("a")); n != 1 {
		t.Error("an adopted client row is not marked reused")
	}
}

// A release the client does not have cached is a miss everywhere: the exhausted route searches again. A known
// client miss within miss_ttl (a skip, no call) schedules no further search.
func TestClientNotCachedEverywhereSearchesAgain(t *testing.T) {
	t.Parallel()
	e, f := clientEnv(t, true, policy.ModeActive)
	withQueue(e)
	f.AddErr[h("a")] = []qbittest.AddError{{Status: 404, Code: "torrent_not_cached", Retryable: true}}
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeExhausted {
		t.Fatalf("result %+v: %s", res, steps(res))
	}
	got := researchJobs(e)
	if len(got) != 1 || got[0].Reason != ReasonExhausted {
		t.Fatalf("research jobs %+v", got)
	}
	if !missedEverywhere(res.Steps) {
		t.Error("a client miss is not a miss everywhere")
	}
	e.clock = e.clock.Add(time.Minute)
	res, err = e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || !hasStep(res, "known_miss") {
		t.Fatalf("known miss: %+v %v", res, err)
	}
	if missedEverywhere(res.Steps) {
		t.Error("a known client miss counts as a fresh miss")
	}
}

// A read that fails mid-pass keeps the reads before it: those downloads advance, and the one not read is left for
// the next pass, not counted missing.
func TestClientPollKeepsReadsBeforeAFailedOne(t *testing.T) {
	t.Parallel()
	e, f := clientEnv(t, true, policy.ModeActive)
	for _, x := range []string{"a", "b"} {
		if _, err := e.r.Route(e.ctx, grabReq(cand(h(x), "Movie.2024.2160p.WEB.H265-GRP"))); err != nil {
			t.Fatal(err)
		}
		f.Set(h(x), held("pausedUP", 1, ""))
	}
	f.Mu.Lock()
	f.InfoDown = map[string]bool{h("b"): true}
	f.Mu.Unlock()
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if _, state, _, _ := e.dl(h("a")); state != StateReady {
		t.Errorf("the download read before the failure: %s", state)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE info_hash = ? AND state = 'added' AND missing_polls = 0
		AND missing_since IS NULL`, h("b")); n != 1 {
		t.Errorf("the unread download was not left as it was")
	}
}

// slowDoer is a client read that takes a minute on the test clock.
type slowDoer struct{ e *env }

func (d slowDoer) Do(ctx context.Context, t gateway.Target, req *http.Request) (*http.Response, error) {
	if strings.HasSuffix(req.URL.Path, pathInfo) {
		d.e.clock = d.e.clock.Add(time.Minute)
	}
	return qbittest.Doer{}.Do(ctx, t, req)
}

func TestClientPollReadyAtIsTheWriteTime(t *testing.T) {
	t.Parallel()
	e, f := clientEnv(t, true, policy.ModeActive)
	withQueue(e)
	if _, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP"))); err != nil {
		t.Fatal(err)
	}
	id, _, _, _ := e.dl(h("a"))
	env := map[string]string{"DC_USER": f.User, "DC_PASS": f.Pass}
	e.r.clients = BuildClients(e.pol, slowDoer{e}, func(k string) string { return env[k] }, e.r.log)
	f.Set(h("a"), held("pausedUP", 1, "/data/symlinks/theatarr/Movie.2024.2160p.WEB.H265-GRP"))
	start := e.clock
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	var readyAt, polledAt int64
	if err := e.st.Reader().QueryRow(`SELECT ready_at, polled_at FROM download WHERE id = ?`, id).
		Scan(&readyAt, &polledAt); err != nil {
		t.Fatal(err)
	}
	// A slow read must not age the download: ready_at is when the ready write ran, not when the poll began.
	if want := store.Millis(e.clock); readyAt != want || polledAt != store.Millis(start) || e.clock == start {
		t.Fatalf("ready_at %d, want %d (poll began %d)", readyAt, want, store.Millis(start))
	}
}
