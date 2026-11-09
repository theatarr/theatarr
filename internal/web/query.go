package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/jobs"
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
	Q    string // substring of the name
	// With: the title has a file for this version key. Without: it has no file for it (no version, or a version
	// that is wanted). These give "no WEB version" and friends.
	With, Without string
	// State: some version of the title is in this state. With plainState, filled also takes cutoff_met (a caller
	// without "see files" sees both as filled).
	State      string
	plainState bool
	Limit      int
	Offset     int
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

// hasFileSQL is true when version v has a file: an arr file or an active link.
const hasFileSQL = `(EXISTS (SELECT 1 FROM version_file vf WHERE vf.version_id = v.id)
	OR EXISTS (SELECT 1 FROM link_version lv JOIN link l ON l.id = lv.link_id WHERE lv.version_id = v.id AND l.state = 'active'))`

func (f TitleFilter) where() (string, []any) {
	var conds []string
	var args []any
	if f.Kind != "" {
		conds = append(conds, "t.kind = ?")
		args = append(args, f.Kind)
	}
	if f.Q != "" {
		conds = append(conds, "t.name LIKE ? ESCAPE '\\'")
		args = append(args, "%"+likeEscape(f.Q)+"%")
	}
	if f.With != "" {
		conds = append(conds, "EXISTS (SELECT 1 FROM version v WHERE v.title_id = t.id AND v.policy_key = ? AND "+hasFileSQL+")")
		args = append(args, f.With)
	}
	if f.Without != "" {
		conds = append(conds, "NOT EXISTS (SELECT 1 FROM version v WHERE v.title_id = t.id AND v.policy_key = ? AND "+hasFileSQL+")")
		args = append(args, f.Without)
	}
	switch {
	case f.State == "filled" && f.plainState:
		conds = append(conds, "EXISTS (SELECT 1 FROM version v WHERE v.title_id = t.id AND v.state IN ('filled', 'cutoff_met'))")
	case f.State != "":
		conds = append(conds, "EXISTS (SELECT 1 FROM version v WHERE v.title_id = t.id AND v.state = ?)")
		args = append(args, f.State)
	}
	conds, args = f.TitleGridFilter.where(conds, args)
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (w *Web) listTitles(ctx context.Context, f TitleFilter) (TitlePage, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	page := TitlePage{Limit: f.Limit, Offset: f.Offset, Items: []TitleRow{}}
	where, args := f.where()
	db := w.st.Reader()
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*)"+titleFrom+where, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	f.today = w.now().UTC().Format(time.DateOnly)
	order, orderArgs := f.orderBy()
	items, err := w.titleRows(ctx, db, `SELECT `+titleCols+titleFrom+where+order+` LIMIT ? OFFSET ?`,
		append(append(args, orderArgs...), f.Limit, f.Offset))
	if err != nil {
		return page, err
	}
	page.Items = items
	return page, nil
}

// titleCols and titleFrom are the list row's columns and tables; m is the title's title_meta row, if any.
const (
	titleCols = `t.id, t.kind, t.name, t.year, t.tmdb_id, t.tvdb_id, COALESCE(t.imdb_id, ''), t.anime, t.monitored`
	titleFrom = ` FROM title t LEFT JOIN title_meta m ON m.title_id = t.id`
)

// titleRows runs q (selecting titleCols) and fills each row's art, facts and versions, in q's order.
func (w *Web) titleRows(ctx context.Context, db *sql.DB, q string, args []any) ([]TitleRow, error) {
	items := []TitleRow{}
	err := scanAll(ctx, db, q, args, func(rows *sql.Rows) error {
		var r TitleRow
		var year, tmdb, tvdb sql.NullInt64
		if err := rows.Scan(&r.ID, &r.Kind, &r.Name, &year, &tmdb, &tvdb, &r.IMDbID, &r.Anime, &r.Monitored); err != nil {
			return err
		}
		r.Year, r.TMDbID, r.TVDbID = nullInt(year), nullInt(tmdb), nullInt(tvdb)
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
	err = scanAll(ctx, db, `SELECT v.title_id, v.id, v.policy_key, v.state, v.monitored, `+hasFileSQL+`
		FROM version v WHERE v.title_id IN (`+store.Placeholders(len(ids))+`) ORDER BY v.policy_key`, ids, func(vrows *sql.Rows) error {
		var tid int64
		var v VersionBrief
		if err := vrows.Scan(&tid, &v.ID, &v.Key, &v.State, &v.Monitored, &v.HasFile); err != nil {
			return err
		}
		it := &items[idx[tid]]
		it.Versions = append(it.Versions, v)
		return nil
	})
	if err != nil {
		return items, err
	}
	if err := w.fillVersionFiles(ctx, db, items); err != nil {
		return items, err
	}
	return items, w.fillCopies(ctx, db, items)
}

func nullInt(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

// A target hash is safe to share when every active symlinked version file on that target agrees. Only targets
// used by the requested versions (one JSON array argument) need evidence, but all versions on those targets vote.
const uniqueLinkedTargetHash = `WITH requested_versions AS (SELECT value AS id FROM json_each(?)),
requested_targets AS (
	SELECT l.target FROM version_file vf CROSS JOIN link l ON l.path = vf.path
	WHERE vf.version_id IN (SELECT id FROM requested_versions) AND l.state = 'active' AND l.kind = 'symlink'
	UNION
	SELECT l.target FROM link l WHERE l.id IN
		(SELECT link_id FROM link_version WHERE version_id IN (SELECT id FROM requested_versions))
		AND l.state = 'active' AND l.kind = 'symlink'
), unique_target_hash AS (
	SELECT l2.target, MIN(COALESCE(c2.info_hash, c3.info_hash)) AS info_hash
	FROM version_file vf2 JOIN link l2 ON l2.path = vf2.path AND l2.state = 'active' AND l2.kind = 'symlink'
	LEFT JOIN content c2 ON c2.id = vf2.content_id
	LEFT JOIN content_file cf2 ON cf2.id = l2.content_file_id
	LEFT JOIN content c3 ON c3.id = cf2.content_id
	WHERE l2.target IN (SELECT target FROM requested_targets)
		AND COALESCE(c2.info_hash, c3.info_hash) IS NOT NULL AND COALESCE(c2.info_hash, c3.info_hash) != ''
	GROUP BY l2.target HAVING COUNT(DISTINCT COALESCE(c2.info_hash, c3.info_hash)) = 1
)
`

// File is a file a version holds, as the arr reported it.
type File struct {
	ID                int64      `json:"id"`
	Instance          string     `json:"instance"`
	ArrFileID         int64      `json:"arr_file_id"`
	Path              string     `json:"path"`
	RelativePath      string     `json:"relative_path"`
	SizeBytes         int64      `json:"size_bytes"`
	Quality           string     `json:"quality"`
	QualityRevision   int64      `json:"quality_revision"`
	ReleaseGroup      string     `json:"release_group"`
	SceneName         string     `json:"scene_name"`
	Edition           string     `json:"edition"`
	Languages         []string   `json:"languages"`
	CustomFormatScore *int64     `json:"custom_format_score,omitempty"`
	CutoffNotMet      bool       `json:"cutoff_not_met"`
	AddedAt           *time.Time `json:"added_at,omitempty"`
	ReleaseTitle      string     `json:"release_title,omitempty"`
	InfoHash          string     `json:"info_hash,omitempty"`
	// Current is true for the file of the instance that owns the version.
	Current bool `json:"current"`
}

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
}

// Version is a version with its files.
type Version struct {
	ID         int64     `json:"id"`
	TitleID    int64     `json:"title_id"`
	TitleName  string    `json:"title_name,omitempty"`
	Key        string    `json:"key"`
	State      string    `json:"state"`
	Monitored  bool      `json:"monitored"`
	HasFile    bool      `json:"has_file"`
	Source     string    `json:"source,omitempty"` // owning arr instance
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	Files      []File    `json:"files"`
	Links      []Link    `json:"links"`
	ArrRecords []ArrRec  `json:"arr_records"`
	// The title page's additions (titlepage.go); empty on other routes.
	Label     string         `json:"label,omitempty"`
	Current   *CurrentFile   `json:"current,omitempty"`
	Providers []string       `json:"providers,omitempty"`
	Episodes  *EpisodeCounts `json:"episodes,omitempty"`
	History   []HistoryEvent `json:"history,omitempty"`
	// Upgrade explains the state against the cutoff; Control says what the title page's controls may do (manage.go).
	Upgrade *UpgradeInfo    `json:"upgrade,omitempty"`
	Control *VersionControl `json:"control,omitempty"`
	// Acquisition is the download in hand for the version (copies.go), for callers who see files.
	Acquisition *Acquisition `json:"acquisition,omitempty"`
}

// ArrRec is how an arr instance holds the title for this version.
type ArrRec struct {
	Instance       string   `json:"instance"`
	ArrID          int64    `json:"arr_id"`
	Monitored      bool     `json:"monitored"`
	QualityProfile string   `json:"quality_profile,omitempty"`
	Status         string   `json:"status"`
	Path           string   `json:"path"`
	Tags           []string `json:"tags"`
}

// RegistryEntry is a content tied to a version of a title.
type RegistryEntry struct {
	InfoHash     string     `json:"info_hash"`
	Name         string     `json:"name"`
	State        string     `json:"state"`
	VersionID    int64      `json:"version_id"`
	VersionKey   string     `json:"version_key"`
	ReleaseTitle string     `json:"release_title"`
	Source       string     `json:"source"`
	GrabbedAt    *time.Time `json:"grabbed_at,omitempty"`
	ImportedAt   *time.Time `json:"imported_at,omitempty"`
	FailedAt     *time.Time `json:"failed_at,omitempty"`
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
	SortName     string          `json:"sort_name"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	ExternalIDs  []ExternalID    `json:"external_ids"`
	ReleaseDates []ReleaseDate   `json:"release_dates"`
	Versions     []Version       `json:"versions"`
	Registry     []RegistryEntry `json:"registry"`
	// Meta is the title's TMDb metadata; null before the meta.art job first tried the title.
	Meta *TitleMeta `json:"meta"`
	// Franchises are the anime franchises that map into the title (titlepage.go).
	Franchises []FranchiseRef `json:"franchises,omitempty"`
}

func (w *Web) getTitle(ctx context.Context, id int64) (*TitleDetail, error) {
	db := w.st.Reader()
	d := &TitleDetail{ExternalIDs: []ExternalID{}, ReleaseDates: []ReleaseDate{}, Versions: []Version{},
		Registry: []RegistryEntry{}}
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
	if err := scanAll(ctx, db, `SELECT source, country, type, date FROM title_release_date WHERE title_id = ?
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
	err = scanAll(ctx, db, `SELECT c.info_hash, c.name, c.state, v.id, v.policy_key, cv.release_title, cv.source,
		cv.grabbed_at, cv.imported_at, cv.failed_at FROM content_version cv JOIN content c ON c.id = cv.content_id
		JOIN version v ON v.id = cv.version_id WHERE v.title_id = ?
		ORDER BY COALESCE(cv.imported_at, cv.grabbed_at, cv.failed_at) DESC, c.info_hash`, []any{id},
		func(r *sql.Rows) error {
			var e RegistryEntry
			var g, i, f sql.NullInt64
			if err := r.Scan(&e.InfoHash, &e.Name, &e.State, &e.VersionID, &e.VersionKey, &e.ReleaseTitle, &e.Source,
				&g, &i, &f); err != nil {
				return err
			}
			e.GrabbedAt, e.ImportedAt, e.FailedAt = nullTS(g), nullTS(i), nullTS(f)
			d.Registry = append(d.Registry, e)
			return nil
		})
	return d, err
}

// versions loads versions matching cond with their files, links and arr records. limit 0 means all.
func (w *Web) versions(ctx context.Context, cond string, args []any, limit, offset int) ([]Version, error) {
	db := w.st.Reader()
	q := `SELECT v.id, v.title_id, t.name, v.policy_key, v.state, v.monitored, ` + hasFileSQL + `,
		COALESCE(vs.instance, ''), v.created_at, v.updated_at FROM version v JOIN title t ON t.id = v.title_id
		LEFT JOIN version_source vs ON vs.version_id = v.id WHERE ` + cond + ` ORDER BY v.title_id, v.policy_key`
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
		if err := r.Scan(&v.ID, &v.TitleID, &v.TitleName, &v.Key, &v.State, &v.Monitored, &v.HasFile, &v.Source,
			&c, &u); err != nil {
			return err
		}
		v.CreatedAt, v.UpdatedAt = ts(c), ts(u)
		v.Files, v.Links, v.ArrRecords = []File{}, []Link{}, []ArrRec{}
		idx[v.ID] = len(out)
		ids = append(ids, v.ID)
		out = append(out, v)
		return nil
	})
	if err != nil || len(ids) == 0 {
		return out, err
	}
	in := store.Placeholders(len(ids))
	targetIDs, _ := json.Marshal(ids)
	targetArgs := append([]any{string(targetIDs)}, ids...)
	err = scanAll(ctx, db, uniqueLinkedTargetHash+`SELECT vf.version_id, vf.id, vf.instance, vf.arr_file_id, vf.path, vf.relative_path,
		vf.size_bytes, vf.quality, vf.quality_revision, vf.release_group, vf.scene_name, vf.edition, vf.languages,
		vf.custom_format_score, vf.cutoff_not_met, vf.added_at, COALESCE(vf.release_title, ''),
		COALESCE(c.info_hash, uth.info_hash, ''), COALESCE(vs.instance = vf.instance, 0)
		FROM version_file vf LEFT JOIN content c ON c.id = vf.content_id
		LEFT JOIN link vf_link ON vf_link.path = vf.path AND vf_link.state = 'active' AND vf_link.kind = 'symlink'
		LEFT JOIN unique_target_hash uth ON uth.target = vf_link.target
		LEFT JOIN version_source vs ON vs.version_id = vf.version_id
		WHERE vf.version_id IN (`+in+`) ORDER BY vf.version_id, vf.instance`, targetArgs, func(r *sql.Rows) error {
		var vid int64
		var f File
		var langs string
		var score, added sql.NullInt64
		if err := r.Scan(&vid, &f.ID, &f.Instance, &f.ArrFileID, &f.Path, &f.RelativePath, &f.SizeBytes, &f.Quality,
			&f.QualityRevision, &f.ReleaseGroup, &f.SceneName, &f.Edition, &langs, &score, &f.CutoffNotMet, &added,
			&f.ReleaseTitle, &f.InfoHash, &f.Current); err != nil {
			return err
		}
		f.Languages = jsonStrings(langs)
		f.CustomFormatScore, f.AddedAt = nullInt(score), nullTS(added)
		v := &out[idx[vid]]
		v.Files = append(v.Files, f)
		return nil
	})
	if err != nil {
		return nil, err
	}
	// A version with one file and no owner: that file is current.
	for i := range out {
		if len(out[i].Files) == 1 && out[i].Source == "" {
			out[i].Files[0].Current = true
		}
	}
	err = scanAll(ctx, db, uniqueLinkedTargetHash+`SELECT lv.version_id, l.id, l.path, l.target, l.kind, l.state, COALESCE(l.release_title, ''),
		COALESCE(l.quality, ''), l.score, l.title_id, COALESCE(c.info_hash, uth.info_hash, '')
		FROM link_version lv JOIN link l ON l.id = lv.link_id
		LEFT JOIN content_file cf ON cf.id = l.content_file_id LEFT JOIN content c ON c.id = cf.content_id
		LEFT JOIN unique_target_hash uth ON uth.target = l.target AND l.state = 'active' AND l.kind = 'symlink'
		WHERE lv.version_id IN (`+in+`) ORDER BY l.path`, targetArgs, func(r *sql.Rows) error {
		var vid int64
		var l Link
		var score sql.NullInt64
		if err := r.Scan(&vid, &l.ID, &l.Path, &l.Target, &l.Kind, &l.State, &l.ReleaseTitle, &l.Quality, &score,
			&l.TitleID, &l.InfoHash); err != nil {
			return err
		}
		l.Score = nullInt(score)
		v := &out[idx[vid]]
		v.Links = append(v.Links, l)
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = scanAll(ctx, db, `SELECT a.version_id, a.instance, a.arr_id, a.monitored, COALESCE(p.name, ''), a.status,
		a.path, a.tags FROM arr_title a LEFT JOIN arr_quality_profile p ON p.instance = a.instance AND p.arr_id = a.quality_profile_id
		WHERE a.version_id IN (`+in+`) ORDER BY a.instance`, ids, func(r *sql.Rows) error {
		var vid int64
		var a ArrRec
		var tags string
		if err := r.Scan(&vid, &a.Instance, &a.ArrID, &a.Monitored, &a.QualityProfile, &a.Status, &a.Path, &tags); err != nil {
			return err
		}
		a.Tags = jsonStrings(tags)
		v := &out[idx[vid]]
		v.ArrRecords = append(v.ArrRecords, a)
		return nil
	})
	return out, err
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
	conds := []string{"1=1"}
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

// Content is a registry entry by info hash.
type Content struct {
	ID          int64      `json:"id"`
	InfoHash    string     `json:"info_hash"`
	Name        string     `json:"name"`
	MountFolder string     `json:"mount_folder,omitempty"`
	SizeBytes   *int64     `json:"size_bytes,omitempty"`
	State       string     `json:"state"`
	FirstSeenAt time.Time  `json:"first_seen_at"`
	LastSeenAt  time.Time  `json:"last_seen_at"`
	Providers   []Provider `json:"providers"`
	Files       []CFile    `json:"files"`
	Versions    []CVersion `json:"versions"`
	ArrFiles    []ArrFile  `json:"arr_files"`
	Links       []Link     `json:"links"`
	History     []Event    `json:"history"`
}

// Provider is where a content is held.
type Provider struct {
	Provider   string    `json:"provider"`
	TorrentID  string    `json:"torrent_id,omitempty"`
	FolderName string    `json:"folder_name,omitempty"`
	SeenAt     time.Time `json:"seen_at"`
}

// CFile is a file inside a content.
type CFile struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	Probed    bool   `json:"probed"`
}

// CVersion is a version a content was grabbed or imported for.
type CVersion struct {
	TitleID      int64      `json:"title_id"`
	TitleName    string     `json:"title_name"`
	VersionID    int64      `json:"version_id"`
	VersionKey   string     `json:"version_key"`
	ReleaseTitle string     `json:"release_title"`
	Source       string     `json:"source"`
	GrabbedAt    *time.Time `json:"grabbed_at,omitempty"`
	ImportedAt   *time.Time `json:"imported_at,omitempty"`
	FailedAt     *time.Time `json:"failed_at,omitempty"`
}

// ArrFile is an arr file that came from a content.
type ArrFile struct {
	TitleID    int64  `json:"title_id"`
	VersionKey string `json:"version_key"`
	Instance   string `json:"instance"`
	Path       string `json:"path"`
	Quality    string `json:"quality"`
}

// Event is an arr history event for a hash.
type Event struct {
	Instance    string    `json:"instance"`
	EventType   string    `json:"event_type"`
	Date        time.Time `json:"date"`
	TitleID     *int64    `json:"title_id,omitempty"`
	SourceTitle string    `json:"source_title"`
	Quality     string    `json:"quality"`
	Score       *int64    `json:"custom_format_score,omitempty"`
}

func normHash(h string) string { return strings.ToLower(strings.TrimSpace(h)) }

func validHash(h string) bool {
	if len(h) != 40 {
		return false
	}
	for _, c := range h {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (w *Web) getContent(ctx context.Context, hash string) (*Content, error) {
	hash = normHash(hash)
	db := w.st.Reader()
	c := &Content{Providers: []Provider{}, Files: []CFile{}, Versions: []CVersion{}, ArrFiles: []ArrFile{},
		Links: []Link{}, History: []Event{}}
	var mount sql.NullString
	var size sql.NullInt64
	var first, last int64
	err := db.QueryRowContext(ctx, `SELECT id, info_hash, name, mount_folder, size_bytes, state, first_seen_at,
		last_seen_at FROM content WHERE info_hash = ?`, hash).Scan(&c.ID, &c.InfoHash, &c.Name, &mount, &size, &c.State,
		&first, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	c.MountFolder, c.SizeBytes = mount.String, nullInt(size)
	c.FirstSeenAt, c.LastSeenAt = ts(first), ts(last)
	steps := []struct {
		q    string
		scan func(*sql.Rows) error
	}{
		{`SELECT provider, COALESCE(torrent_id, ''), COALESCE(folder_name, ''), seen_at FROM content_provider
			WHERE content_id = ? ORDER BY provider`, func(r *sql.Rows) error {
			var p Provider
			var seen int64
			if err := r.Scan(&p.Provider, &p.TorrentID, &p.FolderName, &seen); err != nil {
				return err
			}
			p.SeenAt = ts(seen)
			c.Providers = append(c.Providers, p)
			return nil
		}},
		{`SELECT path, size_bytes, probe IS NOT NULL FROM content_file WHERE content_id = ? ORDER BY path`,
			func(r *sql.Rows) error {
				var f CFile
				if err := r.Scan(&f.Path, &f.SizeBytes, &f.Probed); err != nil {
					return err
				}
				c.Files = append(c.Files, f)
				return nil
			}},
		{`SELECT t.id, t.name, v.id, v.policy_key, cv.release_title, cv.source, cv.grabbed_at, cv.imported_at, cv.failed_at
			FROM content_version cv JOIN version v ON v.id = cv.version_id JOIN title t ON t.id = v.title_id
			WHERE cv.content_id = ? ORDER BY t.name, v.policy_key`, func(r *sql.Rows) error {
			var v CVersion
			var g, i, f sql.NullInt64
			if err := r.Scan(&v.TitleID, &v.TitleName, &v.VersionID, &v.VersionKey, &v.ReleaseTitle, &v.Source,
				&g, &i, &f); err != nil {
				return err
			}
			v.GrabbedAt, v.ImportedAt, v.FailedAt = nullTS(g), nullTS(i), nullTS(f)
			c.Versions = append(c.Versions, v)
			return nil
		}},
		{`SELECT vf.title_id, v.policy_key, vf.instance, vf.path, vf.quality FROM version_file vf
			JOIN version v ON v.id = vf.version_id WHERE vf.content_id = ? ORDER BY vf.path`, func(r *sql.Rows) error {
			var a ArrFile
			if err := r.Scan(&a.TitleID, &a.VersionKey, &a.Instance, &a.Path, &a.Quality); err != nil {
				return err
			}
			c.ArrFiles = append(c.ArrFiles, a)
			return nil
		}},
		{`SELECT l.id, l.path, l.target, l.kind, l.state, COALESCE(l.release_title, ''), COALESCE(l.quality, ''),
			l.score, l.title_id FROM link l JOIN content_file cf ON cf.id = l.content_file_id
			WHERE cf.content_id = ? ORDER BY l.path`, func(r *sql.Rows) error {
			var l Link
			var score sql.NullInt64
			if err := r.Scan(&l.ID, &l.Path, &l.Target, &l.Kind, &l.State, &l.ReleaseTitle, &l.Quality, &score,
				&l.TitleID); err != nil {
				return err
			}
			l.Score, l.InfoHash = nullInt(score), c.InfoHash
			c.Links = append(c.Links, l)
			return nil
		}},
	}
	for _, s := range steps {
		if err := scanAll(ctx, db, s.q, []any{c.ID}, s.scan); err != nil {
			return nil, err
		}
	}
	err = scanAll(ctx, db, `SELECT instance, event_type, date_at, title_id, source_title, quality, custom_format_score
		FROM arr_history WHERE info_hash = ? ORDER BY date_at DESC LIMIT 200`, []any{hash}, func(r *sql.Rows) error {
		var e Event
		var d int64
		var tid, score sql.NullInt64
		if err := r.Scan(&e.Instance, &e.EventType, &d, &tid, &e.SourceTitle, &e.Quality, &score); err != nil {
			return err
		}
		e.Date, e.TitleID, e.Score = ts(d), nullInt(tid), nullInt(score)
		c.History = append(c.History, e)
		return nil
	})
	return c, err
}

// ContentRow is a registry list row.
type ContentRow struct {
	InfoHash   string    `json:"info_hash"`
	Name       string    `json:"name"`
	State      string    `json:"state"`
	Versions   int       `json:"versions"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

// listContent lists registry contents, newest first; q matches a hash prefix or a name substring.
func (w *Web) listContent(ctx context.Context, q, state string, limit, offset int) ([]ContentRow, int, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	conds := []string{"1=1"}
	var args []any
	if q != "" {
		conds = append(conds, "(c.info_hash LIKE ? ESCAPE '\\' OR c.name LIKE ? ESCAPE '\\')")
		args = append(args, likeEscape(normHash(q))+"%", "%"+likeEscape(q)+"%")
	}
	if state != "" {
		conds = append(conds, "c.state = ?")
		args = append(args, state)
	}
	where := " WHERE " + strings.Join(conds, " AND ")
	var total int
	if err := w.st.Reader().QueryRowContext(ctx, "SELECT COUNT(*) FROM content c"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	out := []ContentRow{}
	err := scanAll(ctx, w.st.Reader(), `SELECT c.info_hash, c.name, c.state,
		(SELECT COUNT(*) FROM content_version cv WHERE cv.content_id = c.id), c.last_seen_at FROM content c`+where+`
		ORDER BY c.last_seen_at DESC, c.id DESC LIMIT ? OFFSET ?`, append(args, limit, max(offset, 0)),
		func(r *sql.Rows) error {
			var c ContentRow
			var last int64
			if err := r.Scan(&c.InfoHash, &c.Name, &c.State, &c.Versions, &last); err != nil {
				return err
			}
			c.LastSeenAt = ts(last)
			out = append(out, c)
			return nil
		})
	return out, total, err
}

// ImportRun is one theatarr import-arr run.
type ImportRun struct {
	ID         int64           `json:"id"`
	Instance   string          `json:"instance"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt time.Time       `json:"finished_at"`
	Summary    json.RawMessage `json:"summary"`
}

// Took is the run's duration.
func (r ImportRun) Took() time.Duration { return r.FinishedAt.Sub(r.StartedAt) }

func (w *Web) listImports(ctx context.Context, instance string, limit int) ([]ImportRun, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	cond, args := "", []any{}
	if instance != "" {
		cond, args = " WHERE instance = ?", append(args, instance)
	}
	out := []ImportRun{}
	err := scanAll(ctx, w.st.Reader(), `SELECT id, instance, started_at, finished_at, summary FROM import_run`+cond+`
		ORDER BY started_at DESC, id DESC LIMIT ?`, append(args, limit), func(r *sql.Rows) error {
		var run ImportRun
		var s, f int64
		var sum string
		if err := r.Scan(&run.ID, &run.Instance, &s, &f, &sum); err != nil {
			return err
		}
		run.StartedAt, run.FinishedAt = ts(s), ts(f)
		if !json.Valid([]byte(sum)) {
			sum = "{}"
		}
		run.Summary = json.RawMessage(sum)
		out = append(out, run)
		return nil
	})
	return out, err
}

func (w *Web) getImport(ctx context.Context, id int64) (*ImportRun, error) {
	var run ImportRun
	var s, f int64
	var sum string
	err := w.st.Reader().QueryRowContext(ctx, `SELECT id, instance, started_at, finished_at, summary FROM import_run
		WHERE id = ?`, id).Scan(&run.ID, &run.Instance, &s, &f, &sum)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	run.StartedAt, run.FinishedAt = ts(s), ts(f)
	if !json.Valid([]byte(sum)) {
		sum = "{}"
	}
	run.Summary = json.RawMessage(sum)
	return &run, nil
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
	RetryOf     int64      `json:"retry_of,omitempty"`
	RetriedAs   int64      `json:"retried_as,omitempty"`
	ResolvedBy  int64      `json:"resolved_by,omitempty"`
	DismissedAt *time.Time `json:"dismissed_at,omitempty"`
	DismissedBy string     `json:"dismissed_by,omitempty"`
	Attention   bool       `json:"attention,omitempty"`
}

func jobFromStatus(s jobs.Status) Job {
	return Job{ID: s.ID, Kind: s.Kind, State: s.State, Priority: s.Priority, Attempts: s.Attempts,
		MaxAttempts: s.MaxAttempts, RunAt: s.RunAt, LastError: s.LastError, DedupeKey: s.DedupeKey,
		CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, FinishedAt: s.FinishedAt, RetryOf: s.RetryOf,
		RetriedAs: s.RetriedAs, ResolvedBy: s.ResolvedBy, DismissedAt: s.DismissedAt, DismissedBy: s.DismissedBy,
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
	counts["attention"], err = w.jobs.AttentionCount(ctx)
	return counts, err
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
	for _, t := range []string{"title", "version", "version_file", "content", "link", "import_run", "job"} {
		var n int
		if err := w.st.Reader().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+t).Scan(&n); err != nil {
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

func jsonStrings(s string) []string {
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil || out == nil {
		// languages may be objects ({"name": ...}) in older rows; fall back to names.
		var objs []map[string]any
		if json.Unmarshal([]byte(s), &objs) == nil {
			for _, o := range objs {
				if n, ok := o["name"].(string); ok {
					out = append(out, n)
				}
			}
		}
	}
	if out == nil {
		out = []string{}
	}
	return out
}
