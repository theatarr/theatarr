package indexer

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
)

// Querier is a database handle or transaction the SeaDex catalog reads through.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// SeaDexEntryMapping is one AniList entry filed under one catalog title. Series mappings carry the whole seasons
// (the title's own AniList id as its season 1, whole-season resolver targets) and, apart, the exact episodes
// franchise mappings list; a series mapping without either has no season scope (a held file's own entry recorded on
// its link, or an entry stored targets file under a season only through exact episodes no longer mapped).
type SeaDexEntryMapping struct {
	Title    int64
	Series   bool
	Entry    int
	Seasons  map[int]bool
	Episodes map[[2]int]bool
}

// Covers says whether a series entry covers one of the (season, number) episodes. Precedence is per season: in a
// season where the entry has any exact episode mapping only those episodes are covered, so a split-cour entry (or
// the title's own id as cour 1) never covers the other cour of its season; a season without exact
// episodes is covered whole by a whole-season mapping, so exact episodes in one season never narrow another.
// Movies are covered by every entry mapped to them.
func (m *SeaDexEntryMapping) Covers(episodes [][2]int) bool {
	if !m.Series {
		return true
	}
	for _, e := range episodes {
		if m.Episodes[e] || m.Seasons[e[0]] && !m.exactIn(e[0]) {
			return true
		}
	}
	return false
}

// CoversAll requires every known episode of a held file to belong to this entry.
func (m *SeaDexEntryMapping) CoversAll(episodes [][2]int) bool {
	if !m.Series {
		return true
	}
	if len(episodes) == 0 {
		return false
	}
	for _, ep := range episodes {
		if !m.Covers([][2]int{ep}) {
			return false
		}
	}
	return true
}

// exactIn says whether the entry has an exact episode mapping in season.
func (m *SeaDexEntryMapping) exactIn(season int) bool {
	for se := range m.Episodes {
		if se[0] == season {
			return true
		}
	}
	return false
}

// SeaDexCatalog is every mapping of AniList entries to catalog titles, read in one pass per operation: the titles'
// own AniList ids (a series as its season 1, as the resolver reads them), franchise mappings with their episodes,
// franchise movies, the resolver targets recorded with stored standing, and the entries held files recorded.
type SeaDexCatalog struct {
	byTitle map[int64][]*SeaDexEntryMapping
	mapped  map[int]bool
	series  map[int64]bool
	byTVDB  map[int][]int64
	byTMDb  map[int][]int64
}

// LoadSeaDexCatalog reads the catalog. snapshots are stored standing whose resolver targets count as mappings.
func LoadSeaDexCatalog(ctx context.Context, db Querier, snapshots []SeaDexSnapshot) (*SeaDexCatalog, error) {
	c := &SeaDexCatalog{byTitle: map[int64][]*SeaDexEntryMapping{}, mapped: map[int]bool{}, series: map[int64]bool{},
		byTVDB: map[int][]int64{}, byTMDb: map[int][]int64{}}
	err := each(ctx, db, `SELECT id, kind, COALESCE(tvdb_id, 0), COALESCE(tmdb_id, 0) FROM title`, func(r *sql.Rows) error {
		var id int64
		var kind string
		var tvdb, tmdb int
		if err := r.Scan(&id, &kind, &tvdb, &tmdb); err != nil {
			return err
		}
		switch kind {
		case "series":
			c.series[id] = true
			if tvdb > 0 {
				c.byTVDB[tvdb] = append(c.byTVDB[tvdb], id)
			}
		case "movie":
			if tmdb > 0 {
				c.byTMDb[tmdb] = append(c.byTMDb[tmdb], id)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = each(ctx, db, `SELECT title_id, external_id FROM title_external_id WHERE source = 'anilist'`, func(r *sql.Rows) error {
		var title int64
		var ext string
		if err := r.Scan(&title, &ext); err != nil {
			return err
		}
		if id, err := strconv.Atoi(strings.TrimSpace(ext)); err == nil && id > 0 {
			m := c.add(title, id)
			if m.Series {
				m.Seasons[1] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = each(ctx, db, `SELECT e.anilist_id, m.tvdb_series, m.episodes FROM anime_entry e
		JOIN anime_mapping m ON m.franchise = e.franchise AND m.entry_key = e.key
		WHERE e.anilist_id IS NOT NULL AND m.tvdb_series IS NOT NULL`, func(r *sql.Rows) error {
		var id, tvdb int
		var eps string
		if err := r.Scan(&id, &tvdb, &eps); err != nil {
			return err
		}
		var ses [][2]int
		_ = json.Unmarshal([]byte(eps), &ses)
		for _, title := range c.byTVDB[tvdb] {
			m := c.add(title, id)
			for _, se := range ses {
				if se[0] > 0 {
					m.Episodes[se] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = each(ctx, db, `SELECT e.anilist_id, CAST(j.value AS INTEGER) FROM anime_entry e,
		json_each(CASE WHEN json_valid(e.tmdb_movie) THEN e.tmdb_movie ELSE '[]' END) j
		WHERE e.anilist_id IS NOT NULL`, func(r *sql.Rows) error {
		var id, tmdb int
		if err := r.Scan(&id, &tmdb); err != nil {
			return err
		}
		for _, title := range c.byTMDb[tmdb] {
			c.add(title, id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Only links whose metadata names an entry are parsed; the rest are skipped by a plain substring test.
	err = each(ctx, db, `SELECT title_id, CAST(json_extract(file_metadata, '$.SeaDexEntry') AS INTEGER) FROM link
		WHERE state = 'active' AND instr(file_metadata, '"SeaDexEntry"') > 0 AND json_valid(file_metadata)`, func(r *sql.Rows) error {
		var title int64
		var id sql.NullInt64
		if err := r.Scan(&title, &id); err != nil {
			return err
		}
		if id.Int64 > 0 {
			c.add(title, int(id.Int64))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, s := range snapshots {
		c.addTargets(s)
	}
	return c, nil
}

func each(ctx context.Context, db Querier, q string, f func(*sql.Rows) error) error {
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := f(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (c *SeaDexCatalog) add(title int64, entry int) *SeaDexEntryMapping {
	c.mapped[entry] = true
	for _, m := range c.byTitle[title] {
		if m.Entry == entry {
			return m
		}
	}
	m := &SeaDexEntryMapping{Title: title, Series: c.series[title], Entry: entry, Seasons: map[int]bool{},
		Episodes: map[[2]int]bool{}}
	c.byTitle[title] = append(c.byTitle[title], m)
	return m
}

func (c *SeaDexCatalog) addTargets(s SeaDexSnapshot) {
	if s.AniList <= 0 {
		return
	}
	for _, t := range s.Targets {
		if t.TMDb > 0 {
			for _, title := range c.byTMDb[t.TMDb] {
				c.add(title, s.AniList)
			}
		}
		if t.TVDB > 0 {
			for _, title := range c.byTVDB[t.TVDB] {
				m := c.add(title, s.AniList)
				// A season named only through exact episodes maps the entry to the title but covers no whole season:
				// its episodes come from the franchise mappings.
				if t.Season > 0 && !t.Exact {
					m.Seasons[t.Season] = true
				}
			}
		}
	}
}

// Titles is the catalog by title id.
func (c *SeaDexCatalog) Titles() map[int64][]*SeaDexEntryMapping { return c.byTitle }

// Series says whether title is a series.
func (c *SeaDexCatalog) Series(title int64) bool { return c.series[title] }

// Mapped says whether the snapshot's entry maps to a catalog title, directly or through its own resolver targets.
func (c *SeaDexCatalog) Mapped(s SeaDexSnapshot) bool {
	if c.mapped[s.AniList] {
		return true
	}
	for _, t := range s.Targets {
		if t.TMDb > 0 && len(c.byTMDb[t.TMDb]) > 0 || t.TVDB > 0 && len(c.byTVDB[t.TVDB]) > 0 {
			return true
		}
	}
	return false
}

// SeaDexHeldRefs are the hashes and SeaDex record ids of the files the library holds (active links and arr files):
// a stored snapshot listing one of them can be the only entry provenance of an older held file.
type SeaDexHeldRefs struct {
	Hashes  map[string]bool
	Records map[string]bool
}

// LoadSeaDexHeldRefs reads the held references in one pass.
func LoadSeaDexHeldRefs(ctx context.Context, db Querier) (SeaDexHeldRefs, error) {
	h := SeaDexHeldRefs{Hashes: map[string]bool{}, Records: map[string]bool{}}
	add := func(hashes []string, names []string) {
		for _, x := range hashes {
			if x = NormalizeHash(x); x != "" {
				h.Hashes[x] = true
			}
		}
		for _, n := range names {
			if id := SeaDexRecordID(n); id != "" {
				h.Records[id] = true
			}
		}
	}
	err := each(ctx, db, `SELECT COALESCE(json_extract(CASE WHEN json_valid(l.file_metadata) THEN l.file_metadata ELSE '{}' END, '$.InfoHash'), ''),
		COALESCE(json_extract(CASE WHEN json_valid(l.file_metadata) THEN l.file_metadata ELSE '{}' END, '$.SceneName'), ''),
		COALESCE(l.release_title, ''), COALESCE(d.info_hash, ''), COALESCE(c.info_hash, '')
		FROM link l LEFT JOIN download d ON d.id = l.download_id
		LEFT JOIN content_file cf ON cf.id = l.content_file_id LEFT JOIN content c ON c.id = cf.content_id
		WHERE l.state = 'active'`, func(r *sql.Rows) error {
		var hash, scene, release, dl, content string
		if err := r.Scan(&hash, &scene, &release, &dl, &content); err != nil {
			return err
		}
		add([]string{hash, dl, content}, []string{scene, release})
		return nil
	})
	if err != nil {
		return h, err
	}
	err = each(ctx, db, `SELECT COALESCE(c.info_hash, ''), COALESCE(vf.scene_name, '') FROM version_file vf
		LEFT JOIN content c ON c.id = vf.content_id`, func(r *sql.Rows) error {
		var hash, scene string
		if err := r.Scan(&hash, &scene); err != nil {
			return err
		}
		add([]string{hash}, []string{scene})
		return nil
	})
	return h, err
}

// References says whether the snapshot lists a held hash or record id.
func (h SeaDexHeldRefs) References(s SeaDexSnapshot) bool {
	for _, t := range s.Torrents {
		if h.Hashes[NormalizeHash(t.InfoHash)] || t.RecordID != "" && h.Records[t.RecordID] {
			return true
		}
	}
	return false
}
