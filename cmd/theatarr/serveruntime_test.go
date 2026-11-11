package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func TestPreparedDecisionEngineKeepsSharedLibrary(t *testing.T) {
	t.Parallel()
	library := decide.NewLibrary(map[int64]decide.Title{1: {ID: 1, Name: "Example"}})
	previous, next := &decide.Engine{}, &decide.Engine{}
	previous.SetLibrary(library)

	inheritDecisionLibrary(next, previous)
	if next.Library() != library {
		t.Fatal("replacement decision engine lost the shared title library")
	}
}

func TestServeEgressGuardOffWarning(t *testing.T) {
	t.Parallel()
	pol := &policy.Policy{}
	var out bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&out, nil))
	warnVPNGuardOff(pol, log)
	warnVPNGuardOff(&policy.Policy{Egress: &policy.Egress{}}, log)
	if strings.Count(out.String(), "VPN guard off") != 1 || !strings.Contains(out.String(), `"level":"WARN"`) {
		t.Fatalf("startup log: %s", out.String())
	}
}

func TestServeRuntimeChecksEgressAfterPublish(t *testing.T) {
	checked := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"ip":"127.0.0.1"}`))
		checked <- struct{}{}
	}))
	defer server.Close()
	pol := &policy.Policy{Egress: &policy.Egress{CheckURL: server.URL, VPNIPs: []string{"127.0.0.1"},
		Interval: policy.Duration(time.Hour)}}
	gw, err := gateway.New(context.Background(), pol, gateway.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	checks := newServeEgressChecks(context.Background())
	defer checks.Close()
	(&serveRuntime{Gateway: gw, EgressChecks: checks}).AfterPublish()
	select {
	case <-checked:
	case <-time.After(time.Second):
		t.Fatal("published gateway did not start an immediate egress check")
	}
}

func TestServeHTTPServicesStopsOldWebLoopBeforeClose(t *testing.T) {
	oldReturned := make(chan struct{})
	newStarted := make(chan struct{})
	newReturned := make(chan struct{})
	var runs int
	services := &serveHTTPServices{ctx: context.Background()}
	services.run = func(ctx context.Context) {
		runs++
		if runs == 1 {
			<-ctx.Done()
			close(oldReturned)
			return
		}
		close(newStarted)
		<-ctx.Done()
		close(newReturned)
	}
	services.Start(nil)
	services.Start(nil)
	select {
	case <-oldReturned:
	case <-time.After(time.Second):
		t.Fatal("old web loop did not return before new generation started")
	}
	select {
	case <-newStarted:
	case <-time.After(time.Second):
		t.Fatal("new web loop did not start")
	}
	services.Close()
	select {
	case <-newReturned:
	case <-time.After(time.Second):
		t.Fatal("web loop did not return after Close")
	}
}

func TestStartupFallsBackToLastPublishedSettings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	base, err := policy.Load(filepath.Join("..", "..", "examples", "policy"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := settings.New(db)
	if err != nil {
		t.Fatal(err)
	}
	s.SetTrashRoot("../../internal/settings/testdata/trash")
	initial, err := s.Bootstrap(ctx, base, settings.ImportRequest{Dir: base.Dir, Actor: "test",
		Lookup: func(name string) (string, bool) { return "fixture-" + name, true }})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkPublished(ctx, initial.Generation); err != nil {
		t.Fatal(err)
	}
	req := settings.SaveRequest{ExpectedGeneration: initial.Generation, Actor: "test", Base: base, Records: initial.Records}
	req.Records.Budgets[0].Config.Requests++
	unpublished, err := s.Save(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	got, component, fallback, err := prepareStartupRuntime(ctx, s, base, unpublished, log,
		func(_ context.Context, snap *settings.Snapshot) (any, error) {
			if snap.Generation == unpublished.Generation {
				return nil, errors.New("arr unavailable")
			}
			return snap.Generation, nil
		})
	if err != nil || !fallback || got.Generation != initial.Generation || component != initial.Generation {
		t.Fatalf("startup fallback snapshot=%v component=%v fallback=%t err=%v", got, component, fallback, err)
	}
	if !strings.Contains(logs.String(), "SETTINGS STARTUP FALLBACK") {
		t.Fatal("startup fallback did not log a loud warning")
	}
	var snapshots int
	if err := db.Reader().QueryRow("SELECT COUNT(*) FROM settings_generation WHERE snapshot != '{}'").Scan(&snapshots); err != nil || snapshots != 1 {
		t.Fatalf("fallback retained %d snapshots: %v", snapshots, err)
	}
}

func TestProviderAccountChangeDeletesOnlyItsContentRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec(`INSERT INTO content (id, info_hash, state, first_seen_at, last_seen_at) VALUES
		(7, ?, 'present', 0, 0), (8, ?, 'present', 0, 0)`, strings.Repeat("c", 40), strings.Repeat("d", 40)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec(`INSERT INTO content_provider (content_id, provider, torrent_id, seen_at) VALUES
		(7, 'rd', 'rd-old', 0), (8, 'tb', 'tb-old', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec(`INSERT INTO arr_instance (name, flavour, policy_key, created_at, updated_at) VALUES ('radarr', 'radarr', 'movie-web', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec(`INSERT INTO arr_history (instance, arr_id, event_type, date_at, info_hash, protocol) VALUES ('radarr', 1, 'grabbed', 0, ?, 'torrent')`, strings.Repeat("c", 40)); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error {
		return (&serveRuntime{Continuity: &providerContinuity{changed: []string{"rd"}}}).CommitSettings(ctx, tx)
	}); err != nil {
		t.Fatal(err)
	}
	if n := eCount(db, "SELECT COUNT(*) FROM content_provider WHERE provider = 'rd'"); n != 0 ||
		eCount(db, "SELECT COUNT(*) FROM content_provider WHERE provider = 'tb'") != 1 ||
		eCount(db, "SELECT COUNT(*) FROM content") != 2 ||
		eCount(db, "SELECT COUNT(*) FROM arr_history") != 1 {
		t.Fatalf("provider change removed unrelated rows: rd=%d tb=%d content=%d history=%d", n,
			eCount(db, "SELECT COUNT(*) FROM content_provider WHERE provider = 'tb'"), eCount(db, "SELECT COUNT(*) FROM content"),
			eCount(db, "SELECT COUNT(*) FROM arr_history"))
	}
	if n := eCount(db, "SELECT provider_epoch FROM settings_runtime_state WHERE id = 1"); n != 1 {
		t.Fatalf("provider account change epoch=%d, want 1", n)
	}
}

func importWithoutRD(t *testing.T, db *store.Store, s *settings.Store, base *policy.Policy, initial *settings.Snapshot) (*settings.RuntimeCoordinator, string) {
	t.Helper()
	runtimeBase, err := base.Clone()
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range runtimeBase.Versions {
		providers := version.Providers[:0]
		for _, key := range version.Providers {
			if key != "rd" {
				providers = append(providers, key)
			}
		}
		version.Providers = providers
	}
	dir := filepath.Join(t.TempDir(), "policy")
	if err := copyPolicyDir(base.Dir, dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "providers.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	start, end := strings.Index(text, "  rd:\n"), strings.Index(text, "  ad:\n")
	if start < 0 || end <= start {
		t.Fatal("provider fixture has no rd section")
	}
	if err := os.WriteFile(path, []byte(text[:start]+text[end:]), 0o600); err != nil {
		t.Fatal(err)
	}
	versionsPath := filepath.Join(dir, "versions.yaml")
	versions, err := os.ReadFile(versionsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(versionsPath, []byte(strings.ReplaceAll(string(versions), "[rd, ad, tb]", "[ad, tb]")), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := settings.NewRuntimeCoordinator(s, runtimeBase, initial, &serveRuntime{Policy: initial.Policy},
		func(_ context.Context, snap *settings.Snapshot, previous *settings.Runtime) (any, error) {
			continuity := &providerContinuity{}
			if previous != nil && previous.Policy != nil && snap.Policy.Providers["rd"] == nil {
				continuity.removed = []string{"rd"}
			}
			return &serveRuntime{Policy: snap.Policy, Continuity: continuity, ProviderEpoch: preparedProviderEpoch(snap, continuity)}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	return manager, dir
}

func TestImportAndApplyRefusesActiveProviderRemoval(t *testing.T) {
	t.Parallel()
	db, s, base, initial := runtimeFixture(t)
	ctx := context.Background()
	for _, query := range []string{
		`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'Movie', 0, 0)`,
		`INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'movie-web', 0, 0)`,
		`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider, state, created_at, updated_at)
			VALUES (1, 1, 1, 'movie-web', ?, 'Movie', 'pub', 'rd', 'added', 0, 0)`,
	} {
		var err error
		if strings.Contains(query, "download") {
			_, err = db.Writer().ExecContext(ctx, query, strings.Repeat("a", 40))
		} else {
			_, err = db.Writer().ExecContext(ctx, query)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	manager, dir := importWithoutRD(t, db, s, base, initial)
	_, err := confirmedRuntimeImport(t, manager, settings.ImportRequest{Dir: dir, ExpectedGeneration: initial.Generation, Actor: "test", Lookup: func(name string) (string, bool) { return "fixture-" + name, true }})
	if err == nil || !strings.Contains(err.Error(), "orphan_active_downloads") {
		t.Fatalf("active removal error=%v", err)
	}
	if manager.Current().Generation != initial.Generation || eCount(db, "SELECT COUNT(*) FROM download WHERE state = 'added'") != 1 {
		t.Fatal("refused import changed the active runtime or download")
	}
}

func TestImportAndApplyPublishesNewGeneration(t *testing.T) {
	t.Parallel()
	db, s, base, initial := runtimeFixture(t)
	manager, dir := importWithoutRD(t, db, s, base, initial)
	saved, err := confirmedRuntimeImport(t, manager, settings.ImportRequest{Dir: dir, ExpectedGeneration: initial.Generation, Actor: "test", Lookup: func(name string) (string, bool) { return "fixture-" + name, true }})
	if err != nil || saved.Generation != initial.Generation+1 || manager.Current().Generation != saved.Generation {
		t.Fatalf("import publication snapshot=%v current=%v err=%v", saved, manager.Current(), err)
	}
	if manager.Current().Components.(*serveRuntime).Policy.Providers["rd"] != nil {
		t.Fatal("published runtime retained removed provider")
	}
}

func TestBudgetSaveAfterProviderDisableDoesNotAdvanceProviderEpoch(t *testing.T) {
	t.Parallel()
	db, s, base, initial := runtimeFixture(t)
	ctx := context.Background()
	providerKeys := func(records []settings.Provider) map[string]string {
		keys := map[string]string{}
		for _, record := range records {
			if record.Enabled {
				keys[record.Key] = "fixture-" + record.Key
			}
		}
		return keys
	}
	previous := &serveRuntime{Policy: initial.Policy, ProviderKeys: providerKeys(initial.Records.Providers)}
	manager, err := settings.NewRuntimeCoordinator(s, base, initial, previous,
		func(_ context.Context, snap *settings.Snapshot, prior *settings.Runtime) (any, error) {
			var old *serveRuntime
			if prior != nil {
				old, _ = prior.Components.(*serveRuntime)
			}
			keys := providerKeys(snap.Records.Providers)
			continuity, err := validateProviderContinuity(ctx, db, snap, old, keys, nil)
			if err != nil {
				return nil, err
			}
			return &serveRuntime{Policy: snap.Policy, ProviderKeys: keys, Continuity: continuity,
				ProviderEpoch: preparedProviderEpoch(snap, continuity)}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	disable := settings.SaveRequest{ExpectedGeneration: initial.Generation, Actor: "test", Base: base, Records: initial.Records}
	for i := range disable.Records.Providers {
		if disable.Records.Providers[i].Key == "rd" {
			disable.Records.Providers[i].Enabled = false
		}
	}
	saved, err := manager.SaveAndApply(ctx, disable)
	if err != nil || saved.ProviderEpoch != 1 {
		t.Fatalf("disable save epoch=%v err=%v", saved, err)
	}
	budget := settings.SaveRequest{ExpectedGeneration: saved.Generation, Actor: "test", Base: base, Records: saved.Records}
	budget.Records.Budgets[0].Config.Requests++
	saved, err = manager.SaveAndApply(ctx, budget)
	if err != nil || saved.ProviderEpoch != 1 || manager.Current().ProviderEpoch != 1 ||
		eCount(db, "SELECT provider_epoch FROM settings_runtime_state WHERE id = 1") != 1 {
		t.Fatalf("budget save advanced disabled provider epoch: snapshot=%v current=%v err=%v", saved, manager.Current(), err)
	}
}

func TestInitializedDatabaseOverlaysUnvalidatedYAML(t *testing.T) {
	t.Setenv("THEATARR_TRASH", "../../internal/settings/testdata/trash")
	ctx := context.Background()
	db, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join("..", "..", "examples", "policy")
	base, err := policy.Load(source)
	if err != nil {
		t.Fatal(err)
	}
	s, err := settings.New(db)
	if err != nil {
		t.Fatal(err)
	}
	s.SetTrashRoot("../../internal/settings/testdata/trash")
	if _, err := s.Bootstrap(ctx, base, settings.ImportRequest{Dir: source, Actor: "test",
		Lookup: func(name string) (string, bool) { return "fixture-" + name, true }}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "policy")
	if err := copyPolicyDir(source, dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "indexers.yaml")); err != nil {
		t.Fatal(err)
	}
	stale, err := policy.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := stale.Validate(); err == nil {
		t.Fatal("fixture without its DB-owned indexers unexpectedly validates by itself")
	}
	_, snap, active, err := loadServePolicy(ctx, "db", dir, stale, db)
	if err != nil {
		t.Fatal(err)
	}
	if snap == nil || len(active.Indexers) == 0 || active.Indexers["prowlarr-public"] == nil {
		t.Fatal("DB-only indexer was not overlaid before whole-policy validation")
	}
}

func copyPolicyDir(source, dest string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
}

type continuityProvider struct {
	provider.Provider
	torrents map[string]*provider.Torrent
}

func (p continuityProvider) Torrent(_ context.Context, id string) (*provider.Torrent, error) {
	t := p.torrents[id]
	if t == nil {
		return nil, provider.ErrNotFound
	}
	return t, nil
}

func TestProviderContinuityProtectsActiveDownloads(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	base, err := policy.Load(filepath.Join("..", "..", "examples", "policy"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := settings.New(db)
	if err != nil {
		t.Fatal(err)
	}
	s.SetTrashRoot("../../internal/settings/testdata/trash")
	initial, err := s.Bootstrap(ctx, base, settings.ImportRequest{Dir: base.Dir, Actor: "test",
		Lookup: func(name string) (string, bool) { return "fixture-" + name, true }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().ExecContext(ctx, `INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'movie', 'Movie', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().ExecContext(ctx, `INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'movie-web', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().ExecContext(ctx, `INSERT INTO download (id, title_id, version_id, version_key, info_hash,
		release_title, indexer, provider, provider_torrent_id, state, created_at, updated_at)
		VALUES (1, 1, 1, 'movie-web', ?, 'Movie', 'dmm', 'rd', 'rd-1', 'downloading', 0, 0)`, strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().ExecContext(ctx, `INSERT INTO download (id, title_id, version_id, version_key, info_hash,
		release_title, indexer, provider, state, created_at, updated_at)
		VALUES (2, 1, 1, 'movie-web', ?, 'Movie', 'dmm', 'rd', 'ready', 0, 0)`, strings.Repeat("b", 40)); err != nil {
		t.Fatal(err)
	}
	previous := &serveRuntime{Policy: initial.Policy, ProviderKeys: map[string]string{}}
	if err := initial.WithSecret("providers", "rd", nil, func(key string) error {
		previous.ProviderKeys["rd"] = key
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rotate := settings.SaveRequest{ExpectedGeneration: initial.Generation, Actor: "test", Base: base,
		Records: initial.Records, SecretWrites: map[string]settings.Secret{"providers/rd": settings.NewSecret("rotated-account")}}
	candidate, err := s.Candidate(ctx, rotate)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]string{"rd": "rotated-account"}
	visible := continuityProvider{torrents: map[string]*provider.Torrent{"rd-1": {ID: "rd-1", Hash: strings.Repeat("A", 40)}}}
	clients := map[string]provider.Provider{"rd": visible}
	if _, err := validateProviderContinuity(ctx, db, candidate, previous, keys, clients); err == nil || !strings.Contains(err.Error(), "could not verify 1 active download(s) on the new account; retry") {
		t.Fatalf("missing torrent ID: %v", err)
	}
	if _, err := db.Writer().Exec("DELETE FROM download WHERE id = 2"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		client provider.Provider
		want   string
	}{
		{"not found", continuityProvider{}, "cannot see"},
		{"hash mismatch", continuityProvider{torrents: map[string]*provider.Torrent{"rd-1": {Hash: strings.Repeat("b", 40)}}}, "cannot see"},
		{"empty hash", continuityProvider{torrents: map[string]*provider.Torrent{"rd-1": {Hash: ""}}}, "could not verify 1"},
		{"transient", continuityProbe{probe: func(context.Context, string) (*provider.Torrent, error) { return nil, errors.New("temporary") }}, "could not verify 1"},
		{"timeout", continuityProbe{probe: func(context.Context, string) (*provider.Torrent, error) { return nil, context.DeadlineExceeded }}, "could not verify 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateProviderContinuity(ctx, db, candidate, previous, keys, map[string]provider.Provider{"rd": tc.client})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("probe error: %v", err)
			}
			if tc.name == "empty hash" && strings.Contains(err.Error(), "cannot see") {
				t.Fatalf("empty probe hash was treated as not found: %v", err)
			}
		})
	}
	// With orphan_active_downloads the owner already accepted failing them: a download the new account could not be
	// checked for, and more than the probe limit, no longer refuse the save; they are failed as the unseen ones are.
	orphanCtx := settings.WithOrphanActiveDownloads(ctx, true)
	rollback := errors.New("rollback")
	failedOnCommit := func(c *providerContinuity) int {
		t.Helper()
		var n int
		err := db.WriteTx(ctx, func(tx *sql.Tx) error {
			if err := (&serveRuntime{Continuity: c}).CommitSettings(ctx, tx); err != nil {
				return err
			}
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM download WHERE provider = 'rd' AND state = 'failed'`).Scan(&n); err != nil {
				return err
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatalf("commit: %v", err)
		}
		return n
	}
	transient := continuityProbe{probe: func(context.Context, string) (*provider.Torrent, error) { return nil, errors.New("HTTP 403") }}
	orphaned, err := validateProviderContinuity(orphanCtx, db, candidate, previous, keys, map[string]provider.Provider{"rd": transient})
	if err != nil || len(orphaned.missing["rd"]) != 1 {
		t.Fatalf("uncertain with orphan: %+v %v", orphaned, err)
	}
	if n := failedOnCommit(orphaned); n != 1 {
		t.Fatalf("uncertain with orphan: %d failed on commit", n)
	}
	for i := 3; i <= 102; i++ {
		if _, err := db.Writer().Exec(`INSERT INTO download (id,title_id,version_id,version_key,info_hash,release_title,indexer,provider,provider_torrent_id,state,created_at,updated_at)
			VALUES (?,1,1,'movie-web',?,'Movie','dmm','rd',?,'downloading',0,0)`, i, fmt.Sprintf("%040x", i), fmt.Sprintf("rd-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := validateProviderContinuity(ctx, db, candidate, previous, keys, clients); err == nil || !strings.Contains(err.Error(), "probe limit") {
		t.Fatalf("over the probe limit without orphan: %v", err)
	}
	over, err := validateProviderContinuity(orphanCtx, db, candidate, previous, keys, clients)
	if err != nil || len(over.probed["rd"]) != 1 || len(over.missing["rd"]) != 100 {
		t.Fatalf("over the probe limit with orphan: probed %d missing %d %v", len(over.probed["rd"]), len(over.missing["rd"]), err)
	}
	if n := failedOnCommit(over); n != 100 {
		t.Fatalf("over the probe limit with orphan: %d failed on commit", n)
	}
	if _, err := db.Writer().Exec("DELETE FROM download WHERE id > 2"); err != nil {
		t.Fatal(err)
	}
	continuity, err := validateProviderContinuity(ctx, db, candidate, previous, keys, clients)
	if err != nil || len(continuity.probed["rd"]) != 1 {
		t.Fatalf("visible torrent: %+v %v", continuity, err)
	}
	if _, err := db.Writer().Exec("UPDATE download SET provider_torrent_id = 'changed' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error { return (&serveRuntime{Continuity: continuity}).CommitSettings(ctx, tx) }); !errors.Is(err, settings.ErrDownloadsChanged) {
		t.Fatalf("changed downloads: %v", err)
	}
	if _, err := db.Writer().Exec("UPDATE download SET provider_torrent_id = 'rd-1' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec(`INSERT INTO download (id,title_id,version_id,version_key,info_hash,release_title,indexer,provider,state,created_at,updated_at)
		VALUES (2,1,1,'movie-web',?,'Movie','dmm','rd','adding',0,0)`, strings.Repeat("b", 40)); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteTx(ctx, func(tx *sql.Tx) error { return (&serveRuntime{Continuity: continuity}).CommitSettings(ctx, tx) }); !errors.Is(err, settings.ErrDownloadsChanged) {
		t.Fatalf("new active download: %v", err)
	}
	if _, err := db.Writer().Exec("DELETE FROM download WHERE id = 2"); err != nil {
		t.Fatal(err)
	}
	pacing := settings.SaveRequest{ExpectedGeneration: initial.Generation, Actor: "test", Base: base, Records: initial.Records}
	pacing.Records.Providers = append([]settings.Provider(nil), initial.Records.Providers...)
	for i := range pacing.Records.Providers {
		pacing.Records.Providers[i].Config.Pacing.AddsPerMinute++
	}
	paced, err := s.Candidate(ctx, pacing)
	if err != nil {
		t.Fatal(err)
	}
	c, err := validateProviderContinuity(ctx, db, paced, previous, previous.ProviderKeys, nil)
	if err != nil || len(c.changed) != 0 {
		t.Fatalf("pacing-only edit: %+v %v", c, err)
	}
	remove := settings.SaveRequest{ExpectedGeneration: initial.Generation, Actor: "test", Base: base, Records: initial.Records}
	for i := range remove.Records.Providers {
		if remove.Records.Providers[i].Key == "rd" {
			remove.Records.Providers[i].Enabled = false
		}
	}
	disabled, err := s.Candidate(ctx, remove)
	if err != nil {
		t.Fatal(err)
	}
	removal, err := validateProviderContinuity(ctx, db, disabled, previous, previous.ProviderKeys, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Commit counts rows added after prepare, in all active states.
	for i, state := range []string{"adding", "added", "ready"} {
		_, err := db.Writer().Exec(`INSERT INTO download (id,title_id,version_id,version_key,info_hash,release_title,indexer,provider,state,created_at,updated_at)
    VALUES (?,1,1,'movie-web',?,'Movie','dmm','rd',?,0,0)`, i+2, strings.Repeat(string(rune('b'+i)), 40), state)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		id         int
		key, state string
	}{{5, "rd", "imported"}, {6, "tb", "added"}} {
		if _, err := db.Writer().Exec(`INSERT INTO download (id,title_id,version_id,version_key,info_hash,release_title,indexer,provider,state,created_at,updated_at)
			VALUES (?,1,1,'movie-web',?,'Movie','dmm',?,?,0,0)`, row.id, strings.Repeat("e", 40), row.key, row.state); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Writer().Exec(`INSERT INTO download (id,title_id,version_id,version_key,info_hash,release_title,indexer,provider,state,created_at,updated_at)
		VALUES (7,1,1,'movie-web',?,'Movie','dmm','rd','downloading',0,0)`, strings.Repeat("f", 40)); err != nil {
		t.Fatal(err)
	}
	manager, err := settings.NewRuntimeCoordinator(s, base, initial, previous, func(_ context.Context, snap *settings.Snapshot, _ *settings.Runtime) (any, error) {
		return &serveRuntime{Continuity: removal, ProviderEpoch: preparedProviderEpoch(snap, removal)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SaveAndApply(ctx, remove); err == nil || !strings.Contains(err.Error(), "add(s) in progress") {
		t.Fatalf("unconfirmed removal: %v", err)
	}
	stored, err := s.Load(ctx, base)
	if err != nil || stored.Generation != initial.Generation {
		t.Fatalf("rejected save committed: %v", err)
	}
	removal.orphan = true
	if _, err := manager.SaveAndApply(ctx, remove); err == nil || !strings.Contains(err.Error(), "add(s) in progress") {
		t.Fatalf("orphan confirmation must not override in-progress add: %v", err)
	}
	if _, err := db.Writer().Exec("DELETE FROM download WHERE id = 2"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SaveAndApply(ctx, remove); err != nil {
		t.Fatalf("confirmed removal: %v", err)
	}
	var failed int
	if err := db.Reader().QueryRow(`SELECT COUNT(*) FROM download WHERE provider = 'rd' AND state = 'failed'
		AND error = 'provider removed by settings'`).Scan(&failed); err != nil || failed != 4 {
		t.Fatalf("removed provider failures=%d err=%v, want 4", failed, err)
	}
	if n := eCount(db, `SELECT COUNT(*) FROM router_blocklist WHERE source = 'provider'`); n != 0 {
		t.Fatalf("provider removal blocklisted %d rows", n)
	}
	var state string
	if err := db.Reader().QueryRow("SELECT state FROM download WHERE id = 6").Scan(&state); err != nil || state != "added" {
		t.Fatalf("unrelated provider row state=%q err=%v", state, err)
	}
	// An imported row of the removed provider and the other provider's active row stay exactly as they were.
	if n := eCount(db, `SELECT COUNT(*) FROM download WHERE (id = 5 AND provider = 'rd' AND state = 'imported' OR id = 6 AND state = 'added')
		AND error IS NULL AND updated_at = 0`); n != 2 {
		t.Fatalf("confirmed removal touched imported rd row or other provider row: untouched=%d, want 2", n)
	}
}

func eCount(db *store.Store, query string, args ...any) int {
	var n int
	if err := db.Reader().QueryRow(query, args...).Scan(&n); err != nil {
		return -1
	}
	return n
}

func TestLoadServePolicyReadsStoredGeneration(t *testing.T) {
	t.Setenv("THEATARR_TRASH", "../../internal/settings/testdata/trash")
	ctx := context.Background()
	db, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	base, err := policy.Load(filepath.Join("..", "..", "examples", "policy"))
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(name string) (string, bool) { return "fixture-" + name, true }
	settingsStore, err := settings.New(db)
	if err != nil {
		t.Fatal(err)
	}
	settingsStore.SetTrashRoot("../../internal/settings/testdata/trash")
	_, err = settingsStore.Bootstrap(ctx, base, settings.ImportRequest{Dir: base.Dir, Actor: "test", Lookup: lookup})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := settingsStore.BootstrapQuality(ctx, base, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := settingsStore.BootstrapSwitches(ctx, base, "test", nil); err != nil {
		t.Fatal(err)
	}
	initial, err := settingsStore.BootstrapSections(ctx, base, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	loadedInitial, err := settingsStore.Load(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loadedInitial.Policy.Indexers, base.Indexers) ||
		!reflect.DeepEqual(loadedInitial.Policy.Providers, base.Providers) ||
		!reflect.DeepEqual(loadedInitial.Policy.Budgets, base.Budgets) {
		t.Fatal("bootstrap then load changed the YAML-owned source runtime sections")
	}
	req := settings.SaveRequest{ExpectedGeneration: initial.Generation, Actor: "test", Base: base, Records: initial.Records}
	req.Records.Budgets[0].Config.Requests++
	changed, err := settingsStore.Save(ctx, req)
	if err != nil {
		t.Fatal(err)
	}

	loadedStore, loaded, fromDB, err := loadServePolicy(ctx, "db", base.Dir, base, db)
	if err != nil {
		t.Fatal(err)
	}
	// The upgrade imports the quality records, the global switches and the other sections once, preserving the
	// existing source generation's records.
	if loadedStore == nil || loaded == nil || loaded.Generation != changed.Generation ||
		!reflect.DeepEqual(fromDB.Budgets, changed.Policy.Budgets) {
		t.Fatal("db source did not load the current settings generation")
	}
	// A restart only loads: no new generation, and the seed kept the YAML switches (all off in the example).
	_, restarted, _, err := loadServePolicy(ctx, "db", base.Dir, base, db)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Generation != loaded.Generation || restarted.Records.Switches == nil ||
		*restarted.Records.Switches != (settings.GlobalSwitches{}) || restarted.Policy.Router.Enabled || restarted.Policy.Library.Enabled {
		t.Fatalf("restart changed the settings: generation %d, switches %+v", restarted.Generation, restarted.Records.Switches)
	}
	loaded.Records.Indexers[0].Enabled = false
	loaded.Records.Indexers[0].Priority = 7
	loaded.Records.Providers[0].Enabled = false
	runtimePolicy, priorities, err := runtimeServicePolicy(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := runtimePolicy.Indexers[loaded.Records.Indexers[0].Key]; ok ||
		priorities[loaded.Records.Indexers[0].Key] != 0 {
		t.Fatal("disabled indexer reached runtime services")
	}
	if _, ok := runtimePolicy.Providers[loaded.Records.Providers[0].Key]; ok {
		t.Fatal("disabled provider reached runtime services")
	}
}

// An egress section added after startup gets the periodic check, a changed interval restarts it, and removing the
// section stops it.
func TestServeEgressWorkerFollowsTheSection(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"ip":"127.0.0.1"}`))
	}))
	defer server.Close()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	runtimeFor := func(gen int64, eg *policy.Egress) *serveRuntime {
		pol := &policy.Policy{Egress: eg}
		gw, err := gateway.New(context.Background(), pol, gateway.Options{Logger: quiet})
		if err != nil {
			t.Fatal(err)
		}
		return &serveRuntime{Generation: gen, Policy: pol, Gateway: gw}
	}
	var current atomic.Pointer[serveRuntime]
	workers := newServeWorkers(context.Background(), quiet, egressWorker(func(ctx context.Context) (context.Context, *serveRuntime) {
		return ctx, current.Load()
	}))
	defer workers.Close()
	apply := func(rt *serveRuntime) { current.Store(rt); workers.Apply(rt) }

	off := runtimeFor(1, nil)
	current.Store(off)
	workers.Start(off)
	time.Sleep(50 * time.Millisecond)
	if hits.Load() != 0 || workerStatus(workers, "egress").Running {
		t.Fatal("egress worker ran without an egress section")
	}
	section := func(interval time.Duration) *policy.Egress {
		return &policy.Egress{CheckURL: server.URL, VPNIPs: []string{"127.0.0.1"}, Interval: policy.Duration(interval)}
	}
	apply(runtimeFor(2, section(20*time.Millisecond)))
	waitFor(t, "periodic checks after the section was added", func() bool { return hits.Load() >= 3 })
	if gw := current.Load().Gateway; !gw.EgressStates()[0].OK {
		t.Fatalf("published gateway not checked: %+v", gw.EgressStates())
	}
	apply(runtimeFor(3, section(30*time.Millisecond)))
	waitFor(t, "a restart on the new interval", func() bool { return workerStatus(workers, "egress").Starts == 2 })
	apply(runtimeFor(4, nil))
	waitFor(t, "the worker to stop", func() bool { return !workerStatus(workers, "egress").Running })
	// A check the stop cancelled may already be on the wire and reach the server a little later on a loaded machine:
	// let the count settle for longer than the 30 ms interval first, then a worker still running would add more.
	stopped := hits.Load()
	waitFor(t, "checks in flight at the stop to land", func() bool {
		time.Sleep(50 * time.Millisecond)
		n := hits.Load()
		defer func() { stopped = n }()
		return n == stopped
	})
	time.Sleep(100 * time.Millisecond)
	if hits.Load() != stopped {
		t.Fatal("egress checks continued after the section was removed")
	}
}

func confirmedRuntimeImport(t *testing.T, manager *settings.RuntimeCoordinator, req settings.ImportRequest) (*settings.Snapshot, error) {
	t.Helper()
	approval, err := manager.ConfirmImport(context.Background(), req)
	if err != nil {
		return nil, err
	}
	req.ConfirmationToken = approval.Token
	return manager.ImportAndApply(context.Background(), req)
}
