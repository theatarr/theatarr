package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/availability"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// MovieMetadataSource is the gateway-backed TMDb client used by movie intake.
type MovieMetadataSource interface {
	Collection(context.Context, int) (*tmdb.Collection, error)
	List(context.Context, int) (*tmdb.List, error)
	MovieDetails(context.Context, int) (*tmdb.Movie, error)
}

// MovieIntake refreshes collections and lists through the shared catalog add path.
type MovieIntake struct {
	Store  *store.Store
	Policy *policy.Policy
	TMDb   MovieMetadataSource
	Now    func() time.Time
	Log    *slog.Logger
	// OnAdd starts the existing add-time search after commit, only for monitored, available movies.
	OnAdd func(context.Context, int64, []string) error

	skipLogged sync.Map // collection targets skipped, logged once: "tmdb id:keys"
}

type IntakeResult struct{ Collections, Lists, Added, Skipped int }

// Run refreshes both intakes at startup and on the policy interval until cancellation.
func (s *MovieIntake) Run(ctx context.Context, interval time.Duration) {
	if err := s.init(); err != nil {
		slog.Default().Warn("movie intake unavailable", "err", err)
		return
	}
	if interval <= 0 {
		interval = policy.DefaultListInterval
	}
	refresh := func() {
		if _, err := s.RefreshCollections(ctx); err != nil && ctx.Err() == nil {
			s.Log.Warn("movie collections: refresh failed", "err", err)
		}
		if _, err := s.SyncLists(ctx); err != nil && ctx.Err() == nil {
			s.Log.Warn("movie lists: sync failed", "err", err)
		}
	}
	refresh()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}

func (s *MovieIntake) init() error {
	if s.Store == nil || s.Policy == nil || s.TMDb == nil {
		return errors.New("catalog: movie intake needs store, policy and TMDb")
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	if s.Log == nil {
		s.Log = slog.Default()
	}
	return nil
}

// DiscoverCollections exposes imported collection memberships without enabling intake implicitly.
func DiscoverCollections(ctx context.Context, st *store.Store, now time.Time) error {
	_, err := st.Writer().ExecContext(ctx, `INSERT INTO movie_collection (tmdb_id, versions, updated_at)
  SELECT CAST(x.external_id AS INTEGER), json_group_array(DISTINCT v.policy_key), ?
  FROM title_external_id x JOIN title t ON t.id=x.title_id JOIN version v ON v.title_id=t.id
  WHERE x.source='tmdb_collection' AND t.kind='movie' AND CAST(x.external_id AS INTEGER)>0
  AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id=v.id)
  GROUP BY x.external_id ON CONFLICT(tmdb_id) DO NOTHING`, store.Millis(now))
	return err
}

type movieIntakeConfig struct {
	id, tmdbID                             int64
	versions, minimum, monitor, view, root string
	profile                                int64
	search, monitored                      bool
}

const (
	collectionConfigs = `SELECT tmdb_id,tmdb_id,versions,minimum_availability,'movieOnly',view,root_folder_path,quality_profile_id,search_on_add,monitored
  FROM movie_collection`
	listConfigs = `SELECT id,tmdb_id,versions,minimum_availability,monitor,view,root_folder_path,quality_profile_id,search_on_add,1
  FROM movie_import_list`
)

// configs reads the intake rows of q (collectionConfigs or listConfigs plus a filter and order).
func (s *MovieIntake) configs(ctx context.Context, q string, args ...any) ([]movieIntakeConfig, error) {
	rows, err := s.Store.Reader().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []movieIntakeConfig
	for rows.Next() {
		var c movieIntakeConfig
		if err := rows.Scan(&c.id, &c.tmdbID, &c.versions, &c.minimum, &c.monitor, &c.view, &c.root, &c.profile, &c.search, &c.monitored); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// config reads the one row of q; sql.ErrNoRows when there is none.
func (s *MovieIntake) config(ctx context.Context, q string, id int64) (movieIntakeConfig, error) {
	if err := s.init(); err != nil {
		return movieIntakeConfig{}, err
	}
	configs, err := s.configs(ctx, q, id)
	if err != nil {
		return movieIntakeConfig{}, err
	}
	if len(configs) == 0 {
		return movieIntakeConfig{}, sql.ErrNoRows
	}
	return configs[0], nil
}

// RefreshCollections rereads known memberships, adding only new movies for monitored collections.
func (s *MovieIntake) RefreshCollections(ctx context.Context) (IntakeResult, error) {
	if err := s.init(); err != nil {
		return IntakeResult{}, err
	}
	if err := DiscoverCollections(ctx, s.Store, s.Now()); err != nil {
		return IntakeResult{}, err
	}
	configs, err := s.configs(ctx, collectionConfigs+` ORDER BY tmdb_id`)
	if err != nil {
		return IntakeResult{}, err
	}
	var out IntakeResult
	var errs []error
	for _, cfg := range configs {
		soft, err := s.refreshCollection(ctx, cfg, &out)
		if err != nil {
			return out, err
		}
		errs = append(errs, soft...)
	}
	return out, errors.Join(errs...)
}

// RefreshCollection refreshes one known collection now: its name and parts, and, when it is monitored, its missing
// movies. sql.ErrNoRows when the collection is not known.
func (s *MovieIntake) RefreshCollection(ctx context.Context, tmdbID int64) (IntakeResult, error) {
	cfg, err := s.config(ctx, collectionConfigs+` WHERE tmdb_id=?`, tmdbID)
	if err != nil {
		return IntakeResult{}, err
	}
	var out IntakeResult
	soft, err := s.refreshCollection(ctx, cfg, &out)
	if err != nil {
		return out, err
	}
	return out, errors.Join(soft...)
}

// refreshCollection is one collection's refresh, counted in out. TMDb and per-movie failures come back as soft (the
// caller carries on with the next collection); err is a store failure.
func (s *MovieIntake) refreshCollection(ctx context.Context, cfg movieIntakeConfig, out *IntakeResult) (soft []error, err error) {
	out.Collections++
	c, err := s.TMDb.Collection(ctx, int(cfg.tmdbID))
	if err != nil {
		return []error{fmt.Errorf("collection %d: %w", cfg.tmdbID, err)}, nil
	}
	parts, err := json.Marshal(c.Parts)
	if err != nil {
		return nil, err
	}
	if _, err = s.Store.Writer().ExecContext(ctx, `UPDATE movie_collection SET name=?,movies=?,refreshed_at=? WHERE tmdb_id=?`, c.Name, string(parts), store.Millis(s.Now()), cfg.tmdbID); err != nil {
		return nil, err
	}
	if !cfg.monitored {
		return nil, nil
	}
	if cfg.versions, err = s.collectionTargets(cfg); err != nil {
		return []error{fmt.Errorf("collection %d: %w", cfg.tmdbID, err)}, nil
	}
	if cfg.versions == "" {
		return nil, nil
	}
	for _, m := range c.Parts {
		n, err := s.addMovie(ctx, m, cfg, cfg.tmdbID)
		if err != nil {
			soft = append(soft, fmt.Errorf("collection %d movie %d: %w", cfg.tmdbID, m.ID, err))
			continue
		}
		if n {
			out.Added++
		} else {
			out.Skipped++
		}
	}
	return soft, nil
}

// collectionTargets revalidates a monitored collection's stored targets: only collection targets (policy
// CollectionTarget: active movie versions nothing follows, the ones the library gate lets write) take its movies.
// Others are skipped, logged once; with none left (an empty list among them) it adds nothing, and returns "".
func (s *MovieIntake) collectionTargets(cfg movieIntakeConfig) (string, error) {
	var keys, ok, skipped []string
	if err := json.Unmarshal([]byte(cfg.versions), &keys); err != nil {
		return "", err
	}
	for _, k := range keys {
		if s.Policy.CollectionTarget(k) {
			ok = append(ok, k)
		} else {
			skipped = append(skipped, k)
		}
	}
	if len(skipped) > 0 || len(ok) == 0 {
		if _, seen := s.skipLogged.LoadOrStore(fmt.Sprintf("%d:%s", cfg.tmdbID, strings.Join(skipped, ",")), true); !seen {
			s.Log.Warn("movie collection: targets skipped, not active movie versions the library may write",
				"collection", cfg.tmdbID, "skipped", skipped, "targets", ok)
		}
	}
	if len(ok) == 0 {
		return "", nil
	}
	b, err := json.Marshal(ok)
	return string(b), err
}

// SyncLists imports enabled automatic lists. Existing titles and their monitoring choices are left alone.
func (s *MovieIntake) SyncLists(ctx context.Context) (IntakeResult, error) {
	if err := s.init(); err != nil {
		return IntakeResult{}, err
	}
	configs, err := s.configs(ctx, listConfigs+` WHERE enabled=1 AND enable_auto=1 ORDER BY id`)
	if err != nil {
		return IntakeResult{}, err
	}
	var out IntakeResult
	var errs []error
	for _, cfg := range configs {
		soft, err := s.syncList(ctx, cfg, &out)
		if err != nil {
			return out, err
		}
		errs = append(errs, soft...)
	}
	return out, errors.Join(errs...)
}

// SyncList imports one list now, by its row id, whether or not it is enabled. sql.ErrNoRows when there is no such
// list.
func (s *MovieIntake) SyncList(ctx context.Context, id int64) (IntakeResult, error) {
	cfg, err := s.config(ctx, listConfigs+` WHERE id=?`, id)
	if err != nil {
		return IntakeResult{}, err
	}
	var out IntakeResult
	soft, err := s.syncList(ctx, cfg, &out)
	if err != nil {
		return out, err
	}
	return out, errors.Join(soft...)
}

// syncList is one list's import, counted in out; last_sync_at moves only on a pass without failures. soft and err
// are as for refreshCollection.
func (s *MovieIntake) syncList(ctx context.Context, cfg movieIntakeConfig, out *IntakeResult) (soft []error, err error) {
	out.Lists++
	l, err := s.TMDb.List(ctx, int(cfg.tmdbID))
	if err != nil {
		return []error{fmt.Errorf("list %d: %w", cfg.id, err)}, nil
	}
	cfg.monitored = cfg.monitor != "none"
	for _, m := range l.Items {
		n, err := s.addMovie(ctx, m, cfg, 0)
		if err != nil {
			soft = append(soft, fmt.Errorf("list %d movie %d: %w", cfg.id, m.ID, err))
			continue
		}
		if n {
			out.Added++
		} else {
			out.Skipped++
		}
	}
	if len(soft) == 0 {
		if _, err := s.Store.Writer().ExecContext(ctx, `UPDATE movie_import_list SET last_sync_at=? WHERE id=?`, store.Millis(s.Now()), cfg.id); err != nil {
			return nil, err
		}
	}
	return soft, nil
}

func intakeSkip(ctx context.Context, q Querier, id int) (bool, error) {
	var skip bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM movie_exclusion WHERE tmdb_id=?) OR
  EXISTS(SELECT 1 FROM title WHERE kind='movie' AND tmdb_id=?)`, id, id).Scan(&skip)
	return skip, err
}

// intakeVersions are the versions an intake's targets (keys; none: the policy's defaults) turn on for a movie:
// profiles choose fitting versions first and fall back to their explicit targets, as arr movie adds do. An explicit
// anime root or all-anime targets make the movie anime, as for an arr movie add; the returned flag says so.
func (s *MovieIntake) intakeVersions(keys []string, anime bool, root string) ([]string, bool, error) {
	allAnime := len(keys) > 0
	for _, key := range keys {
		vp := s.Policy.Versions[key]
		if vp == nil || vp.Anime == nil || !*vp.Anime {
			allAnime = false
		}
		if vp != nil && vp.Anime != nil && root != "" && vp.Library == root {
			anime = *vp.Anime
		}
	}
	if allAnime {
		anime = true
	}
	var fit, valid []string
	for _, key := range keys {
		if vp := s.Policy.Versions[key]; vp != nil && vp.Kind == policy.KindMovie {
			valid = append(valid, key)
			if Fits(vp, policy.KindMovie, anime) {
				fit = append(fit, key)
			}
		}
	}
	if len(keys) == 0 {
		fit = DefaultVersions(s.Policy, policy.KindMovie, anime)
	}
	if len(fit) == 0 {
		fit = valid
	}
	if len(fit) == 0 {
		return nil, anime, errors.New("catalog: movie intake has no target versions")
	}
	slices.Sort(fit)
	return fit, anime, nil
}

// addMissingVersions gives a movie the catalog already holds the intake's target versions it has no row for:
// another list or collection may target other versions than the one that added it. A version the
// title has in any state (unmonitored, removed from the views) is a choice already made and stays as it is.
func (s *MovieIntake) addMissingVersions(ctx context.Context, titleID int64, anime bool, cfg movieIntakeConfig,
	collectionID int64) (bool, error) {
	var keys []string
	if err := json.Unmarshal([]byte(cfg.versions), &keys); err != nil {
		return false, err
	}
	fit, _, err := s.intakeVersions(keys, anime, cfg.root)
	if err != nil {
		return false, err
	}
	method := "list"
	if collectionID > 0 {
		method = "collection"
	}
	minimum := cfg.minimum
	if minimum == "" {
		minimum = "released"
	}
	now := s.Now()
	var added []string
	err = s.Store.WriteTx(ctx, func(tx *sql.Tx) error {
		added = nil
		for _, k := range fit {
			var has bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM version WHERE title_id=? AND policy_key=?)`,
				titleID, k).Scan(&has); err != nil {
				return err
			}
			if !has {
				added = append(added, k)
			}
		}
		if len(added) == 0 {
			return nil
		}
		if _, err := TurnOn(ctx, tx, titleID, added, cfg.monitored, now); err != nil {
			return err
		}
		for _, k := range added {
			if _, err := tx.ExecContext(ctx, `UPDATE version SET add_method=? WHERE title_id=? AND policy_key=?`,
				method, titleID, k); err != nil {
				return err
			}
			s.Log.Info("version turned on", "source", "movie-list", "title", titleID, "version", k)
		}
		views := []string{cfg.view}
		if cfg.view == "" {
			views = nil
			for _, key := range added {
				views = append(views, "v/"+key)
			}
		}
		for _, view := range views {
			if _, err := tx.ExecContext(ctx, `INSERT INTO arrview_title(view,title_id,quality_profile_id,root_folder_path,minimum_availability,added_at,updated_at) VALUES(?,?,?,?,?,?,?)
				ON CONFLICT DO NOTHING`, view, titleID, cfg.profile, cfg.root, minimum, store.Millis(now), store.Millis(now)); err != nil {
				return err
			}
		}
		return SyncMonitored(ctx, tx, titleID, now)
	})
	if err != nil || len(added) == 0 {
		return false, err
	}
	if cfg.search && cfg.monitored && s.OnAdd != nil {
		var ready []string
		for _, k := range added {
			var vid int64
			if err := s.Store.Reader().QueryRowContext(ctx, `SELECT id FROM version WHERE title_id=? AND policy_key=?`,
				titleID, k).Scan(&vid); err != nil {
				return true, err
			}
			ok, err := availability.Movie(ctx, s.Store.Reader(), s.Policy, titleID, vid, k, now)
			if err != nil {
				return true, err
			}
			if ok {
				ready = append(ready, k)
			}
		}
		if len(ready) > 0 {
			if err := s.OnAdd(ctx, titleID, ready); err != nil {
				return true, err
			}
		}
	}
	return true, nil
}

func (s *MovieIntake) addMovie(ctx context.Context, summary tmdb.MovieSummary, cfg movieIntakeConfig, collectionID int64) (bool, error) {
	if summary.ID <= 0 || summary.MediaType != "" && summary.MediaType != "movie" {
		return false, nil
	}
	var excluded bool
	if err := s.Store.Reader().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM movie_exclusion WHERE tmdb_id=?)`,
		summary.ID).Scan(&excluded); err != nil || excluded {
		return false, err
	}
	var held int64
	var heldAnime bool
	err := s.Store.Reader().QueryRowContext(ctx, `SELECT id, anime FROM title WHERE kind='movie' AND tmdb_id=?`,
		summary.ID).Scan(&held, &heldAnime)
	switch {
	case err == nil:
		return s.addMissingVersions(ctx, held, heldAnime, cfg, collectionID)
	case !errors.Is(err, sql.ErrNoRows):
		return false, err
	}
	m, err := s.TMDb.MovieDetails(ctx, summary.ID)
	if err != nil {
		return false, err
	}
	if m.ID != summary.ID {
		return false, fmt.Errorf("TMDb movie id %d does not match %d", m.ID, summary.ID)
	}
	var keys []string
	if err := json.Unmarshal([]byte(cfg.versions), &keys); err != nil {
		return false, err
	}
	anime := m.OriginalLanguage == "ja"
	animated := false
	for _, g := range m.Genres {
		if g.ID == 16 {
			animated = true
		}
	}
	fit, anime, err := s.intakeVersions(keys, anime && animated, cfg.root)
	if err != nil {
		return false, err
	}
	if m.Title == "" {
		m.Title = summary.Title
	}
	if m.ReleaseDate == "" {
		m.ReleaseDate = summary.ReleaseDate
	}
	dates := m.ReleaseSchedule()
	minimum := cfg.minimum
	if minimum == "" {
		minimum = "released"
	}
	now := s.Now()
	var result *Result
	err = s.Store.WriteTx(ctx, func(tx *sql.Tx) error {
		if skip, err := intakeSkip(ctx, tx, summary.ID); err != nil || skip {
			return err
		}
		year := 0
		if d, err := time.Parse(time.DateOnly, m.ReleaseDate); err == nil {
			year = d.Year()
		}
		var err error
		method := "list"
		if collectionID > 0 {
			method = "collection"
		}
		result, err = Add(ctx, tx, Input{Kind: policy.KindMovie, TmdbID: int64(m.ID), ImdbID: m.IMDbID, Title: m.Title, Year: year, Anime: &anime,
			Versions: func(bool) ([]string, error) { return fit, nil }, Monitored: cfg.monitored, Source: "movie-list", AddMethod: method}, now, s.Log)
		if err != nil {
			return err
		}
		if collectionID == 0 && m.BelongsToCollection != nil {
			collectionID = int64(m.BelongsToCollection.ID)
		}
		if collectionID > 0 {
			if _, err = tx.ExecContext(ctx, `INSERT INTO title_external_id(title_id,source,external_id) VALUES(?,'tmdb_collection',?) ON CONFLICT DO NOTHING`, result.TitleID, strconv.FormatInt(collectionID, 10)); err != nil {
				return err
			}
			// Radarr's movieAndCollection monitor option also follows future collection members, in the movie's
			// versions that are collection targets (explicit, as every collection's targets are).
			var targets []string
			for _, k := range fit {
				if s.Policy.CollectionTarget(k) {
					targets = append(targets, k)
				}
			}
			if cfg.monitor == "movieAndCollection" && m.BelongsToCollection != nil && len(targets) > 0 {
				vb, err := json.Marshal(targets)
				if err != nil {
					return err
				}
				if _, err = tx.ExecContext(ctx, `INSERT INTO movie_collection(tmdb_id,name,monitored,versions,view,quality_profile_id,root_folder_path,minimum_availability,search_on_add,updated_at)
     VALUES(?,?,1,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, collectionID, m.BelongsToCollection.Name, string(vb), cfg.view, cfg.profile, cfg.root, minimum, cfg.search, store.Millis(now)); err != nil {
					return err
				}
			}
		}
		for kind, date := range dates {
			if date.IsZero() {
				continue
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO title_release_date(title_id,source,country,type,date,fetched_at) VALUES(?,'tmdb','',?,?,?)`, result.TitleID, kind, date.Format(time.DateOnly), store.Millis(now)); err != nil {
				return err
			}
		}
		views := []string{cfg.view}
		if cfg.view == "" {
			views = nil
			for _, key := range fit {
				views = append(views, "v/"+key)
			}
		}
		for _, view := range views {
			if _, err = tx.ExecContext(ctx, `INSERT INTO arrview_title(view,title_id,quality_profile_id,root_folder_path,minimum_availability,added_at,updated_at) VALUES(?,?,?,?,?,?,?)`, view, result.TitleID, cfg.profile, cfg.root, minimum, store.Millis(now), store.Millis(now)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	if result == nil || !result.Created {
		return false, nil
	}
	if cfg.search && cfg.monitored && availability.Available(minimum, dates, now) && s.OnAdd != nil {
		if err := s.OnAdd(ctx, result.TitleID, result.Versions); err != nil {
			return true, err
		}
	}
	return true, nil
}
