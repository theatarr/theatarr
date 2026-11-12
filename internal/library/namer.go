package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/theatarr/theatarr/internal/library/naming"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// VersionNaming is how one version names its files.
type VersionNaming struct {
	Config naming.Config
	// QualityTitles maps a quality name to the instance's definition title (from_arr); a missing name is its own
	// title, as in the arrs' defaults.
	QualityTitles map[string]string
	// Source is "policy" or "from_arr <instance>".
	Source string
}

// QualityTitle returns the definition title for a quality name.
func (n *VersionNaming) QualityTitle(name string) string {
	if t := n.QualityTitles[name]; t != "" {
		return t
	}
	return name
}

var colonByName = map[string]naming.ColonReplacement{"delete": naming.ColonDelete, "dash": naming.ColonDash,
	"space_dash": naming.ColonSpaceDash, "space_dash_space": naming.ColonSpaceDashSpace, "smart": naming.ColonSmart}

var multiByName = map[string]naming.MultiEpisodeStyle{"extend": naming.MultiExtend, "duplicate": naming.MultiDuplicate,
	"repeat": naming.MultiRepeat, "scene": naming.MultiScene, "range": naming.MultiRange,
	"prefixed_range": naming.MultiPrefixedRange}

// NamingFor returns a version's naming: the from_arr instance's config stored in library_naming, or the policy's
// templates over the flavour's defaults with renaming on.
func NamingFor(ctx context.Context, db *sql.DB, pol *policy.Policy, version string) (*VersionNaming, error) {
	vp := pol.Versions[version]
	if vp == nil {
		return nil, fmt.Errorf("library: unknown version %q", version)
	}
	n := vp.Naming
	if n.FromVersion != "" {
		// A parallel version naming like the version it follows (validation keeps this one level deep).
		if n.FromVersion == version || pol.Versions[n.FromVersion] == nil || pol.Versions[n.FromVersion].Naming.FromVersion != "" {
			return nil, fmt.Errorf("library: version %s names from version %q, which has no naming of its own", version, n.FromVersion)
		}
		vn, err := NamingFor(ctx, db, pol, n.FromVersion)
		if err != nil {
			return nil, err
		}
		vn.Source = "from_version " + n.FromVersion + " (" + vn.Source + ")"
		return vn, nil
	}
	if n.FromArr != "" {
		var body, quals string
		err := db.QueryRowContext(ctx, `SELECT naming, qualities FROM library_naming WHERE instance = ?`, n.FromArr).Scan(&body, &quals)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("library: version %s names from %s, whose naming has not been read yet (theatarr library naming-sync)", version, n.FromArr)
		}
		if err != nil {
			return nil, err
		}
		cfg, err := naming.ParseArrConfig([]byte(body))
		if err != nil {
			return nil, err
		}
		vn := &VersionNaming{Config: cfg, QualityTitles: map[string]string{}, Source: "from_arr " + n.FromArr}
		_ = json.Unmarshal([]byte(quals), &vn.QualityTitles)
		return vn, nil
	}
	var cfg naming.Config
	if vp.Kind == policy.KindMovie {
		cfg = naming.DefaultMovieConfig()
		if n.File != "" {
			cfg.StandardMovieFormat = n.File
		}
		if n.Folder != "" {
			cfg.MovieFolderFormat = n.Folder
		}
	} else {
		cfg = naming.DefaultSeriesConfig()
		set := func(dst *string, v string) {
			if v != "" {
				*dst = v
			}
		}
		set(&cfg.StandardEpisodeFormat, n.File)
		set(&cfg.SeriesFolderFormat, n.Folder)
		set(&cfg.SeasonFolderFormat, n.Season)
		set(&cfg.SpecialsFolderFormat, n.Specials)
		set(&cfg.DailyEpisodeFormat, n.Daily)
		set(&cfg.AnimeEpisodeFormat, n.Anime)
		if n.MultiEpisode != "" {
			cfg.MultiEpisodeStyle = multiByName[n.MultiEpisode]
		}
	}
	cfg.Rename = true
	if n.Colon != "" {
		cfg.ColonReplacement = colonByName[n.Colon]
	}
	return &VersionNaming{Config: cfg, Source: "policy"}, nil
}

// DefaultTemplates gives the built-in template NamingFor uses for each naming field a version of kind leaves empty:
// folder and file, and for series also season, specials, daily and anime.
func DefaultTemplates(kind policy.Kind) map[string]string {
	if kind == policy.KindMovie {
		c := naming.DefaultMovieConfig()
		return map[string]string{"folder": c.MovieFolderFormat, "file": c.StandardMovieFormat}
	}
	c := naming.DefaultSeriesConfig()
	return map[string]string{"folder": c.SeriesFolderFormat, "file": c.StandardEpisodeFormat,
		"season": c.SeasonFolderFormat, "specials": c.SpecialsFolderFormat, "daily": c.DailyEpisodeFormat,
		"anime": c.AnimeEpisodeFormat}
}

// ArrGetter is the read-only arr client (internal/arr.Client).
type ArrGetter interface {
	GetJSON(ctx context.Context, endpoint string, query url.Values, v any) error
}

// SyncNaming reads config/naming and qualitydefinition from every importing instance (GET only) and
// stores them in library_naming. getters maps instance names to clients; instances without one are skipped.
func SyncNaming(ctx context.Context, st *store.Store, pol *policy.Policy, getters map[string]ArrGetter, now time.Time) ([]string, error) {
	var done []string
	for _, inst := range pol.ArrFedInstances() {
		g := getters[inst]
		if g == nil {
			continue
		}
		var body json.RawMessage
		if err := g.GetJSON(ctx, "config/naming", nil, &body); err != nil {
			return done, fmt.Errorf("library: %s config/naming: %w", inst, err)
		}
		if _, err := naming.ParseArrConfig(body); err != nil {
			return done, fmt.Errorf("library: %s config/naming: %w", inst, err)
		}
		var defs []struct {
			Title   string `json:"title"`
			Quality struct {
				Name string `json:"name"`
			} `json:"quality"`
		}
		if err := g.GetJSON(ctx, "qualitydefinition", nil, &defs); err != nil {
			return done, fmt.Errorf("library: %s qualitydefinition: %w", inst, err)
		}
		titles := map[string]string{}
		for _, d := range defs {
			if d.Title != "" && d.Title != d.Quality.Name {
				titles[d.Quality.Name] = d.Title
			}
		}
		qb, _ := json.Marshal(titles)
		err := st.WriteTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO library_naming (instance, naming, qualities, fetched_at) VALUES (?, ?, ?, ?)
				ON CONFLICT (instance) DO UPDATE SET naming = excluded.naming, qualities = excluded.qualities,
				fetched_at = excluded.fetched_at`, inst, string(body), string(qb), store.Millis(now))
			return err
		})
		if err != nil {
			return done, err
		}
		done = append(done, inst)
	}
	return done, nil
}
