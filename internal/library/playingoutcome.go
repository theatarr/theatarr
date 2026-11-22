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
		pb.episodes, pb.viewer, pb.player, pb.serving FROM playing_break pb JOIN title t ON t.id = pb.title_id
		WHERE pb.id = ?`, incident).Scan(&o.TitleID, &o.VersionKey, &name, &year, &old, &o.NewRelease, &eps, &o.Viewer,
		&o.Player, &o.Account)
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

// playingFixedTx notes, in an import's transaction after its links are written, the open rows of a title's version
// whose film or episodes the import's files hold and that now have a file for the film or for every one of their
// episodes: fixed by an import (fixed_by replaced, with the new release). A row only partly covered (E01 of a broken
// E01-E02) stays open. It returns the incidents whose watched file it fixed, to report once the import commits.
func playingFixedTx(ctx context.Context, tx *sql.Tx, titleID, versionID int64, version string, episodes []int64,
	release string, now int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, incident, watched, episodes FROM playing_break
		WHERE title_id = ? AND version_key = ? AND settled = '' AND fixed_by = ''`, titleID, version)
	if err != nil {
		return nil, err
	}
	type row struct {
		id, incident int64
		watched      bool
		eps          string
	}
	var hit []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.incident, &r.watched, &r.eps); err != nil {
			rows.Close()
			return nil, err
		}
		var eps []int64
		_ = json.Unmarshal([]byte(r.eps), &eps)
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
		var covered bool
		if err := tx.QueryRowContext(ctx, `SELECT CASE WHEN json_array_length(?) = 0 THEN EXISTS (SELECT 1 FROM link l
				JOIN link_version lv ON lv.link_id = l.id WHERE lv.version_id = ? AND l.state = 'active')
			ELSE NOT EXISTS (SELECT 1 FROM json_each(?) je WHERE NOT EXISTS (SELECT 1 FROM link l
				JOIN link_version lv ON lv.link_id = l.id JOIN link_episode le ON le.link_id = l.id
				WHERE lv.version_id = ? AND l.state = 'active' AND le.episode_id = je.value)) END`,
			r.eps, versionID, r.eps, versionID).Scan(&covered); err != nil {
			return nil, err
		}
		if !covered {
			continue
		}
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

// playingPending reports whether a search started for an incident of a title's version (broken at since, about
// eps; none for a film) may still bring a file: a search job for those episodes queued or running, a schedule row of
// their season due for a reason other than the regular retries, a search of their season or its route still running,
// or a download in flight holding one of them (any, for a film). Another season's searches do not count.
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
	js, _ := json.Marshal(eps)
	if len(eps) == 0 {
		js = []byte("[]")
	}
	// The incident's seasons (-1 for a film), as the schedule and the runs record them.
	seasons := `(SELECT -1 WHERE json_array_length(?1) = 0
		UNION SELECT e.season FROM episode e WHERE e.id IN (SELECT value FROM json_each(?1)))`
	var pending bool
	err := db.QueryRowContext(ctx, `SELECT
		EXISTS (SELECT 1 FROM job WHERE kind = ?2 AND state IN ('queued', 'running')
			AND json_extract(payload, '$.title_id') = ?3 AND json_extract(payload, '$.version') = ?4
			AND (json_array_length(?1) = 0 OR json_array_length(COALESCE(json_extract(payload, '$.episodes'), '[]')) = 0
				OR EXISTS (SELECT 1 FROM json_each(json_extract(payload, '$.episodes')) pe
					WHERE pe.value IN (SELECT value FROM json_each(?1)))))
		OR EXISTS (SELECT 1 FROM search_request WHERE title_id = ?3 AND version_key = ?4 AND why IS NOT NULL
			AND why NOT IN ('new', 'added') AND season IN `+seasons+`)
		OR EXISTS (SELECT 1 FROM acquire_run WHERE title_id = ?3 AND version_key = ?4 AND created_at >= ?5
			AND state IN ('queued', 'searching') AND COALESCE(season, -1) IN `+seasons+`)
		OR EXISTS (SELECT 1 FROM acquire_run r JOIN job j ON j.id = r.route_job WHERE r.title_id = ?3
			AND r.version_key = ?4 AND r.created_at >= ?5 AND j.state IN ('queued', 'running')
			AND COALESCE(r.season, -1) IN `+seasons+`)`,
		string(js), router.JobResearch, titleID, version, since).Scan(&pending)
	if err != nil || pending {
		return pending, err
	}
	return router.InFlight(ctx, db, titleID, versionID, eps)
}
