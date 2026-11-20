package torrents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// DeleteOwnUncachedAdd makes the fake an adder.Deleter: it records our own deletes and refuses one without the proof.
func (a *fakeAdder) DeleteOwnUncachedAdd(_ context.Context, ad *adder.Added, proof adder.OwnAdd) error {
	if !proof.NotOnAccountBefore || !proof.NewEntry {
		return adder.ErrNotOwnAdd
	}
	if a.deleteErr != nil {
		return a.deleteErr
	}
	a.ownDeleted = append(a.ownDeleted, ad.ID)
	return nil
}

// copyEnv is newEnv with an AllDebrid account as well (rd, ad, tb in that order).
func copyEnv(t *testing.T) (*env, *fakeProvider, *fakeAdder) {
	e := newEnv(t)
	e.pol.Providers["ad"] = &policy.Provider{Kind: provider.KindAllDebrid, Order: 2}
	e.pol.Providers["tb"].Order = 3
	ad := &fakeProvider{key: "ad", kind: provider.KindAllDebrid}
	aad := &fakeAdder{key: "ad", kind: provider.KindAllDebrid}
	e.m.Providers["ad"], e.m.Adders["ad"] = ad, aad
	e.rd.detail, ad.detail, e.tb.detail = map[string]*provider.Torrent{}, map[string]*provider.Torrent{},
		map[string]*provider.Torrent{}
	return e, ad, aad
}

func (e *env) copyRows(hash string) map[string]JournalEntry {
	e.t.Helper()
	st, err := e.m.CopyStatus(context.Background(), hash)
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]JournalEntry{}
	for _, r := range st.Results {
		out[r.Provider] = JournalEntry{Provider: r.Provider, Outcome: r.Outcome, Detail: r.Detail}
	}
	return out
}

func (e *env) cacheRow(hash, prov string) string {
	e.t.Helper()
	var state, source string
	_ = e.st.Reader().QueryRow(`SELECT state, source FROM provider_cache WHERE info_hash = ? AND provider = ?`, hash,
		prov).Scan(&state, &source)
	return state + " " + source
}

func (e *env) recorded(prov, hash string) bool {
	e.t.Helper()
	var n int
	_ = e.st.Reader().QueryRow(`SELECT COUNT(*) FROM provider_torrent WHERE provider = ? AND info_hash = ?
		AND progress >= 100`, prov, hash).Scan(&n)
	return n > 0
}

// The button: the hash is on AllDebrid; Real-Debrid has it cached (added: record row and cached evidence), TorBox does
// not (its add_only_if_cached refuses, nothing made), and AllDebrid already holds it (present, nothing sent).
func TestCopyAddsWhereCached(t *testing.T) {
	t.Parallel()
	e, ad, aad := copyEnv(t)
	ctx := context.Background()
	ad.list = []provider.Torrent{tor("m1", hA, "Movie.2020.1080p.BluRay", 10, e.now, provider.StateDownloaded)}
	e.rd.detail["new-aaaa"] = &provider.Torrent{ID: "new-aaaa", Hash: hA, Name: "Movie.2020.1080p.BluRay", Bytes: 10,
		State: provider.StateDownloaded, Status: "downloaded"}
	e.atb.addErr = map[string]error{hA: &adder.Error{Provider: "tb", Code: adder.CodeNotCached}}
	usage := 0
	e.m.UsageStale = func() { usage++ }
	if _, err := e.m.Copy(ctx, CopyPayload{Hash: hA, By: "admin"}); err != nil {
		t.Fatal(err)
	}
	rows := e.copyRows(hA)
	if rows["rd"].Outcome != CopyAdded || rows["ad"].Outcome != CopyPresent || rows["ad"].Detail != "" ||
		rows["tb"].Outcome != CopyNotCached {
		t.Fatalf("outcomes %+v", rows)
	}
	if len(aad.added) != 0 || len(e.ard.added) != 1 || len(e.ard.ownDeleted) != 0 {
		t.Fatalf("adds: ad %v rd %v, deleted %v", aad.added, e.ard.added, e.ard.ownDeleted)
	}
	if !e.recorded("rd", hA) || !e.recorded("ad", hA) || e.recorded("tb", hA) || usage != 2 {
		t.Fatalf("record rows rd %v ad %v tb %v, usage asked %d", e.recorded("rd", hA), e.recorded("ad", hA),
			e.recorded("tb", hA), usage)
	}
	if got := e.cacheRow(hA, "rd"); got != "cached add:rd" {
		t.Errorf("rd evidence %q", got)
	}
	if got := e.cacheRow(hA, "tb"); got != "uncached router:add" {
		t.Errorf("tb evidence %q", got)
	}
}

// Not cached on Real-Debrid and AllDebrid: our own new add is deleted on both, whatever
// router.alldebrid_delete_uncached says, and the evidence says uncached.
func TestCopyDeletesOwnUncachedAdd(t *testing.T) {
	t.Parallel()
	e, ad, aad := copyEnv(t)
	off := false
	e.pol.Router = &policy.Router{AllDebridDeleteUncached: &off}
	ctx := context.Background()
	e.tb.list = []provider.Torrent{tor("t1", hB, "Show.S01.1080p", 10, e.now, provider.StateDownloaded)}
	e.rd.detail["new-bbbb"] = &provider.Torrent{ID: "new-bbbb", Hash: hB, State: provider.StateDownloading,
		Status: "downloading"}
	ad.detail["new-bbbb"] = &provider.Torrent{ID: "new-bbbb", Hash: hB, State: provider.StateDownloading,
		Status: "downloading"}
	if _, err := e.m.Copy(ctx, CopyPayload{Hash: hB}); err != nil {
		t.Fatal(err)
	}
	rows := e.copyRows(hB)
	if rows["rd"].Outcome != CopyNotCached || rows["ad"].Outcome != CopyNotCached || rows["tb"].Outcome != CopyPresent {
		t.Fatalf("outcomes %+v", rows)
	}
	if len(e.ard.ownDeleted) != 1 || len(aad.ownDeleted) != 1 {
		t.Fatalf("own deletes rd %v ad %v", e.ard.ownDeleted, aad.ownDeleted)
	}
	if e.cacheRow(hB, "rd") != "uncached router:add" || e.cacheRow(hB, "ad") != "uncached router:add" {
		t.Errorf("evidence rd %q ad %q", e.cacheRow(hB, "rd"), e.cacheRow(hB, "ad"))
	}
	if e.recorded("rd", hB) || e.recorded("ad", hB) {
		t.Error("an uncached add was recorded as held")
	}
}

// An entry of the hash already on the account, unfinished or failed, is left alone: present with its state, nothing
// sent or deleted, and only a complete one gets a record row.
func TestCopyLeavesExistingEntries(t *testing.T) {
	t.Parallel()
	e, ad, aad := copyEnv(t)
	ctx := context.Background()
	dl := tor("r1", hC, "C", 10, e.now, provider.StateDownloading)
	dl.Progress = 12
	e.rd.list = []provider.Torrent{dl}
	ad.list = []provider.Torrent{tor("a1", hC, "C", 10, e.now, provider.StateError)}
	e.tb.list = []provider.Torrent{tor("t1", hC, "C", 10, e.now, provider.StateDownloaded)}
	if _, err := e.m.Copy(ctx, CopyPayload{Hash: hC}); err != nil {
		t.Fatal(err)
	}
	rows := e.copyRows(hC)
	if r := rows["rd"]; r.Outcome != CopyPresent || r.Detail != "still downloading (12%)" {
		t.Errorf("rd %+v", r)
	}
	if r := rows["ad"]; r.Outcome != CopyPresent || r.Detail != "failed there" {
		t.Errorf("ad %+v", r)
	}
	if len(e.ard.added)+len(aad.added)+len(e.atb.added) != 0 || len(e.ard.ownDeleted)+len(aad.ownDeleted) != 0 {
		t.Fatal("something was sent")
	}
	if e.recorded("rd", hC) || e.recorded("ad", hC) || !e.recorded("tb", hC) {
		t.Error("record rows")
	}
}

// A paced provider is skipped with the time it takes adds again (nothing sent); Real-Debrid's name rule refuses the
// release before any add; a route's guard sees the copy's add (router.RestoredSince), sent under the add lock.
func TestCopySkipsPacedAndRefused(t *testing.T) {
	t.Parallel()
	e, _, aad := copyEnv(t)
	ctx := context.Background()
	e.exec(`INSERT INTO provider_torrent (provider, torrent_id, info_hash, name, state, progress)
		VALUES ('tb', 't1', ?, 'Movie.2020.1080p.WEB-DL.x264-GRP', 'downloaded', 100)`, hD)
	retry := e.now.Add(time.Hour)
	if err := router.RecordAddOutcome(ctx, e.st, "ad", e.pol.Providers["ad"].EffectivePacing(), e.now,
		router.AddOutcome{Reason: "rate_limited", RetryAt: retry}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Copy(ctx, CopyPayload{Hash: hD}); err != nil {
		t.Fatal(err)
	}
	st, err := e.m.CopyStatus(ctx, hD)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]CopyResult{}
	for _, r := range st.Results {
		got[r.Provider] = r
	}
	if r := got["rd"]; r.Outcome != CopyRefused || r.Code != "rd" {
		t.Errorf("rd %+v", r)
	}
	if r := got["ad"]; r.Outcome != CopySkipped || r.Until == nil || r.Until.Before(e.now) || r.Code != "ad" {
		t.Errorf("ad %+v", r)
	}
	if len(e.ard.added) != 0 || len(aad.added) != 0 {
		t.Fatal("an add was sent to a refused or paced provider")
	}

	// The guard: TorBox's add, made under the lock, is seen by a route as the manager's.
	lock := &trackLock{}
	e.m.AddLock = lock
	var locked bool
	var seen string
	e.exec(`DELETE FROM provider_torrent`)
	e.atb.onAdd = func(string, string) {
		locked = lock.held
		seen, _ = router.RestoredSince(ctx, e.st.Writer(), "tb", hE, e.now.Add(-time.Minute))
	}
	if _, err := e.m.Copy(ctx, CopyPayload{Hash: hE, Providers: []string{"tb"}}); err != nil {
		t.Fatal(err)
	}
	if !locked || seen == "" || lock.held {
		t.Fatalf("under the lock %v, guard saw %q, left held %v", locked, seen, lock.held)
	}
}

// KnownHash: a hash Theatarr knows on an account or in its library; CopyTargets: the keyed providers not holding it.
func TestCopyKnownHashAndTargets(t *testing.T) {
	t.Parallel()
	e, _, _ := copyEnv(t)
	ctx := context.Background()
	e.exec(`INSERT INTO provider_torrent (provider, torrent_id, info_hash, name, state, progress)
		VALUES ('tb', 't1', ?, 'X', 'downloaded', 100)`, hA)
	for h, want := range map[string]bool{hA: true, hB: false, "nothash": false} {
		if ok, err := e.m.KnownHash(ctx, h); err != nil || ok != want {
			t.Errorf("%s: %v %v", h, ok, err)
		}
	}
	if got := e.m.CopyTargets([]string{"tb"}); len(got) != 2 || got[0] != "rd" || got[1] != "ad" {
		t.Errorf("targets %v", got)
	}
	delete(e.m.Providers, "ad")
	if got := e.m.CopyTargets([]string{"tb", "rd"}); len(got) != 0 {
		t.Errorf("a provider without a key is no target: %v", got)
	}
}

// Real-Debrid: a pack whose inner video has a name Real-Debrid refuses is refused after the add (nothing selected) and
// our add is deleted again; otherwise every playable file is selected, never the nfo.
func TestCopyRealDebridSelection(t *testing.T) {
	t.Parallel()
	e, _, _ := copyEnv(t)
	ctx := context.Background()
	files := func(video string) []provider.File {
		return []provider.File{{ID: "1", Path: "/Pack/" + video, Bytes: 1 << 30}, {ID: "2", Path: "/Pack/info.nfo", Bytes: 1}}
	}
	e.rd.detail["new-aaaa"] = &provider.Torrent{ID: "new-aaaa", Hash: hA, State: provider.StateQueued,
		Status: "waiting_files_selection", Files: files("Show.S01E01.1080p.WEB-DL.x264.mkv")}
	if _, err := e.m.Copy(ctx, CopyPayload{Hash: hA, Providers: []string{"rd"}}); err != nil {
		t.Fatal(err)
	}
	if r := e.copyRows(hA)["rd"]; r.Outcome != CopyRefused || len(e.ard.selected) != 0 || len(e.ard.ownDeleted) != 1 {
		t.Fatalf("refused inner name: %+v, selected %v, deleted %v", r, e.ard.selected, e.ard.ownDeleted)
	}

	e.now = e.now.Add(time.Hour) // past min_gap
	sel := &provider.Torrent{ID: "new-bbbb", Hash: hB, State: provider.StateQueued, Status: "waiting_files_selection",
		Files: files("Show.S01E01.1080p.BluRay.mkv")}
	e.rd.detail["new-bbbb"] = sel
	e.rd.onDetail = func() {
		if e.ard.selected["new-bbbb"] != nil {
			sel.State, sel.Status = provider.StateDownloaded, "downloaded"
		}
	}
	if _, err := e.m.Copy(ctx, CopyPayload{Hash: hB, Providers: []string{"rd"}}); err != nil {
		t.Fatal(err)
	}
	if r := e.copyRows(hB)["rd"]; r.Outcome != CopyAdded || len(e.ard.selected["new-bbbb"]) != 1 ||
		e.ard.selected["new-bbbb"][0] != "1" {
		t.Fatalf("selection: %+v, selected %v", r, e.ard.selected)
	}
}

// The report's rows carry add_to: the keyed providers whose record does not hold the hash complete.
func TestReportAddTo(t *testing.T) {
	t.Parallel()
	e, _, _ := copyEnv(t)
	held := tor("t1", hA, "A", 1, e.now, provider.StateDownloaded)
	held.Progress = 100
	e.tb.list = []provider.Torrent{held}
	e.rd.list = []provider.Torrent{tor("r1", hA, "A", 1, e.now, provider.StateError)}
	rep, err := e.report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := find(rep, "tb", "t1").AddTo; len(got) != 2 || got[0] != "rd" || got[1] != "ad" {
		t.Errorf("add_to %v", got)
	}
}

// The automatic copy after an import from AllDebrid: queued for TorBox only with also_add on, live cached evidence and
// no complete TorBox entry; never while paused. Its job skips an account at its torrent-limit warning share and, while
// paused, every provider.
func TestAutoCopy(t *testing.T) {
	t.Parallel()
	e, ad, _ := copyEnv(t)
	ctx := context.Background()
	e.m.Queue = jobs.NewQueue(e.st)
	e.pol.Router = &policy.Router{Enabled: true}
	queued := func() string {
		var p string
		_ = e.st.Reader().QueryRow(`SELECT COALESCE(group_concat(payload), '') FROM job WHERE kind = ? AND dedupe_key LIKE '%.auto/%'`, JobCopy).Scan(&p)
		e.exec(`DELETE FROM job`)
		return p
	}
	evidence := func(prov string, expires time.Time) {
		e.exec(`INSERT OR REPLACE INTO provider_cache (info_hash, provider, state, source, checked_at, expires_at)
			VALUES (?, ?, 'cached', 'dmm', 0, ?)`, hA, prov, store.Millis(expires))
	}
	// Switch off: nothing.
	evidence("tb", e.now.Add(time.Hour))
	if _, err := e.m.QueueAutoCopy(ctx, "ad", hA); err != nil || queued() != "" {
		t.Fatal("queued with the switch off")
	}
	e.pol.Providers["tb"].AlsoAdd = true
	e.pol.Providers["rd"].AlsoAdd = true // no evidence on rd
	if _, err := e.m.QueueAutoCopy(ctx, "ad", hA); err != nil {
		t.Fatal(err)
	}
	if got := queued(); got != `{"hash":"`+hA+`","by":"auto","auto":true,"providers":["tb"]}` {
		t.Fatalf("queued %q", got)
	}
	// Expired evidence, or TorBox already holding it: nothing.
	evidence("tb", e.now.Add(-time.Minute))
	if _, _ = e.m.QueueAutoCopy(ctx, "ad", hA); queued() != "" {
		t.Error("queued on expired evidence")
	}
	evidence("tb", e.now.Add(time.Hour))
	e.exec(`INSERT INTO provider_torrent (provider, torrent_id, info_hash, name, state, progress)
		VALUES ('tb', 't1', ?, 'A', 'downloaded', 100)`, hA)
	if _, _ = e.m.QueueAutoCopy(ctx, "ad", hA); queued() != "" {
		t.Error("queued for a provider that holds it")
	}
	// TorBox at 100% without its files present yet (download_present false: the record keeps it downloading) has it
	// on the account: no copy is queued there either.
	e.exec(`UPDATE provider_torrent SET state = 'downloading' WHERE provider = 'tb'`)
	if _, _ = e.m.QueueAutoCopy(ctx, "ad", hA); queued() != "" {
		t.Error("queued for TorBox holding it at 100% without its files")
	}
	e.exec(`DELETE FROM provider_torrent`)
	e.pol.Router.Enabled = false
	if _, _ = e.m.QueueAutoCopy(ctx, "ad", hA); queued() != "" {
		t.Error("queued while paused")
	}

	// The job: paused, every provider skipped; at the limit warning share, skipped there; nothing sent.
	if _, err := e.m.Copy(ctx, CopyPayload{Hash: hA, Auto: true, Providers: []string{"tb", "ad"}}); err != nil {
		t.Fatal(err)
	}
	if rows := e.copyRows(hA); rows["tb"].Detail != "paused" || rows["ad"].Detail != "paused" {
		t.Fatalf("paused: %+v", rows)
	}
	e.pol.Router.Enabled = true
	limit := 10
	e.pol.Providers["ad"].MagnetLimit = &limit
	for i := range 9 {
		ad.list = append(ad.list, tor(fmt.Sprintf("x%d", i), fmt.Sprintf("%040d", i), "X", 1, e.now, provider.StateDownloaded))
	}
	if _, err := e.m.Copy(ctx, CopyPayload{Hash: hA, Auto: true, Providers: []string{"ad"}}); err != nil {
		t.Fatal(err)
	}
	if r := e.copyRows(hA)["ad"]; r.Outcome != CopySkipped || r.Detail != "the account holds 9 of its 10 torrents" {
		t.Fatalf("limit: %+v", r)
	}
	if len(e.atb.added) != 0 {
		t.Fatal("an add was sent")
	}
}

// An entry already on Real-Debrid is handled before the name rule: complete, it is present (a record row), never
// "refused". A provider whose read is refused by the gateway's back-off with a time is skipped until then; one whose
// read fails without a time is failed with the read named, never with the add.
func TestCopyReadsBeforeNameRuleAndBusy(t *testing.T) {
	t.Parallel()
	e, ad, _ := copyEnv(t)
	ctx := context.Background()
	const name = "Movie.2020.1080p.WEB-DL.x264-GRP"
	e.exec(`INSERT INTO provider_torrent (provider, torrent_id, info_hash, name, state, progress)
		VALUES ('tb', 't1', ?, ?, 'downloaded', 100)`, hA, name)
	held := tor("r1", hA, name, 10, e.now, provider.StateDownloaded)
	held.Progress = 100
	e.rd.list = []provider.Torrent{held}
	retry := e.now.Add(30 * time.Minute)
	ad.listErr = fmt.Errorf("ad: %w", &gateway.Refused{Reason: "backoff", Target: "provider:ad", RetryAt: retry})
	e.tb.listErr = errors.New("connection reset")
	if _, err := e.m.Copy(ctx, CopyPayload{Hash: hA}); err != nil {
		t.Fatal(err)
	}
	st, err := e.m.CopyStatus(ctx, hA)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]CopyResult{}
	for _, r := range st.Results {
		got[r.Provider] = r
	}
	if r := got["rd"]; r.Outcome != CopyPresent || !e.recorded("rd", hA) {
		t.Errorf("rd %+v", r)
	}
	if r := got["ad"]; r.Outcome != CopySkipped || r.Until == nil || !r.Until.Equal(retry) {
		t.Errorf("ad %+v", r)
	}
	if r := got["tb"]; r.Outcome != CopyFailed || !strings.HasPrefix(r.Detail, copyReadFailed) {
		t.Errorf("tb %+v", r)
	}
	if len(e.ard.added) != 0 {
		t.Error("an add was sent")
	}
}
