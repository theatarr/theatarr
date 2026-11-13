package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/theatarr/theatarr/internal/arr"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Radarr and Sonarr lists (docs/design/arrview.md, Lists): the titles of an instance, read with the read-only arr
// client, added to the list's chosen libraries that fit them. Nothing is written to the instance, and nothing but the
// title's ids, name, year and series type is taken from it.

// ArrListInterval is how often Radarr and Sonarr lists sync: Radarr's own Radarr list refreshes at most every 15
// minutes (RadarrImport.MinRefreshInterval).
const ArrListInterval = 15 * time.Minute

// ArrReader is the part of the arr client a Radarr or Sonarr list reads with (arr.Client).
type ArrReader interface {
	GetJSON(ctx context.Context, endpoint string, q url.Values, v any) error
}

type arrMovie struct {
	TmdbID int64  `json:"tmdbId"`
	Title  string `json:"title"`
	Year   int    `json:"year"`
}

type arrSeries struct {
	TvdbID     int64  `json:"tvdbId"`
	TmdbID     int64  `json:"tmdbId"`
	ImdbID     string `json:"imdbId"`
	Title      string `json:"title"`
	Year       int    `json:"year"`
	SeriesType string `json:"seriesType"`
}

func (s *MovieIntake) arrReader(baseURL, key string) (ArrReader, error) {
	if s.Arr != nil {
		return s.Arr(baseURL, key)
	}
	c, err := arr.New(baseURL, key, arr.Options{})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// readArr reads the instance's movie or series list into v (the arr client masks the key in its errors).
func (s *MovieIntake) readArr(ctx context.Context, cfg movieIntakeConfig, v any) error {
	c, err := s.arrReader(cfg.url, cfg.apiKey)
	if err != nil {
		return err
	}
	endpoint := "movie"
	if cfg.listType == "sonarr" {
		endpoint = "series"
	}
	return c.GetJSON(ctx, endpoint, nil, v)
}

// syncRadarr adds the instance's movies through addMovie, so every movie list rule holds: exclusions, a library row
// that exists is left as it is, TMDb details for a new movie, availability, search only for monitored adds.
func (s *MovieIntake) syncRadarr(ctx context.Context, cfg movieIntakeConfig, out *IntakeResult) (soft []error) {
	var movies []arrMovie
	if err := s.readArr(ctx, cfg, &movies); err != nil {
		return []error{fmt.Errorf("list %d: %w", cfg.id, err)}
	}
	for _, m := range movies {
		if m.TmdbID <= 0 {
			continue
		}
		n, err := s.addMovie(ctx, tmdb.MovieSummary{ID: int(m.TmdbID), Title: m.Title}, cfg, 0)
		if err != nil {
			soft = append(soft, fmt.Errorf("list %d movie %d: %w", cfg.id, m.TmdbID, err))
			continue
		}
		if n {
			out.Added++
		} else {
			out.Skipped++
		}
	}
	return soft
}

func (s *MovieIntake) syncSonarr(ctx context.Context, cfg movieIntakeConfig, out *IntakeResult) (soft []error) {
	var series []arrSeries
	if err := s.readArr(ctx, cfg, &series); err != nil {
		return []error{fmt.Errorf("list %d: %w", cfg.id, err)}
	}
	for _, it := range series {
		if it.TvdbID <= 0 {
			continue
		}
		_, n, err := s.addSeries(ctx, it, cfg)
		if err != nil {
			soft = append(soft, fmt.Errorf("list %d series %d: %w", cfg.id, it.TvdbID, err))
			continue
		}
		if n {
			out.Added++
		} else {
			out.Skipped++
		}
	}
	return soft
}

// addSeries adds a Sonarr list's series to the list's libraries that fit it and that it has no row in (a row in any
// state is a choice already made). Monitoring is Theatarr's own for a series added to a library: the list's monitor
// says only whether it is monitored at all; the seasons monitored in the instance are not copied. titleID is the
// series' id when added.
func (s *MovieIntake) addSeries(ctx context.Context, it arrSeries, cfg movieIntakeConfig) (int64, bool, error) {
	var keys []string
	if err := json.Unmarshal([]byte(cfg.versions), &keys); err != nil {
		return 0, false, err
	}
	now := s.Now()
	var titleID int64
	var added []string
	err := s.Store.WriteTx(ctx, func(tx *sql.Tx) error {
		titleID, added = 0, nil
		id, stored, found, err := find(ctx, tx, Input{Kind: policy.KindSeries, TvdbID: it.TvdbID, TmdbID: it.TmdbID})
		if err != nil {
			return err
		}
		// A stored title keeps its own anime flag; the instance's series type decides only for a new one.
		anime := stored
		if !found {
			anime = it.SeriesType == "anime"
		}
		fit, _, err := s.fitVersions(cfg, keys, anime, policy.KindSeries)
		if err != nil {
			return err
		}
		missing, err := missingVersions(ctx, tx, id, found, fit)
		if err != nil || len(missing) == 0 {
			return err
		}
		// The instance's ids go along for a stored title too: Add fills the ones it lacks (a series found by TMDb id
		// gets its TVDB id for the episode refresh) and never replaces stored ones.
		in := Input{Kind: policy.KindSeries, TvdbID: it.TvdbID, TmdbID: it.TmdbID, ImdbID: it.ImdbID, Title: it.Title,
			Year: it.Year, Versions: func(bool) ([]string, error) { return missing, nil }, Monitored: cfg.monitored,
			Source: "series-list", AddMethod: "list"}
		if found {
			in.TitleID = id
		} else {
			in.Anime, in.SeriesType = &anime, it.SeriesType
		}
		res, err := Add(ctx, tx, in, now, s.Log)
		if err != nil {
			return err
		}
		titleID, added = res.TitleID, missing
		views := []string{cfg.view}
		if cfg.view == "" {
			views = nil
			for _, k := range missing {
				views = append(views, "v/"+k)
			}
		}
		for _, view := range views {
			if _, err := tx.ExecContext(ctx, `INSERT INTO arrview_title(view,title_id,quality_profile_id,root_folder_path,added_at,updated_at) VALUES(?,?,?,?,?,?)
				ON CONFLICT DO NOTHING`, view, titleID, cfg.profile, cfg.root, store.Millis(now), store.Millis(now)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || len(added) == 0 {
		return 0, false, err
	}
	if cfg.search && cfg.monitored && s.OnAdd != nil {
		return titleID, true, s.OnAdd(ctx, titleID, added, cfg.later)
	}
	return titleID, true, nil
}

// missingVersions are the keys of fit the title (found or not) has no version row for.
func missingVersions(ctx context.Context, q Querier, titleID int64, found bool, fit []string) ([]string, error) {
	if !found {
		return fit, nil
	}
	var out []string
	for _, k := range fit {
		var has bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM version WHERE title_id=? AND policy_key=?)`,
			titleID, k).Scan(&has); err != nil {
			return nil, err
		}
		if !has {
			out = append(out, k)
		}
	}
	return out, nil
}

// ArrCheck is what Check reads from an instance.
type ArrCheck struct {
	App     string `json:"app"`
	Version string `json:"version"`
	// Titles are the instance's movies (Radarr) or series (Sonarr).
	Titles int `json:"titles"`
}

// CheckArrList reads the status and titles of the Radarr (listType radarr) or Sonarr instance at baseURL.
func (s *MovieIntake) CheckArrList(ctx context.Context, listType, baseURL, key string) (ArrCheck, error) {
	var out ArrCheck
	want := map[string]string{"radarr": "Radarr", "sonarr": "Sonarr"}[listType]
	if want == "" {
		return out, fmt.Errorf("list type %q is not radarr or sonarr", listType)
	}
	c, err := s.arrReader(baseURL, key)
	if err != nil {
		return out, err
	}
	var st struct {
		AppName string `json:"appName"`
		Version string `json:"version"`
	}
	if err := c.GetJSON(ctx, "system/status", nil, &st); err != nil {
		return out, err
	}
	if st.AppName != want {
		return out, fmt.Errorf("this address answers as %q, not %s", st.AppName, want)
	}
	var titles []json.RawMessage
	endpoint := "movie"
	if listType == "sonarr" {
		endpoint = "series"
	}
	if err := c.GetJSON(ctx, endpoint, nil, &titles); err != nil {
		return out, err
	}
	return ArrCheck{App: st.AppName, Version: st.Version, Titles: len(titles)}, nil
}

// ListPreview is what a sync of a list would do now, without writing anything.
type ListPreview struct {
	// AddTitle are titles not in Theatarr yet; AddLibrary titles that would be added to a library they are not in.
	AddTitle   int `json:"add_title"`
	AddLibrary int `json:"add_library"`
	// Present are titles every fitting library already has (in any state, removed or unmonitored included).
	Present  int `json:"present"`
	Excluded int `json:"excluded"`
	// Skipped are titles of a Radarr or Sonarr list that fit none of the list's libraries.
	Skipped int `json:"skipped"`
	// Titles are the first 200 titles to add and, apart from them, the first 200 skipped ones.
	Titles []PreviewTitle `json:"titles"`
	Note   string         `json:"note,omitempty"`
}

// PreviewTitle is one title a sync would change, or skip.
type PreviewTitle struct {
	Name   string `json:"name"`
	Year   int    `json:"year,omitempty"`
	TmdbID int64  `json:"tmdb_id,omitempty"`
	TvdbID int64  `json:"tvdb_id,omitempty"`
	// Change: add_title, add_library or skipped.
	Change string `json:"change"`
}

// previewTitles caps the titles to add, and apart from them the skipped ones, in ListPreview.Titles.
const previewTitles = 200

// PreviewList reads the list's titles as a sync does and classifies each without writing and without TMDb detail
// calls. sql.ErrNoRows when there is no such list.
func (s *MovieIntake) PreviewList(ctx context.Context, id int64) (ListPreview, error) {
	out := ListPreview{Titles: []PreviewTitle{}}
	cfg, err := s.config(ctx, listConfigs+` WHERE id=?`, id)
	if err != nil {
		return out, err
	}
	var keys []string
	if err := json.Unmarshal([]byte(cfg.versions), &keys); err != nil {
		return out, err
	}
	note := func(t PreviewTitle) {
		switch t.Change {
		case "add_title":
			out.AddTitle++
		case "add_library":
			out.AddLibrary++
		case "present":
			out.Present++
		case "excluded":
			out.Excluded++
		case "skipped":
			out.Skipped++
		}
		// The titles to add and the skipped ones are capped apart, so a long list still names what it skips.
		if n := map[string]int{"add_title": out.AddTitle + out.AddLibrary, "add_library": out.AddTitle + out.AddLibrary,
			"skipped": out.Skipped}[t.Change]; n > 0 && n <= previewTitles {
			out.Titles = append(out.Titles, t)
		}
	}
	switch cfg.listType {
	case "sonarr":
		var series []arrSeries
		if err := s.readArr(ctx, cfg, &series); err != nil {
			return out, err
		}
		for _, it := range series {
			if it.TvdbID <= 0 {
				continue
			}
			t := PreviewTitle{Name: it.Title, Year: it.Year, TmdbID: it.TmdbID, TvdbID: it.TvdbID}
			if t.Change, _, err = s.previewSeries(ctx, cfg, keys, it); err != nil {
				return out, err
			}
			note(t)
		}
		return out, nil
	case "radarr":
		var movies []arrMovie
		if err := s.readArr(ctx, cfg, &movies); err != nil {
			return out, err
		}
		for _, m := range movies {
			if m.TmdbID <= 0 {
				continue
			}
			t := PreviewTitle{Name: m.Title, Year: m.Year, TmdbID: m.TmdbID}
			if t.Change, _, err = s.previewMovie(ctx, cfg, keys, m.TmdbID); err != nil {
				return out, err
			}
			note(t)
		}
	default:
		l, err := s.TMDb.List(ctx, int(cfg.tmdbID))
		if err != nil {
			return out, err
		}
		for _, m := range l.Items {
			if m.ID <= 0 || m.MediaType != "" && m.MediaType != "movie" {
				continue
			}
			t := PreviewTitle{Name: m.Title, TmdbID: int64(m.ID)}
			if d, err := time.Parse(time.DateOnly, m.ReleaseDate); err == nil {
				t.Year = d.Year()
			}
			if t.Change, _, err = s.previewMovie(ctx, cfg, keys, int64(m.ID)); err != nil {
				return out, err
			}
			note(t)
		}
	}
	out.Note = "A movie not in Theatarr yet is counted here as not anime; Sync now decides it from TMDb."
	return out, nil
}

// previewMovie and previewSeries classify a title as a sync would (previewChange); titleID is its id in Theatarr,
// 0 when it has none (or is excluded).
func (s *MovieIntake) previewMovie(ctx context.Context, cfg movieIntakeConfig, keys []string, tmdbID int64) (
	change string, titleID int64, err error) {
	r := s.Store.Reader()
	var excluded bool
	if err := r.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM movie_exclusion WHERE tmdb_id=?)`, tmdbID).
		Scan(&excluded); err != nil || excluded {
		return "excluded", 0, err
	}
	var anime bool
	err = r.QueryRowContext(ctx, `SELECT id, anime FROM title WHERE kind='movie' AND tmdb_id=?`, tmdbID).Scan(&titleID,
		&anime)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", 0, err
	}
	change, err = s.previewChange(ctx, cfg, keys, titleID, err == nil, anime, policy.KindMovie)
	return change, titleID, err
}

func (s *MovieIntake) previewSeries(ctx context.Context, cfg movieIntakeConfig, keys []string, it arrSeries) (
	change string, titleID int64, err error) {
	r := s.Store.Reader()
	var id int64
	var anime bool
	found := false
	for _, q := range []struct {
		col string
		v   int64
	}{{"tvdb_id", it.TvdbID}, {"tmdb_id", it.TmdbID}} {
		if q.v <= 0 {
			continue
		}
		err := r.QueryRowContext(ctx, `SELECT id, anime FROM title WHERE kind='series' AND `+q.col+`=?`, q.v).Scan(&id, &anime)
		if err == nil {
			found = true
			break
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", 0, err
		}
	}
	if !found {
		anime = it.SeriesType == "anime"
	}
	change, err = s.previewChange(ctx, cfg, keys, id, found, anime, policy.KindSeries)
	return change, id, err
}

func (s *MovieIntake) previewChange(ctx context.Context, cfg movieIntakeConfig, keys []string, titleID int64, found,
	anime bool, kind policy.Kind) (string, error) {
	fit, _, err := s.fitVersions(cfg, keys, anime, kind)
	if err != nil || len(fit) == 0 {
		return "skipped", nil
	}
	missing, err := missingVersions(ctx, s.Store.Reader(), titleID, found, fit)
	switch {
	case err != nil:
		return "", err
	case len(missing) == 0:
		return "present", nil
	case !found:
		return "add_title", nil
	}
	return "add_library", nil
}
