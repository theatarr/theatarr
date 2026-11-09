package router

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/store"
)

// outageToQueued drives download 1 (Movie on rd, torrent t1, also in the registry) through an rd outage past its
// stall limit: endUnavailable queues it again with a route job and backs rd off. It returns the queued job.
func outageToQueued(t *testing.T, e *env) *jobs.Job {
	t.Helper()
	withQueue(e)
	freshMovie(e)
	stall := e.r.stallAfter("rd")
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		provider_torrent_id, state, own_add, added_at, created_at, updated_at) VALUES (1, 1, 10, 'movie', ?,
		'Movie.2024.1080p.WEB.H265-GRP', 'pub', 'rd', 't1', 'added', 1, ?, 0, 0)`, h("a"), store.Millis(e.clock))
	// The registry still names t1 (the provider sync saw it before the outage).
	e.exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (7, ?, 'present', 0, 0)`,
		h("a"))
	e.exec(`INSERT INTO content_provider (content_id, provider, torrent_id, seen_at) VALUES (7, 'rd', 't1', 0)`)
	e.pr["rd"].set("t1", provider.Torrent{Hash: h("a"), State: provider.StateQueued, Status: "magnet_conversion"})
	e.pr["rd"].readErr = map[string]error{"t1": errors.New("rd: connection refused")}
	added := e.clock
	for e.clock.Before(added.Add(stall+10*time.Minute)) && e.count(`SELECT COUNT(*) FROM download
		WHERE id = 1 AND state = 'queued'`) == 0 {
		e.clock = e.clock.Add(time.Minute)
		if _, err := e.r.Poll(e.ctx); err != nil {
			t.Fatal(err)
		}
	}
	var state, why string
	_ = e.st.Reader().QueryRow(`SELECT state, COALESCE(error, '') FROM download WHERE id = 1`).Scan(&state, &why)
	if state != "queued" || !strings.HasPrefix(why, "provider unavailable") {
		t.Fatalf("after the outage: %s %q, want queued as provider unavailable", state, why)
	}
	var payload string
	if err := e.st.Reader().QueryRow(`SELECT payload FROM job WHERE kind = ? AND state = 'queued'`, JobRoute).
		Scan(&payload); err != nil {
		t.Fatal(err)
	}
	return &jobs.Job{Kind: JobRoute, Payload: []byte(payload)}
}

func dlRow(t *testing.T, e *env) (state, pk, tid string, reused bool) {
	t.Helper()
	if err := e.st.Reader().QueryRow(`SELECT state, COALESCE(provider, ''), COALESCE(provider_torrent_id, ''),
		reused FROM download WHERE id = 1`).Scan(&state, &pk, &tid, &reused); err != nil {
		t.Fatal(err)
	}
	return
}

// The route queued by endUnavailable used to reuse t1 on rd (the registry names it, and a read that
// failed trusted the id), restarting the stall clock on the provider that was down. Now rd is deferred while it backs
// off and the healthy fallback takes the release.
func TestOutageContinuationUsesHealthyFallback(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	j := outageToQueued(t, e)
	if err := e.r.handleRoute(e.ctx, j); err != nil {
		t.Fatal(err)
	}
	state, pk, _, reused := dlRow(t, e)
	if pk == "rd" || reused {
		t.Fatalf("the continuation went back to rd: %s on %s, reused %v", state, pk, reused)
	}
	if state != "added" || pk != "ad" || len(e.ad["ad"].adds) != 1 {
		t.Fatalf("download %s on %q (ad adds %d), want added on the healthy fallback ad", state, pk,
			len(e.ad["ad"].adds))
	}
	if len(e.ad["rd"].adds) != 0 {
		t.Fatalf("added to rd %d times during its back-off", len(e.ad["rd"].adds))
	}
	if n := e.count(`SELECT COUNT(*) FROM download_attempt WHERE download_id = 1 AND provider = 'rd'
		AND code = 'paced' AND message LIKE 'backoff: provider unavailable%'`); n != 1 {
		t.Fatalf("%d back-off deferrals of rd recorded, want 1", n)
	}
}

// Past rd's back-off, a known id rd still cannot read is not reused either: rd is deferred (held_unread) and, with
// no other provider, the download waits instead of pointing at an unverified torrent.
func TestUnreadableHeldIDNotReused(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Versions["movie"].Providers = []string{"rd"}
	j := outageToQueued(t, e)
	p, _ := loadPace(e.ctx, e.st.Reader(), "rd")
	e.clock = p.BackoffUntil.Add(time.Second)
	if err := e.r.handleRoute(e.ctx, j); err != nil {
		t.Fatal(err)
	}
	state, pk, tid, reused := dlRow(t, e)
	if state != "queued" || tid != "" || reused {
		t.Fatalf("download %s on %q torrent %q reused %v, want queued: t1 cannot be read", state, pk, tid, reused)
	}
	if n := e.count(`SELECT COUNT(*) FROM download_attempt WHERE download_id = 1 AND code = ?`,
		codeHeldUnread); n == 0 {
		t.Fatalf("no %s step recorded", codeHeldUnread)
	}
	if len(e.ad["rd"].adds) != 0 {
		t.Fatal("added the hash to rd again while its known entry could not be read")
	}
}

// Healthy reuse after the back-off: rd answers for t1 again, the route points at it, and no add-pacing slot is taken
// (the minute's adds are spent, which would defer an add).
func TestHealthyReuseTakesNoPacingSlot(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.pol.Versions["movie"].Providers = []string{"rd"}
	j := outageToQueued(t, e)
	p, _ := loadPace(e.ctx, e.st.Reader(), "rd")
	e.clock = p.BackoffUntil.Add(time.Second)
	// rd recovered; its add rate for this minute is spent.
	e.pr["rd"].readErr = nil
	e.exec(`UPDATE router_pacing SET window_start = ?, window_adds = 3 WHERE provider = 'rd'`, store.Millis(e.clock))
	before, _ := loadPace(e.ctx, e.st.Reader(), "rd")
	if err := e.r.handleRoute(e.ctx, j); err != nil {
		t.Fatal(err)
	}
	state, pk, tid, reused := dlRow(t, e)
	if state != "added" || pk != "rd" || tid != "t1" || !reused {
		t.Fatalf("download %s on %q torrent %q reused %v, want t1 reused on rd", state, pk, tid, reused)
	}
	after, _ := loadPace(e.ctx, e.st.Reader(), "rd")
	if after.WindowAdds != before.WindowAdds || !after.LastAdd.Equal(before.LastAdd) ||
		!after.CooldownUntil.Equal(before.CooldownUntil) {
		t.Fatalf("the reuse took a pacing slot: %+v -> %+v", before, after)
	}
}

// Reconciliation does not adopt a known id the provider cannot read: the account list decides, and when it cannot
// be read either the outcome stays unknown.
func TestReconcileDoesNotAdoptUnreadableID(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
		provider, created_at, updated_at) VALUES (1, 1, 10, 'movie', ?, 'R', 'pub', 'adding', 'rd', 0, ?)`, h("a"),
		e.clock.Add(-addSettle-time.Minute).UnixMilli())
	e.exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (7, ?, 'present', 0, 0)`,
		h("a"))
	e.exec(`INSERT INTO content_provider (content_id, provider, torrent_id, seen_at) VALUES (7, 'rd', 't1', 0)`)
	e.pr["rd"].readErr = map[string]error{"t1": errors.New("rd: connection refused")}
	e.pr["rd"].listErr = errors.New("rd: connection refused")
	c, err := e.r.lookupAdding(e.ctx, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.rec != recUnknown || c.tid != "" {
		t.Fatalf("lookup %+v, want unknown with no torrent adopted", c)
	}
}

// heldOnRD puts hash a on rd as torrent t1, readable, and in the registry.
func heldOnRD(e *env) {
	e.exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (7, ?, 'present', 0, 0)`,
		h("a"))
	e.exec(`INSERT INTO content_provider (content_id, provider, torrent_id, seen_at) VALUES (7, 'rd', 't1', 0)`)
	e.pr["rd"].set("t1", provider.Torrent{Hash: h("a"), State: provider.StateDownloading})
}

// Only a back-off that means the provider is down blocks reuse: one from refused or rate-limited adds leaves a
// verified torrent reusable (no add is needed; skipping it would add the hash a second time elsewhere), while the add
// itself still waits for the back-off.
func TestAddBackoffDoesNotBlockReuse(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		down   bool
		reused bool
	}{
		{"rate-limited adds", false, true},
		{"provider down", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			heldOnRD(e)
			e.exec(`INSERT INTO router_pacing (provider, backoff_until, failures, reason, backoff_down, updated_at)
				VALUES ('rd', ?, 1, 'rate_limited', ?, 0)`, store.Millis(e.clock.Add(30*time.Minute)), tc.down)
			res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.1080p.WEB.H265-GRP")))
			if err != nil {
				t.Fatal(err)
			}
			if got := res.Provider == "rd" && res.Reused; got != tc.reused {
				t.Fatalf("route %+v (%s): reused on rd %v, want %v", res, steps(res), got, tc.reused)
			}
			if len(e.ad["rd"].adds) != 0 {
				t.Fatal("added to rd during its back-off")
			}
			if tc.reused && len(e.ad["ad"].adds)+len(e.ad["tb"].adds) != 0 {
				t.Fatal("a second add elsewhere instead of the reuse")
			}
			if !tc.reused && (res.Provider != "ad" || len(e.ad["ad"].adds) != 1) {
				t.Fatalf("route %+v, want the hash added on ad while rd is down", res)
			}
		})
	}
}

// The back-off's kind follows the failure: a 5xx, a transport error or the provider's maintenance code mean it is
// down; a rate limit, an AllDebrid upload failure or a bare 4xx do not. A later answer clears it.
func TestBackoffKind(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		down bool
	}{
		{"503", &adder.Error{Provider: "rd", Code: adder.CodeUnavailable, Status: 503}, true},
		{"transport", &adder.Error{Provider: "rd", Code: adder.CodeUnavailable}, true},
		{"rd service_unavailable", &adder.Error{Provider: "rd", Code: adder.CodeUnavailable, Status: 200,
			ProviderCode: "25"}, true},
		{"not an adder error", errors.New("boom"), true},
		{"429", &adder.Error{Provider: "rd", Code: adder.CodeRateLimited, Status: 429}, false},
		{"too many active", &adder.Error{Provider: "rd", Code: adder.CodeTooManyActive, Status: 509}, false},
		{"ad upload failed", &adder.Error{Provider: "rd", Code: adder.CodeUnavailable, Status: 200,
			ProviderCode: "MAGNET_FILE_UPLOAD_FAILED"}, false},
		{"bare 400", &adder.Error{Provider: "rd", Code: adder.CodeUnknown, Status: 400}, false},
		{"auth", &adder.Error{Provider: "rd", Code: adder.CodeAuth, Status: 401}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			var p pace
			v, _, _ := e.r.classify(&p, e.pol.Providers["rd"], e.clock, h("a"), tc.err, false)
			if v != vUnavailable || !p.BackoffUntil.After(e.clock) {
				t.Fatalf("verdict %v, back-off until %v: want backed off", v, p.BackoffUntil)
			}
			if p.BackoffDown != tc.down {
				t.Fatalf("back-off down %v, want %v", p.BackoffDown, tc.down)
			}
			if err := savePace(e.ctx, e.st.Writer(), "rd", p, e.clock); err != nil {
				t.Fatal(err)
			}
			if got, _ := loadPace(e.ctx, e.st.Reader(), "rd"); got.BackoffDown != tc.down {
				t.Fatal("backoff_down not stored")
			}
			p.ok()
			if p.BackoffDown {
				t.Fatal("an answer did not clear the down back-off")
			}
		})
	}
	// The torrent manager's restore records the kind it is given.
	e := newEnv(t, true, policy.ModeActive)
	if err := RecordAddOutcome(e.ctx, e.st, "rd", policy.Pacing{}, e.clock, AddOutcome{Reason: "unavailable",
		Down: true}); err != nil {
		t.Fatal(err)
	}
	if p, _ := loadPace(e.ctx, e.st.Reader(), "rd"); !p.BackoffDown {
		t.Fatal("RecordAddOutcome dropped Down")
	}
}

// One failed read of a known id is retried before the provider is passed over: a single glitch reuses t1 instead of
// adding the hash on another provider.
func TestHeldReadRetriedOnce(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	heldOnRD(e)
	reads := 0
	e.pr["rd"].readErr = map[string]error{"t1": errors.New("rd: connection reset")}
	e.pr["rd"].read = func(_ context.Context, id string) {
		if reads++; reads == 2 {
			e.pr["rd"].readErr = nil
		}
	}
	res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.1080p.WEB.H265-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != "rd" || !res.Reused || reads != 2 {
		t.Fatalf("route %+v after %d reads (%s), want t1 reused on rd after one retry", res, reads, steps(res))
	}
	if e.writes() != 0 {
		t.Fatal("an add was sent")
	}
}
