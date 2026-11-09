package notify

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/store"
)

// DigestJob is the job kind that sends the daily digest. The notifier queues one per day once notify.digest_at has
// passed (dedupe key notify.digest:<date>); the claim (notify_event digest/<yyyymmdd>) commits with its deliveries,
// so a restart or a second run never sends the same day twice.
const DigestJob = "notify.digest"

// digestTitles is how many titles the grabbed and imported lines name.
const digestTitles = 10

// DigestSummary is the last 24 hours as the digest tells them.
type DigestSummary struct {
	// Day is the local date the digest is for (2006-01-02).
	Day                 string
	Grabs, Imports      int
	Grabbed, Imported   []string // up to digestTitles "Title (year)"
	Failed, Blocklisted int
	// DeadLinks are library links a relink sweep marked dead in the window (its applied dead actions), counted even
	// when relinked or replaced since; StillDead are those of them still dead.
	DeadLinks, StillDead int
	// OpenErrors counts the new and seen error-inbox groups; Errors names the latest few.
	OpenErrors int
	Errors     []string
	// Health are the checks not OK (but the accounts check), Accounts the debrid accounts check when it is not OK.
	Health   []HealthItem
	Accounts *HealthItem
	// GrabbingOff: router.enabled is false.
	GrabbingOff bool
}

// Quiet reports a day with nothing to tell (grabbing off aside).
func (d *DigestSummary) Quiet() bool {
	return d.Grabs == 0 && d.Imports == 0 && d.Failed == 0 && d.Blocklisted == 0 && d.DeadLinks == 0 &&
		d.OpenErrors == 0 && len(d.Health) == 0 && d.Accounts == nil
}

// RegisterJobs registers the digest job.
func (n *Notifier) RegisterJobs(w *jobs.Worker) { w.Handle(DigestJob, n.runDigest) }

// digestDay is the day key of t: the local date as yyyymmdd.
func digestDay(t time.Time) int64 {
	v, _ := strconv.ParseInt(t.Format("20060102"), 10, 64)
	return v
}

// queueDigest queues today's digest job once notify.digest_at has passed and today's digest is not claimed. A day's
// job is queued once (a job that failed for good is not queued again that day).
func (n *Notifier) queueDigest(ctx context.Context) error {
	if n.q == nil {
		return nil
	}
	set := n.settings(ctx)
	now := n.now()
	slot := time.Date(now.Year(), now.Month(), now.Day(), set.DigestHour, set.DigestMinute, 0, 0, now.Location())
	if now.Before(slot) {
		return nil
	}
	var claimed bool
	if err := n.st.Reader().QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM notify_event WHERE kind = 'digest'
		AND ref = ?)`, digestDay(now)).Scan(&claimed); err != nil || claimed {
		return err
	}
	key := DigestJob + ":" + now.Format(time.DateOnly)
	if seen, err := n.q.Seen(ctx, DigestJob, key, slot); err != nil || seen {
		return err
	}
	_, _, err := n.q.Enqueue(ctx, DigestJob, digestJobPayload{Day: now.Format(time.DateOnly)},
		jobs.EnqueueOptions{DedupeKey: key, MaxAttempts: 3})
	return err
}

type digestJobPayload struct {
	Day string `json:"day"`
}

func (n *Notifier) runDigest(ctx context.Context, j *jobs.Job) error {
	var p digestJobPayload
	if err := j.Decode(&p); err != nil {
		return jobs.Permanent(err)
	}
	day, err := time.ParseInLocation(time.DateOnly, p.Day, n.now().Location())
	if err != nil {
		return jobs.Permanent(err)
	}
	return n.SendDigest(ctx, day)
}

// SendDigest records the digest of day (the 24 hours before now) once.
func (n *Notifier) SendDigest(ctx context.Context, day time.Time) error {
	var claimed bool
	if err := n.st.Reader().QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM notify_event WHERE kind = 'digest'
		AND ref = ?)`, digestDay(day)).Scan(&claimed); err != nil || claimed {
		return err
	}
	d, err := n.digest(ctx, n.now())
	if err != nil {
		return err
	}
	d.Day = day.Format(time.DateOnly)
	return n.emitOnce(ctx, "digest", digestDay(day), Event{Kind: Digest, Digest: d})
}

// digest gathers the 24 hours before now.
func (n *Notifier) digest(ctx context.Context, now time.Time) (*DigestSummary, error) {
	d := &DigestSummary{}
	db := n.st.Reader()
	from := store.Millis(now.Add(-24 * time.Hour))
	titles := func(q string) (int, []string, error) {
		rows, err := db.QueryContext(ctx, q, from)
		if err != nil {
			return 0, nil, err
		}
		defer rows.Close()
		var names []string
		count := 0
		for rows.Next() {
			var name string
			var year int
			if err := rows.Scan(&name, &year); err != nil {
				return 0, nil, err
			}
			count++
			if len(names) < digestTitles {
				if year > 0 {
					name += " (" + strconv.Itoa(year) + ")"
				}
				names = append(names, name)
			}
		}
		return count, names, rows.Err()
	}
	var err error
	if d.Grabs, d.Grabbed, err = titles(`SELECT t.name, COALESCE(t.year, 0) FROM download d JOIN title t ON t.id = d.title_id
		WHERE d.added_at >= ? ORDER BY d.added_at DESC, d.id DESC`); err != nil {
		return nil, err
	}
	if d.Imports, d.Imported, err = titles(`SELECT t.name, COALESCE(t.year, 0) FROM download d JOIN title t ON t.id = d.title_id
		WHERE d.imported_at >= ? AND d.state = 'imported' ORDER BY d.imported_at DESC, d.id DESC`); err != nil {
		return nil, err
	}
	for _, c := range []struct {
		dst *int
		q   string
	}{
		{&d.Failed, `SELECT COUNT(*) FROM download WHERE state = 'failed' AND updated_at >= ?`},
		{&d.Blocklisted, `SELECT COUNT(*) FROM router_blocklist WHERE created_at >= ?`},
		// A symlink gone from the library is recorded as a dead action too ("vanished: ..."); it is not a dead link.
		{&d.DeadLinks, `SELECT COUNT(DISTINCT link_id) FROM library_action WHERE kind = 'dead' AND mode = 'applied'
			AND at >= ? AND detail NOT LIKE 'vanished:%'`},
		{&d.StillDead, `SELECT COUNT(*) FROM link WHERE state = 'dead' AND id IN (SELECT link_id FROM library_action
			WHERE kind = 'dead' AND mode = 'applied' AND at >= ? AND detail NOT LIKE 'vanished:%')`},
	} {
		if err := db.QueryRowContext(ctx, c.q, from).Scan(c.dst); err != nil {
			return nil, err
		}
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM error_event WHERE status IN ('new', 'seen')`).
		Scan(&d.OpenErrors); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT component, message, count FROM error_event WHERE status IN ('new', 'seen')
		ORDER BY last_seen DESC, id DESC LIMIT 5`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var comp, msg string
		var count int64
		if err := rows.Scan(&comp, &msg, &count); err != nil {
			rows.Close()
			return nil, err
		}
		d.Errors = append(d.Errors, comp+": "+clip(msg, 200)+" ("+strconv.FormatInt(count, 10)+"×)")
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if n.health != nil {
		for _, h := range n.health(ctx) {
			switch {
			case h.Type == "ok" || h.Type == "notice":
			case h.Source == "ProviderAccountCheck":
				d.Accounts = &h
			case h.Source == "RouterDisabledCheck":
				// Said once below as "grabbing is off".
			default:
				d.Health = append(d.Health, h)
			}
		}
	}
	p := n.policy(ctx)
	d.GrabbingOff = p.Router == nil || !p.Router.Enabled
	return d, nil
}

// errorAlertsPerHour caps the instant error alerts; the rest wait for the digest (it counts the open groups).
const errorAlertsPerHour = 3

// scanErrors alerts on error-inbox groups that are new: a new fingerprint, or one that happened again after it was
// resolved (the inbox makes it new again and counts a regression). Each group and regression is claimed once
// (notify_event error:<regressions>, or error_held:<regressions> when the hourly cap held it back); a repeat of an
// open group is not announced. Groups that existed when the scan first ran are the baseline.
func (n *Notifier) scanErrors(ctx context.Context) error {
	db := n.st.Reader()
	var since string
	err := db.QueryRowContext(ctx, `SELECT value FROM notify_state WHERE key = 'errors_since'`).Scan(&since)
	if err == sql.ErrNoRows {
		now := store.Millis(n.now())
		return n.st.WriteTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `INSERT INTO notify_state (key, value, updated_at) VALUES ('errors_since', ?, ?)`,
				strconv.FormatInt(now, 10), now); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO notify_event (kind, ref, created_at)
				SELECT 'error_held:' || regressions, id, ? FROM error_event`, now)
			return err
		})
	}
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, `SELECT id, source, component, sample, count, regressions FROM error_event e
		WHERE status = 'new' AND NOT EXISTS (SELECT 1 FROM notify_event x WHERE x.ref = e.id
		AND x.kind IN ('error:' || e.regressions, 'error_held:' || e.regressions)) ORDER BY id`)
	if err != nil {
		return err
	}
	type group struct {
		item        ErrorItem
		regressions int64
	}
	var groups []group
	for rows.Next() {
		var g group
		if err := rows.Scan(&g.item.ID, &g.item.Source, &g.item.Component, &g.item.Sample, &g.item.Count,
			&g.regressions); err != nil {
			rows.Close()
			return err
		}
		g.item.Reopened = g.regressions > 0
		groups = append(groups, g)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, g := range groups {
		var sent int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notify_event WHERE kind LIKE 'error:%'
			AND created_at > ?`, store.Millis(n.now().Add(-time.Hour))).Scan(&sent); err != nil {
			return err
		}
		suffix := strconv.FormatInt(g.regressions, 10)
		if sent >= errorAlertsPerHour {
			if _, err := n.st.Writer().ExecContext(ctx, `INSERT OR IGNORE INTO notify_event (kind, ref, created_at)
				VALUES (?, ?, ?)`, "error_held:"+suffix, g.item.ID, store.Millis(n.now())); err != nil {
				return err
			}
			continue
		}
		item := g.item
		if err := n.emitOnce(ctx, "error:"+suffix, item.ID, Event{Kind: Error, Error: &item}); err != nil {
			return err
		}
	}
	return nil
}
