package settings

import (
	"bytes"
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

// pendingReviews counts the stored reviews still pending (none since sync applies by itself).
func pendingReviews(t *testing.T, svc *QualitySync) int {
	t.Helper()
	var n int
	if err := svc.Store.db.Reader().QueryRow("SELECT COUNT(*) FROM settings_quality_review WHERE state='pending'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A run applies by itself: a specification change, a format added by its TRaSH id and the scores of a bound profile,
// in one generation with one update log entry; nothing waits. The apply and its log entry commit together. A
// preview is a dry run.
func TestQualitySyncAppliesWithoutReview(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, base, initial := syncService(t)
	const newID = "0123456789abcdef0123456789abcdef"
	raw, err := os.ReadFile(filepath.Join(svc.TrashRoot, "radarr", syncFormatID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.ReplaceAll(bytes.ReplaceAll(raw, []byte(syncFormatID), []byte(newID)), []byte("Remux Tier 01"), []byte("Remux Tier 99"))
	if err := os.WriteFile(filepath.Join(svc.TrashRoot, "radarr", newID+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
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
	req := saveRequest(base, initial)
	req.Records.SyncSources = []QualitySyncSource{{Record: Record{Key: "guides"}, Config: QualitySyncConfig{Kind: "trash", Flavour: "radarr", TrashIDs: []string{newID}, Profiles: map[string]string{profileKey: "default"}}}}
	saved, err := svc.Coordinator.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	source := saved.Records.SyncSources[0]
	preview, err := svc.Preview(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Changed) != 1 || len(preview.Added) != 1 || len(preview.Scores) == 0 || preview.Impact.ScoreChanged != 2 || !strings.HasPrefix(preview.Commit, "content:") {
		t.Fatalf("incomplete preview: %+v", preview)
	}
	if svc.Coordinator.Current().Generation != saved.Generation || pendingReviews(t, svc) != 0 {
		t.Fatal("preview applied or stored changes")
	}
	// The settings and the log entry commit together: a failing log insert rolls the generation back.
	if _, err := svc.Store.db.Writer().Exec(`CREATE TRIGGER fail_quality_log BEFORE INSERT ON settings_quality_review BEGIN SELECT RAISE(ABORT,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	if err := svc.RunSource(ctx, source, true); err == nil {
		t.Fatal("log insert failure ignored")
	}
	if r := lastRunOf(t, svc); r.Outcome != "error" || !strings.HasPrefix(r.Error, "the update could not be applied: ") || !strings.HasSuffix(r.Error, "; the last state is kept") {
		t.Fatalf("failed apply run: %+v", r)
	}
	head, err := svc.Store.Load(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if head.Generation != saved.Generation || svc.Coordinator.Current().Generation != saved.Generation {
		t.Fatal("partial apply published")
	}
	if _, err := svc.Store.db.Writer().Exec("DROP TRIGGER fail_quality_log"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RunSource(ctx, source, true); err != nil {
		t.Fatal(err)
	}
	run := lastRunOf(t, svc)
	applied := currentSnap(t, svc.Coordinator)
	if run.Outcome != "applied" || run.ReviewID == nil || applied.Generation != saved.Generation+1 || applied.Policy.Profiles[profileKey].Formats["Remux Tier 01"] != 12345 {
		t.Fatalf("run did not apply: %+v", run)
	}
	added := false
	for _, f := range applied.Records.Formats {
		if f.Key == "Remux Tier 99" && f.Config.TrashID == newID {
			added = true
		}
		if f.Key == "Remux Tier 01" && !strings.Contains(string(f.Specification), `"includeCustomFormatWhenRenaming":true`) {
			t.Fatalf("specification not updated: %s", f.Specification)
		}
	}
	if !added {
		t.Fatal("format named by its TRaSH id not added")
	}
	if applied.ProviderEpoch != saved.ProviderEpoch || !reflect.DeepEqual(applied.Records.Providers, saved.Records.Providers) || !reflect.DeepEqual(applied.Policy.Router, saved.Policy.Router) || !reflect.DeepEqual(applied.Policy.Library, saved.Policy.Library) || !reflect.DeepEqual(applied.Policy.Notify, saved.Policy.Notify) || !sameModes(applied.Policy.Versions, saved.Policy.Versions) {
		t.Fatal("quality sync touched source records or safety switches")
	}
	changes, err := svc.Changes(ctx, 50)
	if err != nil || len(changes) != 1 {
		t.Fatalf("update log: %+v %v", changes, err)
	}
	c := changes[0]
	if c.ID != *run.ReviewID || c.AppliedAt == 0 || c.State != "applied" || c.Source != "guides" || len(c.Added) != 1 || len(c.Changed) != 1 || len(c.Held) != 0 {
		t.Fatalf("log entry: %+v", c)
	}
	scored := false
	for _, s := range c.Scores {
		scored = scored || (s.Profile == profileKey && s.Format == "Remux Tier 01" && s.After == 12345)
	}
	if !scored || pendingReviews(t, svc) != 0 {
		t.Fatalf("log entry scores %+v", c.Scores)
	}
	found := false
	for _, a := range audit(t, svc.Store) {
		found = found || (a.Operation == "quality-import" && a.Actor == "sync:guides")
	}
	if !found {
		t.Fatal("sync actor absent from audit")
	}
	// The next run finds nothing new: unchanged, no generation, no log entry.
	if err := svc.RunSource(ctx, applied.Records.SyncSources[0], true); err != nil {
		t.Fatal(err)
	}
	if changes, _ := svc.Changes(ctx, 50); lastRunOf(t, svc).Outcome != "unchanged" || len(changes) != 1 || svc.Coordinator.Current().Generation != applied.Generation {
		t.Fatalf("second run: %+v", lastRunOf(t, svc))
	}
}

// A run applies to a profile a Live library uses, a raised score included, with no confirmation.
func TestQualitySyncAppliesToLiveLibrary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, base, live := liveGrabFixture(t)
	if v := live.Policy.Versions["movie-web"]; v.Mode != policy.ModeActive {
		t.Fatal("fixture: movie-web is not Live")
	}
	profile := live.Policy.Versions["movie-web"].Profile
	const web01 = "c20f169ef63c5f40c2def54abaf4438e"
	ours, ok := live.Policy.Profiles[profile].Formats["WEB Tier 01"]
	if !ok {
		t.Fatalf("fixture: %s does not score WEB Tier 01", profile)
	}
	svc := &QualitySync{Store: m.store, Coordinator: m, TrashRoot: syncTree(t), Impact: func(context.Context, *policy.Policy, *policy.Policy) (DecisionImpact, error) {
		return DecisionImpact{Examples: []ImpactExample{}}, nil
	}}
	editUpstream(t, svc.TrashRoot, web01, func(f map[string]any) { f["trash_scores"] = map[string]any{"default": ours + 500} })
	req := saveRequest(base, live)
	req.Records.SyncSources = []QualitySyncSource{{Record: Record{Key: "guides"}, Config: QualitySyncConfig{Kind: "trash", Flavour: "radarr", Profiles: map[string]string{profile: "default"}}}}
	saved, err := m.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RunSource(ctx, saved.Records.SyncSources[0], true); err != nil {
		t.Fatal(err)
	}
	head := currentSnap(t, m)
	if r := lastRunOf(t, svc); r.Outcome != "applied" || head.Policy.Profiles[profile].Formats["WEB Tier 01"] != ours+500 || head.Policy.Versions["movie-web"].Mode != policy.ModeActive {
		t.Fatalf("run on a Live library's profile: %+v", r)
	}
}

// Nothing is deleted: a format TRaSH no longer has stays as it is, and a run whose only finding is that retirement is
// unchanged, with no log entry.
func TestQualitySyncNeverDeletes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, base, initial := syncService(t)
	req := saveRequest(base, initial)
	req.Records.SyncSources = []QualitySyncSource{{Record: Record{Key: "guides"}, Config: QualitySyncConfig{Kind: "trash", Flavour: "radarr"}}}
	saved, err := svc.Coordinator.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(svc.TrashRoot, "radarr", syncFormatID+".json")); err != nil {
		t.Fatal(err)
	}
	if err := svc.RunSource(ctx, saved.Records.SyncSources[0], true); err != nil {
		t.Fatal(err)
	}
	head := currentSnap(t, svc.Coordinator)
	changes, _ := svc.Changes(ctx, 50)
	if r := lastRunOf(t, svc); r.Outcome != "unchanged" || head.Generation != saved.Generation || len(changes) != 0 {
		t.Fatalf("retirement alone: %+v %+v", r, changes)
	}
	kept := false
	for _, f := range head.Records.Formats {
		kept = kept || f.Key == "Remux Tier 01"
	}
	if !kept {
		t.Fatal("retired format deleted")
	}
}

func TestQualitySyncScheduleAndUnchanged(t *testing.T) {
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
	if source.Config.Schedule != "24h" {
		t.Fatal("sync schedule default changed")
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
	if len(runs) != 2 || runs[0].Outcome != "applied" || svc.Coordinator.Current().Generation != saved.Generation+1 {
		t.Fatalf("scheduled run did not apply: %+v", runs)
	}
	req = saveRequest(base, currentSnap(t, svc.Coordinator))
	req.Records.SyncSources[0].Config.Schedule = "48h"
	if _, err := svc.Coordinator.SaveAndApply(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := svc.RunSource(ctx, source, true); !errors.Is(err, ErrConflict) {
		t.Fatal("stale source config accepted", err)
	}
	found := false
	for _, a := range audit(t, svc.Store) {
		if a.Operation == "quality-import" && a.Actor == "sync:daily" {
			found = true
		}
	}
	if !found {
		t.Fatal("sync actor absent from audit")
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

// A draft preview (a source not saved yet) shows TRaSH's scores for a bound profile, a new zero score included; it
// stores and applies nothing.
func TestQualitySyncDraftPreviewShowsNewZeroScore(t *testing.T) {
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
	p, err := svc.Preview(ctx, QualitySyncSource{Record: Record{Key: "zero-score"}, Config: QualitySyncConfig{Kind: "trash", Flavour: "radarr", TrashIDs: []string{syncFormatID}, Profiles: map[string]string{"remux-2160p": "french-multi-vf"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Scores) != 1 || p.Scores[0].Before != 0 || p.Scores[0].After != 0 || !p.HasChanges() {
		t.Fatalf("new zero score missing from the preview: scores=%+v", p.Scores)
	}
	if svc.Coordinator.Current().Generation != saved.Generation || pendingReviews(t, svc) != 0 {
		t.Fatal("preview applied or stored the score")
	}
}

func currentSnap(t *testing.T, c *RuntimeCoordinator) *Snapshot {
	t.Helper()
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// A run that changes only formats keeps the stored preset origins of the profiles.
func TestQualitySyncKeepsProfileOrigins(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, base, initial := syncService(t)
	req := saveRequest(base, initial)
	origin := &ProfileOrigin{Preset: presetWeb2160, Name: "WEB 2160p", Flavour: "radarr"}
	req.Records.Profiles[0].Origin = origin
	key := req.Records.Profiles[0].Key
	req.Records.SyncSources = []QualitySyncSource{{Record: Record{Key: "guides"}, Config: QualitySyncConfig{Kind: "trash", Flavour: "radarr"}}}
	saved, err := svc.Coordinator.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	editUpstream(t, svc.TrashRoot, syncFormatID, func(f map[string]any) { f["includeCustomFormatWhenRenaming"] = true })
	if err := svc.RunSource(ctx, saved.Records.SyncSources[0], true); err != nil {
		t.Fatal(err)
	}
	if r := lastRunOf(t, svc); r.Outcome != "applied" {
		t.Fatalf("run %+v", r)
	}
	head, err := svc.Store.Load(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range head.Records.Profiles {
		if p.Key == key && !reflect.DeepEqual(p.Origin, origin) {
			t.Fatalf("origin of %s lost: %+v", key, p.Origin)
		}
	}
}

// A run's preview is applied only at the generation it was made at: a settings save in between is a conflict and
// nothing is applied or logged.
func TestQualitySyncStalePreviewNotApplied(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, base, initial := syncService(t)
	req := saveRequest(base, initial)
	req.Records.SyncSources = []QualitySyncSource{{Record: Record{Key: "guides"}, Config: QualitySyncConfig{Kind: "trash", Flavour: "radarr"}}}
	saved, err := svc.Coordinator.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	editUpstream(t, svc.TrashRoot, syncFormatID, func(f map[string]any) { f["includeCustomFormatWhenRenaming"] = true })
	p, err := svc.previewResolved(withBundledTrash(ctx), saved.Records.SyncSources[0])
	if err != nil || !p.HasChanges() {
		t.Fatalf("preview %+v %v", p, err)
	}
	moved, err := svc.Coordinator.SaveAndApply(ctx, saveRequest(base, saved))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.applyRun(ctx, p, "sync:guides"); !errors.Is(err, ErrConflict) {
		t.Fatal("stale preview applied", err)
	}
	if changes, _ := svc.Changes(ctx, 10); len(changes) != 0 || svc.Coordinator.Current().Generation != moved.Generation {
		t.Fatal("stale preview logged or applied")
	}
}
