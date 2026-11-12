package shadow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/theatarr/theatarr/internal/importarr"
)

// ImportSummary is one arr.import run.
type ImportSummary struct {
	Instances []ImportInstance `json:"instances"`
	TookMS    int64            `json:"took_ms"`
	Error     string           `json:"error,omitempty"`
}

// ImportInstance is one instance's part of an import run.
type ImportInstance struct {
	Instance string `json:"instance"`
	Version  string `json:"version"`
	// Changes is the rows the import changed; History the new history events.
	Changes int    `json:"changes"`
	History int    `json:"history"`
	Error   string `json:"error,omitempty"`
}

// doImport runs arr.import: every import.arr instance whose key is set.
func (r *Runner) doImport(ctx context.Context) (*ImportSummary, error) {
	ctx = r.pin(ctx)
	start := r.now()
	sum := &ImportSummary{}
	im := &importarr.Importer{Store: r.st, Now: r.now}
	var errs []error
	for _, inst := range sortedKeys(r.pol.Import.Arr) {
		a := r.pol.Import.Arr[inst]
		ii := ImportInstance{Instance: inst, Version: a.Version}
		err := r.importOne(ctx, im, inst, &ii)
		if err != nil {
			ii.Error = err.Error()
			errs = append(errs, fmt.Errorf("%s: %w", inst, err))
			r.log.Warn("arr import", "instance", inst, "err", err)
		}
		sum.Instances = append(sum.Instances, ii)
	}
	err := errors.Join(errs...)
	if err != nil {
		sum.Error = err.Error()
	}
	sum.TookMS = r.now().Sub(start).Milliseconds()
	r.record(ctx, KindImport, start, err == nil, sum)
	r.log.Info("arr import", "took_ms", sum.TookMS, "ok", err == nil)
	return sum, err
}

func (r *Runner) importOne(ctx context.Context, im *importarr.Importer, inst string, ii *ImportInstance) error {
	a := r.pol.Import.Arr[inst]
	g, err := r.arr(inst)
	if err != nil {
		return err
	}
	v := r.pol.Versions[a.Version]
	s, err := im.Run(ctx, importarr.Source{Instance: inst, Flavour: a.Flavour, Version: a.Version, Anime: v.Anime,
		Library: v.Library, CollectionTargets: r.pol.CollectionTargets()}, g)
	if err != nil {
		return err
	}
	ii.Changes, ii.History = s.Changes.Total(), s.History.New
	return nil
}

func jsonString(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}
