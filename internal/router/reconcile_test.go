package router

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
)

// deletes counts deletes sent to any provider: reconciliation never sends one.
func (e *env) deletes() int {
	n := 0
	for _, a := range e.ad {
		a.mu.Lock()
		n += len(a.deletes)
		a.mu.Unlock()
	}
	return n
}

func (e *env) adds() int {
	n := 0
	for _, a := range e.ad {
		a.mu.Lock()
		n += len(a.adds)
		a.mu.Unlock()
	}
	return n
}

// landOnAccount makes rd's adds show in its account list, as Real-Debrid's own list would.
func (e *env) landOnAccount(a adder.Magnet) *adder.Added {
	id := "rd-" + a.Hash()[:4]
	p := e.pr["rd"]
	p.mu.Lock()
	p.list = append(p.list, provider.Torrent{ID: id, Hash: a.Hash(), Status: "downloaded"})
	p.mu.Unlock()
	return &adder.Added{ID: id, Hash: a.Hash()}
}

func (e *env) download(id int64) (state, prov, tid string, reused int, own any) {
	e.t.Helper()
	if err := e.st.Reader().QueryRow(`SELECT state, COALESCE(provider, ''), COALESCE(provider_torrent_id, ''),
		reused, own_add FROM download WHERE id = ?`, id).Scan(&state, &prov, &tid, &reused, &own); err != nil {
		e.t.Fatal(err)
	}
	return
}

// Crash immediately after the add: the provider took it, but the process died before the record (the add's answer
// never came back). The retried route finds the row in adding, reads the account, and adopts the torrent: one add
// in all, and nothing deleted.
func TestCrashAfterAddIsReconciledNotAddedAgain(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	ctx, cancel := context.WithCancel(e.ctx)
	e.ad["rd"].add = func(m adder.Magnet, _ adder.AddOptions) (*adder.Added, error) {
		e.landOnAccount(m)
		cancel() // the crash
		return nil, context.Canceled
	}
	req := movieReq(cand(h("a"), "A.2160p"))
	if _, err := e.r.Route(ctx, req); err == nil {
		t.Fatal("an interrupted add reported success")
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE state = 'adding' AND provider = 'rd'`); n != 1 {
		t.Fatalf("%d downloads in adding on rd, want the interrupted one", n)
	}
	e.ad["rd"].add = nil
	res, err := e.r.Route(e.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || res.TorrentID != "rd-aaaa" || !strings.Contains(steps(res), "rd/accepted/reconciled") {
		t.Fatalf("%+v: %s", res, steps(res))
	}
	if n := e.adds(); n != 1 {
		t.Errorf("%d adds, want 1", n)
	}
	state, prov, tid, reused, own := e.download(res.DownloadID)
	if state != StateAdded || prov != "rd" || tid != "rd-aaaa" || reused != 0 || own != nil {
		t.Errorf("download %s %s %s reused %d own_add %v", state, prov, tid, reused, own)
	}
	if e.deletes() != 0 || e.count(`SELECT COUNT(*) FROM download`) != 1 {
		t.Errorf("deletes %d, downloads %d", e.deletes(), e.count(`SELECT COUNT(*) FROM download`))
	}
}

// The add's answer came back and the context ended right after: the add is still recorded.
func TestCancelRightAfterAddStillRecords(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	ctx, cancel := context.WithCancel(e.ctx)
	e.ad["rd"].add = func(m adder.Magnet, _ adder.AddOptions) (*adder.Added, error) {
		a := e.landOnAccount(m)
		cancel()
		return a, nil
	}
	res, _ := e.r.Route(ctx, movieReq(cand(h("a"), "A.2160p")))
	if res == nil || res.DownloadID == 0 {
		t.Fatalf("%+v", res)
	}
	if state, prov, tid, _, _ := e.download(res.DownloadID); state != StateAdded || prov != "rd" || tid != "rd-aaaa" {
		t.Errorf("download %s %s %s, want added on rd", state, prov, tid)
	}
}

// Crash mid-add when the add never reached the provider: the retried route resumes the same download and adds it.
func TestCrashBeforeAddLandedResumes(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	ctx, cancel := context.WithCancel(e.ctx)
	e.ad["rd"].add = func(adder.Magnet, adder.AddOptions) (*adder.Added, error) {
		cancel()
		return nil, context.Canceled
	}
	req := movieReq(cand(h("a"), "A.2160p"))
	if _, err := e.r.Route(ctx, req); err == nil {
		t.Fatal("an interrupted add reported success")
	}
	e.ad["rd"].add = nil
	// Right after, the list without the hash proves nothing: the add may still land.
	res, err := e.r.Route(e.ctx, req)
	if err != nil || res.Outcome != OutcomeInFlight || e.adds() != 1 ||
		!strings.Contains(steps(res), "rd/skipped/add_unsettled") {
		t.Fatalf("%+v %v: %s; adds %d", res, err, steps(res), e.adds())
	}
	e.clock = e.clock.Add(addSettle)
	res, err = e.r.Route(e.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || res.Provider != "rd" || res.Reused {
		t.Fatalf("%+v: %s", res, steps(res))
	}
	if e.count(`SELECT COUNT(*) FROM download`) != 1 || e.adds() != 2 || e.deletes() != 0 {
		t.Errorf("downloads %d, adds %d (the lost one and the real one), deletes %d",
			e.count(`SELECT COUNT(*) FROM download`), e.adds(), e.deletes())
	}
	if n := e.count(`SELECT COUNT(*) FROM download_attempt WHERE code = 'reconciled'`); n != 1 {
		t.Errorf("%d reconciled attempts", n)
	}
}

// The account cannot be read: the row stays in adding (in flight) and nothing is added a second time.
func TestUnreadableAccountAddsNothing(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	ctx, cancel := context.WithCancel(e.ctx)
	e.ad["rd"].add = func(m adder.Magnet, _ adder.AddOptions) (*adder.Added, error) {
		cancel()
		return nil, context.Canceled
	}
	req := movieReq(cand(h("a"), "A.2160p"))
	_, _ = e.r.Route(ctx, req)
	e.ad["rd"].add = nil
	e.pr["rd"].listErr = errors.New("503")
	res, err := e.r.Route(e.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeInFlight || e.adds() != 1 {
		t.Errorf("%+v: %s; adds %d", res, steps(res), e.adds())
	}
}

// The poller settles an interrupted add from the account (found: added; not found: failed), never deleting.
func TestPollReconcilesInterruptedAdds(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	for i, hash := range []string{h("a"), h("b")} {
		e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
			provider, created_at, updated_at) VALUES (?, 1, 10, 'movie', ?, 'R', 'pub', 'adding', 'rd', 0, ?)`,
			i+1, hash, e.clock.Add(-addSettle-time.Minute).UnixMilli())
	}
	e.pr["rd"].list = []provider.Torrent{{ID: "T1", Hash: h("a")}}
	e.pr["rd"].set("T1", provider.Torrent{Hash: h("a"), Status: "downloaded", State: provider.StateDownloaded})
	sum, err := e.r.Poll(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Reconciled != 2 {
		t.Errorf("%+v", sum)
	}
	if state, _, tid, _, own := e.download(1); state != StateReady || tid != "T1" || own != nil {
		t.Errorf("download 1: %s %s own_add %v, want ready on T1", state, tid, own)
	}
	if state, _, _, _, _ := e.download(2); state != StateFailed {
		t.Errorf("download 2: %s, want failed (the add never reached rd)", state)
	}
	if e.writes() != 0 {
		t.Errorf("the poller wrote to a provider (%d)", e.writes())
	}
}

// A route job that lost its lease right before the add sends nothing; a second worker that takes over the expired
// lease resumes the same download and adds it once.
func TestLostLeaseBeforeAddThenTakeover(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	q := jobs.NewQueue(e.st)
	q.SetClock(func() time.Time { return e.clock })
	e.r.q = q
	id, err := e.r.Enqueue(e.ctx, movieReq(cand(h("a"), "A.2160p")), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	first := jobs.NewWorker(q, jobs.WorkerOptions{Owner: "first", Lease: time.Minute})
	var firstErr error
	first.Handle(JobRoute, func(ctx context.Context, j *jobs.Job) error {
		// Another worker takes the job while this one stalls before the add.
		e.exec(`UPDATE job SET lease_owner = 'other', attempts = attempts + 1 WHERE id = ?`, j.ID)
		firstErr = e.r.handleRoute(ctx, j)
		return firstErr
	})
	if ran, err := first.RunOne(e.ctx); !ran || err != nil {
		t.Fatal(ran, err)
	}
	if !errors.Is(firstErr, jobs.ErrLeaseLost) || e.writes() != 0 {
		t.Fatalf("first run: %v, writes %d; want ErrLeaseLost and nothing sent", firstErr, e.writes())
	}
	// The other worker died too; its lease expires and a third takes over.
	e.exec(`UPDATE job SET lease_until = 0 WHERE id = ?`, id)
	second := jobs.NewWorker(q, jobs.WorkerOptions{Owner: "second", Lease: time.Minute})
	second.Handle(JobRoute, e.r.handleRoute)
	if ran, err := second.RunOne(e.ctx); !ran || err != nil {
		t.Fatal(ran, err)
	}
	if e.adds() != 1 || e.count(`SELECT COUNT(*) FROM download`) != 1 ||
		e.count(`SELECT COUNT(*) FROM download WHERE state = 'added'`) != 1 {
		t.Errorf("adds %d, downloads %d", e.adds(), e.count(`SELECT COUNT(*) FROM download`))
	}
	info, _ := q.Get(e.ctx, id)
	if info.State != jobs.StateDone {
		t.Errorf("job %+v", info)
	}
}

// secondRouter is another process's router on the same database (its own r.mu): a route job taken over after a
// lost lease runs there while the first process's add may still be in flight.
func (e *env) secondRouter() *Router {
	e.t.Helper()
	adders := map[string]adder.Adder{}
	provs := map[string]provider.Provider{}
	for k := range e.ad {
		adders[k], provs[k] = e.ad[k], e.pr[k]
	}
	r, err := New(Config{Store: e.st, Policy: e.pol, Adders: adders, Providers: provs,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return e.clock }})
	if err != nil {
		e.t.Fatal(err)
	}
	r.sleep = func(context.Context, time.Duration) error { return nil }
	return r
}

// An answer that does not say whether the provider took the add (the connection reset after RD accepted it): the
// download stays in adding, no other provider is tried, and the next route settles it from the account. One add.
func TestUncertainAddIsSettledNotAddedAgain(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.ad["rd"].add = func(m adder.Magnet, _ adder.AddOptions) (*adder.Added, error) {
		e.landOnAccount(m)
		return nil, &adder.Error{Provider: "rd", Code: adder.CodeUnavailable, Message: "request not sent or failed",
			Err: errors.New("connection reset by peer")}
	}
	req := movieReq(cand(h("a"), "A.2160p"))
	res, err := e.r.Route(e.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeDeferred || !res.RetryAt.Equal(e.clock.Add(addSettle)) ||
		!strings.Contains(steps(res), "rd/error/add_unsettled") || e.adds() != 1 {
		t.Fatalf("%+v: %s; adds %d", res, steps(res), e.adds())
	}
	if state, prov, _, _, _ := e.download(res.DownloadID); state != StateAdding || prov != "rd" {
		t.Fatalf("download %s on %q, want adding on rd", state, prov)
	}
	e.ad["rd"].add = nil
	res, err = e.r.Route(e.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAdded || res.TorrentID != "rd-aaaa" || e.adds() != 1 {
		t.Errorf("%+v: %s; adds %d", res, steps(res), e.adds())
	}
	// A malformed success answer and a 5xx are as uncertain; a provider's refusal and a request the gateway never
	// sent are not.
	for _, c := range []struct {
		err       error
		uncertain bool
	}{
		{&adder.Error{Code: adder.CodeUnknown, Status: 200, Message: "undecodable answer"}, true},
		{&adder.Error{Code: adder.CodeUnavailable, Status: 503}, true},
		{errors.New("plain"), true},
		{&adder.Error{Code: adder.CodeUnavailable, Err: &gateway.Refused{Reason: gateway.ReasonTooLarge}}, true},
		{&adder.Error{Code: adder.CodeUnavailable, Err: &gateway.Refused{Reason: gateway.ReasonBackoff}}, false},
		{&adder.Error{Code: adder.CodeUnknown, Status: 400}, false},
		{&adder.Error{Code: adder.CodeTooManyActive, Status: 509}, false},
		{&adder.Error{Code: adder.CodeNotCached, Status: 200}, false},
		// An error envelope with a code we do not know (AllDebrid answers errors with HTTP 200): a refusal.
		{&adder.Error{Code: adder.CodeUnknown, Status: 200, ProviderCode: "MAGNET_NEW", Refused: true}, false},
		{&adder.Error{Code: adder.CodeUnavailable, Status: 503, Refused: true}, true},
	} {
		if got := addUncertain(c.err); got != c.uncertain {
			t.Errorf("%v: uncertain %v", c.err, got)
		}
	}
}

// An interrupted add whose account cannot be read stays unsettled however long that lasts: the poller never fails
// it into a second add, and a later route adds nothing.
func TestUnreadableAccountNeverTimesOutIntoASecondAdd(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	ctx, cancel := context.WithCancel(e.ctx)
	e.ad["rd"].add = func(m adder.Magnet, _ adder.AddOptions) (*adder.Added, error) {
		e.landOnAccount(m)
		cancel()
		return nil, context.Canceled
	}
	req := movieReq(cand(h("a"), "A.2160p"))
	_, _ = e.r.Route(ctx, req)
	e.ad["rd"].add = nil
	e.pr["rd"].listErr = errors.New("503")
	e.clock = e.clock.Add(11 * time.Minute)
	sum, err := e.r.Poll(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Failed != 0 || sum.Reconciled != 0 {
		t.Errorf("poll %+v", sum)
	}
	res, err := e.r.Route(e.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeInFlight || e.adds() != 1 {
		t.Fatalf("%+v: %s; adds %d", res, steps(res), e.adds())
	}
	var state, why string
	if err := e.st.Reader().QueryRow(`SELECT state, COALESCE(error, '') FROM download WHERE id = ?`,
		res.DownloadID).Scan(&state, &why); err != nil {
		t.Fatal(err)
	}
	if state != StateAdding || !strings.Contains(why, "add not settled") || !strings.Contains(why, "503") {
		t.Errorf("download %s %q, want adding with the reason", state, why)
	}
	// Once the list reads again, the add is found: still one add.
	e.pr["rd"].listErr = nil
	if sum, err = e.r.Poll(e.ctx); err != nil || sum.Reconciled != 1 || e.adds() != 1 {
		t.Errorf("poll %+v %v; adds %d", sum, err, e.adds())
	}
}

// Two processes: the first's add is still in flight when a second takes the route over. The second adds nothing
// while the add can still land; one that comes after addSettle resumes and adds, and the first's late answer then
// writes nothing over the second's result.
func TestTakeoverDuringAnAddInFlight(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	other := e.secondRouter()
	sent, release := make(chan struct{}), make(chan struct{})
	e.ad["rd"].add = func(m adder.Magnet, _ adder.AddOptions) (*adder.Added, error) {
		close(sent)
		<-release
		return &adder.Added{ID: "rd-late", Hash: m.Hash()}, nil
	}
	req := movieReq(cand(h("a"), "A.2160p"))
	var firstErr error
	done := make(chan struct{})
	go func() {
		_, firstErr = e.r.Route(e.ctx, req)
		close(done)
	}()
	<-sent
	e.ad["rd"].mu.Lock()
	e.ad["rd"].add = nil // the second process's adds answer at once
	e.ad["rd"].mu.Unlock()
	res, err := other.Route(e.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeInFlight || e.adds() != 1 {
		t.Fatalf("second route while the add is in flight: %+v: %s; adds %d", res, steps(res), e.adds())
	}
	// The first process stalls past addSettle: the second resumes the download and adds it.
	e.clock = e.clock.Add(addSettle + time.Second)
	res, err = other.Route(e.ctx, req)
	if err != nil || res.Outcome != OutcomeAdded || res.TorrentID != "rd-aaaa" {
		t.Fatalf("%+v %v: %s", res, err, steps(res))
	}
	close(release)
	<-done
	if !errors.Is(firstErr, errAddTaken) {
		t.Errorf("first route: %v, want errAddTaken", firstErr)
	}
	if state, _, tid, _, _ := e.download(res.DownloadID); state != StateAdded || tid != "rd-aaaa" {
		t.Errorf("download %s %s: the late answer overwrote the second route's result", state, tid)
	}
	if e.deletes() != 0 {
		t.Errorf("%d deletes", e.deletes())
	}
}

// TestUnlandedAddsCapped: an add whose answer never says, and which never lands, is resumed and sent
// again at most maxUnlanded times to the same provider; the route then tries the version's next provider instead of
// looping the same add for every resume.
func TestUnlandedAddsCapped(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.ad["rd"].add = func(adder.Magnet, adder.AddOptions) (*adder.Added, error) {
		return nil, &adder.Error{Provider: "rd", Code: adder.CodeUnavailable, Err: errors.New("connection reset")}
	}
	req := movieReq(cand(h("a"), "A.2160p"))
	var res *Result
	for range maxUnlanded + 1 {
		var err error
		if res, err = e.r.Route(e.ctx, req); err != nil {
			t.Fatal(err)
		}
		e.clock = e.clock.Add(addSettle + time.Second)
		for _, k := range []string{"rd", "ad", "tb"} {
			e.exec(`DELETE FROM router_pacing WHERE provider = ?`, k)
		}
	}
	if res.Outcome != OutcomeAdded || res.Provider == "rd" {
		t.Fatalf("%+v: %s", res, steps(res))
	}
	if n := len(e.ad["rd"].adds); n != maxUnlanded {
		t.Fatalf("%d adds to rd, want %d", n, maxUnlanded)
	}
}
