package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
)

// fallbackManager starts a coordinator on the published generation (a startup fallback) while head is stored, with
// the real serve runtime preparation.
func fallbackManager(t *testing.T, db *store.Store, s *settings.Store, base *policy.Policy, initial *settings.Snapshot,
	head func(settings.SaveRequest) settings.SaveRequest) (*settings.RuntimeCoordinator, *settings.Snapshot) {
	t.Helper()
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := s.MarkPublished(ctx, initial.Generation); err != nil {
		t.Fatal(err)
	}
	saved, err := s.Save(ctx, head(settings.SaveRequest{ExpectedGeneration: initial.Generation, Actor: "test", Base: base,
		Records: initial.Records}))
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := s.LoadGeneration(ctx, base, initial.Generation)
	if err != nil {
		t.Fatal(err)
	}
	active, err := prepareServeRuntime(ctx, fallback, nil, db, jobs.NewQueue(db), nil, log)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := settings.NewRuntimeCoordinator(s, base, fallback, active, func(ctx context.Context, snap *settings.Snapshot, old *settings.Runtime) (any, error) {
		return prepareServeRuntime(ctx, snap, old.Components.(*serveRuntime), db, jobs.NewQueue(db), nil, log)
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager, saved
}

// Reloading a stored head that disables a provider with active downloads is refused unless confirmed, as a save is;
// confirmed, it fails exactly those downloads and fences old adds with a new provider epoch.
func TestReloadAfterFallbackRefusesOrphanedDownloads(t *testing.T) {
	t.Parallel()
	db, s, base, initial := runtimeFixture(t)
	ctx := context.Background()
	manager, head := fallbackManager(t, db, s, base, initial, func(req settings.SaveRequest) settings.SaveRequest {
		for i := range req.Records.Providers {
			if req.Records.Providers[i].Key == "rd" {
				req.Records.Providers[i].Enabled = false
			}
		}
		return req
	})
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'Movie', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'movie-web', 0, 0)`,
	} {
		if _, err := db.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Writer().Exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title,
		indexer, provider, provider_torrent_id, state, created_at, updated_at)
		VALUES (1, 1, 1, 'movie-web', ?, 'Movie', 'pub', 'rd', 'rd-1', 'downloading', 0, 0)`, strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	err := manager.Reload(ctx, head.Generation)
	if err == nil || !strings.Contains(err.Error(), "orphan_active_downloads") {
		t.Fatalf("reload orphaning an active download: err=%v", err)
	}
	if manager.Current().Generation != initial.Generation ||
		eCount(db, "SELECT COUNT(*) FROM download WHERE state = 'downloading'") != 1 ||
		eCount(db, "SELECT provider_epoch FROM settings_runtime_state WHERE id = 1") != 0 {
		t.Fatal("a refused reload changed the runtime, a download or the epoch")
	}
	if err := manager.Reload(settings.WithOrphanActiveDownloads(ctx, true), head.Generation); err != nil {
		t.Fatal(err)
	}
	current := manager.Current()
	runtime := current.Components.(*serveRuntime)
	if current.Generation != head.Generation || current.ProviderEpoch != 1 || runtime.Router.ProviderEpoch() != 1 ||
		eCount(db, "SELECT provider_epoch FROM settings_runtime_state WHERE id = 1") != 1 {
		t.Fatalf("confirmed reload: generation=%d epoch=%d router=%d", current.Generation, current.ProviderEpoch,
			runtime.Router.ProviderEpoch())
	}
	if n := eCount(db, "SELECT COUNT(*) FROM download WHERE state = 'failed' AND error = 'provider removed by settings'"); n != 1 {
		t.Fatalf("confirmed reload failed %d downloads, want 1", n)
	}
}

// An account change through reload advances the provider epoch and clears that provider's registry rows.
func TestReloadAccountChangeAdvancesEpochAndClearsRegistry(t *testing.T) {
	t.Parallel()
	db, s, base, initial := runtimeFixture(t)
	ctx := context.Background()
	manager, head := fallbackManager(t, db, s, base, initial, func(req settings.SaveRequest) settings.SaveRequest {
		req.SecretWrites = map[string]settings.Secret{"providers/rd": settings.NewSecret("account-b")}
		return req
	})
	if _, err := db.Writer().Exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES
		(7, ?, 'present', 0, 0), (8, ?, 'present', 0, 0)`, strings.Repeat("c", 40), strings.Repeat("d", 40)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec(`INSERT INTO content_provider (content_id, provider, torrent_id, seen_at) VALUES
		(7, 'rd', 'rd-old', 0), (8, 'tb', 'tb-old', 0)`); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reload(ctx, head.Generation); err != nil {
		t.Fatal(err)
	}
	current := manager.Current()
	if current.Generation != head.Generation || current.ProviderEpoch != 1 ||
		current.Components.(*serveRuntime).Router.ProviderEpoch() != 1 ||
		eCount(db, "SELECT provider_epoch FROM settings_runtime_state WHERE id = 1") != 1 {
		t.Fatalf("account change through reload: generation=%d epoch=%d db=%d", current.Generation, current.ProviderEpoch,
			eCount(db, "SELECT provider_epoch FROM settings_runtime_state WHERE id = 1"))
	}
	if eCount(db, "SELECT COUNT(*) FROM content_provider WHERE provider = 'rd'") != 0 ||
		eCount(db, "SELECT COUNT(*) FROM content_provider WHERE provider = 'tb'") != 1 {
		t.Fatal("reload kept the old account's registry rows or removed another provider's")
	}
}

// stallingList is a provider whose listing completes only after an account change committed meanwhile.
type stallingList struct {
	provider.Provider
	during func()
	list   []provider.Torrent
}

func (p stallingList) Torrents(context.Context) ([]provider.Torrent, error) {
	if p.during != nil {
		p.during()
	}
	return p.list, nil
}

// A provider sync whose listing started under one provider epoch discards its result when the epoch moved before it
// writes the registry; the next sync of the current runtime writes normally.
func TestProviderSyncDiscardsListingFromOlderEpoch(t *testing.T) {
	t.Parallel()
	db, s, base, initial := runtimeFixture(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	old, err := prepareServeRuntime(ctx, initial, nil, db, jobs.NewQueue(db), nil, log)
	if err != nil {
		t.Fatal(err)
	}
	list := []provider.Torrent{{ID: "rd-account-a", Hash: strings.Repeat("e", 40), Name: "A", State: provider.StateDownloaded,
		Links: -1}}
	syncer := old.Providers.syncer
	syncer.Providers["rd"] = stallingList{Provider: syncer.Providers["rd"], list: list, during: func() {
		// Settings switch rd to account B while account A's listing is in flight.
		if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "DELETE FROM content_provider WHERE provider = 'rd'"); err != nil {
				return err
			}
			return settings.IncrementProviderEpoch(ctx, tx)
		}); err != nil {
			t.Error(err)
		}
	}}
	if _, err := syncer.Run(ctx, provider.SyncOptions{Providers: []string{"rd"}}); err != nil {
		t.Fatal(err)
	}
	if n := eCount(db, "SELECT COUNT(*) FROM content_provider WHERE provider = 'rd'"); n != 0 {
		t.Fatalf("stale listing wrote %d registry rows for the new account", n)
	}
	if n := eCount(db, "SELECT COUNT(*) FROM provider_sync_run WHERE provider = 'rd'"); n != 0 {
		t.Fatalf("stale listing recorded %d sync runs", n)
	}
	current, err := s.Load(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	next, err := prepareServeRuntime(ctx, current, nil, db, jobs.NewQueue(db), nil, log)
	if err != nil {
		t.Fatal(err)
	}
	list[0].ID = "rd-account-b"
	next.Providers.syncer.Providers["rd"] = stallingList{Provider: next.Providers.syncer.Providers["rd"], list: list}
	if _, err := next.Providers.syncer.Run(ctx, provider.SyncOptions{Providers: []string{"rd"}}); err != nil {
		t.Fatal(err)
	}
	if n := eCount(db, "SELECT COUNT(*) FROM content_provider WHERE provider = 'rd' AND torrent_id = 'rd-account-b'"); n != 1 {
		t.Fatalf("current-epoch sync wrote %d rows, want 1", n)
	}
}

// While a startup fallback runs, the settings health check is an error naming both generations (Admin ›
// Overview lists it under Needs attention); once the head is loaded again it is OK.
func TestSettingsFallbackHealth(t *testing.T) {
	t.Parallel()
	db, s, base, initial := runtimeFixture(t)
	ctx := context.Background()
	manager, head := fallbackManager(t, db, s, base, initial, func(req settings.SaveRequest) settings.SaveRequest { return req })
	check := settingsFallbackHealth(manager)
	r := check(ctx)
	want := fmt.Sprintf("Running settings generation %d, not the stored %d", initial.Generation, head.Generation)
	if r.Type != health.Error || !strings.Contains(r.Message, want) {
		t.Fatalf("during fallback: %+v", r)
	}
	if err := manager.Reload(ctx, head.Generation); err != nil {
		t.Fatal(err)
	}
	if r := check(ctx); r.Type != health.OK {
		t.Fatalf("after reload: %+v", r)
	}
}
