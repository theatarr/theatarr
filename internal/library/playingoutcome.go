package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/theatarr/theatarr/internal/outcome"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// The outcome of an incident (docs/design/now-playing.md "A file that stops working while it plays"): one per
// incident, about the watched file. same_release when another account made it readable again, replaced when an import
// took its place (apply: playingFixedTx), unavailable when nothing could be done or its search ended with nothing
// imported and nothing of it in flight (SettlePlayingRuns), restored when the old copy is put back. The files found
// broken with it send nothing; History and Now playing show them.

// OnPlayingOutcome sets the hook told each incident's outcome, once (nil: nobody). It runs after the commit that
// decided it, on the deciding goroutine; it must not block.
func (l *Library) OnPlayingOutcome(f func(outcome.Outcome)) { l.onPlaying = f }

// ReportPlaying sends the outcome kind of an incident's watched file, once: the row is marked reported first, so a
// second call (or another kind) sends nothing.
func (l *Library) ReportPlaying(ctx context.Context, incident int64, kind string) error {
	res, err := l.st.Writer().ExecContext(ctx, `UPDATE playing_break SET reported = ?, updated_at = ?
		WHERE id = ? AND watched = 1 AND reported = ''`, kind, store.Millis(l.now()), incident)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return err
	}
	o, err := l.playingOutcome(ctx, incident, kind)
	if err != nil {
		return err
	}
	l.log.Info("library: outcome of a file that broke while playing", "incident", incident, "kind", kind,
		"title", o.Title, "episodes", o.Episodes, "viewer", o.Viewer)
	if l.onPlaying != nil {
		l.onPlaying(o)
	}
	return nil
}

// playingOutcome builds the outcome of an incident from its watched row.
func (l *Library) playingOutcome(ctx context.Context, incident int64, kind string) (outcome.Outcome, error) {
	o := outcome.Outcome{Incident: incident, Kind: kind, At: l.now()}
	db := l.st.Reader()
	var eps string
	var name string
	var year sql.NullInt64
	var old sql.NullString
	err := db.QueryRowContext(ctx, `SELECT pb.title_id, pb.version_key, t.name, t.year, pb.release_title, pb.new_release,
		pb.episodes, pb.viewer, pb.player FROM playing_break pb JOIN title t ON t.id = pb.title_id WHERE pb.id = ?`,
		incident).Scan(&o.TitleID, &o.VersionKey, &name, &year, &old, &o.NewRelease, &eps, &o.Viewer, &o.Player)
	if err != nil {
		return o, fmt.Errorf("library: playing incident %d: %w", incident, err)
	}
	o.Title, o.OldRelease = name, old.String
	if year.Valid && year.Int64 > 0 {
		o.Title = fmt.Sprintf("%s (%d)", name, year.Int64)
	}
	if kind != outcome.Replaced {
		o.NewRelease = ""
	}
	var ids []int64
	_ = json.Unmarshal([]byte(eps), &ids)
	for _, id := range ids {
		var season, number int
		if db.QueryRowContext(ctx, `SELECT season, number FROM episode WHERE id = ?`, id).Scan(&season, &number) == nil {
			o.Episodes = append(o.Episodes, fmt.Sprintf("S%02dE%02d", season, number))
		}
	}
	return o, nil
}

// playingFixedTx notes, in an import's transaction, the open rows of a title's version whose film or episodes the
// imported file holds: fixed by an import (fixed_by replaced, with the new release). It returns the incidents whose
// watched file it fixed, to report once the import commits.
func playingFixedTx(ctx context.Context, tx *sql.Tx, titleID int64, version string, episodes []int64,
	release string, now int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, incident, watched, episodes FROM playing_break
		WHERE title_id = ? AND version_key = ? AND settled = '' AND fixed_by = ''`, titleID, version)
	if err != nil {
		return nil, err
	}
	type row struct {
		id, incident int64
		watched      bool
	}
	var hit []row
	for rows.Next() {
		var r row
		var js string
		if err := rows.Scan(&r.id, &r.incident, &r.watched, &js); err != nil {
			rows.Close()
			return nil, err
		}
		var eps []int64
		_ = json.Unmarshal([]byte(js), &eps)
		if len(eps) == 0 && len(episodes) == 0 || slices.ContainsFunc(eps, func(e int64) bool {
			return slices.Contains(episodes, e)
		}) {
			hit = append(hit, r)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var watched []int64
	for _, r := range hit {
		if _, err := tx.ExecContext(ctx, `UPDATE playing_break SET fixed_by = 'replaced', new_release = ?, updated_at = ?
			WHERE id = ?`, release, now, r.id); err != nil {
			return nil, err
		}
		if r.watched {
			watched = append(watched, r.incident)
		}
	}
	return watched, nil
}

// SettlePlayingRuns looks at each incident whose watched file was marked dead and whose outcome is not sent yet (or
// whose other broken files wait for their search): once nothing of its search is pending (no search job, due search
// row, running search or route, nor a download in flight of its title's version since the break), the outcome is sent
// (replaced when an import fixed it, else unavailable) and the other files' search is queued (QueuePlayingSearch).
// Now playing's worker calls it; it holds no incident id anywhere else.
func (l *Library) SettlePlayingRuns(ctx context.Context) error {
	rows, err := l.st.Reader().QueryContext(ctx, `SELECT pb.id, pb.title_id, pb.version_key, pb.broken_at, pb.episodes,
		pb.fixed_by, pb.reported FROM playing_break pb WHERE pb.watched = 1 AND pb.settled = '' AND pb.searched = 1
		AND (pb.reported = '' OR EXISTS (SELECT 1 FROM playing_break s WHERE s.incident = pb.id AND s.settled = ''
			AND s.searched = 0))`)
	if err != nil {
		return err
	}
	type open struct {
		id, title, since int64
		version, eps     string
		fixed, reported  string
	}
	var todo []open
	for rows.Next() {
		var o open
		if err := rows.Scan(&o.id, &o.title, &o.version, &o.since, &o.eps, &o.fixed, &o.reported); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, o := range todo {
		var eps []int64
		_ = json.Unmarshal([]byte(o.eps), &eps)
		pending, err := l.playingPending(ctx, o.title, o.version, o.since, eps)
		if err != nil {
			return err
		}
		if pending {
			continue
		}
		if o.reported == "" {
			kind := outcome.Unavailable
			if o.fixed == "replaced" {
				kind = outcome.Replaced
			}
			if err := l.ReportPlaying(ctx, o.id, kind); err != nil {
				return err
			}
		}
		if _, err := l.QueuePlayingSearch(ctx, o.id); err != nil {
			return err
		}
	}
	return nil
}

// playingPending reports whether a search of a title's version started for an incident (broken at since) may still
// bring a file: a search job queued or running, a schedule row due for a reason other than the regular retries, a
// search or its route still running, or a download in flight for the episodes (any for a film).
func (l *Library) playingPending(ctx context.Context, titleID int64, version string, since int64,
	eps []int64) (bool, error) {
	db := l.st.Reader()
	var versionID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM version WHERE title_id = ? AND policy_key = ?`, titleID,
		version).Scan(&versionID); errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	var pending bool
	err := db.QueryRowContext(ctx, `SELECT
		EXISTS (SELECT 1 FROM job WHERE kind = ? AND state IN ('queued', 'running')
			AND json_extract(payload, '$.title_id') = ? AND json_extract(payload, '$.version') = ?)
		OR EXISTS (SELECT 1 FROM search_request WHERE title_id = ? AND version_key = ? AND why IS NOT NULL
			AND why NOT IN ('new', 'added'))
		OR EXISTS (SELECT 1 FROM acquire_run WHERE title_id = ? AND version_key = ? AND created_at >= ?
			AND state IN ('queued', 'searching'))
		OR EXISTS (SELECT 1 FROM acquire_run r JOIN job j ON j.id = r.route_job WHERE r.title_id = ?
			AND r.version_key = ? AND r.created_at >= ? AND j.state IN ('queued', 'running'))`,
		router.JobResearch, titleID, version, titleID, version, titleID, version, since, titleID, version,
		since).Scan(&pending)
	if err != nil || pending {
		return pending, err
	}
	return router.InFlight(ctx, db, titleID, versionID, eps)
}
