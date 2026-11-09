package settings

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

const syncFormatID = "3a3ff47579026e76d6504ebea39390de"

func syncTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := fs.WalkDir(os.DirFS("testdata/trash"), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(root, path), 0700)
		}
		b, err := os.ReadFile(filepath.Join("testdata/trash", path))
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root, path), b, 0600)
	}); err != nil {
		t.Fatal(err)
	}
	return root
}

func editUpstream(t *testing.T, root, id string, edit func(map[string]any)) {
	t.Helper()
	path := filepath.Join(root, "radarr", id+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	edit(f)
	b, err = json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func syncService(t *testing.T) (*QualitySync, *policy.Policy, *Snapshot) {
	t.Helper()
	st, _, p := setup(t)
	initial := qualityBootstrap(t, st, p)
	coord, err := NewRuntimeCoordinator(st, p, initial, nil, func(context.Context, *Snapshot, *Runtime) (any, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	svc := &QualitySync{Store: st, Coordinator: coord, TrashRoot: syncTree(t), Impact: func(context.Context, *policy.Policy, *policy.Policy) (DecisionImpact, error) {
		return DecisionImpact{Files: 7, ScoreChanged: 2, Examples: []ImpactExample{}}, nil
	}}
	return svc, p, initial
}

func TestQualitySyncReviewAtomicApproval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, base, initial := syncService(t)
	editUpstream(t, svc.TrashRoot, syncFormatID, func(f map[string]any) {
		f["includeCustomFormatWhenRenaming"] = true
		f["trash_scores"] = map[string]any{"default": 12345}
	})
	profileKey := ""
	for key, p := range initial.Policy.Profiles {
		if _, ok := p.Formats["Remux Tier 01"]; ok {
			profileKey = key
			break
		}
	}
	if profileKey == "" {
		t.Fatal("fixture has no scored format")
	}
	source := QualitySyncSource{Record: Record{Key: "guides"}, Config: QualitySyncConfig{Kind: "trash", Flavour: "radarr", Profiles: map[string]string{profileKey: "default"}}}
	preview, err := svc.PreviewAndStore(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Changed) != 1 || len(preview.Scores) != 1 || preview.Impact.ScoreChanged != 2 || preview.ID == 0 || !strings.HasPrefix(preview.Commit, "content:") {
		t.Fatalf("incomplete preview: %+v", preview)
	}
	if svc.Coordinator.Current().Generation != initial.Generation {
		t.Fatal("preview applied changes")
	}
	// Review state and settings commit together: failure to mark approval must roll back the generation.
	if _, err := svc.Store.db.Writer().Exec(`CREATE TRIGGER fail_quality_approval BEFORE UPDATE OF state ON settings_quality_review WHEN NEW.state='applied' BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Apply(ctx, preview.ID, preview.Generation, "admin"); err == nil {
		t.Fatal("approval trigger ignored")
	}
	head, err := svc.Store.Load(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if head.Generation != initial.Generation || svc.Coordinator.Current().Generation != initial.Generation {
		t.Fatal("partial approval published")
	}
	if _, err := svc.Store.db.Writer().Exec("DROP TRIGGER fail_quality_approval"); err != nil {
		t.Fatal(err)
	}
	saved, err := svc.Apply(ctx, preview.ID, preview.Generation, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Generation != initial.Generation+1 || saved.Policy.Profiles[profileKey].Formats["Remux Tier 01"] != 12345 {
		t.Fatal("review did not save exact candidate")
	}
	if saved.ProviderEpoch != initial.ProviderEpoch || !reflect.DeepEqual(saved.Records.Providers, initial.Records.Providers) || !reflect.DeepEqual(saved.Policy.Router, initial.Policy.Router) || !reflect.DeepEqual(saved.Policy.Library, initial.Policy.Library) || !reflect.DeepEqual(saved.Policy.Notify, initial.Policy.Notify) || !sameModes(saved.Policy.Versions, initial.Policy.Versions) {
		t.Fatal("quality approval touched source records or safety switches")
	}
	if _, err := svc.Apply(ctx, preview.ID, preview.Generation, "admin"); !errors.Is(err, ErrConflict) {
		t.Fatal("approval replay accepted", err)
	}
	reviews, err := svc.Reviews(ctx)
	if err != nil || len(reviews) != 0 {
		t.Fatal("applied review still pending", err)
	}
	stale, err := svc.PreviewAndStore(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if stale.ID != 0 {
		t.Fatal("unchanged preview created a review")
	}
	editUpstream(t, svc.TrashRoot, syncFormatID, func(f map[string]any) { f["includeCustomFormatWhenRenaming"] = false })
	stale, err = svc.PreviewAndStore(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Coordinator.SaveAndApply(ctx, saveRequest(base, saved)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Apply(ctx, stale.ID, stale.Generation, "admin"); !errors.Is(err, ErrConflict) {
		t.Fatal("stale review rebased", err)
	}
}

func TestQualitySyncScheduleReviewAutoAndUnchanged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, base, initial := syncService(t)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	svc.Store.SetClock(func() time.Time { return now })
	req := saveRequest(base, initial)
	req.Records.SyncSources = []QualitySyncSource{{Record: Record{Key: "daily"}, Config: QualitySyncConfig{Kind: "trash", Flavour: "radarr", Enabled: true}}}
	saved, err := svc.Coordinator.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	source := saved.Records.SyncSources[0]
	if source.Config.Mode != "review" || source.Config.Schedule != "24h" {
		t.Fatal("unsafe sync defaults")
	}
	if err := svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	runs, _ := svc.Runs(ctx)
	if len(runs) != 1 || runs[0].Outcome != "unchanged" || svc.Coordinator.Current().Generation != saved.Generation {
		t.Fatal("unchanged run wrote a generation")
	}
	editUpstream(t, svc.TrashRoot, syncFormatID, func(f map[string]any) { f["includeCustomFormatWhenRenaming"] = true })
	if err := svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	runs, _ = svc.Runs(ctx)
	if len(runs) != 1 {
		t.Fatal("ran before schedule")
	}
	now = now.Add(24 * time.Hour)
	if err := svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	runs, _ = svc.Runs(ctx)
	pending, _ := svc.Reviews(ctx)
	if len(runs) != 2 || runs[0].Outcome != "review" || len(pending) != 1 || svc.Coordinator.Current().Generation != saved.Generation {
		t.Fatal("review mode applied or omitted pending review")
	}
	req = saveRequest(base, saved)
	req.Records.SyncSources[0].Config.Mode = "auto"
	saved, err = svc.Coordinator.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RunSource(ctx, source, true); !errors.Is(err, ErrConflict) {
		t.Fatal("stale source config accepted", err)
	}
	if err := svc.RunSource(ctx, saved.Records.SyncSources[0], true); err != nil {
		t.Fatal(err)
	}
	runs, _ = svc.Runs(ctx)
	if runs[0].Outcome != "applied" || svc.Coordinator.Current().Generation != saved.Generation+1 {
		t.Fatal("auto failed to save")
	}
	found := false
	for _, a := range audit(t, svc.Store) {
		if a.Operation == "quality-import" && a.Actor == "sync:daily" {
			found = true
		}
	}
	if !found {
		t.Fatal("auto actor absent from audit")
	}
}

func TestQualitySyncRetainsRemovedAndRenamedFormats(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _, initial := syncService(t)
	for _, renamed := range []bool{true, false} {
		path := filepath.Join(svc.TrashRoot, "radarr", syncFormatID+".json")
		if renamed {
			editUpstream(t, svc.TrashRoot, syncFormatID, func(f map[string]any) { f["name"] = "New upstream name" })
		} else if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		p, err := svc.Preview(ctx, QualitySyncSource{Record: Record{Key: "guides"}, Config: QualitySyncConfig{Kind: "trash", Flavour: "radarr"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Retired) != 1 || p.Retired[0].TrashID != syncFormatID || p.HasChanges() {
			t.Fatalf("retirement changed policy: %+v", p)
		}
		for _, f := range p.candidate.Formats {
			if f.Key == "Remux Tier 01" {
				for _, old := range initial.Records.Formats {
					if old.Key == f.Key && !reflect.DeepEqual(old.Specification, f.Specification) {
						t.Fatal("retired spec replaced")
					}
				}
			}
		}
	}
}

func TestQualitySyncConfigRejectsUnpinnedArchivesAndStripsCredentials(t *testing.T) {
	t.Parallel()
	for _, url := range []string{"https://guides.test/main.tar.gz", "https://user:secret@guides.test/" + strings.Repeat("a", 40), "https://guides.test/" + strings.Repeat("a", 40) + "?token=secret"} {
		c := QualitySyncConfig{Kind: "trash", Flavour: "radarr", ArchiveURL: url, Commit: strings.Repeat("a", 40)}
		if err := c.validate(); err == nil {
			t.Fatal("unsafe URL accepted", url)
		}
	}
	c := QualitySyncConfig{Kind: "recyclarr", Flavour: "radarr", Recyclarr: "radarr:\n  home:\n    base_url: https://private.invalid\n    api_key: sentinel-secret\n"}
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c.Recyclarr, "sentinel-secret") || strings.Contains(c.Recyclarr, "private.invalid") {
		t.Fatal("Recyclarr credentials retained")
	}
}

func TestQualitySyncReviewsNewZeroScore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, base, initial := syncService(t)
	req := saveRequest(base, initial)
	for i := range req.Records.Profiles {
		if req.Records.Profiles[i].Key == "remux-2160p" {
			req.Records.Profiles[i].Config.Formats = map[string]int{}
		}
	}
	saved, err := svc.Coordinator.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	p, err := svc.PreviewAndStore(ctx, QualitySyncSource{Record: Record{Key: "zero-score"}, Config: QualitySyncConfig{Kind: "trash", Flavour: "radarr", TrashIDs: []string{syncFormatID}, Profiles: map[string]string{"remux-2160p": "french-multi-vf"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Scores) != 1 || p.Scores[0].Before != 0 || p.Scores[0].After != 0 || p.ID == 0 || !p.HasChanges() {
		t.Fatalf("new zero score bypassed review: id=%d scores=%+v", p.ID, p.Scores)
	}
	if svc.Coordinator.Current().Generation != saved.Generation {
		t.Fatal("preview applied score")
	}
	result, err := svc.Apply(ctx, p.ID, p.Generation, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if score, ok := result.Policy.Profiles["remux-2160p"].Formats["Remux Tier 01"]; !ok || score != 0 {
		t.Fatal("approved zero score absent")
	}
}
