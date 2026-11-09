package main

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
)

func qualityDecisionImpact(ctx context.Context, db *store.Store, before, after *policy.Policy, trash string) (settings.DecisionImpact, error) {
	return qualityDecisionImpactWithOptions(ctx, db, before, after, decide.Options{TrashRoot: trash})
}

func qualityDecisionImpactWithOptions(ctx context.Context, db *store.Store, before, after *policy.Policy, options decide.Options) (settings.DecisionImpact, error) {
	out := settings.DecisionImpact{Examples: []settings.ImpactExample{}}
	// Empty libraries need no engine or arr reads, including during initial setup.
	rows, err := db.Reader().QueryContext(ctx, "SELECT id,title_id,policy_key FROM version ORDER BY id")
	if err != nil {
		return out, err
	}
	type versionRow struct {
		id, title int64
		key       string
	}
	versions := []versionRow{}
	for rows.Next() {
		var v versionRow
		if err := rows.Scan(&v.id, &v.title, &v.key); err != nil {
			rows.Close()
			return out, err
		}
		versions = append(versions, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(versions) == 0 {
		return out, nil
	}
	oldEngine, oldErr := decide.New(before, options)
	options.Previous = oldEngine
	newEngine, newErr := decide.New(after, options)
	if oldErr != nil || newErr != nil {
		return out, fmt.Errorf("settings: impact engines: %v; %v", oldErr, newErr)
	}
	shared := sharedDecisionInputsEqual(before, after)
	for _, v := range versions {
		oldVersion, newVersion := before.Versions[v.key], after.Versions[v.key]
		if oldVersion == nil || newVersion == nil {
			continue
		}
		// Only versions the change can affect are checked.
		if shared && reflect.DeepEqual(oldVersion, newVersion) &&
			reflect.DeepEqual(before.Profiles[oldVersion.Profile], after.Profiles[newVersion.Profile]) {
			continue
		}
		title, err := decide.LoadTitle(ctx, db.Reader(), v.title)
		if err != nil {
			return out, err
		}
		files := map[int64]*decide.ExistingFile{}
		if title.Kind == policy.KindSeries {
			if err := decide.LoadSeriesVersion(ctx, db.Reader(), &title, v.id); err != nil {
				return out, err
			}
			for _, ep := range title.Episodes {
				if ep.File != nil {
					files[ep.FileID] = ep.File
				}
			}
		} else {
			f, id, err := decide.LoadExistingFile(ctx, db.Reader(), v.id)
			if err != nil {
				return out, err
			}
			if f != nil {
				files[id] = f
			}
		}
		for _, id := range sortedFileIDs(files) {
			oldFile, newFile := *files[id], *files[id]
			oldFile.Score, newFile.Score = nil, nil
			oldV, newV := decide.Version{ID: v.id, Key: v.key, Policy: oldVersion}, decide.Version{ID: v.id, Key: v.key, Policy: newVersion}
			if err := oldEngine.ScoreFile(&title, oldV, &oldFile); err != nil {
				return out, err
			}
			if err := newEngine.ScoreFile(&title, newV, &newFile); err != nil {
				return out, err
			}
			oldUnmet, err := oldEngine.FileCutoffNotMet(&title, oldV, &oldFile)
			if err != nil {
				return out, err
			}
			newUnmet, err := newEngine.FileCutoffNotMet(&title, newV, &newFile)
			if err != nil {
				return out, err
			}
			oldPr, newPr := before.Profiles[oldVersion.Profile], after.Profiles[newVersion.Profile]
			oldUpgrade := oldUnmet && oldVersion.UpgradeRule() != policy.UpgradesOff && (oldPr.Upgrades == nil || *oldPr.Upgrades)
			newUpgrade := newUnmet && newVersion.UpgradeRule() != policy.UpgradesOff && (newPr.Upgrades == nil || *newPr.Upgrades)
			a, b := 0, 0
			if oldFile.Score != nil {
				a = *oldFile.Score
			}
			if newFile.Score != nil {
				b = *newFile.Score
			}
			out.Files++
			if a != b || oldUnmet != newUnmet || oldUpgrade != newUpgrade {
				out.Changed++
			}
			if a != b {
				out.ScoreChanged++
			}
			if oldUnmet != newUnmet {
				out.CutoffChanged++
			}
			if oldUpgrade != newUpgrade {
				out.UpgradeChanged++
			}
			if len(out.Examples) < 20 && (a != b || oldUnmet != newUnmet || oldUpgrade != newUpgrade) {
				out.Examples = append(out.Examples, settings.ImpactExample{Title: title.Name, Version: v.key, File: oldFile.SceneName, BeforeScore: a, AfterScore: b, BeforeCutoffMet: !oldUnmet, AfterCutoffMet: !newUnmet, BeforeUpgrade: oldUpgrade, AfterUpgrade: newUpgrade})
			}
		}
	}
	return out, nil
}

// sharedDecisionInputsEqual reports whether everything but the versions and profiles (formats, format sets and the
// rest) is the same in both policies, so an unchanged version with an unchanged profile decides every file the same.
func sharedDecisionInputsEqual(before, after *policy.Policy) bool {
	b, a := *before, *after
	b.Versions, a.Versions, b.Profiles, a.Profiles = nil, nil, nil, nil
	b.Revision, a.Revision, b.Files, a.Files = "", "", nil, nil
	return reflect.DeepEqual(b, a)
}

func sortedFileIDs(files map[int64]*decide.ExistingFile) []int64 {
	ids := make([]int64, 0, len(files))
	for id := range files {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
