package acquire

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Search now (the Wanted page's buttons). A request is one row of the page: one library's missing or cutoff-unmet
// targets of a movie, or of one series season. It makes that row's schedule row due now with why asked (schedule.go)
// and runs a round of its titles: asked rows go first under router.wanted.per_hour, and what the hour does not allow
// waits for the next round. An asked row is searched whatever the backlog switches say. While Pause is on nothing is
// accepted (ErrDownloadsOff); rows already due wait and start after Pause is turned off.

// ErrDownloadsOff is RequestSearchNow's refusal while Pause is on (router.enabled is false).
var ErrDownloadsOff = errors.New("nothing can be searched while Theatarr is paused")

// SearchNowItem names one row of the Wanted list. Season is nil for a movie; for a series without one, every season
// with something wanted is asked for.
type SearchNowItem struct {
	TitleID int64  `json:"title_id"`
	Version string `json:"version"`
	Season  *int   `json:"season,omitempty"`
}

// SearchNowSkip is an item that was not queued, and why.
type SearchNowSkip struct {
	TitleID int64  `json:"title_id"`
	Version string `json:"version"`
	Reason  string `json:"reason"`
}

// SearchNowResult is what a request did: Queued are the rows it made due (one already asked for and due is not
// counted again), Started the searches its round started, Pending the asked rows still due. NextAt is always nil: a
// waiting row starts at a later round.
type SearchNowResult struct {
	Queued  int             `json:"queued"`
	Started int             `json:"started"`
	Pending int             `json:"pending"`
	Skipped []SearchNowSkip `json:"skipped"`
	NextAt  *time.Time      `json:"next_at"`
}

// Reasons an item is skipped.
const (
	skipNotActive = "the library is not active"
	skipNothing   = "nothing of it is wanted now"
	skipInFlight  = "a download for it is in progress"
	skipSearching = "a search of it is queued or running"
)

type searchRequest struct {
	titleID int64
	key     string
	reason  string
	season  int // -1: a movie
}

// RequestSearchNow asks for a search of the named rows of the Wanted list (only: missing or cutoff) and starts what
// the hourly budget allows.
func (a *Acquirer) RequestSearchNow(ctx context.Context, only string, items []SearchNowItem) (*SearchNowResult, error) {
	ctx = a.captureRuntime(ctx)
	if only != OnlyMissing && only != OnlyCutoff {
		return nil, fmt.Errorf("only must be missing or cutoff")
	}
	if !a.pol.RouterSettings().Enabled {
		return nil, ErrDownloadsOff
	}
	out := &SearchNowResult{Skipped: []SearchNowSkip{}}
	var reqs []searchRequest
	for _, it := range items {
		skip := ""
		vp := a.pol.Versions[it.Version]
		switch {
		case vp == nil || vp.Mode != policy.ModeActive:
			skip = skipNotActive
		default:
			ts, err := a.targets(ctx, a.searchNowSelection(it.TitleID, it.Version, only, seasonOf(it.Season)))
			if err != nil {
				return nil, err
			}
			if len(ts) == 0 {
				skip = skipNothing
			}
			for _, group := range groupTargets(ts) {
				reqs = append(reqs, searchRequest{titleID: it.TitleID, key: it.Version, reason: only,
					season: seasonOf(group[0].season)})
			}
		}
		if skip != "" {
			out.Skipped = append(out.Skipped, SearchNowSkip{TitleID: it.TitleID, Version: it.Version, Reason: skip})
		}
	}
	return a.askSearch(ctx, reqs, out)
}

// RequestSearchNowAll queues every row the Wanted list shows for q (its tab, and its library and waiting filters when
// set), without paging and without a cap: the hourly budget paces it.
func (a *Acquirer) RequestSearchNowAll(ctx context.Context, q WantedQuery) (*SearchNowResult, error) {
	ctx = a.captureRuntime(ctx)
	if q.Only != OnlyMissing && q.Only != OnlyCutoff {
		return nil, fmt.Errorf("only must be missing or cutoff")
	}
	if !a.pol.RouterSettings().Enabled {
		return nil, ErrDownloadsOff
	}
	rows, err := a.wantedRows(ctx, q, &WantedList{Counts: map[string]int{}, DownloadsEnabled: true})
	if err != nil {
		return nil, err
	}
	out := &SearchNowResult{Skipped: []SearchNowSkip{}}
	var reqs []searchRequest
	for _, row := range rows {
		if row.SearchBlocked != "" {
			why := skipInFlight
			if row.SearchBlocked == "downloads_off" {
				why = ErrDownloadsOff.Error()
			}
			out.Skipped = append(out.Skipped, SearchNowSkip{TitleID: row.TitleID, Version: row.Version, Reason: why})
			continue
		}
		reqs = append(reqs, searchRequest{titleID: row.TitleID, key: row.Version, reason: q.Only, season: seasonOf(row.Season)})
	}
	return a.askSearch(ctx, reqs, out)
}

// askSearch makes the requested rows due now with why asked and runs a round of their titles.
func (a *Acquirer) askSearch(ctx context.Context, reqs []searchRequest, out *SearchNowResult) (*SearchNowResult, error) {
	var titles []int64
	running, err := a.busySet(ctx)
	if err != nil {
		return nil, err
	}
	var ready []searchRequest
	for _, r := range reqs {
		if running[requestKey(r.titleID, r.key, r.reason, r.season)] {
			out.Skipped = append(out.Skipped, SearchNowSkip{TitleID: r.titleID, Version: r.key, Reason: skipSearching})
			continue
		}
		ready = append(ready, r)
	}
	if len(ready) > 0 {
		now := store.Millis(a.now())
		if err := a.st.WriteTx(ctx, func(tx *sql.Tx) error {
			for _, r := range ready {
				res, err := tx.ExecContext(ctx, `INSERT INTO search_request (title_id, version_key, reason, season, created_at,
					next_at, tries, why) VALUES (?, ?, ?, ?, ?, ?, 0, ?) ON CONFLICT DO UPDATE SET next_at = excluded.next_at,
					tries = 0, why = excluded.why WHERE NOT (search_request.why IS excluded.why
					AND search_request.next_at <= excluded.next_at)`, r.titleID, r.key, r.reason, r.season, now, now, whyAsked)
				if err != nil {
					return err
				}
				n, _ := res.RowsAffected()
				out.Queued += int(n)
				titles = append(titles, r.titleID)
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	// The rows are stored: starting them no longer depends on the caller (a cancelled HTTP request must not leave
	// them unstarted until the next round).
	ctx = context.WithoutCancel(ctx)
	if len(titles) > 0 {
		sum, err := a.Round(ctx, titles)
		if err != nil {
			return nil, err
		}
		out.Started = sum.Queued
	}
	out.Pending, err = a.askedDue(ctx)
	return out, err
}

// askedDue counts the asked rows that are due.
func (a *Acquirer) askedDue(ctx context.Context) (int, error) {
	var n int
	err := a.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM search_request WHERE why = ? AND next_at <= ?`,
		whyAsked, store.Millis(a.now())).Scan(&n)
	return n, err
}

func (a *Acquirer) searchNowSelection(titleID int64, key, reason string, season int) Selection {
	sel := Selection{Versions: []string{key}, TitleIDs: []int64{titleID}, Only: reason,
		QualityCutoff: a.set.CutoffUnmet == policy.CutoffQuality}
	if season >= 0 {
		sel.Season = &season
	}
	return sel
}

// wantedUsed counts the searches of the last hour that draw on router.wanted.per_hour.
func (a *Acquirer) wantedUsed(ctx context.Context, now time.Time) (int, error) {
	var used int
	err := a.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM acquire_run WHERE source = ? AND search_run IS NULL
		AND infra <> 3 AND created_at > ?`, SourceWanted, store.Millis(now.Add(-time.Hour))).Scan(&used)
	return used, err
}

// ClearSearchNow takes back every asked row that is due: it goes back to the regular schedule (again_after from
// now). Searches already started are not touched.
func (a *Acquirer) ClearSearchNow(ctx context.Context) (int, error) {
	now := a.now()
	res, err := a.st.Writer().ExecContext(ctx, `UPDATE search_request SET why = NULL, tries = 0, next_at = ?
		WHERE why = ? AND next_at <= ?`, store.Millis(now.Add(a.set.AgainAfter)), whyAsked, store.Millis(now))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
