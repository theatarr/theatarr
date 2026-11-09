package shadow

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/journal/snapshot"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
)

// candidateOf is one accepted decision as a router candidate.
func candidateOf(rel indexer.Release, d *decide.Decision) router.Candidate {
	return router.Candidate{Release: rel, Quality: d.Quality, Score: d.Score, Episodes: d.EpisodeIDs,
		Formats: append([]decide.FormatScore{}, d.Formats...), EpisodeMap: d.EpisodeMapping()}
}

// plan asks the router what it would do with a shadow version's grab decision or RSS pick (Router.Route, which for
// a version not in mode active only records a dry-run plan) and links the plan to the row (table shadow_grab or
// shadow_rss_pick). It never routes for a version that is not shadowed here, and a failure only logs: the shadow
// report does not depend on it.
func (r *Runner) plan(ctx context.Context, req router.Request, table string, id int64) {
	if r.rt == nil || !r.versions[req.Version] {
		return
	}
	if vp := r.pol.Versions[req.Version]; vp == nil || vp.Mode == policy.ModeActive {
		return
	}
	res, err := r.rt.Route(ctx, req)
	if err != nil {
		r.log.Warn("router plan", "version", req.Version, "title", req.TitleID, "err", err)
		return
	}
	if res.PlanID == 0 {
		return
	}
	q := `UPDATE shadow_grab SET router_plan_id = ? WHERE id = ?`
	if table == "shadow_rss_pick" {
		q = `UPDATE shadow_rss_pick SET router_plan_id = ? WHERE id = ?`
	}
	if _, err := r.st.Writer().ExecContext(ctx, q, res.PlanID, id); err != nil {
		r.log.Warn("router plan link", "table", table, "id", id, "err", err)
	}
}

// grabPlan plans a stored grab: its ranked decisions, the arr's release among them.
func (r *Runner) grabPlan(ctx context.Context, grabID, titleID int64, v decide.Version, ranked []*decide.Decision,
	rels map[*decide.Decision]indexer.Release) {
	r.plan(ctx, router.Request{TitleID: titleID, VersionID: v.ID, Version: v.Key,
		Candidates: router.Candidates(ranked, rels), Source: "shadow.grab"}, "shadow_grab", grabID)
}

// activeGroup is one title and version's accepted RSS items in a run.
type activeGroup struct {
	title   decide.Title
	version decide.Version
	ds      []*decide.Decision
	rels    map[*decide.Decision]indexer.Release
	picks   map[*decide.Decision]candidate
}

func (s *RSSSummary) collect(c candidate, rel indexer.Release, d *decide.Decision) {
	if s.active == nil {
		s.active = map[string]*activeGroup{}
	}
	k := strconv.FormatInt(c.title.ID, 10) + "/" + c.version.Key
	g := s.active[k]
	if g == nil {
		g = &activeGroup{title: c.title, version: c.version, rels: map[*decide.Decision]indexer.Release{},
			picks: map[*decide.Decision]candidate{}}
		s.active[k] = g
	}
	g.ds = append(g.ds, d)
	g.rels[d] = rel
	g.picks[d] = c
}

// routeActive hands each title/version batch to the router: active versions get one job, shadow versions one
// coverage plan shared by the picks it would place. The router's gate decides whether it writes.
func (r *Runner) routeActive(ctx context.Context, sum *RSSSummary) error {
	for _, k := range sortedKeys(sum.active) {
		g := sum.active[k]
		ranked := decide.Rank(g.ds)
		req := router.Request{TitleID: g.title.ID, VersionID: g.version.ID, Version: g.version.Key,
			Candidates: router.Candidates(ranked, g.rels), Source: "rss"}
		if !r.active[g.version.Key] {
			if err := r.planRSS(ctx, sum, g, ranked, req); err != nil {
				return err
			}
			continue
		}
		busy := true
		for _, d := range g.ds {
			inFlight, err := router.InFlight(ctx, r.st.Reader(), g.title.ID, g.version.ID, d.EpisodeIDs)
			if err != nil {
				return err
			}
			if !inFlight {
				busy = false
				break
			}
		}
		if busy {
			sum.RouteSkipped++
			continue
		}
		// The grab journal keeps what this read decided between (internal/journal/snapshot).
		if err := snapshot.Record(ctx, r.st.Writer(), snapshot.Search{Source: req.Source, TitleID: g.title.ID,
			Version: g.version.Key, Target: "rss", Ranked: ranked, Releases: g.rels, CreatedAt: r.now(),
			PolicyRev: r.pol.Revision}); err != nil {
			r.log.Warn("journal: decision not recorded", "title", g.title.ID, "version", g.version.Key, "err", err)
		}
		job, err := r.rt.Enqueue(ctx, req, r.now())
		if err != nil {
			return fmt.Errorf("routing RSS picks of %s: %w", g.version.Key, err)
		}
		sum.Routed++
		r.log.Info("rss: routed", "title", g.title.Name, "version", g.version.Key, "candidates", len(req.Candidates),
			"best", req.Candidates[0].Release.Title, "job", job)
	}
	return nil
}

func (r *Runner) planRSS(ctx context.Context, sum *RSSSummary, g *activeGroup, ranked []*decide.Decision,
	req router.Request) error {
	var plan *router.Result
	if r.rt != nil {
		req.Source = "shadow.rss"
		var err error
		plan, err = r.rt.Route(ctx, req)
		if err != nil {
			r.log.Warn("router plan", "version", req.Version, "title", req.TitleID, "err", err)
			plan = nil
		}
	}
	var covered []int64
	placed := false
	for _, d := range ranked {
		rel := g.rels[d]
		if plan != nil {
			// The router routes by the release's normalised identity and rejects a conflicting one.
			hash, conflict := indexer.ReleaseIdentity(rel)
			selected := false
			for _, st := range plan.Steps {
				if !conflict && hash != "" && st.Hash == hash && st.Release == rel.Title && st.Outcome == "would_add" {
					selected = true
					break
				}
			}
			if !selected {
				continue
			}
		}
		// Apply coverage even without a router (offline comparisons), and keep duplicate feed items from
		// recording the same planned placement twice.
		if placed && (g.title.Kind != policy.KindSeries || len(d.EpisodeIDs) == 0 || len(covered) == 0) {
			continue
		}
		if slices.ContainsFunc(d.EpisodeIDs, func(id int64) bool { return slices.Contains(covered, id) }) {
			continue
		}
		placed = true
		covered = append(covered, d.EpisodeIDs...)
		id, err := r.insertPick(ctx, rel, g.picks[d], d)
		if err != nil {
			return err
		}
		if id != 0 {
			sum.Picks++
			if plan != nil && plan.PlanID != 0 {
				if _, err := r.st.Writer().ExecContext(ctx, `UPDATE shadow_rss_pick SET router_plan_id = ? WHERE id = ?`,
					plan.PlanID, id); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
