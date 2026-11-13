// Package tvrefresh keeps Theatarr's own episode list of every series (docs/design/tv-metadata.md), as Sonarr's
// RefreshSeriesService and RefreshEpisodeService do (src/NzbDrone.Core/Tv @ cab419a): episodes from TVDB v4
// (metadata.sources.tvdb), or from TMDb for a series TVDB lacks (not anime), stored with their source id, numbers,
// absolute number, air date and time, runtime and name; TVDB slots keep their row even when ids change; dropped episodes
// are deleted unless something hangs off them (a file, a link, a download), then marked removed.
// Each series version's per-episode states follow its durable monitoring intent (series_monitor: monitor new items
// all or none, and its season choices), so new episodes of a monitored series come in monitored and the wanted loop
// searches them.
//
// # When
//
// A series added in Theatarr is refreshed within a minute (series_meta.added_at); its searches wait until that refresh
// finishes (acquire holds them in series_search_wait), as Sonarr searches only after the add's refresh. A sweep every
// metadata.series.interval refreshes the due series, at most per_run: never refreshed first, then Sonarr's
// ShouldRefreshSeries (more than 30 days old; an aired episode still "TBA" after 6 hours; otherwise not within 12
// hours; a continuing or upcoming series; an ended one whose last episode aired within 30 days). The hourly TVDB
// /updates read queues the series it names at once. POST titles/{id}/refresh and the arr RefreshSeries command
// queue a refresh that skips the cache.
//
// # Matching
//
// TVDB rows match by season and number (Sonarr RefreshEpisodeService.cs:56-79); TMDb rows match by source id first.
//
// Each refresh updates the title, sort title, year and runtime, preserving user series type and monitoring.
// Add-time monitor modes wait for episodes, then apply once. TVDB names become all-season aliases; meta.scene refreshes Sonarr/XEM seasonal names
// and XEM episode numbers every three hours through configured metadata sources. The existing catalog stores display
// names only, so separate searchTitle, filterRegex and searchMode fields remain unsupported by its readers.
// Sort names use Theatarr's lowercase convention (the arrs receive sort titles from their metadata servers).
// Anime: the anime pipeline (internal/metadata/anime) keeps its franchise mappings; the refresh
// stores TVDB's episodes and absolute numbers (episode_number scheme absolute_tvdb, source tvdb), and TMDb is never
// used for anime.
package tvrefresh

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/metadata/anime/live"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/metadata/tvdb"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// JobKind refreshes one series (Payload.TitleID) or runs a sweep over the due ones.
const JobKind = "meta.series"

// Payload is a meta.series job's payload.
type Payload struct {
	TitleID int64 `json:"title_id,omitempty"`
	// Force reads the source fresh, past the response cache (an on-demand refresh).
	Force bool `json:"force,omitempty"`
}

// TVDB is the part of tvdb.Client the refresh uses.
type TVDB interface {
	Series(ctx context.Context, id int) (*tvdb.Series, error)
	SeriesEpisodes(ctx context.Context, id int, specialsAirs bool) (*tvdb.Series, []tvdb.Episode, error)
	Invalidate(ctx context.Context, seriesID int) (int64, error)
}

// TMDb is the part of tmdb.Client the refresh uses for series TVDB lacks.
type TMDb interface {
	TVShow(ctx context.Context, tvID int) (*tmdb.TVSeries, error)
	TVSeasonEpisodes(ctx context.Context, tvID, season int) (*tmdb.TVSeason, error)
	FindTVByTVDB(ctx context.Context, tvdbID int) (int, error)
}

var (
	_ TVDB = (*tvdb.Client)(nil)
	_ TMDb = (*tmdb.Client)(nil)
)

// Searches takes the searches that waited for a series' first refresh (acquire.Acquirer).
type Searches interface {
	ReleaseWaiting(ctx context.Context, titleID int64) error
}

// ErrOff is returned when neither TVDB nor TMDb is configured, or metadata.series is off.
var ErrOff = errors.New("tvrefresh: series metadata refresh is off (no TVDB or TMDb key, or metadata.series off)")

// ErrNoTitle is returned for a title that does not exist or is not a series.
var ErrNoTitle = errors.New("tvrefresh: no such series")

// Config is what New needs.
type Config struct {
	Store  *store.Store
	Policy *policy.Policy
	Queue  *jobs.Queue
	// Doer shares the metadata gateway with the source clients. Defaults to TVDB's gateway.
	Doer live.Doer
	// TVDB and TMDb are the sources; nil for one that is not configured.
	TVDB TVDB
	TMDb TMDb
	// Searches gets the held searches after a first refresh (set later with SetSearches when built after).
	Searches Searches
	Logger   *slog.Logger
	// Now is the clock. Default time.Now.
	Now func() time.Time
	// Poll is how often Run looks for new series. Default 1m.
	Poll time.Duration
	// FirstDelay is the wait before the first sweep. Default 2m.
	FirstDelay time.Duration
}

// Service refreshes series metadata.
type Service struct {
	st     *store.Store
	pol    *policy.Policy
	q      *jobs.Queue
	tvdb   TVDB
	tmdb   TMDb
	search Searches
	set    policy.SeriesMetaSettings
	log    *slog.Logger
	now    func() time.Time
	poll   time.Duration
	delay  time.Duration

	sceneMu   sync.Mutex
	sceneDoer live.Doer
	lookupMu  sync.Mutex
	lookups   map[int]lookupEntry
}

// New returns a service. Without a source, or with metadata.series off, it is built but does nothing.
func New(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Policy == nil {
		return nil, errors.New("tvrefresh: store and policy are required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Poll <= 0 {
		cfg.Poll = time.Minute
	}
	if cfg.FirstDelay <= 0 {
		cfg.FirstDelay = 2 * time.Minute
	}
	if cfg.Doer == nil {
		if c, ok := cfg.TVDB.(interface{ MetadataDoer() live.Doer }); ok {
			cfg.Doer = c.MetadataDoer()
		}
	}
	return &Service{sceneDoer: cfg.Doer, st: cfg.Store, pol: cfg.Policy, q: cfg.Queue, tvdb: cfg.TVDB, tmdb: cfg.TMDb, search: cfg.Searches,
		set: cfg.Policy.SeriesMetaSettings(), log: cfg.Logger.With("component", "tvrefresh"), now: cfg.Now,
		poll: cfg.Poll, delay: cfg.FirstDelay}, nil
}

// SetSearches sets where held searches go after a first refresh.
func (s *Service) SetSearches(x Searches) { s.search = x }

// Enabled reports whether refreshes run: a source is configured and metadata.series is on.
func (s *Service) Enabled() bool {
	return s != nil && s.set.Enabled && (s.tvdb != nil || s.tmdb != nil)
}

// RegisterJobs registers the series and scene handlers when the refresh is enabled.
func (s *Service) RegisterJobs(w *jobs.Worker) {
	if !s.Enabled() {
		return
	}
	w.Handle(JobKind, s.handle)
	w.Handle(sceneJob, func(ctx context.Context, _ *jobs.Job) error { return s.RefreshScene(ctx) })
}

// RegisterRuntimeJobs registers the series and scene handlers for theatarr serve with settings in the database: each
// job runs on the service of the settings generation current when it starts, and a job that starts while the
// refresh is off is dropped.
func RegisterRuntimeJobs(w *jobs.Worker, resolve func(context.Context) (context.Context, *Service)) {
	w.Handle(JobKind, func(ctx context.Context, j *jobs.Job) error {
		ctx, s := resolve(ctx)
		if !s.Enabled() {
			return nil
		}
		return s.handle(ctx, j)
	})
	w.Handle(sceneJob, func(ctx context.Context, _ *jobs.Job) error {
		ctx, s := resolve(ctx)
		if !s.Enabled() {
			return nil
		}
		return s.RefreshScene(ctx)
	})
}

func (s *Service) handle(ctx context.Context, j *jobs.Job) error {
	var p Payload
	if err := j.Decode(&p); err != nil {
		return jobs.Permanent(fmt.Errorf("tvrefresh: payload: %w", err))
	}
	if p.TitleID > 0 {
		_, err := s.RefreshTitle(ctx, p.TitleID, p.Force)
		if errors.Is(err, ErrNoTitle) {
			return jobs.Permanent(err)
		}
		return err
	}
	_, err := s.Sweep(ctx)
	return err
}

func titleKey(id int64) string { return fmt.Sprintf("%s:title:%d", JobKind, id) }

// Enqueue queues a refresh of one series now (acquire calls it for a held search). Without a queue or a source it
// does nothing.
func (s *Service) Enqueue(ctx context.Context, titleID int64) error {
	if !s.Enabled() || s.q == nil {
		return nil
	}
	_, _, err := s.q.Enqueue(ctx, JobKind, Payload{TitleID: titleID}, jobs.EnqueueOptions{DedupeKey: titleKey(titleID),
		Priority: 2, MaxAttempts: 3})
	return err
}

// Refresh queues an on-demand refresh of one series that reads its source fresh, and returns the job id.
func (s *Service) Refresh(ctx context.Context, titleID int64) (int64, error) {
	if !s.Enabled() || s.q == nil {
		return 0, ErrOff
	}
	var one int
	err := s.st.Reader().QueryRowContext(ctx, `SELECT 1 FROM title WHERE id = ? AND kind = 'series'`, titleID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNoTitle
	}
	if err != nil {
		return 0, err
	}
	id, _, err := s.q.Enqueue(ctx, JobKind, Payload{TitleID: titleID, Force: true}, jobs.EnqueueOptions{
		DedupeKey: titleKey(titleID), Priority: 1, MaxAttempts: 3})
	return id, err
}

// Changed queues a refresh of our series among these TVDB series ids (from /updates) that have been refreshed
// before; the others wait for the sweep. It returns how many it queued.
func (s *Service) Changed(ctx context.Context, tvdbIDs []int) (int, error) {
	if !s.Enabled() || s.q == nil || s.tvdb == nil {
		return 0, nil
	}
	n := 0
	for len(tvdbIDs) > 0 {
		chunk := tvdbIDs[:min(len(tvdbIDs), 500)]
		tvdbIDs = tvdbIDs[len(chunk):]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := s.st.Reader().QueryContext(ctx, `SELECT t.id FROM title t JOIN series_meta m ON m.title_id = t.id
			WHERE t.kind = 'series' AND m.source = 'tvdb' AND m.refreshed_at IS NOT NULL AND t.tvdb_id IN (`+
			store.Placeholders(len(chunk))+`)`, args...)
		if err != nil {
			return n, err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return n, err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return n, err
		}
		for _, id := range ids {
			if _, _, err := s.q.Enqueue(ctx, JobKind, Payload{TitleID: id}, jobs.EnqueueOptions{
				DedupeKey: titleKey(id), MaxAttempts: 3}); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// Run schedules refreshes until ctx ends: every Poll, a refresh for each series added since its last try; a sweep
// FirstDelay after start and then every metadata.series.interval; scene augmentation every three hours.
func (s *Service) Run(ctx context.Context) {
	if !s.Enabled() || s.q == nil {
		s.log.Info("series metadata refresh off", "tvdb", s.tvdb != nil, "tmdb", s.tmdb != nil, "enabled", s.set.Enabled)
		return
	}
	s.log.Info("series metadata refresh scheduled", "interval", s.set.Interval.String(), "per_run", s.set.PerRun,
		"tvdb", s.tvdb != nil, "tmdb", s.tmdb != nil)
	next := s.now().Add(s.delay)
	nextScene := next
	t := time.NewTicker(min(s.poll, s.delay))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := s.enqueueAdded(ctx); err != nil {
			s.log.Warn("series metadata: new series", "err", err)
		}
		if now := s.now(); !now.Before(nextScene) {
			if _, _, err := s.q.Enqueue(ctx, sceneJob, Payload{}, jobs.EnqueueOptions{DedupeKey: sceneJob, MaxAttempts: 3}); err != nil {
				s.log.Warn("scene refresh: enqueue", "err", err)
			} else {
				nextScene = now.Add(sceneAge)
			}
		}
		if now := s.now(); !now.Before(next) {
			if _, _, err := s.q.Enqueue(ctx, JobKind, Payload{}, jobs.EnqueueOptions{DedupeKey: JobKind,
				MaxAttempts: 1}); err != nil {
				s.log.Warn("series metadata: enqueue sweep", "err", err)
				continue
			}
			next = now.Add(s.set.Interval)
		}
		t.Reset(s.poll)
	}
}

// retryAdded is how long a failed first refresh of an added series waits before Run queues it again.
const retryAdded = 10 * time.Minute

// enqueueAdded queues the series added since their last try (or whose try failed a while ago).
func (s *Service) enqueueAdded(ctx context.Context) error {
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT title_id FROM series_meta WHERE added_at IS NOT NULL
		AND (attempted_at IS NULL OR attempted_at < added_at OR attempted_at < ?) ORDER BY added_at`,
		store.Millis(s.now().Add(-retryAdded)))
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.Enqueue(ctx, id); err != nil {
			return err
		}
	}
	return nil
}
