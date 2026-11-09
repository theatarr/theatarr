package router

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/store"
)

// JobRoute routes a Request (payload) from the job queue. A deferred route (every provider paced or backing off, or
// an add whose outcome is unknown) records the route job that resumes its download when a provider is free in the
// same transaction as the download's state (continuation), whoever called Route.
const JobRoute = "router.route"

// maxDeferrals bounds how often one route waits for providers after sending them an add before its download fails;
// a wait for pacing that asked nothing is not counted. maxDeferWait bounds the whole wait, counted or not.
const (
	maxDeferrals = 100
	maxDeferWait = 48 * time.Hour
)

// routePayload is a Request plus how often it was deferred after an add, and since when it waits (Unix ms; 0 for a
// route not deferred yet or queued by a build before it).
type routePayload struct {
	Request
	Deferrals    int   `json:"deferrals,omitempty"`
	WaitingSince int64 `json:"waiting_since,omitempty"`
}

// giveUp says why the route's download ends instead of waiting again ("" when it waits).
func (s *routeState) giveUp(now time.Time) string {
	if s.sent && s.req.deferrals+1 >= maxDeferrals {
		return fmt.Sprintf("waited for a provider after %d adds; giving up", maxDeferrals)
	}
	if !s.req.waitingSince.IsZero() && now.Sub(s.req.waitingSince) >= maxDeferWait {
		return fmt.Sprintf("waited for a provider for %s; giving up", maxDeferWait)
	}
	return ""
}

// deferSlot staggers the retry of a route deferred on provider pk until at: each deferred route gets the next free
// slot, one add spacing after the last handed out, so the routes waiting on a paced provider wake one by one instead
// of all at the instant it frees up. Under r.mu. The slot is only taken by calling take, once the route's
// continuation is recorded: a route that ends instead (giving up, its download moved on) leaves no phantom slot that
// would push the next deferred route back.
func (r *Router) deferSlot(pk string, at, now time.Time) (slot time.Time, take func()) {
	pv := r.pol.Providers[pk]
	if pv == nil {
		return at, func() {}
	}
	pc := pv.EffectivePacing()
	gap := pc.Gap()
	if gap <= 0 && pc.AddsPerMinute > 0 {
		gap = time.Minute / time.Duration(pc.AddsPerMinute)
	}
	if gap <= 0 {
		gap = 2 * time.Second
	}
	if last := r.shared.slots[pk]; last.After(now) && last.Add(gap).After(at) {
		at = last.Add(gap)
	}
	return at, func() {
		if r.shared.slots == nil {
			r.shared.slots = map[string]time.Time{}
		}
		if at.After(r.shared.slots[pk]) {
			r.shared.slots[pk] = at
		}
	}
}

// RegisterJobs adds the router's job handler to w; q is used to re-enqueue deferred routes.
func (r *Router) RegisterJobs(w *jobs.Worker, q *jobs.Queue) {
	r.q = q
	w.Handle(JobRoute, r.handleRoute)
}

// RegisterRuntimeJobs dispatches each queued route to the runtime captured when the job starts.
func RegisterRuntimeJobs(w *jobs.Worker, q *jobs.Queue,
	resolve func(context.Context) (context.Context, *Router)) {
	w.Handle(JobRoute, func(ctx context.Context, j *jobs.Job) error {
		var p routePayload
		if err := j.Decode(&p); err != nil {
			return jobs.Permanent(fmt.Errorf("router: bad payload: %w", err))
		}
		ctx, active := resolve(ctx)
		if active == nil {
			return fmt.Errorf("router: active settings runtime is unavailable")
		}
		if active.q != q {
			return fmt.Errorf("router: active router is not bound to the route queue")
		}
		return active.handleRoutePayload(ctx, j, p)
	})
}

// Enqueue queues a route to run at at (now when zero).
func (r *Router) Enqueue(ctx context.Context, req Request, at time.Time) (int64, error) {
	if r.q == nil {
		return 0, fmt.Errorf("router: no job queue")
	}
	req.Candidates = slices.Clone(req.Candidates)
	r.stampFresh(&req)
	return r.enqueue(ctx, routePayload{Request: req}, at)
}

// EnqueueTx is Enqueue inside the caller's write transaction, so the caller's state change and the route commit
// together.
func (r *Router) EnqueueTx(ctx context.Context, tx *sql.Tx, req Request, at time.Time) (int64, error) {
	if r.q == nil {
		return 0, fmt.Errorf("router: no job queue")
	}
	if req.DecisionRev == "" {
		req.DecisionRev = r.pol.Revision
	}
	req.Candidates = slices.Clone(req.Candidates)
	r.stampFresh(&req)
	id, _, err := enqueueRoute(ctx, r.q, tx, routePayload{Request: req}, at)
	return id, err
}

func (r *Router) enqueue(ctx context.Context, p routePayload, at time.Time) (int64, error) {
	if p.DecisionRev == "" {
		p.DecisionRev = r.pol.Revision
	}
	id, _, err := enqueueRoute(ctx, r.q, nil, p, at)
	return id, err
}

// enqueueRoute is the one way a route job is queued (in tx when given). Release links never reach the payload, so
// each candidate's hash is first taken from its normalised identity (reported hash, magnet, magnet link), as the
// synchronous route does. A candidate whose reported hash conflicts with its magnet is not queued: what it describes
// is not known to be the torrent its magnet adds.
func enqueueRoute(ctx context.Context, q *jobs.Queue, tx *sql.Tx, p routePayload, at time.Time) (int64, bool, error) {
	if len(p.Candidates) > 0 {
		kept, dropped := identityCandidates(p.Candidates)
		for _, c := range dropped {
			slog.Default().Warn("router: candidate not queued: conflicting or unverifiable torrent identity",
				"title_id", p.TitleID, "version", p.Version, "release", c.Release.Title, "info_hash", c.Release.InfoHash)
		}
		if len(kept) == 0 {
			return 0, false, fmt.Errorf("router: no candidate queued: every torrent identity is conflicting or unverifiable")
		}
		p.Candidates = kept
	}
	opts := routeOptions(p, at)
	if tx == nil {
		return q.Enqueue(ctx, JobRoute, p, opts)
	}
	return q.EnqueueTx(ctx, tx, JobRoute, p, opts)
}

// identityCandidates splits candidates by release identity: kept ones carry their normalised hash (the reported
// hash, else their magnet's or magnet link's); dropped ones report a hash their magnet contradicts.
func identityCandidates(cands []Candidate) (kept, dropped []Candidate) {
	for _, c := range cands {
		hash, conflict := indexer.ReleaseIdentity(c.Release)
		if conflict {
			dropped = append(dropped, c)
			continue
		}
		c.Release.InfoHash = hash
		kept = append(kept, c)
	}
	return kept, dropped
}

// routeOptions: a route that resumes a download is deduplicated by the download and its run time. A new RSS route is
// deduplicated by the whole request (target, version, candidates with their episodes, policy revision): two RSS reads
// before the worker opens a download queue one route, while other episode coverage or changed candidates queue
// their own.
func routeOptions(p routePayload, at time.Time) jobs.EnqueueOptions {
	key := ""
	switch {
	case p.DownloadID != 0:
		key = routeJobKey(p.DownloadID) + strconv.FormatInt(store.Millis(at), 10)
	case p.Source == "rss":
		if b, err := json.Marshal(p.Request); err == nil {
			sum := sha256.Sum256(b)
			key = JobRoute + "/rss/" + hex.EncodeToString(sum[:])
		}
	}
	return jobs.EnqueueOptions{RunAt: at, DedupeKey: key, MaxAttempts: 3}
}

// routeJobKey is the dedupe key prefix of the route jobs that resume download dl.
func routeJobKey(dl int64) string { return JobRoute + "/" + strconv.FormatInt(dl, 10) + "/" }

// strandedQueued reports whether download dl is queued with no route job queued or running to resume it: its route
// was interrupted after the row was queued and before the follow-up job was recorded (or the follow-up ran out of
// attempts). Such a row is resumed by the next route that reaches its hash.
func (r *Router) strandedQueued(ctx context.Context, dl int64) (bool, error) {
	key := routeJobKey(dl)
	var queued, pending int
	err := r.st.Writer().QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM download WHERE id = ? AND state = 'queued'),
		(SELECT COUNT(*) FROM job WHERE kind = ? AND state IN ('queued', 'running')
			AND substr(dedupe_key, 1, ?) = ?)`, dl, JobRoute, len(key), key).Scan(&queued, &pending)
	if err != nil {
		return false, fmt.Errorf("router: stranded check: %w", err)
	}
	return queued == 1 && pending == 0, nil
}

func (r *Router) handleRoute(ctx context.Context, j *jobs.Job) error {
	var p routePayload
	if err := j.Decode(&p); err != nil {
		return jobs.Permanent(fmt.Errorf("router: bad payload: %w", err))
	}
	return r.handleRoutePayload(ctx, j, p)
}

func (r *Router) handleRoutePayload(ctx context.Context, j *jobs.Job, p routePayload) error {
	if p.Source == "" {
		p.Source = "job"
	}
	p.Request.deferrals = p.Deferrals
	if p.WaitingSince > 0 {
		p.Request.waitingSince = store.FromMillis(p.WaitingSince)
	}
	res, err := r.routeStored(ctx, p.Request) // queued: its candidates keep the identity they were queued with
	if errors.Is(err, errProviderChanged) && res != nil {
		p.DownloadID = res.DownloadID
		_, err = r.enqueue(ctx, p, r.now())
		return err
	}
	if err != nil {
		return err
	}
	r.log.Info("route", "version", p.Version, "title", p.TitleID, "mode", res.Mode, "outcome", res.Outcome,
		"release", res.Release, "provider", res.Provider, "download", res.DownloadID, "retry_at", res.RetryAt)
	return nil
}

// continuation is the route job that resumes download dl at at, written in the transaction that leaves the download
// waiting: the route's request from candidate c on, one deferral more when this run sent an add. nil
// without a job queue (a CLI route; the poller's stray sweep resumes the download) and once the route gives up
// (giveUp), when the download fails instead.
func (r *Router) continuation(ctx context.Context, s *routeState, c *Candidate, dl int64,
	at time.Time) func(*sql.Tx) error {
	if r.q == nil || s.dry || s.giveUp(r.now()) != "" {
		return nil
	}
	next := routePayload{Request: s.req, Deferrals: s.req.deferrals}
	if s.sent {
		next.Deferrals++
	}
	since := s.req.waitingSince
	if since.IsZero() {
		since = r.now()
	}
	next.WaitingSince = store.Millis(since)
	next.DownloadID, next.deferrals, next.waitingSince = dl, 0, time.Time{}
	if next.DecisionRev == "" { // decided under the running policy: a later run under another revision decides again
		next.DecisionRev = r.pol.Revision
	}
	hash := indexer.NormalizeHash(c.Release.InfoHash)
	next.Candidates = []Candidate{*c}
	for i, x := range s.req.Candidates {
		if indexer.NormalizeHash(x.Release.InfoHash) == hash {
			next.Candidates = s.req.Candidates[i:]
			break
		}
	}
	return func(tx *sql.Tx) error {
		_, _, err := enqueueRoute(ctx, r.q, tx, next, at)
		return err
	}
}

// strayAfter is how long a queued download may sit with no route job to resume it before the poller resumes it.
const strayAfter = 10 * time.Minute

// pendingRouteSQL is true for a download row (download.id) with a route job queued or running to resume it.
const pendingRouteSQL = `EXISTS (SELECT 1 FROM job j WHERE j.kind = '` + JobRoute + `'
	AND j.state IN ('queued', 'running') AND substr(j.dedupe_key, 1, length('` + JobRoute + `/' || download.id || '/'))
	= '` + JobRoute + `/' || download.id || '/')`

// resumeStrays finds queued downloads no route job will resume (a route that died between the row and
// its continuation, a build before they were written together, a CLI route that deferred) and queues a route for
// each from its row. One whose route job failed since it was queued is ended with that job's error instead, so a
// failing route is not retried forever. Without a job queue (a CLI poll) it does nothing.
func (r *Router) resumeStrays(ctx context.Context, now time.Time) (resumed, failed int, err error) {
	if r.q == nil {
		return 0, 0, nil
	}
	rows, err := r.st.Writer().QueryContext(ctx, `SELECT id, updated_at FROM download WHERE state = 'queued'
		AND updated_at < ? AND NOT `+pendingRouteSQL+` ORDER BY id LIMIT 100`, store.Millis(now.Add(-strayAfter)))
	if err != nil {
		return 0, 0, fmt.Errorf("router: stray queued downloads: %w", err)
	}
	type stray struct{ id, at int64 }
	var strays []stray
	for rows.Next() {
		var x stray
		if err := rows.Scan(&x.id, &x.at); err != nil {
			rows.Close()
			return 0, 0, err
		}
		strays = append(strays, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	for _, x := range strays {
		key := routeJobKey(x.id)
		var lastErr string
		err := r.st.Writer().QueryRowContext(ctx, `SELECT COALESCE(last_error, '') FROM job WHERE kind = ?
			AND state = 'failed' AND substr(dedupe_key, 1, ?) = ? AND finished_at >= ? ORDER BY id DESC LIMIT 1`,
			JobRoute, len(key), key, x.at).Scan(&lastErr)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return resumed, failed, fmt.Errorf("router: download %d: %w", x.id, err)
		}
		if err == nil {
			if err := r.move(ctx, x.id, transition{from: []string{StateQueued},
				set: `state = 'failed', error = ?, updated_at = ?`, args: []any{"its route job failed: " + lastErr,
					store.Millis(now)}}); err != nil && !errors.Is(err, errAddTaken) {
				return resumed, failed, err
			}
			r.log.Warn("poll: queued download ended: its route job failed", "download", x.id, "err", lastErr)
			failed++
			continue
		}
		req, err := r.requestOf(ctx, x.id)
		if err != nil || req == nil {
			return resumed, failed, err
		}
		p := routePayload{Request: *req}
		if p.Source == "" {
			p.Source = "job"
		}
		err = r.move(ctx, x.id, transition{from: []string{StateQueued}, set: `updated_at = ?`,
			args: []any{store.Millis(now)}, after: func(tx *sql.Tx) error {
				_, _, err := enqueueRoute(ctx, r.q, tx, p, now)
				return err
			}})
		if err != nil && !errors.Is(err, errAddTaken) {
			return resumed, failed, err
		}
		r.log.Info("poll: queued download had no route job; resumed", "download", x.id)
		resumed++
	}
	return resumed, failed, nil
}
