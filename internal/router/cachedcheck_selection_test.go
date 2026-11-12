package router

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
)

// Real-Debrid verdicts wait for a recorded selection and its grace, the grace counts from the
// selection's answer, an owed check is not ended by the stall timeouts, and an ambiguous delete keeps its journal.

func setRDStatus(f *fakeDebrid, id, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rd[id].status = status
}

// A pending check on Real-Debrid with no selection recorded is no verdict: a converting magnet, or one queued after
// a selection whose answer we did not get, is not deleted at the grace after the add.
func TestCachedCheckRDNeedsSelection(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"magnet_conversion", "queued"} {
		t.Run(status, func(t *testing.T) {
			e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
			withQueue(e)
			f.rdStuck = true
			f.cached["rd:"+h("a")] = true
			dl := hiccupAdd(t, e, f)
			setRDStatus(f, "RD1001", status)
			if status == "queued" {
				f.mu.Lock()
				f.rd["RD1001"].selected = true
				f.mu.Unlock()
				e.exec(`UPDATE download SET error = 'selecting files: timeout' WHERE id = ?`, dl)
			}
			e.clock = e.clock.Add(15 * time.Second)
			if _, err := e.r.Poll(e.ctx); err != nil {
				t.Fatal(err)
			}
			if f.deletes() != 0 || cachedCheck(e, dl) != checkPending {
				t.Fatalf("deleted before a selection and its grace: deletes %d, check %q, requests %v", f.deletes(),
					cachedCheck(e, dl), f.list())
			}
			if n := e.count(`SELECT COUNT(*) FROM router_blocklist`); n != 0 {
				t.Error("blocklisted")
			}
		})
	}
}

// lostSelection selects on Real-Debrid, which queues the torrent, and loses the answer.
type lostSelection struct {
	adder.Adder
	f *fakeDebrid
}

func (a lostSelection) SelectFiles(ctx context.Context, id string, ids []string) error {
	if err := a.Adder.SelectFiles(ctx, id, ids); err != nil {
		return err
	}
	setRDStatus(a.f, id, "queued")
	return errors.New("selection answer lost")
}

func (a lostSelection) DeleteOwnStalledAdd(ctx context.Context, id string, proof adder.OwnAdd) error {
	return a.Adder.(adder.StalledDeleter).DeleteOwnStalledAdd(ctx, id, proof)
}

// A selection whose answer was lost and a torrent Real-Debrid then shows queued: the poller infers the selection
// then, the grace counts from there, and the torrent finishing ends the check with nothing deleted.
func TestCachedCheckLostSelectionQueued(t *testing.T) {
	t.Parallel()
	e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
	withQueue(e)
	e.r.set.CachedGrace = 30 * time.Second
	e.r.set.PollInterval = 15 * time.Second
	f.cached["rd:"+h("a")] = true
	dl := hiccupAdd(t, e, f)
	e.clock = e.clock.Add(time.Minute) // the selection long after the add
	e.r.adders["rd"] = lostSelection{Adder: e.r.adders["rd"], f: f}
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.clock = e.clock.Add(15 * time.Second)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	var sel int
	var selAt int64
	if err := e.st.Reader().QueryRow(`SELECT files_selected, COALESCE(selected_at, 0) FROM download WHERE id = ?`,
		dl).Scan(&sel, &selAt); err != nil {
		t.Fatal(err)
	}
	if sel != 1 || selAt != e.clock.UnixMilli() {
		t.Fatalf("selection not inferred from queued: files_selected %d, selected_at %d", sel, selAt)
	}
	e.clock = e.clock.Add(15 * time.Second)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if f.deletes() != 0 || cachedCheck(e, dl) != checkPending {
		t.Fatalf("deleted within the grace after the inferred selection: deletes %d, check %q, requests %v",
			f.deletes(), cachedCheck(e, dl), f.list())
	}
	setRDStatus(f, "RD1001", "downloaded")
	e.clock = e.clock.Add(15 * time.Second)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if f.deletes() != 0 || cachedCheck(e, dl) != "" {
		t.Fatalf("finished torrent: deletes %d, check %q, requests %v", f.deletes(), cachedCheck(e, dl), f.list())
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist`); n != 0 {
		t.Error("blocklisted")
	}
}

// slowSelection spends 10s of the clock waiting for the selection's answer.
type slowSelection struct {
	adder.Adder
	clock *time.Time
}

func (a slowSelection) SelectFiles(ctx context.Context, id string, ids []string) error {
	*a.clock = a.clock.Add(10 * time.Second)
	return a.Adder.SelectFiles(ctx, id, ids)
}

func (a slowSelection) DeleteOwnStalledAdd(ctx context.Context, id string, proof adder.OwnAdd) error {
	return a.Adder.(adder.StalledDeleter).DeleteOwnStalledAdd(ctx, id, proof)
}

// The grace after the selection counts from its answer, not from before the request.
func TestCachedCheckGraceFromSelectionAnswer(t *testing.T) {
	t.Parallel()
	e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
	withQueue(e)
	e.r.set.CachedGrace = 30 * time.Second
	e.r.set.PollInterval = 15 * time.Second
	f.cached["rd:"+h("a")] = true
	dl := hiccupAdd(t, e, f)
	e.r.adders["rd"] = slowSelection{Adder: e.r.adders["rd"], clock: &e.clock}
	if _, err := e.r.Poll(e.ctx); err != nil { // selects; the answer comes 10s later
		t.Fatal(err)
	}
	setRDStatus(f, "RD1001", "downloading")
	e.clock = e.clock.Add(25 * time.Second) // 35s after the request, 25s after the answer
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if f.deletes() != 0 || cachedCheck(e, dl) != checkPending {
		t.Fatalf("deleted 25s after the selection's answer, within its 30s grace: deletes %d, check %q, requests %v",
			f.deletes(), cachedCheck(e, dl), f.list())
	}
	e.clock = e.clock.Add(5 * time.Second)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if f.deletes() != 1 {
		t.Fatalf("not deleted at the grace after the answer: %v", f.list())
	}
}

// queuedSelection leaves the torrent queued after the selection.
type queuedSelection struct {
	adder.Adder
	f *fakeDebrid
}

func (a queuedSelection) SelectFiles(ctx context.Context, id string, ids []string) error {
	if err := a.Adder.SelectFiles(ctx, id, ids); err != nil {
		return err
	}
	setRDStatus(a.f, id, "queued")
	return nil
}

func (a queuedSelection) DeleteOwnUncachedAdd(ctx context.Context, added *adder.Added, proof adder.OwnAdd) error {
	return a.Adder.(adder.Deleter).DeleteOwnUncachedAdd(ctx, added, proof)
}

func (a queuedSelection) DeleteOwnStalledAdd(ctx context.Context, id string, proof adder.OwnAdd) error {
	return a.Adder.(adder.StalledDeleter).DeleteOwnStalledAdd(ctx, id, proof)
}

// An owed delete waiting for the gate past stalled_after is not ended as stalled (blocklisted, the check dropped):
// the download stays, and when the gate opens the delete goes through, the miss is recorded and the release routed
// again, nothing blocklisted.
func TestCachedCheckGateClosedPastStall(t *testing.T) {
	t.Parallel()
	e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
	withQueue(e)
	e.r.adders["rd"] = queuedSelection{Adder: e.r.adders["rd"], f: f}
	e.r.sleep = func(context.Context, time.Duration) error {
		e.r.set.Enabled = false
		return nil
	}
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || cachedCheck(e, res.DownloadID) != checkUncached || f.deletes() != 0 {
		t.Fatalf("expected an owed delete behind the gate: %+v %v %v", res, err, f.list())
	}
	dl := res.DownloadID
	e.clock = e.clock.Add(11 * time.Minute)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state IN ('added', 'downloading')
		AND cached_check = 'uncached'`, dl); n != 1 || f.deletes() != 0 {
		t.Fatalf("owed check ended past stalled_after with the gate closed: %s, requests %v", attempts(e, dl), f.list())
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist`); n != 0 {
		t.Fatal("blocklisted while the delete waited for the gate")
	}
	e.r.set.Enabled = true
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if f.count("rd.test DELETE /rest/1.0/torrents/delete/RD1001") != 1 {
		t.Fatalf("not deleted when the gate opened: %v", f.list())
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND cached_check IS NULL
		AND ((state = 'queued' AND provider IS NULL) OR state = 'failed')`, dl); n != 1 {
		t.Errorf("download not routed again: %s", attempts(e, dl))
	}
	if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE info_hash = ? AND provider = 'rd'
		AND state = 'uncached'`, h("a")); n != 1 {
		t.Error("rd not recorded uncached")
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist`); n != 0 {
		t.Error("blocklisted")
	}
}

// flakyRead fails the next status read once when fail is set.
type flakyRead struct {
	provider.Provider
	fail bool
}

func (p *flakyRead) Torrent(ctx context.Context, id string) (*provider.Torrent, error) {
	if p.fail {
		p.fail = false
		return nil, errors.New("read unavailable")
	}
	return p.Provider.Torrent(ctx, id)
}

// lostDelete deletes, then loses the answer, and the read after it fails: the delete's outcome is not known.
type lostDelete struct {
	adder.Adder
	read *flakyRead
}

func (a lostDelete) DeleteOwnUncachedAdd(ctx context.Context, added *adder.Added, proof adder.OwnAdd) error {
	if err := a.Adder.(adder.Deleter).DeleteOwnUncachedAdd(ctx, added, proof); err != nil {
		return err
	}
	a.read.fail = true
	return errors.New("delete answer lost")
}

func (a lostDelete) DeleteOwnStalledAdd(ctx context.Context, id string, proof adder.OwnAdd) error {
	if err := a.Adder.(adder.StalledDeleter).DeleteOwnStalledAdd(ctx, id, proof); err != nil {
		return err
	}
	a.read.fail = true
	return errors.New("delete answer lost")
}

// wantOwedMiss checks that the ambiguous delete was taken as ours once the torrent was gone: the miss recorded, the
// release routed again, nothing blocklisted.
func wantOwedMiss(t *testing.T, e *env, dl int64) {
	t.Helper()
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist`); n != 0 {
		t.Fatalf("our own ambiguous delete blocklisted the release: %s", attempts(e, dl))
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND cached_check IS NULL
		AND ((state = 'queued' AND provider IS NULL) OR state = 'failed') AND error NOT LIKE ?`, dl,
		deletingPrefix+"%"); n != 1 {
		t.Errorf("download not routed again: %s", attempts(e, dl))
	}
	if n := e.count(`SELECT COUNT(*) FROM provider_cache WHERE info_hash = ? AND provider = 'rd'
		AND state = 'uncached'`, h("a")); n != 1 {
		t.Error("rd not recorded uncached")
	}
}

// The poller's owed delete errors and the read after it says nothing: the journal stays, and the torrent then gone
// is our delete.
func TestCachedCheckAmbiguousDelete(t *testing.T) {
	t.Parallel()
	e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
	withQueue(e)
	f.rd["RD1"] = &fakeRD{hash: h("a"), status: "downloading", selected: true}
	insertChecked(e, 1, "rd", "RD1", h("a"), checkUncached, 0, 1)
	p := &flakyRead{Provider: e.r.provs["rd"]}
	e.r.provs["rd"] = p
	e.r.adders["rd"] = lostDelete{Adder: e.r.adders["rd"], read: p}
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if f.deletes() != 1 || cachedCheck(e, 1) != checkUncached ||
		e.count(`SELECT COUNT(*) FROM download WHERE id = 1 AND error LIKE ?`, deletingPrefix+"%") != 1 {
		t.Fatalf("expected the ambiguous delete journaled: deletes %d, %s", f.deletes(), attempts(e, 1))
	}
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.clock = e.clock.Add(missingGrace)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	wantOwedMiss(t, e, 1)
}

// The same in the route's own settle: the uncached verdict's delete errors and the read after it says nothing.
func TestCachedCheckAmbiguousInitialDelete(t *testing.T) {
	t.Parallel()
	e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
	e.pol.Versions["movie"].Providers = []string{"rd"}
	withQueue(e)
	p := &flakyRead{Provider: e.r.provs["rd"]}
	e.r.provs["rd"] = p
	e.r.adders["rd"] = lostDelete{Adder: e.r.adders["rd"], read: p}
	res, err := e.r.Route(e.ctx, movieReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	dl := res.DownloadID
	if err != nil || f.deletes() != 1 || cachedCheck(e, dl) != checkUncached ||
		e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND error LIKE ?`, dl, deletingPrefix+"%") != 1 {
		t.Fatalf("expected the ambiguous delete journaled: %+v %v %v, %s", res, err, f.list(), attempts(e, dl))
	}
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.clock = e.clock.Add(missingGrace)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	wantOwedMiss(t, e, dl)
}

// A selection sent just before stalled_after whose answer was lost, and Real-Debrid showing the add queued past the
// deadline: the poller infers the selection before the stall timeout and holds for its grace; the torrent finishing
// then ends the check with nothing deleted or blocklisted.
func TestCachedCheckLostSelectionAtStallDeadline(t *testing.T) {
	t.Parallel()
	e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
	withQueue(e)
	e.r.set.CachedGrace = 30 * time.Second
	e.r.set.PollInterval = 15 * time.Second
	f.cached["rd:"+h("a")] = true
	dl := hiccupAdd(t, e, f)
	e.clock = e.clock.Add(e.r.stallAfter("rd") - 5*time.Second)
	e.r.adders["rd"] = lostSelection{Adder: e.r.adders["rd"], f: f}
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if f.count("rd.test POST /rest/1.0/torrents/selectFiles/RD1001") != 1 {
		t.Fatalf("selection not sent: %v", f.list())
	}
	e.clock = e.clock.Add(15 * time.Second) // past stalled_after
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'added' AND files_selected = 1
		AND selected_at = ? AND cached_check = 'pending'`, dl, e.clock.UnixMilli()); n != 1 || f.deletes() != 0 {
		t.Fatalf("lost selection not inferred before the stall timeout: %s, requests %v", attempts(e, dl), f.list())
	}
	e.clock = e.clock.Add(15 * time.Second)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if f.deletes() != 0 || cachedCheck(e, dl) != checkPending {
		t.Fatalf("ended within the inferred selection's grace: %s, requests %v", attempts(e, dl), f.list())
	}
	setRDStatus(f, "RD1001", "downloaded")
	e.clock = e.clock.Add(15 * time.Second)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if f.deletes() != 0 || cachedCheck(e, dl) != "" ||
		e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND state = 'ready'`, dl) != 1 {
		t.Fatalf("cached torrent not kept: %s, requests %v", attempts(e, dl), f.list())
	}
	if n := e.count(`SELECT COUNT(*) FROM router_blocklist`); n != 0 {
		t.Error("blocklisted")
	}
}

// A lost selection inferred late in a slow poll pass (earlier status reads of 5s each): the inferred selected_at is
// the time of the inference, not the pass's start, so the next polls within the grace keep the add.
func TestCachedCheckInferenceAfterSlowReads(t *testing.T) {
	t.Parallel()
	e, f, _ := newDebridEnv(t, true, policy.ModeActive, true)
	withQueue(e)
	e.r.set.CachedGrace = 30 * time.Second
	e.r.set.PollInterval = 15 * time.Second
	f.cached["rd:"+h("a")] = true
	dl := hiccupAdd(t, e, f)
	e.clock = e.clock.Add(e.r.stallAfter("rd") - 5*time.Second)
	e.r.adders["rd"] = lostSelection{Adder: e.r.adders["rd"], f: f}
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.clock = e.clock.Add(15 * time.Second)
	for i, hash := range []string{h("b"), h("c"), h("d"), h("e"), h("f")} {
		id := "tb" + string(rune('1'+i))
		e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer,
			provider, provider_torrent_id, state, added_at, created_at, updated_at)
			VALUES (?, 1, 10, 'movie', ?, 'Movie', 'pub', 'tb', ?, 'added', ?, 0, 0)`,
			i+10, hash, id, e.clock.UnixMilli())
		e.pr["tb"].set(id, provider.Torrent{Hash: hash, Status: "queued", State: provider.StateQueued})
	}
	slow := 5
	e.pr["tb"].read = func(context.Context, string) {
		if slow > 0 {
			e.clock = e.clock.Add(5 * time.Second) // the pass's earlier reads take 25s in all
			slow--
		}
	}
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM download WHERE id = ? AND files_selected = 1 AND selected_at = ?`,
		dl, e.clock.UnixMilli()); n != 1 {
		t.Fatalf("inferred selection not stamped at the inference: %s, requests %v", attempts(e, dl), f.list())
	}
	inferredAt := e.clock
	if _, err := e.r.Poll(e.ctx); err != nil { // the 15s ticker fired during the slow pass
		t.Fatal(err)
	}
	e.clock = e.clock.Add(5 * time.Second)
	if _, err := e.r.Poll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if f.deletes() != 0 || cachedCheck(e, dl) != checkPending {
		t.Fatalf("ended %v after the inference, within its grace: %s, requests %v", e.clock.Sub(inferredAt),
			attempts(e, dl), f.list())
	}
}
