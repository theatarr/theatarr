package acquire

import (
	"context"
	"database/sql"
	"errors"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// Re-search after a failure (router.JobResearch, docs/design/router.md "Re-search after a failure"): the router asks for
// a search of a title's version when a download failed for a reason the arrs and decypharr answer with "blocklist
// and search again" (every candidate missed, stalled, dangerous files, the library's guard), and the library when a
// link is dead. Research makes the item's schedule row due now with the reason as its why and runs a round of the
// title (Round), so it is searched under router.wanted.per_hour like every loop search, and while paused it waits.
//
// The row is the film's (missing while the film is wanted, else cutoff), or one per season of the failed episodes
// (missing when one of them is wanted). One re-search per row within router.wanted.again_after (partialAgain after
// a partial answer): a row whose library and season had a loop search after a failure in that time is not made due
// again, so a release that keeps failing cannot loop. A dead link always is.

func (a *Acquirer) handleResearch(ctx context.Context, j *jobs.Job) error {
	var p router.ResearchRequest
	if err := j.Decode(&p); err != nil {
		return jobs.Permanent(err)
	}
	ctx = a.captureRuntime(ctx)
	return a.Research(ctx, p)
}

// Research makes the rows of a router.ResearchRequest due now and runs a round of the title.
func (a *Acquirer) Research(ctx context.Context, p router.ResearchRequest) error {
	ctx = a.captureRuntime(ctx)
	vp := a.pol.Versions[p.Version]
	switch {
	case vp == nil || vp.Mode != policy.ModeActive:
		a.log.Info("re-search skipped: the library is not Live", "title", p.TitleID, "version", p.Version, "reason", p.Reason)
		return nil
	case p.Reason == router.ReasonDeadLink && !library.NewGate(a.pol).Active(p.Version):
		a.log.Info("re-search skipped: the library does not write", "title", p.TitleID, "version", p.Version)
		return nil
	}
	db := a.st.Reader()
	type row struct {
		reason string
		season int
	}
	var rows []row
	if vp.Kind != policy.KindSeries {
		var state string
		if err := db.QueryRowContext(ctx, `SELECT state FROM version WHERE id = ?`, p.VersionID).Scan(&state); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		r := row{reason: OnlyCutoff, season: -1}
		if state == "wanted" {
			r.reason = OnlyMissing
		}
		rows = append(rows, r)
	} else if len(p.Episodes) > 0 {
		args := []any{p.VersionID, p.TitleID}
		for _, id := range p.Episodes {
			args = append(args, id)
		}
		q, err := db.QueryContext(ctx, `SELECT e.season, MAX(COALESCE(s.state, 'wanted') = 'wanted') FROM episode e
			LEFT JOIN episode_version_state s ON s.episode_id = e.id AND s.version_id = ?
			WHERE e.title_id = ? AND e.id IN (`+store.Placeholders(len(p.Episodes))+`) GROUP BY e.season ORDER BY e.season`,
			args...)
		if err != nil {
			return err
		}
		for q.Next() {
			var r row
			var wanted bool
			if err := q.Scan(&r.season, &wanted); err != nil {
				q.Close()
				return err
			}
			r.reason = OnlyCutoff
			if wanted {
				r.reason = OnlyMissing
			}
			rows = append(rows, r)
		}
		q.Close()
		if err := q.Err(); err != nil {
			return err
		}
	}
	now := a.now()
	again := store.Millis(now.Add(-a.set.AgainAfter))
	partial := max(again, store.Millis(now.Add(-partialAgain)))
	written := 0
	for _, r := range rows {
		if p.Reason != router.ReasonDeadLink {
			var recent bool
			if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM acquire_run WHERE version_id = ?
				AND COALESCE(season, -1) = ? AND source = ? AND research IS NOT NULL AND research NOT IN (?, ?)
				AND ((infra = 0 AND created_at > ?) OR (infra = 2 AND created_at > ?)))`, p.VersionID, r.season,
				SourceWanted, whyAsked, whyNew, again, partial).Scan(&recent); err != nil {
				return err
			}
			if recent {
				a.log.Info("re-search skipped: searched again after a failure within router.wanted.again_after",
					"title", p.TitleID, "version", p.Version, "season", r.season, "reason", p.Reason)
				continue
			}
		}
		if _, err := a.st.Writer().ExecContext(ctx, `INSERT INTO search_request (title_id, version_key, reason, season,
			created_at, next_at, tries, why) VALUES (?, ?, ?, ?, ?, ?, 0, ?) ON CONFLICT DO UPDATE
			SET next_at = excluded.next_at, tries = 0, why = excluded.why`, p.TitleID, p.Version, r.reason, r.season,
			store.Millis(now), store.Millis(now), p.Reason); err != nil {
			return err
		}
		written++
		a.log.Info("re-search due", "title", p.TitleID, "version", p.Version, "season", r.season, "reason", p.Reason,
			"download", p.DownloadID)
	}
	if written == 0 {
		return nil
	}
	_, err := a.Round(ctx, []int64{p.TitleID})
	return err
}
