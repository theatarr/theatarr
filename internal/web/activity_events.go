package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/activity"
	"github.com/theatarr/theatarr/internal/auth"
)

// The activity's live events on GET /events, next to jobs and gateway (events.go):
//
//	download  DownloadEvent   a download changed state, step, progress or library import
//	title     TitleEvent      a title's versions changed state, or its art or metadata was refreshed
//
// Like the jobs poller, this one looks for changes every PollInterval and only while someone listens. On the first
// poll with a listener it takes a baseline and sends nothing. The payloads hold what any signed-in user may see
// (no release names, providers or hashes); the SPA refetches the activity for the rest.

// DownloadEvent is the payload of a download event.
type DownloadEvent struct {
	ID       int64         `json:"id"`
	Title    ActivityTitle `json:"title"`
	Version  string        `json:"version"`
	State    string        `json:"state"`
	Previous string        `json:"previous,omitempty"` // the state before, when this poller saw it
	Step     string        `json:"step"`
	Words    string        `json:"words"`
	Progress float64       `json:"progress"`
}

// TitleEvent is the payload of a title event.
type TitleEvent struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Reason string `json:"reason"` // versions, art, acquiring
}

// activityPoller keeps the poller's cursors and what it last sent.
type activityPoller struct {
	since  time.Time        // downloads updated at or after this are looked at
	vSince int64            // version.updated_at cursor
	mSince int64            // title_meta refreshed_at cursor
	sent   map[int64]string // download id → signature last sent
	state  map[int64]string // download id → state last sent
}

func (w *Web) runActivityEvents(ctx context.Context) {
	t := time.NewTicker(w.poll)
	defer t.Stop()
	var p *activityPoller
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.hub.done:
			return
		case <-t.C:
		}
		if w.hub.count() == 0 {
			p = nil
			continue
		}
		if p == nil {
			np, err := w.activityBaseline(ctx)
			if err != nil {
				w.log.Warn("events: activity baseline", "err", err)
				continue
			}
			p = np
			w.activityPolls.Add(1)
			continue
		}
		if err := w.pollActivity(ctx, p); err != nil {
			w.log.Warn("events: activity", "err", err)
			continue
		}
		w.activityPolls.Add(1)
	}
}

func (w *Web) activityBaseline(ctx context.Context) (*activityPoller, error) {
	db := w.st.Reader()
	latest, err := activity.LatestUpdate(ctx, db)
	if err != nil {
		return nil, err
	}
	p := &activityPoller{since: latest, sent: map[int64]string{}, state: map[int64]string{}}
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(updated_at), 0) FROM version`).Scan(&p.vSince); err != nil {
		return nil, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(refreshed_at), 0) FROM title_meta`).Scan(&p.mSince); err != nil {
		return nil, err
	}
	// What the baseline already shows is not news: remember the rows at the cursor, and the state of every download
	// in hand, so a later event can say what it changed from.
	ds, err := activity.Downloads(ctx, db, activity.Filter{States: activity.InHand})
	if err != nil {
		return nil, err
	}
	if !latest.IsZero() {
		at, err := activity.Downloads(ctx, db, activity.Filter{UpdatedSince: latest})
		if err != nil {
			return nil, err
		}
		ds = append(ds, at...)
	}
	for _, d := range ds {
		p.sent[d.ID], p.state[d.ID] = signature(&d), d.State
	}
	return p, nil
}

func signature(d *activity.Download) string {
	return fmt.Sprintf("%s|%.0f|%s", d.State, d.Progress, d.ImportState)
}

// stageOf is a signature without the progress: the download's state and library import.
func stageOf(sig string) string {
	if i, j := strings.Index(sig, "|"), strings.LastIndex(sig, "|"); i >= 0 && j > i {
		return sig[:i] + sig[j:]
	}
	return sig
}

// acquiringEvent is the title event (reason acquiring) for every live requester of the title, whatever copies they
// asked for: it carries the title's id and name only, which they see already, so they refetch its acquiring.
func (w *Web) acquiringEvent(ctx context.Context, titleID int64, name string) (event, error) {
	var who []int64
	err := scanAll(ctx, w.st.Reader(), `SELECT r.user_id FROM request r WHERE r.title_id = ? AND r.user_id IS NOT NULL
		AND r.status NOT IN ('declined', 'canceled')
		UNION SELECT x.user_id FROM request_user x JOIN request r ON r.id = x.request_id WHERE r.title_id = ? AND
		r.status NOT IN ('declined', 'canceled')`, []any{titleID, titleID}, func(rows *sql.Rows) error {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		who = append(who, id)
		return nil
	})
	data, _ := json.Marshal(TitleEvent{ID: titleID, Name: name, Reason: "acquiring"})
	return event{name: "title", data: data, need: auth.PermBrowse, toUsers: true, users: who}, err
}

func (w *Web) pollActivity(ctx context.Context, p *activityPoller) error {
	db := w.st.Reader()
	f := activity.Filter{}
	if !p.since.IsZero() {
		f.UpdatedSince = p.since
	}
	ds, err := activity.Downloads(ctx, db, f)
	if err != nil {
		return err
	}
	var changed []activity.Download
	newest := p.since
	for _, d := range ds {
		if d.Updated.After(newest) {
			newest = d.Updated
		}
		sig := signature(&d)
		if p.sent[d.ID] == sig {
			continue
		}
		changed = append(changed, d)
	}
	if len(changed) > 0 {
		views, err := w.downloadViews(ctx, changed, false)
		if err != nil {
			return err
		}
		acquiring := map[int64]string{}
		for i, v := range views {
			d := &changed[i]
			if stageOf(p.sent[d.ID]) != stageOf(signature(d)) {
				acquiring[d.TitleID] = d.TitleName
			}
			ev := DownloadEvent{ID: v.ID, Title: v.Title, Version: v.Version, State: v.State, Previous: p.state[d.ID],
				Step: v.Step, Words: v.Words, Progress: v.Progress}
			if ev.Previous == ev.State {
				ev.Previous = ""
			}
			data, _ := json.Marshal(ev)
			dev, err := w.downloadEvent(ctx, d, data)
			if err != nil {
				return err
			}
			w.hub.publish(dev)
			p.sent[d.ID], p.state[d.ID] = signature(d), d.State
		}
		// A download's state or import changed: the title's acquiring (title-wide, on rows and the title page) may
		// have too. Requesters whose download events are scoped to their own copies get a title event to refetch.
		for id, name := range acquiring {
			tev, err := w.acquiringEvent(ctx, id, name)
			if err != nil {
				return err
			}
			if len(tev.users) > 0 {
				w.hub.publish(tev)
			}
		}
	}
	// Library imports change without the download row; the newest of both moves the cursor.
	if latest, err := activity.LatestUpdate(ctx, db); err == nil && latest.After(newest) {
		newest = latest
	}
	// Rows at the cursor (same millisecond) are read again next time; sent keeps them from being sent twice.
	if newest.After(p.since) {
		p.since = newest
	}

	// Titles: version state changes and refreshed art.
	rows, err := db.QueryContext(ctx, `SELECT v.title_id, t.name, MAX(v.updated_at) FROM version v
		JOIN title t ON t.id = v.title_id WHERE v.updated_at > ? GROUP BY v.title_id`, p.vSince)
	if err != nil {
		return err
	}
	type tev struct {
		id   int64
		name string
		at   int64
	}
	var vs []tev
	for rows.Next() {
		var x tev
		if err := rows.Scan(&x.id, &x.name, &x.at); err != nil {
			rows.Close()
			return err
		}
		vs = append(vs, x)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, x := range vs {
		p.vSince = max(p.vSince, x.at)
		data, _ := json.Marshal(TitleEvent{ID: x.id, Name: x.name, Reason: "versions"})
		w.hub.publish(event{name: "title", data: data, need: auth.PermBrowse})
	}
	rows, err = db.QueryContext(ctx, `SELECT m.title_id, t.name, m.refreshed_at FROM title_meta m
		JOIN title t ON t.id = m.title_id WHERE m.refreshed_at > ? ORDER BY m.refreshed_at LIMIT 500`, p.mSince)
	if err != nil {
		return err
	}
	var ms []tev
	for rows.Next() {
		var x tev
		if err := rows.Scan(&x.id, &x.name, &x.at); err != nil {
			rows.Close()
			return err
		}
		ms = append(ms, x)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, x := range ms {
		p.mSince = max(p.mSince, x.at)
		data, _ := json.Marshal(TitleEvent{ID: x.id, Name: x.name, Reason: "art"})
		w.hub.publish(event{name: "title", data: data, need: auth.PermBrowse})
	}
	return nil
}

// downloadEvent addresses a download event: those who see files get every one, users only those of the versions and
// seasons they requested, as on GET /activity.
func (w *Web) downloadEvent(ctx context.Context, d *activity.Download, data []byte) (event, error) {
	who, err := w.downloadRequesters(ctx, d)
	if err != nil {
		return event{}, err
	}
	return event{name: "download", data: data, need: auth.PermBrowse, toUsers: true, users: who,
		orPerm: auth.PermSeeFiles}, nil
}
