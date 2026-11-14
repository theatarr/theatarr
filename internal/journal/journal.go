// Package journal is Theatarr's grab journal: one entry per decision (grab, upgrade, search, re-search,
// router dry run) and per thing that happened to one afterwards (failure, stall, blocklist, import, link), each with
// its reasoning (the chosen release, its score and formats, the runners-up and their rejections), provider attempts
// and cache evidence, import results, suspect flags and verdicts. docs/journal.md is the reader's guide.
//
// # Sources
//
// The journal is a reader over the tables that already record all of this; nothing is copied:
//
//   - download, download_attempt, provider_cache, router_blocklist (router), acquire_run (acquirer), router_plan;
//   - library_import, library_action, link, content_file (the library).
//
// Two tables are its own (migration 00024): journal_decision, the candidates a search decided between when it
// handed work to the router (written by internal/journal/snapshot), and journal_verdict.
//
// # Ids
//
// An entry id names its source row: dl:<download> (grab or upgrade), dl:<download>:fail (failure or stall),
// dl:<download>:import, lib:<library_action>, bl:<router_blocklist rowid>, run:<acquire_run>,
// plan:<router_plan>, v:<journal_verdict>, act:<journal_action> (an operator action through the web UI, migration 00039: action.go).
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
// See Prune: the journal's own decision rows are kept in full for 180 days, then trimmed or dropped; verdicts are
// kept for good. Everything else follows its owner's retention.
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

	"github.com/theatarr/theatarr/internal/jobs"
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
	// Queue enqueues the re-search of a "wrong" verdict; nil records verdicts only.
	Queue  *jobs.Queue
	Logger *slog.Logger
	Now    func() time.Time
	// PolicyFor is the policy of the settings generation an operation runs under (serve: the captured runtime, else
	// the current one), so a version activated after startup is journaled and judged as active. nil, or a nil
	// result, uses Policy.
	PolicyFor func(context.Context) *policy.Policy
}

// Journal reads the journal and records verdicts.
type Journal struct {
	st     *store.Store
	pol    *policy.Policy
	polFor func(context.Context) *policy.Policy
	q      *jobs.Queue
	log    *slog.Logger
	now    func() time.Time

	mu     sync.Mutex
	own    *bool // journal_decision and journal_verdict exist (a database not migrated to 00024 has neither)
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
	return &Journal{st: cfg.Store, pol: cfg.Policy, polFor: cfg.PolicyFor, q: cfg.Queue, log: cfg.Logger.With("component", "journal"),
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
	// Title is a title id, or a case-insensitive part of a title's name.
	Title string
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
	// Suspect keeps entries with an unacknowledged flag of severity warn or error.
	Suspect bool
	// Severity keeps entries with an unacknowledged flag of at least this severity (info, warn, error).
	Severity string
	// Cursor continues a listing: the NextCursor of the previous page.
	Cursor string
	// Limit is the page size: default 50, at most 500.
	Limit int
	// ScanLimit caps how many entries one page may build while looking for suspect ones: default 2000.
	ScanLimit int
	// Events keeps these History events (History only, history.go).
	Events []string
}

// Page is one page of entries, newest first.
type Page struct {
	Items []*Entry `json:"items"`
	// NextCursor continues the listing; empty at the end.
	NextCursor string `json:"next_cursor,omitempty"`
	// Scanned counts the entries looked at to fill the page (more than len(Items) with suspect or severity).
	Scanned int `json:"scanned"`
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
	if f.Limit < 0 || f.ScanLimit < 0 {
		return fmt.Errorf("%w: negative limit", ErrBadFilter)
	}
	_, err := decodeCursor(f.Cursor)
	return err
}

func (j *Journal) hasOwnTables(ctx context.Context) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.own != nil {
		return *j.own
	}
	var n int
	err := j.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table'
		AND name IN ('journal_decision', 'journal_verdict')`).Scan(&n)
	if err != nil {
		return false
	}
	ok := n == 2
	j.own = &ok
	return ok
}

// failureRunSQL says an acquire run is a re-search after a failure (router.FailureResearchSQL): Search now, the
// retries of a new release and the old retry chains are ordinary searches.
const failureRunSQL = `(` + router.FailureResearchSQL + `)`

const runHeadSQL = `SELECT 'run:' || r.id AS id, r.created_at AS at, r.title_id, r.version_key,
	CASE WHEN ` + failureRunSQL + ` THEN 'research' ELSE 'search' END AS kind FROM acquire_run r`

// grabKindSQL is a download's kind (alias d): upgrade when the version already had a file of its episodes, else grab.
const grabKindSQL = `CASE WHEN EXISTS (SELECT 1 FROM version_file vf WHERE vf.version_id = d.version_id
			AND COALESCE(vf.added_at, 0) < d.created_at
			AND (d.episodes = '[]' OR EXISTS (SELECT 1 FROM version_file_episode vfe WHERE vfe.version_file_id = vf.id
				AND vfe.episode_id IN (SELECT value FROM json_each(d.episodes)))))
		OR EXISTS (SELECT 1 FROM download p WHERE p.version_id = d.version_id AND p.id <> d.id
			AND p.state = 'imported' AND p.imported_at < d.created_at
			AND (d.episodes = '[]' OR EXISTS (SELECT 1 FROM json_each(p.episodes) pe
				WHERE pe.value IN (SELECT value FROM json_each(d.episodes)))))
	THEN 'upgrade' ELSE 'grab' END`

// headParts are every source's entries as (id, at, title_id, version_key, kind), by id prefix.
var headParts = []struct{ prefix, sql string }{
	{"dl", `SELECT 'dl:' || d.id, d.created_at, d.title_id, d.version_key, ` + grabKindSQL + ` FROM download d`},
	{"dl", `SELECT 'dl:' || d.id || ':fail', d.updated_at, d.title_id, d.version_key,
	CASE WHEN d.error LIKE 'stalled%' THEN 'stall' ELSE 'failure' END
FROM download d WHERE d.state = 'failed'`},
	{"dl", `SELECT 'dl:' || li.download_id || ':import', li.updated_at, d.title_id, d.version_key, 'import'
FROM library_import li JOIN download d ON d.id = li.download_id`},
	{"lib", `SELECT 'lib:' || la.id, la.at,
	COALESCE((SELECT title_id FROM download WHERE id = la.download_id), (SELECT title_id FROM link WHERE id = la.link_id),
		(SELECT vf.title_id FROM version_file vf WHERE vf.path = la.path LIMIT 1), 0),
	COALESCE(la.version_key, ''), 'link'
FROM library_action la WHERE la.kind IN ('link', 'replace', 'relink', 'dead')`},
	{"bl", `SELECT 'bl:' || b.rowid, b.created_at, COALESCE(b.title_id, 0), b.version_key, 'blocklist' FROM router_blocklist b`},
	{"run", runHeadSQL + ` WHERE ` + failureRunSQL + ` OR r.state IN ('nothing', 'failed')`},
	{"plan", `SELECT 'plan:' || p.id, p.created_at, p.title_id, p.version_key, 'plan' FROM router_plan p`},
}

// headSQL is the union of every part.
var headSQL = unionOf(func(string) bool { return true })

// unionOf is the union of the parts keep selects. Its first member only names the columns.
func unionOf(keep func(prefix string) bool) string {
	qs := []string{`SELECT '' AS id, 0 AS at, 0 AS title_id, '' AS version_key, '' AS kind WHERE 0`}
	for _, p := range headParts {
		if keep(p.prefix) {
			qs = append(qs, p.sql)
		}
	}
	return strings.Join(qs, "\nUNION ALL\n")
}

const verdictHeadSQL = `
UNION ALL
SELECT 'v:' || jv.id, jv.created_at, COALESCE(jv.title_id, 0), jv.version_key, 'verdict' FROM journal_verdict jv`

// heads returns up to n entries' places, newest first, after cur.
func (j *Journal) heads(ctx context.Context, f Filter, titles []int64, cur *cursor, n int) ([]head, error) {
	q := headSQL
	if j.hasOwnTables(ctx) {
		q += verdictHeadSQL
	}
	if j.hasActions(ctx) {
		q += actionHeadSQL
	}
	q = `SELECT id, at, title_id, version_key, kind FROM (` + q + `) WHERE 1 = 1`
	var args []any
	if !f.Since.IsZero() {
		q += ` AND at >= ?`
		args = append(args, store.Millis(f.Since))
	}
	if f.Title != "" {
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
	if f.Suspect || f.Severity != "" {
		// A link entry's only flag is dead_link, and only a finding's first dead action carries it (deadlink.go):
		// the repeats are skipped before they are built.
		q += ` AND (kind <> 'link' OR id IN (` + firstDeadSQL + `))`
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

// titleIDs resolves the title filter: a number is an id, anything else words that must all be in the name.
func (j *Journal) titleIDs(ctx context.Context, s string) ([]int64, error) {
	if s == "" {
		return nil, nil
	}
	if id, err := strconv.ParseInt(s, 10, 64); err == nil {
		return []int64{id}, nil
	}
	where, args := textsearch.Where(s, "name")
	if where == "" {
		return nil, nil
	}
	rows, err := j.st.Reader().QueryContext(ctx, `SELECT id FROM title WHERE `+where+` LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
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
	if f.ScanLimit == 0 {
		f.ScanLimit = 2000
	}
	cur, _ := decodeCursor(f.Cursor)
	titles, err := j.titleIDs(ctx, f.Title)
	if err != nil {
		return nil, err
	}
	filtered := f.Suspect || f.Severity != ""
	b := j.newBuilder(ctx)
	page := &Page{Items: []*Entry{}}
	batch := f.Limit + 1
	if filtered {
		batch = 200
	}
	for {
		hs, err := j.heads(ctx, f, titles, cur, batch)
		if err != nil {
			return nil, err
		}
		for _, h := range hs {
			if len(page.Items) == f.Limit || (filtered && page.Scanned >= f.ScanLimit) {
				// cur is the last head looked at: the next page starts after it.
				page.NextCursor = encodeCursor(head{at: cur.at, id: cur.id})
				return page, nil
			}
			page.Scanned++
			cur = &cursor{at: h.at, id: h.id}
			e, err := b.build(h)
			if err != nil {
				return nil, err
			}
			if e != nil && keep(e, f) {
				page.Items = append(page.Items, e)
			}
		}
		if len(hs) < batch {
			return page, nil
		}
	}
}

func keep(e *Entry, f Filter) bool {
	if f.Suspect && !e.Suspect {
		return false
	}
	if f.Severity != "" && SevRank(e.Severity) < SevRank(f.Severity) {
		return false
	}
	return true
}

// Get returns one entry.
func (j *Journal) Get(ctx context.Context, id string) (*Entry, error) {
	h, err := j.headOf(ctx, id)
	if err != nil {
		return nil, err
	}
	e, err := j.newBuilder(ctx).build(h)
	if err != nil {
		return nil, err
	}
	if e == nil {
		return nil, ErrNotFound
	}
	return e, nil
}

// headOf finds one entry's head by id.
func (j *Journal) headOf(ctx context.Context, id string) (head, error) {
	if id == "" || len(id) > 200 {
		return head{}, ErrNotFound
	}
	prefix, _, _ := strings.Cut(id, ":")
	q := unionOf(func(p string) bool { return p == prefix })
	// A direct search handoff includes routed runs too; the feed still represents those by their downloads.
	if prefix == "run" {
		q = runHeadSQL
	}
	if prefix == "v" && j.hasOwnTables(ctx) {
		q += verdictHeadSQL
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
