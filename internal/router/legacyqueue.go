package router

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/store"
)

// Legacy queue reconciliation (`theatarr router reconcile`, docs/design/router.md "Reconciling legacy queued routes"). A
// candidate queued by a build before RouteIdentityVersion carries no identity validation and no explicit origin, so
// what it was decided as cannot be proved from what was stored: its links are gone, and its origin is unknown (a
// prefer-on version's route ends such a row, fresh.go). ReconcileLegacyQueue lists every such row still waiting:
// queued route jobs and queued downloads (no add sent). With apply, an automatic row is ended
// and its title's version gets one deduplicated re-search, so the release is found and decided again under the
// current build; a manual row (a grab, retry or release pick by hand) is only listed, since it
// needs a fresh selection by a person.
// Running jobs, the queued downloads of a title version a running job works on, a job continuing a download whose
// add is in flight, and rows past queued are left alone (listed). Nothing is deleted: rows keep their history,
// provider ownership rows are not touched, and nothing is sent to any provider.

// ReasonReconcile is the re-search reason of a legacy row ended by reconciliation.
const ReasonReconcile = "reconcile"

// Kinds of legacy rows.
const (
	LegacyJob      = "job"
	LegacyDownload = "download"
)

// LegacyRow is one queued row whose candidates cannot be proved under RouteIdentityVersion.
type LegacyRow struct {
	Kind      string  `json:"kind"`
	ID        int64   `json:"id"`
	State     string  `json:"state"`
	TitleID   int64   `json:"title_id"`
	VersionID int64   `json:"version_id"`
	Version   string  `json:"version"`
	Source    string  `json:"source"`
	Release   string  `json:"release"`
	Episodes  []int64 `json:"episodes,omitempty"`
	Manual    bool    `json:"manual"`
	// Action is what reconciliation does (apply) or would do: "cancel", "list" (manual) or "leave" (work in progress:
	// a running job, its queued download, or a job continuing a download whose add is in flight).
	Action string `json:"action"`
}

// LegacyReport is what ReconcileLegacyQueue found and, with apply, did.
type LegacyReport struct {
	Applied    bool              `json:"applied"`
	Rows       []LegacyRow       `json:"rows"`
	Researches []ResearchRequest `json:"researches"`
	// Ended counts the rows ended (apply) or that would be; Queued the re-searches newly queued (apply).
	Ended  int `json:"ended"`
	Queued int `json:"queued"`
}

// proved says whether every candidate carries the current identity validation and an origin.
func proved(cs []Candidate) bool {
	for _, c := range cs {
		if c.Identity < RouteIdentityVersion || c.Origin == "" {
			return false
		}
	}
	return true
}

// ReconcileLegacyQueue inventories legacy queued rows and, with apply, ends the automatic ones and queues one
// re-search per title version (dedupe key per title version, so a second run adds none while the first is waiting).
// It runs in one write transaction.
func ReconcileLegacyQueue(ctx context.Context, st *store.Store, q *jobs.Queue, apply bool, now time.Time) (*LegacyReport, error) {
	rep := &LegacyReport{Applied: apply, Rows: []LegacyRow{}, Researches: []ResearchRequest{}}
	at := store.Millis(now)
	err := st.WriteTx(ctx, func(tx *sql.Tx) error {
		rows, err := legacyRows(ctx, tx)
		if err != nil {
			return err
		}
		type tv struct {
			title   int64
			version string
		}
		research := map[tv]*ResearchRequest{}
		var order []tv
		for i := range rows {
			x := &rows[i]
			switch {
			case x.Action == "leave":
			case x.Manual:
				x.Action = "list"
			default:
				x.Action = "cancel"
				rep.Ended++
				k := tv{x.TitleID, x.Version}
				r := research[k]
				if r == nil {
					r = &ResearchRequest{TitleID: x.TitleID, VersionID: x.VersionID, Version: x.Version,
						Reason: ReasonReconcile}
					research[k] = r
					order = append(order, k)
				}
				r.Episodes = unionEpisodes(r.Episodes, x.Episodes)
				if r.VersionID == 0 {
					r.VersionID = x.VersionID
				}
				if apply {
					if err := endLegacyRow(ctx, tx, *x, at); err != nil {
						return err
					}
				}
			}
		}
		for _, k := range order {
			r := research[k]
			rep.Researches = append(rep.Researches, *r)
			if !apply || r.TitleID == 0 || r.VersionID == 0 {
				continue
			}
			key := JobResearch + "/" + ReasonReconcile + "/" + r.Version + "/" + strconv.FormatInt(r.TitleID, 10)
			_, created, err := q.EnqueueTx(ctx, tx, JobResearch, *r, jobs.EnqueueOptions{DedupeKey: key, MaxAttempts: 1})
			if err != nil {
				return fmt.Errorf("router: reconcile: re-search %s title %d: %w", r.Version, r.TitleID, err)
			}
			if created {
				rep.Queued++
			}
		}
		rep.Rows = rows
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rep, nil
}

// legacyRows reads the queued rows whose candidates are not proved.
func legacyRows(ctx context.Context, tx *sql.Tx) ([]LegacyRow, error) {
	var out []LegacyRow
	// busy holds the downloads a route job is working on (running) or continuing while the add is in flight: their
	// queued download row and the job itself are left alone (listed), so work in progress is never cut short.
	busy := map[int64]bool{}
	// running holds the title versions with a running route job: an initial route opens its queued download before
	// its payload names it, and no column links the two, so every queued download of such a title version is left.
	type titleVersion struct{ title, version int64 }
	running := map[titleVersion]bool{}
	scan := func(q string, args []any, each func(*sql.Rows) error) error {
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if err := each(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	err := scan(`SELECT id, state, payload FROM job WHERE kind = ? AND state IN (?, ?) ORDER BY id`,
		[]any{JobRoute, jobs.StateQueued, jobs.StateRunning}, func(rows *sql.Rows) error {
			var x LegacyRow
			var payload string
			if err := rows.Scan(&x.ID, &x.State, &payload); err != nil {
				return err
			}
			var p routePayload
			if json.Unmarshal([]byte(payload), &p) != nil {
				return nil // unreadable payloads fail on their own when run
			}
			leave := x.State == jobs.StateRunning
			if p.DownloadID != 0 && !leave {
				var state string
				err := tx.QueryRowContext(ctx, `SELECT state FROM download WHERE id = ?`, p.DownloadID).Scan(&state)
				if err != nil && err != sql.ErrNoRows {
					return err
				}
				leave = state != "" && state != StateQueued && state != StateFailed && state != StateImported
			}
			if leave && p.DownloadID != 0 {
				busy[p.DownloadID] = true
			}
			if x.State == jobs.StateRunning {
				running[titleVersion{p.TitleID, p.VersionID}] = true
			}
			if len(p.Candidates) == 0 || proved(p.Candidates) {
				return nil // no candidates: nothing to prove
			}
			if leave {
				x.Action = "leave"
			}
			x.Kind, x.TitleID, x.VersionID, x.Version, x.Source = LegacyJob, p.TitleID, p.VersionID, p.Version, p.Source
			x.Release, x.Manual = p.Candidates[0].Release.Title, manualSource(p.Source)
			for _, c := range p.Candidates {
				x.Episodes = unionEpisodes(x.Episodes, c.Episodes)
			}
			out = append(out, x)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("router: reconcile: jobs: %w", err)
	}
	err = scan(`SELECT id, title_id, version_id, version_key, COALESCE(source, ''), release_title,
		COALESCE(episodes, '[]') FROM download WHERE state = ?
		AND (COALESCE(json_extract(release_data, '$.route_identity'), 0) < ?
			OR COALESCE(json_extract(release_data, '$.route_origin'), '') = '') ORDER BY id`,
		[]any{StateQueued, RouteIdentityVersion}, func(rows *sql.Rows) error {
			x := LegacyRow{Kind: LegacyDownload, State: StateQueued}
			var eps string
			if err := rows.Scan(&x.ID, &x.TitleID, &x.VersionID, &x.Version, &x.Source, &x.Release, &eps); err != nil {
				return err
			}
			_ = json.Unmarshal([]byte(eps), &x.Episodes)
			x.Manual = manualSource(x.Source)
			if busy[x.ID] || running[titleVersion{x.TitleID, x.VersionID}] {
				x.Action = "leave"
			}
			out = append(out, x)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("router: reconcile: downloads: %w", err)
	}
	return out, nil
}

// endLegacyRow ends one automatic legacy row, only while it is still where it was read.
func endLegacyRow(ctx context.Context, tx *sql.Tx, x LegacyRow, at int64) error {
	why := "reconcile: queued by an older build; its identity and origin cannot be proved; searched again"
	var err error
	switch x.Kind {
	case LegacyJob:
		// Failed and dismissed at once: it needs no attention, the re-search replaces it.
		_, err = tx.ExecContext(ctx, `UPDATE job SET state = ?, last_error = ?, finished_at = ?, updated_at = ?,
			dismissed_at = ?, dismissed_by = 'reconcile' WHERE id = ? AND state = ?`,
			jobs.StateFailed, why, at, at, at, x.ID, jobs.StateQueued)
	case LegacyDownload:
		_, err = tx.ExecContext(ctx, `UPDATE download SET state = 'failed', error = ?, updated_at = ?
			WHERE id = ? AND state = ?`, why, at, x.ID, StateQueued)
	}
	if err != nil {
		return fmt.Errorf("router: reconcile: %s %d: %w", x.Kind, x.ID, err)
	}
	return nil
}

// unionEpisodes is a and b merged, sorted, without repeats.
func unionEpisodes(a, b []int64) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, e := range append(append([]int64{}, a...), b...) {
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
