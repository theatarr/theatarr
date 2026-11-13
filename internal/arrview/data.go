package arrview

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/availability"
	"github.com/theatarr/theatarr/internal/store"
)

type sqlTx = sql.Tx

// entry is a title as one view sees it: the title, the view's versions it has (removed ones left out), their files
// the view's own record of it, tags and release dates.
type entry struct {
	id             int64
	kind           string
	name           string
	sortName       string
	year           int
	secondaryYear  int
	tmdb, tvdb     int
	imdb           string
	anime          bool
	seriesType     string // the title's effective type (store.SeriesTypeSQL); '' for a movie
	created        int64
	versions       []*ver // in view rank order
	rec            *viewRec
	tags           []int64
	dates          map[string]time.Time // theatrical, digital, physical
	datesRefreshed bool
	seriesStatus   string // series_meta.status in lower case (Sonarr's names), "" when not refreshed
}

type ver struct {
	id        int64
	key       string
	monitored bool
	state     string
	created   int64
	file      *vfile
	// monitorNew is a series version's monitorNewItems intent (series_monitor), "" without a row.
	monitorNew string
	// rule is a movie version's availability rule (availability.Rules); Radarr views only.
	rule availability.Rule
}

// vfile is a version_file row: the file an arr holds for a version, as imported.
type vfile struct {
	id           int64
	instance     string
	path         string
	relPath      string
	size         int64
	quality      string
	revision     int
	real         int
	isRepack     bool
	group        string
	scene        string
	edition      string
	languages    []string
	mediaInfo    json.RawMessage
	cfScore      sql.NullInt64
	flags        int
	origPath     string
	cutoffNotMet bool
	added        sql.NullInt64
}

// viewRec is an arrview_title row.
type viewRec struct {
	qpid         int64
	root         string
	minAvail     string
	seasonFolder bool
	seriesType   string
	monitorNew   string
	added        int64
}

// viewID is the id a view answers for a title.
func viewID(titleID int64) int64 { return titleID + IDBase }

func (e *entry) viewID() int64 { return viewID(e.id) }

func (e *entry) monitored() bool {
	for _, v := range e.versions {
		if v.monitored {
			return true
		}
	}
	return false
}

// file is the view's file for a movie: the first version in rank order that has one.
func (e *entry) file() (*ver, *vfile) {
	for _, v := range e.versions {
		if v.file != nil {
			return v, v.file
		}
	}
	return nil, nil
}

// hasFile: any of the view's versions has a file (or is filled without a file row, e.g. from a later import).
func (e *entry) hasFile() bool {
	for _, v := range e.versions {
		if v.file != nil || v.state == "filled" || v.state == "cutoff_met" {
			return true
		}
	}
	return false
}

// primary is the version that speaks for the title: the one holding the file, else the first.
func (e *entry) primary() *ver {
	if v, _ := e.file(); v != nil {
		return v
	}
	if len(e.versions) > 0 {
		return e.versions[0]
	}
	return nil
}

func (e *entry) versionKeys() []string {
	out := make([]string, len(e.versions))
	for i, v := range e.versions {
		out[i] = v.key
	}
	return out
}

// scope is the SQL condition for "a version of this view, not removed", over version v joined to title t, with an
// optional title id filter.
func scope(v *View, titleIDs []int64) (string, []any) {
	var b strings.Builder
	args := make([]any, 0, len(v.Versions)+len(titleIDs)+1)
	b.WriteString("v.policy_key IN (")
	for i, k := range v.Versions {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("?")
		args = append(args, k)
	}
	b.WriteString(") AND t.kind = ? AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id)")
	args = append(args, string(v.kind()))
	if titleIDs != nil {
		b.WriteString(" AND v.title_id IN (SELECT value FROM json_each(?))")
		args = append(args, jsonIDs(titleIDs))
	}
	return b.String(), args
}

// load reads the view's titles, all of them (titleIDs nil) or the ones listed, ordered by title id.
func (s *Service) load(ctx context.Context, v *View, titleIDs []int64) ([]*entry, error) {
	db := s.st.Reader()
	where, args := scope(v, titleIDs)
	byID := map[int64]*entry{}
	var order []*entry

	rows, err := db.QueryContext(ctx, `SELECT DISTINCT t.id, t.kind, t.name, t.sort_name, COALESCE(t.year, 0),
		COALESCE(t.secondary_year, 0), COALESCE(t.tmdb_id, 0), COALESCE(t.tvdb_id, 0), COALESCE(t.imdb_id, ''), t.anime,
		t.created_at, EXISTS (SELECT 1 FROM title_meta m WHERE m.title_id = t.id AND m.movie_dates_at IS NOT NULL),
		`+store.SeriesTypeSQL("t")+`, COALESCE((SELECT lower(m.status) FROM series_meta m WHERE m.title_id = t.id), '')
		FROM title t JOIN version v ON v.title_id = t.id WHERE `+where+` ORDER BY t.id`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		e := &entry{dates: map[string]time.Time{}}
		if err := rows.Scan(&e.id, &e.kind, &e.name, &e.sortName, &e.year, &e.secondaryYear, &e.tmdb, &e.tvdb, &e.imdb,
			&e.anime, &e.created, &e.datesRefreshed, &e.seriesType, &e.seriesStatus); err != nil {
			rows.Close()
			return nil, err
		}
		byID[e.id] = e
		order = append(order, e)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	if len(order) == 0 {
		return nil, nil
	}

	rank := map[string]int{}
	for i, k := range v.Versions {
		rank[k] = i
	}
	vers := map[int64]*ver{}
	rows, err = db.QueryContext(ctx, `SELECT v.id, v.title_id, v.policy_key, v.monitored, v.state, v.created_at,
		COALESCE((SELECT m.monitor_new FROM series_monitor m WHERE m.version_id = v.id), '')
		FROM version v JOIN title t ON t.id = v.title_id WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		x := &ver{}
		var tid int64
		if err := rows.Scan(&x.id, &tid, &x.key, &x.monitored, &x.state, &x.created, &x.monitorNew); err != nil {
			rows.Close()
			return nil, err
		}
		if e := byID[tid]; e != nil {
			e.versions = append(e.versions, x)
			vers[x.id] = x
		}
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	for _, e := range order {
		sort.Slice(e.versions, func(i, j int) bool { return rank[e.versions[i].key] < rank[e.versions[j].key] })
	}

	// Files: per version, the owning instance's file first (version_source), then the oldest row.
	rows, err = db.QueryContext(ctx, `SELECT vf.id, vf.version_id, vf.instance, vf.path, vf.relative_path,
		vf.size_bytes, vf.quality, vf.quality_revision, vf.release_group, vf.scene_name, vf.edition, vf.languages,
		vf.media_info, vf.custom_format_score, vf.indexer_flags, vf.original_file_path, vf.cutoff_not_met, vf.added_at
		FROM version_file vf JOIN version v ON v.id = vf.version_id JOIN title t ON t.id = v.title_id
		LEFT JOIN version_source vs ON vs.version_id = v.id
		WHERE `+where+` ORDER BY (vs.instance IS NOT NULL AND vs.instance = vf.instance) DESC, vf.id`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		f := &vfile{}
		var vid int64
		var langs, mi string
		if err := rows.Scan(&f.id, &vid, &f.instance, &f.path, &f.relPath, &f.size, &f.quality, &f.revision, &f.group,
			&f.scene, &f.edition, &langs, &mi, &f.cfScore, &f.flags, &f.origPath, &f.cutoffNotMet, &f.added); err != nil {
			rows.Close()
			return nil, err
		}
		x := vers[vid]
		if x == nil || x.file != nil {
			continue
		}
		_ = json.Unmarshal([]byte(langs), &f.languages)
		if mi != "" && mi != "{}" && json.Valid([]byte(mi)) {
			f.mediaInfo = json.RawMessage(mi)
		}
		x.file = f
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}

	// Our own links: one from an import of ours wins over the arr's file (it is newer); an adopted link (the arr's
	// file, taken over) only fills a version without a file row.
	links, err := s.linkFiles(ctx, v, titleIDs)
	if err != nil {
		return nil, err
	}
	for _, l := range links {
		if x := vers[l.versionID]; x != nil && (x.file == nil || !l.adopted) {
			x.file = l.f
		}
	}

	// The view's own records and tags, restricted to the requested titles.
	rows, err = db.QueryContext(ctx, `SELECT title_id, quality_profile_id, root_folder_path, minimum_availability,
		season_folder, series_type, monitor_new_items, added_at FROM arrview_title WHERE view = ? AND title_id IN (SELECT value FROM json_each(?))`, v.Key, jsonIDs(entryIDs(order)))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		r := &viewRec{}
		var tid int64
		if err := rows.Scan(&tid, &r.qpid, &r.root, &r.minAvail, &r.seasonFolder, &r.seriesType, &r.monitorNew,
			&r.added); err != nil {
			rows.Close()
			return nil, err
		}
		if e := byID[tid]; e != nil {
			e.rec = r
		}
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	// Tags are per version: the view shows a tag that any of its versions it shows (not removed) has, once.
	rows, err = db.QueryContext(ctx, `SELECT DISTINCT tt.title_id, tt.tag_id FROM arrview_title_tag tt
		JOIN version v ON v.title_id = tt.title_id AND v.policy_key = tt.version JOIN title t ON t.id = v.title_id
		WHERE `+where+` ORDER BY tt.title_id, tt.tag_id`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var tid, tag int64
		if err := rows.Scan(&tid, &tag); err != nil {
			rows.Close()
			return nil, err
		}
		if e := byID[tid]; e != nil {
			e.tags = append(e.tags, tag)
		}
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}

	if v.Flavour == Radarr {
		// Each library's minimum availability and delay, the rule acquisition gates on.
		rules, err := availability.Rules(ctx, db, s.pol, entryIDs(order))
		if err != nil {
			return nil, err
		}
		for id, x := range vers {
			x.rule = rules[id]
		}
		// Canonical release dates (country ''), preferring a successful TMDb date refresh.
		rows, err = db.QueryContext(ctx, `SELECT d.title_id, d.type, MIN(d.date) FROM title_release_date d
			WHERE d.country = '' AND (d.source = 'tmdb-art' OR NOT EXISTS
				(SELECT 1 FROM title_meta m WHERE m.title_id = d.title_id AND m.movie_dates_at IS NOT NULL))
			AND d.title_id IN (SELECT v.title_id FROM version v JOIN title t ON t.id = v.title_id
			WHERE `+where+`) GROUP BY d.title_id, d.type`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var tid int64
			var typ, date string
			if err := rows.Scan(&tid, &typ, &date); err != nil {
				rows.Close()
				return nil, err
			}
			if d, err := time.Parse("2006-01-02", date); err == nil && byID[tid] != nil {
				byID[tid].dates[typ] = d
			}
		}
		if err := closeRows(rows); err != nil {
			return nil, err
		}
	}
	return order, nil
}

func closeRows(rows *sql.Rows) error {
	err := rows.Err()
	if cerr := rows.Close(); err == nil {
		err = cerr
	}
	return err
}

// loadOne reads one title of the view, or nil when the view does not have it.
func (s *Service) loadOne(ctx context.Context, v *View, titleID int64) (*entry, error) {
	es, err := s.load(ctx, v, []int64{titleID})
	if err != nil || len(es) == 0 {
		return nil, err
	}
	return es[0], nil
}

// resolve maps an id a client sent to a title id: ids from IDBase up are ours; smaller ones are the replaced
// instances' ids, looked up in the alias table for the view's alias_from instances, in order (docs/migration.md).
func (s *Service) resolve(ctx context.Context, v *View, id int64) (int64, error) {
	if id >= IDBase {
		return id - IDBase, nil
	}
	if len(v.AliasFrom) == 0 || id <= 0 {
		return 0, nil
	}
	q := `SELECT instance, target_id FROM arr_alias WHERE kind = ? AND arr_id = ? AND instance IN (` +
		store.Placeholders(len(v.AliasFrom)) + `)`
	args := []any{string(v.kind()), id}
	for _, in := range v.AliasFrom {
		args = append(args, in)
	}
	rows, err := s.st.Reader().QueryContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	found := map[string]int64{}
	for rows.Next() {
		var in string
		var tid int64
		if err := rows.Scan(&in, &tid); err != nil {
			rows.Close()
			return 0, err
		}
		found[in] = tid
	}
	if err := closeRows(rows); err != nil {
		return 0, err
	}
	for _, in := range v.AliasFrom {
		if tid, ok := found[in]; ok {
			return tid, nil
		}
	}
	return 0, nil
}

// resolveEntry resolves a client id and loads the title, nil when the view does not have it.
func (s *Service) resolveEntry(ctx context.Context, v *View, id int64) (*entry, error) {
	tid, err := s.resolve(ctx, v, id)
	if err != nil || tid == 0 {
		return nil, err
	}
	return s.loadOne(ctx, v, tid)
}

// titleByExternal finds a title of the view's kind by TMDb or TVDB id (whether or not the view has it).
func (s *Service) titleByExternal(ctx context.Context, v *View, tmdb, tvdb int64) (int64, error) {
	var id int64
	var err error
	if v.Flavour == Sonarr {
		err = s.st.Reader().QueryRowContext(ctx, `SELECT id FROM title WHERE kind = 'series' AND tvdb_id = ?`, tvdb).Scan(&id)
	} else {
		err = s.st.Reader().QueryRowContext(ctx, `SELECT id FROM title WHERE kind = 'movie' AND tmdb_id = ?`, tmdb).Scan(&id)
	}
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}

// titleBrief is a title the view may not have, for lookups.
type titleBrief struct {
	id                  int64
	name                string
	year, secondaryYear int
	tmdb, tvdb          int
	imdb                string
	// overview and poster: a TVDB search hit's, for a series the catalog lacks.
	overview, poster string
}

func (s *Service) briefs(ctx context.Context, where string, args ...any) ([]titleBrief, error) {
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT id, name, COALESCE(year, 0), COALESCE(secondary_year, 0),
		COALESCE(tmdb_id, 0), COALESCE(tvdb_id, 0), COALESCE(imdb_id, '') FROM title WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	var out []titleBrief
	for rows.Next() {
		var b titleBrief
		if err := rows.Scan(&b.id, &b.name, &b.year, &b.secondaryYear, &b.tmdb, &b.tvdb, &b.imdb); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, b)
	}
	return out, closeRows(rows)
}

func millis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return store.FromMillis(ms)
}

func entryIDs(es []*entry) []int64 {
	ids := make([]int64, 0, len(es))
	for _, e := range es {
		ids = append(ids, e.id)
	}
	return ids
}
