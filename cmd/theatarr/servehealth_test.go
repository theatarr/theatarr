package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func TestHealthCommand(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "theatarr.db")
	st, err := store.Open(ctx, storetest.Seed(t, db), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	st.Close()
	var out bytes.Buffer
	err = runHealth(commandSettingsArgs(t, []string{"-db", db, "-policy", examplePolicy(t, "192.0.2.1"), "-backup-dir", "off"}), &out)
	text := out.String()
	for _, want := range []string{"MountCheck", "MetadataRefreshCheck", "JobBacklogCheck", "FailedJobsCheck",
		"WARNING  BackupCheck          Backups are off", "ProviderSyncCheck", "DiskSpaceCheck"} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	// Without a running gateway the indexer and VPN checks are left out.
	if strings.Contains(text, "VpnEgressCheck") || strings.Contains(text, "IndexerStatusCheck") || strings.Contains(text, "ProviderAccountCheck") {
		t.Errorf("gateway checks offline:\n%s", text)
	}
	if strings.Contains(text, "ERROR") != (err != nil) {
		t.Errorf("exit %v with output:\n%s", err, text)
	}
	dir := examplePolicy(t, "192.0.2.1")
	optionalEgress(t, dir, "off")
	out.Reset()
	err = runHealth(commandSettingsArgs(t, []string{"-db", filepath.Join(t.TempDir(), "guard-off.db"), "-policy", dir, "-backup-dir", "off"}), &out)
	text = out.String()
	if !strings.Contains(text, "WARNING  VpnEgressCheck       VPN guard off") || strings.Contains(text, "ERROR") != (err != nil) {
		t.Errorf("guard off: %v\n%s", err, text)
	}
}

func TestMountHealthFollowsRuntimeSwitch(t *testing.T) {
	db, st, base, _ := runtimeFixture(t)
	ctx := context.Background()
	seed, err := st.BootstrapSwitches(ctx, base, "test")
	if err != nil {
		t.Fatal(err)
	}
	build := func(snap *settings.Snapshot) *serveRuntime {
		return &serveRuntime{Policy: snap.Policy, Library: &serveLibrary{lib: library.New(library.Options{Store: db, Policy: snap.Policy})}}
	}
	// Pause off first (confirmed), so the library starts on.
	pre, err := settings.NewRuntimeCoordinator(st, base, seed, build(seed), func(_ context.Context, snap *settings.Snapshot, _ *settings.Runtime) (any, error) {
		return build(snap), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	open := settings.SaveRequest{ExpectedGeneration: seed.Generation, Actor: "test", Records: seed.Records}
	open.Records.Switches = &settings.GlobalSwitches{Paused: false}
	proof, err := pre.Confirm(ctx, open)
	if err != nil {
		t.Fatal(err)
	}
	open.ConfirmationToken = proof.Token
	initial, err := pre.SaveAndApply(ctx, open)
	if err != nil {
		t.Fatal(err)
	}
	first := build(initial)
	manager, err := settings.NewRuntimeCoordinator(st, base, initial, first, func(_ context.Context, snap *settings.Snapshot, _ *settings.Runtime) (any, error) {
		return build(snap), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	hc := newServeHealth(serveHealthConfig{pol: initial.Policy, manager: manager, st: db, q: jobs.NewQueue(db), lib: first.Library.lib})
	oldCtx := manager.Capture(ctx)
	records := initial.Records
	records.Switches = &settings.GlobalSwitches{Paused: true}
	if _, err := manager.SaveAndApply(ctx, settings.SaveRequest{ExpectedGeneration: initial.Generation, Actor: "test", Records: records}); err != nil {
		t.Fatal(err)
	}
	for _, check := range hc.Results(ctx) {
		if check.Source == "MountCheck" && !strings.Contains(check.Message, "Library off") {
			t.Fatalf("health kept startup switch: %+v", check)
		}
	}
	_, oldLibrary := first.Library.forOperation(oldCtx)
	// The shared service resolves new operations through the coordinator; in-flight contexts keep their runtime.
	first.Library.manager = manager
	_, newLibrary := first.Library.forOperation(ctx)
	if !oldLibrary.lib.Gate().Enabled() || newLibrary.lib.Gate().Enabled() {
		t.Fatal("library gates mixed generations")
	}
}

type accountProvider struct {
	provider.Provider
	account provider.Account
	reads   int
}

func (p *accountProvider) Account(context.Context) (*provider.Account, error) {
	p.reads++
	a := p.account
	return &a, nil
}

// TestProviderAccountHealth: theatarr serve's health reads its provider clients' accounts, once per interval.
func TestProviderAccountHealth(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "theatarr.db")
	st, err := store.Open(ctx, storetest.Seed(t, db), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Load(examplePolicy(t, "127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	rd := &accountProvider{account: provider.Account{Premium: true, Expires: time.Now().Add(3 * 24 * time.Hour)}}
	hc := newServeHealth(serveHealthConfig{pol: pol, st: st, q: jobs.NewQueue(st), dbPath: db,
		accounts: map[string]provider.Provider{"rd": rd}})
	for range 2 {
		hc.Refresh()
		var found bool
		for _, r := range hc.Results(ctx) {
			if r.Source == "ProviderAccountCheck" {
				found = true
				if r.Type != health.Warning || !strings.Contains(r.Message, "rd: premium ends") {
					t.Fatalf("account check: %+v", r)
				}
			}
		}
		if !found {
			t.Fatal("no ProviderAccountCheck")
		}
	}
	if rd.reads != 1 {
		t.Fatalf("reads %d", rd.reads)
	}
}

// TestProviderAccountHealthAcrossSaves: a settings save rebuilds the provider clients; an account whose key did not
// change keeps its read, and a changed key is read at once.
func TestProviderAccountHealthAcrossSaves(t *testing.T) {
	db, st, base, _ := runtimeFixture(t)
	ctx := context.Background()
	base.Library.Enabled = true
	initial, err := st.BootstrapSwitches(ctx, base, "test")
	if err != nil {
		t.Fatal(err)
	}
	var clients []*accountProvider
	key := "rd-key-1"
	build := func(snap *settings.Snapshot) *serveRuntime {
		rd := &accountProvider{account: provider.Account{Premium: true, Expires: time.Now().Add(90 * 24 * time.Hour)}}
		clients = append(clients, rd)
		return &serveRuntime{Policy: snap.Policy, ProviderKeys: map[string]string{"rd": key},
			Providers: &providerSync{syncer: &provider.Syncer{Providers: map[string]provider.Provider{"rd": rd}}}}
	}
	manager, err := settings.NewRuntimeCoordinator(st, base, initial, build(initial), func(_ context.Context, snap *settings.Snapshot, _ *settings.Runtime) (any, error) {
		return build(snap), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	hc := newServeHealth(serveHealthConfig{pol: initial.Policy, manager: manager, st: db, q: jobs.NewQueue(db)})
	reads := func() (n int) {
		hc.Refresh()
		hc.Results(ctx)
		for _, c := range clients {
			n += c.reads
		}
		return n
	}
	if n := reads(); n != 1 {
		t.Fatalf("first reads %d", n)
	}
	generation := initial.Generation
	save := func() {
		t.Helper()
		records := initial.Records
		records.Switches = &settings.GlobalSwitches{Paused: true}
		snap, err := manager.SaveAndApply(ctx, settings.SaveRequest{ExpectedGeneration: generation, Actor: "test", Records: records})
		if err != nil {
			t.Fatal(err)
		}
		generation = snap.Generation
	}
	save()
	if n := reads(); n != 1 || len(clients) < 2 {
		t.Fatalf("unrelated save: %d reads over %d clients", n, len(clients))
	}
	key = "rd-key-2"
	save()
	if n := reads(); n != 2 || clients[len(clients)-1].reads != 1 {
		t.Fatalf("changed key: %d reads", n)
	}
}

// SetupCheck counts the sources that would run, as Setup does (settings.Snapshot.Ready): the runtime keeps the
// count of its generation, and a command's snapshot with every indexer disabled reports no indexer.
func TestSetupCheckCountsReadySources(t *testing.T) {
	t.Parallel()
	db, _, _, snap := runtimeFixture(t)
	ctx := context.Background()
	rt, err := prepareServeRuntime(ctx, snap, nil, db, jobs.NewQueue(db), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	indexers, providers := snap.Ready(nil)
	if indexers == 0 || providers == 0 || rt.ReadyIndexers != indexers || rt.ReadyProviders != providers {
		t.Fatalf("runtime ready %d/%d, snapshot %d/%d", rt.ReadyIndexers, rt.ReadyProviders, indexers, providers)
	}
	setup := func() health.Result {
		t.Helper()
		hc := newServeHealth(serveHealthConfig{pol: snap.Policy, snap: snap, st: db, q: jobs.NewQueue(db), dbPath: db.Path()})
		for _, r := range hc.Results(ctx) {
			if r.Source == "SetupCheck" {
				return r
			}
		}
		t.Fatal("no SetupCheck")
		return health.Result{}
	}
	if r := setup(); strings.Contains(r.Message, "no enabled indexer") || strings.Contains(r.Message, "no enabled debrid provider") {
		t.Fatalf("ready sources reported missing: %+v", r)
	}
	for i := range snap.Records.Indexers {
		snap.Records.Indexers[i].Enabled = false
	}
	if r := setup(); r.Type != health.Notice || !strings.Contains(r.Message, "no enabled indexer") {
		t.Fatalf("disabled indexers counted: %+v", r)
	}
}
