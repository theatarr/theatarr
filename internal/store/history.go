package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// History rows (migration 00140, docs/journal.md "Stored History rows") are written when their event happens, in the
// transaction that records it, from the source rows it just wrote: a download's grab, failure and import, a library
// action (an upgrade's replaced files, an adoption, a hand unlink, a missing file), a blocklist entry, a title's
// removal. A row keeps its source's journal id (ref). Writes are best effort: one that fails is logged and the event
// goes on. Migration 00140 backfilled the same rows from the same sources; a test holds the two alike.

// Execer is a transaction or a database handle.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Expressions of History's rows; the grab kind and a library action's title are also the journal's (internal/journal).
const (
	// HistoryGrabKindSQL is a download's kind (alias d): upgrade when an earlier download of the version had imported
	// a file of its episodes, else grab.
	HistoryGrabKindSQL = `CASE WHEN EXISTS (SELECT 1 FROM download p WHERE p.version_id = d.version_id AND p.id <> d.id
			AND p.state = 'imported' AND p.imported_at < d.created_at
			AND (d.episodes = '[]' OR EXISTS (SELECT 1 FROM json_each(p.episodes) pe
				WHERE pe.value IN (SELECT value FROM json_each(d.episodes)))))
	THEN 'upgrade' ELSE 'grab' END`
	// historyGrabbedWhere: the download (alias d) reached a provider or client: past adding, or a failed one that was
	// added, reused or accepted by a provider (a requeue clears added_at).
	historyGrabbedWhere = `(d.state NOT IN ('queued', 'adding', 'failed') OR d.added_at IS NOT NULL OR d.reused = 1
		OR EXISTS (SELECT 1 FROM download_attempt a WHERE a.download_id = d.id AND a.outcome = 'accepted'))`
	// HistoryLibTitleSQL is the title of a library action (alias la).
	HistoryLibTitleSQL = `COALESCE(la.title_id, (SELECT title_id FROM download WHERE id = la.download_id),
	(SELECT title_id FROM link WHERE id = la.link_id), 0)`
	// The link row that still holds an action's file facts, when the action recorded none (rows from before 00129):
	// a replaced file's unlinked row (not taken over since), an adoption's still adopted row, a dead link's still dead
	// row; NULL when none.
	historyOldLinkSQL = `(SELECT l.id FROM link l WHERE l.state = 'unlinked'
		AND (l.id = la.link_id OR (la.link_id IS NULL AND l.path = la.path)) ORDER BY l.id DESC LIMIT 1)`
	historyAdoptLinkSQL = `(SELECT l.id FROM link l WHERE l.id = la.link_id AND l.adopted_from IS NOT NULL)`
	historyDeadLinkSQL  = `(SELECT l.id FROM link l WHERE l.id = la.link_id AND l.state = 'dead')`
)

// HistoryAdoptReleaseSQL is an adoption's release (alias la), as its History group row was keyed by (the journal's
// list folds the group into one line).
var HistoryAdoptReleaseSQL = historyRelSQL(historyAdoptLinkSQL)

// historyRelSQL is a library action's release: recorded, else its fallback link's, else empty.
func historyRelSQL(linkSQL string) string {
	return `COALESCE(la.release_title, (SELECT release_title FROM link WHERE id = ` + linkSQL + `), '')`
}

const historyCols = `ref, at, event, kind, title_id, version_key, download_id, release, info_hash, indexer, size,
	quality, score, formats, episodes, files, replaced, provider, reason, source, import_state, by, detail`

// historyUpsert rewrites a row whose source changed; its flags go, so the journal checks it again (journal.Fill).
const historyUpsert = ` ON CONFLICT (ref) DO UPDATE SET at = excluded.at, event = excluded.event,
	kind = excluded.kind, title_id = excluded.title_id, version_key = excluded.version_key,
	download_id = excluded.download_id, release = excluded.release, info_hash = excluded.info_hash,
	indexer = excluded.indexer, size = excluded.size, quality = excluded.quality, score = excluded.score,
	formats = excluded.formats, episodes = excluded.episodes, files = excluded.files, replaced = excluded.replaced,
	provider = excluded.provider, reason = excluded.reason, source = excluded.source,
	import_state = excluded.import_state, by = excluded.by, detail = excluded.detail, flags = NULL,
	severity = ''`

// A download's release facts (alias d): title, version, id, release, hash, indexer, size, quality, a score only
// with scoring evidence (stored formats, '[]' for a calculated 0, or a score), formats.
const historyDownloadFacts = `d.title_id, d.version_key, d.id, d.release_title, d.info_hash, d.indexer,
	COALESCE(d.size_bytes, 0), d.quality, CASE WHEN d.formats IS NOT NULL OR d.score <> 0 THEN d.score END, d.formats`

// History writes are best effort: a failed one is logged and the event goes on (inside a transaction a failed
// statement undoes only itself).
func historyExec(ctx context.Context, ex Execer, q string, args ...any) {
	if _, err := ex.ExecContext(ctx, q, args...); err != nil {
		slog.Default().Warn("history: row not written", "err", err)
	}
}

// HistoryGrab writes download id's grabbed row once it reached a provider or client (an add, a reused torrent, a
// client's hash), dated when it got there; a download whose add was unmade (a not-cached add deleted and requeued)
// and that no provider took before loses its row.
func HistoryGrab(ctx context.Context, ex Execer, id int64) {
	historyExec(ctx, ex, `INSERT INTO history (`+historyCols+`)
		SELECT 'dl:' || d.id, COALESCE(d.added_at, (SELECT MIN(a.attempted_at) FROM download_attempt a
			WHERE a.download_id = d.id AND a.outcome = 'accepted'), d.created_at), 'grabbed', `+HistoryGrabKindSQL+`,
			`+historyDownloadFacts+`, d.episodes, 0, NULL, COALESCE(d.provider, ''), '', '', '', '', ''
		FROM download d WHERE `+historyGrabbedWhere+` AND d.id = ?`+historyUpsert, id)
	historyExec(ctx, ex, `DELETE FROM history WHERE ref = 'dl:' || ?1 AND NOT EXISTS (SELECT 1 FROM download d
		WHERE d.id = ?1 AND `+historyGrabbedWhere+`)`, id)
}

// HistoryFailure writes the failed rows of downloads ids that are failed.
func HistoryFailure(ctx context.Context, ex Execer, ids ...int64) {
	if len(ids) == 0 {
		return
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	historyExec(ctx, ex, `INSERT INTO history (`+historyCols+`)
		SELECT 'dl:' || d.id || ':fail', d.updated_at, 'failed',
			CASE WHEN d.error LIKE 'stalled%' THEN 'stall' ELSE 'failure' END,
			`+historyDownloadFacts+`, d.episodes, 0, NULL, COALESCE(d.provider, ''), COALESCE(d.error, ''), '', '',
			'', ''
		FROM download d WHERE d.state = 'failed' AND d.id IN (`+Placeholders(len(ids))+`)`+historyUpsert, args...)
}

// HistoryImport writes download id's import row once it linked or partly linked (dl:<id>:import), with the episodes
// its links held (recorded with each link action, else the link's current episodes), never the ones it asked for.
func HistoryImport(ctx context.Context, ex Execer, id int64) {
	historyExec(ctx, ex, `INSERT INTO history (`+historyCols+`)
		SELECT 'dl:' || d.id || ':import', li.updated_at, 'imported', 'import', `+historyDownloadFacts+`,
			(SELECT json_group_array(DISTINCT j.value) FROM library_action a, json_each(COALESCE(a.episodes,
				(SELECT json_group_array(le.episode_id) FROM link_episode le WHERE le.link_id = a.link_id))) j
				WHERE a.kind = 'link' AND a.mode = 'applied' AND a.download_id = d.id),
			0, NULL, COALESCE(d.provider, ''), '', '', li.state, '', ''
		FROM library_import li JOIN download d ON d.id = li.download_id
		WHERE li.state IN ('linked', 'partial') AND d.id = ?`+historyUpsert, id)
}

// HistoryHeld writes download id's held row (dl:<id>:held): an import held for review whose download did not fail,
// its own row, so a later import that links keeps it.
func HistoryHeld(ctx context.Context, ex Execer, id int64) {
	historyExec(ctx, ex, `INSERT INTO history (`+historyCols+`)
		SELECT 'dl:' || d.id || ':held', li.updated_at, 'failed', 'import', `+historyDownloadFacts+`, '[]', 0, NULL,
			COALESCE(d.provider, ''), COALESCE(li.last_error, ''), '', li.state, '', ''
		FROM library_import li JOIN download d ON d.id = li.download_id
		WHERE li.state = 'rejected' AND d.state <> 'failed' AND d.id = ?`+historyUpsert, id)
}

// HistoryBlocklist writes the blocked row of the blocklist entry (version, key), as router.Blocklist stored it: the
// release as blocked, with the blocked download's indexer, size, quality and score.
func HistoryBlocklist(ctx context.Context, ex Execer, version, key string) {
	historyExec(ctx, ex, `INSERT INTO history (`+historyCols+`)
		SELECT 'bl:' || b.rowid, b.created_at, 'blocked', 'blocklist', COALESCE(b.title_id, 0), b.version_key,
			b.download_id, b.release_title, b.info_hash, COALESCE(d.indexer, ''), COALESCE(d.size_bytes, 0),
			COALESCE(d.quality, ''), CASE WHEN d.formats IS NOT NULL OR d.score <> 0 THEN d.score END, NULL, '[]', 0,
			NULL, '', b.reason, b.source, '', '', ''
		FROM router_blocklist b LEFT JOIN download d ON d.id = b.download_id
		WHERE b.version_key = ? AND b.info_hash = ?`+historyUpsert, version, key)
}

// HistoryUnblock keeps the blocked row of the blocklist entry (version, key) about to be deleted under a ref of its
// own (bl:<rowid>:<at>), so a later entry that takes the rowid does not write over it. It leaves the flagged view:
// its journal entry is gone.
func HistoryUnblock(ctx context.Context, ex Execer, version, key string) {
	historyExec(ctx, ex, `UPDATE history SET ref = ref || ':' || at, severity = '' WHERE ref = (SELECT 'bl:' || rowid
		FROM router_blocklist WHERE version_key = ? AND info_hash = ?)`, version, key)
}

// HistoryRemoval writes the title_removed row of journal action id (a Remove title or Remove copy), with who removed
// it and what became of its files.
func HistoryRemoval(ctx context.Context, ex Execer, id int64) {
	historyExec(ctx, ex, `INSERT INTO history (`+historyCols+`)
		SELECT 'act:' || a.id, a.created_at, 'title_removed', 'action', COALESCE(a.title_id, 0), a.version_key, NULL,
			NULL, '', '', 0, '', NULL, NULL, '[]', 0, NULL, '', '', '', '', a.by, a.detail
		FROM journal_action a WHERE a.action IN ('remove_title', 'remove_version') AND a.id = ?`+historyUpsert, id)
}

// HistoryRemovalDetail sets a removal row's detail when its outcome is known (the clean-up).
func HistoryRemovalDetail(ctx context.Context, ex Execer, id int64, detail string) {
	historyExec(ctx, ex, `UPDATE history SET detail = ? WHERE ref = ?`, detail, fmt.Sprint("act:", id))
}

// historyActionClass is the History row a library action gives: deleted (an upgrade's replaced file), adopted,
// removed (a hand unlink), missing (a dead link), or "" for none; as the Where clauses above say.
func historyActionClass(kind, mode string, downloadID int64, detail string) string {
	prefix := func(p string) bool { return len(detail) >= len(p) && strings.EqualFold(detail[:len(p)], p) }
	switch {
	case mode != "applied":
		return ""
	case kind == "replace" && downloadID != 0 && !prefix("import undone"):
		return "deleted"
	case kind == "link" && downloadID == 0 && prefix("adopted from "):
		return "adopted"
	case kind == "replace" && downloadID == 0 && !prefix("change of episodes failed"):
		return "removed"
	case kind == "dead":
		return "missing"
	}
	return ""
}

// historyMember is one library action's facts as History shows them, read with the fallback link linkSQL.
func historyMemberSQL(linkSQL string) string {
	return `SELECT la.id, la.at, ` + HistoryLibTitleSQL + `, COALESCE(la.version_key, ''), la.download_id,
		COALESCE(la.path, ''), ` + historyRelSQL(linkSQL) + `,
		CASE WHEN la.release_title IS NOT NULL OR la.quality IS NOT NULL THEN la.quality
			ELSE (SELECT quality FROM link WHERE id = ` + linkSQL + `) END,
		CASE WHEN la.release_title IS NOT NULL OR la.quality IS NOT NULL THEN la.score
			ELSE (SELECT score FROM link WHERE id = ` + linkSQL + `) END,
		COALESCE(la.episodes, (SELECT json_group_array(le.episode_id) FROM link_episode le WHERE le.link_id =
			COALESCE(` + linkSQL + `, la.link_id, (SELECT id FROM link WHERE path = la.path ORDER BY id DESC LIMIT 1), 0))),
		la.detail FROM library_action la WHERE la.id = ?`
}

type historyMember struct {
	id, at, title int64
	version, path string
	dl            sql.NullInt64
	rel, detail   string
	quality       sql.NullString
	score         sql.NullInt64
	episodes      string
}

// HistoryReplacedFile is one file of an upgrade's row (history.replaced): the library action that moved it aside,
// when, its path, its release (empty and no quality when not known), the episode ids it held.
type HistoryReplacedFile struct {
	ID       int64   `json:"id"`
	At       int64   `json:"at"`
	Path     string  `json:"path"`
	Release  string  `json:"release"`
	Quality  *string `json:"quality"`
	Score    *int64  `json:"score"`
	Episodes []int64 `json:"episodes"`
}

// HistoryLibraryAction writes the History row of library action id (its kind, mode, download and detail), when
// History shows it: a missing file or a hand unlink is its own row; an upgrade's replaced files are one row per
// download and library (Upgraded: N files replaced), the action added to its files; adoptions are one row per title,
// library, release and day.
func HistoryLibraryAction(ctx context.Context, ex Execer, id int64, kind, mode string, downloadID int64, detail string) {
	class := historyActionClass(kind, mode, downloadID, detail)
	if class == "" {
		return
	}
	if err := historyLibraryAction(ctx, ex, id, class); err != nil {
		slog.Default().Warn("history: row not written", "action", id, "err", err)
	}
}

func historyLibraryAction(ctx context.Context, ex Execer, id int64, class string) error {
	link := map[string]string{"deleted": historyOldLinkSQL, "removed": historyOldLinkSQL,
		"adopted": historyAdoptLinkSQL, "missing": historyDeadLinkSQL}[class]
	var m historyMember
	if err := ex.QueryRowContext(ctx, historyMemberSQL(link), id).Scan(&m.id, &m.at, &m.title, &m.version, &m.dl,
		&m.path, &m.rel, &m.quality, &m.score, &m.episodes, &m.detail); err != nil {
		return historyErr(err)
	}
	// A row's release: none when neither a release nor a quality is known.
	var rel any
	if m.rel != "" || m.quality.Valid {
		rel = m.rel
	}
	insert := func(event string, files int, replaced any) error {
		_, err := ex.ExecContext(ctx, `INSERT INTO history (`+historyCols+`) VALUES (?, ?, ?, 'link', ?, ?, ?, ?, '',
			'', 0, ?, ?, NULL, ?, ?, ?, '', '', '', '', '', ?)`+historyUpsert, fmt.Sprint("lib:", m.id), m.at, event,
			m.title, m.version, m.dl, rel, m.quality.String, m.score, m.episodes, files, replaced, m.detail)
		return historyErr(err)
	}
	// The episodes of a group row: its own and the new action's.
	const union = `(SELECT json_group_array(DISTINCT value) FROM (SELECT value FROM json_each(history.episodes)
		UNION ALL SELECT value FROM json_each(?)))`
	switch class {
	case "removed", "missing":
		return insert(class, 1, nil)
	case "adopted":
		res, err := ex.ExecContext(ctx, `UPDATE history SET at = MAX(at, ?), files = files + 1, episodes = `+union+`
			WHERE event = 'adopted' AND title_id = ? AND version_key = ? AND COALESCE(release, '') = ?
			AND at / 86400000 = ? / 86400000`, m.at, m.episodes, m.title, m.version, m.rel, m.at)
		if err != nil {
			return historyErr(err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return nil
		}
		return insert(class, 1, nil)
	}
	// An upgrade's replaced file: added to its download's row in its library, which names what replaced it.
	f := HistoryReplacedFile{ID: m.id, At: m.at, Path: m.path, Release: m.rel, Episodes: []int64{}}
	_ = json.Unmarshal([]byte(m.episodes), &f.Episodes)
	if m.quality.Valid {
		f.Quality = &m.quality.String
	}
	if m.score.Valid {
		f.Score = &m.score.Int64
	}
	fj, err := json.Marshal(f)
	if err != nil {
		return err
	}
	res, err := ex.ExecContext(ctx, `UPDATE history SET at = MAX(at, ?), files = files + 1,
		replaced = json_insert(COALESCE(replaced, '[]'), '$[#]', json(?)), episodes = `+union+`
		WHERE event = 'deleted' AND download_id = ? AND version_key = ?`, m.at, string(fj), m.episodes, m.dl, m.version)
	if err != nil {
		return historyErr(err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	_, err = ex.ExecContext(ctx, `INSERT INTO history (`+historyCols+`)
		SELECT ?, ?, 'deleted', 'link', COALESCE(d.title_id, 0), ?, ?, d.release_title, '', '', 0,
			COALESCE(d.quality, ''), CASE WHEN d.formats IS NOT NULL OR d.score <> 0 THEN d.score END, NULL,
			(SELECT json_group_array(DISTINCT value) FROM json_each(?)), 1, json_array(json(?)),
			COALESCE(d.provider, ''), '', '', '', '', ''
		FROM (SELECT ? AS id) x LEFT JOIN download d ON d.id = x.id WHERE true`+historyUpsert,
		fmt.Sprint("lib:", m.id), m.at, m.version, m.dl, m.episodes, string(fj), m.dl)
	return historyErr(err)
}

func historyErr(err error) error {
	if err == nil || err == sql.ErrNoRows {
		return nil
	}
	return fmt.Errorf("store: history: %w", err)
}

// BackfillHistory writes every History row from its sources into an empty history table with migration 00140's own
// statements (but the copy events', whose table is gone): for a database whose sources were written directly (the
// demo seed).
func BackfillHistory(ctx context.Context, ex Execer) error {
	b, err := migrationFiles.ReadFile("migrations/00140_history.sql")
	if err != nil {
		return err
	}
	_, up, _ := strings.Cut(string(b), "-- +goose Up")
	up, _, _ = strings.Cut(up, "-- +goose Down")
	for _, q := range strings.Split(up, ";\n") {
		var keep []string
		for _, line := range strings.Split(q, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "--") {
				keep = append(keep, line)
			}
		}
		q = strings.TrimSpace(strings.Join(keep, "\n"))
		// Copy events: their table went in 00144 (their rows stay in history).
		if !strings.HasPrefix(q, "INSERT") && !strings.HasPrefix(q, "WITH") || strings.Contains(q, "provider_copy_event") {
			continue
		}
		if _, err := ex.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("store: history backfill: %w", err)
		}
	}
	return nil
}
