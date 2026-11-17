package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/availability"
	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// The read model behind the API and the UI. Every query runs on the reader pool.

var errNotFound = errors.New("not found")

func ts(ms int64) time.Time { return store.FromMillis(ms) }

func nullTS(n sql.NullInt64) *time.Time {
	if !n.Valid || n.Int64 == 0 {
		return nil
	}
	t := store.FromMillis(n.Int64)
	return &t
}

// TitleFilter selects titles.
type TitleFilter struct {
	Kind string // movie or series
	Q    string // words of the name, another name or the year (titlesearch.go)
	// With: the title has a file for this version key. Without: it has no file for it (no version, or a version
	// that is wanted), among titles of that library's media kind (withoutKind). These give "no WEB version" and
	// friends.
	With, Without string
	// withoutKind is the Without library's media kind (movie or series; listTitles sets it from the settings, empty
	// for a library no longer there): a series never lacks a movie library's file.
	withoutKind string
	// State: some version of the title is in this state. With plainState, filled also takes cutoff_met (a caller
	// without "see files" sees both as filled).
	State      string
	plainState bool
	// withTags fills the rows' tags (callers who see files; apiTitles sets it).
	withTags bool
	Limit    int
	Offset   int
	// The grid filters and sort (library.go).
	TitleGridFilter
}

// VersionBrief is a version on a list row.
type VersionBrief struct {
	ID        int64  `json:"id"`
	Key       string `json:"key"`
	State     string `json:"state"`
	Monitored bool   `json:"monitored"`
	HasFile   bool   `json:"has_file"`
	// UpgradeSeen: below cutoff, and a better release than the current file has been seen (upgrade.go).
	UpgradeSeen bool `json:"upgrade_seen,omitempty"`
	VersionFiles
	// The library views' per-copy reads (copies.go): ownership (Control), a series copy's episode coverage and the
	// acquisition in hand. Control and Acquisition are for callers who see files.
	Control     *VersionControl `json:"control,omitempty"`
	Coverage    *CopyCoverage   `json:"coverage,omitempty"`
	Acquisition *Acquisition    `json:"acquisition,omitempty"`
	// Profile is the library's quality profile; MinimumAvailability (movies) the copy's availability rule in force
	// (availability.Rules); Folder the title's folder in the library: the library root holding the copy's first
	// active link plus the first path element below it, empty without one. For callers who see files.
	Profile             string `json:"profile,omitempty"`
	MinimumAvailability string `json:"minimum_availability,omitempty"`
	Folder              string `json:"folder,omitempty"`
	// availState is the state availability reads (availStateSQL).
	availState string
}

// TitleRow is a title on a list.
type TitleRow struct {
	ID        int64          `json:"id"`
	Kind      string         `json:"kind"`
	Name      string         `json:"name"`
	Year      *int64         `json:"year,omitempty"`
	TMDbID    *int64         `json:"tmdb_id,omitempty"`
	TVDbID    *int64         `json:"tvdb_id,omitempty"`
	IMDbID    string         `json:"imdb_id,omitempty"`
	Anime     bool           `json:"anime"`
	Monitored bool           `json:"monitored"`
	Versions  []VersionBrief `json:"versions"`
	// Next is the next episode to air (series) or the coming release (movies), when there is one (copies.go).
	Next *NextUp `json:"next_up,omitempty"`
	// HomeRelease is a movie's home release day (availability.HomeRelease over its canonical dates), YYYY-MM-DD.
	HomeRelease string `json:"home_release,omitempty"`
	// Acquiring: some copy has a download in hand that is not blocked and is not an upgrade (copies.go). Every caller
	// gets it; it carries no provider, file or stage detail.
	Acquiring bool `json:"acquiring,omitempty"`
	// TitleAddedAt is when the title was added to Theatarr (title.created_at); list rows only (the title page has
	// created_at).
	TitleAddedAt *time.Time `json:"title_added_at,omitempty"`
	// SeriesType is a series' type in force (store.SeriesTypeSQL: standard, daily or anime); empty for a movie.
	SeriesType string `json:"series_type,omitempty"`
	// Tags are the labels compatible clients set on the title's copies in the library, each once (titleTags); see
	// files only.
	Tags []string `json:"tags,omitempty"`
	// A movie's first canonical release of each kind (availability.DatesFor), YYYY-MM-DD.
	InCinemas       string `json:"in_cinemas,omitempty"`
	DigitalRelease  string `json:"digital_release,omitempty"`
	PhysicalRelease string `json:"physical_release,omitempty"`
	// Released is the title's general release day (releasedSQL), YYYY-MM-DD: a movie's earliest cinema date or TMDb's
	// release date, whichever is earlier; a series' first air date. What the Released column, its sort and the title
	// page's About show; in_cinemas and release_date keep their own meaning.
	Released string `json:"released,omitempty"`
	// SeasonCount is how many seasons (specials left out) a series has episodes in; PreviousAiring is the latest
	// aired episode's day (YYYY-MM-DD). Series only.
	SeasonCount    int    `json:"season_count,omitempty"`
	PreviousAiring string `json:"previous_airing,omitempty"`
	TitleArt
	TitleFacts
}

// TitlePage is one page of titles.
type TitlePage struct {
	Total  int        `json:"total"`
	Limit  int        `json:"limit"`
	Offset int        `json:"offset"`
	Items  []TitleRow `json:"items"`
}

// hasFileSQL is true when version v has a file: an active link.
const hasFileSQL = `EXISTS (SELECT 1 FROM link_version lv JOIN link l ON l.id = lv.link_id WHERE lv.version_id = v.id AND l.state = 'active')`

func (f TitleFilter) where() (string, []any) {
	// Only titles in the library (a current copy): a removed title is in no list, total or facet.
	conds := []string{inLibrary}
	var args []any
	if f.Kind != "" {
		conds = append(conds, "t.kind = ?")
		args = append(args, f.Kind)
	}
	if cond, a := titleTextWhere(f.Q); cond != "" {
		conds = append(conds, cond)
		args = append(args, a...)
	}
	if f.With != "" {
		conds = append(conds, "EXISTS (SELECT 1 FROM version v WHERE v.title_id = t.id AND v.policy_key = ? AND "+
			liveVersion+" AND "+hasFileSQL+")")
		args = append(args, f.With)
	}
	if f.Without != "" {
		conds = append(conds, "NOT EXISTS (SELECT 1 FROM version v WHERE v.title_id = t.id AND v.policy_key = ? AND "+
			liveVersion+" AND "+hasFileSQL+")")
		args = append(args, f.Without)
		if f.withoutKind != "" {
			conds = append(conds, "t.kind = ?")
			args = append(args, f.withoutKind)
		}
	}
	switch {
	case f.State == "filled" && f.plainState:
		conds = append(conds, "EXISTS (SELECT 1 FROM version v WHERE v.title_id = t.id AND "+liveVersion+
			" AND v.state IN ('filled', 'cutoff_met'))")
	case f.State != "":
		conds = append(conds, "EXISTS (SELECT 1 FROM version v WHERE v.title_id = t.id AND "+liveVersion+" AND v.state = ?)")
		args = append(args, f.State)
	}
	conds, args = f.TitleGridFilter.where(conds, args)
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func (w *Web) listTitles(ctx context.Context, f TitleFilter) (TitlePage, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	page := TitlePage{Limit: f.Limit, Offset: f.Offset, Items: []TitleRow{}}
	if f.Provider != "" {
		f.providerKeys = w.providerKeys(f.Provider)
	}
	f.withoutKind = w.libraryKind(f.Without)
	f.clock = newAirClock(w.now())
	db := w.st.Reader()
	if f.Upgrade != "" {
		found, err := w.upgradeFoundTitles(ctx, db)
		if err != nil {
			return page, err
		}
		if f.upgradeFound, err = idsJSON(found); err != nil {
			return page, err
		}
	}
	where, args := f.where()
	order, orderArgs := f.orderBy()
	if rank, rankArgs := titleTextRank(f.Q); rank != "" && (f.Sort == "" || f.Sort == "name") && f.Order == "" {
		// A name search in the default order (the lists send sort=name without an order): the best matches first,
		// then the usual name order. A chosen sort or order stays as chosen.
		order = " ORDER BY (" + rank + "), " + strings.TrimPrefix(order, " ORDER BY ")
		orderArgs = append(rankArgs, orderArgs...)
	}
	// Every matching id in order, in one pass: the total and the page come from it, so the filters' and the sort's
	// expressions run once per title.
	var ids []any
	if err := scanAll(ctx, db, `SELECT t.id`+titleFrom+where+order, append(args, orderArgs...), func(r *sql.Rows) error {
		var id int64
		if err := r.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
		return nil
	}); err != nil {
		return page, err
	}
	page.Total = len(ids)
	ids = ids[min(f.Offset, len(ids)):min(f.Offset+f.Limit, len(ids))]
	if len(ids) == 0 {
		return page, nil
	}
	items, err := w.titleRows(ctx, db, `SELECT `+titleCols+titleFrom+` WHERE t.id IN (`+store.Placeholders(len(ids))+`)`, ids)
	if err != nil {
		return page, err
	}
	at := map[int64]int{}
	for i, id := range ids {
		at[id.(int64)] = i
	}
	slices.SortFunc(items, func(a, b TitleRow) int { return at[a.ID] - at[b.ID] })
	if f.withTags {
		tags, err := titleTags(ctx, db, ids)
		if err != nil {
			return page, err
		}
		for i := range items {
			items[i].Tags = tags[items[i].ID]
		}
	}
	page.Items = items
	return page, nil
}

// libraryKind is a library's media kind (movie or series) from the settings; empty for a key not there.
func (w *Web) libraryKind(key string) string {
	if key == "" || w.pol == nil {
		return ""
	}
	if v := w.pol.Versions[key]; v != nil {
		return string(v.Kind)
	}
	return ""
}

// titleCols and titleFrom are the list row's columns and tables; m is the title's title_meta row, if any.
var titleCols = `t.id, t.kind, t.name, t.year, t.tmdb_id, t.tvdb_id, COALESCE(t.imdb_id, ''), t.anime, t.monitored,
	t.created_at, ` + store.SeriesTypeSQL("t")

const titleFrom = ` FROM title t LEFT JOIN title_meta m ON m.title_id = t.id`

// titleRows runs q (selecting titleCols) and fills each row's art, facts and versions, in q's order.
func (w *Web) titleRows(ctx context.Context, db *sql.DB, q string, args []any) ([]TitleRow, error) {
	items := []TitleRow{}
	err := scanAll(ctx, db, q, args, func(rows *sql.Rows) error {
		var r TitleRow
		var year, tmdb, tvdb, created sql.NullInt64
		if err := rows.Scan(&r.ID, &r.Kind, &r.Name, &year, &tmdb, &tvdb, &r.IMDbID, &r.Anime, &r.Monitored, &created,
			&r.SeriesType); err != nil {
			return err
		}
		r.Year, r.TMDbID, r.TVDbID, r.TitleAddedAt = nullInt(year), nullInt(tmdb), nullInt(tvdb), nullTS(created)
		r.Versions = []VersionBrief{}
		items = append(items, r)
		return nil
	})
	if err != nil || len(items) == 0 {
		return items, err
	}
	idx := map[int64]int{}
	var ids []any
	for i, r := range items {
		idx[r.ID] = i
		ids = append(ids, r.ID)
	}
	if err := w.fillArt(ctx, db, items); err != nil {
		return items, err
	}
	released, err := readReleased(ctx, db, ids)
	if err != nil {
		return items, err
	}
	for i := range items {
		items[i].Released = released[items[i].ID]
	}
	err = scanAll(ctx, db, `SELECT v.title_id, v.id, v.policy_key, v.state, v.monitored, `+hasFileSQL+`, `+
		availStateSQL(newAirClock(w.now()))+` FROM version v WHERE v.title_id IN (`+store.Placeholders(len(ids))+`) AND `+
		liveVersion+` ORDER BY v.policy_key`, ids, func(vrows *sql.Rows) error {
		var tid int64
		var v VersionBrief
		if err := vrows.Scan(&tid, &v.ID, &v.Key, &v.State, &v.Monitored, &v.HasFile, &v.availState); err != nil {
			return err
		}
		it := &items[idx[tid]]
		it.Versions = append(it.Versions, v)
		return nil
	})
	if err != nil {
		return items, err
	}
	vids := make([]any, 0, len(items))
	for _, it := range items {
		for _, v := range it.Versions {
			vids = append(vids, v.ID)
		}
	}
	vf, err := readVersionFiles(ctx, db, vids)
	if err != nil {
		return items, err
	}
	if err := w.fillVersionFiles(ctx, db, items, vf); err != nil {
		return items, err
	}
	return items, w.fillCopies(ctx, db, items, vf.held)
}

// tagOfLiveCopy joins a title tag (tt) to its label (g) and to the copy it was set on (v), which must be in the
// library: a removed copy's tags count nowhere.
const tagOfLiveCopy = `arrview_title_tag tt JOIN version v ON v.title_id = tt.title_id AND v.policy_key = tt.version
	JOIN arrview_tag g ON g.id = tt.tag_id`

// titleTags reads the tag labels of the titles' copies in the library, each once per title, in label order.
func titleTags(ctx context.Context, db *sql.DB, ids []any) (map[int64][]string, error) {
	out := map[int64][]string{}
	err := scanAll(ctx, db, `SELECT DISTINCT tt.title_id, g.label FROM `+tagOfLiveCopy+` WHERE tt.title_id IN (`+
		store.Placeholders(len(ids))+`) AND `+liveVersion+` ORDER BY tt.title_id, g.label`, ids, func(r *sql.Rows) error {
		var id int64
		var label string
		if err := r.Scan(&id, &label); err != nil {
			return err
		}
		out[id] = append(out[id], label)
		return nil
	})
	return out, err
}

func nullInt(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

// A file's torrent hash: the one resolution the title page's current file, the episode window's file, their Cached on
// (heldByHash) and the Torrent link use: link l's content file's content, else the hash
// it records (its download's, the file metadata's InfoHash, the adopted source's), with or without a content row;
// else the hash its mount folder points to (linkShownHashSQL).
const linkHashSQL = `COALESCE(` + linkShownHashSQL + `, '')`

// linkShownHashSQL is the hash the screens show for link l, lower case, or NULL: the one it records
// (router.LinkStoredHashSQL), else its folder hash (link.folder_hash, migration 00145: the one torrent of the record
// its mount folder names, worked out by the torrents manager). The folder hash is an inference, so it is for display
// and list filtering only: removal, the router and the torrents usage read router.LinkStoredHashSQL.
const linkShownHashSQL = `COALESCE(` + router.LinkStoredHashSQL + `, NULLIF(l.folder_hash, ''))`

// Link is a library entry.
type Link struct {
	ID           int64  `json:"id"`
	Path         string `json:"path"`
	Target       string `json:"target"`
	Kind         string `json:"kind"`
	State        string `json:"state"`
	ReleaseTitle string `json:"release_title,omitempty"`
	Quality      string `json:"quality,omitempty"`
	Score        *int64 `json:"score,omitempty"`
	TitleID      int64  `json:"title_id"`
	InfoHash     string `json:"info_hash,omitempty"`
	// SizeBytes is the linked file's size where it is known (versions only; left out without "see files").
	SizeBytes *int64 `json:"size_bytes,omitempty"`
	// ReplaceScope: Replace file for the link (a movie's title page, managers who see files; linkreplace.go).
	ReplaceScope *ReplaceScope `json:"replace_scope,omitempty"`
}

// Version is a version with its files.
type Version struct {
	ID        int64     `json:"id"`
	TitleID   int64     `json:"title_id"`
	TitleName string    `json:"title_name,omitempty"`
	Key       string    `json:"key"`
	State     string    `json:"state"`
	Monitored bool      `json:"monitored"`
	HasFile   bool      `json:"has_file"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Links     []Link    `json:"links"`
	// The title page's additions (titlepage.go); empty on other routes.
	Label     string       `json:"label,omitempty"`
	Current   *CurrentFile `json:"current,omitempty"`
	Providers []string     `json:"providers,omitempty"`
	// CachedOn is Providers as cache codes (rd, ad, tb; cacheCodes), the codes list rows and releases carry.
	CachedOn []string `json:"cached_on,omitempty"`
	// AddTo is, for a movie's current file with a torrent hash, the cache codes of the providers with a key that do
	// not hold it complete ("Add to other providers"; copyTargets).
	AddTo    []string       `json:"add_to,omitempty"`
	Episodes *EpisodeCounts `json:"episodes,omitempty"`
	History  []HistoryEvent `json:"history,omitempty"`
	// Upgrade explains the state against the cutoff; Control says what the title page's controls may do (manage.go).
	Upgrade *UpgradeInfo    `json:"upgrade,omitempty"`
	Control *VersionControl `json:"control,omitempty"`
	// Acquisition is the download in hand for the version (copies.go), for callers who see files.
	Acquisition *Acquisition `json:"acquisition,omitempty"`
	// MinimumAvailability is the minimum availability in force for a movie in this library (availability.Rules:
	// the explicit choice, else the imported value, else the library's default, else released); the title route
	// only, empty for a series.
	MinimumAvailability string `json:"minimum_availability,omitempty"`
	// AnimeSkip (title page, series): a monitored copy in a library searches run for (catalog.Searchable) that
	// acquisition does not search, because the library's anime rule does not fit the series (its anime flag, or its
	// type anime; acquire's targets).
	AnimeSkip bool `json:"anime_skip,omitempty"`
	// LastSearch and Searching (title page, see files): the library's last targeted search (absent: never) and a
	// search under way, "queued" or "searching" (searchFacts).
	LastSearch *LastSearch `json:"last_search,omitempty"`
	Searching  string      `json:"searching,omitempty"`
	// RSS (title page, see files): the RSS read picks up new releases for this library (readsRSS).
	RSS bool `json:"rss,omitempty"`
	// Removed (title page): the version was removed from the library (arrview_removed_version); its rows and links
	// stay. It counts in none of the title's rollups (availability, size, added).
	Removed bool `json:"removed,omitempty"`
	// availState is the state availability reads (availStateSQL; enrichTitle).
	availState string
}

// ExternalID is a further id of a title.
type ExternalID struct {
	Source string `json:"source"`
	ID     string `json:"id"`
}

// ReleaseDate is a title's release date from one source.
type ReleaseDate struct {
	Source  string `json:"source"`
	Country string `json:"country,omitempty"`
	Type    string `json:"type"`
	Date    string `json:"date"`
}

// TitleDetail is a title with everything the title page shows.
type TitleDetail struct {
	TitleRow
	SortName     string        `json:"sort_name"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
	ExternalIDs  []ExternalID  `json:"external_ids"`
	ReleaseDates []ReleaseDate `json:"release_dates"`
	Versions     []Version     `json:"versions"`
	// Meta is the title's TMDb metadata; null before the meta.art job first tried the title.
	Meta *TitleMeta `json:"meta"`
	// Franchises are the anime franchises that map into the title (titlepage.go).
	Franchises []FranchiseRef `json:"franchises,omitempty"`
	// SeriesType is a series' type in force (store.SeriesType: standard, daily or anime); empty for a movie.
	SeriesType string `json:"series_type,omitempty"`
	// AutoSearch says whether Theatarr searches by itself (titlepage.go); for callers who see files.
	AutoSearch *AutoSearch `json:"auto_search,omitempty"`
	// Collection is the stored TMDb collection the movie is part of (its page is /collections/{tmdb_id}); for
	// managers, absent when it is not stored.
	Collection *CollectionRef `json:"collection,omitempty"`
}

// CollectionRef names a collection on the title page.
type CollectionRef struct {
	TmdbID int64  `json:"tmdb_id"`
	Name   string `json:"name"`
}

func (w *Web) getTitle(ctx context.Context, id int64) (*TitleDetail, error) {
	db := w.st.Reader()
	d := &TitleDetail{ExternalIDs: []ExternalID{}, ReleaseDates: []ReleaseDate{}, Versions: []Version{}}
	var year, tmdb, tvdb sql.NullInt64
	var created, updated int64
	err := db.QueryRowContext(ctx, `SELECT id, kind, name, sort_name, year, tmdb_id, tvdb_id, COALESCE(imdb_id, ''),
		anime, monitored, created_at, updated_at FROM title WHERE id = ?`, id).Scan(&d.ID, &d.Kind, &d.Name, &d.SortName,
		&year, &tmdb, &tvdb, &d.IMDbID, &d.Anime, &d.Monitored, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	d.Year, d.TMDbID, d.TVDbID = nullInt(year), nullInt(tmdb), nullInt(tvdb)
	d.CreatedAt, d.UpdatedAt = ts(created), ts(updated)
	d.TitleRow.Versions = nil
	if d.TitleArt, d.TitleFacts, d.Meta, err = w.titleMeta(ctx, db, id); err != nil {
		return nil, err
	}
	released, err := readReleased(ctx, db, []any{id})
	if err != nil {
		return nil, err
	}
	d.Released = released[id]

	if err := scanAll(ctx, db, `SELECT source, external_id FROM title_external_id WHERE title_id = ? ORDER BY source, external_id`,
		[]any{id}, func(r *sql.Rows) error {
			var e ExternalID
			if err := r.Scan(&e.Source, &e.ID); err != nil {
				return err
			}
			d.ExternalIDs = append(d.ExternalIDs, e)
			return nil
		}); err != nil {
		return nil, err
	}
	// The rows behind the release-date choice (activity.FirstReleasesSQL): a refreshed title's primary release (its
	// cinema date, without one there, the canonical one), else the stored rows (the arrs').
	if err := scanAll(ctx, db, `SELECT source, country, type, date FROM title_release_date d WHERE title_id = ?
		AND CASE WHEN EXISTS (SELECT 1 FROM title_meta m WHERE m.title_id = d.title_id AND m.movie_dates_at IS NOT NULL)
		THEN (d.source = 'tmdb' AND d.country <> '') OR (d.source = 'tmdb-art' AND d.type = 'theatrical' AND NOT EXISTS
			(SELECT 1 FROM title_release_date p WHERE p.title_id = d.title_id AND p.source = 'tmdb' AND p.country <> ''
			AND p.type IN ('theatrical', 'theatrical_limited'))) ELSE 1 END
		ORDER BY date, type`, []any{id}, func(r *sql.Rows) error {
		var e ReleaseDate
		if err := r.Scan(&e.Source, &e.Country, &e.Type, &e.Date); err != nil {
			return err
		}
		d.ReleaseDates = append(d.ReleaseDates, e)
		return nil
	}); err != nil {
		return nil, err
	}
	if d.Versions, err = w.versions(ctx, "v.title_id = ?", []any{id}, 0, 0); err != nil {
		return nil, err
	}
	if len(d.Versions) > 0 {
		var removed []int64
		if err := scanAll(ctx, db, `SELECT r.version_id FROM arrview_removed_version r JOIN version v
			ON v.id = r.version_id WHERE v.title_id = ?`, []any{id}, func(r *sql.Rows) error {
			var vid int64
			if err := r.Scan(&vid); err != nil {
				return err
			}
			removed = append(removed, vid)
			return nil
		}); err != nil {
			return nil, err
		}
		for i := range d.Versions {
			d.Versions[i].Removed = slices.Contains(removed, d.Versions[i].ID)
		}
	}
	switch {
	case d.Kind == "series":
		if d.SeriesType, err = store.SeriesType(ctx, db, id); err != nil {
			return nil, err
		}
		if w.pol != nil && len(d.Versions) > 0 {
			anime := d.Anime || d.SeriesType == "anime"
			for i := range d.Versions {
				v := &d.Versions[i]
				vp := w.pol.Versions[v.Key]
				// Only a library searches run for: search now already says why another one is left out.
				active, _ := catalog.Searchable(w.pol, []string{v.Key})
				v.AnimeSkip = vp != nil && len(active) > 0 && v.Monitored && !v.Removed &&
					!catalog.Fits(vp, policy.KindSeries, anime)
			}
		}
	case w.pol != nil && len(d.Versions) > 0:
		rules, err := availability.Rules(ctx, db, w.pol, []int64{id})
		if err != nil {
			return nil, err
		}
		for i := range d.Versions {
			d.Versions[i].MinimumAvailability = rules[d.Versions[i].ID].Minimum
		}
	}
	return d, nil
}

// versions loads versions matching cond with their links. limit 0 means all.
func (w *Web) versions(ctx context.Context, cond string, args []any, limit, offset int) ([]Version, error) {
	db := w.st.Reader()
	q := `SELECT v.id, v.title_id, t.name, v.policy_key, v.state, v.monitored, ` + hasFileSQL + `,
		v.created_at, v.updated_at FROM version v JOIN title t ON t.id = v.title_id WHERE ` + cond + ` ORDER BY v.title_id, v.policy_key`
	if limit > 0 {
		q += " LIMIT ? OFFSET ?"
		args = append(args, limit, offset)
	}
	out := []Version{}
	idx := map[int64]int{}
	var ids []any
	err := scanAll(ctx, db, q, args, func(r *sql.Rows) error {
		var v Version
		var c, u int64
		if err := r.Scan(&v.ID, &v.TitleID, &v.TitleName, &v.Key, &v.State, &v.Monitored, &v.HasFile, &c, &u); err != nil {
			return err
		}
		v.CreatedAt, v.UpdatedAt = ts(c), ts(u)
		v.Links = []Link{}
		idx[v.ID] = len(out)
		ids = append(ids, v.ID)
		out = append(out, v)
		return nil
	})
	if err != nil || len(ids) == 0 {
		return out, err
	}
	in := store.Placeholders(len(ids))
	err = scanAll(ctx, db, `SELECT lv.version_id, l.id, l.path, l.target, l.kind, l.state, COALESCE(l.release_title, ''),
		COALESCE(l.quality, ''), `+linkScoreSQL+`, l.title_id, `+linkHashSQL+`, `+linkSizeSQL+`
		FROM link_version lv JOIN link l ON l.id = lv.link_id
		LEFT JOIN content_file cf ON cf.id = l.content_file_id
		WHERE lv.version_id IN (`+in+`) ORDER BY l.path`, ids, func(r *sql.Rows) error {
		var vid int64
		var l Link
		var score, size sql.NullInt64
		if err := r.Scan(&vid, &l.ID, &l.Path, &l.Target, &l.Kind, &l.State, &l.ReleaseTitle, &l.Quality, &score,
			&l.TitleID, &l.InfoHash, &size); err != nil {
			return err
		}
		l.Score, l.SizeBytes = nullInt(score), nullInt(size)
		v := &out[idx[vid]]
		v.Links = append(v.Links, l)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// VersionFilter selects versions.
type VersionFilter struct {
	Key   string
	State string
	// plainState: filled also takes cutoff_met (a caller without "see files").
	plainState bool
	HasFile    *bool
	Limit      int
	Offset     int
}

func (w *Web) listVersions(ctx context.Context, f VersionFilter) ([]Version, error) {
	// The library's copies only: one removed from its library is not listed (liveVersion).
	conds := []string{liveVersion}
	var args []any
	if f.Key != "" {
		conds = append(conds, "v.policy_key = ?")
		args = append(args, f.Key)
	}
	switch {
	case f.State == "filled" && f.plainState:
		conds = append(conds, "v.state IN ('filled', 'cutoff_met')")
	case f.State != "":
		conds = append(conds, "v.state = ?")
		args = append(args, f.State)
	}
	if f.HasFile != nil {
		if *f.HasFile {
			conds = append(conds, hasFileSQL)
		} else {
			conds = append(conds, "NOT "+hasFileSQL)
		}
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	return w.versions(ctx, strings.Join(conds, " AND "), args, f.Limit, max(f.Offset, 0))
}

// Job is a queue entry. The payload is left out: handlers may put anything there. For a failed job, resolved_by,
// retried_as, dismissed_at and attention tell what became of it (internal/jobs manage.go).
type Job struct {
	ID          int64      `json:"id"`
	Kind        string     `json:"kind"`
	State       string     `json:"state"`
	Priority    int        `json:"priority"`
	Attempts    int        `json:"attempts"`
	MaxAttempts int        `json:"max_attempts"`
	RunAt       time.Time  `json:"run_at"`
	LastError   string     `json:"last_error,omitempty"`
	DedupeKey   string     `json:"dedupe_key,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	RetryOf     int64      `json:"retry_of,omitempty"`
	RetriedAs   int64      `json:"retried_as,omitempty"`
	ResolvedBy  int64      `json:"resolved_by,omitempty"`
	// ResolvedByTask: the scheduled task of the job's kind worked after it failed.
	ResolvedByTask bool       `json:"resolved_by_task,omitempty"`
	DismissedAt    *time.Time `json:"dismissed_at,omitempty"`
	DismissedBy    string     `json:"dismissed_by,omitempty"`
	Attention      bool       `json:"attention,omitempty"`
}

func jobFromStatus(s jobs.Status) Job {
	return Job{ID: s.ID, Kind: s.Kind, State: s.State, Priority: s.Priority, Attempts: s.Attempts,
		MaxAttempts: s.MaxAttempts, RunAt: s.RunAt, LastError: s.LastError, DedupeKey: s.DedupeKey,
		CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, FinishedAt: s.FinishedAt, StartedAt: s.StartedAt, RetryOf: s.RetryOf,
		RetriedAs: s.RetriedAs, ResolvedBy: s.ResolvedBy, ResolvedByTask: s.ResolvedByTask, DismissedAt: s.DismissedAt, DismissedBy: s.DismissedBy,
		Attention: s.Attention}
}

// JobList is the job queue view. Counts has one entry per state plus attention: the failed jobs not yet resolved,
// retried or dismissed.
type JobList struct {
	Counts map[string]int `json:"counts"`
	Items  []Job          `json:"items"`
}

func (w *Web) listJobsFilter(ctx context.Context, f jobs.ListFilter) (JobList, error) {
	out := JobList{Items: []Job{}}
	var err error
	if out.Counts, err = w.jobCounts(ctx); err != nil {
		return out, err
	}
	items, err := w.jobs.List(ctx, f)
	if err != nil {
		return out, err
	}
	for _, s := range items {
		out.Items = append(out.Items, jobFromStatus(s))
	}
	return out, nil
}

// jobCounts is shared by the jobs page and the event stream, which never needs a sorted job-list row.
func (w *Web) jobCounts(ctx context.Context) (map[string]int, error) {
	counts := map[string]int{"queued": 0, "running": 0, "done": 0, "failed": 0, "attention": 0}
	err := scanAll(ctx, w.st.Reader(), `SELECT state, COUNT(*) FROM job GROUP BY state`, nil, func(r *sql.Rows) error {
		var s string
		var n int
		if err := r.Scan(&s, &n); err != nil {
			return err
		}
		counts[s] = n
		return nil
	})
	if err != nil {
		return counts, err
	}
	if counts["attention"], err = w.jobs.AttentionCount(ctx); err != nil || w.tasks == nil {
		return counts, err
	}
	// A scheduled task whose newest run failed needs attention too, until a run works.
	counts["attention"] += w.tasks.Failed()
	return counts, nil
}

func scanJob(r *sql.Rows) (Job, error) {
	var j Job
	var run, c, u int64
	var fin sql.NullInt64
	err := r.Scan(&j.ID, &j.Kind, &j.State, &j.Priority, &j.Attempts, &j.MaxAttempts, &run, &j.LastError, &j.DedupeKey,
		&c, &u, &fin)
	j.RunAt, j.CreatedAt, j.UpdatedAt, j.FinishedAt = ts(run), ts(c), ts(u), nullTS(fin)
	return j, err
}

// jobsChangedSince returns jobs updated after ms (at most 50) and the newest updated_at seen.
func (w *Web) jobsChangedSince(ctx context.Context, ms int64) ([]Job, int64, error) {
	var out []Job
	newest := ms
	err := scanAll(ctx, w.st.Reader(), `SELECT id, kind, state, priority, attempts, max_attempts, run_at,
		COALESCE(last_error, ''), COALESCE(dedupe_key, ''), created_at, updated_at, finished_at FROM job
		WHERE updated_at > ? ORDER BY updated_at LIMIT 50`, []any{ms}, func(r *sql.Rows) error {
		j, err := scanJob(r)
		if err != nil {
			return err
		}
		newest = max(newest, store.Millis(j.UpdatedAt))
		out = append(out, j)
		return nil
	})
	return out, newest, err
}

// Backoff is a gateway back-off as persisted.
type Backoff struct {
	Subject   string     `json:"subject"`
	Until     *time.Time `json:"until,omitempty"`
	Active    bool       `json:"active"`
	Failures  int        `json:"failures"`
	Reason    string     `json:"reason"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// BudgetUse is a budget's configuration and current use.
type BudgetUse struct {
	Key          string   `json:"key"`
	Requests     int      `json:"requests"`
	Per          string   `json:"per"`
	Burst        int      `json:"burst"`
	Hourly       int      `json:"hourly"`
	Daily        int      `json:"daily"`
	UsedHour     int      `json:"used_hour"`
	UsedDay      int      `json:"used_day"`
	UsedBy       []string `json:"used_by"`
	Backoff      *Backoff `json:"backoff,omitempty"`
	HourExceeded bool     `json:"hour_capped"`
	DayExceeded  bool     `json:"day_capped"`
}

// GatewayState is the egress and budget picture.
type GatewayState struct {
	Egress   map[string]any `json:"egress"`
	Budgets  []BudgetUse    `json:"budgets"`
	Backoffs []Backoff      `json:"backoffs"`
}

// gatewayState is the gateway's in-memory snapshot (gateway.State): egress, budgets and back-offs. Without a
// gateway (tests) it is empty. ctx and the error are kept for the callers' sake.
func (w *Web) gatewayState(ctx context.Context) (GatewayState, error) {
	gs := GatewayState{Egress: map[string]any{"vpn_ok": false, "paths": []any{}}, Budgets: []BudgetUse{}, Backoffs: []Backoff{}}
	if w.egress == nil {
		return gs, nil
	}
	s := w.gatewaySnapshot(ctx)
	gs.Egress = s.Egress
	conv := func(b gateway.BackoffState) Backoff {
		o := Backoff{Subject: b.Subject, Active: b.Active, Failures: b.Failures, Reason: b.Reason, UpdatedAt: b.UpdatedAt}
		if !b.Until.IsZero() {
			u := b.Until
			o.Until = &u
		}
		return o
	}
	for _, b := range s.Backoffs {
		gs.Backoffs = append(gs.Backoffs, conv(b))
	}
	for _, b := range s.Budgets {
		bu := BudgetUse{Key: b.Key, Requests: b.Requests, Per: b.Per.String(), Burst: b.Burst, Hourly: b.Hourly,
			Daily: b.Daily, UsedHour: b.UsedHour, UsedDay: b.UsedDay, UsedBy: b.UsedBy, HourExceeded: b.HourCapped,
			DayExceeded: b.DayCapped}
		if bu.UsedBy == nil {
			bu.UsedBy = []string{}
		}
		if b.Backoff != nil {
			bo := conv(*b.Backoff)
			bu.Backoff = &bo
		}
		gs.Budgets = append(gs.Budgets, bu)
	}
	return gs, nil
}

// gatewaySnapshot is the gateway's snapshot for the generation pinned to ctx; w.egress must be set.
func (w *Web) gatewaySnapshot(ctx context.Context) gateway.State {
	if source, ok := w.egress.(interface {
		StateFor(context.Context) gateway.State
	}); ok {
		return source.StateFor(ctx)
	}
	return w.egress.State()
}

// IndexerRequests is an indexer's requests by purpose (docs/design/gateway.md): today (UTC) and the last 7 UTC days, today
// included. Purposes lists those with a request in the 7 days, in display order.
type IndexerRequests struct {
	Today    int               `json:"today"`
	Week     int               `json:"week"`
	Purposes []PurposeRequests `json:"purposes"`
}

// PurposeRequests is one purpose's share of IndexerRequests.
type PurposeRequests struct {
	Purpose string `json:"purpose"`
	Today   int    `json:"today"`
	Week    int    `json:"week"`
}

func indexerRequests(r gateway.IndexerRequests) *IndexerRequests {
	out := &IndexerRequests{Purposes: []PurposeRequests{}}
	for _, p := range gateway.Purposes {
		if r.Week[p] == 0 {
			continue
		}
		out.Purposes = append(out.Purposes, PurposeRequests{Purpose: string(p), Today: r.Today[p], Week: r.Week[p]})
		out.Today += r.Today[p]
		out.Week += r.Week[p]
	}
	return out
}

// Status is a one-call overview.
type Status struct {
	Versions []VersionGrabSummary `json:"versions"`
	Version  string               `json:"version"`
	Schema   map[string]any       `json:"schema"`
	Counts   map[string]int       `json:"counts"`
	// PolicyRevision is the running policy's revision (policy.Policy.Revision), which journal entries name.
	PolicyRevision string `json:"policy_revision"`
	// Health is the worst health check type (ok, notice, warning, error); "" without health checks.
	Health string `json:"health,omitempty"`
}

func (w *Web) status(ctx context.Context) (Status, error) {
	s := Status{Versions: versionGrabSummaries(w.pol), Version: w.version, Counts: map[string]int{}, PolicyRevision: w.pol.Revision}
	if w.health != nil {
		s.Health = health.Worst(w.health.Results(ctx))
	}
	cur, target, err := w.st.SchemaVersion(ctx)
	s.Schema = map[string]any{"current": cur, "target": target}
	if err != nil {
		return s, err
	}
	for _, t := range []string{"title", "version", "content", "link", "job"} {
		var n int
		q := "SELECT COUNT(*) FROM " + t
		if t == "title" {
			// The titles in the library (Home's count): a removed title is not one.
			q += " t WHERE " + inLibrary
		}
		if err := w.st.Reader().QueryRowContext(ctx, q).Scan(&n); err != nil {
			return s, fmt.Errorf("count %s: %w", t, err)
		}
		s.Counts[t] = n
	}
	return s, nil
}

func scanAll(ctx context.Context, db *sql.DB, q string, args []any, fn func(*sql.Rows) error) error {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
