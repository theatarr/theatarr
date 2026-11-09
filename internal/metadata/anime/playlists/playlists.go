// Package playlists builds the anime watch-order playlists in Plex (docs/design/anime.md): one regular
// video playlist per franchise and order kind (policy playlists.orders), and one per alternate route of the watch
// order, from the library's linked episodes and movies.
//
// # Gating
//
// Plex is written only when library.enabled and playlists.enabled are both true and library.plex has a token
// (policy.PlaylistGate). Otherwise Sync is a dry run: it plans every playlist from the database alone, records the
// plan in plex_playlist (mode dry_run) and makes no Plex call at all, not even a read.
//
// # Items
//
// Each order row is an entry (or a slice of one): its TVDB episodes come from the applied mapping (anime_mapping,
// importable statuses only: a conflict range is left out like an import), narrowed to the row's part; an entry with
// no episodes but TMDb ids is a movie. Each episode or movie is the file Plex sees: our active link, else the arr's
// file (version_file) while the arr still owns the version. When enabled, files are found in Plex by path through
// the sections of library.versions (one listing per section and type), and the playlist is synced idempotently
// (plex.SyncPlaylist: diff, then move).
package playlists

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/animemap"
	"github.com/theatarr/theatarr/internal/metadata/anime/animestore"
	"github.com/theatarr/theatarr/internal/plex"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Item is one planned playlist item.
type Item struct {
	Row       int    `json:"row"` // the order row's position
	Entry     string `json:"entry"`
	Title     string `json:"title"`
	Kind      string `json:"kind"` // episode or movie
	TVDB      int    `json:"tvdb,omitempty"`
	Episode   string `json:"episode,omitempty"` // S01E02
	TMDb      int    `json:"tmdb,omitempty"`
	Path      string `json:"path,omitempty"` // the file Plex sees; "" when there is none
	From      string `json:"from,omitempty"` // link or arr
	RatingKey string `json:"rating_key,omitempty"`
	Missing   string `json:"missing,omitempty"` // why the item cannot be added
}

// Playlist is one planned playlist.
type Playlist struct {
	Franchise string           `json:"franchise"`
	Order     string           `json:"order"`
	Route     string           `json:"route,omitempty"`
	Name      string           `json:"name"`
	Items     []Item           `json:"items"`
	Ready     int              `json:"ready"`   // items with a file
	Missing   int              `json:"missing"` // items without one (left out)
	Sync      *plex.SyncResult `json:"sync,omitempty"`
	Error     string           `json:"error,omitempty"`
}

// Report is what Sync did.
type Report struct {
	DryRun    bool       `json:"dry_run"`
	Refused   string     `json:"refused,omitempty"` // why it is a dry run
	Playlists []Playlist `json:"playlists"`
}

// Plex is the part of the Plex client a sync needs.
type Plex interface {
	plex.PlaylistAPI
	Identity(ctx context.Context) (string, error)
	SectionFiles(ctx context.Context, section, typ int) (map[string]plex.FileItem, error)
}

// Syncer plans and syncs playlists.
type Syncer struct {
	Store  *store.Store
	Policy *policy.Policy
	// Plex is the client; nil means no token, which keeps every sync a dry run.
	Plex Plex
	Now  func() time.Time
}

func (s *Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Gate reports whether this syncer may write to Plex, and why not.
func (s *Syncer) Gate() (bool, string) {
	ok, why := s.Policy.PlaylistGate()
	if ok && s.Plex == nil {
		return false, "no Plex token (library.plex.token_env is unset)"
	}
	return ok, why
}

// Sync plans the playlists of one franchise ("" for every built franchise, or the policy's list) and, when the gate
// allows and dryRun is false, writes them to Plex. The plan is recorded either way.
func (s *Syncer) Sync(ctx context.Context, franchise string, dryRun bool) (*Report, error) {
	rep := &Report{DryRun: true}
	ok, why := s.Gate()
	switch {
	case dryRun:
		rep.Refused = "dry run asked for"
	case !ok:
		rep.Refused = why
	default:
		rep.DryRun = false
	}
	pls, err := s.Plan(ctx, franchise)
	if err != nil {
		return nil, err
	}
	rep.Playlists = pls
	if !rep.DryRun {
		if err := s.apply(ctx, rep.Playlists); err != nil {
			return rep, err
		}
	}
	return rep, s.record(ctx, rep)
}

// Plan builds the playlists from the database; it never calls Plex.
func (s *Syncer) Plan(ctx context.Context, franchise string) ([]Playlist, error) {
	set := s.Policy.PlaylistSettings()
	slugs, err := s.franchises(ctx, franchise, set.Franchises)
	if err != nil {
		return nil, err
	}
	as := &animestore.Store{DB: s.Store}
	var out []Playlist
	for _, slug := range slugs {
		res, err := as.Result(ctx, slug)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("playlists: %s: %w", slug, err)
		}
		maps, err := s.mappings(ctx, slug)
		if err != nil {
			return nil, err
		}
		r := &resolver{db: s.Store.Reader(), maps: maps, entries: map[string]*anime.Entry{}}
		for _, e := range res.Entries {
			r.entries[e.Key] = e
		}
		for _, kind := range set.Orders {
			main, _ := orderRows(res, kind)
			pl := Playlist{Franchise: slug, Order: kind, Name: playlistName(set.Name, res.Franchise, kind)}
			if pl.Items, err = r.items(ctx, main, set.SkipOptional); err != nil {
				return nil, err
			}
			out = append(out, finish(pl))
		}
		if set.Routes {
			_, alts := orderRows(res, "watch")
			for _, route := range routeBlocks(alts) {
				title := route[0].Key
				if t := route[0].Title; t != nil && *t != "" {
					title = *t
				}
				pl := Playlist{Franchise: slug, Order: "watch", Route: route[0].Key,
					Name: playlistName(set.Name, res.Franchise, "route: "+title)}
				if pl.Items, err = r.items(ctx, route, false); err != nil {
					return nil, err
				}
				out = append(out, finish(pl))
			}
		}
	}
	return out, nil
}

func finish(pl Playlist) Playlist {
	if pl.Items == nil {
		pl.Items = []Item{}
	}
	for _, it := range pl.Items {
		if it.Path != "" {
			pl.Ready++
		} else {
			pl.Missing++
		}
	}
	return pl
}

func playlistName(format, franchise, order string) string {
	return strings.NewReplacer("{franchise}", franchise, "{order}", orderLabel(order)).Replace(format)
}

func orderLabel(kind string) string {
	switch kind {
	case "release", "watch", "story":
		return kind + " order"
	}
	return kind
}

// orderRows splits an order into its main rows and its alternates.
func orderRows(res *anime.Result, kind string) (main, alts []*anime.Row) {
	switch kind {
	case "release":
		return res.ReleaseOrder, nil
	case "story":
		if res.StoryOrder != nil {
			return res.StoryOrder.Order, res.StoryOrder.Alternates
		}
		return nil, nil
	}
	for _, r := range res.WatchOrder {
		if r.Role == "alternate" {
			alts = append(alts, r)
		} else {
			main = append(main, r)
		}
	}
	return main, alts
}

// routeBlocks groups the watch order's alternates into routes: a root and the rows routed to it, when there are
// any (a lone alternate film is no route).
func routeBlocks(alts []*anime.Row) [][]*anime.Row {
	var out [][]*anime.Row
	for _, r := range alts {
		if r.Route != "" {
			continue
		}
		block := []*anime.Row{r}
		for _, x := range alts {
			if x.Route == r.Key {
				block = append(block, x)
			}
		}
		if len(block) > 1 {
			out = append(out, block)
		}
	}
	return out
}

func (s *Syncer) franchises(ctx context.Context, one string, only []string) ([]string, error) {
	if one != "" {
		return []string{one}, nil
	}
	rows, err := s.Store.Reader().QueryContext(ctx, `SELECT slug FROM anime_franchise WHERE result IS NOT NULL ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	allow := map[string]bool{}
	for _, x := range only {
		allow[x] = true
	}
	var out []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, err
		}
		if len(allow) == 0 || allow[slug] {
			out = append(out, slug)
		}
	}
	return out, rows.Err()
}

type mapping struct {
	status string
	series int
	eps    []anime.SE
}

func (s *Syncer) mappings(ctx context.Context, slug string) (map[string]mapping, error) {
	rows, err := s.Store.Reader().QueryContext(ctx, `SELECT entry_key, status, COALESCE(tvdb_series, 0), episodes
		FROM anime_mapping WHERE franchise = ?`, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]mapping{}
	for rows.Next() {
		var k, eps string
		var m mapping
		if err := rows.Scan(&k, &m.status, &m.series, &eps); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(eps), &m.eps); err != nil {
			return nil, err
		}
		sort.Slice(m.eps, func(i, j int) bool {
			a, b := m.eps[i], m.eps[j]
			return a[0] < b[0] || a[0] == b[0] && a[1] < b[1]
		})
		out[k] = m
	}
	return out, rows.Err()
}

type resolver struct {
	db      *sql.DB
	maps    map[string]mapping
	entries map[string]*anime.Entry
}

func (r *resolver) items(ctx context.Context, rows []*anime.Row, skipOptional bool) ([]Item, error) {
	var out []Item
	seen := map[string]bool{}
	for i, row := range rows {
		if skipOptional && row.Optional != nil && *row.Optional {
			continue
		}
		n := i + 1
		if row.N != nil {
			n = *row.N
		}
		title := row.Key
		if row.Title != nil && *row.Title != "" {
			title = *row.Title
		}
		m, mapped := r.maps[row.Key]
		if mapped && m.series != 0 && len(m.eps) > 0 {
			if !animemap.Importable(m.status) {
				out = append(out, Item{Row: n, Entry: row.Key, Title: title, Kind: "episode", TVDB: m.series,
					Missing: "mapping " + m.status + " (not reviewed)"})
				continue
			}
			for _, se := range anime.RowEpisodes(row, m.eps) {
				k := fmt.Sprintf("tvdb:%d:%s", m.series, se)
				if seen[k] {
					continue
				}
				seen[k] = true
				it := Item{Row: n, Entry: row.Key, Title: title, Kind: "episode", TVDB: m.series, Episode: se.String()}
				var err error
				if it.Path, it.From, err = r.episodeFile(ctx, m.series, se); err != nil {
					return nil, err
				}
				if it.Path == "" {
					it.Missing = "no file"
				}
				out = append(out, it)
			}
			continue
		}
		var tmdbIDs []int
		if e := r.entries[row.Key]; e != nil {
			tmdbIDs = e.TMDbMovie
		}
		if len(tmdbIDs) == 0 {
			tmdbIDs = row.TMDb
		}
		if len(tmdbIDs) == 0 {
			out = append(out, Item{Row: n, Entry: row.Key, Title: title, Kind: "episode", Missing: "no TVDB or TMDb mapping"})
			continue
		}
		for _, id := range tmdbIDs {
			k := fmt.Sprintf("tmdb:%d", id)
			if seen[k] {
				continue
			}
			seen[k] = true
			it := Item{Row: n, Entry: row.Key, Title: title, Kind: "movie", TMDb: id}
			var err error
			if it.Path, it.From, err = r.movieFile(ctx, id); err != nil {
				return nil, err
			}
			if it.Path == "" {
				it.Missing = "no file"
			}
			out = append(out, it)
		}
	}
	return out, nil
}

func (r *resolver) episodeFile(ctx context.Context, tvdb int, se anime.SE) (string, string, error) {
	var p string
	err := r.db.QueryRowContext(ctx, `SELECT l.path FROM link l JOIN link_episode le ON le.link_id = l.id
		JOIN episode e ON e.id = le.episode_id JOIN title t ON t.id = e.title_id
		WHERE t.kind = 'series' AND t.tvdb_id = ? AND e.season = ? AND e.number = ? AND l.state = 'active'
		ORDER BY l.id LIMIT 1`, tvdb, se[0], se[1]).Scan(&p)
	if err == nil {
		return p, "link", nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	err = r.db.QueryRowContext(ctx, `SELECT vf.path FROM version_file vf JOIN version_file_episode vfe ON vfe.version_file_id = vf.id
		JOIN episode e ON e.id = vfe.episode_id JOIN title t ON t.id = e.title_id
		WHERE t.kind = 'series' AND t.tvdb_id = ? AND e.season = ? AND e.number = ? ORDER BY vf.id LIMIT 1`,
		tvdb, se[0], se[1]).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return p, "arr", err
}

func (r *resolver) movieFile(ctx context.Context, tmdb int) (string, string, error) {
	var p string
	err := r.db.QueryRowContext(ctx, `SELECT l.path FROM link l JOIN title t ON t.id = l.title_id
		WHERE t.kind = 'movie' AND t.tmdb_id = ? AND l.state = 'active' ORDER BY l.id LIMIT 1`, tmdb).Scan(&p)
	if err == nil {
		return p, "link", nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	err = r.db.QueryRowContext(ctx, `SELECT vf.path FROM version_file vf JOIN title t ON t.id = vf.title_id
		WHERE t.kind = 'movie' AND t.tmdb_id = ? ORDER BY vf.id LIMIT 1`, tmdb).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return p, "arr", err
}

// apply finds every item in Plex by its file and syncs each playlist. A playlist that fails is reported and the
// others go on.
func (s *Syncer) apply(ctx context.Context, pls []Playlist) error {
	machine, err := s.Plex.Identity(ctx)
	if err != nil {
		return err
	}
	files := map[string]plex.FileItem{}
	sections := map[int]bool{}
	for _, lv := range s.Policy.LibrarySettings().Versions {
		if lv != nil && lv.PlexSection > 0 {
			sections[lv.PlexSection] = true
		}
	}
	var ids []int
	for id := range sections {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		for _, typ := range []int{plex.TypeEpisode, plex.TypeMovie} {
			fs, err := s.Plex.SectionFiles(ctx, id, typ)
			if err != nil {
				return fmt.Errorf("playlists: section %d: %w", id, err)
			}
			for p, it := range fs {
				files[p] = it
			}
		}
	}
	known, err := s.knownKeys(ctx)
	if err != nil {
		return err
	}
	for i := range pls {
		pl := &pls[i]
		var keys []string
		seen := map[string]bool{}
		for j := range pl.Items {
			it := &pl.Items[j]
			if it.Path == "" {
				continue
			}
			f, ok := files[it.Path]
			if !ok {
				it.Missing = "not in Plex"
				continue
			}
			it.RatingKey = f.RatingKey
			if !seen[f.RatingKey] {
				seen[f.RatingKey] = true
				keys = append(keys, f.RatingKey)
			}
		}
		res, err := plex.SyncPlaylist(ctx, s.Plex, machine, known[key(pl)], pl.Name, keys)
		if err != nil {
			pl.Error = err.Error()
			continue
		}
		pl.Sync = &res
	}
	return nil
}

func key(pl *Playlist) string { return pl.Franchise + "\x00" + pl.Order + "\x00" + pl.Route }

func (s *Syncer) knownKeys(ctx context.Context) (map[string]string, error) {
	rows, err := s.Store.Reader().QueryContext(ctx, `SELECT franchise, order_kind, route, rating_key FROM plex_playlist WHERE rating_key != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var f, o, r, rk string
		if err := rows.Scan(&f, &o, &r, &rk); err != nil {
			return nil, err
		}
		out[f+"\x00"+o+"\x00"+r] = rk
	}
	return out, rows.Err()
}

// record keeps each playlist's plan (and, when applied, its rating key and items) in plex_playlist.
func (s *Syncer) record(ctx context.Context, rep *Report) error {
	now := store.Millis(s.now())
	return s.Store.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, pl := range rep.Playlists {
			plan, _ := json.Marshal(pl.Items)
			mode, detail, rk, items := "dry_run", rep.Refused, "", "[]"
			var synced any
			if !rep.DryRun {
				mode, detail = "applied", ""
				if pl.Error != "" {
					mode, detail = "failed", pl.Error
				}
				if pl.Sync != nil {
					rk = pl.Sync.RatingKey
					var keys []string
					for _, it := range pl.Items {
						if it.RatingKey != "" {
							keys = append(keys, it.RatingKey)
						}
					}
					b, _ := json.Marshal(append([]string{}, keys...))
					items, synced = string(b), now
				}
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO plex_playlist (franchise, order_kind, route, name, rating_key, items, plan,
				mode, detail, synced_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (franchise, order_kind, route) DO UPDATE SET name = excluded.name,
				rating_key = CASE WHEN excluded.rating_key != '' THEN excluded.rating_key ELSE plex_playlist.rating_key END,
				items = CASE WHEN excluded.mode = 'applied' THEN excluded.items ELSE plex_playlist.items END,
				plan = excluded.plan, mode = excluded.mode, detail = excluded.detail,
				synced_at = COALESCE(excluded.synced_at, plex_playlist.synced_at), updated_at = excluded.updated_at`,
				pl.Franchise, pl.Order, pl.Route, pl.Name, rk, items, string(plan), mode, detail, synced, now, now); err != nil {
				return err
			}
		}
		return nil
	})
}
