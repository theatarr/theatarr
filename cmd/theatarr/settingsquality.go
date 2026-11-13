package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/recyclarr"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
)

func newQualitySync(st *settings.Store, db *store.Store, manager *settings.RuntimeCoordinator, trash string) *settings.QualitySync {
	return &settings.QualitySync{Store: st, Coordinator: manager, TrashRoot: trash, ConvertRecyclarr: convertQualityRecyclarr,
		Impact: func(ctx context.Context, before, after *policy.Policy) (settings.DecisionImpact, error) {
			ctx = manager.Capture(ctx)
			runtime, err := runtimeForSearch(ctx, manager)
			if err != nil {
				return settings.DecisionImpact{}, err
			}
			return qualityDecisionImpactWithOptions(ctx, db, before, after, decide.Options{TrashRoot: trash, Previous: runtime.Engine})
		},
		FetchArchive: func(ctx context.Context, c settings.QualitySyncConfig) ([]byte, error) {
			ctx = manager.Capture(ctx)
			runtime, err := runtimeForSearch(ctx, manager)
			if err != nil {
				return nil, err
			}
			return runtime.Gateway.QualityArchive(ctx, runtime.Policy, c.ArchiveURL)
		},
		FetchCommit: func(ctx context.Context, c settings.QualitySyncConfig) (string, error) {
			ctx = manager.Capture(ctx)
			runtime, err := runtimeForSearch(ctx, manager)
			if err != nil {
				return "", err
			}
			return runtime.Gateway.QualityCommit(ctx, runtime.Policy, settings.TrackedCommitURL(c))
		},
	}
}

// qualitySyncHealth is the Quality sync health check: a warning while an enabled source's newest run failed (the
// settings keep their last state).
func qualitySyncHealth(ctx context.Context, qs *settings.QualitySync) health.Result {
	states, err := qs.SourceStates(ctx)
	if err != nil {
		return health.Result{Type: health.Warning, Message: "Could not read quality sync runs"}
	}
	snap, err := qs.Coordinator.Snapshot(ctx)
	if err != nil {
		return health.Result{Type: health.Warning, Message: "Could not read quality sync sources"}
	}
	enabled := map[string]bool{}
	for _, s := range snap.Records.SyncSources {
		enabled[s.Key] = s.Config.Enabled
	}
	var failed []string
	for _, st := range states {
		if enabled[st.Source] && st.LastRun.Outcome == "error" {
			last := st.LastCommit
			if len(last) > 12 {
				last = last[:12]
			}
			if last == "" {
				last = "none yet"
			}
			failed = append(failed, fmt.Sprintf("%s (%s; last good commit %s)", st.Source, st.LastRun.Error, last))
		}
	}
	detail := map[string]any{"sources": states}
	if len(failed) > 0 {
		return health.Result{Type: health.Warning, Detail: detail, Message: "Quality sync failed for " + strings.Join(failed, ", ")}
	}
	return health.Result{Type: health.OK, Detail: detail, Message: "Quality sync is up to date"}
}

// convertQualityRecyclarr converts a sync source's Recyclarr config: formats, and per profile its complete
// conversion, managed fields and reset rule. Keys the sync does not apply become notes.
func convertQualityRecyclarr(raw []byte, root, flavour string, sizes bool) (settings.RecyclarrImport, error) {
	out := settings.RecyclarrImport{}
	cfg, err := recyclarr.Parse(raw)
	if err != nil {
		return out, err
	}
	converted, err := recyclarr.Convert(cfg, recyclarr.Options{TrashRoot: root, QualitySizes: sizes, Reconcile: true})
	if err != nil {
		return out, err
	}
	trash, err := format.LoadTrash(root, format.Flavour(flavour))
	if err != nil {
		return out, err
	}
	seen := map[string]json.RawMessage{}
	for i, instance := range converted {
		if string(instance.Flavour) != flavour {
			return out, errors.New("settings: the Recyclarr instance flavour differs from the sync source")
		}
		for _, k := range cfg.Instances[i].Ignored {
			// Connection keys are stripped from the source; they are not quality settings.
			if k != "base_url" && k != "api_key" {
				out.Notes = append(out.Notes, fmt.Sprintf("%s.%s: skipped (Theatarr has no equivalent)", instance.Instance, k))
			}
		}
		hasSizes := cfg.Instances[i].QualityDefinition != nil
		for _, qp := range cfg.Instances[i].QualityProfiles {
			hasSizes = hasSizes || qp.QualityDefinition != nil
		}
		if hasSizes && !sizes {
			out.Notes = append(out.Notes, instance.Instance+".quality_definition: skipped (quality sizes are off for this source)")
		}
		for _, n := range instance.Notes {
			if strings.HasPrefix(n, "quality_definition: ") && !strings.Contains(n, "not applied") {
				out.Notes = append(out.Notes, instance.Instance+"."+n)
			}
		}
		for _, f := range instance.Formats {
			spec := trash.Raw(f.TrashID)
			if old, ok := seen[f.Name]; ok {
				var a, b any
				_ = json.Unmarshal(old, &a)
				_ = json.Unmarshal(spec, &b)
				aj, _ := json.Marshal(a)
				bj, _ := json.Marshal(b)
				if string(aj) != string(bj) {
					return out, fmt.Errorf("settings: Recyclarr formats named %q differ between instances; use separate sources", f.Name)
				}
				continue
			}
			seen[f.Name] = spec
			out.Formats = append(out.Formats, settings.CustomFormat{Record: settings.Record{Key: f.Name}, Config: policy.FormatRef{TrashID: f.TrashID, Flavour: flavour}, Specification: append(json.RawMessage(nil), spec...)})
		}
		for _, p := range instance.Profiles {
			out.Profiles = append(out.Profiles, settings.RecyclarrProfile{Key: p.Key, Config: *p.Policy, Fields: p.Fields, Reset: p.Reset})
		}
	}
	return out, nil
}
