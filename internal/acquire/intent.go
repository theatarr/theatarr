package acquire

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/theatarr/theatarr/internal/availability"
	"github.com/theatarr/theatarr/internal/importarr"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Add-time searches. Like the arrs, a follower version searches when a title or an episode is added or newly
// monitored, and after that relies on RSS (the deployment's wanted loop selects neither missing nor cutoff-unmet
// items). The arr import records each change as a follow_search_intent in its own transaction
// (importarr.SaveFollowSearches); DrainFollowSearches turns them into searches under the wanted loop's hourly budget
// (router.wanted.per_hour, shared with the loop's own searches), oldest first, after every import and at the start
// of every wanted round. So a change over the cap waits for the budget instead of being lost.
//
// An intent is deleted once each of its targets was searched within again_after (a run finished, not queued or
// searching), and when it asks for nothing any more: the version is gone, not active or unmonitored, or nothing of
// it is wanted. A started search that fails is recorded on its run and never repeated from the intent, except one
// that got no answer (the VPN guard refused it, or no indexer answered: acquire_run.infra = runInfra): that one is
// not counted, and a drain starts the search again after infraBackoff (15 m, 1 h, 4 h), then gives the target up. A
// partial answer (runPartial) counts as searched. A drain that fails on an intent counts an
// attempt; after intentAttempts the intent is dropped with a warning. While router.enabled is false the intents
// are dropped, as before: nothing is searched with grabbing off, and RSS covers those titles once it is on. An intent
// for an active version with grab off is kept, uncounted, until grab is on: its searches could only be dry runs.

// intentAttempts caps the drains that may fail on one intent.
const intentAttempts = 3

type followIntent struct {
	id       int64
	change   importarr.FollowSearchTarget
	attempts int
	created  int64
}

// DrainFollowSearches starts the pending add-time searches the hourly budget allows, oldest first, and reports how
// many it started.
func (a *Acquirer) DrainFollowSearches(ctx context.Context) (int, error) {
	ctx = a.captureRuntime(ctx)
	a.drainMu.Lock()
	defer a.drainMu.Unlock()
	if !a.pol.RouterSettings().Enabled {
		res, err := a.st.Writer().ExecContext(ctx, `DELETE FROM follow_search_intent`)
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			a.log.Info("add-time searches dropped: router.enabled is false", "intents", n)
		}
		return 0, nil
	}
	rows, err := a.st.Reader().QueryContext(ctx, `SELECT id, title_id, version_key, all_items, episodes, attempts,
		created_at FROM follow_search_intent ORDER BY created_at, id`)
	if err != nil {
		return 0, err
	}
	var intents []followIntent
	for rows.Next() {
		var in followIntent
		var all int
		var eps string
		if err := rows.Scan(&in.id, &in.change.TitleID, &in.change.VersionKey, &all, &eps, &in.attempts,
			&in.created); err != nil {
			rows.Close()
			return 0, err
		}
		in.change.All = all == 1
		_ = json.Unmarshal([]byte(eps), &in.change.EpisodeIDs)
		intents = append(intents, in)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(intents) == 0 {
		return 0, nil
	}
	used, err := a.wantedUsed(ctx, a.now())
	if err != nil {
		return 0, err
	}
	started := 0
	for _, in := range intents {
		if used >= a.set.PerHour {
			break
		}
		n, done, err := a.drainIntent(ctx, in.change, in.created, a.set.PerHour-used)
		started += n
		used += n
		switch {
		case err != nil && ctx.Err() != nil:
			return started, ctx.Err()
		case err != nil && in.attempts+1 >= intentAttempts:
			a.log.Warn("add-time search dropped after repeated errors", "title", in.change.TitleID,
				"version", in.change.VersionKey, "attempts", in.attempts+1, "err", err)
			done = true
		case err != nil:
			a.log.Warn("add-time search", "title", in.change.TitleID, "version", in.change.VersionKey, "err", err)
			if _, uerr := a.st.Writer().ExecContext(ctx, `UPDATE follow_search_intent SET attempts = attempts + 1,
				updated_at = ? WHERE id = ?`, store.Millis(a.now()), in.id); uerr != nil {
				return started, uerr
			}
			continue
		}
		if done {
			if _, err := a.st.Writer().ExecContext(ctx, `DELETE FROM follow_search_intent WHERE id = ?`, in.id); err != nil {
				return started, err
			}
		}
	}
	if started > 0 {
		a.log.Info("add-time searches started", "started", started, "pending", len(intents), "per_hour", a.set.PerHour)
	}
	return started, nil
}

// drainIntent starts at most left of an intent's searches; done when nothing of it is left to start or wait for.
// A target counts as searched by a run within again_after, or since the intent was recorded (created).
func (a *Acquirer) drainIntent(ctx context.Context, c importarr.FollowSearchTarget, created int64, left int) (int,
	bool, error) {
	vp := a.pol.Versions[c.VersionKey]
	if vp == nil || vp.Mode != policy.ModeActive {
		return 0, true, nil
	}
	if !a.pol.Grabs(c.VersionKey) {
		return 0, false, nil
	}
	var vid, titleID int64
	var monitored int
	var kind string
	if err := a.st.Reader().QueryRowContext(ctx, `SELECT v.id, v.title_id, v.monitored, t.kind FROM version v
		JOIN title t ON t.id = v.title_id WHERE v.policy_key = ? AND v.title_id = ?`, c.VersionKey, c.TitleID).Scan(&vid,
		&titleID, &monitored, &kind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, true, nil
		}
		return 0, false, err
	}
	if monitored == 0 {
		return 0, true, nil
	}
	var ts []target
	if kind != string(policy.KindSeries) {
		available, err := availability.Movie(ctx, a.st.Reader(), a.pol, titleID, vid, c.VersionKey, a.now())
		if err != nil {
			return 0, false, err
		}
		if available {
			ts = []target{{titleID: titleID, versionID: vid, key: c.VersionKey, name: "movie"}}
		}
	} else if c.All {
		got, err := a.seriesTargets(ctx, a.st.Reader(), vid, titleID, c.VersionKey, Selection{Only: OnlyMissing},
			store.Millis(a.now()))
		if err != nil {
			return 0, false, err
		}
		ts = got
	} else if len(c.EpisodeIDs) > 0 {
		got, err := a.seriesTargets(ctx, a.st.Reader(), vid, titleID, c.VersionKey, Selection{EpisodeIDs: c.EpisodeIDs,
			Only: OnlyMissing}, store.Millis(a.now()))
		if err != nil {
			return 0, false, err
		}
		ts = bySeason(got)
	}
	n, done := 0, true
	var own string // which of the version's runs the retry budget reads (ordinaryOf)
	ownRead := false
	since := min(store.Millis(a.now().Add(-a.set.AgainAfter)), created-1)
	for _, t := range ts {
		var recent, pending int
		if err := a.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(state IN ('queued', 'searching')), 0)
			FROM acquire_run WHERE version_id = ? AND target = ? AND infra NOT IN (1, 3) AND `+ordinaryRuns+`
			AND created_at > ?`, t.versionID, t.name, since).Scan(&recent, &pending); err != nil {
			return n, false, err
		}
		if recent > 0 {
			if pending > 0 {
				done = false // kept until the search ends: it may get no answer
			}
			continue
		}
		// Runs without an answer: started again after infraBackoff; given up once it is spent.
		if !ownRead { // once an intent, when a target needs it
			o, err := a.ordinaryOf(ctx, vid)
			if err != nil {
				return n, false, err
			}
			own, ownRead = o, true
		}
		fails, last, err := a.infraRuns(ctx, t.versionID, t.name, since, false, own)
		if err != nil {
			return n, false, err
		}
		next, ok := infraWait(fails, last)
		if !ok {
			a.log.Warn("add-time search got no answer repeatedly; given up", "title", t.titleID, "version", t.key,
				"target", t.name, "attempts", fails)
			continue
		}
		if next > store.Millis(a.now()) {
			done = false
			continue
		}
		if n >= left {
			return n, false, nil
		}
		at, err := a.followJoinedAt(ctx, c, t)
		if err != nil {
			return n, false, err
		}
		t.joinedAt = at
		if err := a.start(ctx, SourceWanted, 0, t, ""); err != nil {
			return n, false, err
		}
		n++
		done = false
	}
	return n, done, nil
}
