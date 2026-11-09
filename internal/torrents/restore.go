package torrents

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/settings"
)

// Restore actions in a plan.
const (
	ActPresent = "present"      // the target already has the hash: nothing to do
	ActReadd   = "readd"        // it will be re-added
	ActInvalid = "skip_invalid" // not a v1 info hash
	ActNoTo    = "skip_target"  // the target provider is not configured or cannot be listed
	// ActResume: the target holds the torrent an earlier restore added, whose file selection did not finish: the
	// restore selects its files now.
	ActResume = "resume"
)

// Restore outcomes.
const (
	OutAdded    = "added"    // added, and the provider holds the content
	OutUncached = "uncached" // added, and the provider is downloading it (or refused it as not cached)
	OutFailed   = "failed"
	OutSkipped  = "skipped" // not attempted (the run stopped, or the plan changed since the confirmation)
	// OutIncomplete: added, but the provider still waits for the file selection; Error says whether
	// a restore again resumes it or the person selects on the provider's site. The job fails, naming it.
	OutIncomplete = "incomplete"
)

// rdWaitingFiles is Real-Debrid's status while it waits for a file selection.
const rdWaitingFiles = "waiting_files_selection"

// RestoreRequest names what to restore: a stored backup, optionally one provider's list of it and some hashes,
// restored to Target (default: the provider each list came from).
type RestoreRequest struct {
	BackupID string   `json:"backup_id"`
	Provider string   `json:"provider,omitempty"`
	Target   string   `json:"target,omitempty"`
	Hashes   []string `json:"hashes,omitempty"`
}

// RestoreItem is one torrent of a restore.
type RestoreItem struct {
	Target string `json:"target"`
	Source string `json:"source"`
	Hash   string `json:"hash"`
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes,omitempty"`
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
	// Files is the number of selected files the backup holds; Selection says how a provider that waits for a
	// selection (Real-Debrid) gets one (SelFiles, SelAll, SelUnknown).
	Files     int    `json:"files"`
	Selection string `json:"selection"`
	// Outcome, NewID and Error are set by the run.
	Outcome string `json:"outcome,omitempty"`
	NewID   string `json:"new_id,omitempty"`
	Error   string `json:"error,omitempty"`

	files  []BackupFile
	backup string
}

// RestorePlan is the preview of a restore. Token confirms exactly its re-adds.
type RestorePlan struct {
	BackupID string         `json:"backup_id"`
	Items    []RestoreItem  `json:"items"`
	Counts   map[string]int `json:"counts"`
	Readd    int            `json:"readd"`
	// Resume counts the torrents whose file selection the restore finishes (ActResume).
	Resume int    `json:"resume"`
	Bytes  int64  `json:"bytes"`
	Token  string `json:"confirmation_token"`

	// stale are pending selections of torrents the target's list no longer holds (Restore forgets them).
	stale []pendingKey
}

// PlanRestore previews req: for every torrent, whether it is already on the target, will be re-added, or is skipped.
// It reads the target providers' lists; it writes nothing. Every hash of the backup not on its target is re-added:
// the backup is the user's own debrid list.
func (m *Manager) PlanRestore(ctx context.Context, req RestoreRequest) (*RestorePlan, error) {
	b, err := m.ReadBackup(req.BackupID)
	if err != nil {
		return nil, err
	}
	if req.Target != "" && m.Policy.Providers[req.Target] == nil {
		return nil, fmt.Errorf("torrents: provider %q is not in the policy", req.Target)
	}
	want := map[string]bool{}
	for _, h := range req.Hashes {
		want[strings.ToLower(strings.TrimSpace(h))] = true
	}
	pending, err := m.pendingSelections(ctx)
	if err != nil {
		return nil, err
	}
	plan := &RestorePlan{BackupID: b.ID, Items: []RestoreItem{}, Counts: map[string]int{}}
	present := map[string]map[string]bool{}
	listed := map[pendingKey]string{} // target and torrent id -> hash
	listErr := map[string]error{}
	seen := map[string]bool{}
	for _, p := range b.Providers {
		if req.Provider != "" && p.Provider != req.Provider {
			continue
		}
		target := req.Target
		if target == "" {
			target = p.Provider
		}
		if _, done := present[target]; !done && listErr[target] == nil {
			present[target] = map[string]bool{}
			list, err := m.list(ctx, target)
			if err != nil {
				listErr[target] = err
			}
			for _, t := range list {
				present[target][strings.ToLower(t.Hash)] = true
				listed[pendingKey{target, t.ID}] = strings.ToLower(t.Hash)
			}
		}
		for _, t := range p.Torrents {
			if len(want) > 0 && !want[t.Hash] {
				continue
			}
			if seen[target+"\x00"+t.Hash] {
				continue
			}
			seen[target+"\x00"+t.Hash] = true
			it := RestoreItem{Target: target, Source: p.Provider, Hash: t.Hash, Name: t.Name, Bytes: t.Bytes,
				Files: len(t.Files), Selection: selectionOf(t), files: t.Files, backup: b.ID}
			h := indexer.NormalizeHash(t.Hash)
			switch {
			case h == "":
				it.Action, it.Reason = ActInvalid, "not a v1 info hash"
			case listErr[target] != nil:
				it.Action, it.Reason = ActNoTo, listErr[target].Error()
			case present[target][h]:
				it.Action = ActPresent
				if p := pending.find(target, h); p != nil && listed[p.key] == h {
					it.Action, it.NewID, it.Selection, it.files = ActResume, p.key.id, p.selection, p.files
				}
			default:
				it.Action = ActReadd
			}
			plan.Counts[it.Action]++
			if it.Action == ActReadd {
				plan.Readd++
				plan.Bytes += it.Bytes
			}
			if it.Action == ActResume {
				plan.Resume++
			}
			plan.Items = append(plan.Items, it)
		}
	}
	for _, p := range pending {
		if _, read := present[p.key.provider]; read && listErr[p.key.provider] == nil && listed[p.key] != p.hash {
			plan.stale = append(plan.stale, p.key)
		}
	}
	plan.Token = callerToken(ctx, "r2-", restoreToken(plan))
	return plan, nil
}

// tokenKey signs confirmation tokens. It is made once per process and never stored: after a restart, preview again.
var tokenKey = func() []byte {
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	return k
}()

// callerToken is the confirmation a preview hands out: an HMAC of the caller (settings.Confirmer: "user:<id>" for a
// session, "key:<id>" for an API key) and the plan digest, so only a preview gives it and only the same caller can
// use it. The CLI (no confirmer) previews and confirms in two processes as the local operator: it gets the digest.
func callerToken(ctx context.Context, prefix, digest string) string {
	c := settings.Confirmer(ctx)
	if c == "" && settings.MayConfirm(ctx) {
		return digest
	}
	mac := hmac.New(sha256.New, tokenKey)
	mac.Write([]byte(c + "\x00" + digest))
	return prefix + hex.EncodeToString(mac.Sum(nil)[:16])
}

// tokenMatches reports whether token is the plan's confirmation.
func tokenMatches(token, want string) bool {
	return token != "" && hmac.Equal([]byte(token), []byte(want))
}

// restoreToken is the plan digest of the plan's re-adds (callerToken binds it to the caller).
func restoreToken(p *RestorePlan) string {
	var lines []string
	for _, it := range p.Items {
		if it.Action == ActReadd || it.Action == ActResume {
			lines = append(lines, it.Target+"|"+it.Hash)
		}
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return fmt.Sprintf("r1-%d-%s", len(lines), hex.EncodeToString(sum[:8]))
}

// ErrConfirmation: the confirmation token does not match the set as it is now.
var ErrConfirmation = errors.New("torrents: the confirmation does not match; preview again")

// CheckRestore plans req and checks token against it: what an API call does before it queues the restore job.
func (m *Manager) CheckRestore(ctx context.Context, req RestoreRequest, token string) (*RestorePlan, error) {
	plan, err := m.PlanRestore(ctx, req)
	if err != nil {
		return nil, err
	}
	if !tokenMatches(token, plan.Token) {
		return plan, ErrConfirmation
	}
	return plan, nil
}

// restoreWait bounds one wait for a provider's pacing; a longer one ends the provider's part of the run.
const restoreWait = 15 * time.Minute

// Restore re-adds the torrents of req that were confirmed (token) and are still to be re-added: the plan is made
// again, and only items in both the confirmed set and the new plan are added. Adds are paced by the router's
// pacing rows (shared with the router) and the gateway budgets; a provider that refuses for its account or rate
// stops its part of the run. Each add is journaled.
func (m *Manager) Restore(ctx context.Context, req RestoreRequest, confirmed []string, by string) (*RestorePlan,
	error) {
	opMu.Lock()
	defer opMu.Unlock()
	plan, err := m.PlanRestore(ctx, req)
	if err != nil {
		return nil, err
	}
	ok := map[string]bool{}
	for _, c := range confirmed {
		ok[c] = true
	}
	m.forgetPending(ctx, plan.stale)
	trackers := m.Policy.RouterSettings().Trackers
	stopped := map[string]string{}
	for i := range plan.Items {
		it := &plan.Items[i]
		if it.Action != ActReadd && it.Action != ActResume {
			continue
		}
		if !ok[it.Target+"|"+it.Hash] {
			it.Outcome, it.Error = OutSkipped, "not in the confirmed set"
			continue
		}
		if why := stopped[it.Target]; why != "" {
			it.Outcome, it.Error = OutSkipped, why
			continue
		}
		if err := ctx.Err(); err != nil {
			it.Outcome, it.Error = OutSkipped, err.Error()
			continue
		}
		je := JournalEntry{Action: "restore", Provider: it.Target, Hash: it.Hash, Name: it.Name, Bytes: it.Bytes,
			BackupID: plan.BackupID, By: by}
		if it.Action == ActResume {
			m.resumeOne(ctx, it)
		} else {
			m.restoreOne(ctx, it, trackers, stopped, &je)
		}
		je.TorrentID, je.Outcome, je.Detail = it.NewID, it.Outcome, it.Error
		m.journal(ctx, je)
	}
	return plan, nil
}

// restoreOne re-adds it. je is its journal row: written as "sending" just before the add (je.ID set), and completed
// by the caller. A backup's known selection that comes back uncached on Real-Debrid may be added once more with
// every playable file or the known set's playable part (retryPlayable), unless it is already a subset of that set; the first add's row is completed
// here, and je is the second's.
func (m *Manager) restoreOne(ctx context.Context, it *RestoreItem, trackers []string, stopped map[string]string,
	je *JournalEntry) {
	for retried := false; ; retried = true {
		added, t := m.restoreAdd(ctx, it, trackers, stopped, je, !retried)
		if added == nil || !m.retryPlayable(ctx, it, added, t, stopped, je) {
			return
		}
	}
}

// restoreAdd is one add of it and its file selection. When may (the first add) and the add is the restore's own
// uncached Real-Debrid add of a known selection (mayRetry), the torrent is read once more after the router's
// cached_grace; still not downloaded, it returns the add and that read, for retryPlayable.
func (m *Manager) restoreAdd(ctx context.Context, it *RestoreItem, trackers []string, stopped map[string]string,
	je *JournalEntry, may bool) (*adder.Added, *provider.Torrent) {
	ad := m.Adders[it.Target]
	pv := m.Policy.Providers[it.Target]
	if ad == nil || pv == nil {
		it.Outcome, it.Error = OutFailed, ErrNoClient.Error()
		stopped[it.Target] = it.Error
		return nil, nil
	}
	mag, err := adder.Restore(it.Hash, trackers)
	if err != nil {
		it.Outcome, it.Error = OutFailed, err.Error()
		return nil, nil
	}
	added, err := m.sendRestore(ctx, it, pv.EffectivePacing(), ad, mag, je)
	var paced *pacedError
	switch {
	case errors.As(err, &paced):
		it.Outcome, it.Error = paced.outcome, paced.Error()
		if paced.stop {
			stopped[it.Target] = paced.stopWhy
		}
		return nil, nil
	case errors.Is(err, errNotAnnounced):
		it.Outcome, it.Error = OutFailed, err.Error()
		return nil, nil
	}
	if err != nil {
		// A transport error means the provider is down, as the router classifies it (an outage blocks reuse there).
		o := router.AddOutcome{Reason: "error", Down: ctx.Err() == nil}
		var e *adder.Error
		if errors.As(err, &e) {
			o.Reason, o.RetryAt = string(e.Code), e.RetryAt
			o.Down = router.ProviderDown(e)
			switch e.Code {
			case adder.CodeNotCached:
				o = router.AddOutcome{OK: true, Uncached: true}
			case adder.CodeBlocked, adder.CodeInvalid:
				o = router.AddOutcome{OK: true}
			case adder.CodeTrafficExceeded, adder.CodeAuth:
				o.ToMax = true
				stopped[it.Target] = err.Error()
			case adder.CodeAccountHold:
				// A 451 fails this torrent; a hold it starts (451s on different hashes) paces the next restore.
				o.Hold, o.Hash = true, it.Hash
			case adder.CodeTooManyActive, adder.CodeRateLimited:
				stopped[it.Target] = err.Error()
			}
		}
		_ = router.RecordAddOutcome(ctx, m.Store, it.Target, pv.EffectivePacing(), m.now(), o)
		it.Outcome, it.Error = OutFailed, err.Error()
		if o.Uncached {
			it.Outcome = OutUncached
		}
		return nil, nil
	}
	it.NewID = added.ID
	cached := added.Ready
	incomplete := false
	if ad.SelectsFiles() {
		var state provider.State
		state, incomplete = m.selectNew(ctx, ad, it)
		cached = cached || state == provider.StateDownloaded
	}
	var again *provider.Torrent
	if !cached && !incomplete && may && m.mayRetry(it) {
		// A cached set can take a moment to show downloaded: one more read before calling it uncached.
		if err := m.sleep(ctx, m.Policy.RouterSettings().CachedGrace); err == nil {
			if t, err := m.Providers[it.Target].Torrent(ctx, it.NewID); err == nil {
				cached, again = t.State == provider.StateDownloaded, t
			}
		}
	}
	_ = router.RecordAddOutcome(ctx, m.Store, it.Target, pv.EffectivePacing(), m.now(),
		router.AddOutcome{OK: true, Uncached: !cached})
	switch {
	case incomplete:
		it.Outcome = OutIncomplete
	case cached:
		it.Outcome = OutAdded
	default:
		it.Outcome = OutUncached
		if again != nil {
			return added, again
		}
	}
	return nil, nil
}

// OutSending is a restore journal row's outcome while its add is sent: the router reads it (router.RestoredSince).
const OutSending = "sending"

// errNotAnnounced: the restore's journal row could not be written, so nothing was sent.
var errNotAnnounced = errors.New("the restore could not be journaled before its add; nothing was sent")

// pacedError: the restore's add was not sent for the provider's pacing (or the run's end while it waited).
type pacedError struct {
	outcome string
	msg     string
	stop    bool   // the provider's part of the run ends
	stopWhy string // what the rest of its items say
}

func (e *pacedError) Error() string { return e.msg }

// sendRestore takes the add's pacing slot and sends it. The add is announced first (je written as sending) and sent
// under the router's add lock: on AllDebrid an upload of a hash already on the account returns the
// existing magnet, so a restore landing between a route's look at the account and its add would look like the
// route's own new add, and with router.alldebrid_delete_uncached the route would delete it as uncached. The lock
// keeps the two apart in this process; the row tells a route of any process (router.RestoredSince) that the entry
// may be the restore's.
//
// The pacing slot is reserved under the same lock, just before the add: a slot reserved before
// waiting for the lock would be stale by the time the add goes, after a route's own add (min_gap) or a hold or
// back-off it started. While the provider is paced the lock is released, and the slot is asked again once it is
// held once more.
func (m *Manager) sendRestore(ctx context.Context, it *RestoreItem, pc policy.Pacing, ad adder.Adder,
	mag adder.Magnet, je *JournalEntry) (*adder.Added, error) {
	lock := m.AddLock
	if lock == nil {
		lock = noLock{}
	}
	for {
		lock.Lock()
		until, why, err := router.ReserveAdd(ctx, m.Store, it.Target, pc, m.now())
		if err != nil {
			lock.Unlock()
			return nil, &pacedError{outcome: OutFailed, msg: err.Error(), stop: true, stopWhy: "pacing: " + err.Error()}
		}
		if until.IsZero() {
			break
		}
		lock.Unlock()
		wait := until.Sub(m.now())
		if wait > restoreWait {
			msg := fmt.Sprintf("%s is paced until %s (%s)", it.Target, until.UTC().Format(time.RFC3339), why)
			return nil, &pacedError{outcome: OutSkipped, msg: msg, stop: true, stopWhy: msg}
		}
		if err := m.sleep(ctx, max(wait, time.Second)); err != nil {
			return nil, &pacedError{outcome: OutSkipped, msg: err.Error()}
		}
	}
	defer lock.Unlock()
	e := *je
	e.Outcome = OutSending
	id, err := m.journalRow(ctx, e)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errNotAnnounced, err)
	}
	je.ID = id
	return ad.AddMagnet(ctx, mag, adder.AddOptions{})
}

type noLock struct{}

func (noLock) Lock()   {}
func (noLock) Unlock() {}

// errNoMatch: none of the backup's selected files is among the new torrent's files.
var errNoMatch = errors.New("none of the backup's selected files is in the torrent")

// errNoPlayable: the backup never read the files (SelAll) and none of the torrent's files is playable (a video, and
// the audio beside it: router.Playable). Nothing is selected, as for errNoMatch: selecting the nfo, text or images
// can make Real-Debrid pack the torrent into a RAR that cannot be streamed.
var errNoPlayable = errors.New("none of the torrent's files is playable")

// selectRestored selects the backup's files on a provider that waits for a selection (Real-Debrid): the files the
// backup names (by path), or every playable file when the backup never read them (SelAll: router.Playable, never
// the nfo, text or images). An unknown selection is never taken as every file: the caller leaves it
// to the person.
func (m *Manager) selectRestored(ctx context.Context, ad adder.Adder, it *RestoreItem) (provider.State, error) {
	p := m.Providers[it.Target]
	if p == nil {
		return "", ErrNoClient
	}
	var t *provider.Torrent
	for try := 0; ; try++ {
		got, err := p.Torrent(ctx, it.NewID)
		if err != nil {
			return "", err
		}
		t = got
		if t.Status == rdWaitingFiles && len(t.Files) > 0 {
			break
		}
		if !awaitingSelection(t.Status) {
			return t.State, nil // already selected (cached content can skip the wait)
		}
		if try >= 5 {
			return t.State, errors.New("the provider did not list the files in time")
		}
		if err := m.sleep(ctx, 2*time.Second); err != nil {
			return "", err
		}
	}
	ids := pickFiles(t.Files, it.files, it.Selection == SelAll)
	if len(ids) == 0 && it.Selection == SelAll {
		return t.State, errNoPlayable
	}
	if len(ids) == 0 {
		return t.State, errNoMatch
	}
	if err := ad.SelectFiles(ctx, it.NewID, ids); err != nil {
		return "", err
	}
	if got, err := p.Torrent(ctx, it.NewID); err == nil {
		return got.State, nil
	}
	return "", nil
}

// pickFiles maps the backup's selection onto the new torrent's files by path; every playable file when all (the
// backup never read the files): the router's selection (router.PlayableIDs), the set Real-Debrid most likely holds
// cached.
func pickFiles(files []provider.File, want []BackupFile, all bool) []string {
	clean := func(p string) string { return strings.TrimPrefix(p, "/") }
	var ids []string
	if all {
		return router.PlayableIDs(files)
	}
	byPath := map[string]string{}
	for _, f := range files {
		byPath[clean(f.Path)] = f.ID
	}
	seen := map[string]bool{}
	for _, w := range want {
		id, ok := byPath[clean(w.Path)]
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

// Confirmed lists the plan's re-adds and resumed selections as Restore takes them.
func (p *RestorePlan) Confirmed() []string {
	var out []string
	for _, it := range p.Items {
		if it.Action == ActReadd || it.Action == ActResume {
			out = append(out, it.Target+"|"+it.Hash)
		}
	}
	return out
}
