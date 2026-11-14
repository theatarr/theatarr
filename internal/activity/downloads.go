// Package activity is the shared reader behind the arr views' queue (internal/arrview/queue.go) and the native
// API's activity, calendar and Home "Continue" row (internal/web/activity.go): the router's download table with each
// download's title and library import, the steps a download walks through, its state in plain words, the
// blocklist, and the release dates and air dates of monitored titles. It only reads.
//
// History is not here: it is the grab journal (internal/journal), which the web layer lists with the kinds that
// make up a history (grabs, imports, failures, stalls, blocklists, re-searches).
package activity

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// States in hand: downloads that are still running. Activity also lists undismissed failures.
var InHand = []string{"queued", "adding", "added", "downloading", "ready", "importing"}

// QueueFailureSQL selects failed downloads still needing attention in the arr queue. The download alias is d.
// Failed attempts stay in history; queue expiry never deletes or changes their ownership evidence.
const QueueFailureSQL = `d.state='failed' AND d.dismissed_at IS NULL AND d.updated_at > ?
 AND (d.error LIKE 'stalled:%' OR (d.error LIKE 'library:%' AND li.state IN ('rejected','failed')))
 AND NOT EXISTS (SELECT 1 FROM download newer WHERE newer.title_id=d.title_id
  AND newer.version_key=d.version_key AND newer.id>d.id AND newer.state='imported')`

// NativeQueueFailureSQL selects the failed downloads Activity's queue and System › Status still show (the arr queue
// keeps QueueFailureSQL). The download alias is d and its title t (Downloads joins it); its one argument is the 7-day
// cutoff (ms). A miss on every provider and a download never started (NotStarted) belong to History only. Any other
// failure (stalled, refused by the library, not imported in time, a provider error) shows until it is covered, for at
// most 7 days: a movie when its title and library hold an active link from an imported download newer than the
// failure; a series download when each of its episodes (download.episodes, and a manual import's download_file
// episodes) holds such a link (any newer download: a pack, then an upgrade of one episode, still covers it). A series
// download whose episodes are not known is never covered. It only reads: stored attempts and ownership evidence are
// untouched.
var NativeQueueFailureSQL = `d.state='failed' AND d.dismissed_at IS NULL AND d.updated_at > ?
 AND COALESCE(d.error, '') NOT GLOB 'miss on every provider*'
 AND NOT ` + notStartedSQL("d") + `
 AND ((t.kind = 'movie' AND NOT EXISTS (` + newerLinkSQL + `))
  OR (COALESCE(t.kind, '') <> 'movie' AND (NOT EXISTS (` + scopeEpisodesSQL + `)
   OR EXISTS (SELECT 1 FROM (` + scopeEpisodesSQL + `) e WHERE NOT EXISTS (` + newerLinkSQL + `
    AND EXISTS (SELECT 1 FROM link_episode le WHERE le.link_id = l.id AND le.episode_id = e.value))))))`

// newerLinkSQL selects the active links of d's title and library that an imported download newer than d made.
const newerLinkSQL = `SELECT 1 FROM link l JOIN download n ON n.id = l.download_id WHERE l.state = 'active'
  AND n.title_id = d.title_id AND n.version_key = d.version_key AND n.id > d.id AND n.state = 'imported'`

// scopeEpisodesSQL selects the episodes download d is for (value): its own list, and a manual import's per file.
const scopeEpisodesSQL = `SELECT value FROM json_each(d.episodes)
  UNION SELECT j.value FROM download_file f, json_each(f.episodes) j WHERE f.download_id = d.id`

// Download is one download row with its title and its library import (migrations 00014, 00015).
type Download struct {
	ID, TitleID, VersionID int64
	Version, Hash, Release string
	Indexer, Quality       string
	Score                  int
	Size                   int64
	Episodes               []int64
	Formats                string // the routed decision's matched custom formats (download.formats, JSON); "" when none
	Provider, TorrentID    string
	Client                 string // the download client of a client-routed download (add_via: client)
	Name                   string
	Progress               float64
	State, Error, Source   string
	Cached                 *bool
	Reused                 bool
	Created, Updated       time.Time
	Added, Ready, Imported time.Time // zero when not reached
	TitleName, TitleKind   string
	TitleYear              int
	Anime                  bool
	ImportState, ImportErr string // library_import: waiting_mount, linked, partial, rejected, dry_run, failed; "" when none
	WaitingSince           time.Time
	ImportAttempts         int
	ImportUpdated          time.Time // library_import.updated_at; zero without one
	RetryAt                time.Time // the latest provider attempt's retry_at (deferred or paced); zero when none
	tried                  bool      // failed rows only: an attempt shows it was tried at a provider or client (Tried)
}

// Filter selects downloads. Every field is optional; empty means any.
type Filter struct {
	// IDs, when non-nil, selects only these downloads (an empty list selects none).
	IDs      []int64
	Versions []string
	TitleIDs []int64
	States   []string
	// ArrQueueAt applies the arr queue's failure expiry/replacement rules when nonzero.
	ArrQueueAt time.Time
	// QueueAt, when nonzero, selects Activity's queue: downloads in hand and the failures NativeQueueFailureSQL
	// keeps.
	QueueAt time.Time
	// Undismissed hides downloads explicitly dismissed in Activity.
	Undismissed bool
	// UpdatedSince keeps downloads updated at or after it (the event poller's cursor).
	UpdatedSince time.Time
	// Limit caps the rows (0: no cap).
	Limit int
}

// Downloads reads the downloads f selects, oldest first (by id).
func Downloads(ctx context.Context, db *sql.DB, f Filter) ([]Download, error) {
	q := `SELECT d.id, d.title_id, d.version_id, d.version_key, d.info_hash, d.release_title, d.indexer, d.quality,
		d.score, COALESCE(d.size_bytes, 0), d.episodes, COALESCE(d.provider, ''), COALESCE(d.provider_torrent_id, ''),
		COALESCE(d.client, ''), d.name, d.progress, d.state, COALESCE(d.error, ''), d.source, d.cached, d.reused, d.created_at, d.updated_at,
		COALESCE(d.added_at, 0), COALESCE(d.ready_at, 0), COALESCE(d.imported_at, 0), COALESCE(d.formats, ''),
		COALESCE(t.name, ''), COALESCE(t.kind, ''), COALESCE(t.year, 0), COALESCE(t.anime, 0),
		COALESCE(li.state, ''), COALESCE(li.last_error, ''), COALESCE(li.waiting_since, 0), COALESCE(li.attempts, 0),
		COALESCE(li.updated_at, 0), COALESCE((SELECT a.retry_at FROM download_attempt a WHERE a.download_id = d.id
			ORDER BY a.id DESC LIMIT 1), 0), ` + triedColumnSQL + `
		FROM download d LEFT JOIN title t ON t.id = d.title_id LEFT JOIN library_import li ON li.download_id = d.id
		WHERE 1 = 1`
	var args []any
	if !f.ArrQueueAt.IsZero() {
		q += ` AND (d.state IN ('queued','adding','added','downloading','ready','importing') OR (` + QueueFailureSQL + `))`
		args = append(args, store.Millis(f.ArrQueueAt.Add(-7*24*time.Hour)))
	}
	if !f.QueueAt.IsZero() {
		q += ` AND (d.state IN ('queued','adding','added','downloading','ready','importing') OR (` + NativeQueueFailureSQL + `))`
		args = append(args, store.Millis(f.QueueAt.Add(-7*24*time.Hour)))
	}
	if f.Undismissed {
		q += ` AND d.dismissed_at IS NULL`
	}
	if f.IDs != nil {
		b, _ := json.Marshal(f.IDs)
		q += ` AND d.id IN (SELECT value FROM json_each(?))`
		args = append(args, string(b))
	}
	if len(f.States) > 0 {
		q += ` AND d.state IN (` + store.Placeholders(len(f.States)) + `)`
		for _, s := range f.States {
			args = append(args, s)
		}
	}
	if len(f.Versions) > 0 {
		q += ` AND d.version_key IN (` + store.Placeholders(len(f.Versions)) + `)`
		for _, k := range f.Versions {
			args = append(args, k)
		}
	}
	if len(f.TitleIDs) > 0 {
		q += ` AND d.title_id IN (` + store.Placeholders(len(f.TitleIDs)) + `)`
		for _, id := range f.TitleIDs {
			args = append(args, id)
		}
	}
	if !f.UpdatedSince.IsZero() {
		q += ` AND d.id IN (SELECT id FROM download WHERE updated_at >= ?
			UNION ALL SELECT download_id FROM library_import WHERE updated_at >= ?)`
		args = append(args, store.Millis(f.UpdatedSince), store.Millis(f.UpdatedSince))
	}
	q += ` ORDER BY d.id`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Download
	for rows.Next() {
		var x Download
		var eps string
		var cached sql.NullInt64
		var created, updated, added, ready, imported, waiting, liUpdated, retry int64
		if err := rows.Scan(&x.ID, &x.TitleID, &x.VersionID, &x.Version, &x.Hash, &x.Release, &x.Indexer, &x.Quality,
			&x.Score, &x.Size, &eps, &x.Provider, &x.TorrentID, &x.Client, &x.Name, &x.Progress, &x.State, &x.Error, &x.Source,
			&cached, &x.Reused, &created, &updated, &added, &ready, &imported, &x.Formats, &x.TitleName, &x.TitleKind, &x.TitleYear,
			&x.Anime, &x.ImportState, &x.ImportErr, &waiting, &x.ImportAttempts, &liUpdated, &retry, &x.tried); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(eps), &x.Episodes)
		if cached.Valid {
			c := cached.Int64 == 1
			x.Cached = &c
		}
		x.Created, x.Updated = at(created), at(updated)
		x.Added, x.Ready, x.Imported, x.WaitingSince = at(added), at(ready), at(imported), at(waiting)
		x.ImportUpdated, x.RetryAt = at(liUpdated), at(retry)
		out = append(out, x)
	}
	return out, rows.Err()
}

func at(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return store.FromMillis(ms)
}

// LatestUpdate is the newest updated_at of any download or library import: the event poller's change marker.
func LatestUpdate(ctx context.Context, db *sql.DB) (time.Time, error) {
	var d, li int64
	err := db.QueryRowContext(ctx, `SELECT COALESCE((SELECT MAX(updated_at) FROM download), 0),
		COALESCE((SELECT MAX(updated_at) FROM library_import), 0)`).Scan(&d, &li)
	return at(max(d, li)), err
}
