package shadow

import (
	"context"
	"slices"

	"github.com/theatarr/theatarr/internal/ceiling"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
)

func (r *Runner) recordCeiling(ctx context.Context, titleID int64, key, target, source string,
	ds []*decide.Decision, rels map[*decide.Decision]indexer.Release, found *cachedSearch) {
	at := r.now()
	var coverage []ceiling.Coverage
	if found != nil {
		at = found.at
		for _, c := range found.coverage {
			if slices.Contains(r.pol.Versions[key].Indexers, c.Indexer) {
				coverage = append(coverage, c)
			}
		}
	}
	if err := ceiling.Record(ctx, r.st.Writer(), r.pol, titleID, key, target, source, ds, rels, coverage, at); err != nil {
		r.log.Warn("ceiling: evidence not recorded", "title", titleID, "version", key, "err", err)
	}
}
