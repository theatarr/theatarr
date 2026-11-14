package journal

import (
	"database/sql"
	"slices"
	"strconv"
	"strings"
)

// History (docs/design/ui.md "Activity", History) is the journal read as what happened to a title's files, worded like
// Radarr's: Filter.History lists these events only, one row each, and sets Entry.Hist. Activity › History and the title
// page's History tab both read it (GET /activity?state=history), so they cannot disagree.
//
//   - grabbed: a download (journal kinds grab and upgrade both; the journal's classification is not changed);
//   - imported: a download's import that linked (linked or partial), one row per import, not one per link;
//   - adopted: files taken over from Sonarr/Radarr, one row per library, release and day;
//   - deleted: files an import moved aside for an upgrade, one row per import and old release;
//   - removed: a link taken out by hand (an unlink, recorded as a replace without a download);
//   - missing: a link whose file is gone (dead);
//   - failed: a failed or stalled download, or an import held for review whose download did not fail;
//   - blocked: a release put on a version's blocklist;
//   - copy_added, copy_not_cached: a copy to another provider ("Add to other providers") that ended so.
//
// Searches, dry runs, mount waits, repaired links and plans stay in the journal only. The release facts of library
// rows are those recorded with the action (migration 00129); older rows fall back to a link row that still holds the
// same file (an unlinked row, or an adopted one), and are left unknown otherwise.

// History events.
const (
	EvGrabbed       = "grabbed"
	EvImported      = "imported"
	EvAdopted       = "adopted"
	EvDeleted       = "deleted"
	EvRemoved       = "removed"
	EvMissing       = "missing"
	EvFailed        = "failed"
	EvBlocked       = "blocked"
	EvCopyAdded     = "copy_added"
	EvCopyNotCached = "copy_not_cached"
)

// HistoryEvents lists every History event, for filters.
var HistoryEvents = []string{EvGrabbed, EvImported, EvAdopted, EvDeleted, EvRemoved, EvMissing, EvFailed, EvBlocked,
	EvCopyAdded, EvCopyNotCached}

// KindCopy is the kind of a provider copy's History row (copy:<provider_copy_event>); not a journal kind.
const KindCopy = "copy"

// HistoryFacts are what a History row shows.
type HistoryFacts struct {
	Event string
	// Release is the file the event is about: the grabbed, imported, failed or blocked release; the old file of a
	// deleted or removed row; the adopted file; the copied torrent. Nil when not known.
	Release *Release
	// ReplacedBy is a deleted row's new release (the download that replaced it).
	ReplacedBy *Release
	// Episodes are the row's episodes (S01E02); Files the files it stands for (adopted, deleted).
	Episodes []string
	Files    int
	Provider string
	// Reason: a failure's error, a block's reason, an unlink's reason, a copy's message. Source: a block's source.
	Reason string
	Source string
}

// The title of a library action (as the journal's head).
const libTitleSQL = `COALESCE((SELECT title_id FROM download WHERE id = la.download_id),
	(SELECT title_id FROM link WHERE id = la.link_id), (SELECT vf.title_id FROM version_file vf WHERE vf.path = la.path LIMIT 1), 0)`

// The link row that still holds a replaced file's facts (an unlinked row: not taken over since) and an adoption's
// (still adopted); NULL when none.
const (
	oldLinkSQL = `(SELECT l.id FROM link l WHERE l.state = 'unlinked'
		AND (l.id = la.link_id OR (la.link_id IS NULL AND l.path = la.path)) ORDER BY l.id DESC LIMIT 1)`
	adoptLinkSQL = `(SELECT l.id FROM link l WHERE l.id = la.link_id AND l.adopted_from IS NOT NULL)`
)

// relSQL is an action's release: recorded, else its fallback link's, else empty.
func relSQL(linkSQL string) string {
	return `COALESCE(la.release_title, (SELECT release_title FROM link WHERE id = ` + linkSQL + `), '')`
}

const (
	deletedWhere = `la.kind = 'replace' AND la.mode = 'applied' AND la.download_id IS NOT NULL
		AND la.detail NOT LIKE 'import undone%'`
	adoptedWhere = `la.kind = 'link' AND la.mode = 'applied' AND la.download_id IS NULL AND la.detail LIKE 'adopted from %'`
	removedWhere = `la.kind = 'replace' AND la.mode = 'applied' AND la.download_id IS NULL
		AND la.detail NOT LIKE 'change of episodes failed%'`
	missingWhere = `la.kind = 'dead' AND la.mode = 'applied'`
)

// historyHeadSQL is History's union: (id, at, title_id, version_key, kind, event).
var historyHeadSQL = strings.Join([]string{
	`SELECT 'dl:' || d.id AS id, d.created_at AS at, d.title_id AS title_id, d.version_key AS version_key, ` +
		grabKindSQL + ` AS kind, 'grabbed' AS event FROM download d`,
	`SELECT 'dl:' || d.id || ':fail', d.updated_at, d.title_id, d.version_key,
	CASE WHEN d.error LIKE 'stalled%' THEN 'stall' ELSE 'failure' END, 'failed' FROM download d WHERE d.state = 'failed'`,
	`SELECT 'dl:' || li.download_id || ':import', li.updated_at, d.title_id, d.version_key, 'import',
	CASE li.state WHEN 'rejected' THEN 'failed' ELSE 'imported' END
FROM library_import li JOIN download d ON d.id = li.download_id
WHERE li.state IN ('linked', 'partial') OR (li.state = 'rejected' AND d.state <> 'failed')`,
	`SELECT 'lib:' || la.id, la.at, ` + libTitleSQL + `, COALESCE(la.version_key, ''), 'link',
	CASE la.kind WHEN 'dead' THEN 'missing' ELSE 'removed' END
FROM library_action la WHERE (` + removedWhere + `) OR (` + missingWhere + `)`,
	`SELECT 'lib:' || MIN(la.id), MAX(la.at), (SELECT title_id FROM download WHERE id = la.download_id),
	COALESCE(la.version_key, ''), 'link', 'deleted'
FROM library_action la WHERE ` + deletedWhere + `
GROUP BY la.download_id, la.version_key, ` + relSQL(oldLinkSQL),
	`SELECT 'lib:' || MIN(la.id), MAX(la.at), ` + libTitleSQL + `, COALESCE(la.version_key, ''), 'link', 'adopted'
FROM library_action la WHERE ` + adoptedWhere + `
GROUP BY ` + libTitleSQL + `, la.version_key, ` + relSQL(adoptLinkSQL) + `, la.at / 86400000`,
	`SELECT 'bl:' || b.rowid, b.created_at, COALESCE(b.title_id, 0), b.version_key, 'blocklist', 'blocked'
FROM router_blocklist b`,
	`SELECT 'copy:' || c.id, c.at, COALESCE(c.title_id, 0), '', 'copy',
	CASE c.state WHEN 'added' THEN 'copy_added' ELSE 'copy_not_cached' END FROM provider_copy_event c`,
}, "\nUNION ALL\n")

// history sets e.Hist for a History row (Filter.History).
func (b *builder) history(e *Entry, h head) error {
	if e.Hist != nil { // a copy: copyEntry set it
		e.Hist.Event = h.event
		return nil
	}
	x := &HistoryFacts{Event: h.event, Release: e.Release}
	if e.Download != nil {
		x.Provider = e.Download.Provider
	}
	if e.Release != nil {
		x.Episodes = e.Release.Episodes
	}
	e.Hist = x
	prefix, rest, _ := strings.Cut(h.id, ":")
	switch h.event {
	case EvFailed:
		if e.Download != nil {
			x.Reason = e.Download.Error
		}
		if e.Kind == KindImport && e.Import != nil {
			x.Reason = "Import held for review: " + e.Import.Error
		}
	case EvBlocked:
		var source, reason string
		if err := b.db.QueryRowContext(b.ctx, `SELECT source, reason FROM router_blocklist WHERE rowid = ?`,
			rest).Scan(&source, &reason); err != nil {
			return err
		}
		x.Source, x.Reason = source, cleanMessage(reason)
	default:
		if prefix == "lib" {
			return b.libHistory(e, x, h, rest)
		}
	}
	return nil
}

// libHistory fills a library row's facts from its actions: one, or the group the head stands for (adopted, deleted).
func (b *builder) libHistory(e *Entry, x *HistoryFacts, h head, rest string) error {
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return sql.ErrNoRows
	}
	linkSQL, where := oldLinkSQL, `la.id = ?`
	args := []any{id}
	switch h.event {
	case EvDeleted:
		where = deletedWhere + ` AND la.download_id = (SELECT download_id FROM library_action WHERE id = ?)
			AND COALESCE(la.version_key, '') = ?`
		args = append(args, h.version)
	case EvAdopted:
		linkSQL = adoptLinkSQL
		where = adoptedWhere + ` AND la.at / 86400000 = (SELECT at / 86400000 FROM library_action WHERE id = ?)
			AND COALESCE(la.version_key, '') = ?`
		args = append(args, h.version)
	case EvMissing:
		linkSQL = `(SELECT l.id FROM link l WHERE l.id = la.link_id AND l.state = 'dead')`
	}
	// Each action: its title, release (relSQL), facts recorded with it or else its fallback link's, the link whose
	// episodes it names (the fallback, its own, the one at its path), and its download.
	q := `SELECT ` + libTitleSQL + `, ` + relSQL(linkSQL) + `,
		CASE WHEN la.release_title IS NOT NULL OR la.quality IS NOT NULL THEN la.quality
			ELSE (SELECT quality FROM link WHERE id = ` + linkSQL + `) END,
		CASE WHEN la.release_title IS NOT NULL OR la.quality IS NOT NULL THEN la.score
			ELSE (SELECT score FROM link WHERE id = ` + linkSQL + `) END,
		COALESCE(` + linkSQL + `, la.link_id, (SELECT id FROM link WHERE path = la.path ORDER BY id DESC LIMIT 1), 0),
		COALESCE(la.download_id, 0), la.detail
		FROM library_action la WHERE ` + where + ` ORDER BY la.id`
	rows, err := b.db.QueryContext(b.ctx, q, args...)
	if err != nil {
		return err
	}
	type member struct {
		title, link, dl int64
		rel, detail     string
		quality         sql.NullString
		score           sql.NullInt64
	}
	var ms []member
	for rows.Next() {
		var m member
		if err := rows.Scan(&m.title, &m.rel, &m.quality, &m.score, &m.link, &m.dl, &m.detail); err != nil {
			rows.Close()
			return err
		}
		ms = append(ms, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// The group is the head's: the same title and release as the action the head names.
	var title int64
	var rel string
	if err := b.db.QueryRowContext(b.ctx, `SELECT `+libTitleSQL+`, `+relSQL(linkSQL)+` FROM library_action la
		WHERE la.id = ?`, id).Scan(&title, &rel); err != nil {
		return err
	}
	ms = slices.DeleteFunc(ms, func(m member) bool { return m.title != title || m.rel != rel })
	if len(ms) == 0 {
		return sql.ErrNoRows
	}
	x.Files = len(ms)
	x.Release = nil
	if rel != "" || ms[0].quality.Valid {
		x.Release = &Release{Title: clean(rel), Quality: ms[0].quality.String}
		if ms[0].score.Valid {
			s := int(ms[0].score.Int64)
			x.Release.Score = &s
		}
	}
	var links []any
	for _, m := range ms {
		if m.link > 0 {
			links = append(links, m.link)
		}
	}
	x.Episodes = nil
	if ti, err := b.title(title); err != nil {
		return err
	} else if ti != nil && len(links) > 0 {
		var eps []int64
		err := scanIDs(b, `SELECT DISTINCT le.episode_id FROM link_episode le JOIN episode ep ON ep.id = le.episode_id
			WHERE le.link_id IN (`+placeholders(len(links))+`) ORDER BY ep.season, ep.number`, links, &eps)
		if err != nil {
			return err
		}
		x.Episodes = ti.labels(eps)
	}
	switch h.event {
	case EvDeleted:
		if d, err := b.loadDownload(ms[0].dl); err == nil {
			score := d.score
			x.ReplacedBy = &Release{Title: clean(d.release), Quality: d.quality, Score: &score}
			x.Provider = d.provider
		} else if err != sql.ErrNoRows {
			return err
		}
	case EvRemoved:
		// "unlinked (<reason>), moved aside to …"
		if r, ok := strings.CutPrefix(ms[0].detail, "unlinked ("); ok {
			if i := strings.LastIndex(r, "), moved aside"); i >= 0 {
				x.Reason = cleanMessage(r[:i])
			}
		}
	}
	return nil
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?, ", n), ", ") }

func scanIDs(b *builder, q string, args []any, out *[]int64) error {
	rows, err := b.db.QueryContext(b.ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		*out = append(*out, id)
	}
	return rows.Err()
}

// copyEntry builds a provider copy's History row (copy:<provider_copy_event>).
func (b *builder) copyEntry(e *Entry, rest string) error {
	var hash, name, provider, state, message string
	var quality sql.NullString
	var score sql.NullInt64
	if err := b.db.QueryRowContext(b.ctx, `SELECT info_hash, name, provider, state, message, quality, score
		FROM provider_copy_event WHERE id = ?`, rest).Scan(&hash, &name, &provider, &state, &message, &quality,
		&score); err != nil {
		return err
	}
	e.Source = "router"
	e.Release = &Release{Title: clean(name), InfoHash: hash, Quality: quality.String}
	if score.Valid {
		s := int(score.Int64)
		e.Release.Score = &s
	}
	e.Summary = "copy to " + provider + " " + state + ": " + cleanMessage(message)
	e.Hist = &HistoryFacts{Release: e.Release, Provider: provider, Reason: cleanMessage(message)}
	return nil
}
