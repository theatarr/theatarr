package web

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/theatarr/theatarr/internal/notify"
	"github.com/theatarr/theatarr/internal/store"
)

func (w *Web) requestNotification(ctx context.Context, r *Request, ev RequestEvent) (*notify.Event, error) {
	var kind notify.Kind
	switch ev.Change {
	case "created", "merged":
		if r.Status == ReqPending {
			kind = notify.RequestPending
		}
	case "approved":
		kind = notify.RequestApproved
		if r.DecidedBy != nil && r.DecidedBy.ID == r.userID {
			kind = notify.RequestAutoApproved
		}
	case "declined":
		kind = notify.RequestDeclined
	case "partial_available":
		kind = notify.RequestPartial
	case "status":
		switch r.Status {
		case ReqAvailable:
			kind = notify.RequestAvailable
		case ReqFailed:
			kind = notify.RequestFailed
		}
	}
	if kind == "" {
		return nil, nil
	}
	items := []Request{*r}
	if err := w.fillRequests(ctx, items, nil, true); err != nil {
		return nil, err
	}
	r = &items[0]
	n := &notify.Request{ID: r.ID, Name: r.Name, Kind: r.Kind, Status: r.Status, Previous: ev.Previous,
		Change: ev.Change, Seasons: r.Seasons, Image: r.Poster, Reason: r.Reason,
		Versions: []notify.RequestVersion{}, Users: []notify.RequestUser{}}
	if r.TitleID != nil {
		n.TitleID = *r.TitleID
	}
	if r.TMDbID != nil {
		n.TMDbID = *r.TMDbID
	}
	if r.TVDbID != nil {
		n.TVDbID = *r.TVDbID
	}
	for _, v := range r.Versions {
		n.Versions = append(n.Versions, notify.RequestVersion{Key: v.Key, Label: v.Label, Available: v.Available})
	}
	users := r.OtherUser
	if r.User != nil {
		users = append([]RequestUser{*r.User}, users...)
	}
	// The Discord ids stay inside the notification (a Discord target's @mentions): never in the SSE event or the
	// native payload.
	discord := map[int64]string{}
	if len(users) > 0 {
		ids := make([]any, len(users))
		for i, u := range users {
			ids[i] = u.ID
		}
		err := scanAll(ctx, w.st.Reader(), `SELECT id, discord_id FROM web_user WHERE discord_id != '' AND id IN (`+
			store.Placeholders(len(ids))+`)`, ids, func(rows *sql.Rows) error {
			var id int64
			var d string
			if err := rows.Scan(&id, &d); err != nil {
				return err
			}
			discord[id] = d
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for _, u := range users {
		n.Users = append(n.Users, notify.RequestUser{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName,
			DiscordID: discord[u.ID]})
	}
	return &notify.Event{Kind: kind, At: w.now(), TitleID: n.TitleID, Request: n}, nil
}

// Partial availability follows the episode counts shown in the request detail, even if the status did not move.
// Claim it once per request in the existing notifier ledger so a restart cannot repeat the alert.
func (w *Web) notifyRequestPartial(ctx context.Context, r *Request) error {
	items := []Request{*r}
	if err := w.fillRequests(ctx, items, nil, false); err != nil {
		return err
	}
	d, err := w.requestDetail(ctx, &items[0])
	if err != nil {
		return err
	}
	have := false
	for _, p := range d.Progress {
		if p.Have != nil && *p.Have > 0 {
			have = true
		}
	}
	if !have {
		return nil
	}
	res, err := w.st.Writer().ExecContext(ctx, `INSERT INTO notify_event (kind, ref, created_at)
		VALUES (?, ?, ?) ON CONFLICT DO NOTHING`, string(notify.RequestPartial), r.ID, store.Millis(w.now()))
	if err != nil {
		return fmt.Errorf("request partial notification: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		w.requestChanged(ctx, RequestEvent{ID: r.ID, Status: r.Status, Name: r.Name, TitleID: r.TitleID,
			Change: "partial_available"})
	}
	return nil
}
