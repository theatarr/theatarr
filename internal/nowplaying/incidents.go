package nowplaying

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// incidentsWithin is how far back Now playing lists incidents.
const incidentsWithin = 24 * time.Hour

// Incident is a file that broke while someone played it, as Now playing lists it: the watched file and what came of
// it (internal/library playing_break).
type Incident struct {
	ID      int64  `json:"id"`
	TitleID int64  `json:"title_id"`
	Title   string `json:"title"`
	Version string `json:"version"`
	// Episodes are the watched file's (S01E02); empty for a film.
	Episodes []string `json:"episodes,omitempty"`
	// Files is how many files the incident holds: the watched one and those of the release found broken with it.
	Files  int    `json:"files"`
	Viewer string `json:"viewer,omitempty"`
	Player string `json:"player,omitempty"`
	// State is looking, replaced, readable, unavailable or restored; Status says it in words.
	State      string `json:"state"`
	Status     string `json:"status"`
	OldRelease string `json:"old_release,omitempty"`
	NewRelease string `json:"new_release,omitempty"`
	// Message is what the viewer's player was told, or why Plex refused.
	Message string    `json:"message,omitempty"`
	At      time.Time `json:"at"`
}

// incidentState is an incident's state and its words, from its watched row.
func incidentState(settled, fixed, reported string) (string, string) {
	switch {
	case settled == "restored":
		return "restored", "Earlier copy put back"
	case settled == "copy" || settled == "revived":
		return "readable", "Readable again"
	case fixed == "replaced":
		return "replaced", "Replaced"
	case settled == "none" || reported == "unavailable":
		return "unavailable", "No other copy"
	}
	return "looking", "Looking for another copy"
}

// Incidents lists the incidents of the last 24 hours, newest first. Who may see which is the caller's (the web API's
// own-session and file-detail permissions); the title's History keeps the durable rows.
func (t *Tracker) Incidents() []Incident {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db := t.o.Store.Reader()
	rows, err := db.QueryContext(ctx, `SELECT pb.id, pb.title_id, t.name, t.year, pb.version_key, pb.episodes,
		(SELECT COUNT(*) FROM playing_break f WHERE f.incident = pb.id), pb.viewer, pb.player, pb.settled, pb.fixed_by,
		pb.reported, COALESCE(pb.release_title, ''), pb.new_release, pb.message, pb.broken_at
		FROM playing_break pb JOIN title t ON t.id = pb.title_id WHERE pb.watched = 1 AND pb.broken_at > ?
		ORDER BY pb.broken_at DESC, pb.id DESC`, store.Millis(t.o.Now().Add(-incidentsWithin)))
	if err != nil {
		t.o.Logger.Debug("nowplaying: incidents", "err", err)
		return []Incident{}
	}
	out := []Incident{}
	var eps []string
	for rows.Next() {
		var in Incident
		var year sql.NullInt64
		var js, settled, fixed, reported string
		var at int64
		if err := rows.Scan(&in.ID, &in.TitleID, &in.Title, &year, &in.Version, &js, &in.Files, &in.Viewer, &in.Player,
			&settled, &fixed, &reported, &in.OldRelease, &in.NewRelease, &in.Message, &at); err != nil {
			continue
		}
		if year.Valid && year.Int64 > 0 {
			in.Title = fmt.Sprintf("%s (%d)", in.Title, year.Int64)
		}
		in.State, in.Status = incidentState(settled, fixed, reported)
		if in.State != "replaced" {
			in.NewRelease = ""
		}
		in.At = time.UnixMilli(at)
		eps = append(eps, js)
		out = append(out, in)
	}
	rows.Close()
	for i := range out {
		var ids []int64
		_ = json.Unmarshal([]byte(eps[i]), &ids)
		for _, id := range ids {
			var season, number int
			if db.QueryRowContext(ctx, `SELECT season, number FROM episode WHERE id = ?`, id).Scan(&season, &number) == nil {
				out[i].Episodes = append(out[i].Episodes, fmt.Sprintf("S%02dE%02d", season, number))
			}
		}
	}
	return out
}
