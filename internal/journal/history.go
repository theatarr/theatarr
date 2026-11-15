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
// per event, read from the rows stored when each event happened (table history, internal/store history.go;
// docs/journal.md "Stored History rows"). Activity › History and the title page's History tab both read it (GET
// /activity?state=history), so they cannot disagree.
//
//   - grabbed: a download added or reused at a provider or client, dated when it got there (one that never started is
//     no grab; the journal's grab/upgrade classification is read only when kinds are filtered);
//   - imported: a download's import that linked (linked or partial), one row per import with the episodes its links
//     hold, not one per link;
//   - adopted: files taken over from Sonarr/Radarr, one row per library, release and day; the removals of the files
//     they replaced (same title, library and episodes, within a minute) are folded into it, in Replaced;
//   - deleted: an upgrade, one row per download and library ("Upgraded"): the new release (with its import's facts
//     and journal entry; the import's own row is folded into it), the files it replaced in Replaced;
//   - removed: a link taken out by hand (an unlink, recorded as a replace without a download), unless an adoption
//     stands for it;
//   - missing: a link whose file is gone (dead);
//   - failed: a failed or stalled download, or an import held for review (its own row, dl:<id>:held);
//   - blocked: a release put on a version's blocklist;
//   - copy_added, copy_already, copy_not_cached: a copy to another provider ("Add to other providers") that was added,
//     found already there, or not cached;
//   - title_removed: a title taken out of a library (Remove title, Remove copy), one row per library, with who
//     removed it and what became of its files (Removal).
//
// Searches, dry runs, mount waits, repaired links and plans stay in the journal only.

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
	EvCopyAlready   = "copy_already"
	EvTitleRemoved  = "title_removed"
)

// HistoryEvents lists every History event, for filters.
var HistoryEvents = []string{EvGrabbed, EvImported, EvAdopted, EvDeleted, EvRemoved, EvMissing, EvFailed, EvBlocked,
	EvCopyAdded, EvCopyAlready, EvCopyNotCached, EvTitleRemoved}

// KindCopy is the kind of a provider copy's History row (copy:<provider_copy_event>); not a journal kind.
const KindCopy = "copy"

// HistoryRow is one History row.
type HistoryRow struct {
	// ID is the journal id of the row's source (dl:<id>, dl:<id>:fail, dl:<id>:import, lib:<id> (a group's first
	// action), bl:<rowid>, act:<id> (a removal)), or copy:<provider_copy_event>, which has no journal entry.
	ID string
	At time.Time
	// Kind is the journal kind (grab for every grab unless kinds are filtered; copy for a copy).
	Kind    string
	Event   string
	Title   TitleRef
	Anime   bool
	Version string
	// Release is the file the event is about: the grabbed, imported, failed or blocked release; an upgrade's new
	// release (its import's facts); the removed file; the adopted or missing file; the copied torrent. Nil when not
	// known.
	Release *Release
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
	// By is who did it: a title_removed row's remover. Removal is what a title_removed row did with its files.
	By      string
	Removal *Removal
	// Replaced are the files an upgrade replaced (one row per download and library), or the files an adoption's
	// removals took out (the removals' rows folded into it), oldest first.
	Replaced []ReplacedFile
	// Entry is the row's journal entry (its Journal link): its id, a held import's import entry; "" for a copy, a
	// release unblocked since, and a row whose source is gone (retention).
	Entry string
}

// ReplacedFile is one file an upgrade replaced: its path, its release (title, quality, score; nil when not known) and
// the episodes it held.
type ReplacedFile struct {
	Path     string
	Release  *Release
	Episodes []string
}

// HistoryPage is one page of History, newest first.
type HistoryPage struct {
	Items      []*HistoryRow
	NextCursor string
}

// libTitleSQL is the title of a library action (alias la), as the journal's own list reads it.
const libTitleSQL = store.HistoryLibTitleSQL

// History returns one page of History, newest first. Every filter applies to the stored rows; Suspect and Severity
// do not apply. A folded row (an upgrade's import, a removal an adoption replaced) is left out only when the row
// that stands for it matches the same filter, so paging and filters never show half of a pair.
func (j *Journal) History(ctx context.Context, f Filter) (*HistoryPage, error) {
	page := &HistoryPage{Items: []*HistoryRow{}}
	where, wargs, none, err := j.historyFilter(ctx, &f)
	if err != nil {
		return nil, err
	}
	if none {
		return page, nil
	}
	l := &historyLoader{ctx: ctx, db: j.st.Reader()}
	type stored struct {
		h                                            head
		release, formats, replaced                   sql.NullString
		event, hash, indexer, quality, eps, provider string
		reason                                       string
		source, importSt, by, detail                 string
		dl, score                                    sql.NullInt64
		size, files                                  int64
		episodes                                     []int64
		replacedFiles                                []store.HistoryReplacedFile
	}
	var rows []*stored
	err = l.query(`SELECT ref, at, title_id, version_key, kind, event, download_id, release, info_hash, indexer, size,
		quality, score, formats, episodes, files, replaced, provider, reason, source, import_state, by, detail
		FROM history WHERE `+where+` ORDER BY at DESC, ref DESC LIMIT ?`, append(wargs, f.Limit+1),
		func(r *sql.Rows) error {
			var s stored
			if err := r.Scan(&s.h.id, &s.h.at, &s.h.titleID, &s.h.version, &s.h.kind, &s.event, &s.dl, &s.release,
				&s.hash, &s.indexer, &s.size, &s.quality, &s.score, &s.formats, &s.eps, &s.files, &s.replaced,
				&s.provider, &s.reason, &s.source, &s.importSt, &s.by, &s.detail); err != nil {
				return err
			}
			// A grab's kind is grab unless kinds are filtered (the journal's grab/upgrade split).
			if s.h.kind == "upgrade" && len(f.Kinds) == 0 {
				s.h.kind = "grab"
			}
			_ = json.Unmarshal([]byte(s.eps), &s.episodes)
			l.epIDs = append(l.epIDs, s.episodes...)
			if s.replaced.Valid {
				_ = json.Unmarshal([]byte(s.replaced.String), &s.replacedFiles)
				slices.SortFunc(s.replacedFiles, func(a, b store.HistoryReplacedFile) int { return int(a.ID - b.ID) })
				for _, rf := range s.replacedFiles {
					l.epIDs = append(l.epIDs, rf.Episodes...)
				}
			}
			rows = append(rows, &s)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("journal: history: %w", err)
	}
	if len(rows) > f.Limit {
		rows = rows[:f.Limit]
		page.NextCursor = encodeCursor(rows[f.Limit-1].h)
	}

	// Folded rows (historyWhere): an upgrade takes its import's release facts and journal entry; an adoption the files
	// its removals took out, as an upgrade's replaced files.
	type importFacts struct {
		version, hash, indexer, provider, state string
		size                                    int64
		formats                                 sql.NullString
		episodes                                []int64
	}
	imports := map[string]importFacts{}
	var importRefs, adoptRefs []string
	for _, s := range rows {
		switch {
		case s.event == EvDeleted && s.dl.Valid:
			importRefs = append(importRefs, fmt.Sprintf("dl:%d:import", s.dl.Int64))
		case s.event == EvAdopted:
			adoptRefs = append(adoptRefs, s.h.id)
		}
	}
	if len(importRefs) > 0 {
		if err := l.query(`SELECT ref, version_key, info_hash, indexer, size, formats, provider, import_state, episodes
			FROM history WHERE ref IN (SELECT value FROM json_each(?))`, []any{jsonList(importRefs)},
			func(r *sql.Rows) error {
				var ref, eps string
				var x importFacts
				if err := r.Scan(&ref, &x.version, &x.hash, &x.indexer, &x.size, &x.formats, &x.provider, &x.state,
					&eps); err != nil {
					return err
				}
				_ = json.Unmarshal([]byte(eps), &x.episodes)
				l.epIDs = append(l.epIDs, x.episodes...)
				imports[ref] = x
				return nil
			}); err != nil {
			return nil, fmt.Errorf("journal: history: %w", err)
		}
	}
	if len(adoptRefs) > 0 {
		byRef := map[string]*stored{}
		for _, s := range rows {
			byRef[s.h.id] = s
		}
		if err := l.query(`SELECT p.ref, CAST(substr(history.ref, 5) AS INTEGER), history.at, history.release,
				history.quality, history.score, history.episodes, COALESCE(la.path, '')
			FROM history p JOIN history ON history.event = 'removed'
				AND `+adoptionOfRemovalSQL(`history.at BETWEEN p.at - 60000 AND p.at + 60000`)+`
			LEFT JOIN library_action la ON la.id = CAST(substr(history.ref, 5) AS INTEGER)
			WHERE p.ref IN (SELECT value FROM json_each(?)) ORDER BY history.at, history.ref`,
			[]any{jsonList(adoptRefs)}, func(r *sql.Rows) error {
				var ref, quality, eps string
				var rel sql.NullString
				var score sql.NullInt64
				var rf store.HistoryReplacedFile
				if err := r.Scan(&ref, &rf.ID, &rf.At, &rel, &quality, &score, &eps, &rf.Path); err != nil {
					return err
				}
				if rel.Valid {
					rf.Release, rf.Quality = rel.String, &quality
				}
				if score.Valid {
					rf.Score = &score.Int64
				}
				_ = json.Unmarshal([]byte(eps), &rf.Episodes)
				l.epIDs = append(l.epIDs, rf.Episodes...)
				if s := byRef[ref]; s != nil {
					s.replacedFiles = append(s.replacedFiles, rf)
				}
				return nil
			}); err != nil {
			return nil, fmt.Errorf("journal: history: %w", err)
		}
	}

	var titleIDs, failed []int64
	for _, s := range rows {
		titleIDs = append(titleIDs, s.h.titleID)
		if strings.HasSuffix(s.h.id, ":fail") && s.dl.Valid {
			failed = append(failed, s.dl.Int64)
		}
	}
	attempts := map[int64][]Attempt{}
	if len(failed) > 0 {
		in, args := inList(failed)
		if err := l.query(`SELECT download_id, provider, outcome, code, attempted_at FROM download_attempt
			WHERE download_id IN (`+in+`) ORDER BY id`, args, func(r *sql.Rows) error {
			var dl, at int64
			var a Attempt
			if err := r.Scan(&dl, &a.Provider, &a.Outcome, &a.Code, &at); err != nil {
				return err
			}
			a.At = store.FromMillis(at)
			attempts[dl] = append(attempts[dl], a)
			return nil
		}); err != nil {
			return nil, fmt.Errorf("journal: history: %w", err)
		}
	}
	titles, animes := map[int64]TitleRef{}, map[int64]bool{}
	if len(titleIDs) > 0 {
		in, args := inList(titleIDs)
		if err := l.query(`SELECT id, name, COALESCE(year, 0), kind, anime FROM title WHERE id IN (`+in+`)`, args,
			func(r *sql.Rows) error {
				var t TitleRef
				var anime bool
				if err := r.Scan(&t.ID, &t.Name, &t.Year, &t.Kind, &anime); err != nil {
					return err
				}
				titles[t.ID], animes[t.ID] = t, anime
				return nil
			}); err != nil {
			return nil, fmt.Errorf("journal: history: %w", err)
		}
	}
	if err := l.loadLabels(); err != nil {
		return nil, fmt.Errorf("journal: history: %w", err)
	}
	entries := slices.Clone(importRefs)
	for _, s := range rows {
		if e := entryOf(s.h.id); e != "" {
			entries = append(entries, e)
		}
	}
	live, err := l.liveEntries(entries)
	if err != nil {
		return nil, fmt.Errorf("journal: history: %w", err)
	}

	for _, s := range rows {
		x := &HistoryRow{ID: s.h.id, At: store.FromMillis(s.h.at), Kind: s.h.kind, Event: s.event,
			Title: titles[s.h.titleID], Anime: animes[s.h.titleID], Version: s.h.version,
			Episodes: l.labelsOf(s.episodes), Files: int(s.files), Provider: s.provider, Source: s.source,
			Import: s.importSt, By: s.by}
		if x.Title.ID == 0 {
			x.Title.ID = s.h.titleID
		}
		if e := entryOf(x.ID); live[e] {
			x.Entry = e
		}
		if s.release.Valid {
			x.Release = &Release{Title: clean(s.release.String), Indexer: s.indexer, InfoHash: s.hash, Size: s.size,
				Quality: s.quality}
			if s.score.Valid {
				sc := int(s.score.Int64)
				x.Release.Score = &sc
			}
			if s.formats.Valid {
				_ = json.Unmarshal([]byte(s.formats.String), &x.Release.Formats)
			}
		}
		switch {
		case x.Event == EvFailed && x.Kind == KindImport:
			x.Reason = "Import held for review: " + cleanMessage(s.reason)
		case x.Event == EvRemoved:
			// "unlinked (<reason>), moved aside to …"
			if r, ok := strings.CutPrefix(s.detail, "unlinked ("); ok {
				if i := strings.LastIndex(r, "), moved aside"); i >= 0 {
					x.Reason = cleanMessage(r[:i])
				}
			}
		case s.reason != "":
			x.Reason = cleanMessage(s.reason)
		}
		if strings.HasSuffix(x.ID, ":fail") && s.dl.Valid {
			x.Attempts = attempts[s.dl.Int64]
		}
		if x.Event == EvTitleRemoved {
			rm := ParseRemovalDetail(s.detail)
			x.Removal = &rm
		}
		if x.Event == EvDeleted && s.dl.Valid {
			// The upgrade's row stands for its import: the import's release facts, episodes (the files it brought; the
			// old files' are in Replaced), state (a partial import) and journal entry.
			ref := fmt.Sprintf("dl:%d:import", s.dl.Int64)
			if im, ok := imports[ref]; ok && im.version == s.h.version {
				if x.Release != nil {
					x.Release.InfoHash, x.Release.Indexer, x.Release.Size = im.hash, im.indexer, im.size
					if im.formats.Valid {
						_ = json.Unmarshal([]byte(im.formats.String), &x.Release.Formats)
					}
				}
				if x.Provider == "" {
					x.Provider = im.provider
				}
				x.Episodes, x.Import = l.labelsOf(im.episodes), im.state
				if live[ref] {
					x.Entry = ref
				}
			}
		}
		if x.Event == EvDeleted || x.Event == EvAdopted {
			// The row's release is the new file; Replaced the files it replaced.
			for _, rf := range s.replacedFiles {
				f := ReplacedFile{Path: rf.Path}
				if rf.Release != "" || rf.Quality != nil {
					f.Release = &Release{Title: clean(rf.Release)}
					if rf.Quality != nil {
						f.Release.Quality = *rf.Quality
					}
					if rf.Score != nil {
						sc := int(*rf.Score)
						f.Release.Score = &sc
					}
				}
				f.Episodes = l.labelsOf(rf.Episodes)
				x.Replaced = append(x.Replaced, f)
			}
		}
		page.Items = append(page.Items, x)
	}
	return page, nil
}

// jsonList is ss as a JSON array, for json_each.
func jsonList(ss []string) string {
	b, _ := json.Marshal(ss)
	return string(b)
}

// entryOf is the journal entry a History row id names: its id, a held import's import entry; "" for a copy (no
// entry) and a release unblocked since (bl:<rowid>:<at>).
func entryOf(id string) string {
	switch {
	case strings.HasPrefix(id, "copy:"):
		return ""
	case strings.HasSuffix(id, ":held"):
		return strings.TrimSuffix(id, ":held") + ":import"
	case strings.HasPrefix(id, "bl:") && strings.Count(id, ":") > 1:
		return ""
	}
	return id
}

// liveEntries are the journal entries among ids whose source rows still exist (retention deletes old failed
// downloads and library actions), in one read.
func (l *historyLoader) liveEntries(ids []string) (map[string]bool, error) {
	var dls, imports, libs, bls, acts []int64
	for _, id := range ids {
		n := idNum(id)
		switch {
		case strings.HasSuffix(id, ":import"):
			imports = append(imports, n)
		case strings.HasPrefix(id, "dl:"):
			dls = append(dls, n)
		case strings.HasPrefix(id, "lib:"):
			libs = append(libs, n)
		case strings.HasPrefix(id, "bl:"):
			bls = append(bls, n)
		case strings.HasPrefix(id, "act:"):
			acts = append(acts, n)
		}
	}
	js := func(ns []int64) string {
		b, _ := json.Marshal(ns)
		return string(b)
	}
	live := map[string]bool{}
	err := l.query(`SELECT 'dl:' || id FROM download WHERE id IN (SELECT value FROM json_each(?1))
		UNION ALL SELECT 'dl:' || id || ':fail' FROM download WHERE state = 'failed'
			AND id IN (SELECT value FROM json_each(?1))
		UNION ALL SELECT 'dl:' || download_id || ':import' FROM library_import
			WHERE download_id IN (SELECT value FROM json_each(?2))
		UNION ALL SELECT 'lib:' || id FROM library_action WHERE id IN (SELECT value FROM json_each(?3))
		UNION ALL SELECT 'bl:' || rowid FROM router_blocklist WHERE rowid IN (SELECT value FROM json_each(?4))
		UNION ALL SELECT 'act:' || id FROM journal_action WHERE id IN (SELECT value FROM json_each(?5))`,
		[]any{js(dls), js(imports), js(libs), js(bls), js(acts)}, func(r *sql.Rows) error {
			var id string
			if err := r.Scan(&id); err != nil {
				return err
			}
			live[id] = true
			return nil
		})
	return live, err
}

// idNum is the number of a journal id (dl:12:fail is 12).
func idNum(id string) int64 {
	_, rest, _ := strings.Cut(id, ":")
	rest, _, _ = strings.Cut(rest, ":")
	n, _ := strconv.ParseInt(rest, 10, 64)
	return n
}

// historyFilter checks f and sets its page size; the WHERE clause of its rows, or none when no row can match.
func (j *Journal) historyFilter(ctx context.Context, f *Filter) (string, []any, bool, error) {
	if err := f.Validate(); err != nil {
		return "", nil, false, err
	}
	for _, ev := range f.Events {
		if !slices.Contains(HistoryEvents, ev) {
			return "", nil, false, fmt.Errorf("%w: event %q: want one of %s", ErrBadFilter, ev,
				strings.Join(HistoryEvents, ", "))
		}
	}
	if f.Limit == 0 {
		f.Limit = 50
	}
	f.Limit = min(f.Limit, 500)
	titles, err := j.titleIDs(ctx, f.Title)
	if err != nil {
		return "", nil, false, err
	}
	if (f.Title != "" && len(titles) == 0) || (f.Restricted && len(f.Within) == 0) {
		return "", nil, true, nil
	}
	cur, _ := decodeCursor(f.Cursor)
	where, args := historyWhere(*f, titles, cur)
	return where, args, false, nil
}

// historyWhere is the filter of the history rows, with the rows another row stands for left out (docs/journal.md
// "History's folded rows"): an upgrade's import, and a removal its adoption replaced, are shown on their partner's row
// when that row is listed too, so a page or a filter never shows half of a pair. The partner shares the row's title
// and library, so of the filter only the event, kind and since bounds can tell them apart.
func historyWhere(f Filter, titles []int64, cur *cursor) (string, []any) {
	w, args := historyMatch(f, titles)
	// listed: a partner row (kind link) with event ev can be listed under f's events and kinds.
	listed := func(ev string) bool {
		return (len(f.Events) == 0 || slices.Contains(f.Events, ev)) && (len(f.Kinds) == 0 || slices.Contains(f.Kinds, KindLink))
	}
	since, sinceArgs := "", []any{}
	if !f.Since.IsZero() {
		since, sinceArgs = ` AND p.at >= ?`, []any{store.Millis(f.Since)}
	}
	if listed(EvDeleted) {
		w = append(w, `NOT (event = 'imported' AND EXISTS (SELECT 1 FROM history p WHERE `+upgradeOfImportSQL+since+`))`)
		args = append(args, sinceArgs...)
	}
	if listed(EvAdopted) {
		w = append(w, `NOT (event = 'removed' AND EXISTS (SELECT 1 FROM history p WHERE `+
			adoptionOfRemovalSQL(`p.at BETWEEN history.at - 60000 AND history.at + 60000`)+since+`))`)
		args = append(args, sinceArgs...)
	}
	if cur != nil {
		w, args = append(w, `(at < ? OR (at = ? AND ref < ?))`), append(args, cur.at, cur.at, cur.id)
	}
	return strings.Join(w, " AND "), args
}

// The upgrade row (deleted, alias p) of an import (history): the same download and library.
const upgradeOfImportSQL = `p.event = 'deleted' AND p.download_id = history.download_id
	AND p.version_key IS history.version_key`

// adoptionOfRemovalSQL is the adoption (alias p) that replaced a removed file (history): the same title and library,
// within a minute (window: the bound on p.at or on history.at, so the (title_id, at) index serves the side looked up;
// an adoption's row is dated by its group's latest file, and the unlink is a step of its own), and the same episodes
// (a movie's none). A removal of a file whose link arrived within that minute is the adopted file going again, not one
// it replaced (an adoption that takes over a link at the same path keeps its creation time, so it still folds).
func adoptionOfRemovalSQL(window string) string {
	return `p.event = 'adopted' AND p.title_id = history.title_id AND p.version_key IS history.version_key
		AND ` + window + `
		AND ((json_array_length(p.episodes) = 0 AND json_array_length(history.episodes) = 0)
			OR EXISTS (SELECT 1 FROM json_each(p.episodes) pe, json_each(history.episodes) he WHERE pe.value = he.value))
		AND NOT EXISTS (SELECT 1 FROM library_action ra JOIN link l ON l.id = ra.link_id
			WHERE ra.id = CAST(substr(history.ref, 5) AS INTEGER) AND l.created_at >= history.at - 60000)`
}

// historyMatch is the filter's conditions on the history columns.
func historyMatch(f Filter, titles []int64) ([]string, []any) {
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
	return w, args
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

// inList is ids as an IN list's placeholders and arguments, each id once.
func inList(ids []int64) (string, []any) {
	u := slices.Clone(ids)
	slices.Sort(u)
	u = slices.Compact(u)
	out := make([]any, len(u))
	for i, x := range u {
		out[i] = x
	}
	return store.Placeholders(len(out)), out
}

// loadLabels reads the season and number of every episode the page names.
func (l *historyLoader) loadLabels() error {
	l.labels = map[int64]epLabel{}
	slices.Sort(l.epIDs)
	ids := slices.Compact(l.epIDs)
	if len(ids) == 0 {
		return nil
	}
	in, args := inList(ids)
	return l.query(`SELECT id, season, number FROM episode WHERE id IN (`+in+`)`, args,
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
