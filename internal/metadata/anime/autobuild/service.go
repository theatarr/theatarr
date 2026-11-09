package autobuild

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/anime/animestore"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// JobKind builds one franchise: a discovery for a TVDB series, a rebuild of a stored franchise, or an ad-hoc seed.
const JobKind = "anime_build"

// Payload is an anime_build job's payload; exactly one of TVDBID (with TitleID), Slug, or AniListID is the request.
type Payload struct {
	TitleID   int64  `json:"title_id,omitempty"`
	TVDBID    int    `json:"tvdb_id,omitempty"`
	Slug      string `json:"slug,omitempty"`
	AniListID int    `json:"anilist_id,omitempty"`
	Name      string `json:"name,omitempty"`
}

// Dedupe keys: one queued or running job per TVDB series, franchise and seed.
func TVDBKey(id int) string      { return fmt.Sprintf("%s:tvdb:%d", JobKind, id) }
func SlugKey(slug string) string { return JobKind + ":slug:" + slug }
func SeedKey(anilist int) string { return fmt.Sprintf("%s:seed:%d", JobKind, anilist) }
func jobOptions(key string) jobs.EnqueueOptions {
	return jobs.EnqueueOptions{DedupeKey: key, MaxAttempts: 2, Priority: -1}
}

// Errors the enqueue calls return.
var (
	ErrNotConfigured = errors.New("anime builds need the anime metadata sources (metadata.sources)")
	ErrNoFranchise   = errors.New("no such franchise")
	ErrNoSeries      = errors.New("no anime series with this TVDB id")
	ErrSlugTaken     = errors.New("another franchise has this slug")
	ErrInvalid       = errors.New("invalid build request")
)

// Config is one settings generation's anime builder.
type Config struct {
	Policy *policy.Policy
	Store  *store.Store
	Queue  *jobs.Queue
	// Live is the live source set; nil when the policy lacks a required source (Off says which).
	Live *Live
	// Off says why builds cannot run (Live is nil).
	Off    string
	Logger *slog.Logger
	Now    func() time.Time
	// Poll is the discovery loop's tick; default a minute. FirstDelay is the wait before its first pass; default 2m.
	Poll, FirstDelay time.Duration
	// Build replaces anime.Build (tests).
	Build func(ctx context.Context, src *anime.Sources, f anime.Franchise, opt anime.Options) (*anime.Result, error)
	// Sources replaces Live.Sources (tests).
	Sources func(ctx context.Context) (*anime.Sources, error)
}

// Service builds franchises for one settings generation.
type Service struct {
	cfg Config
	set policy.AnimeBuildSettings
	log *slog.Logger
}

// New returns the builder.
func New(cfg Config) *Service {
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
	if cfg.Sources == nil && cfg.Live != nil {
		cfg.Sources = cfg.Live.Sources
	}
	if cfg.Build == nil {
		cfg.Build = anime.Build
	}
	return &Service{cfg: cfg, set: cfg.Policy.AnimeBuildSettings(), log: cfg.Logger.With("component", "anime-build")}
}

// Ready reports whether builds can run (the sources are configured).
func (s *Service) Ready() bool { return s != nil && s.cfg.Sources != nil }

// Status is the builder's state for the UI.
type Status struct {
	AutoBuild       bool   `json:"auto_build"`
	Ready           bool   `json:"ready"`
	Off             string `json:"off,omitempty"`
	RefreshInterval string `json:"refresh_interval"`
}

// Status reports the settings and whether builds can run.
func (s *Service) Status() Status {
	if s == nil {
		return Status{Off: "anime builds are not set up"}
	}
	return Status{AutoBuild: s.set.AutoBuild, Ready: s.Ready(), Off: s.cfg.Off,
		RefreshInterval: s.set.RefreshInterval.String()}
}

// RefreshInterval is metadata.anime.refresh_interval.
func (s *Service) RefreshInterval() time.Duration { return s.set.RefreshInterval }

// building lets one build run at a time in the process, across settings generations.
var building sync.Mutex

// wake tells the loop a build finished, so the next one starts without waiting for the tick.
var wake = make(chan struct{}, 1)

func signalWake() {
	select {
	case wake <- struct{}{}:
	default:
	}
}

// RegisterRuntimeJobs registers the anime_build handler; each job runs on the builder resolve returns (the settings
// generation in force). A job that starts while builds cannot run fails with the reason.
func RegisterRuntimeJobs(w *jobs.Worker, resolve func(context.Context) (context.Context, *Service)) {
	w.Handle(JobKind, func(ctx context.Context, j *jobs.Job) error {
		ctx, s := resolve(ctx)
		if s == nil || !s.Ready() {
			off := "anime builds are not set up"
			if s != nil && s.cfg.Off != "" {
				off = s.cfg.Off
			}
			return jobs.Permanent(errors.New(off))
		}
		return s.Handle(ctx, j)
	})
}

// Handle runs one anime_build job.
func (s *Service) Handle(ctx context.Context, j *jobs.Job) error {
	var p Payload
	if err := j.Decode(&p); err != nil {
		return jobs.Permanent(fmt.Errorf("anime_build: payload: %w", err))
	}
	if !building.TryLock() {
		return jobs.Later(time.Minute)
	}
	defer func() {
		building.Unlock()
		signalWake()
	}()
	err := s.run(ctx, p)
	if err != nil && p.TitleID > 0 && ctx.Err() == nil {
		if rerr := s.record(ctx, p.TitleID, p.TVDBID, OutcomeFailed, "", err.Error()); rerr != nil {
			s.log.Warn("anime: record outcome", "err", rerr)
		}
	}
	return err
}

func (s *Service) run(ctx context.Context, p Payload) error {
	src, err := s.cfg.Sources(ctx)
	if err != nil {
		return err
	}
	shipped, err := shippedFranchises(s.cfg.Policy.OverridesPath())
	if err != nil {
		return jobs.Permanent(err)
	}
	var f anime.Franchise
	origin, tvdb := OriginManual, 0
	switch {
	case p.TVDBID > 0:
		d := &Discoverer{Store: s.cfg.Store, Lists: src.Lists, AniList: src.AniList, Shipped: shipped}
		name, err := s.titleName(ctx, p.TitleID)
		if err != nil {
			return err
		}
		seed, err := d.Discover(ctx, p.TVDBID, name)
		if err != nil {
			return err
		}
		if seed.Outcome != "" {
			detail := ""
			if seed.Outcome == OutcomeNoMapping {
				detail = fmt.Sprintf("no AniList mapping for TVDB %d", p.TVDBID)
			}
			s.log.Info("anime: discovery", "tvdb", p.TVDBID, "outcome", seed.Outcome, "franchise", seed.CoveredBy)
			return s.record(ctx, p.TitleID, p.TVDBID, seed.Outcome, seed.CoveredBy, detail)
		}
		f, origin, tvdb = seed.Franchise, seed.Origin, p.TVDBID
	case p.Slug != "" && p.AniListID == 0:
		var err error
		if f, origin, tvdb, err = s.stored(ctx, p.Slug, shipped); err != nil {
			return err
		}
	case p.AniListID > 0:
		f = anime.Franchise{Slug: p.Slug, Name: p.Name, SeedAniList: p.AniListID}
		for _, x := range shipped {
			if x.SeedAniList == p.AniListID {
				f, origin = x, OriginList // the shipped franchise of that seed, under its slug
			}
		}
	default:
		return jobs.Permanent(errors.New("anime_build: empty payload"))
	}
	t0 := s.cfg.Now()
	res, err := s.build(ctx, src, f)
	if err != nil {
		return err
	}
	res.Config["origin"] = origin
	if tvdb > 0 {
		res.Config["tvdb_id"] = tvdb
	}
	if p.TVDBID > 0 || p.AniListID > 0 {
		// A new franchise: builds run one at a time, so ownership read here holds until the save.
		covered, err := s.owned(ctx, f, res, shipped)
		if err != nil {
			return err
		}
		if covered != "" {
			s.log.Info("anime: discovery", "tvdb", p.TVDBID, "outcome", OutcomeCovered, "franchise", covered)
			if p.TitleID > 0 {
				return s.record(ctx, p.TitleID, p.TVDBID, OutcomeCovered, covered, "")
			}
			return nil
		}
		if x := shippedIn(res, shipped, f.Slug); x != nil && origin != OriginList {
			// The crawl reached a shipped franchise's seed: build that franchise instead, under its slug.
			s.log.Info("anime: crawl reached a shipped franchise", "franchise", f.Slug, "shipped", x.Slug)
			f, origin = *x, OriginList
			if res, err = s.build(ctx, src, f); err != nil {
				return err
			}
			res.Config["origin"] = origin
			if tvdb > 0 {
				res.Config["tvdb_id"] = tvdb
			}
			if covered, err := s.owned(ctx, f, res, shipped); err != nil || covered != "" {
				if err == nil {
					err = jobs.Permanent(fmt.Errorf("shipped franchise %s: AniList %d is built as %s", x.Slug, x.SeedAniList, covered))
				}
				return err
			}
		}
	}
	// A run that lost its lease must not write: another run owns the job now.
	if err := jobs.Fence(ctx); err != nil {
		return err
	}
	saved, err := (&animestore.Store{DB: s.cfg.Store, Now: s.cfg.Now}).Save(ctx, res, "live", false)
	if err != nil {
		return err
	}
	if saved.Applied {
		s.log.Info("anime: franchise built", "franchise", f.Slug, "origin", origin, "review_items", saved.NewReview,
			"took", s.cfg.Now().Sub(t0).Round(time.Second))
	} else {
		s.log.Info("anime: refresh held for review", "franchise", f.Slug, "review_items", saved.NewReview, "why", saved.Held)
	}
	if p.TitleID > 0 {
		return s.record(ctx, p.TitleID, p.TVDBID, OutcomeBuilt, f.Slug, "")
	}
	return nil
}

// build crawls franchise f with its overrides.
func (s *Service) build(ctx context.Context, src *anime.Sources, f anime.Franchise) (*anime.Result, error) {
	ov, err := anime.LoadOverrides(s.cfg.Policy.OverridesPath(), f.Slug)
	if err != nil {
		return nil, jobs.Permanent(err)
	}
	res, err := s.cfg.Build(ctx, src, f, anime.Options{Overrides: ov})
	if err != nil {
		return nil, err
	}
	if res.Config == nil {
		res.Config = map[string]any{}
	}
	return res, nil
}

// owned checks a new franchise's ownership again just before its save: another franchise built from its seed covers
// it (that franchise's slug), and a slug a stored franchise of another seed has, or the shipped list reserves for
// one, is ErrSlugTaken.
func (s *Service) owned(ctx context.Context, f anime.Franchise, res *anime.Result, shipped []anime.Franchise) (string, error) {
	owner, err := seedOwner(ctx, s.cfg.Store, f.SeedAniList)
	if err != nil || owner != "" && owner != f.Slug {
		return owner, err
	}
	var seed int
	err = s.cfg.Store.Reader().QueryRowContext(ctx, `SELECT seed_anilist FROM anime_franchise WHERE slug = ?`, f.Slug).Scan(&seed)
	if err == nil && seed != f.SeedAniList {
		return "", jobs.Permanent(fmt.Errorf("%s: %w", f.Slug, ErrSlugTaken))
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	for _, x := range shipped {
		if x.Slug == f.Slug && x.SeedAniList != f.SeedAniList {
			return "", jobs.Permanent(fmt.Errorf("%s: %w (shipped)", f.Slug, ErrSlugTaken))
		}
	}
	return "", nil
}

// shippedIn is the shipped franchise (other than slug) whose seed the crawl reached, or nil.
func shippedIn(res *anime.Result, shipped []anime.Franchise, slug string) *anime.Franchise {
	_, anilist := animestore.Covers(res)
	for i := range shipped {
		if x := &shipped[i]; x.Slug != slug && anilist[x.SeedAniList] {
			return x
		}
	}
	return nil
}

func (s *Service) titleName(ctx context.Context, titleID int64) (string, error) {
	var name string
	err := s.cfg.Store.Reader().QueryRowContext(ctx, `SELECT name FROM title WHERE id = ?`, titleID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", jobs.Permanent(fmt.Errorf("title %d: %w", titleID, ErrNoSeries))
	}
	return name, err
}

// stored is a stored franchise's definition: the shipped list's entry for its slug, else the stored one.
func (s *Service) stored(ctx context.Context, slug string, shipped []anime.Franchise) (anime.Franchise, string, int, error) {
	var f anime.Franchise
	var cfg string
	err := s.cfg.Store.Reader().QueryRowContext(ctx, `SELECT slug, name, seed_anilist, scope, config FROM anime_franchise
		WHERE slug = ?`, slug).Scan(&f.Slug, &f.Name, &f.SeedAniList, &f.Scope, &cfg)
	if errors.Is(err, sql.ErrNoRows) {
		return f, "", 0, jobs.Permanent(fmt.Errorf("%s: %w", slug, ErrNoFranchise))
	}
	if err != nil {
		return f, "", 0, err
	}
	var c struct {
		Origin    string `json:"origin"`
		TVDB      int    `json:"tvdb_id"`
		LineDepth *int   `json:"line_depth"`
		ExtraTVDB []int  `json:"extra_tvdb"`
	}
	_ = json.Unmarshal([]byte(cfg), &c)
	f.LineDepth, f.ExtraTVDB = c.LineDepth, c.ExtraTVDB
	origin := c.Origin
	for _, x := range shipped {
		if x.Slug == slug {
			f, origin = x, OriginList
		}
	}
	if origin == "" {
		origin = OriginManual
	}
	return f, origin, c.TVDB, nil
}

// record stores a title's discovery outcome.
func (s *Service) record(ctx context.Context, titleID int64, tvdb int, outcome, slug, detail string) error {
	return record(ctx, s.cfg.Store, titleID, tvdb, outcome, slug, detail, s.cfg.Now())
}

func record(ctx context.Context, st *store.Store, titleID int64, tvdb int, outcome, slug, detail string, now time.Time) error {
	return st.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO anime_discovery (title_id, tvdb_id, outcome, franchise, detail, tried_at)
			VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (title_id) DO UPDATE SET tvdb_id = excluded.tvdb_id,
			outcome = excluded.outcome, franchise = excluded.franchise, detail = excluded.detail, tried_at = excluded.tried_at`,
			titleID, tvdb, outcome, slug, detail, store.Millis(now))
		return err
	})
}

// EnqueueDiscover queues a discovery and build for the anime series with TVDB id tvdbID.
func (s *Service) EnqueueDiscover(ctx context.Context, tvdbID int) (int64, error) {
	if !s.Ready() {
		return 0, ErrNotConfigured
	}
	var titleID int64
	err := s.cfg.Store.Reader().QueryRowContext(ctx, `SELECT id FROM title WHERE kind = 'series' AND anime = 1 AND tvdb_id = ?
		ORDER BY id LIMIT 1`, tvdbID).Scan(&titleID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNoSeries
	}
	if err != nil {
		return 0, err
	}
	return s.enqueueDiscover(ctx, titleID, tvdbID)
}

func (s *Service) enqueueDiscover(ctx context.Context, titleID int64, tvdbID int) (int64, error) {
	id, _, err := s.cfg.Queue.Enqueue(ctx, JobKind, Payload{TitleID: titleID, TVDBID: tvdbID}, jobOptions(TVDBKey(tvdbID)))
	if err != nil {
		return 0, err
	}
	return id, record(ctx, s.cfg.Store, titleID, tvdbID, OutcomeQueued, "", "", s.cfg.Now())
}

// EnqueueRebuild queues a rebuild of a stored franchise.
func (s *Service) EnqueueRebuild(ctx context.Context, slug string) (int64, error) {
	if !s.Ready() {
		return 0, ErrNotConfigured
	}
	ok, err := franchiseExists(ctx, s.cfg.Store, slug)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, ErrNoFranchise
	}
	id, _, err := s.cfg.Queue.Enqueue(ctx, JobKind, Payload{Slug: slug}, jobOptions(SlugKey(slug)))
	return id, err
}

// EnqueueSeed queues an ad-hoc build from an AniList seed (the CLI's -seed) and returns the slug the build uses. The
// seed decides first: a seed a stored franchise was built from, or has as an entry, queues a rebuild of that
// franchise; a shipped franchise's seed builds it under its own slug. Else slug defaults to the name's slug, else
// anilist-<id>; a slug a stored franchise of another seed has, the shipped list reserves, or a queued build of
// another seed claims is ErrSlugTaken. A seed already queued keeps that job and its slug.
func (s *Service) EnqueueSeed(ctx context.Context, anilistID int, slug, name string) (int64, string, error) {
	if !s.Ready() {
		return 0, "", ErrNotConfigured
	}
	if anilistID <= 0 {
		return 0, "", fmt.Errorf("%w: anilist_id must be positive", ErrInvalid)
	}
	if slug == "" {
		slug = Slugify(name)
	}
	if slug != "" && Slugify(slug) != slug {
		return 0, "", fmt.Errorf("%w: slug %q: use lower-case letters, digits and '-'", ErrInvalid, slug)
	}
	owner, err := seedOwner(ctx, s.cfg.Store, anilistID)
	if err == nil && owner == "" {
		owner, err = hasEntry(ctx, s.cfg.Store, nil, []int{anilistID})
	}
	if err != nil {
		return 0, "", err
	}
	if owner != "" {
		id, err := s.EnqueueRebuild(ctx, owner)
		return id, owner, err
	}
	shipped, err := shippedFranchises(s.cfg.Policy.OverridesPath())
	if err != nil {
		return 0, "", err
	}
	for _, x := range shipped {
		if x.SeedAniList == anilistID {
			slug, name = x.Slug, x.Name
		}
	}
	if slug == "" {
		slug = fmt.Sprintf("anilist-%d", anilistID)
	}
	if name == "" {
		name = slug
	}
	// a build of this seed already queued or running keeps its job and slug
	db := s.cfg.Store.Reader()
	var id int64
	var payload string
	err = db.QueryRowContext(ctx, `SELECT id, payload FROM job WHERE kind = ? AND dedupe_key = ?
		AND state IN ('queued', 'running') ORDER BY id LIMIT 1`, JobKind, SeedKey(anilistID)).Scan(&id, &payload)
	if err == nil {
		var p Payload
		_ = json.Unmarshal([]byte(payload), &p)
		return id, p.Slug, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, "", err
	}
	taken, err := slugReserved(ctx, s.cfg.Store, shipped, slug, anilistID)
	if err == nil && !taken {
		var n int
		err = db.QueryRowContext(ctx, `SELECT count(*) FROM job WHERE kind = ? AND state IN ('queued', 'running')
			AND json_extract(payload, '$.slug') = ? AND COALESCE(json_extract(payload, '$.anilist_id'), 0) <> ?`,
			JobKind, slug, anilistID).Scan(&n)
		taken = n > 0
	}
	if err != nil {
		return 0, "", err
	}
	if taken {
		return 0, "", ErrSlugTaken
	}
	id, _, err = s.cfg.Queue.Enqueue(ctx, JobKind, Payload{Slug: slug, AniListID: anilistID, Name: name}, jobOptions(SeedKey(anilistID)))
	return id, slug, err
}
