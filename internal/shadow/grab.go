package shadow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/ceiling"
	"github.com/theatarr/theatarr/internal/decide"
	dshadow "github.com/theatarr/theatarr/internal/decide/shadow"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// GrabResult is one grab re-decided.
type GrabResult struct {
	GrabID   int64    `json:"grab_id"`
	Instance string   `json:"instance"`
	History  int      `json:"history_id"`
	Title    string   `json:"title"`
	Version  string   `json:"version"`
	Outcome  Outcome  `json:"outcome"`
	Searched bool     `json:"searched"`
	Note     string   `json:"note,omitempty"`
	Arr      Decided  `json:"arr"`
	Pick     *Decided `json:"pick,omitempty"`
	Results  int      `json:"results"`
	Accepted int      `json:"accepted"`
	// Deferred: the search waits (hourly cap, or every indexer failed); a later job finishes the grab.
	Deferred bool `json:"deferred,omitempty"`
	// Skipped says why the grab is not shadowed (its version is not), when it is not.
	Skipped string `json:"skipped,omitempty"`
	// Basis is what the grab was compared against (search, rss, manual); Ours the row's release of ours;
	// SearchFoundBetter our search's pick when it ranks above ours on an RSS or manual grab.
	Basis             string   `json:"basis,omitempty"`
	Ours              *Decided `json:"ours,omitempty"`
	SearchFoundBetter *Decided `json:"search_found_better,omitempty"`
}

// errNoIndexer: every indexer the search tried failed (VPN down, all backing off).
var errNoIndexer = errors.New("no indexer answered")

func (r *Runner) handleGrab(ctx context.Context, j *jobs.Job) error {
	var p grabPayload
	if err := j.Decode(&p); err != nil {
		return jobs.Permanent(err)
	}
	run, err := r.forOperation(ctx)
	if err != nil {
		return err
	}
	ctx = run.pin(ctx)
	_, err = run.grab(ctx, p, false)
	return err
}

// Grab re-decides one arr grab (theatarr shadow run-once -grab), replacing what was stored for it.
func (r *Runner) Grab(ctx context.Context, p grabPayload) (*GrabResult, error) {
	run, err := r.forOperation(ctx)
	if err != nil {
		return nil, err
	}
	return run.grab(run.pin(ctx), p, true)
}

// GrabByID is Grab for a history id: of instance, or of the one instance that has it.
func (r *Runner) GrabByID(ctx context.Context, instance string, historyID int, noSearch bool) (*GrabResult, error) {
	run, err := r.forOperation(ctx)
	if err != nil {
		return nil, err
	}
	r = run
	if instance == "" {
		rows, err := r.st.Reader().QueryContext(ctx, `SELECT instance FROM arr_history WHERE arr_id = ? AND event_type = 'grabbed'`,
			historyID)
		if err != nil {
			return nil, err
		}
		var found []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				rows.Close()
				return nil, err
			}
			found = append(found, s)
		}
		rows.Close()
		switch len(found) {
		case 0:
			return nil, fmt.Errorf("no grab with history id %d", historyID)
		case 1:
			instance = found[0]
		default:
			return nil, fmt.Errorf("history id %d is a grab in %s: pick one with -instance", historyID, strings.Join(found, " and "))
		}
	}
	p := grabPayload{Instance: instance, HistoryID: historyID, NoSearch: noSearch}
	return run.grab(run.pin(ctx), p, true)
}

func (r *Runner) grab(ctx context.Context, p grabPayload, force bool) (*GrabResult, error) {
	ctx = decide.WithParseMemo(ctx)
	res := &GrabResult{Instance: p.Instance, History: p.HistoryID}
	key, ok := r.versionForInstance(p.Instance)
	res.Version = key
	if !ok {
		res.Skipped = fmt.Sprintf("version %q of %s is not shadowed", key, p.Instance)
		return res, nil
	}
	if vp := r.pol.Versions[key]; vp != nil && vp.Kind == policy.KindSeries {
		return r.grabSeries(ctx, p, key, force, res)
	}
	db := r.st.Reader()
	if !force {
		var state string
		err := db.QueryRowContext(ctx, `SELECT state FROM shadow_grab WHERE instance = ? AND history_id = ?`,
			p.Instance, p.HistoryID).Scan(&state)
		if err == nil && state == "done" {
			res.Skipped = "already done"
			return res, nil
		}
	}
	titles, err := r.titles(ctx)
	if err != nil {
		return nil, err
	}
	g, err := dshadow.LoadGrab(ctx, db, p.Instance, p.HistoryID, titles)
	if err != nil && strings.Contains(err.Error(), "do not hold") {
		// A title the last import added: reload and try once more.
		if titles, err = r.refresh(ctx); err != nil {
			return nil, err
		}
		g, err = dshadow.LoadGrab(ctx, db, p.Instance, p.HistoryID, titles)
	}
	if err != nil {
		return nil, err
	}
	res.Title = fmt.Sprintf("%s (%d)", g.Title.Name, g.Title.Year)
	v, err := decide.LoadVersion(ctx, db, r.pol, g.Title.ID, key)
	if err != nil {
		return nil, err
	}

	// The version's file at grab time: what the import saw, else the version_file row if it predates the grab,
	// else the file rebuilt from history.
	existing, source := p.Existing, "version_file"
	if existing == nil {
		files, err := r.files(ctx, p.Instance)
		if err != nil {
			return nil, err
		}
		if f, ok := files[v.ID]; ok {
			existing = fileAt(store.Millis(g.Date), &f)
		}
	}
	if existing == nil && g.Existing != nil {
		existing, source = g.Existing, "history"
	}
	if existing == nil {
		source = "none"
	}

	arrRel := g.Release
	arrRel.InfoHash = strings.ToLower(g.InfoHash)
	decidedRel := arrRel
	arrD, err := r.eng.Decide(ctx, arrRel, g.Title, v, existing)
	if err != nil {
		return nil, err
	}
	withoutOurs(arrD)
	arrRel.Indexer = g.Indexer
	res.Arr = summarize(arrD, arrRel, true)

	// Our own search.
	var found *cachedSearch
	searched := false
	var notes []string
	if p.NoSearch || !r.set.Search || r.idx == nil {
		notes = append(notes, "search off")
	} else {
		late := r.now().Sub(g.Date) > maxDefer
		var serr error
		found, serr = r.search(ctx, g.Title, key)
		switch {
		case serr == nil:
			searched = true
		case late || r.q == nil || force:
			notes = append(notes, "not searched: "+serr.Error())
		default:
			return r.deferGrab(ctx, p, g, key, res, serr)
		}
	}
	if found != nil {
		notes = append(notes, found.notes...)
	}

	// Decide every result for this version, rank them with the arr's release, and keep the best.
	var ds []*decide.Decision
	rels := map[*decide.Decision]indexer.Release{}
	// The arr's release is decided against stored standing whether or not our search ran or found anything;
	// a search that did run has stored its answers first, and cached results replay nothing.
	ctx, err = decide.WithStoredSeaDex(ctx, r.st.Reader())
	if err != nil {
		return nil, err
	}
	searchRels := r.withArrCacheEvidence(ctx, found, &decidedRel)
	arrD, err = r.eng.Decide(ctx, decidedRel, g.Title, v, existing)
	if err != nil {
		return nil, err
	}
	r.recacheArr(arrD, v, arrRel, decidedRel)
	withoutOurs(arrD)
	res.Arr = summarize(arrD, arrRel, true)
	if found != nil {
		vp := r.pol.Versions[key]
		for i, rel := range found.releases {
			if !slices.Contains(vp.Indexers, rel.Indexer) {
				continue
			}
			d, err := r.eng.Decide(ctx, decide.Release{Release: searchRels[i]}, g.Title, v, existing)
			if err != nil {
				return nil, err
			}
			ds = append(ds, d)
			rels[d] = rel
			if d.Accepted {
				res.Accepted++
			}
		}
	}
	if found != nil {
		r.recordCeiling(ctx, g.Title.ID, key, "movie", "shadow_search", ds, rels, found)
	}
	res.Results = len(ds)
	cands := ds
	if arrD.Accepted {
		cands = append(slices.Clone(ds), arrD)
	}
	ranked := decide.Rank(cands)
	if arrD.Accepted {
		rels[arrD] = arrCandidate(arrRel)
	}
	var kept []Decided
	foundArr := false
	for _, d := range ranked {
		if d == arrD {
			if d.Accepted && res.Pick == nil {
				p := res.Arr
				res.Pick = &p
			}
			continue
		}
		s := summarize(d, decide.Release{Release: rels[d]}, false)
		if sameRelease(&s, &res.Arr) {
			foundArr = true
		}
		if d.Accepted && res.Pick == nil {
			p := s
			res.Pick = &p
		}
		if len(kept) < r.set.KeepResults {
			kept = append(kept, s)
		}
	}
	res.Searched = searched
	res.Note = strings.Join(notes, "; ")
	if err := r.store(ctx, g, key, source, res, kept, foundArr); err != nil {
		return nil, err
	}
	r.grabPlan(ctx, res.GrabID, g.Title.ID, v, ranked, rels)
	r.m.grabs.WithLabelValues(res.Outcome.Bucket).Inc()
	r.log.Info("shadow grab", "instance", p.Instance, "history_id", p.HistoryID, "title", res.Title, "version", key,
		"bucket", res.Outcome.Bucket, "reason", res.Outcome.Reason, "searched", searched, "results", res.Results)
	if err := r.RefreshMetrics(ctx); err != nil {
		r.log.Warn("shadow metrics", "err", err)
	}
	return res, nil
}

// arrCandidate is the arr's release as the router plans it. The arr's indexer name is free text from its history: one
// equal to the placeholder of a release added by hand (policy.ManualIndexer) is not carried onto the candidate, so
// the plan does not treat an arr's grab as a manual add. The summary shown for the grab keeps the name.
func arrCandidate(rel decide.Release) indexer.Release {
	out := rel.Release
	if policy.IsManualAdd(out.Indexer) {
		out.Indexer = ""
	}
	return out
}

// deferGrab stores the grab as pending and enqueues it again: at the next hour when the cap held it back, else in
// a quarter of an hour.
func (r *Runner) deferGrab(ctx context.Context, p grabPayload, g *dshadow.Grab, key string, res *GrabResult, why error) (*GrabResult, error) {
	now := r.now()
	at := now.Add(15 * time.Minute)
	if errors.Is(why, errHourlyCap) {
		at = now.Truncate(time.Hour).Add(time.Hour + time.Duration(p.HistoryID%300)*time.Second)
	}
	if r.q == nil {
		return nil, fmt.Errorf("shadow grab %d deferred (%w) but there is no job queue to run it later", p.HistoryID, why)
	}
	p.Defers++
	// The pending row and the job that finishes it commit together: a pending grab always has its job.
	err := r.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO shadow_grab (instance, history_id, title_id, version_key,
			grabbed_at, release_title, release_source, arr_indexer, arr_quality, arr_score, state, search_note, created_at,
			updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?)
			ON CONFLICT (instance, history_id) DO UPDATE SET search_note = excluded.search_note, updated_at = excluded.updated_at
			WHERE shadow_grab.state = 'pending'`,
			p.Instance, p.HistoryID, g.Title.ID, key, store.Millis(g.Date), g.Release.Title, g.ReleaseSource, g.Indexer,
			g.Quality, nullInt(g.GrabScore), "waiting: "+why.Error(), store.Millis(now), store.Millis(now)); err != nil {
			return err
		}
		_, _, err := r.q.EnqueueTx(ctx, tx, KindGrab, p, jobs.EnqueueOptions{RunAt: at,
			DedupeKey: grabKey(p.Instance, p.HistoryID, p.Defers), Priority: priorityGrab, MaxAttempts: 3})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("deferring shadow grab %d: %w", p.HistoryID, err)
	}
	r.m.deferred.Inc()
	res.Deferred, res.Note = true, fmt.Sprintf("search deferred to %s: %v", at.UTC().Format(time.RFC3339), why)
	r.log.Info("shadow grab deferred", "instance", p.Instance, "history_id", p.HistoryID, "until", at.UTC(), "why", why)
	return res, nil
}

var errHourlyCap = errors.New("the hourly shadow search cap is reached")

// withCacheEvidence is a copy of rels with the cache evidence a live search applies before deciding: the same hash in
// another release and live stored provider_cache rows (Service.ApplyCachedOn), so the ranking's cache tie-break sees
// what the route reads. Stored evidence only: it asks no provider or DMM.
func (r *Runner) withCacheEvidence(ctx context.Context, rels []indexer.Release) []indexer.Release {
	out := slices.Clone(rels) // evidence is added with fresh slices and maps: the cached search is not changed
	if r.idx != nil {
		r.idx.ApplyCachedOn(ctx, out)
	}
	return out
}

// withArrCacheEvidence applies withCacheEvidence to the arr's release and the search results together: arr, the
// release decided, gets its evidence, and the results with theirs are returned in found's order. Both only rank: the
// evidence projects stored rows onto provider kinds, so plans carry the releases as read and order provider accounts
// by their own per-account rows.
func (r *Runner) withArrCacheEvidence(ctx context.Context, found *cachedSearch, arr *decide.Release) []indexer.Release {
	all := []indexer.Release{arr.Release}
	if found != nil {
		all = append(all, found.releases...)
	}
	all = r.withCacheEvidence(ctx, all)
	arr.CachedOn, arr.CachedVia = all[0].CachedOn, all[0].CachedVia
	return all[1:]
}

// recacheArr sets the arr decision's cache standing from the release the plan carries (the arr's indexer, whose
// provider allowlist applies) with the evidence decided gained.
func (r *Runner) recacheArr(d *decide.Decision, v decide.Version, arr, decided decide.Release) {
	arr.CachedOn, arr.CachedVia = decided.CachedOn, decided.CachedVia
	r.eng.Recache(d, v, arr.Release)
}

// search returns the title's search results across the indexers of the shadowed versions: from the reuse cache,
// or a new search (one at a time, within the hourly cap, skipping indexers whose budget is spent or backing off).
func (r *Runner) search(ctx context.Context, t decide.Title, version string) (*cachedSearch, error) {
	return r.searchTitle(ctx, indexer.Title{Kind: policy.KindMovie, Anime: t.Anime, Title: t.Name, Year: t.Year,
		Aliases: t.Aliases, IMDb: t.IMDb, TMDb: t.TMDb}, fmt.Sprint(t.ID), version)
}

// searchTitle is search for any title the planner plans: key names it in the reuse cache.
func (r *Runner) searchTitle(ctx context.Context, it indexer.Title, key, version string) (*cachedSearch, error) {
	key = fmt.Sprintf("%d/%s", r.generation, key)
	if c := r.cached(key); c != nil {
		return c, nil
	}
	r.searchMu.Lock()
	defer r.searchMu.Unlock()
	if c := r.cached(key); c != nil {
		return c, nil
	}
	n, err := r.searchesThisHour(ctx)
	if err != nil {
		return nil, err
	}
	if n >= r.set.SearchesPerHour {
		return nil, fmt.Errorf("%w (%d)", errHourlyCap, r.set.SearchesPerHour)
	}
	plan := indexer.PlanSearch(r.pol, r.idx, it)
	c := &cachedSearch{at: r.now()}
	ctx = gateway.WithPurpose(ctx, gateway.PurposeShadow)
	tried, answered := 0, 0
	for _, st := range plan.Steps {
		if !slices.ContainsFunc(st.Versions, func(v string) bool { return r.versions[v] }) {
			continue
		}
		if why := r.budgetBlocked(ctx, st.Indexer); why != "" {
			c.notes = append(c.notes, st.Indexer+": skipped, "+why)
			continue
		}
		for _, q := range st.Queries {
			tried++
			out, err := r.idx.Search(ctx, st.Indexer, q)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				c.notes = append(c.notes, st.Indexer+": "+err.Error())
				r.m.searches.WithLabelValues(st.Indexer, "error").Inc()
				continue
			}
			answered++
			season := -1
			if it.Kind == policy.KindSeries {
				season = q.Season
			}
			c.coverage = append(c.coverage, ceiling.Coverage{Indexer: st.Indexer, Kind: st.Kind, Season: season,
				Complete: !out.Cached && !out.Shared && !out.Truncated && q.Episode == 0})
			r.m.searches.WithLabelValues(st.Indexer, "ok").Inc()
			c.indexers = append(c.indexers, st.Indexer)
			c.releases = append(c.releases, out.Releases...)
			if !out.Cached && !out.Shared {
				c.requests += out.Requests
			}
			if out.Truncated {
				c.notes = append(c.notes, st.Indexer+": truncated")
			}
		}
	}
	for k, why := range plan.Skipped {
		if slices.ContainsFunc(r.pol.Versions[version].Indexers, func(s string) bool { return s == k }) {
			c.notes = append(c.notes, k+": "+why)
		}
	}
	if tried > 0 && answered == 0 {
		return nil, fmt.Errorf("%w: %s", errNoIndexer, strings.Join(c.notes, "; "))
	}
	r.cacheMu.Lock()
	for id, e := range r.searches {
		if r.now().Sub(e.at) >= r.set.SearchReuse {
			delete(r.searches, id)
		}
	}
	r.searches[key] = c
	r.cacheMu.Unlock()
	r.m.requests.Add(float64(c.requests))
	return c, nil
}

func (r *Runner) cached(key string) *cachedSearch {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	if c := r.searches[key]; c != nil && r.now().Sub(c.at) < r.set.SearchReuse {
		return c
	}
	return nil
}

// searchesThisHour counts the titles searched in the current clock hour (a reused search is the same title).
func (r *Runner) searchesThisHour(ctx context.Context) (int, error) {
	var n int
	err := r.st.Reader().QueryRowContext(ctx, `SELECT COUNT(DISTINCT title_id) FROM shadow_grab
		WHERE searched = 1 AND searched_at >= ?`, store.Millis(r.now().Truncate(time.Hour))).Scan(&n)
	return n, err
}

// budgetBlocked says why an indexer should not be asked now: its budget's hourly or daily cap is spent, or it or
// its budget is backing off. Empty when it may be asked.
func (r *Runner) budgetBlocked(ctx context.Context, key string) string {
	if r.budgets == nil {
		return ""
	}
	ix := r.pol.Indexers[key]
	if ix == nil {
		return ""
	}
	st := r.budgets.StateFor(ctx)
	for _, b := range st.Backoffs {
		if b.Active && (b.Subject == "indexer:"+key || (ix.Budget != "" && b.Subject == "budget:"+ix.Budget)) {
			return fmt.Sprintf("%s backing off until %s (%s)", b.Subject, b.Until.UTC().Format(time.RFC3339), b.Reason)
		}
	}
	for _, b := range st.Budgets {
		if b.Key == ix.Budget && (b.HourCapped || b.DayCapped) {
			return fmt.Sprintf("budget %s is spent for this period", b.Key)
		}
	}
	return ""
}

func nonNilSlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// store writes the grab, links the version's open RSS picks to it, and classifies it into its diff row, in one
// transaction.
func (r *Runner) store(ctx context.Context, g *dshadow.Grab, key, source string, res *GrabResult, kept []Decided,
	foundArr bool) error {
	now := store.Millis(r.now())
	historyIDs, err := jsonString(nonNilSlice(g.HistoryIDs))
	if err != nil {
		return err
	}
	episodes, err := jsonString(nonNilSlice(g.Episodes))
	if err != nil {
		return err
	}
	arrJSON, err := jsonString(res.Arr)
	if err != nil {
		return err
	}
	var pickJSON any
	if res.Pick != nil {
		s, err := jsonString(res.Pick)
		if err != nil {
			return err
		}
		pickJSON = s
	}
	if kept == nil {
		kept = []Decided{}
	}
	keptJSON, err := jsonString(kept)
	if err != nil {
		return err
	}
	var searchedAt any
	if res.Searched {
		searchedAt = now
	}
	return r.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var id int64
		err := tx.QueryRowContext(ctx, `INSERT INTO shadow_grab (instance, history_id, title_id, version_key, grabbed_at,
			release_title, release_source, arr_indexer, arr_quality, arr_score, existing_source, state, searched,
			searched_at, search_note, result_count, accepted_count, found_arr, arr_decision, pick, results, created_at,
			updated_at, history_ids, episodes) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'done', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (instance, history_id) DO UPDATE SET title_id = excluded.title_id,
				version_key = excluded.version_key, existing_source = excluded.existing_source, state = 'done', error = '',
				searched = excluded.searched, searched_at = excluded.searched_at, search_note = excluded.search_note,
				result_count = excluded.result_count, accepted_count = excluded.accepted_count,
				found_arr = excluded.found_arr, arr_decision = excluded.arr_decision, pick = excluded.pick,
				results = excluded.results, updated_at = excluded.updated_at, history_ids = excluded.history_ids,
				episodes = excluded.episodes
			RETURNING id`,
			g.Instance, g.HistoryID, g.Title.ID, key, store.Millis(g.Date), g.Release.Title, g.ReleaseSource, g.Indexer,
			g.Quality, nullInt(g.GrabScore), source, boolInt(res.Searched), searchedAt, res.Note, res.Results,
			res.Accepted, boolInt(foundArr), arrJSON, pickJSON, keptJSON, now, now, historyIDs, episodes).Scan(&id)
		if err != nil {
			return err
		}
		res.GrabID = id
		// Our RSS picks for this version, still open: the arr grabbed it, so they are linked.
		if _, err := tx.ExecContext(ctx, `UPDATE shadow_rss_pick SET status = 'linked', grab_id = ?
			WHERE title_id = ? AND version_key = ? AND status = 'open'`, id, g.Title.ID, key); err != nil {
			return err
		}
		cls, _, err := r.classifyStored(ctx, tx, id)
		if err != nil {
			return err
		}
		res.Outcome, res.Basis, res.Ours, res.SearchFoundBetter = cls.Outcome, cls.Basis, cls.Ours, cls.SearchBetter
		return nil
	})
}
