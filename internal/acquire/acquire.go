// Package acquire runs the searches of the versions in mode active and hands what they accept to the router: the
// arr views' search commands (Submit) and the search schedule (Round, Run; schedule.go). A shared search has one root
// acquire_run and one child row per served version-target; it searches once, decides each target and enqueues each
// accepted route.
//
// It never searches for a version in shadow mode: Submit and the loop drop every version that is not active, and the
// job checks the mode again before it searches. Whether the router may write is still the router's gate alone
// (router.enabled and the version's mode); with the switch off a route is a dry run recorded in router_plan.
package acquire

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/theatarr/theatarr/internal/availability"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/journal/snapshot"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
)

// Job kinds.
const (
	JobSearch = "acquire.search"
	JobWanted = "acquire.wanted"
)

// Sources of a run.
const (
	SourceCommand = "command"
	SourceWanted  = "wanted"
)

// Reasons a run searches: Only in a Selection.
const (
	OnlyMissing = "missing"
	OnlyCutoff  = "cutoff"
)

// MaxBulk caps the searches one command over a whole view (MissingMoviesSearch and the like) starts; the items
// searched longest ago go first.
const MaxBulk = 100

// Enqueuer is the router's job entry (router.Router.Enqueue, EnqueueTx).
type Enqueuer interface {
	Enqueue(ctx context.Context, req router.Request, at time.Time) (int64, error)
	EnqueueTx(ctx context.Context, tx *sql.Tx, req router.Request, at time.Time) (int64, error)
}

// SearchFunc searches and decides one target (router.SearchRequest with the engine and the indexer service).
type SearchFunc func(ctx context.Context, t decide.Title, v decide.Version, existing *decide.ExistingFile,
	ss *decide.SeriesSearch) (*router.Searched, error)

// SearchBatchFunc searches the union of these targets' indexer queries once and decides each target separately.
type SearchBatchFunc func(ctx context.Context, t decide.Title, targets []router.SearchTarget) (map[string]*router.Searched, error)

// Config is what New needs. Store, Policy, Queue and Router are required; Search, or Engine and Indexers.
type Config struct {
	Store    *store.Store
	Policy   *policy.Policy
	Queue    *jobs.Queue
	Router   Enqueuer
	Engine   *decide.Engine
	Indexers *indexer.Service
	// Search replaces router.SearchRequest (tests).
	Search SearchFunc
	// SearchBatch replaces router.SearchRequests (tests).
	SearchBatch SearchBatchFunc
	// RuntimeGeneration identifies the immutable settings snapshot captured by a request.
	RuntimeGeneration func() int64
	// CaptureRuntime pins a job or request to the current immutable runtime.
	CaptureRuntime func(context.Context) context.Context
	// RuntimeEngine selects the decision engine pinned to the request's settings generation.
	RuntimeEngine func(context.Context) *decide.Engine
	// Metadata, when set, holds the searches of a series added in Theatarr until its first metadata refresh finishes
	// (internal/metadata/tvrefresh); nil searches at once.
	Metadata MetadataWait
	Logger   *slog.Logger
	Now      func() time.Time
}

// MetadataWait is the series metadata refresh as the acquirer uses it (tvrefresh.Service).
type MetadataWait interface {
	// Enqueue starts a refresh of the title now.
	Enqueue(ctx context.Context, titleID int64) error
}

// Acquirer holds the jobs.
type Acquirer struct {
	st                *store.Store
	pol               *policy.Policy
	q                 *jobs.Queue
	rt                Enqueuer
	search            SearchFunc
	searchBatch       SearchBatchFunc
	eng               *decide.Engine
	meta              MetadataWait
	runtimeGeneration func() int64
	runtimeCapture    func(context.Context) context.Context
	runtimeEngine     func(context.Context) *decide.Engine
	set               policy.WantedSettings
	log               *slog.Logger
	now               func() time.Time
	// drainMu serialises the rounds, the only searches that check and spend router.wanted.per_hour. So no target
	// starts twice and two of them never spend the same slot of the hour.
	drainMu sync.Mutex
}

// New builds the acquirer.
func New(cfg Config) (*Acquirer, error) {
	if cfg.Store == nil || cfg.Policy == nil || cfg.Queue == nil || cfg.Router == nil {
		return nil, errors.New("acquire: store, policy, queue and router are required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	a := &Acquirer{st: cfg.Store, pol: cfg.Policy, q: cfg.Queue, rt: cfg.Router, search: cfg.Search, eng: cfg.Engine,
		searchBatch: cfg.SearchBatch, meta: cfg.Metadata, runtimeGeneration: cfg.RuntimeGeneration, runtimeCapture: cfg.CaptureRuntime,
		runtimeEngine: cfg.RuntimeEngine,
		set:           cfg.Policy.WantedSettings(),
		log:           cfg.Logger.With("component", "acquire"), now: cfg.Now}
	if a.search == nil && a.searchBatch == nil {
		if cfg.Engine == nil || cfg.Indexers == nil {
			return nil, errors.New("acquire: an engine and an indexer service are required to search")
		}
		eng, idx := cfg.Engine, cfg.Indexers
		a.search = func(ctx context.Context, t decide.Title, v decide.Version, existing *decide.ExistingFile,
			ss *decide.SeriesSearch) (*router.Searched, error) {
			return router.SearchRequest(decide.WithSearch(ctx), eng, idx, t, v, existing, ss)
		}
	}
	if a.searchBatch == nil && cfg.Engine != nil && cfg.Indexers != nil {
		eng, idx := cfg.Engine, cfg.Indexers
		a.searchBatch = func(ctx context.Context, t decide.Title, targets []router.SearchTarget) (map[string]*router.Searched, error) {
			return router.SearchRequests(decide.WithSearch(ctx), eng, idx, t, targets)
		}
	}
	if a.search == nil && a.searchBatch != nil {
		a.search = func(ctx context.Context, t decide.Title, v decide.Version, existing *decide.ExistingFile,
			ss *decide.SeriesSearch) (*router.Searched, error) {
			got, err := a.searchBatch(ctx, t, []router.SearchTarget{{ID: v.Key, Title: &t, Version: v,
				Existing: existing, Series: ss}})
			if err != nil {
				return nil, err
			}
			return got[v.Key], nil
		}
	}
	return a, nil
}

// SetMetadata sets the series metadata refresh whose first refresh a new series' searches wait for (built after the
// acquirer in theatarr serve).
func (a *Acquirer) SetMetadata(m MetadataWait) { a.meta = m }

// RegisterJobs adds the handlers to w.
func (a *Acquirer) RegisterJobs(w *jobs.Worker) {
	w.Handle(JobSearch, a.handleSearch)
	w.Handle(JobWanted, func(ctx context.Context, _ *jobs.Job) error {
		_, err := a.Round(ctx, nil)
		return err
	})
	w.Handle(router.JobResearch, a.handleResearch)
}

// Selection is what a search asks for, in the arrs' terms.
type Selection struct {
	// Versions are the version keys; only those in mode active are searched.
	Versions []string
	// TitleIDs limits the search to these titles; empty means every title of the versions, which needs Only.
	TitleIDs []int64
	// Season limits a series search to one season; EpisodeIDs to these episodes (one search each).
	Season     *int
	EpisodeIDs []int64
	// Only is "" (every monitored item: MoviesSearch, SeriesSearch, SeasonSearch, EpisodeSearch), OnlyMissing or
	// OnlyCutoff.
	Only string
	// QualityCutoff narrows OnlyCutoff to files below the profile's quality cutoff (router.wanted.cutoff_unmet:
	// quality), as the arrs' cutoff-unmet search selects. Without it OnlyCutoff takes every item below the full
	// cutoff (state filled).
	QualityCutoff bool
	userInvoked   bool
	// requested are the versions the caller named when Submit widened a title search to the titles' other active
	// versions (nil: every version searched was named). Only these take the user-invoked semantics.
	requested []string
}

// named is a user-invoked search of the movies or episodes it names (MoviesSearch, EpisodeSearch): it takes them
// unmonitored too, as the arrs do (Radarr MoviesSearchService skips unmonitored movies only for automatic triggers;
// Sonarr's EpisodeSearch searches with MonitoredEpisodesOnly false). Season and series searches stay monitored-only.
func (sel Selection) named(kind policy.Kind) bool {
	if !sel.userInvoked || sel.Only != "" || sel.Season != nil {
		return false
	}
	if kind == policy.KindSeries {
		return len(sel.EpisodeIDs) > 0
	}
	return len(sel.TitleIDs) > 0
}

// target is one search: a movie version, a season or an episode of a series version.
type target struct {
	titleID, versionID int64
	key                string
	name               string // movie, s01, s01e02
	reason             string
	season             *int
	episodes           []int64
	joinedAt           int64
	// anyMonitored: a named episode search (Selection.named) that also takes unmonitored episodes.
	anyMonitored bool
	// joined: a version the caller did not name, which a title search widened to (Submit). Its route keeps the
	// router's freshness check (acquire_run.requested = 0).
	joined bool
	// why is the schedule row's why a loop search started for (acquire_run.research; "" for none).
	why string
}

// namedEpisode tells a stored run of a named episode search: an arr command's run for one episode with no reason
// (missing and cutoff runs have one; season searches are named sNN).
func namedEpisode(source, reason, name string, episodes []int64) bool {
	return source == SourceCommand && reason == "" && len(episodes) == 1 && strings.Contains(name, "e")
}

// Active filters keys to the versions in mode active, sorted.
func (a *Acquirer) Active(keys []string) []string {
	var out []string
	for _, k := range keys {
		if vp := a.pol.Versions[k]; vp != nil && vp.Mode == policy.ModeActive && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Submit records one run per item sel selects, for its active versions only, and enqueues their searches. It returns
// how many it queued and how many it found (a bulk command queues at most MaxBulk). commandID links the runs to an
// arr view command (0 for none).
//
// A search for series still waiting for their first metadata refresh (added in Theatarr, Sonarr's add-then-refresh) is
// held for those titles and started when the refresh finishes (ReleaseWaiting); each held title counts as one
// queued item.
func (a *Acquirer) Submit(ctx context.Context, commandID int64, sel Selection) (queued, found int, err error) {
	ctx = a.captureRuntime(ctx)
	requested := a.Active(sel.Versions)
	if len(requested) == 0 {
		return 0, 0, nil
	}
	sel.Versions = requested
	named := len(sel.TitleIDs) > 0
	held, err := a.hold(ctx, commandID, &sel)
	if err != nil {
		return 0, 0, err
	}
	if held > 0 && len(sel.TitleIDs) == 0 {
		return held, held, nil
	}
	// A command that names titles can serve another active version of the same titles. The target rows below keep
	// the command status and each version's decision separate while the group shares the indexer query. A command
	// that names none (MissingMoviesSearch, CutoffUnmetEpisodeSearch) searches only what that arr would: the
	// versions it asked for (the view's), never another view's or another kind. A version the command did not name
	// joins only where the title (or episode) is monitored there, not removed and available, as an automatic search
	// would take it; the user-invoked semantics (unmonitored, before availability) are for the named versions only.
	if named {
		sel.requested = requested
		sel.Versions = a.pol.ActiveVersions()
	}
	queued, found = held, held
	sel.userInvoked = true
	ts, err := a.targets(ctx, sel)
	if err != nil {
		return 0, 0, err
	}
	groups := groupTargets(ts)
	found += len(groups)
	if len(sel.TitleIDs) == 0 && len(groups) > MaxBulk {
		if ts, err = a.oldestFirst(ctx, ts); err != nil {
			return 0, found, err
		}
		groups = groupTargets(ts)
		groups = groups[:MaxBulk]
	}
	for _, group := range groups {
		if err := a.startRuns(ctx, SourceCommand, commandID, group); err != nil {
			return queued, found, err
		}
		queued++
	}
	return queued, found, nil
}

// startRuns records one search and its served version-target rows in the same transaction as its job: the wanted
// loop's searches (source wanted, Round) and Submit's (source command). Each run stores its target's why
// (acquire_run.research). A loop search also moves its row's next_at to again_after from now until the search ends
// (reschedule), so it is not started twice.
func (a *Acquirer) startRuns(ctx context.Context, source string, commandID int64, ts []target) error {
	if len(ts) == 0 {
		return nil
	}
	now := store.Millis(a.now())
	var cmd any
	if commandID > 0 {
		cmd = commandID
	}
	// The run and its search job commit together: a run never exists without the job that runs it.
	return a.st.WriteTx(ctx, func(tx *sql.Tx) error {
		rootID := int64(0)
		for i, t := range ts {
			season := any(nil)
			if t.season != nil {
				season = *t.season
			}
			eps, _ := json.Marshal(nonNil(t.episodes))
			shared := any(nil)
			if i > 0 {
				shared = rootID
			}
			res, err := tx.ExecContext(ctx, `INSERT INTO acquire_run (source, command_id, title_id, version_id,
				version_key, target, reason, season, episodes, research, policy_rev, state, created_at, updated_at,
				search_run, requested) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued', ?, ?, ?, ?)`, source, cmd,
				t.titleID, t.versionID, t.key, t.name, t.reason, season, string(eps), nullString(t.why), a.pol.Revision, now,
				now, shared, !t.joined)
			if err != nil {
				return fmt.Errorf("acquire: recording a run: %w", err)
			}
			if source == SourceWanted {
				if _, err := tx.ExecContext(ctx, `INSERT INTO search_request (title_id, version_key, reason, season,
					created_at, next_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT DO UPDATE SET next_at = excluded.next_at`,
					t.titleID, t.key, t.reason, seasonOf(t.season), now, now+a.set.AgainAfter.Milliseconds()); err != nil {
					return err
				}
			}
			id, _ := res.LastInsertId()
			if i == 0 {
				rootID = id
			}
		}
		_, _, err := a.q.EnqueueTx(ctx, tx, JobSearch, SearchJob{RunID: rootID}, jobs.EnqueueOptions{
			DedupeKey: JobSearch + "/" + strconv.FormatInt(rootID, 10), MaxAttempts: searchAttempts})
		return err
	})
}

// searchAttempts caps a run's search job. A search that fails is recorded on the run and does not retry; the
// attempts are for runs whose process died or shut down mid-search (the lease expires, or the job is handed back).
const searchAttempts = 3

// SearchJob is the payload of a JobSearch job.
type SearchJob struct {
	RunID int64 `json:"run_id"`
}

func (a *Acquirer) captureRuntime(ctx context.Context) context.Context {
	if settings.ContextRuntime(ctx) != nil || settings.ContextGeneration(ctx) != 0 {
		return ctx
	}
	if a.runtimeCapture != nil {
		return a.runtimeCapture(ctx)
	}
	if a.runtimeGeneration == nil {
		return ctx
	}
	return settings.WithGeneration(ctx, a.runtimeGeneration())
}

// RuntimeGeneration returns the generation captured for a search job, or zero for legacy callers.
func RuntimeGeneration(ctx context.Context) int64 {
	return settings.ContextGeneration(ctx)
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// targets resolves a selection into searches, active versions only.
func (a *Acquirer) targets(ctx context.Context, sel Selection) ([]target, error) {
	if len(sel.TitleIDs) == 0 && sel.Only == "" && len(sel.EpisodeIDs) == 0 {
		return nil, nil
	}
	db := a.st.Reader()
	now := store.Millis(a.now())
	// One pinned standing for every title judged here, read only if a prefer-on version needs it.
	ctx, err := decide.WithStoredSeaDex(ctx, db)
	if err != nil {
		return nil, err
	}
	var out []target
	all := sel
	for _, k := range a.Active(all.Versions) {
		vp := a.pol.Versions[k]
		sel := all
		joined := all.requested != nil && !slices.Contains(all.requested, k)
		if joined {
			sel.userInvoked = false
		}
		if sel.Only == OnlyCutoff && vp.UpgradeRule() == policy.UpgradesOff {
			continue // upgrades: off never replaces a file, so a cutoff search would find nothing it may take
		}
		q := `SELECT v.id, v.title_id, v.state, t.kind, t.anime, ` + store.SeriesTypeSQL("t") + `, v.created_at
			FROM version v JOIN title t ON t.id = v.title_id
			WHERE v.policy_key = ? AND t.kind = ?`
		if !sel.named(vp.Kind) {
			q += ` AND v.monitored = 1`
		}
		if joined {
			q += ` AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id)`
		}
		args := []any{k, vp.Kind}
		if len(sel.TitleIDs) > 0 {
			q += ` AND v.title_id IN (` + store.Placeholders(len(sel.TitleIDs)) + `)`
			for _, id := range sel.TitleIDs {
				args = append(args, id)
			}
		}
		if vp.Kind != policy.KindSeries {
			switch sel.Only {
			case OnlyMissing:
				q += ` AND v.state = 'wanted'`
			case OnlyCutoff:
				if vp.PreferSeaDex {
					q += ` AND v.state IN ('filled','cutoff_met')`
				} else {
					q += ` AND v.state = 'filled'`
				}
			}
		}
		q += ` ORDER BY v.title_id`
		type vrow struct {
			id, title int64
			created   int64
			state     string
			kind      policy.Kind
			anime     int
			series    string
		}
		var vs []vrow
		rows, err := db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var r vrow
			if err := rows.Scan(&r.id, &r.title, &r.state, &r.kind, &r.anime, &r.series, &r.created); err != nil {
				rows.Close()
				return nil, err
			}
			vs = append(vs, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		for _, v := range vs {
			if vp.Anime != nil && *vp.Anime != (v.anime == 1 || v.series == "anime") {
				continue
			}
			if vp.Kind != policy.KindSeries {
				if sel.Season != nil || len(sel.EpisodeIDs) > 0 {
					continue
				}
				// Radarr AvailabilitySpecification.cs:22-26: user-invoked searches skip availability.
				if !sel.userInvoked {
					available, err := availability.Movie(ctx, db, a.pol, v.title, v.id, k, a.now())
					if err != nil {
						return nil, err
					}
					if !available {
						continue
					}
				}
				if sel.Only == OnlyCutoff && (sel.QualityCutoff || vp.PreferSeaDex) {
					below, err := a.belowCutoff(ctx, k, v.id, v.title, !sel.QualityCutoff)
					if err != nil {
						return nil, err
					}
					if !below {
						continue
					}
				}
				out = append(out, target{titleID: v.title, versionID: v.id, key: k, name: "movie", reason: sel.Only,
					joinedAt: v.created, joined: joined})
				continue
			}
			ts, err := a.seriesTargets(ctx, db, v.id, v.title, k, sel, now)
			if err != nil {
				return nil, err
			}
			for i := range ts {
				ts[i].joined = joined
			}
			out = append(out, ts...)
		}
	}
	return out, nil
}

// seriesTargets are a series version's searches: one per episode when the selection names episodes, else one per
// season with selected episodes (an episode search when a missing or cutoff search finds one episode in a season).
// Specials are searched only when named. Missing and cutoff searches take aired episodes only.
//
// A whole-series search (SeriesSearch: no season, episodes or Only) of a version whose profile does not allow
// upgrades takes only the episodes without a file, so a season whose episodes all have one is not searched: Sonarr
// v5's SeriesSearchService passes missingOnly = !UpgradeAllowed to ReleaseSearchService.SeasonSearch, which skips a
// season left with no episode (b94cf9b3b).
func (a *Acquirer) seriesTargets(ctx context.Context, db *sql.DB, versionID, titleID int64, key string, sel Selection,
	now int64) ([]target, error) {
	vp := a.pol.Versions[key]
	missingOnly := sel.Only == "" && sel.Season == nil && len(sel.EpisodeIDs) == 0 && !a.upgradesAllowed(key)
	var keep map[int64]bool  // quality cutoff: the filled episodes whose file is below it
	pack := map[int64]bool{} // the kept episodes a curated season pack makes wanted: searched by season
	if sel.Only == OnlyCutoff && (sel.QualityCutoff || vp.PreferSeaDex) {
		var err error
		if keep, err = a.cutoffEpisodes(ctx, key, versionID, titleID, !sel.QualityCutoff, pack); err != nil {
			return nil, err
		}
	}
	// An episode the metadata source dropped (removed_at; kept for its files or history) is searched only when named.
	rows, err := db.QueryContext(ctx, `SELECT e.id, e.season, e.number, COALESCE(s.state, 'wanted'), e.air_date_utc_at,
		COALESCE(s.updated_at, e.created_at)
		FROM episode e LEFT JOIN episode_version_state s ON s.episode_id = e.id AND s.version_id = ?
		WHERE e.title_id = ? AND (COALESCE(s.monitored, e.monitored) = 1 OR ?) AND (e.removed_at IS NULL OR ?)
		ORDER BY e.season, e.number`, versionID, titleID, sel.named(policy.KindSeries), len(sel.EpisodeIDs) > 0)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type ep struct {
		id             int64
		season, number int
		joinedAt       int64
	}
	bySeason := map[int][]ep{}
	var seasons []int
	for rows.Next() {
		var e ep
		var state string
		var aired sql.NullInt64
		if err := rows.Scan(&e.id, &e.season, &e.number, &state, &aired, &e.joinedAt); err != nil {
			return nil, err
		}
		switch {
		case len(sel.EpisodeIDs) > 0 && !slices.Contains(sel.EpisodeIDs, e.id):
			continue
		case sel.Season != nil && e.season != *sel.Season:
			continue
		case e.season == 0 && sel.Only == OnlyCutoff && sel.Season == nil && len(sel.EpisodeIDs) == 0:
			continue
		case sel.Only == OnlyMissing && state != "wanted", sel.Only == OnlyCutoff && state != "filled" && !(vp.PreferSeaDex && state == "cutoff_met"):
			continue
		case keep != nil && !keep[e.id]:
			continue
		case missingOnly && state != "wanted":
			continue
		case e.season == 0 && sel.Season == nil && len(sel.EpisodeIDs) == 0 && (!aired.Valid || aired.Int64 > now):
			continue
		case sel.Only != "" && (!aired.Valid || aired.Int64 > now):
			continue
		}
		if _, ok := bySeason[e.season]; !ok {
			seasons = append(seasons, e.season)
		}
		bySeason[e.season] = append(bySeason[e.season], e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []target
	for _, s := range seasons {
		eps := bySeason[s]
		season := s
		if len(sel.EpisodeIDs) > 0 || (sel.Only != "" && len(eps) == 1 && !pack[eps[0].id]) {
			for _, e := range eps {
				out = append(out, target{titleID: titleID, versionID: versionID, key: key, reason: sel.Only,
					name: fmt.Sprintf("s%02de%02d", e.season, e.number), season: &season, episodes: []int64{e.id}, joinedAt: e.joinedAt,
					anyMonitored: sel.named(policy.KindSeries)})
			}
			continue
		}
		t := target{titleID: titleID, versionID: versionID, key: key, reason: sel.Only,
			name: fmt.Sprintf("s%02d", s), season: &season}
		for _, e := range eps {
			t.episodes = append(t.episodes, e.id)
			t.joinedAt = max(t.joinedAt, e.joinedAt)
		}
		out = append(out, t)
	}
	return out, nil
}

// upgradesAllowed is the version's profile's upgradeAllowed: from the engine's built profile,
// else the policy profile's upgrades (default true).
func (a *Acquirer) upgradesAllowed(key string) bool {
	vp := a.pol.Versions[key]
	if vp == nil {
		return true
	}
	if a.eng != nil {
		if p := a.eng.Profile(vp.Profile, format.Sonarr); p != nil && p.Profile != nil {
			return p.Profile.UpgradeAllowed
		}
	}
	if pr := a.pol.Profiles[vp.Profile]; pr != nil && pr.Upgrades != nil {
		return *pr.Upgrades
	}
	return true
}

// The quality cutoff of router.wanted.cutoff_unmet: quality. Radarr's MovieCutoffService.MoviesWhereCutoffUnmet and
// Sonarr's EpisodeCutoffService.EpisodesWhereCutoffUnmet (ref radarr/src/NzbDrone.Core/Movies/MovieCutoffService.cs,
// sonarr/src/NzbDrone.Core/Tv/EpisodeCutoffService.cs, and the repositories' BuildQualityCutoffWhereClause) list a
// file when its quality is one the profile ranks below the cutoff (the first allowed quality when upgrades are off);
// the custom-format score is not looked at. CutoffUnmetMoviesSearch and CutoffUnmetEpisodeSearch search that list
// (MoviesSearchService, EpisodeSearchService). The file judged is the one decide compares a release with: the newest
// active link, else the arr's file. Without a decision engine the profile is unknown and the state alone decides.
// The arr views' wanted/cutoff lists and cutoff-unmet commands use the same judgement.

// BelowQualityCutoff says whether a filled movie version's file is below the quality cutoff (true without an engine).
func (a *Acquirer) BelowQualityCutoff(ctx context.Context, key string, versionID, titleID int64) (bool, error) {
	return a.belowCutoff(ctx, key, versionID, titleID, false)
}
func (a *Acquirer) belowCutoff(ctx context.Context, key string, versionID, titleID int64, full bool) (bool, error) {
	if a.eng == nil {
		return true, nil
	}
	if vp := a.pol.Versions[key]; vp != nil && vp.PreferSeaDex {
		var err error
		ctx, err = decide.WithStoredSeaDex(ctx, a.st.Reader())
		if err != nil {
			return false, err
		}
		ctx = decide.WithSeaDexTitle(decide.WithSeaDexPolicy(ctx, a.pol, vp), titleID)
	}
	f, err := decide.LoadExisting(ctx, a.st.Reader(), versionID)
	if err != nil || f == nil {
		return false, err
	}
	below, err := a.currentFileBelowCutoff(ctx, key, versionID, titleID, f, full)
	if err != nil {
		a.log.Warn("cutoff search: file not judged", "version", key, "title", titleID, "err", err)
		return false, nil
	}
	return below, nil
}

// EpisodesBelowQualityCutoff are a series version's episodes whose file is below the quality cutoff; nil without an
// engine (every filled episode then).
func (a *Acquirer) EpisodesBelowQualityCutoff(ctx context.Context, key string, versionID,
	titleID int64) (map[int64]bool, error) {
	return a.episodesBelowCutoff(ctx, key, versionID, titleID, false)
}

func (a *Acquirer) episodesBelowCutoff(ctx context.Context, key string, versionID, titleID int64, full bool) (map[int64]bool, error) {
	return a.cutoffEpisodes(ctx, key, versionID, titleID, full, nil)
}

// cutoffEpisodes are a series version's episodes whose file is below cutoff (full: the upgrade rule). pack, when
// given, receives the episodes a curated alternative makes wanted: the alternative is a season pack, so their
// search is a season search even for one episode.
func (a *Acquirer) cutoffEpisodes(ctx context.Context, key string, versionID, titleID int64, full bool,
	pack map[int64]bool) (map[int64]bool, error) {
	if a.eng == nil {
		return nil, nil
	}
	if vp := a.pol.Versions[key]; vp != nil && vp.PreferSeaDex {
		var err error
		ctx, err = decide.WithStoredSeaDex(ctx, a.st.Reader())
		if err != nil {
			return nil, err
		}
		ctx = decide.WithSeaDexTitle(decide.WithSeaDexPolicy(ctx, a.pol, vp), titleID)
	}
	var eps []decide.Episode
	var title *decide.Title
	var err error
	if full || a.pol.Versions[key].PreferSeaDex {
		t, loadErr := decide.LoadTitle(ctx, a.st.Reader(), titleID)
		if loadErr != nil {
			return nil, loadErr
		}
		title = &t
	}
	if a.pol.Versions[key].PreferSeaDex {
		err = decide.LoadSeriesVersion(ctx, a.st.Reader(), title, versionID)
		eps = title.Episodes
	} else {
		eps, err = decide.LoadEpisodes(ctx, a.st.Reader(), titleID, versionID)
		if title != nil {
			title.Episodes = eps
		}
	}
	if err != nil {
		return nil, err
	}
	out := map[int64]bool{}
	judged := map[int64]bool{} // by file
	for _, e := range eps {
		if e.File == nil {
			continue
		}
		below, ok := judged[e.FileID]
		if !ok {
			if below, err = a.currentFileBelowCutoff(ctx, key, versionID, titleID, e.File, full, title); err != nil {
				a.log.Warn("cutoff search: file not judged", "version", key, "title", titleID, "episode", e.ID, "err", err)
				below = false
			}
			judged[e.FileID] = below
		}
		if below {
			out[e.ID] = true
			if pack != nil && title != nil && a.eng.CuratorWanted(title, decide.Version{ID: versionID, Key: key}, e.File) {
				pack[e.ID] = true
			}
		}
	}
	return out, nil
}

// lastRuns are a target's latest runs (milliseconds, 0 for none): complete searches, partial ones (runPartial), and
// attempts of any kind (infrastructure failures too).
type lastRuns struct{ complete, partial, any int64 }

// lastSearched maps each version's seasons (seasonKey; -1 for a film) to their latest runs of any source.
func (a *Acquirer) lastSearched(ctx context.Context) (map[string]lastRuns, error) {
	rows, err := a.st.Reader().QueryContext(ctx, `SELECT version_id, COALESCE(season, -1),
		COALESCE(MAX(CASE WHEN infra = 0 THEN created_at END), 0), COALESCE(MAX(CASE WHEN infra = 2 THEN created_at END), 0),
		MAX(created_at) FROM acquire_run GROUP BY version_id, COALESCE(season, -1)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]lastRuns{}
	for rows.Next() {
		var v int64
		var season int
		var l lastRuns
		if err := rows.Scan(&v, &season, &l.complete, &l.partial, &l.any); err != nil {
			return nil, err
		}
		out[seasonKey(v, season)] = l
	}
	return out, rows.Err()
}

// groupTargets shares one title query across its matching movie or series-season targets.
func groupTargets(ts []target) [][]target {
	var out [][]target
	idx := map[string]int{}
	for _, t := range ts {
		name := t.name
		if t.season != nil {
			name = "s" + strconv.Itoa(*t.season)
		}
		key := strconv.FormatInt(t.titleID, 10) + "/" + name
		i, ok := idx[key]
		if !ok {
			idx[key] = len(out)
			out = append(out, []target{t})
			continue
		}
		out[i] = append(out[i], t)
	}
	return out
}

// oldestFirst orders targets by their latest attempt of any kind (an infrastructure failure too, so an item that
// keeps failing does not stay in front of the rest), never attempted first, newest added first among those (stable
// otherwise).
func (a *Acquirer) oldestFirst(ctx context.Context, ts []target) ([]target, error) {
	last, err := a.lastSearched(ctx)
	if err != nil {
		return nil, err
	}
	return orderTargets(ts, last), nil
}

func orderTargets(ts []target, last map[string]lastRuns) []target {
	sort.SliceStable(ts, func(i, j int) bool {
		li := last[seasonKey(ts[i].versionID, seasonOf(ts[i].season))].any
		lj := last[seasonKey(ts[j].versionID, seasonOf(ts[j].season))].any
		if li == 0 && lj == 0 && ts[i].joinedAt != ts[j].joinedAt {
			return ts[i].joinedAt > ts[j].joinedAt
		}
		return li < lj
	})
	return ts
}

// handleSearch runs one acquire_run: search, decide, and enqueue the route.
func (a *Acquirer) handleSearch(ctx context.Context, j *jobs.Job) error {
	var p SearchJob
	if err := j.Decode(&p); err != nil {
		return jobs.Permanent(err)
	}
	ctx = a.captureRuntime(ctx)
	return a.run(ctx, p, fmt.Sprintf("job:%d/%d", j.ID, j.Attempt))
}

// claims numbers the claims of runs started outside the job queue (Run1).
var claims atomic.Int64

// Run1 runs one acquire_run now: a queued run, or one left searching by an interrupted search.
func (a *Acquirer) Run1(ctx context.Context, runID int64) error {
	return a.RunJob(ctx, SearchJob{RunID: runID})
}

// RunJob is Run1 for a search job's payload.
func (a *Acquirer) RunJob(ctx context.Context, p SearchJob) error {
	ctx = a.captureRuntime(ctx)
	return a.run(ctx, p, fmt.Sprintf("run1:%d/%d", a.now().UnixNano(), claims.Add(1)))
}

// errLostClaim: another lease took the run over while this one searched; its outcome is not recorded.
var errLostClaim = errors.New("acquire: the run was claimed by another lease")

// run is the job's body. It claims the run for the job's lease (claim), taking over a run left searching by an
// interrupted lease, and records the outcome only while it still holds the claim.
func (a *Acquirer) run(ctx context.Context, sj SearchJob, claim string) error {
	runID := sj.RunID
	var t target
	var eps, state, source string
	var season sql.NullInt64
	err := a.st.Reader().QueryRowContext(ctx, `SELECT title_id, version_id, version_key, target, reason, season,
		episodes, state, source FROM acquire_run WHERE id = ?`, runID).Scan(&t.titleID,
		&t.versionID, &t.key, &t.name, &t.reason, &season, &eps, &state, &source)
	if errors.Is(err, sql.ErrNoRows) {
		return jobs.Permanent(fmt.Errorf("acquire: no run %d", runID))
	}
	if err != nil {
		return err
	}
	if state != "queued" && state != "searching" {
		return nil
	}
	var children int
	if err := a.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM acquire_run WHERE search_run = ?`, runID).
		Scan(&children); err != nil {
		return err
	}
	if children > 0 {
		return a.runFanout(ctx, runID, claim, state)
	}
	_ = json.Unmarshal([]byte(eps), &t.episodes)
	t.anyMonitored = namedEpisode(source, t.reason, t.name, t.episodes)
	if season.Valid {
		s := int(season.Int64)
		t.season = &s
	}
	res, err := a.st.Writer().ExecContext(ctx, `UPDATE acquire_run SET state = 'searching', claim = ?, updated_at = ?
		WHERE id = ? AND state IN ('queued', 'searching')`, claim, store.Millis(a.now()), runID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return nil // finished meanwhile
	}
	if state == "searching" {
		a.log.Info("resuming an interrupted search", "run", runID, "version", t.key, "target", t.name)
	}
	// infra is the outcome's acquire_run.infra (runComplete, runInfra, runPartial, runGated).
	infra := runComplete
	// finishTx records the outcome, with route (when set) enqueued in the same transaction, if the claim still holds,
	// and reschedules the item's rows (reschedule).
	finishTx := func(state, msg string, results, accepted int, route *router.Request) (int64, error) {
		var job int64
		err := a.st.WriteTx(ctx, func(tx *sql.Tx) error {
			var rj any
			if route != nil {
				var err error
				if job, err = a.rt.EnqueueTx(ctx, tx, *route, time.Time{}); err != nil {
					return err
				}
				rj = job
			}
			res, err := tx.ExecContext(ctx, `UPDATE acquire_run SET state = ?, message = ?, results = ?,
				accepted = ?, route_job = ?, infra = ?, claim = NULL, updated_at = ? WHERE id = ? AND state = 'searching'
				AND claim = ?`, state, msg, results, accepted, rj, infra, store.Millis(a.now()), runID, claim)
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err != nil {
				return err
			} else if n == 0 {
				return errLostClaim
			}
			return a.reschedule(ctx, tx, source, t, infra)
		})
		if errors.Is(err, errLostClaim) {
			a.log.Warn("search outcome dropped: the run was taken over", "run", runID, "claim", claim)
			return 0, nil
		}
		return job, err
	}
	finish := func(state, msg string, results, accepted int) error {
		_, err := finishTx(state, msg, results, accepted, nil)
		return err
	}
	if vp := a.pol.Versions[t.key]; vp == nil || vp.Mode != policy.ModeActive {
		return finish("skipped", "version "+t.key+" is not active: nothing searched", 0, 0)
	}
	// A loop search is checked for Pause again before it asks any indexer: its row stays due (runGated).
	if source == SourceWanted && !a.pol.RouterSettings().Enabled {
		infra = runGated
		return finish("skipped", "paused: nothing searched", 0, 0)
	}
	db := a.st.Reader()
	if busy, err := a.inFlight(ctx, t); err != nil {
		return err
	} else if busy {
		return finish("skipped", "a download for it is in flight already", 0, 0)
	}
	title, err := decide.LoadTitle(ctx, db, t.titleID)
	if err != nil {
		return finish("failed", err.Error(), 0, 0)
	}
	v, err := decide.LoadVersion(ctx, db, a.pol, t.titleID, t.key)
	if err != nil {
		return finish("failed", err.Error(), 0, 0)
	}
	if title.Kind == policy.KindMovie && source != SourceCommand {
		available, err := availability.Movie(ctx, db, a.pol, t.titleID, t.versionID, t.key, a.now())
		if err != nil {
			return err
		}
		if !available {
			return finish("skipped", "movie has not reached minimum availability", 0, 0)
		}
	}
	var existing *decide.ExistingFile
	var ss *decide.SeriesSearch
	if title.Kind == policy.KindSeries {
		if err := decide.LoadSeriesVersion(ctx, db, &title, v.ID); err != nil {
			return finish("failed", err.Error(), 0, 0)
		}
		ss = seriesSearch(&title, t)
	} else if existing, err = decide.LoadExisting(ctx, db, v.ID); err != nil {
		return finish("failed", err.Error(), 0, 0)
	}
	found, err := a.search(gateway.WithPurpose(ctx, searchPurpose(t.reason, held(&title, t, existing))), title, v, existing, ss)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		a.log.Warn("search failed", "run", runID, "title", title.Name, "version", t.key, "target", t.name, "err", err)
		if infra = runKind(err, nil); infra == runInfra {
			return finish("failed", "search failed: "+err.Error()+infraNote, 0, 0)
		}
		return finish("failed", "search failed: "+err.Error(), 0, 0)
	}
	if infra = runKind(nil, found); infra == runInfra {
		a.log.Warn("search unanswered", "run", runID, "title", title.Name, "version", t.key,
			"target", t.name, "notes", found.Notes)
		return finish("failed", noAnswerMessage(found)+infraNote, 0, 0)
	}
	req := found.Request
	req.Source = Source(runID)
	// The grab journal keeps what this search decided between (internal/journal/snapshot).
	if err := snapshot.Record(ctx, a.st.Writer(), snapshot.Search{Policy: a.pol, Coverage: found.Coverage, Source: req.Source, RunID: runID,
		TitleID: t.titleID, Version: t.key, Target: t.name, Ranked: found.Ranked, Releases: found.Releases,
		Notes: found.Notes, CreatedAt: a.now()}); err != nil {
		a.log.Warn("journal: decision not recorded", "run", runID, "err", err)
	}
	notes := ""
	if len(found.Notes) > 0 {
		notes = " (" + strings.Join(found.Notes, "; ") + ")"
	}
	if infra == runPartial {
		notes += partialNote
	}
	if len(req.Candidates) == 0 {
		a.log.Info("searched: nothing accepted", "run", runID, "title", title.Name, "version", t.key, "target", t.name,
			"results", len(found.Ranked))
		return finish("nothing", fmt.Sprintf("no accepted release among %d results%s", len(found.Ranked), notes),
			len(found.Ranked), 0)
	}
	// The route and the routed state commit together: a crash can't route twice or leave a run without its route.
	job, err := finishTx("routed", fmt.Sprintf("%d of %d results accepted; best %s%s", len(req.Candidates),
		len(found.Ranked), req.Candidates[0].Release.Title, notes), len(found.Ranked), len(req.Candidates), &req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return finish("failed", "routing: "+err.Error(), len(found.Ranked), len(req.Candidates))
	}
	a.log.Info("searched: routing", "run", runID, "title", title.Name, "version", t.key, "target", t.name,
		"results", len(found.Ranked), "accepted", len(req.Candidates), "best", req.Candidates[0].Release.Title,
		"route_job", job)
	return nil
}

type fanoutRun struct {
	id int64
	target
	state  string
	source string
}

type fanoutOutcome struct {
	run      fanoutRun
	state    string
	message  string
	results  int
	accepted int
	route    *router.Request
	infra    int // acquire_run.infra (runKind)
}

// runFanout claims every target row, performs the union search once, and commits each target's route with its
// outcome. A retry therefore resumes the whole shared search without routing any target twice.
func (a *Acquirer) runFanout(ctx context.Context, rootID int64, claim, rootState string) error {
	rows, err := a.st.Reader().QueryContext(ctx, `SELECT id, title_id, version_id, version_key, target, reason,
		season, episodes, state, source FROM acquire_run WHERE id = ? OR search_run = ? ORDER BY id`, rootID, rootID)
	if err != nil {
		return err
	}
	var runs []fanoutRun
	for rows.Next() {
		var r fanoutRun
		var season sql.NullInt64
		var eps string
		if err := rows.Scan(&r.id, &r.titleID, &r.versionID, &r.key, &r.name, &r.reason, &season, &eps, &r.state, &r.source); err != nil {
			rows.Close()
			return err
		}
		_ = json.Unmarshal([]byte(eps), &r.episodes)
		r.anyMonitored = namedEpisode(r.source, r.reason, r.name, r.episodes)
		if season.Valid {
			s := int(season.Int64)
			r.season = &s
		}
		runs = append(runs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(runs) < 2 {
		return fmt.Errorf("acquire: shared search %d has no served targets", rootID)
	}
	if rootState == "searching" {
		a.log.Info("resuming an interrupted shared search", "run", rootID, "served", len(runs))
	}
	claimed := false
	if err := a.st.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE acquire_run SET state = 'searching', claim = ?, updated_at = ?
			WHERE id = ? AND state IN ('queued', 'searching')`, claim, store.Millis(a.now()), rootID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return nil
		}
		claimed = true
		_, err = tx.ExecContext(ctx, `UPDATE acquire_run SET state = 'searching', claim = ?, updated_at = ?
			WHERE search_run = ? AND state IN ('queued', 'searching')`, claim, store.Millis(a.now()), rootID)
		return err
	}); err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	titleID := runs[0].titleID
	baseTitle, err := decide.LoadTitle(ctx, a.st.Reader(), titleID)
	if err != nil {
		return a.finishFanout(ctx, claim, fanoutFailures(runs, err.Error()))
	}
	var items []fanoutRun
	var targets []router.SearchTarget
	var outcomes []fanoutOutcome
	// The shared search is an upgrade search when every target it serves is one.
	purpose := gateway.PurposeUpgrade
	// A loop search is checked for Pause again before it asks any indexer: its rows stay due (runGated).
	paused := runs[0].source == SourceWanted && !a.pol.RouterSettings().Enabled
	for _, r := range runs {
		outcome := fanoutOutcome{run: r}
		vp := a.pol.Versions[r.key]
		if vp == nil || vp.Mode != policy.ModeActive {
			outcome.state, outcome.message = "skipped", "version "+r.key+" is not active: nothing searched"
			outcomes = append(outcomes, outcome)
			continue
		}
		if paused {
			outcome.state, outcome.message, outcome.infra = "skipped", "paused: nothing searched", runGated
			outcomes = append(outcomes, outcome)
			continue
		}
		if baseTitle.Kind == policy.KindMovie && r.source != SourceCommand {
			available, err := availability.Movie(ctx, a.st.Reader(), a.pol, r.titleID, r.versionID, r.key, a.now())
			if err != nil {
				return err
			}
			if !available {
				outcome.state, outcome.message = "skipped", "movie has not reached minimum availability"
				outcomes = append(outcomes, outcome)
				continue
			}
		}
		if busy, err := a.inFlight(ctx, r.target); err != nil {
			outcome.state, outcome.message = "failed", err.Error()
			outcomes = append(outcomes, outcome)
			continue
		} else if busy {
			outcome.state, outcome.message = "skipped", "a download for it is in flight already"
			outcomes = append(outcomes, outcome)
			continue
		}
		v, err := decide.LoadVersion(ctx, a.st.Reader(), a.pol, titleID, r.key)
		if err != nil {
			outcome.state, outcome.message = "failed", err.Error()
			outcomes = append(outcomes, outcome)
			continue
		}
		t := baseTitle
		var existing *decide.ExistingFile
		var ss *decide.SeriesSearch
		if t.Kind == policy.KindSeries {
			t = baseTitle
			if err := decide.LoadSeriesVersion(ctx, a.st.Reader(), &t, v.ID); err != nil {
				outcome.state, outcome.message = "failed", err.Error()
				outcomes = append(outcomes, outcome)
				continue
			}
			ss = seriesSearch(&t, r.target)
		} else if existing, err = decide.LoadExisting(ctx, a.st.Reader(), v.ID); err != nil {
			outcome.state, outcome.message = "failed", err.Error()
			outcomes = append(outcomes, outcome)
			continue
		}
		items = append(items, r)
		targets = append(targets, router.SearchTarget{ID: strconv.FormatInt(r.id, 10), Title: &t, Version: v,
			Existing: existing, Series: ss})
		if searchPurpose(r.reason, held(&t, r.target, existing)) != gateway.PurposeUpgrade {
			purpose = gateway.PurposeWanted
		}
	}
	if len(items) == 0 {
		return a.finishFanout(ctx, claim, outcomes)
	}
	var found map[string]*router.Searched
	ctx = gateway.WithPurpose(decide.WithParseMemo(ctx), purpose)
	if a.searchBatch != nil {
		found, err = a.searchBatch(ctx, baseTitle, targets)
	} else {
		found = make(map[string]*router.Searched, len(items))
		for i, item := range items {
			var v decide.Version
			v, err = decide.LoadVersion(ctx, a.st.Reader(), a.pol, titleID, item.key)
			if err != nil {
				break
			}
			var existing *decide.ExistingFile
			var ss *decide.SeriesSearch
			if baseTitle.Kind == policy.KindSeries {
				t := baseTitle
				if err = decide.LoadSeriesVersion(ctx, a.st.Reader(), &t, v.ID); err != nil {
					break
				}
				ss = seriesSearch(&t, item.target)
			} else if existing, err = decide.LoadExisting(ctx, a.st.Reader(), v.ID); err != nil {
				break
			}
			searchTitle := baseTitle
			if targets[i].Title != nil {
				searchTitle = *targets[i].Title
			}
			found[strconv.FormatInt(item.id, 10)], err = a.search(ctx, searchTitle, v, existing, ss)
			if err != nil {
				break
			}
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		failed := fanoutFailures(items, "search failed: "+err.Error())
		if runKind(err, nil) == runInfra {
			for i := range failed {
				failed[i].infra, failed[i].message = runInfra, failed[i].message+infraNote
			}
		}
		return a.finishFanout(ctx, claim, append(outcomes, failed...))
	}
	for _, item := range items {
		key := strconv.FormatInt(item.id, 10)
		searched := found[key]
		if searched == nil {
			outcomes = append(outcomes, fanoutOutcome{run: item, state: "failed", message: "search returned no result"})
			continue
		}
		kind := runKind(nil, searched)
		if kind == runInfra {
			outcomes = append(outcomes, fanoutOutcome{run: item, state: "failed", infra: runInfra,
				message: noAnswerMessage(searched) + infraNote})
			continue
		}
		req := searched.Request
		req.Source = Source(item.id)
		if err := snapshot.Record(ctx, a.st.Writer(), snapshot.Search{Policy: a.pol, Coverage: searched.Coverage,
			Source: req.Source, RunID: item.id, TitleID: item.titleID, Version: item.key, Target: item.name,
			Ranked: searched.Ranked, Releases: searched.Releases, Notes: searched.Notes, CreatedAt: a.now()}); err != nil {
			a.log.Warn("journal: decision not recorded", "run", item.id, "err", err)
		}
		if len(req.Candidates) == 0 {
			message := fmt.Sprintf("no accepted release among %d results", len(searched.Ranked))
			if len(searched.Notes) > 0 {
				message += " (" + strings.Join(searched.Notes, "; ") + ")"
			}
			if kind == runPartial {
				message += partialNote
			}
			outcomes = append(outcomes, fanoutOutcome{run: item, state: "nothing", message: message, infra: kind,
				results: len(searched.Ranked)})
			continue
		}
		message := fmt.Sprintf("%d of %d results accepted; best %s", len(req.Candidates), len(searched.Ranked),
			req.Candidates[0].Release.Title)
		if len(searched.Notes) > 0 {
			message += " (" + strings.Join(searched.Notes, "; ") + ")"
		}
		if kind == runPartial {
			message += partialNote
		}
		outcomes = append(outcomes, fanoutOutcome{run: item, state: "routed", message: message, infra: kind,
			results: len(searched.Ranked), accepted: len(req.Candidates), route: &req})
	}
	return a.finishFanout(ctx, claim, outcomes)
}

func fanoutFailures(runs []fanoutRun, message string) []fanoutOutcome {
	out := make([]fanoutOutcome, 0, len(runs))
	for _, r := range runs {
		out = append(out, fanoutOutcome{run: r, state: "failed", message: message})
	}
	return out
}

func (a *Acquirer) finishFanout(ctx context.Context, claim string, outcomes []fanoutOutcome) error {
	err := a.st.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, o := range outcomes {
			var routeJob any
			if o.route != nil {
				job, err := a.rt.EnqueueTx(ctx, tx, *o.route, time.Time{})
				if err != nil {
					return err
				}
				routeJob = job
			}
			res, err := tx.ExecContext(ctx, `UPDATE acquire_run SET state = ?, message = ?, results = ?, accepted = ?,
				route_job = ?, infra = ?, claim = NULL, updated_at = ? WHERE id = ? AND state = 'searching'
				AND claim = ?`, o.state, o.message, o.results, o.accepted, routeJob, o.infra, store.Millis(a.now()),
				o.run.id, claim)
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err != nil {
				return err
			} else if n == 0 {
				return errLostClaim
			}
			if err := a.reschedule(ctx, tx, o.run.source, o.run.target, o.infra); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, errLostClaim) {
		a.log.Warn("shared search outcome dropped: one target was taken over", "claim", claim)
		return nil
	}
	return err
}

// What a run's search established, recorded in acquire_run.infra:
//
//   - runComplete (0): searched.
//   - runInfra (1): no answer at all (the VPN guard refused it, or no indexer answered: Searched.NoAnswer). It says
//     nothing about the title and does not count as searched: its row is tried again an hour later, a few times
//     (nextSearch).
//   - runPartial (2): some indexer answered and some request failed (an error, a back-off or a spent budget, a paging
//     failure; our own page_cap is not one: indexer.Result.Capped). It counts as searched, its releases route as
//     usual, but the schedule and the re-search limit take it as searched for partialAgain only (again_after when
//     shorter), so the indexers that failed get another chance sooner without the item crowding out the rest.
const (
	runComplete = 0
	runInfra    = 1
	runPartial  = 2
	// runGated: a loop search that ended before its search because Pause was turned on meanwhile. Nothing was
	// searched: it counts as no search and no attempt, and not against router.wanted.per_hour (infra <> 3); its row
	// stays due.
	runGated = 3
)

const (
	infraNote   = "; not counted as searched: asked again later"
	partialNote = "; partial answer: due again sooner"
)

// partialAgain is how long a partial search counts as searched (router.wanted.again_after when shorter).
const partialAgain = 6 * time.Hour

// runKind classifies a search's outcome: err from the search, or its answer.
func runKind(err error, found *router.Searched) int {
	switch {
	case err != nil && errors.Is(err, gateway.ErrVPNRequired):
		return runInfra
	case err != nil || found == nil:
		return runComplete
	case found.NoAnswer:
		return runInfra
	case found.Incomplete:
		return runPartial
	}
	return runComplete
}

func noAnswerMessage(found *router.Searched) string {
	return "search failed: no indexer answered (" + strings.Join(found.Notes, "; ") + ")"
}

// searchPurpose is what a run's indexer requests count as (docs/design/gateway.md): a cutoff run, or a run for something
// held (held), searches for an upgrade; everything else for something missing.
func searchPurpose(reason string, held bool) gateway.Purpose {
	if reason == OnlyCutoff || held && reason != OnlyMissing {
		return gateway.PurposeUpgrade
	}
	return gateway.PurposeWanted
}

// held says whether a run's target already has its files: a movie its file, a series target a file for each of its
// episodes (t loaded with LoadSeriesVersion).
func held(t *decide.Title, tg target, existing *decide.ExistingFile) bool {
	if t.Kind != policy.KindSeries {
		return existing != nil
	}
	if len(tg.episodes) == 0 {
		return false
	}
	files := map[int64]bool{}
	for _, e := range t.Episodes {
		files[e.ID] = e.File != nil
	}
	for _, id := range tg.episodes {
		if !files[id] {
			return false
		}
	}
	return true
}

// Source is the router request source of a run: download.source and router_plan.source carry it.
func Source(runID int64) string { return "acquire:" + strconv.FormatInt(runID, 10) }

// seriesSearch is the arr's search criteria for a target: a season search for several episodes, an episode search
// for one (the arr's automatic search: monitored episodes only). A missing or cutoff season target (named sNN) with
// one episode is still a season search: seriesTargets makes one only when a curated season pack made that episode
// wanted. The name and reason are stored with the run, so the intent survives the queue.
func seriesSearch(t *decide.Title, tg target) *decide.SeriesSearch {
	season := len(tg.episodes) != 1 || tg.reason != "" && seasonTarget(tg.name)
	rt := ""
	if season {
		rt = "SeasonPack"
	}
	ss := decide.SearchFor(t, tg.episodes, "Search", rt)
	if ss == nil {
		ss = &decide.SeriesSearch{}
	}
	if season {
		ss.Episode = 0
		ss.Pack = len(tg.episodes) == 1 // the curated pack target: the planner keeps it a season query
		if tg.season != nil && len(tg.episodes) == 0 {
			ss.Season = *tg.season
		}
	}
	ss.MonitoredOnly = !tg.anyMonitored
	return ss
}

// seasonTarget says whether a target name is a season's (sNN), not an episode's (sNNeNN) or a movie.
func seasonTarget(name string) bool {
	return len(name) > 1 && name[0] == 's' && !strings.Contains(name, "e")
}

func (a *Acquirer) inFlight(ctx context.Context, t target) (bool, error) {
	if len(t.episodes) == 0 {
		return router.InFlight(ctx, a.st.Reader(), t.titleID, t.versionID, nil)
	}
	// A season still needs its search when only some episodes are in flight. The router skips overlaps with each
	// candidate's full episode set, so the search criteria and its single indexer query stay intact.
	for _, id := range t.episodes {
		busy, err := router.InFlight(ctx, a.st.Reader(), t.titleID, t.versionID, []int64{id})
		if err != nil || !busy {
			return false, err
		}
	}
	return true, nil
}

func (a *Acquirer) currentFileBelowCutoff(ctx context.Context, key string, versionID, titleID int64, f *decide.ExistingFile, full bool, titles ...*decide.Title) (bool, error) {
	v := decide.Version{ID: versionID, Key: key}
	if !full && !a.pol.Versions[key].PreferSeaDex {
		return a.eng.QualityCutoffNotMet(v, f)
	}
	if len(titles) > 0 && titles[0] != nil {
		if full {
			return a.eng.FileCutoffNotMet(titles[0], v, f)
		}
		return a.eng.QualityCutoffNotMet(v, f, titles[0])
	}
	t, err := decide.LoadTitle(ctx, a.st.Reader(), titleID)
	if err != nil {
		return false, err
	}
	if t.Kind == "series" && a.pol.Versions[key].PreferSeaDex {
		err = decide.LoadSeriesVersion(ctx, a.st.Reader(), &t, versionID)
		if err != nil {
			return false, err
		}
	}
	if !full {
		return a.eng.QualityCutoffNotMet(v, f, &t)
	}
	return a.eng.FileCutoffNotMet(&t, v, f)
}
