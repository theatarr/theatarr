package settings

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

type dropPublishMarker struct {
	db interface {
		Exec(string, ...any) (sql.Result, error)
	}
}

func (p dropPublishMarker) AfterPublish() { _, _ = p.db.Exec("DROP TABLE settings_runtime_state") }

type cancelOnPublish struct{ cancel context.CancelFunc }

func (c cancelOnPublish) AfterPublish() { c.cancel() }

func TestPublishMarkerSurvivesRequestCancellationAndRepairs(t *testing.T) {
	t.Parallel()
	s, db, p := setup(t)
	initial := bootstrap(t, s, p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager, err := NewRuntimeCoordinator(s, p, initial, nil, func(context.Context, *Snapshot, *Runtime) (any, error) { return cancelOnPublish{cancel}, nil })
	if err != nil {
		t.Fatal(err)
	}
	saved, err := manager.SaveAndApply(ctx, saveRequest(p, initial))
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == nil {
		t.Fatal("publish did not cancel request")
	}
	gen, err := s.LastPublishedGeneration(context.Background())
	if err != nil || gen != saved.Generation {
		t.Fatalf("marker=%d err=%v", gen, err)
	}
	if _, err := db.Writer().Exec("UPDATE settings_runtime_state SET last_published_generation = ? WHERE id = 1", initial.Generation); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reload(ctx, saved.Generation); err != nil {
		t.Fatal(err)
	}
	gen, err = s.LastPublishedGeneration(context.Background())
	if err != nil || gen != saved.Generation {
		t.Fatalf("repaired marker=%d err=%v", gen, err)
	}
}

func TestPreparedRevisionEqualsCommittedRevision(t *testing.T) {
	t.Parallel()
	s, _, p := setup(t)
	initial := bootstrap(t, s, p)
	var prepared string
	manager, err := NewRuntimeCoordinator(s, p, initial, nil, func(_ context.Context, snap *Snapshot, _ *Runtime) (any, error) {
		prepared = snap.Revision
		return snap.Revision, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	req := saveRequest(p, initial)
	req.Records.Budgets[0].Config.Requests++
	saved, err := manager.SaveAndApply(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := s.Load(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if prepared != saved.Revision || stored.Revision != prepared || manager.Current().Components != stored.Revision || manager.Current().Revision != stored.Revision {
		t.Fatal("candidate, component, runtime and stored revisions differ")
	}
	generation, err := s.LoadGeneration(context.Background(), p, saved.Generation)
	if err != nil || generation.Revision != prepared {
		t.Fatalf("stored generation revision differs: %+v, %v", generation, err)
	}
}

func TestUnpublishedSavesRetainOnlyThreeSnapshots(t *testing.T) {
	t.Parallel()
	s, db, p := setup(t)
	ctx := context.Background()
	snap := bootstrap(t, s, p)
	if err := s.MarkPublished(ctx, snap.Generation); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Save(ctx, saveRequest(p, snap))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkPublished(ctx, snap.Generation); err != nil {
		t.Fatal(err)
	}
	for range 6 {
		snap, err = s.Save(ctx, saveRequest(p, snap))
		if err != nil {
			t.Fatal(err)
		}
		var count int
		if err := db.Reader().QueryRow("SELECT COUNT(*) FROM settings_generation WHERE snapshot != '{}'").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 3 {
			t.Fatalf("retained %d snapshots", count)
		}
	}
	// Startup fallback drops the unpublished head as well.
	if err := s.PruneSnapshots(ctx, 2); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.Reader().QueryRow("SELECT COUNT(*) FROM settings_generation WHERE snapshot != '{}'").Scan(&count); err != nil || count != 2 {
		t.Fatalf("fallback retained=%d err=%v", count, err)
	}
}

type epochCommitter struct {
	bump  bool
	epoch int64
}

func (e epochCommitter) PreparedProviderEpoch() int64 { return e.epoch }

func (e epochCommitter) CommitSettings(ctx context.Context, tx *sql.Tx) error {
	if !e.bump {
		return nil
	}
	return IncrementProviderEpoch(ctx, tx)
}

func TestProviderEpochOnlyChangesForProviderAccountEdits(t *testing.T) {
	t.Parallel()
	s, db, p := setup(t)
	initial := bootstrap(t, s, p)
	if err := s.MarkPublished(context.Background(), initial.Generation); err != nil {
		t.Fatal(err)
	}
	// The fake runtime computes the provider transition against the active account, as the serve runtime does.
	account := initial.secrets["providers/rd"].value
	manager, err := NewRuntimeCoordinator(s, p, initial, nil, func(_ context.Context, snap *Snapshot, _ *Runtime) (any, error) {
		bump := snap.secrets["providers/rd"].value != account
		account = snap.secrets["providers/rd"].value
		epoch := snap.ProviderEpoch
		if bump {
			epoch++
		}
		return epochCommitter{bump: bump, epoch: epoch}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	req := saveRequest(p, initial)
	req.SecretWrites = map[string]Secret{"providers/rd": NewSecret("rotated")}
	changed, err := manager.SaveAndApply(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	var epoch int64
	if err := db.Reader().QueryRow("SELECT provider_epoch FROM settings_runtime_state WHERE id = 1").Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if epoch != 1 || changed.ProviderEpoch != 1 || manager.Current().ProviderEpoch != 1 {
		t.Fatalf("account change epoch=%d snapshot=%d runtime=%d", epoch, changed.ProviderEpoch, manager.Current().ProviderEpoch)
	}
	req = saveRequest(p, changed)
	req.Records.Budgets[0].Config.Requests++
	budget, err := manager.SaveAndApply(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Reader().QueryRow("SELECT provider_epoch FROM settings_runtime_state WHERE id = 1").Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if epoch != 1 || budget.ProviderEpoch != 1 {
		t.Fatalf("budget save changed provider epoch: db=%d snapshot=%d", epoch, budget.ProviderEpoch)
	}
}

func TestStartupFallbackReadsCurrentProviderEpoch(t *testing.T) {
	t.Parallel()
	s, db, p := setup(t)
	initial := bootstrap(t, s, p)
	if err := s.MarkPublished(context.Background(), initial.Generation); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec("UPDATE settings_runtime_state SET provider_epoch = 7 WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	req := saveRequest(p, initial)
	req.Records.Budgets[0].Config.Requests++
	head, err := s.Save(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadGeneration(context.Background(), p, initial.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProviderEpoch != 7 || head.ProviderEpoch != 7 {
		t.Fatalf("fallback epoch=%d head epoch=%d", got.ProviderEpoch, head.ProviderEpoch)
	}
}

func TestSaveReturnsSnapshotWhenPublishMarkerFails(t *testing.T) {
	t.Parallel()
	s, db, p := setup(t)
	initial := bootstrap(t, s, p)
	manager, err := NewRuntimeCoordinator(s, p, initial, nil, func(context.Context, *Snapshot, *Runtime) (any, error) {
		return dropPublishMarker{db.Writer()}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := manager.SaveAndApply(context.Background(), saveRequest(p, initial))
	if err != nil || saved == nil || saved.Generation != initial.Generation+1 {
		t.Fatalf("marker failure returned snapshot=%v err=%v", saved, err)
	}
}

func TestSaveAndApplyUsesCoordinatorBase(t *testing.T) {
	t.Parallel()
	s, _, p := setup(t)
	initial := bootstrap(t, s, p)
	if p.Router != nil && p.Router.Enabled {
		t.Fatal("fixture router must start disabled")
	}
	manager, err := NewRuntimeCoordinator(s, p, initial, nil, func(_ context.Context, snap *Snapshot, _ *Runtime) (any, error) {
		return snap.Policy, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	forged, err := p.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if forged.Router == nil {
		forged.Router = &policy.Router{}
	}
	forged.Router.Enabled = true
	req := saveRequest(p, initial)
	req.Base = forged
	saved, err := manager.SaveAndApply(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]*policy.Policy{"saved": saved.Policy, "runtime": manager.Current().Policy,
		"components": manager.Current().Components.(*policy.Policy)} {
		if got.Router != nil && got.Router.Enabled {
			t.Fatalf("%s policy took router.enabled from the caller's base", name)
		}
	}
}

func TestSaveRefusesCommittedEpochOtherThanPrepared(t *testing.T) {
	t.Parallel()
	s, _, p := setup(t)
	initial := bootstrap(t, s, p)
	// The runtime prepared a router for an advanced epoch, but its commit hook does not advance it.
	manager, err := NewRuntimeCoordinator(s, p, initial, nil, func(_ context.Context, snap *Snapshot, _ *Runtime) (any, error) {
		return epochCommitter{epoch: snap.ProviderEpoch + 1}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SaveAndApply(context.Background(), saveRequest(p, initial)); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched epoch save err=%v", err)
	}
	stored, err := s.Load(context.Background(), p)
	if err != nil || stored.Generation != initial.Generation || manager.Current().Generation != initial.Generation {
		t.Fatalf("refused save committed or published: stored=%v err=%v", stored, err)
	}
}
