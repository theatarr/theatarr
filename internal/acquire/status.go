package acquire

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// RunStatus is one acquire_run as a command sees it.
type RunStatus struct {
	ID      int64  `json:"id"`
	Version string `json:"version"`
	Target  string `json:"target"`
	// Status is the arrs' command status: queued, started, completed, failed.
	Status  string `json:"status"`
	Message string `json:"message"`
	Updated time.Time
}

// CommandStatus is an arr view command's state, from its runs and their route jobs.
type CommandStatus struct {
	Status  string // queued, started, completed, failed
	Message string
	Started *time.Time
	Ended   *time.Time
	Runs    []RunStatus
}

// LoadCommandStatus reads the runs of a command and their routes: queued while every run waits, started while any
// searches or its route has not finished (or waits for a provider), completed when all have ended, failed when every
// run failed. nil when the command has no runs.
func LoadCommandStatus(ctx context.Context, db *sql.DB, commandID int64) (*CommandStatus, error) {
	rows, err := db.QueryContext(ctx, `SELECT r.id, r.version_key, r.target, r.state, r.message, r.updated_at,
		r.created_at, COALESCE(j.state, ''), COALESCE(j.last_error, '') FROM acquire_run r
		LEFT JOIN job j ON j.id = r.route_job WHERE r.command_id = ? ORDER BY r.id`, commandID)
	if err != nil {
		return nil, err
	}
	type raw struct {
		RunStatus
		state, jobState, jobErr string
		created                 int64
	}
	var rs []raw
	for rows.Next() {
		var r raw
		var upd int64
		if err := rows.Scan(&r.ID, &r.Version, &r.Target, &r.state, &r.Message, &upd, &r.created, &r.jobState,
			&r.jobErr); err != nil {
			rows.Close()
			return nil, err
		}
		r.Updated = store.FromMillis(upd)
		rs = append(rs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, nil
	}
	out := &CommandStatus{}
	n := map[string]int{}
	var first, last time.Time
	for _, r := range rs {
		switch r.state {
		case "queued":
			r.Status, r.Message = "queued", "waiting to search"
		case "searching":
			r.Status, r.Message = "started", "searching"
		case "nothing", "skipped":
			r.Status = "completed"
		case "failed":
			r.Status = "failed"
		case "routed":
			r.Status, r.Message, err = routeStatus(ctx, db, r.ID, r.jobState, r.jobErr, r.Message)
			if err != nil {
				return nil, err
			}
		}
		if r.Status != "queued" {
			c := store.FromMillis(r.created)
			if first.IsZero() || c.Before(first) {
				first = c
			}
		}
		if r.Updated.After(last) {
			last = r.Updated
		}
		n[r.Status]++
		out.Runs = append(out.Runs, r.RunStatus)
	}
	switch {
	case n["queued"] == len(rs):
		out.Status = "queued"
	case n["queued"]+n["started"] > 0:
		out.Status = "started"
	case n["failed"] == len(rs):
		out.Status = "failed"
	default:
		out.Status = "completed"
	}
	if !first.IsZero() {
		out.Started = &first
	}
	if out.Status == "completed" || out.Status == "failed" {
		out.Ended = &last
	}
	var msgs []string
	for i, r := range out.Runs {
		if i == 5 {
			msgs = append(msgs, fmt.Sprintf("and %d more", len(out.Runs)-5))
			break
		}
		msgs = append(msgs, fmt.Sprintf("%s %s: %s", r.Version, r.Target, r.Message))
	}
	out.Message = strings.Join(msgs, "; ")
	return out, nil
}

// routeStatus is a routed run's status from its route job and what the router did with the run's request: the
// download it opened (active), or the plan it recorded (a dry run while the kill switch is off).
func routeStatus(ctx context.Context, db *sql.DB, runID int64, jobState, jobErr, searched string) (string, string, error) {
	switch jobState {
	case "queued", "running", "":
		return "started", "routing: " + searched, nil
	case "failed":
		return "failed", "routing failed: " + jobErr, nil
	}
	src := Source(runID)
	var state, release, prov, dlErr string
	err := db.QueryRowContext(ctx, `SELECT state, release_title, COALESCE(provider, ''), COALESCE(error, '')
		FROM download WHERE source = ? ORDER BY CASE WHEN state = 'failed' THEN 1 ELSE 0 END, id DESC LIMIT 1`, src).
		Scan(&state, &release, &prov, &dlErr)
	switch {
	case err == nil && state == router.StateQueued:
		return "started", "waiting for a provider to take " + release, nil
	case err == nil && state == router.StateFailed:
		return "completed", "no provider took it: " + dlErr, nil
	case err == nil:
		return "completed", fmt.Sprintf("added %s on %s (%s)", release, prov, state), nil
	case err != sql.ErrNoRows:
		return "", "", err
	}
	// An upgrade held for approval (versions[*].upgrades: approve) opens no download.
	err = db.QueryRowContext(ctx, `SELECT release_title FROM upgrade_hold WHERE source = ? ORDER BY id DESC LIMIT 1`,
		src).Scan(&release)
	switch {
	case err == nil:
		return "completed", "held " + release + " for upgrade approval", nil
	case err != sql.ErrNoRows:
		return "", "", err
	}
	var mode, outcome, steps string
	err = db.QueryRowContext(ctx, `SELECT mode, outcome, release_title, provider, steps FROM router_plan
		WHERE source = ? ORDER BY id DESC LIMIT 1`, src).Scan(&mode, &outcome, &release, &prov, &steps)
	switch {
	case err == sql.ErrNoRows:
		return "completed", "nothing placed: " + searched, nil
	case err != nil:
		return "", "", err
	}
	var st []router.Step
	_ = json.Unmarshal([]byte(steps), &st)
	return "completed", fmt.Sprintf("dry run (%s): %s", mode, router.PlanSummary(outcome, release, prov, st)), nil
}
