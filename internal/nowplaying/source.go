package nowplaying

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sort"
	"strings"

	"github.com/theatarr/theatarr/internal/store"
)

// Source is where a playing file comes from: Plex's path, the mount target it resolves to, Theatarr's link (title,
// versions, release) and the content with the providers holding it.
type Source struct {
	File    string `json:"file"`
	Target  string `json:"target,omitempty"`
	LinkID  int64  `json:"link_id,omitempty"`
	TitleID int64  `json:"title_id,omitempty"`
	Title   string `json:"title,omitempty"`
	// Versions are the policy keys of the link's versions.
	Versions []string `json:"versions,omitempty"`
	Release  string   `json:"release,omitempty"`
	Quality  string   `json:"quality,omitempty"`
	Hash     string   `json:"hash,omitempty"`
	// Providers hold the content per the registry (as of its last sync).
	Providers []string `json:"providers,omitempty"`
	// SingleCopy: exactly one provider holds it.
	SingleCopy bool `json:"single_copy,omitempty"`
	// ServedBy is the account zurg last logged serving the file from ("Large transfer").
	ServedBy string `json:"served_by,omitempty"`
	// Matched says how the file was found: link (a Theatarr link with this target or path), content (the registry by
	// the release folder), zurg (zurg's library entry) or "" (not found).
	Matched string `json:"matched"`

	versionIDs []int64
	episodes   []int64
	rel        string // the path under the release root: <folder>/<file>
}

// mountRel is target's path under the release root (<all_dir>/), "" when it has none.
func mountRel(target, allDir string) string {
	i := strings.Index(target, "/"+allDir+"/")
	if i < 0 {
		return ""
	}
	return target[i+len(allDir)+2:]
}

// resolve maps a Plex file to its source. The join is on the link target, not the path: Plex libraries may still
// point at the arr roots, whose symlinks resolve to the same mount target as Theatarr's links.
func (t *Tracker) resolve(ctx context.Context, file string) *Source {
	src := &Source{File: file, Target: file}
	if !within(file, t.o.LinkPath) && !within(file, t.o.MountPath) {
		// Only paths outside the mount are read as links: a readlink inside it would be a FUSE call.
		if tgt, err := t.o.Readlink(file); err == nil {
			if !filepath.IsAbs(tgt) {
				tgt = filepath.Join(filepath.Dir(file), tgt)
			}
			src.Target = filepath.Clean(tgt)
		}
	}
	src.rel = mountRel(src.Target, t.o.AllDir)
	db := t.o.Store.Reader()
	type cand struct {
		id, titleID          int64
		title, release, qual string
		path                 string
		contentFile          sql.NullInt64
		versionIDs           []int64
		versions             []string
		under                bool // a version's root, or the root of the version it follows, holds file
	}
	var cands []*cand
	q := `SELECT l.id, l.title_id, t.name, COALESCE(l.release_title, ''), COALESCE(l.quality, ''), l.path,
		l.content_file_id FROM link l JOIN title t ON t.id = l.title_id WHERE l.state = 'active' AND `
	scan := func(where string, arg string) {
		rows, err := db.QueryContext(ctx, q+where+` ORDER BY l.id`, arg)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			c := &cand{}
			if rows.Scan(&c.id, &c.titleID, &c.title, &c.release, &c.qual, &c.path, &c.contentFile) == nil {
				cands = append(cands, c)
			}
		}
	}
	if src.rel != "" {
		scan(`l.kind = 'symlink' AND l.target = ?`, filepath.Join(t.o.LinkPath, t.o.AllDir, src.rel))
	}
	if len(cands) == 0 {
		scan(`l.path = ?`, file)
	}
	// Several versions may link the same target: the one whose root holds Plex's path is the one playing. When
	// that is not decided (none or several), the source names every version sharing the target.
	var chosen []*cand
	for _, c := range cands {
		if c.path == file {
			chosen = []*cand{c}
			break
		}
	}
	if chosen == nil && len(cands) > 1 {
		for _, c := range cands {
			t.linkVersions(ctx, c.id, func(id int64, key string) {
				c.versionIDs, c.versions = append(c.versionIDs, id), append(c.versions, key)
				if t.o.Roots == nil {
					return
				}
				for _, k := range append([]string{key}, t.strings(ctx, `SELECT f.policy_key FROM version_follow vf
					JOIN version f ON f.id = vf.follows_id WHERE vf.version_id = ?`, id)...) {
					for _, r := range t.o.Roots(k) {
						c.under = c.under || within(file, r)
					}
				}
			})
			if c.under {
				chosen = append(chosen, c)
			}
		}
		if len(chosen) != 1 {
			chosen = cands
		}
	}
	if chosen == nil {
		chosen = cands
	}
	var contentID int64
	if len(chosen) > 0 {
		c := chosen[0]
		src.Matched, src.LinkID, src.TitleID, src.Title, src.Release, src.Quality = "link", c.id, c.titleID, c.title,
			c.release, c.qual
		seen := map[int64]bool{}
		for _, c := range chosen {
			if c.versionIDs == nil {
				t.linkVersions(ctx, c.id, func(id int64, key string) {
					c.versionIDs, c.versions = append(c.versionIDs, id), append(c.versions, key)
				})
			}
			for i, id := range c.versionIDs {
				if !seen[id] {
					seen[id] = true
					src.versionIDs, src.Versions = append(src.versionIDs, id), append(src.Versions, c.versions[i])
				}
			}
		}
		src.episodes = t.ints(ctx, `SELECT episode_id FROM link_episode WHERE link_id = ? ORDER BY episode_id`, src.LinkID)
		if c.contentFile.Valid {
			_ = db.QueryRowContext(ctx, `SELECT content_id FROM content_file WHERE id = ?`, c.contentFile.Int64).Scan(&contentID)
		}
	}
	folder, rest, _ := strings.Cut(src.rel, "/")
	if contentID == 0 && folder != "" {
		_ = db.QueryRowContext(ctx, `SELECT id FROM content WHERE mount_folder = ? ORDER BY id LIMIT 1`, folder).Scan(&contentID)
		if contentID != 0 && src.Matched == "" {
			src.Matched = "content"
		}
	}
	if contentID == 0 && folder != "" && t.o.Zurg != nil {
		// zurg's entry names the torrent; its signed streaming path is never read.
		if e, err := t.o.Zurg.Entry(ctx, folder, rest); err == nil && e != nil && e.Hash != "" {
			src.Hash = strings.ToLower(e.Hash)
			_ = db.QueryRowContext(ctx, `SELECT id FROM content WHERE info_hash = ?`, src.Hash).Scan(&contentID)
			if src.Matched == "" {
				src.Matched = "zurg"
			}
		}
	}
	if contentID != 0 {
		_ = db.QueryRowContext(ctx, `SELECT info_hash FROM content WHERE id = ?`, contentID).Scan(&src.Hash)
		src.Providers = t.strings(ctx, `SELECT provider FROM content_provider WHERE content_id = ? AND state = 'present'
			ORDER BY provider`, contentID)
		src.SingleCopy = len(src.Providers) == 1
	}
	return src
}

// linkVersions calls on with each version of a link, by id.
func (t *Tracker) linkVersions(ctx context.Context, linkID int64, on func(id int64, key string)) {
	rows, err := t.o.Store.Reader().QueryContext(ctx, `SELECT v.id, v.policy_key FROM link_version lv
		JOIN version v ON v.id = lv.version_id WHERE lv.link_id = ? ORDER BY v.id`, linkID)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var key string
		if rows.Scan(&id, &key) == nil {
			on(id, key)
		}
	}
}

func within(p, dir string) bool {
	return dir != "" && (p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/"))
}

func (t *Tracker) ints(ctx context.Context, q string, args ...any) []int64 {
	var out []int64
	rows, err := t.o.Store.Reader().QueryContext(ctx, q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var v int64
		if rows.Scan(&v) == nil {
			out = append(out, v)
		}
	}
	return out
}

func (t *Tracker) strings(ctx context.Context, q string, args ...any) []string {
	var out []string
	rows, err := t.o.Store.Reader().QueryContext(ctx, q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		if rows.Scan(&v) == nil {
			out = append(out, v)
		}
	}
	return out
}

// episodeRow is an episode with what ordering and the pre-warm need.
type episodeRow struct {
	ID, Season, Number int64
	Name               string
	Absolute           sql.NullInt64
	AiredAt            sql.NullInt64 // ms
	Monitored          bool
}

// errNoNext: the episode has no next one to warm (the last, not aired yet, or not monitored).
var errNoNext = errors.New("no next episode to warm")

// nextEpisode is the episode after current (the playing link's episodes) in the version's order: a version whose
// series type is anime (its arr instance's, else the title's) goes by absolute numbers where the episodes have them,
// skipping specials, anything else by season and number, specials staying among themselves.
func (t *Tracker) nextEpisode(ctx context.Context, titleID, versionID int64, current []int64, nowMs int64) (*episodeRow, error) {
	db := t.o.Store.Reader()
	var monitored bool
	if err := db.QueryRowContext(ctx, `SELECT monitored FROM version WHERE id = ? AND title_id = ?`,
		versionID, titleID).Scan(&monitored); err != nil {
		return nil, err
	}
	if !monitored {
		return nil, errNoNext
	}
	st, err := store.LoadSourceSettings(ctx, db, versionID)
	if err != nil {
		return nil, err
	}
	var anime bool
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(series_type, '') = 'anime' OR anime = 1 FROM title WHERE id = ?`,
		titleID).Scan(&anime); err != nil {
		return nil, err
	}
	if st.SeriesType != "" {
		anime = st.SeriesType == "anime"
	}
	rows, err := db.QueryContext(ctx, `SELECT e.id, e.season, e.number, e.name,
		COALESCE((SELECT MIN(n.number) FROM episode_number n WHERE n.episode_id = e.id AND n.scheme = 'absolute'),
			(SELECT MIN(n.number) FROM episode_number n WHERE n.episode_id = e.id AND n.scheme = 'absolute_tvdb')),
		e.air_date_utc_at, COALESCE(s.monitored, e.monitored) = 1
		FROM episode e LEFT JOIN episode_version_state s ON s.episode_id = e.id AND s.version_id = ?
		WHERE e.title_id = ? AND e.removed_at IS NULL`, versionID, titleID)
	if err != nil {
		return nil, err
	}
	var eps []episodeRow
	for rows.Next() {
		var e episodeRow
		if err := rows.Scan(&e.ID, &e.Season, &e.Number, &e.Name, &e.Absolute, &e.AiredAt, &e.Monitored); err != nil {
			rows.Close()
			return nil, err
		}
		eps = append(eps, e)
	}
	rows.Close()
	next := pickNext(eps, current, anime)
	if next == nil || !next.Monitored || !next.AiredAt.Valid || next.AiredAt.Int64 > nowMs {
		return nil, errNoNext
	}
	return next, nil
}

// pickNext orders eps and returns the one after the last of current, or nil.
func pickNext(eps []episodeRow, current []int64, anime bool) *episodeRow {
	cur := map[int64]bool{}
	for _, id := range current {
		cur[id] = true
	}
	var last *episodeRow
	byAbsolute := anime
	for i := range eps {
		// Partial absolute metadata cannot define a complete order. Fall back to season/episode so an
		// unnumbered regular episode is never silently dropped. Specials do not affect regular ordering.
		if eps[i].Season != 0 && !eps[i].Absolute.Valid {
			byAbsolute = false
		}
		if cur[eps[i].ID] {
			byAbsolute = byAbsolute && eps[i].Absolute.Valid
			last = &eps[i]
		}
	}
	if last == nil {
		return nil
	}
	// Anime goes by absolute number through the regular episodes only: a special numbered between two of them is
	// not the next one. A special playing keeps season order among the specials.
	byAbsolute = byAbsolute && last.Season != 0
	key := func(e episodeRow) [2]int64 {
		if byAbsolute {
			return [2]int64{e.Absolute.Int64, 0}
		}
		return [2]int64{e.Season, e.Number}
	}
	var cands []episodeRow
	for _, e := range eps {
		if (e.Season == 0) != (last.Season == 0) {
			continue
		}
		cands = append(cands, e)
	}
	sort.Slice(cands, func(i, j int) bool {
		a, b := key(cands[i]), key(cands[j])
		return a[0] < b[0] || (a[0] == b[0] && a[1] < b[1])
	})
	lastKey := [2]int64{-1 << 62, 0}
	for _, e := range cands {
		if k := key(e); cur[e.ID] && (k[0] > lastKey[0] || (k[0] == lastKey[0] && k[1] > lastKey[1])) {
			lastKey = k
		}
	}
	for i, e := range cands {
		if k := key(e); !cur[e.ID] && (k[0] > lastKey[0] || (k[0] == lastKey[0] && k[1] > lastKey[1])) {
			return &cands[i]
		}
	}
	return nil
}
