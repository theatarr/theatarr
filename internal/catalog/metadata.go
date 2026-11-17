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

// MovieMetadataSource is the gateway-backed TMDb client used by movie intake (and by series lists: TVDetails gives a
// TMDb series its TVDB id).
type MovieMetadataSource interface {
	Collection(context.Context, int) (*tmdb.Collection, error)
	List(context.Context, int) (*tmdb.List, error)
	MovieDetails(context.Context, int) (*tmdb.Movie, error)
	TVDetails(context.Context, int) (*tmdb.TV, error)
	// DiscoverMovies and DiscoverTV read a discover list's rule (tmdb_discover).
	DiscoverMovies(context.Context, tmdb.Discover) ([]tmdb.MovieSummary, error)
	DiscoverTV(context.Context, tmdb.Discover) ([]tmdb.MovieSummary, error)
	// FindByIMDb and FindTVByIMDb resolve a JSON list's IMDb ids (ErrNotFound when TMDb has none).
	FindByIMDb(context.Context, string) (int, error)
	FindTVByIMDb(context.Context, string) (int, error)
	// FindTVByTVDB resolves a series exclusion for Preview (cached).
	FindTVByTVDB(context.Context, int) (int, error)
}

// MovieIntake refreshes collections and lists through the shared catalog add path.
type MovieIntake struct {
	Store  *store.Store
	Policy *policy.Policy
	TMDb   MovieMetadataSource
	Now    func() time.Time
	Log    *slog.Logger
	// OnAdd asks the add-time search after commit, only for monitored, available movies (title id, the libraries).
	// later is true for list syncs and the timed collection sync (the search schedule serves them, under the hourly
	// limit) and false when a person adds or refreshes one collection (searched at once).
	OnAdd func(ctx context.Context, titleID int64, versions []string, later bool) error
	// Arr opens the read-only client of a Radarr or Sonarr list's instance; nil: arr.New with the default options.
	Arr func(url, key string) (ArrReader, error)
	// Fetch reads a JSON list's address through the gateway (gateway.ListGet); nil: JSON lists cannot be read.
	Fetch func(ctx context.Context, url string) ([]byte, error)

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
	// later: the add's search goes on the search schedule (OnAdd's later).
	later bool
	// listType is an import list's source (tmdb, radarr, sonarr); "" for a collection. url and apiKey are a Radarr
	// or Sonarr list's instance. media is what the list adds: movie or series (a Sonarr list, a TMDb series list).
	// rule is a discover list's rule (tmdb.Discover as JSON).
	listType, url, apiKey, media, rule string
	// importOnly: the one-time import from an arr (importConfig), which adds to its one library only: the sync
	// groups do not widen it.
	importOnly bool
}

// syncPolicy is the policy whose sync groups widen the intake's adds (Input.Policy); nil for the one-time import.
func (s *MovieIntake) syncPolicy(cfg movieIntakeConfig) *policy.Policy {
	if cfg.importOnly {
		return nil
	}
	return s.Policy
}

const (
	collectionConfigs = `SELECT tmdb_id,tmdb_id,versions,minimum_availability,'movieOnly',view,root_folder_path,quality_profile_id,search_on_add,monitored,'','','','movie',''
  FROM movie_collection`
	listConfigs = `SELECT id,tmdb_id,versions,minimum_availability,monitor,view,root_folder_path,quality_profile_id,search_on_add,1,list_type,url,api_key,media,COALESCE(rule,'')
  FROM import_list`
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
		if err := rows.Scan(&c.id, &c.tmdbID, &c.versions, &c.minimum, &c.monitor, &c.view, &c.root, &c.profile, &c.search, &c.monitored,
			&c.listType, &c.url, &c.apiKey, &c.media, &c.rule); err != nil {
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
		cfg.later = true
		soft, gone, err := s.refreshCollection(ctx, cfg, &out)
		if err != nil {
			return out, err
		}
		if !gone {
			errs = append(errs, soft...)
		}
	}
	return out, errors.Join(errs...)
}

// RefreshCollection refreshes one known collection now: its name and parts, and, when it is monitored, its missing
// movies. sql.ErrNoRows when the collection is not known; TMDb's not found (with the row gone when it was not
// monitored) when TMDb no longer has it.
func (s *MovieIntake) RefreshCollection(ctx context.Context, tmdbID int64) (IntakeResult, error) {
	cfg, err := s.config(ctx, collectionConfigs+` WHERE tmdb_id=?`, tmdbID)
	if err != nil {
		return IntakeResult{}, err
	}
	var out IntakeResult
	soft, _, err := s.refreshCollection(ctx, cfg, &out)
	if err != nil {
		return out, err
	}
	return out, errors.Join(soft...)
}

// refreshCollection is one collection's refresh, counted in out. TMDb and per-movie failures come back as soft (the
// caller carries on with the next collection); err is a store failure. gone: TMDb no longer has the collection
// (soft holds its not found); an unmonitored one's row and memberships are deleted then, so discovery does not
// bring it back.
func (s *MovieIntake) refreshCollection(ctx context.Context, cfg movieIntakeConfig, out *IntakeResult) (soft []error, gone bool, err error) {
	out.Collections++
	c, err := s.TMDb.Collection(ctx, int(cfg.tmdbID))
	if errors.Is(err, tmdb.ErrNotFound) {
		return []error{fmt.Errorf("collection %d: %w", cfg.tmdbID, err)}, true, s.collectionGone(ctx, cfg)
	}
	if err != nil {
		return []error{fmt.Errorf("collection %d: %w", cfg.tmdbID, err)}, false, nil
	}
	parts, err := json.Marshal(c.Parts)
	if err != nil {
		return nil, false, err
	}
	if _, err = s.Store.Writer().ExecContext(ctx, `UPDATE movie_collection SET name=?,movies=?,refreshed_at=? WHERE tmdb_id=?`, c.Name, string(parts), store.Millis(s.Now()), cfg.tmdbID); err != nil {
		return nil, false, err
	}
	if !cfg.monitored {
		return nil, false, nil
	}
	if cfg.versions, err = s.collectionTargets(cfg); err != nil {
		return []error{fmt.Errorf("collection %d: %w", cfg.tmdbID, err)}, false, nil
	}
	if cfg.versions == "" {
		return nil, false, nil
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
	return soft, false, nil
}

// collectionGone handles a collection TMDb answers not found for: a monitored one keeps its row and settings; an
// unmonitored one loses its row and its tmdb_collection memberships. Titles, files and versions stay.
func (s *MovieIntake) collectionGone(ctx context.Context, cfg movieIntakeConfig) error {
	var name string
	if err := s.Store.Reader().QueryRowContext(ctx, `SELECT name FROM movie_collection WHERE tmdb_id=?`, cfg.tmdbID).Scan(&name); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if cfg.monitored {
		s.Log.Info("movie collection: not found on TMDb, kept (monitored)", "collection", cfg.tmdbID, "name", name)
		return nil
	}
	s.Log.Info("movie collection: not found on TMDb, removed (not monitored)", "collection", cfg.tmdbID, "name", name)
	return s.Store.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM title_external_id WHERE source='tmdb_collection' AND external_id=?`, strconv.FormatInt(cfg.tmdbID, 10)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM movie_collection WHERE tmdb_id=?`, cfg.tmdbID)
		return err
	})
}

// collectionTargets revalidates a monitored collection's stored targets: only collection targets (policy
// CollectionTarget: movie libraries) take its movies.
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
			s.Log.Warn("movie collection: targets skipped, not movie versions",
				"collection", cfg.tmdbID, "skipped", skipped, "targets", ok)
		}
	}
	if len(ok) == 0 {
		return "", nil
	}
	b, err := json.Marshal(ok)
	return string(b), err
}

// SyncLists imports enabled automatic lists of every kind but Radarr and Sonarr (SyncArrLists). Existing titles and
// their monitoring choices are left alone.
func (s *MovieIntake) SyncLists(ctx context.Context) (IntakeResult, error) {
	return s.syncLists(ctx, listConfigs+` WHERE enabled=1 AND enable_auto=1 AND list_type NOT IN ('radarr','sonarr') ORDER BY id`)
}

// SyncArrLists imports enabled Radarr and Sonarr lists, every ArrListInterval.
func (s *MovieIntake) SyncArrLists(ctx context.Context) (IntakeResult, error) {
	return s.syncLists(ctx, listConfigs+` WHERE enabled=1 AND enable_auto=1 AND list_type IN ('radarr','sonarr') ORDER BY id`)
}

func (s *MovieIntake) syncLists(ctx context.Context, q string) (IntakeResult, error) {
	if err := s.init(); err != nil {
		return IntakeResult{}, err
	}
	configs, err := s.configs(ctx, q)
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

// syncList is one list's import, counted in out; last_sync_at moves only on a pass without failures, and last_error
// says the list's own last result: cleared by a pass without failures, else why the list could not be read or how
// many of its titles failed (the first one's error). soft and err are as for refreshCollection. A list that cannot be
// read adds nothing.
func (s *MovieIntake) syncList(ctx context.Context, cfg movieIntakeConfig, out *IntakeResult) (soft []error, err error) {
	out.Lists++
	cfg.monitored = cfg.monitor != "none"
	cfg.later = true
	var readErr error
	switch cfg.listType {
	case "radarr":
		soft, readErr = s.syncRadarr(ctx, cfg, out)
	case "sonarr":
		soft, readErr = s.syncSonarr(ctx, cfg, out)
	case "tmdb_discover":
		var items []tmdb.MovieSummary
		if items, readErr = s.discover(ctx, cfg); readErr == nil {
			soft = s.addItems(ctx, cfg, items, out)
		}
	case "json":
		var items []tmdb.MovieSummary
		var unresolved int
		if items, unresolved, readErr = s.jsonItems(ctx, cfg); readErr == nil {
			out.Skipped += unresolved
			soft = s.addItems(ctx, cfg, items, out)
		}
	default:
		var l *tmdb.List
		if l, readErr = s.TMDb.List(ctx, int(cfg.tmdbID)); readErr == nil {
			soft = s.addItems(ctx, cfg, l.Items, out)
		}
	}
	if readErr != nil {
		soft = []error{fmt.Errorf("list %d: %w", cfg.id, readErr)}
	}
	if ctx.Err() != nil {
		return soft, nil // stopped: the list's last result stays as it was
	}
	if len(soft) == 0 {
		_, err = s.Store.Writer().ExecContext(ctx, `UPDATE import_list SET last_sync_at=?, last_error=NULL WHERE id=?`,
			store.Millis(s.Now()), cfg.id)
		return nil, err
	}
	var msg string
	if readErr != nil {
		msg = "The list could not be read: " + readErr.Error()
	} else {
		first := strings.TrimPrefix(soft[0].Error(), fmt.Sprintf("list %d ", cfg.id))
		msg = fmt.Sprintf("%d of its titles could not be added; the first: %s", len(soft), first)
	}
	if r := []rune(msg); len(r) > 500 {
		msg = string(r[:500]) + "…"
	}
	if _, err := s.Store.Writer().ExecContext(ctx, `UPDATE import_list SET last_error=? WHERE id=?`, msg, cfg.id); err != nil {
		return nil, err
	}
	return soft, nil
}

// discover reads a discover list's titles: its rule's movies or series, by the list's media.
func (s *MovieIntake) discover(ctx context.Context, cfg movieIntakeConfig) ([]tmdb.MovieSummary, error) {
	var d tmdb.Discover
	if err := json.Unmarshal([]byte(cfg.rule), &d); err != nil {
		return nil, fmt.Errorf("discover rule: %w", err)
	}
	if cfg.media == "series" {
		return s.TMDb.DiscoverTV(ctx, d)
	}
	return s.TMDb.DiscoverMovies(ctx, d)
}

// addItems adds a TMDb list's items of the list's media, counted in out: a movie list its movies (addMovie), a
// series list its TV items (addTMDbSeries); the other media's items are skipped.
func (s *MovieIntake) addItems(ctx context.Context, cfg movieIntakeConfig, items []tmdb.MovieSummary, out *IntakeResult) (soft []error) {
	for _, m := range items {
		var n bool
		var err error
		what := "movie"
		if cfg.media == "series" {
			what = "series"
			n, err = s.addTMDbSeries(ctx, m, cfg)
		} else {
			n, err = s.addMovie(ctx, m, cfg, 0)
		}
		if err != nil {
			soft = append(soft, fmt.Errorf("list %d %s %d: %w", cfg.id, what, m.ID, err))
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

// addTMDbSeries adds a TMDb list's TV item through the Sonarr list path (addSeries: exclusions, fitting libraries,
// a library row in any state left alone). A series Theatarr holds keeps its own ids and anime flag; a new one is
// resolved to its TVDB id with TMDb's details (one request), anime by LooksAnime. An item with no TVDB id is skipped
// (counted, not added): a series without one gets no episodes.
func (s *MovieIntake) addTMDbSeries(ctx context.Context, item tmdb.MovieSummary, cfg movieIntakeConfig) (bool, error) {
	if item.ID <= 0 || item.MediaType != "tv" {
		return false, nil
	}
	it := arrSeries{TmdbID: int64(item.ID), Title: item.Name}
	var tvdb sql.NullInt64
	err := s.Store.Reader().QueryRowContext(ctx, `SELECT tvdb_id FROM title WHERE kind='series' AND tmdb_id=?`, item.ID).
		Scan(&tvdb)
	switch {
	case err == nil && tvdb.Valid && tvdb.Int64 > 0:
		it.TvdbID = tvdb.Int64
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return false, err
	default:
		t, err := s.TMDb.TVDetails(ctx, item.ID)
		if err != nil {
			return false, err
		}
		if t.ExternalIDs.TVDbID <= 0 {
			s.Log.Info("series list: skipped, TMDb has no TVDB id for it", "list", cfg.id, "tmdb", item.ID, "name", t.Name)
			return false, nil
		}
		it.TvdbID, it.ImdbID, it.Title = int64(t.ExternalIDs.TVDbID), t.ExternalIDs.IMDbID, t.Name
		if len(t.FirstAirDate) >= 4 {
			it.Year, _ = strconv.Atoi(t.FirstAirDate[:4])
		}
		genres := make([]int, 0, len(t.Genres))
		for _, g := range t.Genres {
			genres = append(genres, g.ID)
		}
		if tmdb.LooksAnime(t.OriginalLanguage, genres) {
			it.SeriesType = "anime"
		}
	}
	_, added, err := s.addSeries(ctx, it, cfg)
	return added, err
}

func intakeSkip(ctx context.Context, q Querier, id int) (bool, error) {
	var skip bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM movie_exclusion WHERE tmdb_id=?) OR
  EXISTS(SELECT 1 FROM title WHERE kind='movie' AND tmdb_id=?)`, id, id).Scan(&skip)
	return skip, err
}

// intakeVersions are the versions an intake's targets (keys; none: the policy's defaults) turn on for a title of
// kind: profiles choose fitting versions first and fall back to their explicit targets, as arr movie adds do. An
// explicit anime root or all-anime targets make the title anime, as for an arr movie add; the returned flag says so.
func (s *MovieIntake) intakeVersions(keys []string, anime bool, root string, kind policy.Kind) ([]string, bool, error) {
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
		if vp := s.Policy.Versions[key]; vp != nil && vp.Kind == kind {
			valid = append(valid, key)
			if Fits(vp, kind, anime) {
				fit = append(fit, key)
			}
		}
	}
	if len(keys) == 0 {
		fit = DefaultVersions(s.Policy, kind, anime)
	}
	if len(fit) == 0 {
		fit = valid
	}
	if len(fit) == 0 {
		return nil, anime, errors.New("catalog: " + string(kind) + " intake has no target versions")
	}
	slices.Sort(fit)
	return fit, anime, nil
}

// fitVersions are the versions a list's or collection's targets turn on for a title of kind. TMDb lists (movie or
// series libraries, by the list's media) and collections take intakeVersions, fallbacks included. A Radarr or Sonarr
// list takes only its chosen libraries that fit the title (its kind, and anime or not); none when none fits, and the
// title is skipped.
func (s *MovieIntake) fitVersions(cfg movieIntakeConfig, keys []string, anime bool, kind policy.Kind) ([]string, bool,
	error) {
	if cfg.listType != "radarr" && cfg.listType != "sonarr" {
		return s.intakeVersions(keys, anime, cfg.root, kind)
	}
	var fit []string
	for _, k := range keys {
		if Fits(s.Policy.Versions[k], kind, anime) {
			fit = append(fit, k)
		}
	}
	slices.Sort(fit)
	return fit, anime, nil
}

// addMissingVersions gives a movie the catalog already holds the intake's target versions it has no row for:
// another list or collection may target other versions than the one that added it. A version the
// title has in any state (unmonitored, removed from the views) is a choice already made and stays as it is. A
// version it creates brings the libraries in sync with it (TurnOnSynced), added and searched like it.
func (s *MovieIntake) addMissingVersions(ctx context.Context, titleID int64, anime bool, cfg movieIntakeConfig,
	collectionID int64) (bool, error) {
	var keys []string
	if err := json.Unmarshal([]byte(cfg.versions), &keys); err != nil {
		return false, err
	}
	fit, _, err := s.fitVersions(cfg, keys, anime, policy.KindMovie)
	if err != nil || len(fit) == 0 {
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
		_, synced, err := TurnOnSynced(ctx, tx, s.syncPolicy(cfg), titleID, added, cfg.monitored, false, now)
		if err != nil {
			return err
		}
		views := []string{cfg.view}
		if cfg.view == "" {
			views = nil
			for _, key := range added {
				views = append(views, "v/"+key)
			}
		}
		// The libraries in sync count as added: they are searched like the others, and take the minimum
		// availability unless they have their own (ChoiceKeys).
		added = append(added, synced...)
		for _, k := range added {
			if _, err := tx.ExecContext(ctx, `UPDATE version SET add_method=? WHERE title_id=? AND policy_key=?`,
				method, titleID, k); err != nil {
				return err
			}
			s.Log.Info("version turned on", "source", "movie-list", "title", titleID, "version", k)
		}
		// The view's record holds the profile and root folder only; the minimum availability is a choice of each
		// library added, on its own row, so it never reaches a copy the movie already had.
		for _, view := range views {
			if _, err := tx.ExecContext(ctx, `INSERT INTO arrview_title(view,title_id,quality_profile_id,root_folder_path,added_at,updated_at) VALUES(?,?,?,?,?,?)
				ON CONFLICT DO NOTHING`, view, titleID, cfg.profile, cfg.root, store.Millis(now), store.Millis(now)); err != nil {
				return err
			}
		}
		if err := availability.Choose(ctx, tx, titleID, ChoiceKeys(s.Policy, added, synced), minimum, now); err != nil {
			return err
		}
		return SyncMonitored(ctx, tx, titleID, now)
	})
	if err != nil || len(added) == 0 {
		return false, err
	}
	if cfg.search && cfg.monitored {
		return true, s.searchReady(ctx, titleID, added, now, cfg.later)
	}
	return true, nil
}

// searchReady hands OnAdd the libraries of keys the movie is available in now, each by its own stored rule and
// dates (availability.Movie, the gate of every automatic search), with later.
func (s *MovieIntake) searchReady(ctx context.Context, titleID int64, keys []string, now time.Time, later bool) error {
	if s.OnAdd == nil {
		return nil
	}
	var ready []string
	for _, k := range keys {
		var vid int64
		if err := s.Store.Reader().QueryRowContext(ctx, `SELECT id FROM version WHERE title_id=? AND policy_key=?`,
			titleID, k).Scan(&vid); err != nil {
			return err
		}
		ok, err := availability.Movie(ctx, s.Store.Reader(), s.Policy, titleID, vid, k, now)
		if err != nil {
			return err
		}
		if ok {
			ready = append(ready, k)
		}
	}
	if len(ready) == 0 {
		return nil
	}
	return s.OnAdd(ctx, titleID, ready, later)
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
	_, added, err := s.addNewMovie(ctx, summary, m, cfg, collectionID)
	return added, err
}

// addNewMovie adds a movie new to Theatarr with its TMDb details m to the intake's versions that fit it. titleID is
// the new title's id when added.
func (s *MovieIntake) addNewMovie(ctx context.Context, summary tmdb.MovieSummary, m *tmdb.Movie, cfg movieIntakeConfig,
	collectionID int64) (titleID int64, added bool, err error) {
	var keys []string
	if err := json.Unmarshal([]byte(cfg.versions), &keys); err != nil {
		return 0, false, err
	}
	fit, anime, err := s.fitVersions(cfg, keys, tmdbAnime(m), policy.KindMovie)
	if err != nil || len(fit) == 0 {
		return 0, false, err
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
			Versions: func(bool) ([]string, error) { return fit, nil }, Monitored: cfg.monitored, Source: "movie-list", AddMethod: method,
			Policy: s.syncPolicy(cfg)}, now, s.Log)
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
		// As in addMissingVersions: the view's record without a minimum, and a choice per library added.
		for _, view := range views {
			if _, err = tx.ExecContext(ctx, `INSERT INTO arrview_title(view,title_id,quality_profile_id,root_folder_path,added_at,updated_at) VALUES(?,?,?,?,?,?)`, view, result.TitleID, cfg.profile, cfg.root, store.Millis(now), store.Millis(now)); err != nil {
				return err
			}
		}
		return availability.Choose(ctx, tx, result.TitleID, ChoiceKeys(s.Policy, result.Versions, result.Synced), minimum, now)
	})
	if err != nil {
		return 0, false, err
	}
	if result == nil || !result.Created {
		return 0, false, nil
	}
	if cfg.search && cfg.monitored {
		return result.TitleID, true, s.searchReady(ctx, result.TitleID, result.Versions, now, cfg.later)
	}
	return result.TitleID, true, nil
}

// tmdbAnime is the anime rule for a movie new to Theatarr: Japanese original language and TMDb's Animation genre.
func tmdbAnime(m *tmdb.Movie) bool {
	if m.OriginalLanguage != "ja" {
		return false
	}
	for _, g := range m.Genres {
		if g.ID == 16 {
			return true
		}
	}
	return false
}
