package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// History (docs/design/ui.md "Activity", History) is what happened to the titles' files, worded like Radarr's, one row
// per event. Activity › History and the title page's History tab both read it (GET /activity?state=history), so they
// cannot disagree. It reads the journal's sources (not its entries: no decision, checks or verdicts per row), a page
// at a time, the facts of a page's rows in a few batch queries.
//
//   - grabbed: a download added or reused at a provider or client (one that never started is no grab; the
//     journal's grab/upgrade classification is not changed and read only when kinds are filtered);
//   - imported: a download's import that linked (linked or partial), one row per import with the episodes its links
//     hold, not one per link;
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
// same file (an unlinked row, an adopted one, a dead one), and are left unknown otherwise.

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

// HistoryRow is one History row.
type HistoryRow struct {
	// ID is the journal id of the row's source (dl:<id>, dl:<id>:fail, dl:<id>:import, lib:<id> (a group's first
	// action), bl:<rowid>), or copy:<provider_copy_event>, which has no journal entry.
	ID string
	At time.Time
	// Kind is the journal kind (grab for every grab unless kinds are filtered; copy for a copy).
	Kind    string
	Event   string
	Title   TitleRef
	Version string
	// Release is the file the event is about: the grabbed, imported, failed or blocked release; the old file of a
	// deleted or removed row; the adopted or missing file; the copied torrent. Nil when not known.
	Release *Release
	// ReplacedBy is a deleted row's new release (the download that replaced it).
	ReplacedBy *Release
	// Episodes are the row's episodes (S01E02), in order; Files the files a library row stands for.
	Episodes []string
	Files    int
	Provider string
	// Reason: a failure's error, a block's reason, an unlink's reason, a held import's, a copy's message (cleaned).
	// Source: a block's source. Import: an import row's library_import state. Attempts: a failed download's.
	Reason   string
	Source   string
	Import   string
	Attempts []Attempt
}

// HistoryPage is one page of History, newest first.
type HistoryPage struct {
	Items      []*HistoryRow
	NextCursor string
}

// The title of a library action (alias la).
const libTitleSQL = `COALESCE((SELECT title_id FROM download WHERE id = la.download_id),
	(SELECT title_id FROM link WHERE id = la.link_id), (SELECT vf.title_id FROM version_file vf WHERE vf.path = la.path LIMIT 1), 0)`

// The link row that still holds an action's file facts, when the action recorded none: a replaced file's unlinked row
// (not taken over since), an adoption's still adopted row, a dead link's still dead row; NULL when none.
const (
	oldLinkSQL = `(SELECT l.id FROM link l WHERE l.state = 'unlinked'
		AND (l.id = la.link_id OR (la.link_id IS NULL AND l.path = la.path)) ORDER BY l.id DESC LIMIT 1)`
	adoptLinkSQL = `(SELECT l.id FROM link l WHERE l.id = la.link_id AND l.adopted_from IS NOT NULL)`
	deadLinkSQL  = `(SELECT l.id FROM link l WHERE l.id = la.link_id AND l.state = 'dead')`
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
	// grabbedWhere: the download reached a provider or client: past adding, or a failed one that was added, reused or
	// accepted by a provider (a requeue clears added_at).
	grabbedWhere = `(d.state NOT IN ('queued', 'adding', 'failed') OR d.added_at IS NOT NULL OR d.reused = 1
		OR EXISTS (SELECT 1 FROM download_attempt a WHERE a.download_id = d.id AND a.outcome = 'accepted'))`
)

// historyArm is one source of History's rows: a SELECT of (id, at, title_id, version_key, kind, event), and the
// events it can give.
type historyArm struct {
	sql    string
	events []string
}

func historyArms(kinds bool) []historyArm {
	grabKind := `'grab'`
	if kinds {
		grabKind = grabKindSQL
	}
	return []historyArm{
		{`SELECT 'dl:' || d.id AS id, d.created_at AS at, d.title_id AS title_id, d.version_key AS version_key,
			` + grabKind + ` AS kind, 'grabbed' AS event FROM download d WHERE ` + grabbedWhere, []string{EvGrabbed}},
		{`SELECT 'dl:' || d.id || ':fail' AS id, d.updated_at AS at, d.title_id AS title_id, d.version_key AS version_key,
			CASE WHEN d.error LIKE 'stalled%' THEN 'stall' ELSE 'failure' END AS kind, 'failed' AS event
			FROM download d WHERE d.state = 'failed'`, []string{EvFailed}},
		{`SELECT 'dl:' || li.download_id || ':import' AS id, li.updated_at AS at, d.title_id AS title_id,
			d.version_key AS version_key, 'import' AS kind,
			CASE li.state WHEN 'rejected' THEN 'failed' ELSE 'imported' END AS event
			FROM library_import li JOIN download d ON d.id = li.download_id
			WHERE li.state IN ('linked', 'partial') OR (li.state = 'rejected' AND d.state <> 'failed')`,
			[]string{EvImported, EvFailed}},
		{`SELECT 'lib:' || la.id AS id, la.at AS at, ` + libTitleSQL + ` AS title_id,
			COALESCE(la.version_key, '') AS version_key, 'link' AS kind,
			CASE la.kind WHEN 'dead' THEN 'missing' ELSE 'removed' END AS event
			FROM library_action la WHERE (` + removedWhere + `) OR (` + missingWhere + `)`, []string{EvRemoved, EvMissing}},
		{`SELECT 'lib:' || MIN(la.id) AS id, MAX(la.at) AS at, (SELECT title_id FROM download WHERE id = la.download_id)
			AS title_id, COALESCE(la.version_key, '') AS version_key, 'link' AS kind, 'deleted' AS event
			FROM library_action la WHERE ` + deletedWhere + `
			GROUP BY la.download_id, la.version_key, ` + relSQL(oldLinkSQL), []string{EvDeleted}},
		{`SELECT 'lib:' || MIN(la.id) AS id, MAX(la.at) AS at, ` + libTitleSQL + ` AS title_id,
			COALESCE(la.version_key, '') AS version_key, 'link' AS kind, 'adopted' AS event
			FROM library_action la WHERE ` + adoptedWhere + `
			GROUP BY ` + libTitleSQL + `, la.version_key, ` + relSQL(adoptLinkSQL) + `, la.at / 86400000`,
			[]string{EvAdopted}},
		{`SELECT 'bl:' || b.rowid AS id, b.created_at AS at, COALESCE(b.title_id, 0) AS title_id,
			b.version_key AS version_key, 'blocklist' AS kind, 'blocked' AS event FROM router_blocklist b`,
			[]string{EvBlocked}},
		{`SELECT 'copy:' || c.id AS id, c.at AS at, COALESCE(c.title_id, 0) AS title_id, '' AS version_key,
			'copy' AS kind, CASE c.state WHEN 'added' THEN 'copy_added' ELSE 'copy_not_cached' END AS event
			FROM provider_copy_event c`, []string{EvCopyAdded, EvCopyNotCached}},
	}
}

// History returns one page of History rows, newest first. Every filter applies inside each source, which then gives
// at most a page of its newest rows; Suspect, Severity and ScanLimit do not apply.
func (j *Journal) History(ctx context.Context, f Filter) (*HistoryPage, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	for _, ev := range f.Events {
		if !slices.Contains(HistoryEvents, ev) {
			return nil, fmt.Errorf("%w: event %q: want one of %s", ErrBadFilter, ev, strings.Join(HistoryEvents, ", "))
		}
	}
	if f.Limit == 0 {
		f.Limit = 50
	}
	f.Limit = min(f.Limit, 500)
	page := &HistoryPage{Items: []*HistoryRow{}}
	titles, err := j.titleIDs(ctx, f.Title)
	if err != nil {
		return nil, err
	}
	if (f.Title != "" && len(titles) == 0) || (f.Restricted && len(f.Within) == 0) {
		return page, nil
	}
	cur, _ := decodeCursor(f.Cursor)
	where, wargs := historyWhere(f, titles, cur)
	var qs []string
	var args []any
	for _, a := range historyArms(len(f.Kinds) > 0) {
		if len(f.Events) > 0 && !slices.ContainsFunc(a.events, func(ev string) bool { return slices.Contains(f.Events, ev) }) {
			continue
		}
		qs = append(qs, `SELECT * FROM (SELECT * FROM (`+a.sql+`) WHERE `+where+` ORDER BY at DESC, id DESC LIMIT ?)`)
		args = append(append(args, wargs...), f.Limit+1)
	}
	if len(qs) == 0 {
		return page, nil
	}
	rows, err := j.st.Reader().QueryContext(ctx, `SELECT id, at, title_id, COALESCE(version_key, ''), kind, event FROM (`+
		strings.Join(qs, "\nUNION ALL\n")+`) ORDER BY at DESC, id DESC LIMIT ?`, append(args, f.Limit+1)...)
	if err != nil {
		return nil, fmt.Errorf("journal: history: %w", err)
	}
	var hs []head
	var events []string
	for rows.Next() {
		var h head
		var ev string
		if err := rows.Scan(&h.id, &h.at, &h.titleID, &h.version, &h.kind, &ev); err != nil {
			rows.Close()
			return nil, err
		}
		hs, events = append(hs, h), append(events, ev)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(hs) > f.Limit {
		hs, events = hs[:f.Limit], events[:f.Limit]
		page.NextCursor = encodeCursor(hs[f.Limit-1])
	}
	items, err := (&historyLoader{ctx: ctx, db: j.st.Reader()}).load(hs, events)
	if err != nil {
		return nil, fmt.Errorf("journal: history: %w", err)
	}
	page.Items = items
	return page, nil
}

// historyWhere is the filter of every source's rows (columns id, at, title_id, version_key, kind, event).
func historyWhere(f Filter, titles []int64, cur *cursor) (string, []any) {
	w := []string{"1 = 1"}
	var args []any
	in := func(col string, n int) { w = append(w, col+` IN (`+store.Placeholders(n)+`)`) }
	if !f.Since.IsZero() {
		w, args = append(w, `at >= ?`), append(args, store.Millis(f.Since))
	}
	if f.Title != "" {
		in("title_id", len(titles))
		for _, t := range titles {
			args = append(args, t)
		}
	}
	if f.Restricted {
		var or []string
		for _, t := range f.Within {
			vs, narrowed := f.WithinVersions[t]
			if !narrowed {
				or, args = append(or, `title_id = ?`), append(args, t)
				continue
			}
			or, args = append(or, `title_id = ? AND COALESCE(version_key, '') IN (''`+strings.Repeat(", ?", len(vs))+`)`),
				append(args, t)
			for _, v := range vs {
				args = append(args, v)
			}
		}
		w = append(w, `(`+strings.Join(or, " OR ")+`)`)
	}
	switch f.TitleKind {
	case "anime":
		w = append(w, `title_id IN (SELECT id FROM title WHERE anime = 1)`)
	case "movie", "series":
		w, args = append(w, `title_id IN (SELECT id FROM title WHERE anime = 0 AND kind = ?)`), append(args, f.TitleKind)
	}
	if f.Version != "" {
		w, args = append(w, `version_key = ?`), append(args, f.Version)
	}
	if len(f.Kinds) > 0 {
		in("kind", len(f.Kinds))
		for _, k := range f.Kinds {
			args = append(args, k)
		}
	}
	if len(f.Events) > 0 {
		in("event", len(f.Events))
		for _, ev := range f.Events {
			args = append(args, ev)
		}
	}
	if cur != nil {
		w, args = append(w, `(at < ? OR (at = ? AND id < ?))`), append(args, cur.at, cur.at, cur.id)
	}
	return strings.Join(w, " AND "), args
}

// historyLoader loads the facts of one page's rows, a batch query per source.
type historyLoader struct {
	ctx context.Context
	db  *sql.DB
	// episode ids whose labels are needed, and the labels
	epIDs  []int64
	labels map[int64]epLabel
}

type epLabel struct {
	season, number int
}

type histDownload struct {
	titleID                                      int64
	version, hash, release, indexer, quality     string
	score                                        int
	size                                         sql.NullInt64
	formats                                      sql.NullString
	episodes, provider, errText, state, importSt string
	importErr                                    sql.NullString
	eps, linked                                  []int64
}

// libMember is one library action of a library row.
type libMember struct {
	id, title, at, dl, link int64
	version, rel, detail    string
	quality                 sql.NullString
	score                   sql.NullInt64
}

func (l *historyLoader) query(q string, args []any, scan func(*sql.Rows) error) error {
	rows, err := l.db.QueryContext(l.ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func anys[T any](xs []T) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

// members loads library actions (alias la) matching where, with the facts of linkSQL's fallback link.
func (l *historyLoader) members(linkSQL, where string, args []any) ([]libMember, error) {
	var out []libMember
	q := `SELECT la.id, ` + libTitleSQL + `, la.at, COALESCE(la.download_id, 0), COALESCE(la.version_key, ''),
		` + relSQL(linkSQL) + `,
		CASE WHEN la.release_title IS NOT NULL OR la.quality IS NOT NULL THEN la.quality
			ELSE (SELECT quality FROM link WHERE id = ` + linkSQL + `) END,
		CASE WHEN la.release_title IS NOT NULL OR la.quality IS NOT NULL THEN la.score
			ELSE (SELECT score FROM link WHERE id = ` + linkSQL + `) END,
		COALESCE(` + linkSQL + `, la.link_id, (SELECT id FROM link WHERE path = la.path ORDER BY id DESC LIMIT 1), 0),
		la.detail FROM library_action la WHERE ` + where + ` ORDER BY la.id`
	err := l.query(q, args, func(r *sql.Rows) error {
		var m libMember
		if err := r.Scan(&m.id, &m.title, &m.at, &m.dl, &m.version, &m.rel, &m.quality, &m.score, &m.link,
			&m.detail); err != nil {
			return err
		}
		out = append(out, m)
		return nil
	})
	return out, err
}

func idOf(id string) int64 {
	_, rest, _ := strings.Cut(id, ":")
	rest, _, _ = strings.Cut(rest, ":")
	n, _ := strconv.ParseInt(rest, 10, 64)
	return n
}

func (l *historyLoader) load(hs []head, events []string) ([]*HistoryRow, error) {
	byEvent := map[string][]int64{}
	var titleIDs, dlIDs, blIDs, copyIDs []int64
	for i, h := range hs {
		titleIDs = append(titleIDs, h.titleID)
		id := idOf(h.id)
		switch {
		case strings.HasPrefix(h.id, "dl:"):
			dlIDs = append(dlIDs, id)
		case strings.HasPrefix(h.id, "bl:"):
			blIDs = append(blIDs, id)
		case strings.HasPrefix(h.id, "copy:"):
			copyIDs = append(copyIDs, id)
		default:
			byEvent[events[i]] = append(byEvent[events[i]], id)
		}
	}

	// Library rows: the actions of each row (a group's by its key), keyed by the row's first action id.
	groups := map[int64][]libMember{}
	add := func(ms []libMember, key func(libMember) string, heads []int64) {
		byKey := map[string][]libMember{}
		for _, m := range ms {
			byKey[key(m)] = append(byKey[key(m)], m)
		}
		for _, g := range byKey {
			if slices.Contains(heads, g[0].id) {
				groups[g[0].id] = g
			}
		}
	}
	single := func(m libMember) string { return strconv.FormatInt(m.id, 10) }
	for ev, linkSQL := range map[string]string{EvRemoved: oldLinkSQL, EvMissing: deadLinkSQL} {
		if ids := byEvent[ev]; len(ids) > 0 {
			ms, err := l.members(linkSQL, `la.id IN (`+store.Placeholders(len(ids))+`)`, anys(ids))
			if err != nil {
				return nil, err
			}
			add(ms, single, ids)
		}
	}
	if ids := byEvent[EvDeleted]; len(ids) > 0 {
		ms, err := l.members(oldLinkSQL, deletedWhere+` AND la.download_id IN (SELECT download_id FROM library_action
			WHERE id IN (`+store.Placeholders(len(ids))+`))`, anys(ids))
		if err != nil {
			return nil, err
		}
		add(ms, func(m libMember) string { return fmt.Sprint(m.dl, "\x00", m.version, "\x00", m.rel) }, ids)
	}
	if ids := byEvent[EvAdopted]; len(ids) > 0 {
		ms, err := l.members(adoptLinkSQL, adoptedWhere+` AND la.at / 86400000 IN (SELECT at / 86400000
			FROM library_action WHERE id IN (`+store.Placeholders(len(ids))+`))`, anys(ids))
		if err != nil {
			return nil, err
		}
		add(ms, func(m libMember) string {
			return fmt.Sprint(m.title, "\x00", m.version, "\x00", m.rel, "\x00", m.at/86400000)
		}, ids)
	}
	var linkIDs []int64
	for _, g := range groups {
		for _, m := range g {
			if m.link > 0 {
				linkIDs = append(linkIDs, m.link)
			}
			if m.dl > 0 {
				dlIDs = append(dlIDs, m.dl)
			}
		}
	}

	type blRow struct {
		hash, release, reason, source string
		dl                            int64
	}
	bls := map[int64]blRow{}
	if len(blIDs) > 0 {
		if err := l.query(`SELECT rowid, info_hash, release_title, reason, source, COALESCE(download_id, 0)
			FROM router_blocklist WHERE rowid IN (`+store.Placeholders(len(blIDs))+`)`, anys(blIDs), func(r *sql.Rows) error {
			var id int64
			var b blRow
			if err := r.Scan(&id, &b.hash, &b.release, &b.reason, &b.source, &b.dl); err != nil {
				return err
			}
			bls[id] = b
			if b.dl > 0 {
				dlIDs = append(dlIDs, b.dl)
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}

	dls := map[int64]*histDownload{}
	if len(dlIDs) > 0 {
		ph := store.Placeholders(len(dlIDs))
		if err := l.query(`SELECT d.id, d.title_id, d.version_key, d.info_hash, d.release_title, d.indexer, d.quality,
			d.score, d.size_bytes, d.formats, d.episodes, COALESCE(d.provider, ''), COALESCE(d.error, ''), d.state,
			COALESCE(li.state, ''), li.last_error
			FROM download d LEFT JOIN library_import li ON li.download_id = d.id WHERE d.id IN (`+ph+`)`, anys(dlIDs),
			func(r *sql.Rows) error {
				var id int64
				var d histDownload
				if err := r.Scan(&id, &d.titleID, &d.version, &d.hash, &d.release, &d.indexer, &d.quality, &d.score,
					&d.size, &d.formats, &d.episodes, &d.provider, &d.errText, &d.state, &d.importSt,
					&d.importErr); err != nil {
					return err
				}
				_ = json.Unmarshal([]byte(d.episodes), &d.eps)
				l.epIDs = append(l.epIDs, d.eps...)
				dls[id] = &d
				return nil
			}); err != nil {
			return nil, err
		}
		// The episodes an import's links hold (one link per file it made).
		if err := l.query(`SELECT a.download_id, le.episode_id FROM library_action a JOIN link_episode le
			ON le.link_id = a.link_id WHERE a.kind = 'link' AND a.mode = 'applied' AND a.download_id IN (`+ph+`)`,
			anys(dlIDs), func(r *sql.Rows) error {
				var dl, ep int64
				if err := r.Scan(&dl, &ep); err != nil {
					return err
				}
				if d := dls[dl]; d != nil && !slices.Contains(d.linked, ep) {
					d.linked = append(d.linked, ep)
					l.epIDs = append(l.epIDs, ep)
				}
				return nil
			}); err != nil {
			return nil, err
		}
	}
	attempts := map[int64][]Attempt{}
	var failed []int64
	for i, h := range hs {
		if events[i] == EvFailed && strings.HasSuffix(h.id, ":fail") {
			failed = append(failed, idOf(h.id))
		}
	}
	if len(failed) > 0 {
		if err := l.query(`SELECT download_id, provider, outcome, code, attempted_at FROM download_attempt
			WHERE download_id IN (`+store.Placeholders(len(failed))+`) ORDER BY id`, anys(failed), func(r *sql.Rows) error {
			var dl, at int64
			var a Attempt
			if err := r.Scan(&dl, &a.Provider, &a.Outcome, &a.Code, &at); err != nil {
				return err
			}
			a.At = store.FromMillis(at)
			attempts[dl] = append(attempts[dl], a)
			return nil
		}); err != nil {
			return nil, err
		}
	}

	linkEps := map[int64][]int64{}
	if len(linkIDs) > 0 {
		if err := l.query(`SELECT link_id, episode_id FROM link_episode WHERE link_id IN (`+
			store.Placeholders(len(linkIDs))+`)`, anys(linkIDs), func(r *sql.Rows) error {
			var lk, ep int64
			if err := r.Scan(&lk, &ep); err != nil {
				return err
			}
			linkEps[lk] = append(linkEps[lk], ep)
			l.epIDs = append(l.epIDs, ep)
			return nil
		}); err != nil {
			return nil, err
		}
	}

	type copyRow struct {
		hash, name, provider, message string
		quality                       sql.NullString
		score                         sql.NullInt64
	}
	copies := map[int64]copyRow{}
	if len(copyIDs) > 0 {
		if err := l.query(`SELECT id, info_hash, name, provider, message, quality, score FROM provider_copy_event
			WHERE id IN (`+store.Placeholders(len(copyIDs))+`)`, anys(copyIDs), func(r *sql.Rows) error {
			var id int64
			var c copyRow
			if err := r.Scan(&id, &c.hash, &c.name, &c.provider, &c.message, &c.quality, &c.score); err != nil {
				return err
			}
			copies[id] = c
			return nil
		}); err != nil {
			return nil, err
		}
	}

	titles := map[int64]TitleRef{}
	if len(titleIDs) > 0 {
		if err := l.query(`SELECT id, name, COALESCE(year, 0), kind FROM title WHERE id IN (`+
			store.Placeholders(len(titleIDs))+`)`, anys(titleIDs), func(r *sql.Rows) error {
			var t TitleRef
			if err := r.Scan(&t.ID, &t.Name, &t.Year, &t.Kind); err != nil {
				return err
			}
			titles[t.ID] = t
			return nil
		}); err != nil {
			return nil, err
		}
	}
	if err := l.loadLabels(); err != nil {
		return nil, err
	}

	release := func(d *histDownload) *Release {
		r := &Release{Title: clean(d.release), Indexer: d.indexer, InfoHash: d.hash, Size: d.size.Int64, Quality: d.quality}
		if d.formats.Valid {
			_ = json.Unmarshal([]byte(d.formats.String), &r.Formats)
		}
		// A score only with scoring evidence: stored formats ('[]' for a calculated 0) or a score.
		if d.formats.Valid || d.score != 0 {
			s := d.score
			r.Score = &s
		}
		return r
	}
	out := make([]*HistoryRow, 0, len(hs))
	for i, h := range hs {
		x := &HistoryRow{ID: h.id, At: store.FromMillis(h.at), Kind: h.kind, Event: events[i], Title: titles[h.titleID],
			Version: h.version}
		if x.Title.ID == 0 {
			x.Title.ID = h.titleID
		}
		id := idOf(h.id)
		switch {
		case strings.HasPrefix(h.id, "dl:"):
			d := dls[id]
			if d == nil {
				continue
			}
			x.Release, x.Provider = release(d), d.provider
			x.Episodes = l.labelsOf(d.eps)
			switch {
			case strings.HasSuffix(h.id, ":fail"):
				x.Reason, x.Attempts = cleanMessage(d.errText), attempts[id]
			case strings.HasSuffix(h.id, ":import"):
				x.Import = d.importSt
				if len(d.linked) > 0 {
					x.Episodes = l.labelsOf(d.linked)
				}
				if x.Event == EvFailed {
					x.Reason = "Import held for review: " + cleanMessage(d.importErr.String)
				}
			}
		case strings.HasPrefix(h.id, "bl:"):
			b, ok := bls[id]
			if !ok {
				continue
			}
			x.Release = &Release{Title: clean(b.release), InfoHash: b.hash}
			if d := dls[b.dl]; d != nil {
				x.Release = release(d)
				x.Release.Title, x.Release.InfoHash, x.Release.Formats = clean(b.release), b.hash, nil
			}
			x.Source, x.Reason = b.source, cleanMessage(b.reason)
		case strings.HasPrefix(h.id, "copy:"):
			c, ok := copies[id]
			if !ok {
				continue
			}
			x.Release = &Release{Title: clean(c.name), InfoHash: c.hash, Quality: c.quality.String}
			if c.score.Valid {
				s := int(c.score.Int64)
				x.Release.Score = &s
			}
			x.Provider, x.Reason = c.provider, cleanMessage(c.message)
		default:
			g := groups[id]
			if len(g) == 0 {
				continue
			}
			l.libRow(x, g, linkEps)
			if x.Event == EvDeleted {
				if d := dls[g[0].dl]; d != nil {
					x.ReplacedBy, x.Provider = release(d), d.provider
					x.ReplacedBy.Indexer, x.ReplacedBy.InfoHash, x.ReplacedBy.Size, x.ReplacedBy.Formats = "", "", 0, nil
				}
			}
		}
		out = append(out, x)
	}
	return out, nil
}

// libRow fills a library row from its actions.
func (l *historyLoader) libRow(x *HistoryRow, g []libMember, linkEps map[int64][]int64) {
	m := g[0]
	x.Files = len(g)
	if m.rel != "" || m.quality.Valid {
		x.Release = &Release{Title: clean(m.rel), Quality: m.quality.String}
		if m.score.Valid {
			s := int(m.score.Int64)
			x.Release.Score = &s
		}
	}
	var eps []int64
	for _, m := range g {
		for _, ep := range linkEps[m.link] {
			if !slices.Contains(eps, ep) {
				eps = append(eps, ep)
			}
		}
	}
	x.Episodes = l.labelsOf(eps)
	if x.Event == EvRemoved {
		// "unlinked (<reason>), moved aside to …"
		if r, ok := strings.CutPrefix(m.detail, "unlinked ("); ok {
			if i := strings.LastIndex(r, "), moved aside"); i >= 0 {
				x.Reason = cleanMessage(r[:i])
			}
		}
	}
}

// loadLabels reads the season and number of every episode the page names.
func (l *historyLoader) loadLabels() error {
	l.labels = map[int64]epLabel{}
	slices.Sort(l.epIDs)
	ids := slices.Compact(l.epIDs)
	if len(ids) == 0 {
		return nil
	}
	return l.query(`SELECT id, season, number FROM episode WHERE id IN (`+store.Placeholders(len(ids))+`)`, anys(ids),
		func(r *sql.Rows) error {
			var id int64
			var e epLabel
			if err := r.Scan(&id, &e.season, &e.number); err != nil {
				return err
			}
			l.labels[id] = e
			return nil
		})
}

// labelsOf are episodes' labels (S01E02), in season and number order; unknown ids left out.
func (l *historyLoader) labelsOf(ids []int64) []string {
	var es []epLabel
	for _, id := range ids {
		if e, ok := l.labels[id]; ok {
			es = append(es, e)
		}
	}
	slices.SortFunc(es, func(a, b epLabel) int {
		if a.season != b.season {
			return a.season - b.season
		}
		return a.number - b.number
	})
	var out []string
	for _, e := range es {
		out = append(out, fmt.Sprintf("S%02dE%02d", e.season, e.number))
	}
	return out
}
