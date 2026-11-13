package router

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/provider"
)

// The fix round's rules for copies (copy.go "The rules"): ownership, the checks before every delete, Pause before
// every write, no stranded rows, routes and copies waiting for each other, "already there", the fresh limit, the
// uncached count, TorBox's ambiguous answers and every known name.

// seedChecking puts a copy in checking with own_add on pk, its torrent tid on the account in state st.
func (e *copyEnv) seedChecking(pk, tid string, st provider.State, owed bool) {
	e.t.Helper()
	e.pr[pk].set(tid, provider.Torrent{Hash: h("a"), State: st, Status: string(st)})
	msg := ""
	if owed {
		msg = copyDeletingPrefix + tid + " on " + pk
	}
	e.exec(`INSERT INTO provider_copy (info_hash, name, provider, torrent_id, own_add, state, message, created_at,
		updated_at) VALUES (?, 'Movie.2024.2160p.WEB-GRP', ?, ?, 1, 'checking', ?, 0, ?)`, h("a"), pk, tid, msg,
		e.clock.UnixMilli())
	if _, err := e.r.ResumeCopies(e.ctx); err != nil {
		e.t.Fatal(err)
	}
}

// A: Real-Debrid lists the account before the add too; a failed listing sends nothing and the row waits.
func TestCopyRuleListingBeforeAdd(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloaded, "rd")
	e.pr["rd"].listErr = errors.New("503")
	e.queue(h("a"), "Movie.2024.2160p.WEB-GRP")
	e.run()
	if s, _ := e.copyState(h("a"), "rd"); s != CopyQueued || len(e.ad["rd"].adds) != 0 || e.pr["rd"].listReads == 0 {
		t.Fatalf("failed RD listing: %s, adds %v, list reads %d", s, e.ad["rd"].adds, e.pr["rd"].listReads)
	}
}

// A: AllDebrid hands back an id that was already on the account (a list entry without a hash): not ours, never
// deleted. A registry row showing the hash present on the provider: not ours either; an absent one (an old torrent
// since removed) does not count.
func TestCopyRuleOwnershipFromTheListing(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloading, "ad")
	e.pr["ad"].list = []provider.Torrent{{ID: "ad-" + h("a")[:4]}}
	e.queue(h("a"), "Movie.2024.2160p.WEB-GRP")
	e.run()
	if s, _ := e.copyState(h("a"), "ad"); s != CopyNotCached || e.del["ad"].deleted() != 0 {
		t.Fatalf("listed id: %s, %d deletes", s, e.del["ad"].deleted())
	}
	e2 := newCopyEnv(t, provider.StateDownloading, "rd")
	e2.exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (7, ?, 'present', 0, 0)`, h("b"))
	e2.exec(`INSERT INTO content_provider (content_id, provider, torrent_id, seen_at, state, progress)
		VALUES (7, 'rd', 'OLD', 0, 'absent', 0)`)
	e2.exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES (8, ?, 'present', 0, 0)`, h("c"))
	e2.exec(`INSERT INTO content_provider (content_id, provider, torrent_id, seen_at, state, progress)
		VALUES (8, 'rd', 'HELD', 0, 'present', 40)`)
	e2.queue(h("b"), "Movie.2024.2160p.WEB-GRP")
	e2.queue(h("c"), "Movie.2024.2160p.WEB-GRP")
	e2.run()
	if s, _ := e2.copyState(h("b"), "rd"); s != CopyNotCached || e2.del["rd"].deleted() != 1 {
		t.Fatalf("absent registry row: %s, %d deletes", s, e2.del["rd"].deleted())
	}
	if s, msg := e2.copyState(h("c"), "rd"); s != CopyNotCached || e2.del["rd"].deleted() != 1 ||
		!strings.Contains(msg, "left on the account") {
		t.Fatalf("present registry row: %s %q, %d deletes", s, msg, e2.del["rd"].deleted())
	}
}

// B: before every delete, owed or not, the torrent is read again: completed meanwhile -> added, Cached on written;
// another hash or in use -> left in place.
func TestCopyRuleChecksBeforeEveryDelete(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloaded, "rd")
	e.seedChecking("rd", "rd-1", provider.StateDownloaded, true)
	e.run()
	if s, _ := e.copyState(h("a"), "rd"); s != CopyAdded || e.del["rd"].deleted() != 0 ||
		e.count(`SELECT COUNT(*) FROM content_provider WHERE provider = 'rd' AND progress = 100`) != 1 {
		t.Fatalf("completed meanwhile: %s, %d deletes", s, e.del["rd"].deleted())
	}
	e2 := newCopyEnv(t, provider.StateDownloading, "rd")
	e2.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		provider_torrent_id, state, created_at, updated_at) VALUES (5, 1, 10, 'movie', ?, 'x', 'pub', 'rd', 'rd-1',
		'downloading', 0, 0)`, h("a"))
	e2.seedChecking("rd", "rd-1", provider.StateDownloading, true)
	e2.run()
	if s, msg := e2.copyState(h("a"), "rd"); s != CopyFailed || e2.del["rd"].deleted() != 0 || msg == "" {
		t.Fatalf("in use: %s %q, %d deletes", s, msg, e2.del["rd"].deleted())
	}
	e3 := newCopyEnv(t, provider.StateDownloading, "rd")
	e3.seedChecking("rd", "rd-1", provider.StateDownloading, true)
	e3.pr["rd"].set("rd-1", provider.Torrent{Hash: h("z"), State: provider.StateDownloading})
	e3.run()
	if s, _ := e3.copyState(h("a"), "rd"); s != CopyFailed || e3.del["rd"].deleted() != 0 {
		t.Fatalf("other hash: %s, %d deletes", s, e3.del["rd"].deleted())
	}
}

// C: a Pause that lands during the settle grace stops the delete; the row stays unfinished, never skipped. A Pause
// that lands during the DMM read stops the add.
func TestCopyRulePauseBeforeEveryWrite(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloading, "rd")
	e.r.sleep = func(context.Context, time.Duration) error { e.r.set.Enabled = false; return nil }
	e.queue(h("a"), "Movie.2024.2160p.WEB-GRP")
	e.run()
	if s, _ := e.copyState(h("a"), "rd"); s != CopyChecking || e.del["rd"].deleted() != 0 {
		t.Fatalf("paused in the grace: %s, %d deletes", s, e.del["rd"].deleted())
	}
	e2 := newCopyEnv(t, provider.StateDownloaded, "rd")
	e2.r.availHash = func(context.Context, string, string) (bool, bool, error) {
		e2.r.set.Enabled = false
		return false, true, nil
	}
	e2.queue(h("a"), "Movie.2024.2160p.WEB-GRP")
	e2.run()
	if len(e2.ad["rd"].adds) != 0 {
		t.Fatalf("added after Pause: %v", e2.ad["rd"].adds)
	}
}

// D: a settle that cannot read the torrent hands the job back with a growing wait; it never uses up the job's
// attempts. A new request for an unfinished row without a job queues one.
func TestCopyRuleNeverStrands(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloading, "rd")
	e.pr["rd"].readErr = map[string]error{"rd-1": errors.New("503")}
	e.pr["rd"].listErr = errors.New("503")
	e.seedChecking("rd", "rd-1", provider.StateDownloading, false)
	for range 7 {
		e.run()
		e.clock = e.clock.Add(time.Hour)
	}
	if n := e.count(`SELECT COUNT(*) FROM job WHERE kind = ? AND state = 'queued' AND attempts = 0`, JobCopy); n != 1 {
		t.Fatalf("job: %d queued without attempts", n)
	}
	e2 := newCopyEnv(t, provider.StateDownloaded, "rd")
	e2.exec(`INSERT INTO provider_copy (info_hash, name, provider, state, created_at, updated_at)
		VALUES (?, 'x', 'rd', 'queued', 0, 0)`, h("a"))
	e2.queue(h("a"), "Movie.2024.2160p.WEB-GRP")
	if n := e2.count(`SELECT COUNT(*) FROM job WHERE kind = ?`, JobCopy); n != 1 {
		t.Fatalf("stranded row: %d jobs", n)
	}
}

// E: a route defers while a copy of the hash to that provider is unfinished; a copy waits while a route's add of the
// hash there is unsettled.
func TestCopyRuleRouteAndCopyWait(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloaded, "rd")
	e.pol.Versions["movie"].Providers = []string{"rd"}
	e.r.adders["rd"] = e.del["rd"]
	e.exec(`INSERT INTO provider_copy (info_hash, name, provider, torrent_id, own_add, state, created_at, updated_at)
		VALUES (?, 'x', 'rd', 'rd-9', 1, 'checking', 0, 0)`, h("a"))
	res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil || len(e.ad["rd"].adds) != 0 {
		t.Fatalf("route beside a copy: %+v %v, adds %v", res, err, e.ad["rd"].adds)
	}
	e2 := newCopyEnv(t, provider.StateDownloaded, "rd")
	e2.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		state, created_at, updated_at) VALUES (5, 1, 10, 'movie', ?, 'x', 'pub', 'rd', 'adding', 0, 0)`, h("a"))
	e2.queue(h("a"), "Movie.2024.2160p.WEB-GRP")
	e2.run()
	if s, _ := e2.copyState(h("a"), "rd"); s != CopyQueued || len(e2.ad["rd"].adds) != 0 {
		t.Fatalf("copy beside an unsettled add: %s, adds %v", s, e2.ad["rd"].adds)
	}
}

// F, G: an entry already on the account still downloading is reported with its progress and nothing is written as
// complete; an account near its limit by the fresh listing is skipped.
func TestCopyRuleAlreadyThereAndLimit(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloaded, "ad")
	e.pr["ad"].list = []provider.Torrent{{ID: "77", Hash: h("a"), State: provider.StateDownloading, Progress: 12}}
	e.queue(h("a"), "Movie.2024.2160p.WEB-GRP")
	e.run()
	if s, msg := e.copyState(h("a"), "ad"); s != CopyAdded || msg != "Already on AD, still downloading (12%)" ||
		e.count(`SELECT COUNT(*) FROM content_provider WHERE progress >= 100`) != 0 {
		t.Fatalf("downloading: %s %q", s, msg)
	}
	e2 := newCopyEnv(t, provider.StateDownloaded, "ad")
	limit := 10
	e2.pol.Providers["ad"].MagnetLimit = &limit
	for i := range 9 {
		e2.pr["ad"].list = append(e2.pr["ad"].list, provider.Torrent{ID: string(rune('a' + i)), Hash: h(string(rune('0' + i)))})
	}
	e2.queue(h("a"), "Movie.2024.2160p.WEB-GRP")
	e2.run()
	if s, _ := e2.copyState(h("a"), "ad"); s != CopySkipped || len(e2.ad["ad"].adds) != 0 {
		t.Fatalf("near the limit: %s, adds %v", s, e2.ad["ad"].adds)
	}
}

// H, I: the uncached verdict counts once, before the delete, however the delete goes; TorBox answering a missing id
// with an error is settled by a fresh listing that shows neither the id nor the hash.
func TestCopyRuleUncachedCountAndTorBoxAnswer(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloading, "rd")
	e.del["rd"].fail = 5
	e.del["rd"].gone = func(string) {}
	e.queue(h("a"), "Movie.2024.2160p.WEB-GRP")
	e.run()
	e.clock = e.clock.Add(time.Hour)
	e.run()
	if p, _ := loadPace(e.ctx, e.st.Writer(), "rd"); p.HourUncached != 1 {
		t.Fatalf("uncached counted %d times", p.HourUncached)
	}
	e2 := newCopyEnv(t, provider.StateDownloading, "tb")
	e2.seedChecking("tb", "tb-1", provider.StateDownloading, true)
	// TorBox's read lists the account itself after its 500 and answers not found.
	e2.pr["tb"].readErr = map[string]error{"tb-1": provider.ErrNotFound}
	e2.run()
	if s, _ := e2.copyState(h("a"), "tb"); s != CopyNotCached || e2.del["tb"].deleted() != 0 {
		t.Fatalf("TorBox 500 for a deleted id: %s, %d deletes", s, e2.del["tb"].deleted())
	}
}

// J1: Real-Debrid's name rule applies to every known name of the torrent, not only the stored display name.
func TestCopyRuleEveryKnownName(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloaded, "rd")
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		state, created_at, updated_at) VALUES (5, 1, 10, 'movie', ?, 'Movie.2024.1080p.WEB.x264-YIFY', 'pub', 'ad',
		'imported', 0, 0)`, h("a"))
	e.queue(h("a"), "Movie 2024")
	e.run()
	if s, _ := e.copyState(h("a"), "rd"); s != CopyRefused || len(e.ad["rd"].adds) != 0 {
		t.Fatalf("recorded release name: %s, adds %v", s, e.ad["rd"].adds)
	}
}

// L: a hold's end travels as a time, not as words in the message.
func TestCopyRuleHoldTime(t *testing.T) {
	t.Parallel()
	e := newCopyEnv(t, provider.StateDownloaded, "rd")
	until := e.clock.Add(15 * time.Minute)
	e.exec(`INSERT INTO router_pacing (provider, hold_until, cooldown_until, updated_at) VALUES ('rd', ?, ?, 0)`,
		until.UnixMilli(), until.UnixMilli())
	e.queue(h("a"), "Movie.2024.2160p.WEB-GRP")
	e.run()
	v, err := CopiesByHash(e.ctx, e.st.Reader(), []string{h("a")})
	if err != nil || len(v[h("a")]) != 1 || v[h("a")][0].Until == nil || !v[h("a")][0].Until.Equal(until) ||
		v[h("a")][0].Message != "Skipped: Real-Debrid is holding adds" {
		t.Fatalf("%+v %v", v, err)
	}
}
