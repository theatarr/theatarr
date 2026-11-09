// Package art keeps title metadata and art (docs/design/ui.md, docs/design/artwork.md): the meta.art job fills title_meta
// from TMDb, and [Images] is the disk cache behind the /img proxy.
//
// # The job
//
// meta.art fetches one TMDb details request per title (movies: release dates, names and images appended; series: content
// ratings and images appended) and writes one title_meta row. A run takes titles with no row first, then due ones
// (refresh_after passed), at most metadata.art.per_run. [Service.Run] enqueues a run every metadata.art.interval
// and, every couple of minutes, whenever a title has no row yet, so new titles are filled soon after they appear.
// POST /api/theatarr/v1/titles/{id}/refresh enqueues a one-title run ([Service.Refresh]).
//
// Movies follow Radarr ShouldRefreshMovie: announced, in cinemas or physically released within 30 days, every
// 12 hours; older releases, every 180 days. Series art refreshes after 3, 14 or 90 days by airing status and age,
// with a few hours of spread by title id. A 404 marks the row's error and retries after 30 days; a
// title with no TMDb id, TVDB id or IMDb id to find it by retries after 7 days.
//
// # Budgets and errors
//
// Every request goes through the gateway (the tmdb budget for details, tmdb-images for posters). A run stops at the
// first error that is not about one title (a gateway refusal such as a back-off or a spent budget, a network error,
// a 5xx after the client's retries): the failing title is pushed back an hour and the job fails, so the worker
// retries it with its exponential back-off, as other jobs do. After a filled title the run pre-warms its poster
// (metadata.art.prewarm); a pre-warm failure is logged and stops pre-warming for the rest of the run, never the run.
package art

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/availability"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// JobKind is the job that fills title_meta.
const JobKind = "meta.art"

// batchKey dedupes scheduled runs: at most one is queued or running.
const batchKey = JobKind

// Payload is a meta.art job's payload: one title (TitleID set) or a run over new and due titles.
type Payload struct {
	TitleID int64 `json:"title_id,omitempty"`
}

// TMDb is the part of the TMDb client the job uses.
type TMDb interface {
	MovieDetails(ctx context.Context, id int) (*tmdb.Movie, error)
	TVDetails(ctx context.Context, id int) (*tmdb.TV, error)
	FindByIMDb(ctx context.Context, imdbID string) (int, error)
	FindTVByIMDb(ctx context.Context, imdbID string) (int, error)
	FindTVByTVDB(ctx context.Context, tvdbID int) (int, error)
}

// ErrNoTMDb is returned by Refresh when the TMDb source or its key is missing.
var ErrNoTMDb = errors.New("art: no TMDb client (metadata.sources.tmdb and its key)")

// ErrNoTitle is returned by Refresh for a title that does not exist.
var ErrNoTitle = errors.New("art: no such title")

// Config is what New needs.
type Config struct {
	Store *store.Store
	Queue *jobs.Queue
	// TMDb fetches details; nil turns the job off (no key), while the image proxy keeps serving what it has.
	TMDb TMDb
	// Images is the image cache; nil serves fallback art only.
	Images   *Images
	Settings policy.ArtSettings
	Logger   *slog.Logger
	// Now is the clock. Default time.Now.
	Now func() time.Time
	// NewPoll is how often Run looks for titles with no row. Default 2m.
	NewPoll time.Duration
	// FirstDelay is the wait before the first scheduled run after start. Default 1m.
	FirstDelay time.Duration
}

// Service runs the job and serves title art.
type Service struct {
	st     *store.Store
	q      *jobs.Queue
	tmdb   TMDb
	images *Images
	set    policy.ArtSettings
	log    *slog.Logger
	now    func() time.Time
	poll   time.Duration
	delay  time.Duration
}

// New returns a service.
func New(cfg Config) (*Service, error) {
	if cfg.Store == nil {
		return nil, errors.New("art: no store")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.NewPoll <= 0 {
		cfg.NewPoll = 2 * time.Minute
	}
	if cfg.FirstDelay <= 0 {
		cfg.FirstDelay = time.Minute
	}
	if cfg.Settings.PerRun <= 0 {
		cfg.Settings.PerRun = policy.DefaultArtPerRun
	}
	if cfg.Settings.Interval <= 0 {
		cfg.Settings.Interval = policy.DefaultArtInterval
	}
	return &Service{st: cfg.Store, q: cfg.Queue, tmdb: cfg.TMDb, images: cfg.Images, set: cfg.Settings,
		log: cfg.Logger.With("component", "art"), now: cfg.Now, poll: cfg.NewPoll, delay: cfg.FirstDelay}, nil
}

// Enabled reports whether the job runs (a TMDb client is set).
func (s *Service) Enabled() bool { return s.tmdb != nil }

// Images returns the image cache, or nil.
func (s *Service) Images() *Images { return s.images }

// RegisterJobs registers the meta.art handler. Without a TMDb client nothing is registered.
func (s *Service) RegisterJobs(w *jobs.Worker) {
	if s.tmdb == nil {
		return
	}
	w.Handle(JobKind, s.handle)
}

// RegisterRuntimeJobs registers meta.art for theatarr serve with settings in the database: each job runs on the
// service of the settings generation current when it starts (a saved metadata section's TMDb key or art settings),
// and a job that starts while TMDb is off is dropped.
func RegisterRuntimeJobs(w *jobs.Worker, resolve func(context.Context) (context.Context, *Service)) {
	w.Handle(JobKind, func(ctx context.Context, j *jobs.Job) error {
		ctx, s := resolve(ctx)
		if s == nil || s.tmdb == nil {
			return nil
		}
		return s.handle(ctx, j)
	})
}

func (s *Service) handle(ctx context.Context, j *jobs.Job) error {
	var p Payload
	if err := j.Decode(&p); err != nil {
		return jobs.Permanent(fmt.Errorf("art: payload: %w", err))
	}
	if p.TitleID > 0 {
		return s.FillTitle(ctx, p.TitleID)
	}
	_, err := s.RunBatch(ctx)
	return err
}

// Refresh enqueues a one-title run ahead of scheduled work and returns its job id.
func (s *Service) Refresh(ctx context.Context, titleID int64) (int64, error) {
	if s.tmdb == nil || s.q == nil {
		return 0, ErrNoTMDb
	}
	var one int
	err := s.st.Reader().QueryRowContext(ctx, `SELECT 1 FROM title WHERE id = ?`, titleID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNoTitle
	}
	if err != nil {
		return 0, err
	}
	id, _, err := s.q.Enqueue(ctx, JobKind, Payload{TitleID: titleID}, jobs.EnqueueOptions{
		DedupeKey: fmt.Sprintf("%s:title:%d", JobKind, titleID), Priority: 1, MaxAttempts: 3})
	return id, err
}

// Run schedules runs until ctx ends: one FirstDelay after start, then every interval, and one whenever a title has
// no row yet (checked every NewPoll). Without a TMDb client it returns at once.
func (s *Service) Run(ctx context.Context) {
	if s.tmdb == nil || s.q == nil {
		s.log.Info("meta.art off: no TMDb key")
		return
	}
	s.log.Info("meta.art scheduled", "interval", s.set.Interval.String(), "per_run", s.set.PerRun,
		"prewarm", s.set.Prewarm, "images", s.images != nil)
	next := s.now().Add(s.delay)
	t := time.NewTicker(min(s.poll, s.delay))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := s.now()
		due := !now.Before(next)
		if !due {
			n, err := s.missing(ctx)
			if err != nil {
				s.log.Warn("meta.art: looking for new titles", "err", err)
				continue
			}
			due = n > 0
		}
		if !due {
			continue
		}
		if err := s.enqueueBatch(ctx); err != nil {
			s.log.Warn("meta.art: enqueue", "err", err)
			continue
		}
		if !now.Before(next) {
			next = now.Add(s.set.Interval)
		}
		t.Reset(s.poll)
	}
}

func (s *Service) enqueueBatch(ctx context.Context) error {
	_, _, err := s.q.Enqueue(ctx, JobKind, Payload{}, jobs.EnqueueOptions{DedupeKey: batchKey})
	return err
}

// missing counts titles with no title_meta row.
func (s *Service) missing(ctx context.Context) (int, error) {
	var n int
	err := s.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM title t
		WHERE NOT EXISTS (SELECT 1 FROM title_meta m WHERE m.title_id = t.id)`).Scan(&n)
	return n, err
}

// Summary is what a run did.
type Summary struct {
	Filled   int // rows written from a good answer
	NotFound int // TMDb has no such title, or no id to find it by
	Prewarm  int // posters fetched into the cache
}

// RunBatch fills new titles, then due ones, up to per_run. It stops at the first error that is not about one title.
func (s *Service) RunBatch(ctx context.Context) (Summary, error) {
	var sum Summary
	if s.tmdb == nil {
		return sum, ErrNoTMDb
	}
	ids, err := s.pick(ctx)
	if err != nil {
		return sum, err
	}
	prewarm := s.images != nil && s.set.Prewarm != ""
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		r, err := s.fill(ctx, id)
		if err != nil {
			s.log.Warn("meta.art: run stopped", "title", id, "filled", sum.Filled, "err", err)
			return sum, err
		}
		if r.found {
			sum.Filled++
		} else {
			sum.NotFound++
		}
		if prewarm && r.poster != "" {
			fetched, err := s.images.Prewarm(ctx, s.set.Prewarm, r.poster)
			if err != nil {
				s.log.Info("meta.art: poster pre-warm stopped for this run", "title", id, "err", err)
				prewarm = false
			} else if fetched {
				sum.Prewarm++
			}
		}
	}
	if len(ids) > 0 {
		s.log.Info("meta.art run", "titles", len(ids), "filled", sum.Filled, "not_found", sum.NotFound,
			"posters", sum.Prewarm)
	}
	return sum, nil
}

// pick lists the titles a run fetches: no row first (oldest titles first), then due rows (longest overdue first).
func (s *Service) pick(ctx context.Context) ([]int64, error) {
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT t.id FROM title t LEFT JOIN title_meta m ON m.title_id = t.id
		WHERE m.title_id IS NULL OR m.refresh_after <= ?
		ORDER BY m.title_id IS NOT NULL, m.refresh_after, t.id LIMIT ?`, store.Millis(s.now()), s.set.PerRun)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// FillTitle fetches one title now. A 404 or a title TMDb can't be found by is recorded on its row, not returned.
func (s *Service) FillTitle(ctx context.Context, id int64) error {
	if s.tmdb == nil {
		return ErrNoTMDb
	}
	r, err := s.fill(ctx, id)
	if err != nil {
		return err
	}
	if s.images != nil && s.set.Prewarm != "" && r.poster != "" {
		if _, err := s.images.Prewarm(ctx, s.set.Prewarm, r.poster); err != nil {
			s.log.Info("meta.art: poster pre-warm", "title", id, "err", err)
		}
	}
	return nil
}

type titleRow struct {
	id     int64
	kind   string
	tmdbID sql.NullInt64
	tvdbID sql.NullInt64
	imdbID string
}

type fillResult struct {
	found  bool
	poster string // TMDb path, "" for none
}

// Retry waits after a failed try.
const (
	NotFoundRetry = 30 * 24 * time.Hour
	NoIDRetry     = 7 * 24 * time.Hour
	ErrorRetry    = time.Hour
)

func (s *Service) fill(ctx context.Context, id int64) (fillResult, error) {
	var t titleRow
	err := s.st.Reader().QueryRowContext(ctx, `SELECT id, kind, tmdb_id, tvdb_id, COALESCE(imdb_id, '')
		FROM title WHERE id = ?`, id).Scan(&t.id, &t.kind, &t.tmdbID, &t.tvdbID, &t.imdbID)
	if errors.Is(err, sql.ErrNoRows) {
		return fillResult{}, nil // deleted since it was picked
	}
	if err != nil {
		return fillResult{}, err
	}
	now := s.now()
	tmdbID, err := s.resolve(ctx, &t)
	switch {
	case errors.Is(err, tmdb.ErrNotFound) || (err == nil && tmdbID == 0):
		return fillResult{}, s.markError(ctx, id, 0, "no TMDb id, and none found by TVDB or IMDb id", now, NoIDRetry)
	case err != nil:
		return fillResult{}, s.failed(ctx, id, err, now)
	}
	var m Meta
	var movie *tmdb.Movie
	if t.kind == "series" {
		tv, err := s.tmdb.TVDetails(ctx, tmdbID)
		if errors.Is(err, tmdb.ErrNotFound) {
			return fillResult{}, s.markError(ctx, id, tmdbID, "TMDb has no TV id "+fmt.Sprint(tmdbID), now, NotFoundRetry)
		}
		if err != nil {
			return fillResult{}, s.failed(ctx, id, err, now)
		}
		m = fromTV(tv)
	} else {
		mv, err := s.tmdb.MovieDetails(ctx, tmdbID)
		if errors.Is(err, tmdb.ErrNotFound) {
			return fillResult{}, s.markError(ctx, id, tmdbID, "TMDb has no movie id "+fmt.Sprint(tmdbID), now, NotFoundRetry)
		}
		if err != nil {
			return fillResult{}, s.failed(ctx, id, err, now)
		}
		m = fromMovie(mv)
		movie = mv
		dates := movieDates(mv)
		m.Status = availability.Status(dates, now)
		m.physicalRelease = dates["physical"]
		if cinema := dates["theatrical"]; !cinema.IsZero() {
			m.ReleaseDate = cinema.Format(time.DateOnly)
		}
	}
	m.TitleID, m.TMDbID = id, tmdbID
	m.ArtHash = ArtHash(m.PosterPath, m.BackdropPath, m.LogoPath)
	refreshed := now
	m.RefreshedAt = &refreshed
	m.AttemptedAt = now
	m.RefreshAfter = now.Add(RefreshAge(t.kind, m, now))
	if t.kind == "series" {
		m.RefreshAfter = m.RefreshAfter.Add(spread(id))
	}
	if err := s.write(ctx, &m, movie); err != nil {
		return fillResult{}, err
	}
	return fillResult{found: true, poster: m.PosterPath}, nil
}

// resolve finds the TMDb id: the title's own, else by TVDB id (series) or IMDb id.
func (s *Service) resolve(ctx context.Context, t *titleRow) (int, error) {
	if t.tmdbID.Valid && t.tmdbID.Int64 > 0 {
		return int(t.tmdbID.Int64), nil
	}
	if t.kind == "series" {
		if t.tvdbID.Valid && t.tvdbID.Int64 > 0 {
			id, err := s.tmdb.FindTVByTVDB(ctx, int(t.tvdbID.Int64))
			if !errors.Is(err, tmdb.ErrNotFound) || t.imdbID == "" {
				return id, err
			}
		}
		if t.imdbID != "" {
			return s.tmdb.FindTVByIMDb(ctx, t.imdbID)
		}
		return 0, nil
	}
	if t.imdbID != "" {
		return s.tmdb.FindByIMDb(ctx, t.imdbID)
	}
	return 0, nil
}

// failed records a run-stopping error on the title (pushed back ErrorRetry, so one bad title can't block the rest)
// and returns it.
func (s *Service) failed(ctx context.Context, id int64, err error, now time.Time) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if merr := s.markError(ctx, id, 0, errText(err), now, ErrorRetry); merr != nil {
		return errors.Join(err, merr)
	}
	return err
}

func errText(err error) string {
	var r *gateway.Refused
	if errors.As(err, &r) {
		return "gateway: " + r.Error()
	}
	s := err.Error()
	if len(s) > 500 {
		s = s[:500] + "..."
	}
	return s
}

// markError records a failed try, keeping whatever the row held from its last good fetch.
func (s *Service) markError(ctx context.Context, id int64, tmdbID int, msg string, now time.Time, retry time.Duration) error {
	var tid any
	if tmdbID > 0 {
		tid = tmdbID
	}
	return s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO title_meta (title_id, tmdb_id, attempted_at, refresh_after, error)
			SELECT id, ?, ?, ?, ? FROM title WHERE id = ?
			ON CONFLICT (title_id) DO UPDATE SET tmdb_id = COALESCE(excluded.tmdb_id, title_meta.tmdb_id),
				attempted_at = excluded.attempted_at, refresh_after = excluded.refresh_after, error = excluded.error`,
			tid, store.Millis(now), store.Millis(now.Add(retry)), msg, id)
		return err
	})
}

func (s *Service) write(ctx context.Context, m *Meta, movie *tmdb.Movie) error {
	genres, err := json.Marshal(m.Genres)
	if err != nil {
		return err
	}
	return s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO title_meta (title_id, tmdb_id, overview, tagline, poster_path,
			backdrop_path, logo_path, genres, runtime_minutes, rating, rating_votes, certification, status, network,
			original_language, release_date, last_air_date, next_air_date, art_hash, refreshed_at, attempted_at,
			refresh_after, error)
			SELECT id, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '' FROM title WHERE id = ?
			ON CONFLICT (title_id) DO UPDATE SET tmdb_id = excluded.tmdb_id, overview = excluded.overview,
				tagline = excluded.tagline, poster_path = excluded.poster_path, backdrop_path = excluded.backdrop_path,
				logo_path = excluded.logo_path, genres = excluded.genres,
				runtime_minutes = COALESCE(excluded.runtime_minutes, title_meta.runtime_minutes),
				rating = excluded.rating, rating_votes = excluded.rating_votes, certification = excluded.certification,
				status = excluded.status, network = excluded.network, original_language = excluded.original_language,
				release_date = excluded.release_date, last_air_date = excluded.last_air_date,
				next_air_date = excluded.next_air_date, art_hash = excluded.art_hash,
				refreshed_at = excluded.refreshed_at, attempted_at = excluded.attempted_at,
				refresh_after = excluded.refresh_after, error = ''`,
			m.TMDbID, m.Overview, m.Tagline, m.PosterPath, m.BackdropPath, m.LogoPath, string(genres),
			nullPos(m.RuntimeMinutes), m.Rating, m.RatingVotes, m.Certification, m.Status, m.Network,
			m.OriginalLanguage, m.ReleaseDate, m.LastAirDate, m.NextAirDate, m.ArtHash, store.Millis(*m.RefreshedAt),
			store.Millis(m.AttemptedAt), store.Millis(m.RefreshAfter), m.TitleID)
		if err != nil || movie == nil {
			return err
		}
		// Radarr Movies/RefreshMovieService.cs:106-133 refreshes identity, keeping user settings.
		if _, err := tx.ExecContext(ctx, `UPDATE title SET name = ?, sort_name = ?, year = ?, runtime_minutes = COALESCE(?, runtime_minutes),
			imdb_id = ?, original_language = ?, updated_at = ? WHERE id = ?`, movie.Title, strings.ToLower(movie.Title),
			nullPos(movie.Year()), nullPos(movie.Runtime), movie.IMDbID, movie.OriginalLanguage, store.Millis(m.AttemptedAt), m.TitleID); err != nil {
			return err
		}
		if err := writeMovieDates(ctx, tx, m.TitleID, movie, m.AttemptedAt); err != nil {
			return err
		}
		// TMDb's collection membership (belongs_to_collection), which movie intake's collection discovery reads
		// (docs/design/arrview.md, Collections): natively added movies get it here, as imported ones do from enrich.
		if c := movie.BelongsToCollection; c != nil && c.ID > 0 {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO title_external_id (title_id, source, external_id)
				VALUES (?, 'tmdb_collection', ?)`, m.TitleID, strconv.Itoa(c.ID)); err != nil {
				return err
			}
		}
		// Imported arr names belong to their own sources; replace only TMDb's names.
		if _, err := tx.ExecContext(ctx, `DELETE FROM title_alias WHERE title_id = ? AND source IN ('tmdb', 'tmdb-original')`, m.TitleID); err != nil {
			return err
		}
		add := func(source, kind, name, language, country string) error {
			if strings.TrimSpace(name) == "" {
				return nil
			}
			_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO title_alias (title_id, source, kind, name, language, country)
				SELECT id, ?, ?, ?, ?, ? FROM title WHERE id = ?`, source, kind, name, language, country, m.TitleID)
			return err
		}
		if err := add("tmdb-original", "alternative", movie.OriginalTitle, movie.OriginalLanguage, ""); err != nil {
			return err
		}
		for _, a := range movie.AlternativeTitles.Titles {
			if err := add("tmdb", "alternative", a.Title, "", a.Country); err != nil {
				return err
			}
		}
		for _, tr := range movie.Translations.Translations {
			if err := add("tmdb", "translation", tr.Data.Title, tr.Language, tr.Country); err != nil {
				return err
			}
		}
		return nil
	})
}

func nullPos(n int) any {
	if n <= 0 {
		return nil
	}
	return n
}

// Meta is a title_meta row.
type Meta struct {
	physicalRelease  time.Time // Radarr PhysicalReleaseDate: physical, else cinema + 90 days, else unknown
	TitleID          int64
	TMDbID           int
	Overview         string
	Tagline          string
	PosterPath       string
	BackdropPath     string
	LogoPath         string
	Genres           []string
	RuntimeMinutes   int
	Rating           float64
	RatingVotes      int
	Certification    string
	Status           string
	Network          string
	OriginalLanguage string
	ReleaseDate      string
	LastAirDate      string
	NextAirDate      string
	ArtHash          string
	RefreshedAt      *time.Time
	AttemptedAt      time.Time
	RefreshAfter     time.Time
	Error            string
}

// CertificationCountry is whose certification title_meta keeps.
const CertificationCountry = "US"

func fromMovie(mv *tmdb.Movie) Meta {
	m := Meta{Overview: mv.Overview, Tagline: mv.Tagline, PosterPath: imagePath(mv.PosterPath),
		BackdropPath: imagePath(mv.BackdropPath), LogoPath: pickLogo(mv.Images.Logos, mv.OriginalLanguage),
		RuntimeMinutes: mv.Runtime, Rating: mv.VoteAverage, RatingVotes: mv.VoteCount,
		Certification: mv.Certification(CertificationCountry), Status: mv.Status,
		OriginalLanguage: mv.OriginalLanguage, ReleaseDate: mv.ReleaseDate, Genres: []string{}}
	for _, g := range mv.Genres {
		m.Genres = append(m.Genres, g.Name)
	}
	if len(mv.ProductionCompanies) > 0 {
		m.Network = mv.ProductionCompanies[0].Name
	}
	return m
}

func fromTV(tv *tmdb.TV) Meta {
	m := Meta{Overview: tv.Overview, Tagline: tv.Tagline, PosterPath: imagePath(tv.PosterPath),
		BackdropPath: imagePath(tv.BackdropPath), LogoPath: pickLogo(tv.Images.Logos, tv.OriginalLanguage),
		Rating: tv.VoteAverage, RatingVotes: tv.VoteCount, Certification: tv.Certification(CertificationCountry),
		Status: tv.Status, OriginalLanguage: tv.OriginalLanguage, ReleaseDate: tv.FirstAirDate,
		LastAirDate: tv.LastAirDate, Genres: []string{}}
	for _, g := range tv.Genres {
		m.Genres = append(m.Genres, g.Name)
	}
	if len(tv.EpisodeRunTime) > 0 {
		m.RuntimeMinutes = tv.EpisodeRunTime[0]
	} else if tv.LastEpisodeToAir != nil {
		m.RuntimeMinutes = tv.LastEpisodeToAir.Runtime
	}
	if len(tv.Networks) > 0 {
		m.Network = tv.Networks[0].Name
	}
	if tv.NextEpisodeToAir != nil {
		m.NextAirDate = tv.NextEpisodeToAir.AirDate
	}
	return m
}

// imagePath keeps a TMDb path only when the proxy could serve it ('/' plus a name FileRE accepts).
func imagePath(p string) string {
	if strings.HasPrefix(p, "/") && FileRE.MatchString(p[1:]) {
		return p
	}
	return ""
}

// pickLogo chooses a logo the proxy can serve (PNG or JPEG; TMDb also has SVG logos): English first, then the
// title's original language, then one without a language; the best voted within each.
func pickLogo(logos []tmdb.Image, origLang string) string {
	rank := func(lang string) int {
		switch lang {
		case "en":
			return 0
		case origLang:
			return 1
		case "":
			return 2
		}
		return 3
	}
	var cands []tmdb.Image
	for _, l := range logos {
		if imagePath(l.FilePath) != "" && rank(l.Language) < 3 {
			cands = append(cands, l)
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		ri, rj := rank(cands[i].Language), rank(cands[j].Language)
		if ri != rj {
			return ri < rj
		}
		return cands[i].VoteAverage > cands[j].VoteAverage
	})
	if len(cands) == 0 {
		return ""
	}
	return cands[0].FilePath
}

// ArtHash is a short hash of the art paths, the ?v= of image URLs; "" when there is no art.
func ArtHash(poster, backdrop, logo string) string {
	if poster == "" && backdrop == "" && logo == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(poster + "|" + backdrop + "|" + logo))
	return hex.EncodeToString(sum[:5])
}

// Refresh ages.
const (
	RecentAge = 3 * 24 * time.Hour
	YearAge   = 14 * 24 * time.Hour
	OldAge    = 90 * 24 * time.Hour
)

// RefreshAge is how long a freshly fetched row stays current (see the package doc).
func RefreshAge(kind string, m Meta, now time.Time) time.Duration {
	within := func(date string, d time.Duration) (bool, bool) {
		t, err := time.Parse("2006-01-02", date)
		if err != nil {
			return false, false
		}
		return now.Sub(t) <= d, true
	}
	if kind == "series" {
		switch m.Status {
		case "Returning Series", "In Production", "Planned", "Pilot":
			return RecentAge
		}
		if m.NextAirDate != "" {
			return RecentAge
		}
		last := m.LastAirDate
		if last == "" {
			last = m.ReleaseDate
		}
		recent, ok := within(last, 90*24*time.Hour)
		if !ok || recent {
			return RecentAge
		}
		if year, _ := within(last, 365*24*time.Hour); year {
			return YearAge
		}
		return OldAge
	}
	// Radarr Movies/ShouldRefreshMovie.cs:30-57 and MovieMetadata.cs:77-80 @ a96bf7c.
	if m.Status == "announced" || m.Status == "inCinemas" {
		return 12 * time.Hour
	}
	if m.Status == "released" {
		physical := m.physicalRelease
		if physical.IsZero() {
			if cinema, err := time.Parse(time.DateOnly, m.ReleaseDate); err == nil {
				physical = cinema.AddDate(0, 0, 90)
			}
		}
		if physical.IsZero() || !physical.Before(now.AddDate(0, 0, -30)) {
			return 12 * time.Hour
		}
	}
	return 180 * 24 * time.Hour
}

// spread adds up to 11 hours by title id, so titles filled together come due over half a day.
func spread(id int64) time.Duration { return time.Duration(id%12) * time.Hour }

// LoadMeta reads title_meta rows for the given titles.
func LoadMeta(ctx context.Context, db *sql.DB, ids []int64) (map[int64]Meta, error) {
	out := map[int64]Meta{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.QueryContext(ctx, `SELECT title_id, COALESCE(tmdb_id, 0), overview, tagline, poster_path,
		backdrop_path, logo_path, genres, COALESCE(runtime_minutes, 0), COALESCE(rating, 0), COALESCE(rating_votes, 0),
		certification, status, network, original_language, release_date, last_air_date, next_air_date, art_hash,
		refreshed_at, attempted_at, refresh_after, error FROM title_meta
		WHERE title_id IN (`+store.Placeholders(len(ids))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var m Meta
		var genres string
		var refreshed sql.NullInt64
		var attempted, after int64
		if err := rows.Scan(&m.TitleID, &m.TMDbID, &m.Overview, &m.Tagline, &m.PosterPath, &m.BackdropPath,
			&m.LogoPath, &genres, &m.RuntimeMinutes, &m.Rating, &m.RatingVotes, &m.Certification, &m.Status,
			&m.Network, &m.OriginalLanguage, &m.ReleaseDate, &m.LastAirDate, &m.NextAirDate, &m.ArtHash, &refreshed,
			&attempted, &after, &m.Error); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(genres), &m.Genres) != nil || m.Genres == nil {
			m.Genres = []string{}
		}
		if refreshed.Valid {
			t := store.FromMillis(refreshed.Int64)
			m.RefreshedAt = &t
		}
		m.AttemptedAt, m.RefreshAfter = store.FromMillis(attempted), store.FromMillis(after)
		out[m.TitleID] = m
	}
	return out, rows.Err()
}
