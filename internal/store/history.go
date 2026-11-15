package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// History rows (migration 00140, docs/design/ui.md "Activity", History) are written when their event happens, in the
// transaction that records it, from the source rows it just wrote: a download's grab, failure and import, a library
// action (an upgrade's replaced files, an adoption, a hand unlink, a missing file), a blocklist entry, a title's
// removal. A row keeps its source's journal id (ref) and is rewritten when its source changes the same way the
// derived History reader would show it (a download failing again, a removal's clean-up finishing). Migration 00140
// backfills the same rows from the same statements (the library actions grouped in SQL); a test holds them equal.

// Execer is a transaction or a database handle.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Where clauses and expressions History shares with the derived reader (internal/journal history.go).
const (
	// HistoryGrabKindSQL is a download's kind (alias d): upgrade when an earlier download of the version had imported
	// a file of its episodes, else grab.
	HistoryGrabKindSQL = `CASE WHEN EXISTS (SELECT 1 FROM download p WHERE p.version_id = d.version_id AND p.id <> d.id
			AND p.state = 'imported' AND p.imported_at < d.created_at
			AND (d.episodes = '[]' OR EXISTS (SELECT 1 FROM json_each(p.episodes) pe
				WHERE pe.value IN (SELECT value FROM json_each(d.episodes)))))
	THEN 'upgrade' ELSE 'grab' END`
	// HistoryGrabbedWhere: the download (alias d) reached a provider or client: past adding, or a failed one that was
	// added, reused or accepted by a provider (a requeue clears added_at).
	HistoryGrabbedWhere = `(d.state NOT IN ('queued', 'adding', 'failed') OR d.added_at IS NOT NULL OR d.reused = 1
		OR EXISTS (SELECT 1 FROM download_attempt a WHERE a.download_id = d.id AND a.outcome = 'accepted'))`
	// The library actions (alias la) History shows: an upgrade's replaced files, an adoption, a hand unlink, a missing
	// file.
	HistoryDeletedWhere = `la.kind = 'replace' AND la.mode = 'applied' AND la.download_id IS NOT NULL
		AND la.detail NOT LIKE 'import undone%'`
	HistoryAdoptedWhere = `la.kind = 'link' AND la.mode = 'applied' AND la.download_id IS NULL AND la.detail LIKE 'adopted from %'`
	HistoryRemovedWhere = `la.kind = 'replace' AND la.mode = 'applied' AND la.download_id IS NULL
		AND la.detail NOT LIKE 'change of episodes failed%'`
	HistoryMissingWhere = `la.kind = 'dead' AND la.mode = 'applied'`
	// HistoryLibTitleSQL is the title of a library action (alias la).
	HistoryLibTitleSQL = `COALESCE(la.title_id, (SELECT title_id FROM download WHERE id = la.download_id),
	(SELECT title_id FROM link WHERE id = la.link_id), 0)`
	// The link row that still holds an action's file facts, when the action recorded none (rows from before 00129):
	// a replaced file's unlinked row (not taken over since), an adoption's still adopted row, a dead link's still dead
	// row; NULL when none.
	HistoryOldLinkSQL = `(SELECT l.id FROM link l WHERE l.state = 'unlinked'
		AND (l.id = la.link_id OR (la.link_id IS NULL AND l.path = la.path)) ORDER BY l.id DESC LIMIT 1)`
	HistoryAdoptLinkSQL = `(SELECT l.id FROM link l WHERE l.id = la.link_id AND l.adopted_from IS NOT NULL)`
	HistoryDeadLinkSQL  = `(SELECT l.id FROM link l WHERE l.id = la.link_id AND l.state = 'dead')`
)

// HistoryRelSQL is a library action's release: recorded, else its fallback link's, else empty.
func HistoryRelSQL(linkSQL string) string {
	return `COALESCE(la.release_title, (SELECT release_title FROM link WHERE id = ` + linkSQL + `), '')`
}

const historyCols = `ref, at, event, kind, title_id, version_key, download_id, release, info_hash, indexer, size,
	quality, score, formats, episodes, files, replaced, provider, reason, source, import_state, by, detail`

// historyUpsert rewrites a row whose source changed.
const historyUpsert = ` ON CONFLICT (ref) DO UPDATE SET at = excluded.at, event = excluded.event,
	kind = excluded.kind, title_id = excluded.title_id, version_key = excluded.version_key,
	download_id = excluded.download_id, release = excluded.release, info_hash = excluded.info_hash,
	indexer = excluded.indexer, size = excluded.size, quality = excluded.quality, score = excluded.score,
	formats = excluded.formats, episodes = excluded.episodes, files = excluded.files, replaced = excluded.replaced,
	provider = excluded.provider, reason = excluded.reason, source = excluded.source,
	import_state = excluded.import_state, by = excluded.by, detail = excluded.detail`

// A download's release facts (alias d): title, version, id, release, hash, indexer, size, quality, a score only
// with scoring evidence (stored formats, '[]' for a calculated 0, or a score), formats.
const historyDownloadFacts = `d.title_id, d.version_key, d.id, d.release_title, d.info_hash, d.indexer,
	COALESCE(d.size_bytes, 0), d.quality, CASE WHEN d.formats IS NOT NULL OR d.score <> 0 THEN d.score END, d.formats`

// historyDownloadSQL are the statements writing the grabbed, failed and import rows of the downloads (alias d) that
// filter selects.
func historyDownloadSQL(filter string) []string {
	return []string{
		`INSERT INTO history (` + historyCols + `)
		SELECT 'dl:' || d.id, COALESCE(d.added_at, (SELECT MIN(a.attempted_at) FROM download_attempt a
			WHERE a.download_id = d.id AND a.outcome = 'accepted'), d.created_at), 'grabbed', ` + HistoryGrabKindSQL + `,
			` + historyDownloadFacts + `, d.episodes, 0, NULL, COALESCE(d.provider, ''), '', '', '', '', ''
		FROM download d WHERE ` + HistoryGrabbedWhere + ` AND ` + filter + historyUpsert,
		`INSERT INTO history (` + historyCols + `)
		SELECT 'dl:' || d.id || ':fail', d.updated_at, 'failed',
			CASE WHEN d.error LIKE 'stalled%' THEN 'stall' ELSE 'failure' END,
			` + historyDownloadFacts + `, d.episodes, 0, NULL, COALESCE(d.provider, ''), COALESCE(d.error, ''), '', '',
			'', ''
		FROM download d WHERE d.state = 'failed' AND ` + filter + historyUpsert,
		// An import that linked, or one held for review whose download did not fail; its episodes are those its links
		// held (recorded with each link action, else the link's current episodes), never the ones it asked for.
		`INSERT INTO history (` + historyCols + `)
		SELECT 'dl:' || d.id || ':import', li.updated_at, CASE li.state WHEN 'rejected' THEN 'failed' ELSE 'imported' END,
			'import', ` + historyDownloadFacts + `,
			(SELECT json_group_array(DISTINCT j.value) FROM library_action a, json_each(COALESCE(a.episodes,
				(SELECT json_group_array(le.episode_id) FROM link_episode le WHERE le.link_id = a.link_id))) j
				WHERE a.kind = 'link' AND a.mode = 'applied' AND a.download_id = d.id),
			0, NULL, COALESCE(d.provider, ''), CASE li.state WHEN 'rejected' THEN COALESCE(li.last_error, '') ELSE '' END,
			'', li.state, '', ''
		FROM library_import li JOIN download d ON d.id = li.download_id
		WHERE (li.state IN ('linked', 'partial') OR (li.state = 'rejected' AND d.state <> 'failed')) AND ` + filter +
			historyUpsert,
	}
}

// historyBlocklistSQL writes the blocked rows of the blocklist entries (alias b) filter selects: the release as
// blocked, with the blocked download's indexer, size, quality and score.
func historyBlocklistSQL(filter string) string {
	return `INSERT INTO history (` + historyCols + `)
	SELECT 'bl:' || b.rowid, b.created_at, 'blocked', 'blocklist', COALESCE(b.title_id, 0), b.version_key,
		b.download_id, b.release_title, b.info_hash, COALESCE(d.indexer, ''), COALESCE(d.size_bytes, 0),
		COALESCE(d.quality, ''), CASE WHEN d.formats IS NOT NULL OR d.score <> 0 THEN d.score END, NULL, '[]', 0, NULL,
		'', b.reason, b.source, '', '', ''
	FROM router_blocklist b LEFT JOIN download d ON d.id = b.download_id WHERE ` + filter + historyUpsert
}

// historyRemovalSQL writes the title_removed rows of the journal actions (alias a) filter selects (Remove title,
// Remove copy), with who removed it and what became of its files.
func historyRemovalSQL(filter string) string {
	return `INSERT INTO history (` + historyCols + `)
	SELECT 'act:' || a.id, a.created_at, 'title_removed', 'action', COALESCE(a.title_id, 0), a.version_key, NULL, NULL,
		'', '', 0, '', NULL, NULL, '[]', 0, NULL, '', '', '', '', a.by, a.detail
	FROM journal_action a WHERE a.action IN ('remove_title', 'remove_version') AND ` + filter + historyUpsert
}

// historyCopySQL writes the rows of finished provider copies ("Add to other providers"); only the backfill writes
// them.
const historyCopySQL = `INSERT INTO history (` + historyCols + `)
	SELECT 'copy:' || c.id, c.at, CASE c.state WHEN 'added' THEN 'copy_added' WHEN 'already' THEN 'copy_already'
		ELSE 'copy_not_cached' END, 'copy', COALESCE(c.title_id, 0), '', NULL, c.name, c.info_hash, '', 0,
		COALESCE(c.quality, ''), c.score, NULL, '[]', 0, NULL, c.provider, c.message, '', '', '', ''
	FROM provider_copy_event c WHERE true` + historyUpsert

// HistoryDownloads writes the History rows of downloads ids as they stand: its grab once it reached a provider or
// client, its failure, its import. Called after a download changes state or its import is recorded.
func HistoryDownloads(ctx context.Context, ex Execer, ids ...int64) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	for _, q := range historyDownloadSQL(`d.id IN (` + Placeholders(len(ids)) + `)`) {
		if _, err := ex.ExecContext(ctx, q, args...); err != nil {
			return fmt.Errorf("store: history: %w", err)
		}
	}
	return nil
}

// HistoryBlocklist writes the blocked row of the blocklist entry (version, key), as router.Blocklist stored it.
func HistoryBlocklist(ctx context.Context, ex Execer, version, key string) error {
	if _, err := ex.ExecContext(ctx, historyBlocklistSQL(`b.version_key = ? AND b.info_hash = ?`), version,
		key); err != nil {
		return fmt.Errorf("store: history: %w", err)
	}
	return nil
}

// HistoryRemoval writes the title_removed row of journal action id, when it is a removal; again when its detail
// changes (the clean-up's outcome).
func HistoryRemoval(ctx context.Context, ex Execer, id int64) error {
	if _, err := ex.ExecContext(ctx, historyRemovalSQL(`a.id = ?`), id); err != nil {
		return fmt.Errorf("store: history: %w", err)
	}
	return nil
}

// historyMember is one library action's facts as History shows them, read with the fallback link linkSQL.
func historyMemberSQL(linkSQL string) string {
	return `SELECT la.id, la.at, ` + HistoryLibTitleSQL + `, COALESCE(la.version_key, ''), la.download_id,
		COALESCE(la.path, ''), ` + HistoryRelSQL(linkSQL) + `,
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
	ID       int64           `json:"id"`
	At       int64           `json:"at"`
	Path     string          `json:"path"`
	Release  string          `json:"release"`
	Quality  *string         `json:"quality"`
	Score    *int64          `json:"score"`
	Episodes json.RawMessage `json:"episodes"`
}

// HistoryLibraryAction writes the History row of library action id, when History shows it: a missing file or a hand
// unlink is its own row; an upgrade's replaced files are one row per download and library (Upgraded: N files
// replaced), the action added to its files; adoptions are one row per title, library, release and day.
func HistoryLibraryAction(ctx context.Context, ex Execer, id int64) error {
	var class string
	err := ex.QueryRowContext(ctx, `SELECT CASE WHEN `+HistoryDeletedWhere+` THEN 'deleted'
		WHEN `+HistoryAdoptedWhere+` THEN 'adopted' WHEN `+HistoryRemovedWhere+` THEN 'removed'
		WHEN `+HistoryMissingWhere+` THEN 'missing' ELSE '' END FROM library_action la WHERE la.id = ?`, id).Scan(&class)
	if err != nil || class == "" {
		return historyErr(err)
	}
	link := map[string]string{"deleted": HistoryOldLinkSQL, "removed": HistoryOldLinkSQL,
		"adopted": HistoryAdoptLinkSQL, "missing": HistoryDeadLinkSQL}[class]
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
	f := HistoryReplacedFile{ID: m.id, At: m.at, Path: m.path, Release: m.rel, Episodes: json.RawMessage(m.episodes)}
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

// BackfillHistory writes every History row from its sources, as migration 00140 does: for a database whose sources
// were written directly (the demo seed) and for tests.
func BackfillHistory(ctx context.Context, ex Execer) error {
	qs := append(historyDownloadSQL(`true`), historyBlocklistSQL(`true`), historyRemovalSQL(`true`), historyCopySQL)
	for _, q := range qs {
		if _, err := ex.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("store: history backfill: %w", err)
		}
	}
	rows, err := ex.QueryContext(ctx, `SELECT la.id FROM library_action la WHERE (`+strings.Join([]string{
		HistoryDeletedWhere, HistoryAdoptedWhere, HistoryRemovedWhere, HistoryMissingWhere}, `) OR (`)+`) ORDER BY la.id`)
	if err != nil {
		return fmt.Errorf("store: history backfill: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := HistoryLibraryAction(ctx, ex, id); err != nil {
			return err
		}
	}
	return nil
}
