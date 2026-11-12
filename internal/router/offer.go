package router

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Offers (grab once, link twice): when a release is imported for
// one version, or held through an adopted link or imported arr file, it is offered to the title's other versions.
// The target's own rules decide (its profile, through the decision engine, against its current files), as a
// partner Radarr's manual-import evaluation would. An accepted offer is a route request with that one
// release; the provider holds the hash already, so the router reuses it (no second add) and the library links the
// same content into the target's root. Nothing is ever removed from a target.
//
// What a partner sync between two arrs does, and how it is done here:
//   - on import or upgrade (its webhook): JobOffer is enqueued by the library when a download is imported;
//   - the partner looked up by title id: the same title, every other version of the policy;
//   - skip when the partner links the same file: the target already holds the hash (link or arr file) -> same;
//   - SKIP qualities: none needed, the target's profile rejects them (declined with its reasons);
//   - rejections remembered so the same file is not probed again: router_offer (version, title, hash);
//   - partner lacks the title yet, or the file is unreadable: pending every 15 min for 6 h, then gave_up;
//     gave_up retries daily at most seven more times; a readiness change can bring a retry forward;
//   - the 6-hourly sweep both ways: imported downloads, adopted links and imported arr files;
//     never back to their source or the arr's own library, never without a known hash (no_hash);
//   - replaced sources are not offered; their pending offers are declined with reason superseded;
//   - offer/backfill commands: theatarr router offer -download ID | -sweep [-dry-run] [-limit N].
//
// Shadow targets get the same decision; the route is then a dry-run plan (router_plan), so an offer is visible
// before cutover without writing anything.

// JobOffer offers an imported download or file source to the title's other versions.
const JobOffer = "router.offer"

// Offer retry schedule and sweep pacing.
const (
	OfferRetryEvery  = 15 * time.Minute
	OfferRetryFor    = 6 * time.Hour
	OfferGaveUpEvery = 24 * time.Hour
	OfferGaveUpLimit = 7
	// A planned offer (accepted while the gate was shut) older than OfferPlannedExpiry is not routed when the gate
	// opens: it is declined with reason planned_expired. A pass (sweep or file-source job) routes at most
	// OfferPlannedPerPass planned offers; the rest wait for the next sweep.
	OfferPlannedExpiry  = 7 * 24 * time.Hour
	OfferPlannedPerPass = 20
)

// Offer outcomes (router_offer.outcome).
const (
	OfferPending  = "pending"  // the target cannot decide yet; retried
	OfferRouted   = "routed"   // accepted; a route request was enqueued for an active target
	OfferPlanned  = "planned"  // accepted for a shadow target; the route is a dry-run plan
	OfferDeclined = "declined" // the target's rules rejected it (remembered)
	OfferSame     = "same"     // the target holds this release already
	OfferGaveUp   = "gave_up"  // still pending after OfferRetryFor
)

// DecideFunc decides a release for a version (decide.Engine.Decide).
type DecideFunc func(ctx context.Context, rel decide.Release, t decide.Title, v decide.Version,
	existing *decide.ExistingFile) (*decide.Decision, error)

// DecideSeriesFunc decides an imported series release for its mapped episodes and search criteria.
type DecideSeriesFunc func(ctx context.Context, rel decide.Release, t decide.Title, v decide.Version,
	search *decide.SeriesSearch) (*decide.Decision, error)

// Offerer runs offers. Store, Policy, Decide and Router are required.
type Offerer struct {
	Store        *store.Store
	Policy       *policy.Policy
	Decide       DecideFunc
	DecideSeries DecideSeriesFunc
	Router       interface {
		Enqueue(ctx context.Context, req Request, at time.Time) (int64, error)
	}
	Queue          *jobs.Queue
	Logger         *slog.Logger
	Now            func() time.Time
	CaptureRuntime func(context.Context) context.Context
	Runtime        func(context.Context) (*policy.Policy, DecideFunc, DecideSeriesFunc, error)
	seen           map[string]bool
	// planned counts the planned offers a pass has taken up again (OfferPlannedPerPass).
	planned int
}

// OfferResult is one offer of one source to one version.
type OfferResult struct {
	Version  string   `json:"version"`
	Outcome  string   `json:"outcome"`
	Reasons  []string `json:"reasons,omitempty"`
	Release  string   `json:"release"`
	Enqueued int64    `json:"job,omitempty"`
}

type offerPayload struct {
	DownloadID int64  `json:"download_id,omitempty"`
	SourceKind string `json:"source_kind,omitempty"`
	SourceID   int64  `json:"source_id,omitempty"`
}

func (o *Offerer) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o *Offerer) log() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return slog.Default()
}

// RegisterJobs adds the offer job.
func (o *Offerer) RegisterJobs(w *jobs.Worker) { w.Handle(JobOffer, o.handle) }

// EnqueueOffer queues an offer of an imported download (the library calls it after an import).
func EnqueueOffer(ctx context.Context, q *jobs.Queue, downloadID int64, at time.Time) error {
	if q == nil {
		return nil
	}
	_, _, err := q.Enqueue(ctx, JobOffer, offerPayload{DownloadID: downloadID}, jobs.EnqueueOptions{RunAt: at,
		DedupeKey: fmt.Sprintf("%s/%d/%d", JobOffer, downloadID, store.Millis(at)), MaxAttempts: 3})
	return err
}

func (o *Offerer) handle(ctx context.Context, j *jobs.Job) error {
	var p offerPayload
	if err := j.Decode(&p); err != nil {
		return jobs.Permanent(fmt.Errorf("router: bad offer payload: %w", err))
	}
	var res []OfferResult
	var err error
	if p.SourceKind == "" {
		res, err = o.Offer(ctx, p.DownloadID, false)
	} else {
		if o.CaptureRuntime != nil {
			ctx = o.CaptureRuntime(ctx)
		}
		active, e := o.forRuntime(ctx)
		if e != nil {
			return e
		}
		pass := *active
		pass.seen, pass.planned = map[string]bool{}, 0
		active = &pass
		if _, err = active.cancelSuperseded(ctx, false); err == nil {
			var sources []offerSource
			sources, _, err = active.fileSources(ctx)
			sources = groupOfferFiles(sources)
			for i := range sources {
				if err != nil {
					break
				}
				if sources[i].sourceKind == p.SourceKind && sources[i].id == p.SourceID {
					var offered []OfferResult
					offered, err = active.offerSource(ctx, &sources[i], false)
					res = append(res, offered...)
				}
			}
		}
	}
	if err != nil {
		return err
	}
	for _, r := range res {
		o.log().Info("offer", "download", p.DownloadID, "version", r.Version, "outcome", r.Outcome, "release", r.Release,
			"reasons", r.Reasons)
	}
	return nil
}

type offerSource struct {
	id                     int64
	titleID                int64
	version, hash, release string
	indexer, quality, kind string
	episodes               []int64
	size                   int64
	formats                string
	importedAt             int64
	sourceKind             string
	arrVersion             string
	path, target           string
	unreadable             bool
}

// Offer offers one imported download to every other version of its title, of the same kind. dryRun decides and
// reports without recording or enqueuing anything.
func (o *Offerer) Offer(ctx context.Context, downloadID int64, dryRun bool) ([]OfferResult, error) {
	if o.CaptureRuntime != nil {
		ctx = o.CaptureRuntime(ctx)
	}
	active, err := o.forRuntime(ctx)
	if err != nil {
		return nil, err
	}
	return active.offer(ctx, downloadID, dryRun)
}

func (o *Offerer) offer(ctx context.Context, downloadID int64, dryRun bool) ([]OfferResult, error) {
	db := o.Store.Reader()
	var s offerSource
	var size sql.NullInt64
	var formats sql.NullString
	var imported sql.NullInt64
	var episodes string
	err := db.QueryRowContext(ctx, `SELECT d.id, d.title_id, d.version_key, d.info_hash, d.release_title, d.indexer,
		d.quality, d.size_bytes, d.formats, d.imported_at, t.kind, d.episodes FROM download d JOIN title t ON t.id = d.title_id
		WHERE d.id = ? AND d.state = 'imported'`, downloadID).Scan(&s.id, &s.titleID, &s.version, &s.hash, &s.release,
		&s.indexer, &s.quality, &size, &formats, &imported, &s.kind, &episodes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil // not imported (or gone): nothing to offer
	}
	if err != nil {
		return nil, fmt.Errorf("router: offer: %w", err)
	}
	s.size, s.formats, s.importedAt = size.Int64, formats.String, imported.Int64
	_ = json.Unmarshal([]byte(episodes), &s.episodes)
	s.hash = indexer.NormalizeHash(s.hash)
	s.sourceKind = "download"
	if superseded, err := o.downloadSuperseded(ctx, s.id, s.version); err != nil {
		return nil, err
	} else if superseded {
		return o.cancelSource(ctx, &s, dryRun)
	}
	return o.offerSource(ctx, &s, dryRun)
}

func (o *Offerer) offerSource(ctx context.Context, s *offerSource, dryRun bool) ([]OfferResult, error) {
	ctx, err := decide.WithStoredSeaDex(ctx, o.Store.Reader())
	if err != nil {
		return nil, err
	}
	src := o.Policy.Versions[s.version]
	if src == nil || src.Kind != policy.Kind(s.kind) {
		return nil, nil
	}
	var out []OfferResult
	for _, key := range sortedVersionKeys(o.Policy) {
		if key == s.version || key == s.arrVersion {
			continue
		}
		vp := o.Policy.Versions[key]
		if vp.Kind != src.Kind || (src.Anime != nil && vp.Anime != nil && *src.Anime != *vp.Anime) {
			continue
		}
		r, err := o.offerTo(ctx, s, key, dryRun)
		if err != nil {
			return out, err
		}
		if r != nil {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (o *Offerer) offerTo(ctx context.Context, s *offerSource, key string, dryRun bool) (*OfferResult, error) {
	ctx = decide.WithSeaDexTitle(decide.WithSeaDexPolicy(ctx, o.Policy, o.Policy.Versions[key]), s.titleID)
	db := o.Store.Reader()
	res := &OfferResult{Version: key, Release: s.release}
	identity := fmt.Sprintf("%s/%d/%s", key, s.titleID, s.hash)
	if o.seen[identity] {
		return nil, nil
	}
	if o.seen != nil {
		o.seen[identity] = true
	}
	// Remembered: daily retries are bounded separately from the initial pending window.
	var memory offerMemory
	var reasons string
	var updatedAt int64
	err := db.QueryRowContext(ctx, `SELECT outcome, first_at, COALESCE(next_at, 0), retry_attempts, target_state,
		reasons, updated_at FROM router_offer WHERE version_key = ? AND title_id = ? AND info_hash = ?`,
		key, s.titleID, s.hash).Scan(&memory.outcome, &memory.firstAt, &memory.nextAt, &memory.retries, &memory.state,
		&reasons, &updatedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	switch memory.outcome {
	case "", OfferPending, OfferGaveUp:
	case OfferDeclined:
		// An unknown_indexer decline is decided again once the source's indexer is a configured one; the target's
		// own rules then decide. private_indexer declines, which no longer happen but older databases still hold,
		// are decided again the same way.
		if (reasons != `["unknown_indexer"]` && reasons != `["private_indexer"]`) || o.Policy.Indexers[s.indexer] == nil {
			return nil, nil
		}
		memory.outcome = ""
	case OfferPlanned:
		// Accepted while the gate was shut (router off, or the target in shadow): offered again once grabbing is on
		// for the target.
		if !o.gateOpen(key) {
			return nil, nil
		}
		// Not all at once at the first sweep after the flip, and not at all once old.
		if now := o.now(); now.Sub(store.FromMillis(updatedAt)) >= OfferPlannedExpiry {
			res.Outcome, res.Reasons = OfferDeclined, []string{"planned_expired"}
			return o.finish(ctx, s, res, memory, now, dryRun)
		}
		if o.seen != nil {
			if o.planned >= OfferPlannedPerPass {
				return nil, nil
			}
			o.planned++
		}
		memory.outcome = ""
	default:
		return nil, nil
	}
	if memory.outcome == OfferGaveUp && memory.retries >= OfferGaveUpLimit {
		return nil, nil
	}
	now := o.now()
	v, err := decide.LoadVersion(ctx, db, o.Policy, s.titleID, key)
	if err != nil {
		return nil, err
	}
	busy := false
	if v.ID != 0 {
		busy, err = InFlight(ctx, db, s.titleID, v.ID, nil)
		if err != nil {
			return nil, err
		}
	}
	state := fmt.Sprintf("%d/%t/%t", v.ID, busy, s.unreadable)
	if memory.outcome == OfferGaveUp && store.Millis(now) < memory.nextAt {
		previous := strings.Split(memory.state, "/")
		ready := len(previous) == 3 && ((v.ID != 0 && previous[0] != fmt.Sprint(v.ID)) ||
			(!busy && previous[1] == "true") || (!s.unreadable && previous[2] == "true"))
		if !ready {
			return nil, nil
		}
	}
	if memory.outcome == OfferGaveUp {
		memory.retries++
	}
	memory.state = state
	if s.hash == "" {
		res.Outcome, res.Reasons = OfferDeclined, []string{"no_hash"}
		return o.finish(ctx, s, res, memory, now, dryRun)
	}
	if s.unreadable {
		res.Outcome, res.Reasons = OfferPending, []string{"unreadable"}
		return o.finish(ctx, s, res, memory, now, dryRun)
	}
	if v.ID == 0 {
		res.Outcome, res.Reasons = OfferPending, []string{"the title has no " + key + " version yet"}
		return o.finish(ctx, s, res, memory, now, dryRun)
	}
	same, err := o.holds(ctx, v.ID, s.hash)
	if err != nil {
		return nil, err
	}
	if same {
		res.Outcome = OfferSame
		return o.finish(ctx, s, res, memory, now, dryRun)
	}
	if busy {
		res.Outcome, res.Reasons = OfferPending, []string{"the version has a download in flight"}
		return o.finish(ctx, s, res, memory, now, dryRun)
	}
	t, err := decide.LoadTitle(ctx, db, s.titleID)
	if err != nil {
		return nil, err
	}
	var existing *decide.ExistingFile
	var seriesSearch *decide.SeriesSearch
	if t.Kind == policy.KindSeries {
		if err := decide.LoadSeriesVersion(ctx, db, &t, v.ID); err != nil {
			return nil, err
		}
		seriesSearch = offerSeriesSearch(&t, s.episodes)
	} else if existing, err = decide.LoadExisting(ctx, db, v.ID); err != nil {
		return nil, err
	}
	// The source's indexer must be a configured one, so the target's indexer list (IndexerNotInVersion) can decide
	// whether its releases may go to the target's providers.
	ix := o.Policy.Indexers[s.indexer]
	if ix == nil {
		res.Outcome, res.Reasons = OfferDeclined, []string{"unknown_indexer"}
		return o.finish(ctx, s, res, memory, now, dryRun)
	}
	rel := indexer.Release{Indexer: s.indexer, Kind: ix.Kind, Privacy: ix.Privacy, Title: s.release, InfoHash: s.hash,
		Size: s.size, Seeders: -1, Peers: -1}
	var d *decide.Decision
	var crit []*decide.SeriesSearch
	if t.Kind == policy.KindSeries && o.DecideSeries != nil {
		// Judged in TVDB numbering and in each scene numbering of its episodes, the best kept (plainCriteria).
		crit = plainCriteria(seriesSearch, criteriaQueries(&t, seriesSearch, now))
		d, err = decideBest(ctx, o.DecideSeries, rel, t, v, crit, seriesSearch)
	} else {
		d, err = o.Decide(ctx, decide.Release{Release: rel}, t, v, existing)
	}
	if err != nil {
		return nil, err
	}
	if !d.Accepted {
		res.Outcome = OfferDeclined
		for _, r := range d.Rejections {
			res.Reasons = append(res.Reasons, r.String())
		}
		return o.finish(ctx, s, res, memory, now, dryRun)
	}
	// The route of a target the gate refuses (router.enabled off, or the target in shadow) is a dry-run plan: the
	// offer is recorded planned, not routed, and offered again once the gate is open.
	res.Outcome = OfferPlanned
	if o.gateOpen(key) {
		res.Outcome = OfferRouted
	}
	if !dryRun {
		req := Request{TitleID: s.titleID, VersionID: v.ID, Version: key, Source: "offer",
			Candidates: []Candidate{{Search: t.Kind == policy.KindSeries, Criteria: crit, Release: rel, Quality: d.Quality, Score: d.Score,
				Episodes: d.EpisodeIDs, Formats: append([]decide.FormatScore{}, d.Formats...), EpisodeMap: d.EpisodeMapping()}}}
		id, err := o.Router.Enqueue(ctx, req, time.Time{})
		if err != nil {
			return nil, err
		}
		res.Enqueued = id
	}
	return o.finish(ctx, s, res, memory, now, dryRun)
}

// gateOpen reports whether the router writes for the target version: router.enabled (Pause off) and the version
// active (as Router.Gate).
func (o *Offerer) gateOpen(version string) bool {
	return o.Policy.Grabs(version)
}

// offerSeriesSearch is the search for an offer's episodes, in TVDB numbering (the scene criteria are derived from it).
func offerSeriesSearch(t *decide.Title, ids []int64) *decide.SeriesSearch {
	if len(ids) == 0 {
		return nil
	}
	s := &decide.SeriesSearch{Episodes: append([]int64(nil), ids...), MonitoredOnly: true}
	for i := range t.Episodes {
		if t.Episodes[i].ID == ids[0] {
			s.Season, s.Episode = t.Episodes[i].Season, t.Episodes[i].Number
			break
		}
	}
	multi := len(ids) > 1
	switch {
	case t.SeriesType == "anime" && multi:
		s.Kind = profile.SearchAnimeSeason
	case t.SeriesType == "anime":
		s.Kind = profile.SearchAnimeEpisode
	case t.SeriesType == "daily" && !multi:
		s.Kind = profile.SearchDaily
	case t.SeriesType == "daily":
		s.Kind = profile.SearchDailySeason
	case multi:
		s.Kind = profile.SearchSeason
	default:
		s.Kind = profile.SearchSingleEpisode
	}
	return s
}

// holds reports whether a version already has the hash: an active link of ours, or an arr file of that content.
func (o *Offerer) holds(ctx context.Context, versionID int64, hash string) (bool, error) {
	var n int
	err := o.Store.Reader().QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM link l JOIN link_version lv ON lv.link_id = l.id LEFT JOIN content_file cf ON cf.id = l.content_file_id
			LEFT JOIN content c ON c.id = cf.content_id WHERE lv.version_id = ? AND l.state = 'active'
			AND lower(COALESCE(NULLIF(c.info_hash, ''), json_extract(l.file_metadata, '$.InfoHash'), '')) = ?)
		+ (SELECT COUNT(*) FROM version_file vf JOIN content c ON c.id = vf.content_id WHERE vf.version_id = ?
			AND lower(c.info_hash) = ?)
		+ (SELECT COUNT(*) FROM download WHERE version_id = ? AND lower(info_hash) = ? AND state <> 'failed')`,
		versionID, hash, versionID, hash, versionID, hash).Scan(&n)
	return n > 0, err
}

type offerMemory struct {
	outcome         string
	firstAt, nextAt int64
	retries         int
	state           string
}

// finish records the outcome and the next pending or bounded daily retry (unless a dry run).
func (o *Offerer) finish(ctx context.Context, s *offerSource, res *OfferResult, memory offerMemory, now time.Time,
	dryRun bool) (*OfferResult, error) {
	if memory.firstAt == 0 {
		memory.firstAt = store.Millis(now)
	}
	var next any
	var nextAt time.Time
	if res.Outcome == OfferPending {
		if now.Sub(store.FromMillis(memory.firstAt)) >= OfferRetryFor {
			res.Outcome = OfferGaveUp
			if memory.retries < OfferGaveUpLimit {
				nextAt = now.Add(OfferGaveUpEvery)
			} else {
				res.Reasons = append(res.Reasons, "retry_limit")
			}
		} else {
			nextAt = now.Add(OfferRetryEvery)
		}
	}
	if !nextAt.IsZero() {
		next = store.Millis(nextAt)
	}
	if dryRun {
		return res, nil
	}
	reasons, _ := json.Marshal(res.Reasons)
	if res.Reasons == nil {
		reasons = []byte("[]")
	}
	err := o.Store.WriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO router_offer (version_key, title_id, info_hash, from_version,
			release_title, outcome, reasons, attempts, first_at, next_at, updated_at,
			source_kind, source_id, retry_attempts, target_state) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (version_key, title_id, info_hash) DO UPDATE SET outcome = excluded.outcome,
			reasons = excluded.reasons, attempts = attempts + 1, next_at = excluded.next_at, updated_at = excluded.updated_at,
			from_version = excluded.from_version, source_kind = excluded.source_kind, source_id = excluded.source_id,
			retry_attempts = excluded.retry_attempts, target_state = excluded.target_state`,
			res.Version, s.titleID, s.hash, s.version, s.release, res.Outcome, string(reasons), memory.firstAt, next,
			store.Millis(now), s.sourceKind, s.id, memory.retries, memory.state)
		return err
	})
	if err == nil && !nextAt.IsZero() && o.Queue != nil {
		if s.sourceKind == "download" {
			err = EnqueueOffer(ctx, o.Queue, s.id, nextAt)
		} else {
			_, _, err = o.Queue.Enqueue(ctx, JobOffer, offerPayload{SourceKind: s.sourceKind, SourceID: s.id},
				jobs.EnqueueOptions{RunAt: nextAt, DedupeKey: fmt.Sprintf("%s/%s/%d/%d", JobOffer, s.sourceKind, s.id,
					store.Millis(nextAt)), MaxAttempts: 3})
		}
	}
	return res, err
}

// SweepResult counts a sweep's outcomes.
type SweepResult struct {
	Downloads int            `json:"downloads"`
	Outcomes  map[string]int `json:"outcomes"`
	Errors    []string       `json:"errors,omitempty"`
	Offers    []OfferResult  `json:"offers,omitempty"`
}

// Sweep offers imported downloads, adopted links and arr files (the 6-hourly backfill).
// limit caps the sources offered (0: all); a sweep stops after 5 errors.
func (o *Offerer) Sweep(ctx context.Context, dryRun bool, limit int) (*SweepResult, error) {
	if o.CaptureRuntime != nil {
		ctx = o.CaptureRuntime(ctx)
	}
	active, err := o.forRuntime(ctx)
	if err != nil {
		return nil, err
	}
	pass := *active
	pass.seen, pass.planned = map[string]bool{}, 0
	o = &pass
	// One standing pin for the whole sweep: every source is offered against the same SeaDex standing.
	if ctx, err = decide.WithStoredSeaDex(ctx, o.Store.Reader()); err != nil {
		return nil, err
	}
	rows, err := o.Store.Reader().QueryContext(ctx, `SELECT d.id FROM download d
		WHERE d.state = 'imported' ORDER BY d.imported_at DESC, d.id DESC`)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	sr := &SweepResult{Outcomes: map[string]int{}}
	cancelled, err := o.cancelSuperseded(ctx, dryRun)
	if err != nil {
		return sr, err
	}
	for _, r := range cancelled {
		sr.Outcomes[r.Outcome]++
	}
	if dryRun {
		sr.Offers = append(sr.Offers, cancelled...)
	}
	for _, id := range ids {
		if limit > 0 && sr.Downloads >= limit {
			break
		}
		res, err := o.offer(ctx, id, dryRun)
		if err != nil {
			sr.Errors = append(sr.Errors, fmt.Sprintf("download %d: %v", id, err))
			if len(sr.Errors) >= 5 {
				return sr, fmt.Errorf("router: offer sweep stopped after 5 errors")
			}
			continue
		}
		if len(res) > 0 {
			sr.Downloads++
		}
		for _, r := range res {
			sr.Outcomes[r.Outcome]++
		}
		if dryRun {
			sr.Offers = append(sr.Offers, res...)
		}
	}
	sources, record, err := o.fileSources(ctx)
	if err != nil {
		return sr, err
	}
	if !dryRun {
		// What the arr history gave for adopted links is kept on the link, so they keep their offers after it goes.
		if err := o.recordAdoptedSources(ctx, record); err != nil {
			return sr, err
		}
	}
	sources = groupOfferFiles(sources)
	for i := range sources {
		if limit > 0 && sr.Downloads >= limit {
			break
		}
		s := &sources[i]
		res, err := o.offerSource(ctx, s, dryRun)
		if err != nil {
			sr.Errors = append(sr.Errors, fmt.Sprintf("%s %d: %v", s.sourceKind, s.id, err))
			if len(sr.Errors) >= 5 {
				return sr, fmt.Errorf("router: offer sweep stopped after 5 errors")
			}
			continue
		}
		if len(res) > 0 {
			sr.Downloads++
		}
		for _, r := range res {
			sr.Outcomes[r.Outcome]++
		}
		if dryRun {
			sr.Offers = append(sr.Offers, res...)
		}
	}
	return sr, nil
}

func (o *Offerer) forRuntime(ctx context.Context) (*Offerer, error) {
	if o.Runtime == nil {
		return o, nil
	}
	pol, decide, decideSeries, err := o.Runtime(ctx)
	if err != nil {
		return nil, err
	}
	active := *o
	active.Policy, active.Decide, active.DecideSeries = pol, decide, decideSeries
	return &active, nil
}

func sortedVersionKeys(p *policy.Policy) []string { return sortedKeys(p.Versions) }
