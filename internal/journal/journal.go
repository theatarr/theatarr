// Package journal is Theatarr's grab journal: one entry per decision (grab, upgrade, search, re-search) and per thing that happened to one afterwards (failure, stall, blocklist, import, link), each with
// its reasoning (the chosen release, its score and formats, the runners-up and their rejections), provider attempts
// and cache evidence, import results and suspect flags. docs/journal.md is the reader's guide.
//
// # Sources
//
// The journal is a reader over the tables that already record all of this; nothing is copied:
//
//   - download, download_attempt, provider_cache, router_blocklist (router), acquire_run (acquirer);
//   - library_import, library_action, link, content_file (the library).
//
// Its own tables: journal_decision (migration 00024), the candidates a search decided between when it handed work to
// the router (written by internal/journal/snapshot), and journal_action (operator actions, action.go). Flags are
// stored on the History rows (flag.go).
//
// # Ids
//
// An entry id names its source row: dl:<download> (grab or upgrade), dl:<download>:fail (failure or stall),
// dl:<download>:import, lib:<library_action>, bl:<router_blocklist rowid>, run:<acquire_run>,
// act:<journal_action> (an operator action through the web UI, migration 00039: action.go).
//
// # Safety
//
// Every free-text field goes through clean (provider.CleanMessage and the arr history's unsafe-value test): no URL,
// key, passkey, token or magnet is ever returned. GUIDs, download links and magnets are never read. File checks use
// what the library's probe (which reads through the zurg mount only) stored; the journal reads no media
// and talks to no provider.
//
// # Retention
//
// See Prune: the journal's own decision rows are kept in full for 180 days, then trimmed or dropped. Everything else
// follows its owner's retention.
package journal

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/textsearch"
)

// ErrNotFound is an id the journal does not know.
var ErrNotFound = errors.New("journal: no such entry")

// ErrBadFilter is a filter value the journal cannot use.
var ErrBadFilter = errors.New("journal: bad filter")

// Config is what New needs.
type Config struct {
	Store  *store.Store
	Policy *policy.Policy
	Logger *slog.Logger
	Now    func() time.Time
	// PolicyFor is the policy of the settings generation an operation runs under (serve: the captured runtime, else
	// the current one), so a version activated after startup is journaled and judged as active. nil, or a nil
	// result, uses Policy.
	PolicyFor func(context.Context) *policy.Policy
	// ReadOnly leaves the flags unchecked rows wait for alone (theatarr journal reads the database only).
	ReadOnly bool
}

// Journal reads the journal and stores its flags.
type Journal struct {
	st     *store.Store
	pol    *policy.Policy
	polFor func(context.Context) *policy.Policy
	log    *slog.Logger
	now    func() time.Time
	ro     bool

	fillMu sync.Mutex // one flag fill at a time (flag.go)
	mu     sync.Mutex
	acts   *bool // journal_action exists (migration 00039)
	sizes  *sizeStats
	sizeAt time.Time
	titles map[int64]*titleInfo // shared by builders for titleTTL, under tmu
	tAt    time.Time
	tmu    sync.Mutex
}

// titleTTL is how long loaded titles are reused.
const titleTTL = 5 * time.Minute

// New builds a journal.
func New(cfg Config) *Journal {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Journal{st: cfg.Store, pol: cfg.Policy, polFor: cfg.PolicyFor, ro: cfg.ReadOnly, log: cfg.Logger.With("component", "journal"),
		now: cfg.Now}
}

// policy is the policy in force for ctx (Config.PolicyFor), else the startup one.
func (j *Journal) policy(ctx context.Context) *policy.Policy {
	if j.polFor != nil {
		if p := j.polFor(ctx); p != nil {
			return p
		}
	}
	return j.pol
}

// Filter selects entries. Every field is optional.
type Filter struct {
	// Since keeps entries at or after it.
	Since time.Time
	// Title is what a person typed: words that must all be in a title's name (case-insensitive); a number also
	// matches the title with that id ("2001" finds title 2001 and "2001: A Space Odyssey").
	Title string
	// TitleID keeps one title's entries, by its id (a title's own History and links from it); it takes the place of
	// Title.
	TitleID int64
	// Version is a version key.
	Version string
	// Restricted keeps entries of the titles in Within only (none when Within is empty): an account's own titles.
	// The listing pages over them, so a page is never short because of them.
	Restricted bool
	Within     []int64
	// WithinVersions narrows Restricted titles listed in it to entries of those versions (and entries naming no
	// version); a title in Within but not here keeps every entry.
	WithinVersions map[int64][]string
	// TitleKind keeps entries of titles of this kind: movie, series (both without anime) or anime.
	TitleKind string
	// Kinds keeps these kinds.
	Kinds []string
	// Suspect keeps entries with a stored flag of severity warn or error (flag.go).
	Suspect bool
	// Severity keeps entries with a stored flag of at least this severity (info, warn, error).
	Severity string
	// Cursor continues a listing: the NextCursor of the previous page.
	Cursor string
	// Limit is the page size: default 50, at most 500.
	Limit int
	// Events keeps these History events (History only, history.go).
	Events []string
	// Household is History for an account without "see files" (History only, history.go): no Grabbed rows, and the
	// rows of one title, episodes, event and day are one row, the latest.
	Household bool
}

// Page is one page of entries, newest first.
type Page struct {
	Items []*Entry `json:"items"`
	// NextCursor continues the listing; empty at the end.
	NextCursor string `json:"next_cursor,omitempty"`
}

// head is one entry's place in the listing.
type head struct {
	id      string
	at      int64
	titleID int64
	version string
	kind    string
}

type cursor struct {
	at int64
	id string
}

func encodeCursor(h head) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(h.at, 10) + "|" + h.id))
}

func decodeCursor(s string) (*cursor, error) {
	if s == "" {
		return nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%w: cursor", ErrBadFilter)
	}
	at, id, ok := strings.Cut(string(b), "|")
	n, err := strconv.ParseInt(at, 10, 64)
	if !ok || err != nil || id == "" {
		return nil, fmt.Errorf("%w: cursor", ErrBadFilter)
	}
	return &cursor{at: n, id: id}, nil
}

// Validate checks a filter's values.
func (f *Filter) Validate() error {
	for _, k := range f.Kinds {
		if !slices.Contains(Kinds, k) {
			return fmt.Errorf("%w: kind %q: want one of %s", ErrBadFilter, k, strings.Join(Kinds, ", "))
		}
	}
	if f.TitleKind != "" && f.TitleKind != "movie" && f.TitleKind != "series" && f.TitleKind != "anime" {
		return fmt.Errorf("%w: title kind %q: want movie, series or anime", ErrBadFilter, f.TitleKind)
	}
	if f.Severity != "" && SevRank(f.Severity) == 0 {
		return fmt.Errorf("%w: severity %q: want info, warn or error", ErrBadFilter, f.Severity)
	}
	if f.Limit < 0 {
		return fmt.Errorf("%w: negative limit", ErrBadFilter)
	}
	_, err := decodeCursor(f.Cursor)
	return err
}

// failureRunSQL says an acquire run is a re-search after a failure (router.FailureResearchSQL): Search now, the
// retries of a new release and the old retry chains are ordinary searches.
const failureRunSQL = `(` + router.FailureResearchSQL + `)`

const runHeadSQL = `SELECT 'run:' || r.id AS id, r.created_at AS at, r.title_id, r.version_key,
	CASE WHEN ` + failureRunSQL + ` THEN 'research' ELSE 'search' END AS kind, NULL AS parent FROM acquire_run r`

// grabKindSQL is a download's kind (alias d): upgrade when an earlier download of the version had imported a file
// of its episodes, else grab.
const grabKindSQL = store.HistoryGrabKindSQL

// headParts are every source's entries as (id, at, title_id, version_key, kind, parent), by id prefix. parent is the
// entry whose line stands for this one in the list (docs/journal.md "The list"), or NULL: a download's failure, import
// and link/replace actions stand under its grab; the other files of an adoption (its History group) under the
// group's first; a file an adoption replaced under the adoption; a hand unlink's action under its library action.
// A parent always has the entry's title and library.
var headParts = []struct{ prefix, sql string }{
	{"dl", `SELECT 'dl:' || d.id, d.created_at, d.title_id, d.version_key, ` + grabKindSQL + `, NULL FROM download d`},
	{"dl", `SELECT 'dl:' || d.id || ':fail', d.updated_at, d.title_id, d.version_key,
	CASE WHEN d.error LIKE 'stalled%' THEN 'stall' ELSE 'failure' END, 'dl:' || d.id
FROM download d WHERE d.state = 'failed'`},
	{"dl", `SELECT 'dl:' || li.download_id || ':import', li.updated_at, d.title_id, d.version_key, 'import', 'dl:' || d.id
FROM library_import li JOIN download d ON d.id = li.download_id`},
	{"lib", libHeadSQL + `WHERE la.kind IN ('link', 'replace', 'relink', 'dead')`},
	{"bl", `SELECT 'bl:' || b.rowid, b.created_at, COALESCE(b.title_id, 0), b.version_key, 'blocklist', NULL
FROM router_blocklist b`},
	{"run", runHeadSQL + ` WHERE ` + failureRunSQL + ` OR r.state IN ('nothing', 'failed')`},
}

// adoptGroupSQL is the History group row (alias p) of an adoption's action (alias la) that is not the group's first:
// the same title, library, release and day (store.HistoryLibraryAction's grouping).
var adoptGroupSQL = `FROM history p WHERE p.event = 'adopted' AND p.title_id = ` + libTitleSQL + `
		AND p.version_key = COALESCE(la.version_key, '')
		AND p.at >= la.at / 86400000 * 86400000 AND p.at < la.at / 86400000 * 86400000 + 86400000
		AND COALESCE(p.release, '') = ` + store.HistoryAdoptReleaseSQL + ` AND p.ref <> 'lib:' || la.id
		AND NOT EXISTS (SELECT 1 FROM history o WHERE o.ref = 'lib:' || la.id)
		AND EXISTS (SELECT 1 FROM library_action pa WHERE pa.id = CAST(substr(p.ref, 5) AS INTEGER))`

// libParentSQL is a library action's parent (alias la; headParts).
var libParentSQL = `CASE
	WHEN la.download_id IS NOT NULL THEN CASE WHEN la.kind IN ('link', 'replace') THEN (SELECT 'dl:' || d.id
		FROM download d WHERE d.id = la.download_id AND d.version_key = COALESCE(la.version_key, '')
		AND (la.title_id IS NULL OR la.title_id = d.title_id)) END
	WHEN la.kind = 'link' AND la.mode = 'applied' AND la.detail LIKE 'adopted from %' THEN ` + adoptParentMark + `
	WHEN la.kind = 'replace' AND la.mode = 'applied' THEN (SELECT p.ref FROM history JOIN history p ON ` +
	adoptionOfRemovalSQL(`p.at BETWEEN history.at - 60000 AND history.at + 60000`) + `
		AND EXISTS (SELECT 1 FROM library_action pa WHERE pa.id = CAST(substr(p.ref, 5) AS INTEGER))
		WHERE history.ref = 'lib:' || la.id AND history.event = 'removed' ORDER BY p.at, p.ref LIMIT 1)
END`

// parentAtSQL and parentKindSQL are the time and kind of a head's parent (column parent), for the filter.
const (
	parentAtSQL = `COALESCE(CASE WHEN parent LIKE 'dl:%' THEN (SELECT d.created_at FROM download d
		WHERE d.id = CAST(substr(parent, 4) AS INTEGER)) ELSE (SELECT la.at FROM library_action la
		WHERE la.id = CAST(substr(parent, 5) AS INTEGER)) END, 0)`
	parentKindSQL = `CASE WHEN parent LIKE 'dl:%' THEN (SELECT ` + grabKindSQL + ` FROM download d
		WHERE d.id = CAST(substr(parent, 4) AS INTEGER)) ELSE 'link' END`
)

// adoptParentMark stands for an adoption's parent in libParentSQL: the group's first action (adoptGroupSQL) where the
// parent's time is needed (a since bound), else only whether there is one (headFastSQL).
const adoptParentMark = `/*adopt-parent*/`

// headSQL is the union of every part, with each parent's id; headFastSQL the same where an adoption's parent is only
// known to exist ('lib:'): an adoption action is not its group's first exactly when it has no History row of its own,
// one lookup in the ref index instead of a search of the day's group rows. The filter needs a lib parent's id only for
// its time (parentAtSQL, the since bound): its kind is always link.
var (
	headSQL = strings.Replace(unionOf(func(string) bool { return true }), adoptParentMark,
		`(SELECT p.ref `+adoptGroupSQL+` ORDER BY p.id LIMIT 1)`, 1)
	headFastSQL = strings.Replace(unionOf(func(string) bool { return true }), adoptParentMark,
		`CASE WHEN NOT EXISTS (SELECT 1 FROM history o WHERE o.ref = 'lib:' || la.id) THEN 'lib:' END`, 1)
)

// flaggedHeadSQL lists the History rows by their entries, at the entries' own times (a grab's row is dated when a
// provider took it, its entry when it was created), before a severity condition.
const flaggedHeadSQL = `SELECT ` + entryOfRefSQL + ` AS id, CASE WHEN kind IN ('grab', 'upgrade') THEN COALESCE((SELECT
	d.created_at FROM download d WHERE d.id = history.download_id), at) ELSE at END AS at, title_id, version_key, kind
	FROM history WHERE `

// libHeadSQL is the library part without its filter.
var libHeadSQL = `SELECT 'lib:' || la.id, la.at, ` + libTitleSQL + `,
	COALESCE(la.version_key, ''), 'link', ` + libParentSQL + `
FROM library_action la `

// unionOf is the union of the parts keep selects. Its first member only names the columns.
func unionOf(keep func(prefix string) bool) string {
	qs := []string{`SELECT '' AS id, 0 AS at, 0 AS title_id, '' AS version_key, '' AS kind, NULL AS parent WHERE 0`}
	for _, p := range headParts {
		if keep(p.prefix) {
			qs = append(qs, p.sql)
		}
	}
	return strings.Join(qs, "\nUNION ALL\n")
}

// heads returns up to n entries' places, newest first, after cur. A flagged listing (Suspect or Severity) reads the
// flagged History rows (flag.go) instead of every source, and folds nothing. Otherwise an entry whose parent
// (headParts) matches the filter too is left out: its parent's line stands for it. Title and library are the
// parent's own, so only the since bound and the kinds can tell them apart; folding does not depend on the page.
func (j *Journal) heads(ctx context.Context, f Filter, titles []int64, cur *cursor, n int) ([]head, error) {
	q := headFastSQL
	if !f.Since.IsZero() {
		q = headSQL
	}
	var args []any
	fold := false
	switch {
	case f.Severity == SevError:
		q = flaggedHeadSQL + `severity = 'error'`
	case f.Suspect || f.Severity == SevWarn:
		q = flaggedHeadSQL + flaggedWhere
	case f.Severity == SevInfo:
		q = flaggedHeadSQL + `severity <> ''`
	default:
		fold = true
		if j.hasActions(ctx) {
			q += actionHeadSQL
		}
	}
	q = `SELECT id, at, title_id, version_key, kind FROM (` + q + `) WHERE 1 = 1`
	if fold {
		var match []string
		if !f.Since.IsZero() {
			match, args = append(match, parentAtSQL+` >= ?`), append(args, store.Millis(f.Since))
		}
		if len(f.Kinds) > 0 {
			match = append(match, parentKindSQL+` IN (`+store.Placeholders(len(f.Kinds))+`)`)
			for _, k := range f.Kinds {
				args = append(args, k)
			}
		}
		if len(match) == 0 {
			q += ` AND parent IS NULL`
		} else {
			q += ` AND NOT (parent IS NOT NULL AND ` + strings.Join(match, " AND ") + `)`
		}
	}
	if !f.Since.IsZero() {
		q += ` AND at >= ?`
		args = append(args, store.Millis(f.Since))
	}
	if f.byTitle() {
		if len(titles) == 0 {
			return nil, nil
		}
		q += ` AND title_id IN (` + store.Placeholders(len(titles)) + `)`
		for _, t := range titles {
			args = append(args, t)
		}
	}
	if f.Restricted {
		if len(f.Within) == 0 {
			return nil, nil
		}
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
		q += ` AND (` + strings.Join(or, " OR ") + `)`
	}
	switch f.TitleKind {
	case "anime":
		q += ` AND title_id IN (SELECT id FROM title WHERE anime = 1)`
	case "movie", "series":
		q += ` AND title_id IN (SELECT id FROM title WHERE anime = 0 AND kind = ?)`
		args = append(args, f.TitleKind)
	}
	if f.Version != "" {
		q += ` AND version_key = ?`
		args = append(args, f.Version)
	}
	if len(f.Kinds) > 0 {
		q += ` AND kind IN (` + store.Placeholders(len(f.Kinds)) + `)`
		for _, k := range f.Kinds {
			args = append(args, k)
		}
	}
	if cur != nil {
		q += ` AND (at < ? OR (at = ? AND id < ?))`
		args = append(args, cur.at, cur.at, cur.id)
	}
	q += ` ORDER BY at DESC, id DESC LIMIT ?`
	args = append(args, n)
	rows, err := j.st.Reader().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("journal: list: %w", err)
	}
	defer rows.Close()
	var out []head
	for rows.Next() {
		var h head
		if err := rows.Scan(&h.id, &h.at, &h.titleID, &h.version, &h.kind); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// byTitle says whether the filter keeps some titles only (TitleID or Title).
func (f *Filter) byTitle() bool { return f.TitleID > 0 || f.Title != "" }

// titleIDs resolves the title filter: TitleID exactly; typed text as words that must all be in the name, and a
// typed number as that id too ("2001" finds title 2001 and "2001: A Space Odyssey"; "1923" also "Hollywood in 1923").
func (j *Journal) titleIDs(ctx context.Context, f Filter) ([]int64, error) {
	if f.TitleID > 0 {
		return []int64{f.TitleID}, nil
	}
	s := strings.TrimSpace(f.Title)
	if s == "" {
		return nil, nil
	}
	var out []int64
	where, args := textsearch.Where(s, "name")
	if id, err := strconv.ParseInt(s, 10, 64); err == nil {
		// The id counts even when its title row is gone (entries outlive a removed title), as before.
		out = append(out, id)
		if where != "" {
			where, args = where+` AND id <> ?`, append(args, id)
		}
	}
	if where == "" {
		return out, nil
	}
	rows, err := j.st.Reader().QueryContext(ctx, `SELECT id FROM title WHERE `+where+` LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// List returns one page of entries, newest first.
func (j *Journal) List(ctx context.Context, f Filter) (*Page, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	if f.Limit == 0 {
		f.Limit = 50
	}
	f.Limit = min(f.Limit, 500)
	cur, _ := decodeCursor(f.Cursor)
	titles, err := j.titleIDs(ctx, f)
	if err != nil {
		return nil, err
	}
	if _, err := j.Fill(ctx, fillBatch); err != nil {
		return nil, err
	}
	b := j.newBuilder(ctx)
	b.list = true
	page := &Page{Items: []*Entry{}}
	batch := f.Limit + 1
	for {
		hs, err := j.heads(ctx, f, titles, cur, batch)
		if err != nil {
			return nil, err
		}
		for _, h := range hs {
			if len(page.Items) == f.Limit {
				// cur is the last head looked at: the next page starts after it.
				page.NextCursor = encodeCursor(head{at: cur.at, id: cur.id})
				return page, b.setFlags(page.Items)
			}
			cur = &cursor{at: h.at, id: h.id}
			e, err := b.load(h)
			if err != nil {
				return nil, err
			}
			if e != nil {
				page.Items = append(page.Items, e)
			}
		}
		if len(hs) < batch {
			return page, b.setFlags(page.Items)
		}
	}
}

// Get returns one entry.
func (j *Journal) Get(ctx context.Context, id string) (*Entry, error) {
	if _, err := j.Fill(ctx, fillBatch); err != nil {
		return nil, err
	}
	h, err := j.headOf(ctx, id)
	if err != nil {
		return nil, err
	}
	b := j.newBuilder(ctx)
	b.full = true
	e, err := b.load(h)
	if err != nil {
		return nil, err
	}
	if e == nil {
		return nil, ErrNotFound
	}
	return e, b.setFlags([]*Entry{e})
}

// headOf finds one entry's head by id.
func (j *Journal) headOf(ctx context.Context, id string) (head, error) {
	if id == "" || len(id) > 200 {
		return head{}, ErrNotFound
	}
	prefix, _, _ := strings.Cut(id, ":")
	q := strings.Replace(unionOf(func(p string) bool { return p == prefix }), adoptParentMark, "NULL", 1) // parent unused
	// A direct search handoff includes routed runs too; the feed still represents those by their downloads.
	if prefix == "run" {
		q = runHeadSQL
	}
	if prefix == "act" && j.hasActions(ctx) {
		q += actionHeadSQL
	}
	var h head
	err := j.st.Reader().QueryRowContext(ctx, `SELECT id, at, title_id, version_key, kind FROM (`+q+`) WHERE id = ?`,
		id).Scan(&h.id, &h.at, &h.titleID, &h.version, &h.kind)
	if errors.Is(err, sql.ErrNoRows) {
		return head{}, ErrNotFound
	}
	if err != nil {
		return head{}, fmt.Errorf("journal: get: %w", err)
	}
	return h, nil
}
