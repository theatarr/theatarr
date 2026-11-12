package rss

import (
	"context"
	"fmt"
	"strconv"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/journal/snapshot"
	"github.com/theatarr/theatarr/internal/router"
)

// activeGroup is one title and version's accepted RSS items in a run.
type activeGroup struct {
	title   decide.Title
	version decide.Version
	ds      []*decide.Decision
	rels    map[*decide.Decision]indexer.Release
}

func (s *RSSSummary) collect(c candidate, rel indexer.Release, d *decide.Decision) {
	if s.active == nil {
		s.active = map[string]*activeGroup{}
	}
	k := strconv.FormatInt(c.title.ID, 10) + "/" + c.version.Key
	g := s.active[k]
	if g == nil {
		g = &activeGroup{title: c.title, version: c.version, rels: map[*decide.Decision]indexer.Release{}}
		s.active[k] = g
	}
	g.ds = append(g.ds, d)
	g.rels[d] = rel
}

// routeActive hands each title/version batch to the router as one job. The router's gate decides whether it writes.
func (r *Runner) routeActive(ctx context.Context, sum *RSSSummary) error {
	for _, k := range sortedKeys(sum.active) {
		g := sum.active[k]
		ranked := decide.Rank(g.ds)
		req := router.Request{TitleID: g.title.ID, VersionID: g.version.ID, Version: g.version.Key,
			Candidates: router.Candidates(ranked, g.rels), Source: "rss"}
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
