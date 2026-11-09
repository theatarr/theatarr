package acquire

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"

	"github.com/theatarr/theatarr/internal/store"
)

// hold takes out of sel the series whose first metadata refresh has not finished (series_meta.added_at, set by an
// add) and keeps their search in series_search_wait, one row per title, until ReleaseWaiting. A search naming
// episodes is never held: its episodes exist. It returns how many titles it held.
func (a *Acquirer) hold(ctx context.Context, commandID int64, sel *Selection) (int, error) {
	if a.meta == nil || len(sel.TitleIDs) == 0 || len(sel.EpisodeIDs) > 0 {
		return 0, nil
	}
	args := make([]any, len(sel.TitleIDs))
	for i, id := range sel.TitleIDs {
		args[i] = id
	}
	rows, err := a.st.Reader().QueryContext(ctx, `SELECT m.title_id FROM series_meta m JOIN title t ON t.id = m.title_id
		WHERE t.kind = 'series' AND m.added_at IS NOT NULL AND m.title_id IN (`+store.Placeholders(len(args))+`)`, args...)
	if err != nil {
		return 0, err
	}
	var wait []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		wait = append(wait, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(wait) == 0 {
		return 0, nil
	}
	var cmd any
	if commandID > 0 {
		cmd = commandID
	}
	now := store.Millis(a.now())
	err = a.st.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, id := range wait {
			one := *sel
			one.TitleIDs = []int64{id}
			b, err := json.Marshal(one)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO series_search_wait (title_id, command_id, selection, created_at)
				VALUES (?, ?, ?, ?)`, id, cmd, string(b), now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, id := range wait {
		a.log.Info("search waits for the series' metadata", "title", id, "command", commandID)
		if err := a.meta.Enqueue(ctx, id); err != nil {
			a.log.Warn("metadata refresh not queued", "title", id, "err", err)
		}
	}
	sel.TitleIDs = slices.DeleteFunc(sel.TitleIDs, func(id int64) bool { return slices.Contains(wait, id) })
	return len(wait), nil
}

// ReleaseWaiting starts the searches held for a series (tvrefresh calls it after the series' first refresh). A
// command whose held search finds nothing is completed with a message saying so.
func (a *Acquirer) ReleaseWaiting(ctx context.Context, titleID int64) error {
	rows, err := a.st.Reader().QueryContext(ctx, `SELECT id, COALESCE(command_id, 0), selection FROM series_search_wait
		WHERE title_id = ? ORDER BY id`, titleID)
	if err != nil {
		return err
	}
	type held struct {
		id, command int64
		sel         Selection
	}
	var hs []held
	for rows.Next() {
		var h held
		var b string
		if err := rows.Scan(&h.id, &h.command, &b); err != nil {
			rows.Close()
			return err
		}
		if json.Unmarshal([]byte(b), &h.sel) == nil {
			hs = append(hs, h)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, h := range hs {
		queued, _, err := a.Submit(ctx, h.command, h.sel)
		if err != nil {
			return err
		}
		a.log.Info("held search started", "title", titleID, "command", h.command, "queued", queued)
		if queued == 0 && h.command > 0 {
			now := store.Millis(a.now())
			if _, err := a.st.Writer().ExecContext(ctx, `UPDATE arrview_command SET status = 'completed',
				result = 'successful', message = ?, started_at = COALESCE(started_at, ?), ended_at = ? WHERE id = ?
				AND status IN ('queued', 'started')`, "Nothing to search after the series' metadata refresh: no "+
				"monitored episode of the active versions matches", now, now, h.command); err != nil {
				return err
			}
		}
		if _, err := a.st.Writer().ExecContext(ctx, `DELETE FROM series_search_wait WHERE id = ?`, h.id); err != nil {
			return err
		}
	}
	return nil
}
