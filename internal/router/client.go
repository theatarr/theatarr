package router

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/qbit"
	"github.com/theatarr/theatarr/internal/store"
)

// Client-routed versions (versions[*].add_via: client; docs/design/external-clients.md).
//
// The route decides as for any version up to openDownload; then, instead of the providers, it sends one add to the
// version's download client (a decypharr client, which adds to its own debrid account, checks the cache and makes
// its symlinks) and the poller reads the client's torrents/info back. A client download keeps provider NULL and
// names its client in download.client, so the provider sweeps, reconcile, stall timeout, torrent manager and delete
// paths, which all select on the provider, never act on it. Nothing here removes anything from a client.

// Step codes of client routes.
const (
	codeClientAdded    = "client_add"
	codeClientAdopted  = "client_adopted"
	codeClientMiss     = "client_not_cached"
	codeClientRefused  = "client_refused"
	codeClientDeferred = "client_deferred"
	codeClientIndexer  = "indexer_providers"
	codeNoClient       = "no_client"
)

// BlocklistClient is the blocklist source of a release a download client refused for good (blocked, an invalid
// magnet, a permanent error). The route tries its next candidate itself, so it asks for no re-search.
const BlocklistClient = "client"

// clientCacheKey is the provider_cache key of a client's verdicts on a hash (its miss rows).
func clientCacheKey(k string) string { return "client:" + k }

// BuildClients builds the download clients a client-routed version may send to: one per client of a debrid kind,
// with the credentials its username_env and password_env name (constructing one sends nothing).
func BuildClients(pol *policy.Policy, d qbit.Doer, getenv func(string) string, log *slog.Logger) map[string]*qbit.Client {
	out := map[string]*qbit.Client{}
	for k, c := range pol.Clients {
		if c.Kind != "decypharr" {
			continue
		}
		cl, err := qbit.New(k, c, getenv(c.UsernameEnv), getenv(c.PasswordEnv), d)
		if err != nil {
			log.Warn("router: client left out", "client", k, "err", err)
			continue
		}
		out[k] = cl
	}
	return out
}

// clientSkip says why a client-routed version does not send a release to its client at all, without asking it:
// the indexer keeps its releases on the providers it lists (indexers.K.providers; a client picks its own account),
// or the client missed the hash within miss_ttl. "" when it may.
func (r *Router) clientSkip(ctx context.Context, s *routeState, rel *indexer.Release, hash string) (code, msg string,
	err error) {
	if ix := r.pol.Indexers[rel.Indexer]; ix != nil && len(ix.Providers) > 0 {
		return codeClientIndexer, fmt.Sprintf("indexer %s keeps its releases on the providers it lists "+
			"(indexers.%s.providers); client %s picks its own account", rel.Indexer, rel.Indexer, s.vp.Client), nil
	}
	state, err := r.cacheState(ctx, hash, clientCacheKey(s.vp.Client), r.now())
	if err != nil {
		return "", "", err
	}
	if state == "miss" {
		return "known_miss", "client " + s.vp.Client + " did not have it cached within miss_ttl", nil
	}
	return "", "", nil
}

// viaClient places one release of a client-routed version on its client, in place of the provider loop: it adopts
// the hash when the client holds it already (under any category), else sends one add. The same gate as a provider add
// (mayWrite) comes last.
func (r *Router) viaClient(ctx context.Context, s *routeState, c *Candidate, hash string) (verdict, time.Time, error) {
	rel := &c.Release
	key := s.vp.Client
	step := func(st Step) {
		st.Release, st.Hash, st.Client = rel.Title, hash, key
		s.res.Steps = append(s.res.Steps, st)
	}
	record := func(st Step) error {
		step(st)
		st.Client = key
		if s.dry || s.dlID == 0 {
			return nil
		}
		return r.recordAttempt(ctx, s.dlID, st)
	}
	end := func(code, msg string) (verdict, time.Time, error) {
		if err := record(Step{Outcome: "skipped", Code: code, Message: msg}); err != nil {
			return 0, time.Time{}, err
		}
		if !s.dry {
			return vContent, time.Time{}, r.failDownload(ctx, s.dlID, msg, StateQueued)
		}
		return vContent, time.Time{}, nil
	}
	if code, msg, err := r.clientSkip(ctx, s, rel, hash); err != nil {
		return 0, time.Time{}, err
	} else if code != "" {
		return end(code, msg)
	}
	cl := r.clients[key]
	if cl == nil {
		return end(codeNoClient, "client "+key+" is not set up (a decypharr client with a valid url)")
	}
	accepted := func(reused bool) {
		s.res.Release, s.res.Hash, s.res.Indexer, s.res.Client, s.res.Reused = rel.Title, hash, rel.Indexer, key, reused
		s.res.Outcome = OutcomeAdded
	}
	if s.dry {
		step(Step{Outcome: "would_add", Code: codeClientAdded, Message: "would send the bare magnet to client " + key})
		s.res.Release, s.res.Hash, s.res.Indexer, s.res.Client = rel.Title, hash, rel.Indexer, key
		s.res.Outcome = OutcomeWouldAdd
		return vAccepted, time.Time{}, nil
	}
	// Held already, under any category (an add whose answer was lost, another version's grab of the hash): adopted.
	held, err := cl.Info(ctx, []string{hash})
	if err != nil {
		return r.deferClient(ctx, s, c, Step{Outcome: "deferred", Code: codeClientDeferred,
			Message: "client " + key + " did not answer: " + err.Error()})
	}
	if len(held) > 0 {
		st := Step{Outcome: "accepted", Code: codeClientAdopted, Message: "client " + key + " holds this hash " +
			"under category " + held[0].Category + " already; nothing added"}
		if err := movedOnIfTaken(r.markClientAdded(ctx, s.dlID, key, held[0].Name, true, fromQueued)); err != nil {
			return 0, time.Time{}, err
		}
		accepted(true)
		return vAccepted, time.Time{}, record(st)
	}
	if err := r.mayWrite(s.req.Version); err != nil {
		// The gate closed while the route ran: as for a provider add, the download ends and the route stops.
		if eerr := r.endQueued(ctx, s.dlID, "route stopped: "+err.Error()); eerr != nil && !errors.Is(eerr, errMovedOn) {
			return 0, time.Time{}, eerr
		}
		return 0, time.Time{}, fmt.Errorf("%w: %w", errMovedOn, err)
	}
	attempt, err := r.markClientAdding(ctx, s.dlID, key)
	if err != nil {
		return 0, time.Time{}, movedOnIfTaken(err)
	}
	s.sent = true
	aerr := cl.Add(ctx, hash)
	rctx := context.WithoutCancel(ctx)
	if aerr != nil && ctx.Err() != nil {
		// Interrupted mid-add: the row stays adding and is settled from the client (settleClient).
		return 0, time.Time{}, fmt.Errorf("router: add to client %s interrupted: %w", key, context.Cause(ctx))
	}
	if aerr == nil {
		r.log.Info("sent to client", "client", key, "release", rel.Title, "hash", hash, "version", s.req.Version,
			"download", s.dlID)
		if err := r.markClientAdded(rctx, s.dlID, key, "", false, fromAdding(attemptOf(attempt))); err != nil {
			return 0, time.Time{}, err
		}
		accepted(false)
		return vAccepted, time.Time{}, record(Step{Outcome: "accepted", Code: codeClientAdded,
			Message: "sent the bare magnet to client " + key})
	}
	if err := r.unmarkAdding(rctx, s.dlID, attempt); err != nil {
		return 0, time.Time{}, err
	}
	now := r.now()
	switch qbit.Classify(aerr) {
	case qbit.Miss:
		// A cache miss on this client: remembered for miss_ttl; the next candidate is tried, nothing blocklisted.
		if err := r.setCache(rctx, hash, clientCacheKey(key), "miss", "client", now.Add(r.verdictTTL(rctx, s, now)),
			now); err != nil {
			return 0, time.Time{}, err
		}
		msg := "client " + key + ": not cached (" + aerr.Error() + ")"
		if err := record(Step{Outcome: "skipped", Code: codeClientMiss, Message: msg}); err != nil {
			return 0, time.Time{}, err
		}
		return vContent, time.Time{}, r.failDownload(rctx, s.dlID, msg, StateQueued)
	case qbit.Refused:
		msg := "client " + key + " refused it: " + aerr.Error()
		if err := record(Step{Outcome: "refused", Code: codeClientRefused, Message: msg}); err != nil {
			return 0, time.Time{}, err
		}
		r.log.Info("client refused the add", "client", key, "release", rel.Title, "hash", hash, "err", aerr.Error())
		return vContent, time.Time{}, r.failRefused(rctx, s.dlID, msg)
	}
	if notSent(aerr) {
		s.sent = false
	}
	return r.deferClient(ctx, s, c, Step{Outcome: "deferred", Code: codeClientDeferred,
		Message: "client " + key + ": " + aerr.Error()})
}

// deferClient leaves the route's download queued and the route deferred, as when every provider is paced: the
// client did not answer, or asked to be asked again.
func (r *Router) deferClient(ctx context.Context, s *routeState, c *Candidate, st Step) (verdict, time.Time, error) {
	now := r.now()
	at := now.Add(BackoffBase)
	st.RetryAt = at
	st.Release, st.Hash, st.Client = c.Release.Title, indexer.NormalizeHash(c.Release.InfoHash), s.vp.Client
	s.res.Steps = append(s.res.Steps, st)
	if err := r.recordAttempt(ctx, s.dlID, st); err != nil {
		return 0, time.Time{}, err
	}
	var err error
	if why := s.giveUp(now); why != "" {
		err = r.endQueued(ctx, s.dlID, why)
	} else {
		err = r.queueDownload(ctx, s.dlID, at, r.continuation(ctx, s, c, s.dlID, at))
	}
	if errors.Is(err, errMovedOn) {
		return r.movedOn(s, c, err), time.Time{}, nil
	}
	if err != nil {
		return 0, time.Time{}, err
	}
	s.res.Client = s.vp.Client
	return vUnavailable, at, nil
}

// failRefused fails the queued download a client refused for good and blocklists its hash for the version.
func (r *Router) failRefused(ctx context.Context, dl int64, why string) error {
	now := store.Millis(r.now())
	err := r.move(ctx, dl, transition{from: []string{StateQueued}, set: `state = 'failed', error = ?, updated_at = ?`,
		args: []any{why, now}, after: func(tx *sql.Tx) error {
			return blocklistDownload(ctx, tx, dl, why, BlocklistClient, now)
		}})
	if errors.Is(err, errAddTaken) {
		return nil
	}
	return err
}

// markClientAdding records that an add of download dl is going to client key (provider stays NULL).
func (r *Router) markClientAdding(ctx context.Context, dl int64, key string) (string, error) {
	attempt := newAttempt()
	err := r.move(ctx, dl, transition{from: []string{StateQueued}, lease: true,
		set:  `state = 'adding', client = ?, provider = NULL, add_attempt = ?, error = NULL, updated_at = ?`,
		args: []any{key, attempt, store.Millis(r.now())}})
	return attempt, err
}

// markClientAdded records that client key holds download dl's hash. Its torrent id is the hash; reused when the
// client held it before (adopted).
func (r *Router) markClientAdded(ctx context.Context, dl int64, key, name string, reused bool, f addFence) error {
	now := store.Millis(r.now())
	t := transition{from: []string{StateQueued}, set: `state = 'added', client = ?, provider = NULL,
		provider_torrent_id = info_hash, name = COALESCE(NULLIF(?, ''), name), add_attempt = NULL, error = NULL,
		reused = ?, added_at = ?, missing_polls = 0, missing_since = NULL, updated_at = ?`,
		args: []any{key, name, b2i(reused), now, now}}
	if f.adding {
		t.from, t.attempt = []string{StateAdding}, &f.attempt
	}
	return r.move(ctx, dl, t)
}

// clientRow reads a download's client and add state; client "" for a provider download.
func (r *Router) clientRow(ctx context.Context, dl int64) (client, state, hash string, attempt sql.NullString,
	sent time.Time, err error) {
	var at int64
	err = r.st.Writer().QueryRowContext(ctx, `SELECT COALESCE(client, ''), state, info_hash, add_attempt, updated_at
		FROM download WHERE id = ?`, dl).Scan(&client, &state, &hash, &attempt, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", attempt, time.Time{}, nil
	}
	return client, state, hash, attempt, store.FromMillis(at), err
}

// settleClient settles a client download left in adding from the client: its hash held (under any category) is the add
// landed (added; recFound), absent is the add never made (recAbsent: the caller queues it again). A client that
// does not answer leaves it in adding (recUnknown). recNone when it is not in adding; client "" when dl is not a
// client download (the caller settles it from the providers).
func (r *Router) settleClient(ctx context.Context, dl int64) (addCheck, error) {
	key, state, hash, attempt, sent, err := r.clientRow(ctx, dl)
	c := addCheck{client: key, attempt: attempt, sent: sent, hash: hash, tid: hash}
	if err != nil || key == "" || state != StateAdding {
		return c, err
	}
	cl := r.clients[key]
	if cl == nil {
		c.rec, c.why = recUnknown, "client "+key+" is not set up; the interrupted add is not settled"
		return c, nil
	}
	held, err := cl.Info(ctx, []string{hash})
	switch {
	case err != nil:
		c.rec, c.why = recUnknown, "client "+key+" did not answer: "+err.Error()
	case len(held) > 0:
		if err := r.markClientAdded(ctx, dl, key, held[0].Name, false, fromAdding(attempt)); err != nil {
			if errors.Is(err, errAddTaken) {
				return addCheck{client: key}, nil // settled meanwhile: recNone
			}
			return c, err
		}
		c.rec, c.why = recFound, "the interrupted add is on client "+key
	default:
		c.rec, c.why = recAbsent, "the interrupted add is not on client "+key
	}
	return c, nil
}

// clientOutage is one client's outage as the poller sees it (guarded by shared.poll).
type clientOutage struct {
	since time.Time // the first failed read
	fails int       // failed reads in a row
	next  time.Time // the next read
}

// clientRead is one open client download as the poller reads it.
type clientRead struct {
	pollRow
	attempt sql.NullString
	sent    time.Time
}

// pollClients reads each client's open downloads back, one torrents/info request per hash (decypharr reads one
// hash per hashes value) under any category: downloading,
// ready (with the client's name, and for import_from download_folder its mapped content_path as source_path) or
// failed (blocklisted and searched again, as a provider failure). A hash missing from the answer over missingGrace
// fails the same way. An add left in adding by an interrupted route with no route job to settle it is settled
// here. A client that does not answer fails nothing: its reads back off and the outage is logged once. Called
// under shared.poll, without r.mu (taken for each write).
func (r *Router) pollClients(ctx context.Context, now time.Time, sum *PollSummary) error {
	rows, err := r.st.Writer().QueryContext(ctx, `SELECT id, client, info_hash, state, missing_polls,
		COALESCE(missing_since, 0), add_attempt, updated_at FROM download WHERE client IS NOT NULL
		AND (state IN ('added', 'downloading') OR (state = 'adding' AND updated_at < ? AND NOT `+pendingRouteSQL+`))
		ORDER BY id`, store.Millis(now.Add(-reconcileAfter)))
	if err != nil {
		return fmt.Errorf("router: poll clients: %w", err)
	}
	byClient := map[string][]clientRead{}
	for rows.Next() {
		var x clientRead
		var missingSince, sent int64
		if err := rows.Scan(&x.id, &x.pk, &x.hash, &x.state, &x.missingPolls, &missingSince, &x.attempt,
			&sent); err != nil {
			rows.Close()
			return err
		}
		x.tid, x.sent = x.hash, store.FromMillis(sent)
		if missingSince > 0 {
			x.missingSince = store.FromMillis(missingSince)
		}
		byClient[x.pk] = append(byClient[x.pk], x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	keys := make([]string, 0, len(byClient))
	for k := range byClient {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		xs := byClient[key]
		cl := r.clients[key]
		if cl == nil {
			sum.Unpolled += len(xs)
			r.log.Warn("poll: client unavailable; downloads left unpolled", "client", key, "count", len(xs))
			continue
		}
		out := r.shared.clientDown[key]
		if out != nil && now.Before(out.next) {
			continue // backing off after failed reads
		}
		// One read per hash, each with its own deadline, applied as it arrives. The first failed read ends the
		// client's pass: the downloads not read are left for the next pass, never counted missing.
		for i, x := range xs {
			rctx, cancel := context.WithTimeout(ctx, pollPassLimit)
			ts, err := cl.Info(rctx, []string{x.hash})
			cancel()
			if err != nil {
				sum.Errors++
				if out == nil {
					out = &clientOutage{since: now}
					if r.shared.clientDown == nil {
						r.shared.clientDown = map[string]*clientOutage{}
					}
					r.shared.clientDown[key] = out
					r.log.Error("poll: download client does not answer; its downloads wait", "client", key,
						"downloads", len(xs)-i, "err", err)
				}
				out.fails++
				out.next = r.pollBackoff(now, out.fails)
				break
			}
			sum.Read++
			if out != nil {
				delete(r.shared.clientDown, key)
				r.log.Info("poll: download client answers again", "client", key,
					"down_for", now.Sub(out.since).Round(time.Second))
				out = nil
			}
			var t qbit.Torrent
			if len(ts) > 0 {
				t = ts[0]
			}
			r.mu.Lock()
			err = r.pollClientOne(ctx, x, cl, t, len(ts) > 0, now, sum)
			r.mu.Unlock()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// pollClientOne records one client read of download x (under r.mu): t is its torrent, when the client listed it.
func (r *Router) pollClientOne(ctx context.Context, x clientRead, cl *qbit.Client, t qbit.Torrent, listed bool,
	now time.Time, sum *PollSummary) error {
	key := cl.Key()
	if x.state == StateAdding {
		if listed {
			err := r.markClientAdded(ctx, x.id, key, t.Name, false, fromAdding(x.attempt))
			if err == nil {
				sum.Reconciled++
			}
			if errors.Is(err, errAddTaken) {
				return nil
			}
			return err
		}
		// Never landed: queued again, and the stray sweep routes it (resumeStrays).
		ok, err := r.settleAdding(ctx, x.id, x.attempt, StateQueued, "")
		if ok {
			sum.Reconciled++
			r.log.Info("interrupted add not on the client; its download is queued again", "download", x.id,
				"client", key)
		}
		return err
	}
	if !listed {
		gone, err := r.missingRead(ctx, x.pollRow, now)
		if err != nil || !gone {
			return err
		}
		sum.Failed++
		return r.failProviderDownload(ctx, x.id, "the torrent is no longer on client "+key)
	}
	switch t.Status() {
	case qbit.StatusFailed:
		sum.Failed++
		return r.failProviderDownload(ctx, x.id, "client "+key+" status "+t.State)
	case qbit.StatusReady:
		var source any
		if pc := r.pol.Clients[key]; pc.FromDownloadFolder() {
			if t.ContentPath == "" {
				break // its folder is not reported yet: downloading until it is
			}
			source = pc.LocalContentPath(t.ContentPath)
		}
		err := r.move(ctx, x.id, transition{from: []string{StateAdded, StateDownloading},
			set: `state = 'ready', provider_status = ?, name = COALESCE(NULLIF(?, ''), name), progress = 100,
			source_path = COALESCE(?, source_path), ready_at = COALESCE(ready_at, ?), missing_polls = 0,
			missing_since = NULL, error = NULL, polled_at = ?, updated_at = ?`,
			args: []any{t.State, t.Name, source, store.Millis(now), store.Millis(now), store.Millis(now)}})
		if err == nil {
			sum.Ready++
			r.notifyReady()
		}
		if errors.Is(err, errAddTaken) {
			return nil
		}
		return err
	}
	err := r.move(ctx, x.id, transition{from: []string{StateAdded, StateDownloading},
		set: `state = 'downloading', provider_status = ?, name = COALESCE(NULLIF(?, ''), name), progress = ?,
		missing_polls = 0, missing_since = NULL, polled_at = ?, updated_at = ?`,
		args: []any{t.State, t.Name, min(t.Progress, 1) * 100, store.Millis(now), store.Millis(now)}})
	if errors.Is(err, errAddTaken) {
		return nil
	}
	return err
}
