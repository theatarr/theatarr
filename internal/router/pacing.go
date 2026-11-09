package router

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Pacing per provider (docs/design/router.md): adds per minute, a cooldown after a burst, an hourly cap on uncached
// adds, and an exponential back-off after errors. The state is one router_pacing row per provider, read and written
// under the router's lock, so a restart keeps it.
//
//   - adds_per_minute: the adds sent in the current minute (from the first of them). Using the last one starts a
//     cooldown when the provider has one (a burst); otherwise the provider waits for the minute to end. 0 = no limit.
//   - min_gap: the least time between two adds to the provider, counted from the last add sent (last_add_at,
//     migration 00082). Real-Debrid's default is 20 s: "Two adds on one account need 20 seconds between them"
//     (https://notes.debridmediamanager.com/zurg/providers/realdebrid/). A route whose provider is inside the gap
//     defers like any paced provider: the release waits for its turn (or goes to the next provider), it is not dropped.
//   - cooldown: also follows Real-Debrid's account hold: a 451 on a name its rule does not refuse, on a different hash
//     of another title from another 451 within holdWindow (one 451 alone, or 451s on several releases of one title,
//     block only those hashes: note451; title451Limit of them skip the title there). The hold escalates
//     (holdLadder): a 451 that repeats while the previous hold runs or within holdQuiet after it ends
//     takes the next step (5 min, 15 min, 60 min; never below cooldown), and a quiet holdQuiet resets it. Real-Debrid's
//     throttle "has memory. Once tripped it keeps refusing through minutes of quiet" and "clears with quiet and not
//     with retries" (same page).
//   - uncached_adds_per_hour: adds that turned out uncached, and TorBox's not-cached refusals (they cost the same
//     quota), per clock hour. 0 = no limit.
//   - back-off: BackoffBase × 2^(failures-1), capped at backoff_max (default 1 h). Account conditions (traffic,
//     auth) go straight to the cap. A provider's Retry-After, when later, wins.

// BackoffBase is the first back-off after an error.
const BackoffBase = time.Minute

// DefaultBackoffMax caps the back-off when the provider's pacing sets none.
const DefaultBackoffMax = time.Hour

type pace struct {
	WindowStart   time.Time `json:"window_start,omitzero"`
	WindowAdds    int       `json:"window_adds"`
	HourStart     time.Time `json:"hour_start,omitzero"`
	HourUncached  int       `json:"hour_uncached"`
	CooldownUntil time.Time `json:"cooldown_until,omitzero"`
	BackoffUntil  time.Time `json:"backoff_until,omitzero"`
	Failures      int       `json:"failures"`
	Reason        string    `json:"reason,omitempty"`
	// BackoffDown: the running back-off means the provider is down (unreachable, a 5xx, its maintenance, downloads
	// that stalled while it was down: providerDown, endUnavailable), not that an add was refused or rate-limited.
	// Only such a back-off keeps a route from reusing a torrent the provider holds.
	BackoffDown bool `json:"backoff_down,omitempty"`
	// LastAdd is when the last add was sent (min_gap). HoldLevel and HoldUntil are the account-hold ladder's step
	// and the end of the last hold.
	LastAdd   time.Time `json:"last_add,omitzero"`
	HoldLevel int       `json:"hold_level"`
	HoldUntil time.Time `json:"hold_until,omitzero"`
}

// holdLadder is the account-hold cooldown per step; a provider's cooldown is the floor of every step.
var holdLadder = []time.Duration{5 * time.Minute, 15 * time.Minute, time.Hour}

// holdQuiet: a 451 more than this after the last hold ended starts the ladder again.
const holdQuiet = time.Hour

// holdWindow: 451s on different hashes of different titles within this hold the account; a 451 alone, or 451s on
// several releases of one title, are verdicts on those hashes.
const holdWindow = 10 * time.Minute

// title451Limit: after 451s on this many different hashes of one title within miss_ttl, the provider is not tried for
// the title's other releases until they expire (Real-Debrid's infringing_file blocks are usually per title).
const title451Limit = 3

// note451 records a 451 on hash at provider for title (0: unknown) and reports whether it holds the account: a 451
// came on another hash of another title (or of an unknown one) within holdWindow, or the account is held already
// (inHold: a 451 during a running hold is the hold's). A hash refused again (the same release retried) and the
// releases of one title refused one after another are no sign of a hold, so neither can pin the account on the hold
// ladder. When the account is held, the window's earlier 451s are explained by it: their hashes are
// returned (explained) so that their misses are lifted, and they no longer count against their titles. Rows older
// than keep (at least holdWindow) are pruned; keep 0 prunes nothing (the torrent manager does not know miss_ttl).
func note451(ctx context.Context, x interface {
	queryer
	execer
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}, provider, hash string, title int64, now time.Time, keep time.Duration, inHold bool) (bool, []string, error) {
	at := store.Millis(now)
	since := store.Millis(now.Add(-holdWindow))
	if keep > 0 {
		cut := store.Millis(now.Add(-max(keep, holdWindow)))
		if _, err := x.ExecContext(ctx, `DELETE FROM router_refusal_451 WHERE refused_at < ?`, cut); err != nil {
			return false, nil, fmt.Errorf("router: 451 memory: %w", err)
		}
	}
	held := inHold
	if !held {
		var others int
		if err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM router_refusal_451 WHERE provider = ?
			AND held = 0 AND info_hash <> ? AND refused_at >= ? AND (title_id = 0 OR ? = 0 OR title_id <> ?)`,
			provider, hash, since, title, title).Scan(&others); err != nil {
			return false, nil, fmt.Errorf("router: 451 memory: %w", err)
		}
		held = others > 0
	}
	var explained []string
	if held {
		rows, err := x.QueryContext(ctx, `SELECT info_hash FROM router_refusal_451 WHERE provider = ? AND info_hash <> ?
			AND refused_at >= ? AND held = 0`, provider, hash, since)
		if err != nil {
			return false, nil, fmt.Errorf("router: 451 memory: %w", err)
		}
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				rows.Close()
				return false, nil, err
			}
			explained = append(explained, h)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return false, nil, err
		}
		if _, err := x.ExecContext(ctx, `UPDATE router_refusal_451 SET held = 1 WHERE provider = ? AND refused_at >= ?`,
			provider, since); err != nil {
			return false, nil, fmt.Errorf("router: 451 memory: %w", err)
		}
	}
	if _, err := x.ExecContext(ctx, `INSERT INTO router_refusal_451 (provider, info_hash, refused_at, title_id, held)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT (provider, info_hash) DO UPDATE SET refused_at = excluded.refused_at,
		title_id = CASE WHEN excluded.title_id <> 0 THEN excluded.title_id ELSE router_refusal_451.title_id END,
		held = excluded.held`, provider, hash, at, title, held); err != nil {
		return false, nil, fmt.Errorf("router: 451 memory: %w", err)
	}
	return held, explained, nil
}

// title451s counts the hashes of title the provider refused with a 451 since then that no account hold explains.
func title451s(ctx context.Context, q queryer, provider string, title int64, since time.Time) (int, error) {
	if title == 0 {
		return 0, nil
	}
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(DISTINCT info_hash) FROM router_refusal_451 WHERE provider = ?
		AND title_id = ? AND held = 0 AND refused_at >= ?`, provider, title, store.Millis(since)).Scan(&n); err != nil {
		return 0, fmt.Errorf("router: 451 memory: %w", err)
	}
	return n, nil
}

func loadPace(ctx context.Context, q queryer, provider string) (pace, error) {
	var p pace
	var ws, hs, cu, bu, la, hu int64
	err := q.QueryRowContext(ctx, `SELECT window_start, window_adds, hour_start, hour_uncached, cooldown_until,
		backoff_until, failures, reason, last_add_at, hold_level, hold_until, backoff_down FROM router_pacing
		WHERE provider = ?`, provider).Scan(&ws, &p.WindowAdds, &hs, &p.HourUncached, &cu, &bu, &p.Failures, &p.Reason,
		&la, &p.HoldLevel, &hu, &p.BackoffDown)
	if errors.Is(err, sql.ErrNoRows) {
		return pace{}, nil
	}
	if err != nil {
		return pace{}, fmt.Errorf("router: pacing of %s: %w", provider, err)
	}
	p.WindowStart, p.HourStart, p.CooldownUntil, p.BackoffUntil = ms(ws), ms(hs), ms(cu), ms(bu)
	p.LastAdd, p.HoldUntil = ms(la), ms(hu)
	return p, nil
}

func savePace(ctx context.Context, x execer, provider string, p pace, now time.Time) error {
	_, err := x.ExecContext(ctx, `INSERT INTO router_pacing (provider, window_start, window_adds, hour_start,
		hour_uncached, cooldown_until, backoff_until, failures, reason, last_add_at, hold_level, hold_until, backoff_down,
		updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (provider) DO UPDATE SET window_start = excluded.window_start, window_adds = excluded.window_adds,
		hour_start = excluded.hour_start, hour_uncached = excluded.hour_uncached,
		cooldown_until = excluded.cooldown_until, backoff_until = excluded.backoff_until,
		failures = excluded.failures, reason = excluded.reason, last_add_at = excluded.last_add_at,
		hold_level = excluded.hold_level, hold_until = excluded.hold_until, backoff_down = excluded.backoff_down,
		updated_at = excluded.updated_at`,
		provider, toMs(p.WindowStart), p.WindowAdds, toMs(p.HourStart), p.HourUncached, toMs(p.CooldownUntil),
		toMs(p.BackoffUntil), p.Failures, p.Reason, toMs(p.LastAdd), p.HoldLevel, toMs(p.HoldUntil), p.BackoffDown,
		store.Millis(now))
	if err != nil {
		return fmt.Errorf("router: saving pacing of %s: %w", provider, err)
	}
	return nil
}

// updatePace applies fn to provider's pacing inside one write transaction and saves it when fn says so: a route's
// reservation and outcome must not overwrite, with a copy read earlier, what a concurrent ReserveAdd or
// RecordAddOutcome (the torrent manager's restore) wrote meanwhile.
func updatePace(ctx context.Context, st *store.Store, provider string, now time.Time,
	fn func(tx *sql.Tx, p *pace) (bool, error)) error {
	return st.WriteTx(ctx, func(tx *sql.Tx) error {
		p, err := loadPace(ctx, tx, provider)
		if err != nil {
			return err
		}
		save, err := fn(tx, &p)
		if err != nil || !save {
			return err
		}
		return savePace(ctx, tx, provider, p, now)
	})
}

// unadd takes back an add reserved (before → after) for a request the gateway refused before it was sent, leaving
// what another add changed since.
func (p *pace) unadd(before, after pace) {
	if p.LastAdd.Equal(after.LastAdd) {
		p.LastAdd = before.LastAdd
	}
	if p.WindowStart.Equal(after.WindowStart) && p.WindowAdds > 0 {
		p.WindowAdds--
	}
	if !after.CooldownUntil.Equal(before.CooldownUntil) && p.CooldownUntil.Equal(after.CooldownUntil) {
		p.CooldownUntil, p.Reason = before.CooldownUntil, before.Reason
	}
}

// blocked says until when the provider takes no add, and why; zero when it takes one now.
func (p *pace) blocked(now time.Time, pc policy.Pacing) (time.Time, string) {
	var until time.Time
	why := ""
	later := func(t time.Time, w string) {
		if t.After(now) && t.After(until) {
			until, why = t, w
		}
	}
	later(p.BackoffUntil, "backoff: "+p.Reason)
	later(p.CooldownUntil, "cooldown: "+p.Reason)
	if g := pc.Gap(); g > 0 && !p.LastAdd.IsZero() {
		later(p.LastAdd.Add(g), fmt.Sprintf("min_gap %s since the last add", g))
	}
	if pc.AddsPerMinute > 0 && now.Sub(p.WindowStart) < time.Minute && p.WindowAdds >= pc.AddsPerMinute {
		later(p.WindowStart.Add(time.Minute), fmt.Sprintf("adds_per_minute %d used", pc.AddsPerMinute))
	}
	if pc.UncachedPerHr > 0 && sameHour(p.HourStart, now) && p.HourUncached >= pc.UncachedPerHr {
		later(now.UTC().Truncate(time.Hour).Add(time.Hour), fmt.Sprintf("uncached_adds_per_hour %d used",
			pc.UncachedPerHr))
	}
	return until, why
}

// added counts an add sent (accepted or refused: either costs the provider's add rate). Using the minute's last
// add starts the cooldown after a burst.
func (p *pace) added(now time.Time, pc policy.Pacing) {
	if now.Sub(p.WindowStart) >= time.Minute || p.WindowStart.After(now) {
		p.WindowStart, p.WindowAdds = now, 0
	}
	p.WindowAdds++
	p.LastAdd = now
	if pc.AddsPerMinute > 0 && p.WindowAdds >= pc.AddsPerMinute && pc.Cooldown > 0 {
		p.CooldownUntil, p.Reason = now.Add(pc.Cooldown.D()), fmt.Sprintf("burst of %d adds", p.WindowAdds)
	}
}

// uncached counts an uncached add (or a not-cached refusal) in the current clock hour.
func (p *pace) uncached(now time.Time) {
	if !sameHour(p.HourStart, now) {
		p.HourStart, p.HourUncached = now.UTC().Truncate(time.Hour), 0
	}
	p.HourUncached++
}

// ok clears the error count after a provider answered normally.
func (p *pace) ok() {
	p.Failures = 0
	p.BackoffDown = false // it answered: whatever back-off still runs is no outage
	if p.BackoffUntil.IsZero() && p.CooldownUntil.IsZero() {
		p.Reason = ""
	}
}

// fail backs off exponentially; retryAt (a provider's Retry-After) wins when later. toMax goes straight to the cap.
// down: the failure says the provider is down (providerDown); a down back-off still running stays down when an add
// refusal extends it.
func (p *pace) fail(now time.Time, pc policy.Pacing, reason string, retryAt time.Time, toMax, down bool) time.Time {
	p.BackoffDown = down || (p.BackoffDown && p.BackoffUntil.After(now))
	p.Failures++
	maxD := pc.BackoffMax.D()
	if maxD <= 0 {
		maxD = DefaultBackoffMax
	}
	d := BackoffBase << min(p.Failures-1, 20)
	if toMax || d > maxD {
		d = maxD
	}
	until := now.Add(d)
	if retryAt.After(until) {
		until = retryAt
	}
	p.BackoffUntil, p.Reason = until, reason
	return until
}

// hold pauses adds after Real-Debrid's account hold (a 451 its name rule does not explain), escalating: a hold that
// comes while the last one runs or within holdQuiet after it ended takes the next step of holdLadder (5, 15, 60
// min); one after a quiet holdQuiet starts at the first again. The provider's cooldown is the floor of every step,
// and retryAt wins when later.
func (p *pace) hold(now time.Time, pc policy.Pacing, retryAt time.Time) time.Time {
	if !p.HoldUntil.IsZero() && now.Before(p.HoldUntil.Add(holdQuiet)) {
		p.HoldLevel = min(p.HoldLevel+1, len(holdLadder)-1)
	} else {
		p.HoldLevel = 0
	}
	d := max(holdLadder[p.HoldLevel], pc.Cooldown.D())
	until := now.Add(d)
	if retryAt.After(until) {
		until = retryAt
	}
	reason := "account hold (451)"
	if p.HoldLevel > 0 {
		reason = fmt.Sprintf("account hold (451), repeated: step %d of %d, %s", p.HoldLevel+1, len(holdLadder), d)
	}
	p.CooldownUntil, p.HoldUntil, p.Reason = until, until, reason
	return until
}

func sameHour(a, b time.Time) bool {
	return !a.IsZero() && a.UTC().Truncate(time.Hour).Equal(b.UTC().Truncate(time.Hour))
}

func ms(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return store.FromMillis(v)
}

func toMs(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return store.Millis(t)
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Adds made outside a route (a restore from the torrent manager, internal/torrents) share these rows, so a restore
// and the router never together send more than the provider's pacing allows.

// ReserveAdd takes one add from provider's pacing now: the zero time when the add may go (it is counted), else until
// when the provider takes no add and why.
func ReserveAdd(ctx context.Context, st *store.Store, provider string, pc policy.Pacing, now time.Time) (time.Time,
	string, error) {
	var until time.Time
	var why string
	err := st.WriteTx(ctx, func(tx *sql.Tx) error {
		p, err := loadPace(ctx, tx, provider)
		if err != nil {
			return err
		}
		if until, why = p.blocked(now, pc); !until.IsZero() {
			return nil
		}
		p.added(now, pc)
		return savePace(ctx, tx, provider, p, now)
	})
	return until, why, err
}

// PacedUntil is when provider's pacing takes an add again and why (the zero time when it takes one now), read
// without reserving anything: the torrent manager's restore asks it before giving up an add it holds for another.
func PacedUntil(ctx context.Context, st *store.Store, provider string, pc policy.Pacing, now time.Time) (time.Time,
	string, error) {
	p, err := loadPace(ctx, st.Writer(), provider)
	if err != nil {
		return time.Time{}, "", err
	}
	until, why := p.blocked(now, pc)
	return until, why, nil
}

// AddOutcome is the provider's answer to an add ReserveAdd allowed.
type AddOutcome struct {
	// OK: accepted. Uncached: accepted (or refused) and not cached; it counts against uncached_adds_per_hour.
	OK, Uncached bool
	// Reason, RetryAt and ToMax back the provider off after a failure, as a route's failure does. Down: the failure
	// says the provider is down (ProviderDown), which also stops routes reusing its torrents for the back-off.
	Reason  string
	RetryAt time.Time
	ToMax   bool
	Down    bool
	// Hold: Real-Debrid's 451 (adder.CodeAccountHold) on Hash. It holds the account (the escalating hold cooldown)
	// only when another hash got one within holdWindow, or during a running hold (note451); alone it is a verdict on
	// the hash.
	Hold bool
	Hash string
}

// RecordAddOutcome records o in provider's pacing.
func RecordAddOutcome(ctx context.Context, st *store.Store, provider string, pc policy.Pacing, now time.Time,
	o AddOutcome) error {
	return st.WriteTx(ctx, func(tx *sql.Tx) error {
		p, err := loadPace(ctx, tx, provider)
		if err != nil {
			return err
		}
		if o.Uncached {
			p.uncached(now)
		}
		switch {
		case o.OK:
			p.ok()
		case o.Hold:
			held, explained, err := note451(ctx, tx, provider, o.Hash, 0, now, 0, p.HoldUntil.After(now))
			if err != nil {
				return err
			}
			if held {
				if err := liftMisses(ctx, tx, provider, explained); err != nil {
					return err
				}
				p.hold(now, pc, o.RetryAt)
			}
		case o.Reason != "":
			p.fail(now, pc, o.Reason, o.RetryAt, o.ToMax, o.Down)
		}
		return savePace(ctx, tx, provider, p, now)
	})
}

// Step code for a release skipped at a provider that refused title451Limit releases of its title with a 451.
const codeTitle451 = "title_451"

// titleRefused says why the provider is not tried for title's releases ("" when it is): it refused title451Limit
// of them with a 451 within miss_ttl, none explained by an account hold.
func (r *Router) titleRefused(ctx context.Context, pk string, title int64, now time.Time) (string, error) {
	if r.set.MissTTL <= 0 {
		return "", nil
	}
	n, err := title451s(ctx, r.st.Writer(), pk, title, now.Add(-r.set.MissTTL))
	if err != nil || n < title451Limit {
		return "", err
	}
	return fmt.Sprintf("%s refused %d releases of this title with 451 within miss_ttl (%s); the title is not tried "+
		"there until they expire", pk, n, r.set.MissTTL), nil
}

// liftMisses takes back the misses earlier 451s on hashes put on provider: an account hold explains them, and the
// releases are tried there again after the hold.
func (r *Router) liftMisses(ctx context.Context, provider string, hashes []string) error {
	return liftMisses(ctx, r.st.Writer(), provider, hashes)
}

func liftMisses(ctx context.Context, x execer, provider string, hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	args := []any{provider}
	for _, h := range hashes {
		args = append(args, h)
	}
	if _, err := x.ExecContext(ctx, `DELETE FROM provider_cache WHERE provider = ? AND state = 'miss'
		AND source = 'router:451' AND info_hash IN (`+store.Placeholders(len(hashes))+`)`, args...); err != nil {
		return fmt.Errorf("router: provider cache: %w", err)
	}
	return nil
}
