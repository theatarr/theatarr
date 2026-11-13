package router

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
)

// Copies to other providers (copy.go): ordinary sequences with fake providers.

// copyDeleter is a fake adder that can delete a copy of ours (adder.StalledDeleter), as every real adder can.
type copyDeleter struct {
	*fakeAdder
	mu      sync.Mutex
	stalled []string
	// fail makes the next deletes answer an error; gone, when set, is called before the error (the provider
	// deleted it, the answer was lost).
	fail int
	gone func(id string)
}

func (d *copyDeleter) DeleteOwnStalledAdd(_ context.Context, id string, p adder.OwnAdd) error {
	if !p.NotOnAccountBefore || !p.NewEntry {
		return adder.ErrNotOwnAdd
	}
	d.mu.Lock()
	d.stalled = append(d.stalled, id)
	fail := d.fail > 0
	if fail {
		d.fail--
	}
	d.mu.Unlock()
	if fail {
		if d.gone != nil {
			d.gone(id)
		}
		return &adder.Error{Code: adder.CodeUnavailable, Message: "connection reset"}
	}
	d.gone(id)
	return nil
}

func (d *copyDeleter) deleted() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.stalled)
}

type copyEnv struct {
	*env
	w   *jobs.Worker
	del map[string]*copyDeleter
}

// newCopyEnv is a router with only the given providers configured; adds land on the fake account in state (its
// torrents keep the hash), and the deleters remove them from it.
func newCopyEnv(t *testing.T, state provider.State, keys ...string) *copyEnv {
	t.Helper()
	e := &copyEnv{env: newEnv(t, true, policy.ModeActive), del: map[string]*copyDeleter{}}
	e.w = withWorker(e.env)
	for k := range e.r.adders {
		delete(e.r.adders, k)
	}
	for _, k := range keys {
		fa, fp := e.ad[k], e.pr[k]
		fa.add = func(m adder.Magnet, _ adder.AddOptions) (*adder.Added, error) {
			id := k + "-" + m.Hash()[:4]
			fp.set(id, provider.Torrent{Hash: m.Hash(), State: state, Status: string(state),
				Files: []provider.File{{ID: "1", Path: "/Movie.mkv", Bytes: 1 << 30}}})
			return &adder.Added{ID: id, Hash: m.Hash()}, nil
		}
		d := &copyDeleter{fakeAdder: fa}
		d.gone = func(id string) {
			fp.mu.Lock()
			delete(fp.torrents, id)
			fp.mu.Unlock()
		}
		e.del[k] = d
		e.r.adders[k] = d
	}
	return e
}

// run works every ready job.
func (e *copyEnv) run() {
	e.t.Helper()
	for range 20 {
		ok, err := e.w.RunOne(e.ctx)
		if err != nil {
			e.t.Fatal(err)
		}
		if !ok {
			return
		}
	}
	e.t.Fatal("jobs keep coming")
}

func (e *copyEnv) copyState(hash, pk string) (state, msg string) {
	e.t.Helper()
	if err := e.st.Reader().QueryRow(`SELECT state, message FROM provider_copy WHERE info_hash = ? AND provider = ?`,
		hash, pk).Scan(&state, &msg); err != nil {
		e.t.Fatal(err)
	}
	return state, msg
}

func (e *copyEnv) queue(hash, name string) []CopyView {
	e.t.Helper()
	v, err := e.r.QueueCopies(e.ctx, CopyRequest{Hash: hash, Name: name, TitleID: 1})
	if err != nil {
		e.t.Fatal(err)
	}
	return v
}

// TorBox cached: added, and "Cached on" learns TB (content_provider present at 100). TorBox uncached: nothing added.
func TestCopyTorBox(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloaded, "tb")
	e.pr["tb"].cache = map[string]bool{h("a"): true}
	e.queue(h("a"), "Movie.2024.2160p.WEB-GRP")
	e.queue(h("b"), "Other.2024.2160p.WEB-GRP")
	e.run()
	if s, msg := e.copyState(h("a"), "tb"); s != CopyAdded || msg != "Added to TB" {
		t.Fatalf("cached: %s %q", s, msg)
	}
	if n := e.count(`SELECT COUNT(*) FROM content_provider cp JOIN content c ON c.id = cp.content_id
		WHERE c.info_hash = ? AND cp.provider = 'tb' AND cp.state = 'present' AND cp.progress = 100`, h("a")); n != 1 {
		t.Fatal("Cached on does not show TB")
	}
	if s, _ := e.copyState(h("b"), "tb"); s != CopyNotCached {
		t.Fatalf("uncached: %s", s)
	}
	if got := e.ad["tb"].adds; len(got) != 1 || got[0] != h("a") {
		t.Fatalf("adds %v", got)
	}
}

// Real-Debrid with no evidence: a trial add. Complete: added. Not complete after the grace: our add is deleted,
// not_cached, and the uncached add counts in pacing.
func TestCopyRealDebridTrial(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloaded, "rd")
	e.queue(h("a"), "Movie.2024.2160p.WEB-GRP")
	e.run()
	if s, _ := e.copyState(h("a"), "rd"); s != CopyAdded {
		t.Fatalf("complete: %s", s)
	}
	e2 := newCopyEnv(t, provider.StateDownloading, "rd")
	e2.queue(h("b"), "Movie.2024.2160p.WEB-GRP")
	e2.run()
	if s, msg := e2.copyState(h("b"), "rd"); s != CopyNotCached || msg != "Not cached on RD; our add was deleted" {
		t.Fatalf("not complete: %s %q", s, msg)
	}
	if e2.del["rd"].deleted() != 1 || len(e2.ad["rd"].adds) != 1 {
		t.Fatalf("deletes %d adds %d", e2.del["rd"].deleted(), len(e2.ad["rd"].adds))
	}
	if p, _ := loadPace(e2.ctx, e2.st.Writer(), "rd"); p.HourUncached != 1 || p.WindowAdds != 1 {
		t.Fatalf("pacing %+v", p)
	}
}

// AllDebrid: the hash on the account's list is added, nothing sent or deleted. A failed list read sends nothing.
func TestCopyAllDebridList(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloading, "ad")
	e.pr["ad"].list = []provider.Torrent{{ID: "77", Hash: h("a"), State: provider.StateDownloaded}}
	e.queue(h("a"), "Movie.2024.2160p.WEB-GRP")
	e.run()
	if s, msg := e.copyState(h("a"), "ad"); s != CopyAdded || msg != "Already on AD" {
		t.Fatalf("on the list: %s %q", s, msg)
	}
	if e.ad["ad"].writes() != 0 || e.del["ad"].deleted() != 0 {
		t.Fatal("wrote to AllDebrid")
	}
	e.pr["ad"].listErr = errors.New("503")
	e.queue(h("b"), "Other.2024.2160p.WEB-GRP")
	e.run()
	if s, _ := e.copyState(h("b"), "ad"); s != CopyQueued || e.ad["ad"].writes() != 0 {
		t.Fatalf("failed list read: %s, %d writes", s, e.ad["ad"].writes())
	}
}

// Real-Debrid's name rule: refused, never sent. Pause: no copy is queued, and a queued one is skipped.
func TestCopyRefusedAndPaused(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloaded, "rd")
	e.queue(h("a"), "Movie.2024.1080p.WEB.x264-YIFY")
	e.queue(h("b"), "Movie.2024.2160p.WEB-GRP")
	e.r.set.Enabled = false
	if _, err := e.r.QueueCopies(e.ctx, CopyRequest{Hash: h("c")}); !errors.Is(err, ErrCopyPaused) {
		t.Fatalf("paused: %v", err)
	}
	e.r.set.Enabled = true
	e.run()
	if s, msg := e.copyState(h("a"), "rd"); s != CopyRefused || msg != "Real-Debrid refuses this name" {
		t.Fatalf("name rule: %s %q", s, msg)
	}
	if s, _ := e.copyState(h("b"), "rd"); s != CopyAdded {
		t.Fatalf("b: %s", s)
	}
	e.queue(h("d"), "Third.2024.2160p.WEB-GRP")
	e.r.set.Enabled = false
	e.run()
	if s, msg := e.copyState(h("d"), "rd"); s != CopySkipped || msg != "Skipped: paused" {
		t.Fatalf("paused run: %s %q", s, msg)
	}
	if len(e.ad["rd"].adds) != 1 {
		t.Fatalf("adds %v", e.ad["rd"].adds)
	}
}

// Inside Real-Debrid's minimum gap the job is handed back, runs after the gap and ends added.
func TestCopyWaitsForMinGap(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloaded, "rd")
	e.pol.Providers["rd"].Pacing = policy.Pacing{} // RD's 20s default
	e.exec(`INSERT INTO router_pacing (provider, last_add_at, updated_at) VALUES ('rd', ?, 0)`,
		e.clock.Add(-5*time.Second).UnixMilli())
	e.queue(h("a"), "Movie.2024.2160p.WEB-GRP")
	e.run()
	if s, _ := e.copyState(h("a"), "rd"); s != CopyQueued || len(e.ad["rd"].adds) != 0 {
		t.Fatalf("inside the gap: %s, adds %v", s, e.ad["rd"].adds)
	}
	e.clock = e.clock.Add(15 * time.Second)
	e.run()
	if s, _ := e.copyState(h("a"), "rd"); s != CopyAdded || len(e.ad["rd"].adds) != 1 {
		t.Fatalf("after the gap: %s, adds %v", s, e.ad["rd"].adds)
	}
}

// After a restart (a fresh router, its adders' ledgers empty) a copy in checking with own_add whose torrent is not
// complete is deleted once on Real-Debrid and on AllDebrid, and ends not_cached.
func TestCopyRestartInChecking(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloading, "rd", "ad")
	for _, pk := range []string{"rd", "ad"} {
		e.pr[pk].set(pk+"-1", provider.Torrent{Hash: h("a"), State: provider.StateDownloading, Status: "downloading"})
		e.exec(`INSERT INTO provider_copy (info_hash, name, provider, torrent_id, own_add, state, created_at,
			updated_at) VALUES (?, 'x', ?, ?, 1, 'checking', 0, 0)`, h("a"), pk, pk+"-1")
	}
	r := e.secondRouter()
	for k, d := range e.del {
		r.adders[k] = d
	}
	delete(r.adders, "tb")
	q := jobs.NewQueue(e.st)
	q.SetClock(func() time.Time { return e.clock })
	w := jobs.NewWorker(q, jobs.WorkerOptions{})
	r.RegisterJobs(w, q)
	if n, err := r.ResumeCopies(e.ctx); err != nil || n != 2 {
		t.Fatalf("resumed %d: %v", n, err)
	}
	e.w = w
	e.run()
	for _, pk := range []string{"rd", "ad"} {
		if s, _ := e.copyState(h("a"), pk); s != CopyNotCached || e.del[pk].deleted() != 1 {
			t.Fatalf("%s: %s, %d deletes", pk, s, e.del[pk].deleted())
		}
	}
	if e.writes() != 0 {
		t.Fatal("added again")
	}
}

// A delete whose answer is lost: the delete is owed on the row; once the torrent is gone the copy ends not_cached.
// It is never added again.
func TestCopyLostDeleteAnswer(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloading, "rd")
	e.del["rd"].fail = 1
	e.del["rd"].gone = func(string) {} // the first delete's answer is lost and the torrent still shows
	e.queue(h("a"), "Movie.2024.2160p.WEB-GRP")
	e.run()
	if s, msg := e.copyState(h("a"), "rd"); s != CopyChecking || msg == "" {
		t.Fatalf("after the lost answer: %s %q", s, msg)
	}
	// The provider finishes the delete; the job's retry finds the torrent gone.
	e.pr["rd"].mu.Lock()
	delete(e.pr["rd"].torrents, "rd-"+h("a")[:4])
	e.pr["rd"].mu.Unlock()
	e.clock = e.clock.Add(time.Hour)
	e.run()
	if s, _ := e.copyState(h("a"), "rd"); s != CopyNotCached {
		t.Fatalf("after the retry: %s", s)
	}
	if len(e.ad["rd"].adds) != 1 || e.del["rd"].deleted() != 1 {
		t.Fatalf("adds %d deletes %d", len(e.ad["rd"].adds), e.del["rd"].deleted())
	}
}

// One request per hash and provider: a second request (a double click, a season pack's other episodes) returns the
// unfinished row; a route never deletes a copy's entry (guardBefore sees it).
func TestCopyDedupeAndRouteGuard(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloaded, "rd", "ad")
	a := e.queue(h("a"), "Movie.2024.2160p.WEB-GRP")
	b := e.queue(h("a"), "Movie.2024.2160p.WEB-GRP")
	if len(a) != 2 || len(b) != 2 || e.count(`SELECT COUNT(*) FROM job WHERE kind = ?`, JobCopy) != 2 {
		t.Fatalf("rows %v %v, jobs %d", a, b, e.count(`SELECT COUNT(*) FROM job WHERE kind = ?`, JobCopy))
	}
	e.exec(`UPDATE provider_copy SET state = 'checking', torrent_id = '77' WHERE provider = 'ad'`)
	g, err := e.r.guardBefore(e.ctx, e.pol.Providers["ad"], "ad", h("a"), 0, false)
	if err != nil || g.before == "" {
		t.Fatalf("guard %+v %v", g, err)
	}
}

// An adding row (the add's answer was lost) waits addSettle, then is finished from the account: a torrent of the
// hash there is taken up but never deleted (it may be an older one); none there: the copy is added again.
func TestCopyRecoversAdding(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloaded, "rd", "ad")
	e.pr["rd"].list = []provider.Torrent{{ID: "old", Hash: h("a")}}
	e.pr["rd"].set("old", provider.Torrent{Hash: h("a"), State: provider.StateDownloading, Status: "downloading"})
	for _, pk := range []string{"rd", "ad"} {
		e.exec(`INSERT INTO provider_copy (info_hash, name, provider, own_add, state, created_at, updated_at)
			VALUES (?, 'Movie.2024.2160p.WEB-GRP', ?, 1, 'adding', 0, ?)`, h("a"), pk, e.clock.UnixMilli())
	}
	if n, err := e.r.ResumeCopies(e.ctx); err != nil || n != 2 {
		t.Fatalf("resumed %d: %v", n, err)
	}
	e.run()
	if s, _ := e.copyState(h("a"), "ad"); s != CopyAdding || e.writes() != 0 {
		t.Fatalf("inside addSettle: %s, %d writes", s, e.writes())
	}
	e.clock = e.clock.Add(addSettle)
	e.run()
	if s, msg := e.copyState(h("a"), "rd"); s != CopyNotCached || e.del["rd"].deleted() != 0 ||
		msg != "Not cached on RD; left on the account: it was there before our add" {
		t.Fatalf("rd: %s %q, %d deletes", s, msg, e.del["rd"].deleted())
	}
	if s, _ := e.copyState(h("a"), "ad"); s != CopyAdded || len(e.ad["ad"].adds) != 1 {
		t.Fatalf("ad: %s, adds %v", s, e.ad["ad"].adds)
	}
}
