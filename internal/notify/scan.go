package notify

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/theatarr/theatarr/internal/store"
)

// scanAdds observes the common version insert, including native, arr, list and follower intake. Updates during a
// follower's later passes do not insert another version; the title/key claim also survives a version being removed.
func (n *Notifier) scanAdds(ctx context.Context, since int64) error {
	rows, err := n.st.Reader().QueryContext(ctx, `SELECT v.title_id, v.policy_key, t.kind FROM version v
		JOIN title t ON t.id = v.title_id WHERE v.created_at >= ? AND NOT EXISTS
		(SELECT 1 FROM notify_event e WHERE e.ref = v.title_id AND e.kind =
		(CASE t.kind WHEN 'movie' THEN 'movie_added:' ELSE 'series_add:' END) || v.policy_key) ORDER BY v.id`, since)
	if err != nil {
		return err
	}
	var events []Event
	for rows.Next() {
		var ev Event
		var kind string
		if err := rows.Scan(&ev.TitleID, &ev.Version, &kind); err != nil {
			rows.Close()
			return err
		}
		ev.Kind = SeriesAdd
		if kind == "movie" {
			ev.Kind = MovieAdded
		}
		events = append(events, ev)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, ev := range events {
		if err := n.emitOnce(ctx, string(ev.Kind)+":"+ev.Version, ev.TitleID, ev); err != nil {
			return err
		}
	}
	return nil
}

func (n *Notifier) scanRenames(ctx context.Context) error {
	rows, err := n.st.Reader().QueryContext(ctx, `SELECT id, title_id, version_key, link_id, previous_path, path,
		quality, quality_version FROM notify_rename ORDER BY id`)
	if err != nil {
		return err
	}
	type rename struct {
		id int64
		ev Event
	}
	var events []rename
	for rows.Next() {
		r := rename{ev: Event{Kind: Rename}}
		var f RenamedFile
		if err := rows.Scan(&r.id, &r.ev.TitleID, &r.ev.Version, &f.File.ID, &f.PreviousPath, &f.File.Path,
			&f.File.Quality, &f.File.QualityVersion); err != nil {
			rows.Close()
			return err
		}
		f.File.ID += 1000000000 // arrview.LinkFileBase
		f.File.RelativePath, f.PreviousRelativePath = filepath.Base(f.File.Path), filepath.Base(f.PreviousPath)
		r.ev.RenamedFiles = []RenamedFile{f}
		events = append(events, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range events {
		if err := n.emitOnce(ctx, "rename", r.id, r.ev); err != nil {
			return err
		}
		if _, err := n.st.Writer().ExecContext(ctx, `DELETE FROM notify_rename WHERE id = ?`, r.id); err != nil {
			return err
		}
	}
	return nil
}

func (n *Notifier) scanHolds(ctx context.Context, since int64) error {
	rows, err := n.st.Reader().QueryContext(ctx, `SELECT d.id, d.title_id, d.version_key, COALESCE(i.last_error, ''),
		i.state, d.episodes FROM library_import i JOIN download d ON d.id = i.download_id
		WHERE d.state = 'ready' AND i.updated_at >= ? AND i.state = 'rejected' ORDER BY d.id`, since)
	if err != nil {
		return err
	}
	type held struct {
		ev     Event
		reason string
	}
	var events []held
	for rows.Next() {
		h := held{ev: Event{Kind: ManualInteractionRequired}}
		var state, episodes string
		if err := rows.Scan(&h.ev.DownloadID, &h.ev.TitleID, &h.ev.Version, &h.reason, &state, &episodes); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal([]byte(episodes), &h.ev.Episodes); err != nil {
			rows.Close()
			return err
		}
		h.ev.Reason = "Import held for review"
		events = append(events, h)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, h := range events {
		// The full reason identifies a transition, but can never leave the process: errors include release data.
		key := fmt.Sprintf("manual_interaction_required:%x", sha256.Sum256([]byte(h.reason)))
		if err := n.emitOnce(ctx, key, h.ev.DownloadID, h.ev); err != nil {
			return err
		}
	}
	return nil
}

// emitOnce commits the claim and its deliveries together, so a failed payload or outbox write is retried.
func (n *Notifier) emitOnce(ctx context.Context, kind string, ref int64, ev Event) error {
	var exists bool
	if err := n.st.Reader().QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM notify_event WHERE kind = ? AND ref = ?)`,
		kind, ref).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	ev.At = n.now()
	ds, err := n.deliveries(ctx, ev)
	if err != nil {
		return err
	}
	return n.st.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO notify_event (kind, ref, created_at) VALUES (?, ?, ?)
			ON CONFLICT DO NOTHING`, kind, ref, store.Millis(n.now()))
		if err != nil {
			return err
		}
		count, err := res.RowsAffected()
		if err != nil || count == 0 {
			return err
		}
		return n.recordTx(ctx, tx, ds)
	})
}
