// Package lookup implements the metadata lookups the indexer service gives its sources (indexer.Metadata): AniList
// ids for SeaDex and episode counts for Torrentio, from our own database first, then from metadata clients. They
// do what a bridge would do on its own: seadexerr maps TVDB seasons and TMDb movies to AniList through
// anibridge, and Torrentio pack sizes need episode counts from TMDb.
package lookup

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/animemap"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/metadata/tvdb"
)

// DB answers from our database: the franchise mappings (anime_mapping, anime_entry), AniList ids recorded for a
// title (title_external_id), and the episodes we hold (episode).
type DB struct {
	DB *sql.DB
}

// TorrentNames uses provider inventory, never a seeded arr/search release title that may carry synthetic quality.
func (d DB) TorrentNames(ctx context.Context, hash string) ([]string, error) {
	hash = indexer.NormalizeHash(hash)
	if hash == "" {
		return nil, nil
	}
	rows, err := d.DB.QueryContext(ctx, `SELECT DISTINCT p.folder_name FROM content_provider p
		JOIN content c ON c.id=p.content_id WHERE c.info_hash=? AND p.state='present' AND p.folder_name<>'' AND (p.progress>=100 OR p.links>0)
		ORDER BY p.folder_name`, hash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// AniListIDs maps a series season through the franchise entries that map episodes into it, and a movie through
// the entries that list its TMDb id; else the title's own recorded AniList id (a series only when it has one).
func (d DB) AniListIDs(ctx context.Context, q indexer.Query) ([]int, error) {
	switch q.Type {
	case indexer.QuerySeries:
		tvdbID := q.TVDB
		if tvdbID == 0 {
			return nil, nil
		}
		am, err := animemap.Load(ctx, d.DB, tvdbID)
		if err != nil {
			return nil, err
		}
		if ids := SeasonAniList(am, q.Season); len(ids) > 0 {
			return ids, nil
		}
		ids, err := d.external(ctx, `SELECT x.external_id FROM title_external_id x JOIN title t ON t.id = x.title_id
			WHERE x.source = 'anilist' AND t.kind = 'series' AND t.tvdb_id = ?`, tvdbID)
		if err != nil || len(ids) != 1 {
			return nil, err
		}
		return ids, nil
	case indexer.QueryMovie:
		tmdbID := q.TMDb
		if tmdbID == 0 && q.IMDb != "" {
			err := d.DB.QueryRowContext(ctx, `SELECT tmdb_id FROM title WHERE kind = 'movie' AND imdb_id = ? AND tmdb_id IS NOT NULL`,
				q.IMDb).Scan(&tmdbID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
		}
		if tmdbID == 0 {
			return nil, nil
		}
		ids, err := d.external(ctx, `SELECT DISTINCT e.anilist_id FROM anime_entry e, json_each(e.tmdb_movie) j
			WHERE j.value = ? AND e.anilist_id IS NOT NULL ORDER BY e.anilist_id`, tmdbID)
		if err != nil || len(ids) > 0 {
			return ids, err
		}
		return d.external(ctx, `SELECT x.external_id FROM title_external_id x JOIN title t ON t.id = x.title_id
			WHERE x.source = 'anilist' AND t.kind = 'movie' AND t.tmdb_id = ?`, tmdbID)
	}
	return nil, nil
}

func (d DB) external(ctx context.Context, query string, args ...any) ([]int, error) {
	rows, err := d.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("lookup: %w", err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		if id, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && id > 0 && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

// AniListTargets maps an AniList id back to what it is filed under, the reverse of AniListIDs: the TVDB seasons the
// franchise mappings put its episodes in (regular seasons, the same mappings AniListIDs reads; marked Exact, as they
// cover only the mapped episodes), the movies its franchise entries list, and the titles that recorded it as their
// own id (a series as its season 1, a whole-season target).
func (d DB) AniListTargets(ctx context.Context, anilist int) ([]indexer.AniListTarget, error) {
	if anilist <= 0 {
		return nil, nil
	}
	rows, err := d.DB.QueryContext(ctx, `SELECT m.tvdb_series, m.episodes FROM anime_mapping m
		JOIN anime_entry e ON e.franchise = m.franchise AND e.key = m.entry_key
		WHERE e.anilist_id = ? AND m.tvdb_series IS NOT NULL ORDER BY m.franchise, m.entry_key`, anilist)
	if err != nil {
		return nil, fmt.Errorf("lookup: %w", err)
	}
	var out []indexer.AniListTarget
	add := func(t indexer.AniListTarget) {
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	for rows.Next() {
		var tvdbID int
		var eps string
		if err := rows.Scan(&tvdbID, &eps); err != nil {
			rows.Close()
			return nil, err
		}
		var ses []anime.SE
		if json.Unmarshal([]byte(eps), &ses) != nil {
			continue
		}
		for _, se := range ses {
			if se[0] > 0 {
				// A summary of exact episodes, not a whole-season mapping.
				add(indexer.AniListTarget{TVDB: tvdbID, Season: se[0], Exact: true})
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	movies, err := d.external(ctx, `SELECT DISTINCT j.value FROM anime_entry e, json_each(e.tmdb_movie) j
		WHERE e.anilist_id = ? ORDER BY j.value`, anilist)
	if err != nil {
		return nil, err
	}
	for _, m := range movies {
		add(indexer.AniListTarget{TMDb: m})
	}
	if len(out) > 0 {
		return out, nil
	}
	own, err := d.DB.QueryContext(ctx, `SELECT t.kind, COALESCE(t.tvdb_id, 0), COALESCE(t.tmdb_id, 0)
		FROM title_external_id x JOIN title t ON t.id = x.title_id
		WHERE x.source = 'anilist' AND x.external_id = ? ORDER BY t.id`, strconv.Itoa(anilist))
	if err != nil {
		return nil, fmt.Errorf("lookup: %w", err)
	}
	defer own.Close()
	for own.Next() {
		var kind string
		var tvdbID, tmdbID int
		if err := own.Scan(&kind, &tvdbID, &tmdbID); err != nil {
			return nil, err
		}
		switch {
		case kind == "series" && tvdbID > 0:
			add(indexer.AniListTarget{TVDB: tvdbID, Season: 1})
		case kind == "movie" && tmdbID > 0:
			add(indexer.AniListTarget{TMDb: tmdbID})
		}
	}
	return out, own.Err()
}

// TitleName names one of our titles: a series by TVDB id, a movie by TMDb id (else IMDb id). seadexerr takes the
// name from Sonarr or Radarr; ours is the arr's name an arr import stored, or the one the title was added with.
func (d DB) TitleName(ctx context.Context, q indexer.Query) (string, int, bool, error) {
	var where string
	var args []any
	switch {
	case q.Type == indexer.QuerySeries && q.TVDB > 0:
		where, args = "kind = 'series' AND tvdb_id = ?", []any{q.TVDB}
	case q.Type == indexer.QueryMovie && q.TMDb > 0:
		where, args = "kind = 'movie' AND tmdb_id = ?", []any{q.TMDb}
	case q.Type == indexer.QueryMovie && q.IMDb != "":
		where, args = "kind = 'movie' AND imdb_id = ?", []any{q.IMDb}
	default:
		return "", 0, false, nil
	}
	var name string
	var year int
	err := d.DB.QueryRowContext(ctx, `SELECT name, COALESCE(year, 0) FROM title WHERE `+where+` ORDER BY id LIMIT 1`,
		args...).Scan(&name, &year)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, fmt.Errorf("lookup: %w", err)
	}
	return name, year, name != "", nil
}

// EpisodeCounts counts the episodes we hold per season for the series (by TVDB id, else IMDb id).
func (d DB) EpisodeCounts(ctx context.Context, q indexer.Query) (map[int]int, error) {
	if q.Type != indexer.QuerySeries {
		return nil, nil
	}
	var where string
	var arg any
	switch {
	case q.TVDB > 0:
		where, arg = "t.tvdb_id = ?", q.TVDB
	case q.IMDb != "":
		where, arg = "t.imdb_id = ?", q.IMDb
	default:
		return nil, nil
	}
	rows, err := d.DB.QueryContext(ctx, `SELECT e.season, COUNT(*) FROM episode e JOIN title t ON t.id = e.title_id
		WHERE t.kind = 'series' AND `+where+` GROUP BY e.season`, arg)
	if err != nil {
		return nil, fmt.Errorf("lookup: %w", err)
	}
	defer rows.Close()
	out := map[int]int{}
	for rows.Next() {
		var s, n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, rows.Err()
}

// SeasonAniList lists the AniList ids whose franchise entry maps episodes into a TVDB season, in entry order.
func SeasonAniList(am *animemap.Series, season int) []int {
	if am == nil || season <= 0 {
		return nil
	}
	var out []int
	for _, e := range am.Entries {
		id, ok := anilistKey(e.Key)
		if !ok || slices.Contains(out, id) {
			continue
		}
		for _, se := range e.Episodes {
			if se[0] == season {
				out = append(out, id)
				break
			}
		}
	}
	return out
}

// anilistKey reads an entry key "al:<id>" or "al:<id>#<n>".
func anilistKey(k string) (int, bool) {
	rest, ok := strings.CutPrefix(k, "al:")
	if !ok {
		return 0, false
	}
	rest, _, _ = strings.Cut(rest, "#")
	id, err := strconv.Atoi(rest)
	return id, err == nil && id > 0
}

// Anibridge is the anibridge-mappings list indexed the other way round: TVDB season and TMDb movie to AniList ids,
// as seadexerr's mapping.rs indexes it (every AniList entry mapped into the season, not only the first).
type Anibridge struct {
	seasons map[[2]int][]int // {tvdb, season}
	movies  map[int][]int
	// targets is the other way: AniList id to the seasons and movies it is filed under (seadexerr's
	// anilist_to_entries and anilist_to_tmdb).
	targets map[int][]indexer.AniListTarget
}

// NewAnibridge indexes the parsed list (anime.Lists.Anibridge).
func NewAnibridge(l *anime.Lists) *Anibridge {
	a := &Anibridge{seasons: map[[2]int][]int{}, movies: map[int][]int{}, targets: map[int][]indexer.AniListTarget{}}
	addTarget := func(al int, t indexer.AniListTarget) {
		if !slices.Contains(a.targets[al], t) {
			a.targets[al] = append(a.targets[al], t)
		}
	}
	if l == nil {
		return a
	}
	for key, targets := range l.Anibridge {
		al, err := strconv.Atoi(strings.TrimPrefix(key, "anilist:"))
		if err != nil || al <= 0 {
			continue
		}
		for target := range targets {
			parts := strings.Split(target, ":")
			switch {
			case len(parts) >= 2 && parts[0] == "tmdb_movie":
				if id, err := strconv.Atoi(parts[1]); err == nil && id > 0 && !slices.Contains(a.movies[id], al) {
					a.movies[id] = append(a.movies[id], al)
					addTarget(al, indexer.AniListTarget{TMDb: id})
				}
			case len(parts) == 3 && parts[0] == "tvdb_show" && strings.HasPrefix(parts[2], "s"):
				id, err1 := strconv.Atoi(parts[1])
				s, err2 := strconv.Atoi(parts[2][1:])
				k := [2]int{id, s}
				if err1 == nil && err2 == nil && id > 0 && !slices.Contains(a.seasons[k], al) {
					a.seasons[k] = append(a.seasons[k], al)
					addTarget(al, indexer.AniListTarget{TVDB: id, Season: s})
				}
			}
		}
	}
	for k := range a.seasons {
		slices.Sort(a.seasons[k])
	}
	for k := range a.movies {
		slices.Sort(a.movies[k])
	}
	for k := range a.targets {
		slices.SortFunc(a.targets[k], func(x, y indexer.AniListTarget) int {
			if c := cmp.Compare(x.Season, y.Season); c != 0 {
				return c
			}
			if c := cmp.Compare(x.TVDB, y.TVDB); c != 0 {
				return c
			}
			return cmp.Compare(x.TMDb, y.TMDb)
		})
	}
	return a
}

// AniListTargets answers what an AniList id is filed under: its TVDB seasons and TMDb movies.
func (a *Anibridge) AniListTargets(_ context.Context, anilist int) ([]indexer.AniListTarget, error) {
	return slices.Clone(a.targets[anilist]), nil
}

// AniListIDs answers series by TVDB id and season (specials excluded, as seadexerr does) and movies by TMDb id.
func (a *Anibridge) AniListIDs(_ context.Context, q indexer.Query) ([]int, error) {
	switch q.Type {
	case indexer.QuerySeries:
		if q.TVDB > 0 && q.Season > 0 {
			return slices.Clone(a.seasons[[2]int{q.TVDB, q.Season}]), nil
		}
	case indexer.QueryMovie:
		if q.TMDb > 0 {
			return slices.Clone(a.movies[q.TMDb]), nil
		}
	}
	return nil, nil
}

// AniListTargets answers from the current index, loading it when due.
func (d *Daily) AniListTargets(ctx context.Context, anilist int) ([]indexer.AniListTarget, error) {
	idx, err := d.index(ctx)
	if idx == nil {
		return nil, err
	}
	return idx.AniListTargets(ctx, anilist)
}

// Daily loads an index on first use and again once it is a day old (anibridge publishes daily; seadexerr refreshes
// every 6 h, and our list fetcher keeps it a day). A failed reload keeps the old index.
type Daily struct {
	Load  func(ctx context.Context) (*Anibridge, error)
	Every time.Duration // default 24h
	Now   func() time.Time

	mu     sync.Mutex
	cur    *Anibridge
	at     time.Time
	failAt time.Time
}

// AniListIDs answers from the current index, loading it when due.
func (d *Daily) AniListIDs(ctx context.Context, q indexer.Query) ([]int, error) {
	idx, err := d.index(ctx)
	if idx == nil {
		return nil, err
	}
	return idx.AniListIDs(ctx, q)
}

func (d *Daily) index(ctx context.Context) (*Anibridge, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	every := d.Every
	if every <= 0 {
		every = 24 * time.Hour
	}
	t := now()
	if d.cur != nil && t.Sub(d.at) < every || t.Sub(d.failAt) < 10*time.Minute {
		return d.cur, nil
	}
	idx, err := d.Load(ctx)
	if err != nil {
		d.failAt = t
		return d.cur, fmt.Errorf("anibridge: %w", err)
	}
	d.cur, d.at = idx, t
	return idx, nil
}

// TVDB counts a series' episodes on TVDB v4 (cached listing).
type TVDB struct{ C *tvdb.Client }

func (c TVDB) EpisodeCounts(ctx context.Context, q indexer.Query) (map[int]int, error) {
	if c.C == nil || q.Type != indexer.QuerySeries || q.TVDB <= 0 {
		return nil, nil
	}
	return c.C.EpisodeCounts(ctx, q.TVDB)
}

// TMDb counts a series' episodes on TMDb by its IMDb id.
type TMDb struct{ C *tmdb.Client }

func (c TMDb) EpisodeCounts(ctx context.Context, q indexer.Query) (map[int]int, error) {
	if c.C == nil || q.Type != indexer.QuerySeries || q.IMDb == "" {
		return nil, nil
	}
	id, err := c.C.FindTVByIMDb(ctx, q.IMDb)
	if errors.Is(err, tmdb.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return c.C.SeasonEpisodeCounts(ctx, id)
}
