package shadow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// ErrNotFound is a diff id that does not exist.
var ErrNotFound = errors.New("shadow: not found")

// Summary is the report's head: agreement over the window, bucket and reason counts, and the runner's state.
type Summary struct {
	PendingMetadata int       `json:"pending_metadata"`
	Window          string    `json:"window"`
	Since           time.Time `json:"since"`
	// Bar is the exit bar (0.95); Agreement is agree over the window's grab rows that count (hand grabs left out),
	// nil with none.
	Bar       float64  `json:"bar"`
	Agreement *float64 `json:"agreement"`
	// Grabs counts the window's grab rows (including live theatarr-only grabs), hand grabs left out; Searched those where our
	// search ran; SearchFoundBetter the RSS and hand grabs whose later search found a release above the row's own
	// (information, not a bucket).
	Grabs             int `json:"grabs"`
	Searched          int `json:"searched"`
	SearchFoundBetter int `json:"search_found_better"`
	// HandGrabs are the arr's InteractiveSearch grabs in the window: listed, but outside every agreement ratio (the
	// overall one, per kind and per version), since a person picked the release.
	HandGrabs HandGrabs `json:"hand_grabs"`
	// Buckets counts every bucket in the window, the RSS one and hand grabs included.
	Buckets  map[string]int   `json:"buckets"`
	Reasons  []ReasonCount    `json:"reasons"`
	Versions []VersionSummary `json:"versions"`
	// Kinds is the agreement per kind (movie, series): each kind has its own exit bar.
	Kinds []KindSummary `json:"kinds"`
	// Pending grabs wait for a search; OpenPicks are RSS picks inside the link window.
	Pending   int      `json:"pending"`
	OpenPicks int      `json:"open_picks"`
	Cursors   []Cursor `json:"cursors"`
	LastRuns  []Run    `json:"last_runs"`
	// SearchedThisHour counts the titles searched in the current clock hour.
	SearchedThisHour int `json:"searched_this_hour"`
}

// ReasonCount is one reason category's count in a bucket.
type ReasonCount struct {
	Bucket string `json:"bucket"`
	Reason string `json:"reason"`
	Count  int    `json:"count"`
	// SearchFoundBetter counts the rows whose later search found a release above the row's own.
	SearchFoundBetter int `json:"search_found_better,omitempty"`
}

// HandGrabs counts hand grabs (InteractiveSearch): Accepted we accept (manual_grab_accepted), Rejected we reject
// (manual_grab_rejected).
type HandGrabs struct {
	Grabs    int `json:"grabs"`
	Accepted int `json:"accepted"`
	Rejected int `json:"rejected"`
}

// IsHandGrab is a diff reason of a hand grab (InteractiveSearch), which the agreement ratios leave out.
func IsHandGrab(reason string) bool {
	return reason == ReasonManualAccepted || reason == ReasonManualRejected
}

// VersionSummary is the agreement of one version (hand grabs left out).
type VersionSummary struct {
	Version   string   `json:"version"`
	Grabs     int      `json:"grabs"`
	Agree     int      `json:"agree"`
	Agreement *float64 `json:"agreement"`
}

// KindSummary is the agreement of one kind of title over the window, against the bar (hand grabs left out).
type KindSummary struct {
	Kind      string   `json:"kind"`
	Grabs     int      `json:"grabs"`
	Agree     int      `json:"agree"`
	Agreement *float64 `json:"agreement"`
	MeetsBar  bool     `json:"meets_bar"`
}

// Run is a runner run (arr.import or shadow.rss).
type Run struct {
	Kind       string          `json:"kind"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt time.Time       `json:"finished_at"`
	OK         bool            `json:"ok"`
	Summary    json.RawMessage `json:"summary"`
}

// LoadSummary reads the summary for the 14 days before now.
func LoadSummary(ctx context.Context, db *sql.DB, now time.Time) (*Summary, error) {
	since := now.Add(-agreementWindow)
	s := &Summary{Window: agreementWinName, Since: since.UTC(), Bar: AgreementBar, Buckets: map[string]int{},
		Reasons: []ReasonCount{}, Versions: []VersionSummary{}, Kinds: []KindSummary{}, Cursors: []Cursor{}, LastRuns: []Run{}}
	for _, b := range Buckets {
		s.Buckets[b] = 0
	}
	rows, err := db.QueryContext(ctx, `SELECT bucket, reason, version_key, searched, kind, COUNT(*),
		SUM(search_better IS NOT NULL) FROM shadow_diff WHERE at >= ? GROUP BY bucket, reason, version_key, searched, kind`,
		store.Millis(since))
	if err != nil {
		return nil, err
	}
	reasons := map[[2]string]int{}
	better := map[[2]string]int{}
	versions := map[string]*VersionSummary{}
	kinds := map[string]*KindSummary{}
	for _, k := range []string{"movie", "series"} {
		kinds[k] = &KindSummary{Kind: k}
	}
	for rows.Next() {
		var b, reason, v, kind string
		var searched bool
		var n, nb int
		if err := rows.Scan(&b, &reason, &v, &searched, &kind, &n, &nb); err != nil {
			rows.Close()
			return nil, err
		}
		if IsMetadataPending(reason) {
			s.PendingMetadata += n
			continue
		}
		s.Buckets[b] += n
		reasons[[2]string{b, reason}] += n
		better[[2]string{b, reason}] += nb
		s.SearchFoundBetter += nb
		if IsHandGrab(reason) {
			s.HandGrabs.Grabs += n
			if reason == ReasonManualAccepted {
				s.HandGrabs.Accepted += n
			} else {
				s.HandGrabs.Rejected += n
			}
			continue
		}
		if b != BucketRSSOnly || reason == ReasonTheatarrOnly {
			ks := kinds[kind]
			if ks == nil {
				ks = &KindSummary{Kind: kind}
				kinds[kind] = ks
			}
			ks.Grabs += n
			if b == BucketAgree {
				ks.Agree += n
			}
		}
		if b == BucketRSSOnly && reason != ReasonTheatarrOnly {
			continue
		}
		s.Grabs += n
		if searched {
			s.Searched += n
		}
		vs := versions[v]
		if vs == nil {
			vs = &VersionSummary{Version: v}
			versions[v] = vs
		}
		vs.Grabs += n
		if b == BucketAgree {
			vs.Agree += n
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	agree := 0
	for _, vs := range versions {
		agree += vs.Agree
	}
	s.Agreement = ratio(agree, s.Grabs)
	for k, n := range reasons {
		s.Reasons = append(s.Reasons, ReasonCount{Bucket: k[0], Reason: k[1], Count: n, SearchFoundBetter: better[k]})
	}
	order := map[string]int{}
	for i, b := range Buckets {
		order[b] = i
	}
	sort.Slice(s.Reasons, func(i, j int) bool {
		a, b := s.Reasons[i], s.Reasons[j]
		if a.Bucket != b.Bucket {
			return order[a.Bucket] < order[b.Bucket]
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Reason < b.Reason
	})
	s.Kinds = []KindSummary{}
	for _, k := range sortedKeys(kinds) {
		ks := kinds[k]
		ks.Agreement = ratio(ks.Agree, ks.Grabs)
		ks.MeetsBar = ks.Agreement != nil && *ks.Agreement >= AgreementBar
		s.Kinds = append(s.Kinds, *ks)
	}
	for _, k := range sortedKeys(versions) {
		vs := versions[k]
		vs.Agreement = ratio(vs.Agree, vs.Grabs)
		s.Versions = append(s.Versions, *vs)
	}
	if err := db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM shadow_grab WHERE state = 'pending'),
		(SELECT COUNT(*) FROM shadow_rss_pick WHERE status = 'open'),
		(SELECT COUNT(DISTINCT title_id) FROM shadow_grab WHERE searched = 1 AND searched_at >= ?)`,
		store.Millis(now.Truncate(time.Hour))).Scan(&s.Pending, &s.OpenPicks, &s.SearchedThisHour); err != nil {
		return nil, err
	}
	cs, err := Cursors(ctx, db)
	if err != nil {
		return nil, err
	}
	for _, k := range sortedKeys(cs) {
		s.Cursors = append(s.Cursors, cs[k])
	}
	rrows, err := db.QueryContext(ctx, `SELECT kind, started_at, finished_at, ok, summary FROM shadow_run r
		WHERE id = (SELECT MAX(id) FROM shadow_run WHERE kind = r.kind) ORDER BY kind`)
	if err != nil {
		return nil, err
	}
	defer rrows.Close()
	for rrows.Next() {
		var run Run
		var st, fi int64
		var sum string
		if err := rrows.Scan(&run.Kind, &st, &fi, &run.OK, &sum); err != nil {
			return nil, err
		}
		run.StartedAt, run.FinishedAt, run.Summary = store.FromMillis(st), store.FromMillis(fi), json.RawMessage(sum)
		s.LastRuns = append(s.LastRuns, run)
	}
	return s, rrows.Err()
}

func ratio(n, of int) *float64 {
	if of == 0 {
		return nil
	}
	r := float64(n) / float64(of)
	return &r
}

// DiffRow is one row of the diff report.
type DiffRow struct {
	ID        int64     `json:"id"`
	At        time.Time `json:"at"`
	TitleID   int64     `json:"title_id"`
	TitleName string    `json:"title"`
	Year      int       `json:"year,omitempty"`
	Version   string    `json:"version"`
	Bucket    string    `json:"bucket"`
	Reason    string    `json:"reason"`
	Detail    string    `json:"detail"`
	Searched  bool      `json:"searched"`
	GrabID    *int64    `json:"grab_id,omitempty"`
	RSSPickID *int64    `json:"rss_pick_id,omitempty"`
	// Ours: our pick. Arr: the arr's release, with our quality and score for it.
	OurRelease string `json:"our_release,omitempty"`
	OurQuality string `json:"our_quality,omitempty"`
	OurScore   *int   `json:"our_score,omitempty"`
	ArrRelease string `json:"arr_release,omitempty"`
	ArrQuality string `json:"arr_quality,omitempty"`
	ArrScore   *int   `json:"arr_score,omitempty"`
	// Instance and ReleaseSource of the arr grab, and the Basis it was compared against (search, rss, manual, live).
	Instance      string `json:"instance,omitempty"`
	ReleaseSource string `json:"release_source,omitempty"`
	Basis         string `json:"basis,omitempty"`
	// SearchFoundBetter is our later search's pick when it ranks above the row's own on an RSS or hand grab:
	// information, it does not change the bucket.
	SearchFoundBetter *Better `json:"search_found_better,omitempty"`
	// RouterPlan is what the router would do with the row's grab or RSS pick: "would add X on P", or why not
	// (router.PlanSummary). Empty when no plan was made.
	RouterPlan string `json:"router_plan,omitempty"`
}

// RouterPlan is the router's dry run for a grab or RSS pick (router_plan).
type RouterPlan struct {
	ID       int64         `json:"id"`
	Mode     string        `json:"mode"`
	Outcome  string        `json:"outcome"`
	Release  string        `json:"release,omitempty"`
	Indexer  string        `json:"indexer,omitempty"`
	Provider string        `json:"provider,omitempty"`
	At       time.Time     `json:"at"`
	Summary  string        `json:"summary"`
	Steps    []router.Step `json:"steps"`
}

// DiffFilter selects diff rows. Zero fields do not filter; Limit 0 is 50.
type DiffFilter struct {
	Bucket, Reason, Version string
	TitleID                 int64
	Since                   time.Time
	// SearchFoundBetter keeps only the rows whose later search found a release above the row's own.
	SearchFoundBetter bool
	Limit, Offset     int
}

const diffSelect = `SELECT d.id, d.at, d.title_id, t.name, COALESCE(t.year, 0), d.version_key, d.bucket, d.reason,
	d.detail, d.searched, d.grab_id, d.rss_pick_id, d.our_release, d.our_quality, d.our_score, d.arr_release,
	d.arr_quality, d.arr_score, COALESCE(g.instance, d.compare_instance), COALESCE(g.release_source, ''), d.search_better,
	p.outcome, p.release_title, p.provider, p.steps, d.compare_key
	FROM shadow_diff d JOIN title t ON t.id = d.title_id LEFT JOIN shadow_grab g ON g.id = d.grab_id
	LEFT JOIN shadow_rss_pick rp ON rp.id = d.rss_pick_id
	LEFT JOIN router_plan p ON p.id = COALESCE(g.router_plan_id, rp.router_plan_id)`

// ListDiffs returns diff rows newest first, and how many match.
func ListDiffs(ctx context.Context, db *sql.DB, f DiffFilter) ([]DiffRow, int, error) {
	var where []string
	var args []any
	add := func(cond string, v any) {
		where = append(where, cond)
		args = append(args, v)
	}
	if f.Bucket != "" {
		add("d.bucket = ?", f.Bucket)
	}
	if f.Reason != "" {
		add("d.reason = ?", f.Reason)
	}
	if f.Version != "" {
		add("d.version_key = ?", f.Version)
	}
	if f.TitleID > 0 {
		add("d.title_id = ?", f.TitleID)
	}
	if !f.Since.IsZero() {
		add("d.at >= ?", store.Millis(f.Since))
	}
	if f.SearchFoundBetter {
		where = append(where, "d.search_better IS NOT NULL")
	}
	w := ""
	if len(where) > 0 {
		w = " WHERE " + strings.Join(where, " AND ")
	}
	var total int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM shadow_diff d`+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	rows, err := db.QueryContext(ctx, diffSelect+w+` ORDER BY d.at DESC, d.id DESC LIMIT ? OFFSET ?`,
		append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []DiffRow{}
	for rows.Next() {
		d, err := scanDiff(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, d)
	}
	return out, total, rows.Err()
}

func scanDiff(rows interface{ Scan(...any) error }) (DiffRow, error) {
	var d DiffRow
	var at int64
	var grab, pick, ours, arrs sql.NullInt64
	var better, pOutcome, pRelease, pProvider, pSteps, compareKey sql.NullString
	err := rows.Scan(&d.ID, &at, &d.TitleID, &d.TitleName, &d.Year, &d.Version, &d.Bucket, &d.Reason, &d.Detail,
		&d.Searched, &grab, &pick, &d.OurRelease, &d.OurQuality, &ours, &d.ArrRelease, &d.ArrQuality, &arrs,
		&d.Instance, &d.ReleaseSource, &better, &pOutcome, &pRelease, &pProvider, &pSteps, &compareKey)
	if err != nil {
		return d, err
	}
	if pOutcome.Valid {
		var steps []router.Step
		_ = json.Unmarshal([]byte(pSteps.String), &steps)
		d.RouterPlan = router.PlanSummary(pOutcome.String, pRelease.String, pProvider.String, steps)
	}
	if grab.Valid {
		d.Basis = BasisFor(d.ReleaseSource)
	}
	if compareKey.Valid {
		d.Basis = BasisLive
	}
	if better.Valid {
		d.SearchFoundBetter = &Better{}
		if err := json.Unmarshal([]byte(better.String), d.SearchFoundBetter); err != nil {
			return d, err
		}
	}
	d.At = store.FromMillis(at)
	d.GrabID, d.RSSPickID, d.OurScore, d.ArrScore = nullInt64(grab), nullInt64(pick), nullIntP(ours), nullIntP(arrs)
	return d, nil
}

func nullInt64(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

func nullIntP(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}

// DiffDetail is one diff row with both decisions.
type DiffDetail struct {
	DiffRow
	Grab *GrabDetail `json:"grab,omitempty"`
	Pick *PickDetail `json:"rss_pick,omitempty"`
}

// GrabDetail is the stored re-decision of an arr grab.
type GrabDetail struct {
	ID             int64      `json:"id"`
	Instance       string     `json:"instance"`
	HistoryID      int        `json:"history_id"`
	GrabbedAt      time.Time  `json:"grabbed_at"`
	Release        string     `json:"release"`
	ReleaseSource  string     `json:"release_source"`
	ArrIndexer     string     `json:"arr_indexer"`
	ArrQuality     string     `json:"arr_quality"`
	ArrScore       *int       `json:"arr_score"`
	ExistingSource string     `json:"existing_source"`
	State          string     `json:"state"`
	Searched       bool       `json:"searched"`
	SearchedAt     *time.Time `json:"searched_at,omitempty"`
	SearchNote     string     `json:"search_note,omitempty"`
	ResultCount    int        `json:"result_count"`
	AcceptedCount  int        `json:"accepted_count"`
	FoundArr       bool       `json:"found_arr"`
	ArrDecision    Decided    `json:"arr_decision"`
	Pick           *Decided   `json:"pick,omitempty"`
	Results        []Decided  `json:"results"`
	// Plan is the router's dry run for the grab's ranked decisions.
	Plan *RouterPlan `json:"router_plan,omitempty"`
}

// PickDetail is an RSS pick.
type PickDetail struct {
	ID        int64      `json:"id"`
	Indexer   string     `json:"indexer"`
	Release   string     `json:"release"`
	SeenAt    time.Time  `json:"seen_at"`
	Published *time.Time `json:"published,omitempty"`
	Status    string     `json:"status"`
	Decision  Decided    `json:"decision"`
	// Plan is the router's dry run for the pick.
	Plan *RouterPlan `json:"router_plan,omitempty"`
}

// LoadDiff reads one diff row with its grab or RSS pick.
func LoadDiff(ctx context.Context, db *sql.DB, id int64) (*DiffDetail, error) {
	d, err := scanDiff(db.QueryRowContext(ctx, diffSelect+` WHERE d.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	out := &DiffDetail{DiffRow: d}
	if d.GrabID != nil {
		if out.Grab, err = loadGrab(ctx, db, *d.GrabID); err != nil {
			return nil, err
		}
	}
	if d.RSSPickID != nil {
		if out.Pick, err = loadPick(ctx, db, *d.RSSPickID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func loadGrab(ctx context.Context, db *sql.DB, id int64) (*GrabDetail, error) {
	var g GrabDetail
	var at int64
	var searchedAt, score sql.NullInt64
	var arrJSON, results string
	var pick sql.NullString
	var planID sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT id, instance, history_id, grabbed_at, release_title, release_source,
		arr_indexer, arr_quality, arr_score, existing_source, state, searched, searched_at, search_note, result_count,
		accepted_count, found_arr, arr_decision, pick, results, router_plan_id FROM shadow_grab WHERE id = ?`, id).Scan(&g.ID,
		&g.Instance, &g.HistoryID, &at, &g.Release, &g.ReleaseSource, &g.ArrIndexer, &g.ArrQuality, &score,
		&g.ExistingSource, &g.State, &g.Searched, &searchedAt, &g.SearchNote, &g.ResultCount, &g.AcceptedCount,
		&g.FoundArr, &arrJSON, &pick, &results, &planID)
	if err != nil {
		return nil, fmt.Errorf("shadow_grab %d: %w", id, err)
	}
	if g.Plan, err = loadPlan(ctx, db, planID); err != nil {
		return nil, err
	}
	g.GrabbedAt, g.ArrScore = store.FromMillis(at), nullIntP(score)
	if searchedAt.Valid {
		t := store.FromMillis(searchedAt.Int64)
		g.SearchedAt = &t
	}
	if err := json.Unmarshal([]byte(arrJSON), &g.ArrDecision); err != nil {
		return nil, err
	}
	if pick.Valid {
		g.Pick = &Decided{}
		if err := json.Unmarshal([]byte(pick.String), g.Pick); err != nil {
			return nil, err
		}
	}
	if err := json.Unmarshal([]byte(results), &g.Results); err != nil {
		return nil, err
	}
	if g.Results == nil {
		g.Results = []Decided{}
	}
	return &g, nil
}

func loadPick(ctx context.Context, db *sql.DB, id int64) (*PickDetail, error) {
	var p PickDetail
	var seen int64
	var pub sql.NullInt64
	var dec string
	var planID sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT id, indexer, release_title, seen_at, published_at, status, decision,
		router_plan_id FROM shadow_rss_pick WHERE id = ?`, id).Scan(&p.ID, &p.Indexer, &p.Release, &seen, &pub,
		&p.Status, &dec, &planID)
	if err != nil {
		return nil, fmt.Errorf("shadow_rss_pick %d: %w", id, err)
	}
	if p.Plan, err = loadPlan(ctx, db, planID); err != nil {
		return nil, err
	}
	p.SeenAt = store.FromMillis(seen)
	if pub.Valid {
		t := store.FromMillis(pub.Int64)
		p.Published = &t
	}
	return &p, json.Unmarshal([]byte(dec), &p.Decision)
}

// loadPlan reads a router plan; nil for none.
func loadPlan(ctx context.Context, db *sql.DB, id sql.NullInt64) (*RouterPlan, error) {
	if !id.Valid {
		return nil, nil
	}
	p := &RouterPlan{}
	var at int64
	var steps string
	err := db.QueryRowContext(ctx, `SELECT id, mode, outcome, release_title, indexer, provider, steps, created_at
		FROM router_plan WHERE id = ?`, id.Int64).Scan(&p.ID, &p.Mode, &p.Outcome, &p.Release, &p.Indexer, &p.Provider,
		&steps, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("router_plan %d: %w", id.Int64, err)
	}
	p.At = store.FromMillis(at)
	if err := json.Unmarshal([]byte(steps), &p.Steps); err != nil {
		return nil, err
	}
	p.Summary = router.PlanSummary(p.Outcome, p.Release, p.Provider, p.Steps)
	return p, nil
}
