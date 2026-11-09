package settings

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
	"go.yaml.in/yaml/v3"
)

const exampleDir = "../../examples/policy"
const inventedSecret = "invented-credential-for-settings-tests"

// Only fixture references are resolved; these tests never inspect the process environment.
func fixtureLookup(name string) (string, bool) { return inventedSecret + "-" + name, true }

func setup(t *testing.T) (*Store, *store.Store, *policy.Policy) {
	t.Helper()
	db, err := store.Open(context.Background(), storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	s.SetTrashRoot("testdata/trash")
	p, err := policy.Load(exampleDir)
	if err != nil {
		t.Fatal(err)
	}
	return s, db, p
}

func bootstrap(t *testing.T, s *Store, p *policy.Policy) *Snapshot {
	t.Helper()
	snap, err := s.Bootstrap(context.Background(), p, ImportRequest{Dir: exampleDir, Actor: "admin", Lookup: fixtureLookup})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func saveRequest(p *policy.Policy, snap *Snapshot) SaveRequest {
	b, _ := json.Marshal(snap.Records)
	var records Records
	if err := json.Unmarshal(b, &records); err != nil {
		panic(err)
	}
	return SaveRequest{ExpectedGeneration: snap.Generation, Actor: "admin", Base: p, Records: records}
}

func audit(t *testing.T, s *Store) []Audit {
	t.Helper()
	a, err := s.Audit(context.Background(), 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestBootstrapRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db, p := setup(t)
	if snap, err := s.Load(ctx, &policy.Policy{}); err != nil || snap.Generation != 0 {
		t.Fatalf("fresh settings: %v", err)
	}
	snap := bootstrap(t, s, p)
	if snap.Generation != 1 || snap.Revision == p.Revision || snap.Policy.Revision != snap.Revision {
		t.Fatal("missing settings revision")
	}
	loaded, err := s.Load(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != snap.Revision || !reflect.DeepEqual(loaded.Records, snap.Records) {
		t.Fatal("load changed snapshot")
	}
	for _, got := range []*policy.Policy{snap.Policy, loaded.Policy} {
		if !reflect.DeepEqual(got.Indexers, p.Indexers) || !reflect.DeepEqual(got.Providers, p.Providers) || !reflect.DeepEqual(got.Budgets, p.Budgets) {
			t.Fatal("YAML/DB sections differ")
		}
		copy, err := p.Clone()
		if err != nil {
			t.Fatal(err)
		}
		copy.Revision = got.Revision
		if !reflect.DeepEqual(copy, got) {
			t.Fatal("adapter changed YAML-only policy or path metadata")
		}
	}
	for _, r := range snap.Records.Indexers {
		if r.ID == 0 || r.CreatedAt.IsZero() || r.CreatedAt != r.UpdatedAt || !r.Enabled {
			t.Fatalf("bad indexer metadata: %+v", r.Record)
		}
		if r.SecretSet != (r.Config.APIKeyEnv != "") {
			t.Fatal("incorrect indexer credential state")
		}
	}
	for _, r := range snap.Records.Providers {
		if !r.SecretSet || r.ID == 0 || !r.Enabled {
			t.Fatal("missing account metadata")
		}
	}
	var count int
	if err := db.Reader().QueryRow("SELECT count(*) FROM setting_secret").Scan(&count); err != nil || count == 0 {
		t.Fatalf("secrets absent: %v", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		stat, err := os.Stat(db.Path() + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if stat.Mode().Perm() != 0o600 {
			t.Fatalf("database %s mode %o", suffix, stat.Mode().Perm())
		}
	}
	a := audit(t, s)
	if a[len(a)-1].Operation != "bootstrap-import" || a[len(a)-1].Generation != 1 {
		t.Fatal("no bootstrap event")
	}
	// Later starts ignore even an unreadable mount and never consult the lookup.
	again, err := s.Bootstrap(ctx, p, ImportRequest{Dir: "missing-directory", Lookup: func(string) (string, bool) { t.Fatal("bootstrap resolved environment again"); return "", false }})
	if err != nil || again.Generation != snap.Generation {
		t.Fatalf("second bootstrap: %v", err)
	}
	if !reflect.DeepEqual(a, audit(t, s)) {
		t.Fatal("second bootstrap wrote audit")
	}
	// YAML changes outside the source records still reach the runtime; DB sections take precedence over the YAML mount.
	base, _ := p.Clone()
	base.Revision = "new-yaml-revision"
	base.Indexers = nil
	base.Providers = nil
	base.Budgets = nil
	base.Metadata.Sources["tmdb"].URL = "https://different.example/api"
	next, err := s.Load(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if next.Policy.Metadata.Sources["tmdb"].URL != base.Metadata.Sources["tmdb"].URL || !reflect.DeepEqual(next.Policy.Indexers, p.Indexers) || next.Revision == snap.Revision {
		t.Fatal("wrong YAML/DB precedence")
	}
}

func TestSecretCarryoverFallbackAndRotation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db, p := setup(t)
	snap := bootstrap(t, s, p)
	check := func(snap *Snapshot, entity, key, want string, lookup LookupSecret) {
		t.Helper()
		if err := snap.WithSecret(entity, key, lookup, func(value string) error {
			if value != want {
				t.Error("wrong credential")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	stored, _ := fixtureLookup(p.Providers["rd"].APIKeyEnv)
	check(snap, "providers", "rd", stored, func(string) (string, bool) { t.Fatal("DB credential did not win"); return "", false })
	check(snap, "indexers", "prowlarr-public", inventedSecret+"-THEATARR_PROWLARR_KEY", nil)
	check(snap, "indexers", "torrentio", "", nil)
	before := audit(t, s)
	req := saveRequest(p, snap)
	req.SecretWrites = map[string]Secret{"providers/rd": NewSecret("rotated-" + inventedSecret)}
	rotated, err := s.Save(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Revision == snap.Revision {
		t.Fatal("rotation kept revision")
	}
	check(rotated, "providers", "rd", "rotated-"+inventedSecret, nil)
	check(snap, "providers", "rd", stored, nil) // old snapshots remain consistent
	after := audit(t, s)
	if len(after) != len(before)+2 {
		t.Fatal("credential rotation should audit account and generation")
	}
	var diff map[string]any
	if err := json.Unmarshal(after[len(before)].After, &diff); err != nil || diff["secret"] != "set" {
		t.Fatalf("rotation marker: %v", err)
	}
	req = saveRequest(p, rotated)
	req.SecretWrites = map[string]Secret{"providers/rd": NewSecret("")}
	cleared, err := s.Save(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range cleared.Records.Providers {
		if r.Key == "rd" && r.SecretSet {
			t.Fatal("clear still marked set")
		}
	}
	// An explicit clear means no credential: the retained THEATARR_RD_KEY fallback is not consulted.
	check(cleared, "providers", "rd", "", func(name string) (string, bool) {
		t.Fatal("a cleared credential consulted its environment fallback " + name)
		return "fallback-test-credential", true
	})
	check(cleared, "providers", "rd", "", nil)
	check(cleared, "providers", "rd", "", func(string) (string, bool) { return "unavailable", false })
	loaded, err := s.Load(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	check(loaded, "providers", "rd", "", nil)
	// The secret table stores plain text, not a digest or encryption envelope.
	var raw string
	if err := db.Reader().QueryRow("SELECT value FROM setting_secret WHERE id = (SELECT secret_id FROM setting_provider WHERE key = 'ad')").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if want, _ := fixtureLookup("THEATARR_AD_KEY"); raw != want {
		t.Fatal("unexpected credential storage")
	}
	if err := loaded.WithSecret("providers", "rd", nil, func(value string) error { return fmt.Errorf("remote error with %s", inventedSecret) }); err == nil || strings.Contains(err.Error(), inventedSecret) {
		t.Fatal("credential constructor error leaked")
	}
	if err := loaded.WithSecret("providers", "unknown", nil, func(string) error { t.Fatal("unknown reference used"); return nil }); err == nil {
		t.Fatal("unknown credential accepted")
	}
}

func TestRedaction(t *testing.T) {
	ctx := context.Background()
	s, _, p := setup(t)
	snap := bootstrap(t, s, p)
	req := saveRequest(p, snap)
	req.SecretWrites = map[string]Secret{"providers/rd": NewSecret(inventedSecret)}
	preview, err := s.PreviewImport(ctx, ImportRequest{Dir: exampleDir, Base: p, ExpectedGeneration: 1, Lookup: fixtureLookup})
	if err != nil {
		t.Fatal(err)
	}
	exported, err := snap.Export()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(exported), "_env") || strings.Contains(string(exported), "THEATARR_RD_KEY") || !strings.Contains(string(exported), "credential: set") {
		t.Fatal("export did not strip references or show markers")
	}
	var log bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&log, nil))
	for _, value := range []any{NewSecret(inventedSecret), snap, snap.Records, req, preview, audit(t, s)} {
		for _, format := range []string{"%s", "%v", "%+v", "%#v", "%d", "%x", "%q"} {
			assertRedacted(t, fmt.Sprintf(format, value))
		}
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		assertRedacted(t, string(b))
		b, err = yaml.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		assertRedacted(t, string(b))
		logger.Info("settings", "record", value)
	}
	assertRedacted(t, log.String())
	assertRedacted(t, string(exported))
	assertRedacted(t, preview.YAML)
	for _, secret := range []Secret{{}, NewSecret(inventedSecret)} {
		b, err := json.Marshal(secret)
		if err != nil || string(b) != `"`+secret.String()+`"` {
			t.Fatal("bad marker")
		}
	}
	// Validation may echo invalid values; known credential bytes are removed while paths stay structured.
	req.Records.Providers[0].Config.Kind = inventedSecret
	_, err = s.Save(ctx, req)
	var validation *policy.Errors
	if !errors.As(err, &validation) {
		t.Fatalf("missing structured errors: %v", err)
	}
	assertRedacted(t, fmt.Sprintf("%+v", err))
	// Even early metadata validation and joined driver/cancellation errors stay redacted.
	req.Records.Indexers[0].Key = inventedSecret
	req.Records.Indexers[0].Priority = -1
	_, err = s.Save(ctx, req)
	if !errors.As(err, &validation) {
		t.Fatalf("missing priority validation: %v", err)
	}
	assertRedacted(t, fmt.Sprintf("%+v", err))
	for _, sentinel := range []error{ErrConflict, ErrUninitialized, context.Canceled, context.DeadlineExceeded} {
		err := dbError(errors.Join(errors.New(inventedSecret), sentinel))
		if !errors.Is(err, sentinel) {
			t.Fatal("lost sentinel error")
		}
		assertRedacted(t, fmt.Sprintf("%+v", err))
	}
}

func assertRedacted(t *testing.T, text string) {
	t.Helper()
	for _, secret := range []string{inventedSecret, "rotated-" + inventedSecret} {
		h := sha256.Sum256([]byte(secret))
		if strings.Contains(text, secret) || strings.Contains(text, hex.EncodeToString(h[:])) {
			t.Fatal("credential or digest disclosed")
		}
	}
}

func TestSaveRollback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db, p := setup(t)
	snap := bootstrap(t, s, p)
	before := audit(t, s)
	req := saveRequest(p, snap)
	req.SecretWrites = map[string]Secret{"providers/rd": NewSecret("rotated-" + inventedSecret)}
	req.Records.Budgets[0].Config.Requests++
	// Fail at the final audit event, after records, secrets and earlier audit rows have been written.
	if _, err := db.Writer().Exec(`CREATE TRIGGER fail_settings BEFORE INSERT ON settings_audit WHEN NEW.entity = 'settings' BEGIN SELECT RAISE(ABORT, 'invented-credential-for-settings-tests'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := s.Save(ctx, req)
	if err == nil {
		t.Fatal("save succeeded")
	}
	assertRedacted(t, fmt.Sprintf("%+v", err))
	loaded, err := s.Load(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Generation != snap.Generation || loaded.Revision != snap.Revision || !reflect.DeepEqual(loaded.Records, snap.Records) || !reflect.DeepEqual(loaded.secrets, snap.secrets) {
		t.Fatal("failed save changed state")
	}
	if !reflect.DeepEqual(before, audit(t, s)) {
		t.Fatal("failed save left audit rows")
	}
	if _, err := db.Writer().Exec(`DROP TRIGGER fail_settings`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(ctx, req); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func TestConcurrentWriters(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db, p := setup(t)
	snap := bootstrap(t, s, p)
	// A second Store uses an independent writer connection to the same database.
	otherDB, err := store.Open(ctx, db.Path(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer otherDB.Close()
	other, err := New(otherDB)
	if err != nil {
		t.Fatal(err)
	}
	const writers = 12
	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			writer := s
			if i%2 == 1 {
				writer = other
			}
			_, err := writer.Save(ctx, saveRequest(p, snap))
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	won, stale := 0, 0
	for err := range errs {
		if err == nil {
			won++
		} else if errors.Is(err, ErrConflict) {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if won != 1 || stale != writers-1 {
		t.Fatalf("winners %d, stale %d", won, stale)
	}
	loaded, err := s.Load(ctx, p)
	if err != nil || loaded.Generation != 2 {
		t.Fatalf("generation: %v", err)
	}
	for _, a := range audit(t, s) {
		if a.Generation > 2 {
			t.Fatal("lost generation serialization")
		}
	}
}

func TestConcurrentBootstrap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, _ := setup(t)
	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			var once sync.Once
			lookup := func(name string) (string, bool) {
				once.Do(func() { ready <- struct{}{}; <-start })
				return fixtureLookup(name)
			}
			// With no base, each contender uses the imported YAML-only sections.
			snap, err := s.Bootstrap(ctx, nil, ImportRequest{Dir: exampleDir, Actor: "admin", Lookup: lookup})
			if err == nil && snap.Generation != 1 {
				err = errors.New("bootstrap created more than one generation")
			}
			errs <- err
		}()
	}
	<-ready
	<-ready
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range audit(t, s) {
		if a.Generation != 1 {
			t.Fatal("bootstrap wrote another generation")
		}
	}
}

func copyFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	err := filepath.WalkDir(exampleDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(exampleDir, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dir, rel), 0o700)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, rel), b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func editFixture(t *testing.T, dir, file, from, to string) {
	t.Helper()
	path := filepath.Join(dir, file)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), from) {
		t.Fatalf("fixture missing %q", from)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(b), from, to)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidImportsChangeNothing(t *testing.T) {
	t.Parallel()
	for _, initialized := range []bool{false, true} {
		for _, kind := range []string{"parse", "invalid", "partial", "secret", "current-base"} {
			t.Run(fmt.Sprintf("initialized=%t/%s", initialized, kind), func(t *testing.T) {
				ctx := context.Background()
				s, db, p := setup(t)
				var before *Snapshot
				gen := int64(0)
				if initialized {
					before = bootstrap(t, s, p)
					gen = before.Generation
				}
				a := audit(t, s)
				dir := copyFixture(t)
				lookup := LookupSecret(fixtureLookup)
				base, _ := p.Clone()
				switch kind {
				case "parse":
					editFixture(t, dir, "providers.yaml", "api_key_env: THEATARR_RD_KEY", "api_key: "+inventedSecret)
				case "invalid":
					editFixture(t, dir, "providers.yaml", "kind: realdebrid", "kind: "+inventedSecret)
				case "partial":
					if err := os.Remove(filepath.Join(dir, "budgets.yaml")); err != nil {
						t.Fatal(err)
					}
				case "secret":
					lookup = func(name string) (string, bool) {
						if name == "THEATARR_TB_KEY" {
							return "", false
						}
						return fixtureLookup(name)
					}
				case "current-base":
					base.Versions["movie-web"].Providers = []string{"absent-account"}
				}
				req := ImportRequest{Dir: dir, Base: base, Actor: "admin", ExpectedGeneration: gen, Lookup: lookup}
				if _, err := s.PreviewImport(ctx, req); err == nil {
					t.Fatal("preview accepted invalid input")
				} else {
					assertRedacted(t, err.Error())
				}
				if _, err := s.Import(ctx, req); err == nil {
					t.Fatal("import accepted invalid input")
				} else {
					assertRedacted(t, err.Error())
				}
				if !initialized {
					if _, err := s.Bootstrap(ctx, base, req); err == nil {
						t.Fatal("bootstrap accepted invalid input")
					}
				}
				if !reflect.DeepEqual(a, audit(t, s)) {
					t.Fatal("invalid import wrote audit")
				}
				if initialized {
					after, err := s.Load(ctx, p)
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatalf("invalid import changed state: %v", err)
					}
				} else {
					for _, table := range []string{"settings_generation", "setting_secret", "setting_indexer", "setting_provider", "setting_budget"} {
						var count int
						if err := db.Reader().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
							t.Fatalf("partial %s: %v", table, err)
						}
					}
				}
			})
		}
	}
}

func TestPreviewAndExplicitImport(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, p := setup(t)
	snap := bootstrap(t, s, p)
	dir := copyFixture(t)
	editFixture(t, dir, "indexers.yaml", "page_cap: 200", "page_cap: 99")
	req := ImportRequest{Dir: dir, Base: p, Actor: "admin", ExpectedGeneration: 1, Lookup: fixtureLookup}
	before := audit(t, s)
	preview, err := s.PreviewImport(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, audit(t, s)) || !strings.Contains(preview.YAML, "page_cap: 99") {
		t.Fatal("preview wrote or missed change")
	}
	for _, r := range preview.After.Indexers {
		if r.Key == "dmm" && r.Config.PageCap != 99 {
			t.Fatal("preview missed indexer change")
		}
	}
	imported, err := confirmedImport(t, s, req)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Generation != 2 || imported.Policy.Indexers["dmm"].PageCap != 99 {
		t.Fatal("import not applied")
	}
	for i, r := range snap.Records.Indexers {
		if imported.Records.Indexers[i].ID != r.ID || imported.Records.Indexers[i].CreatedAt != r.CreatedAt {
			t.Fatal("import replaced stable identity")
		}
	}
	if _, err := s.Import(ctx, req); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale import: %v", err)
	}
	if _, err := s.PreviewImport(ctx, req); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale preview: %v", err)
	}
	page, err := s.Audit(ctx, before[len(before)-1].ID, 1)
	if err != nil || len(page) != 1 || page[0].Generation != 2 {
		t.Fatalf("audit page: %v", err)
	}
	// Newest first: the first page starts at the newest record, and a before cursor continues to older ones.
	all, err := s.Audit(ctx, 0, 1000)
	if err != nil || len(all) < 2 {
		t.Fatalf("audit: %v", err)
	}
	newest, err := s.AuditBefore(ctx, 0, 2)
	if err != nil || len(newest) != 2 || newest[0].ID != all[len(all)-1].ID || newest[1].ID != all[len(all)-2].ID {
		t.Fatalf("audit newest first: %v %+v", err, newest)
	}
	older, err := s.AuditBefore(ctx, newest[1].ID, 1000)
	if err != nil || len(older) != len(all)-2 || older[0].ID != all[len(all)-3].ID || older[len(older)-1].ID != all[0].ID {
		t.Fatalf("audit older page: %v %+v", err, older)
	}
	// Dropping an optional environment reference preserves the DB credential in preview and apply.
	editFixture(t, dir, "indexers.yaml", "api_key_env: THEATARR_PROWLARR_KEY", "api_key_env: ''")
	req.ExpectedGeneration = imported.Generation
	preview, err = s.PreviewImport(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range preview.After.Indexers {
		if r.Key == "prowlarr-public" && !r.SecretSet {
			t.Fatal("preview lost stored credential")
		}
	}
	imported, err = s.Import(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := imported.WithSecret("indexers", "prowlarr-public", nil, func(value string) error {
		if want, _ := fixtureLookup("THEATARR_PROWLARR_KEY"); value != want {
			t.Fatal("import lost stored credential")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSaveValidationAndRecordLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db, p := setup(t)
	snap := bootstrap(t, s, p)
	req := saveRequest(p, snap)
	// Removing a referenced indexer, invalid pacing and an unknown provider in an indexer's allowlist must be
	// rejected by whole-policy validation.
	for _, mutate := range []func(*Records){
		func(r *Records) { r.Indexers[0].Priority = -1 },
		func(r *Records) { r.Indexers = nil },
		func(r *Records) { r.Providers[0].Config.Pacing.AddsPerMinute = -1 },
		func(r *Records) {
			for i := range r.Indexers {
				if r.Indexers[i].Key == "dmm" {
					r.Indexers[i].Config.Providers = []string{"nope"}
				}
			}
		},
	} {
		copy, err := s.Load(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		req.Records = copy.Records
		mutate(&req.Records)
		_, err = s.Save(ctx, req)
		var validation *policy.Errors
		if !errors.As(err, &validation) {
			t.Fatalf("expected policy errors: %v", err)
		}
	}
	req = saveRequest(p, snap)
	// A DB-only provider needs no environment indirection; the adapter supplies Validate's reference shape.
	req.Records.Providers = append(req.Records.Providers, Provider{Record: Record{Key: "spare"}, Config: policy.Provider{Kind: "torbox", Order: 4, URL: "https://spare.example/api"}})
	req.SecretWrites = map[string]Secret{"providers/spare": NewSecret(inventedSecret)}
	created, err := s.Save(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	var spare Provider
	for _, r := range created.Records.Providers {
		if r.Key == "spare" {
			spare = r
		}
	}
	if spare.ID == 0 || spare.Enabled || !spare.SecretSet || spare.Config.APIKeyEnv != "" {
		t.Fatal("bad DB-only provider")
	}
	if err := created.WithSecret("providers", "spare", nil, func(value string) error {
		if value != inventedSecret {
			t.Fatal("missing DB-only secret")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	req = saveRequest(p, created)
	req.Records.Providers = nil
	for _, r := range created.Records.Providers {
		if r.Key != "spare" {
			req.Records.Providers = append(req.Records.Providers, r)
		}
	}
	req.Records.Indexers[0].Priority = 10
	req.Records.Indexers[0].Enabled = false
	deleted, err := s.Save(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Records.Indexers[0].Priority != 10 || deleted.Records.Indexers[0].Enabled {
		t.Fatal("indexer metadata lost")
	}
	if _, ok := deleted.secrets["providers/spare"]; ok {
		t.Fatal("deleted credential retained in snapshot")
	}
	var count int
	if err := db.Reader().QueryRow("SELECT count(*) FROM setting_secret WHERE value = ?", inventedSecret).Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphan credential: %v", err)
	}
	// Deleted IDs are not reused.
	req = saveRequest(p, deleted)
	deletedID := spare.ID
	spare.ID = 0
	req.Records.Providers = append(req.Records.Providers, spare)
	recreated, err := s.Save(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recreated.Records.Providers {
		if r.Key == "spare" && r.ID <= deletedID {
			t.Fatal("reused deleted ID")
		}
	}
	loaded, err := s.Load(ctx, p)
	if err != nil || !reflect.DeepEqual(recreated.Records, loaded.Records) {
		t.Fatalf("lifecycle load: %v", err)
	}
}

func TestRuntimeBootstrapReload(t *testing.T) {
	ctx := context.Background()
	s, _, base := setup(t)
	initial, err := s.Bootstrap(ctx, base, ImportRequest{Dir: exampleDir, Actor: "admin", Lookup: fixtureLookup})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Policy.Indexers, base.Indexers) || !reflect.DeepEqual(loaded.Policy.Providers, base.Providers) ||
		!reflect.DeepEqual(loaded.Policy.Budgets, base.Budgets) {
		t.Fatal("bootstrap and load differ from YAML for source sections")
	}

	failPrepare := false
	runtime, err := NewRuntimeCoordinator(s, base, loaded, "first", func(_ context.Context, snap *Snapshot, _ *Runtime) (any, error) {
		if failPrepare {
			return nil, errors.New("prepare failed")
		}
		return snap.Generation, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	old := runtime.Current()
	searchStarted, finishSearch := make(chan struct{}), make(chan struct{})
	searchResult := make(chan int64, 1)
	go func(captured *Runtime) {
		close(searchStarted)
		<-finishSearch
		searchResult <- captured.Generation
	}(runtime.Current())
	<-searchStarted

	req := saveRequest(base, loaded)
	req.Records.Budgets[0].Config.Requests++
	next, err := s.Save(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	failPrepare = true
	if err := runtime.Reload(ctx, next.Generation); err == nil {
		t.Fatal("failed prepare succeeded")
	}
	if runtime.Current() != old {
		t.Fatal("failed prepare changed active runtime")
	}
	failPrepare = false
	if err := runtime.Reload(ctx, next.Generation); err != nil {
		t.Fatal(err)
	}
	close(finishSearch)
	if got := <-searchResult; got != initial.Generation {
		t.Fatalf("in-flight search used generation %d, want %d", got, initial.Generation)
	}
	if runtime.Current().Generation != next.Generation {
		t.Fatalf("active generation %d, want %d", runtime.Current().Generation, next.Generation)
	}
}

func TestRuntimeReloadPreservesYAMLSafetySwitches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, base := setup(t)
	initial := bootstrap(t, s, base)
	before := initial.Policy
	req := saveRequest(base, initial)
	req.Records.Budgets[0].Config.Requests++
	next, err := s.Save(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntimeCoordinator(s, base, initial, nil, func(_ context.Context, snap *Snapshot, _ *Runtime) (any, error) {
		return snap.Generation, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Reload(ctx, next.Generation); err != nil {
		t.Fatal(err)
	}
	after := runtime.Current().Policy
	if !reflect.DeepEqual(before.Router, after.Router) || !reflect.DeepEqual(before.Library, after.Library) ||
		!reflect.DeepEqual(before.Notify, after.Notify) || !sameModes(before.Versions, after.Versions) {
		t.Fatal("reload changed a YAML-owned safety switch or version mode")
	}
}

func TestSaveAndApplyPreparesBeforeCommit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, base := setup(t)
	initial := bootstrap(t, s, base)
	fail := true
	runtime, err := NewRuntimeCoordinator(s, base, initial, "initial", func(_ context.Context, _ *Snapshot, _ *Runtime) (any, error) {
		if fail {
			return nil, errors.New("prepare failed")
		}
		return "prepared", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	req := saveRequest(base, initial)
	req.Records.Budgets[0].Config.Requests++
	beforeAudit := len(audit(t, s))
	if _, err := runtime.SaveAndApply(ctx, req); err == nil || !strings.Contains(err.Error(), "prepare failed") {
		t.Fatalf("save with failed prepare: %v", err)
	}
	loaded, err := s.Load(ctx, base)
	if err != nil || loaded.Generation != initial.Generation {
		t.Fatalf("failed prepare committed generation %v (%v)", loaded, err)
	}
	if len(audit(t, s)) != beforeAudit || runtime.Current().Generation != initial.Generation {
		t.Fatal("failed prepare changed audit or active runtime")
	}
	fail = false
	saved, err := runtime.SaveAndApply(ctx, req)
	if err != nil || saved.Generation != initial.Generation+1 || runtime.Current().Generation != saved.Generation {
		t.Fatalf("successful save/apply: snapshot=%v active=%d err=%v", saved, runtime.Current().Generation, err)
	}
}

func TestRuntimeGenerationDoesNotWaitForPrepare(t *testing.T) {
	ctx := context.Background()
	s, _, base := setup(t)
	initial := bootstrap(t, s, base)
	started, release := make(chan struct{}), make(chan struct{})
	runtime, err := NewRuntimeCoordinator(s, base, initial, nil, func(_ context.Context, _ *Snapshot, _ *Runtime) (any, error) {
		close(started)
		<-release
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	req := saveRequest(base, initial)
	req.Records.Budgets[0].Config.Requests++
	next, err := s.Save(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	reloadDone := make(chan error, 1)
	go func() { reloadDone <- runtime.Reload(ctx, next.Generation) }()
	<-started
	readDone := make(chan bool, 1)
	go func() {
		got, ok := runtime.Generation(initial.Generation)
		readDone <- ok && got.Generation == initial.Generation
	}()
	select {
	case ok := <-readDone:
		if !ok {
			t.Fatal("Generation did not return the active runtime")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Generation waited for runtime preparation")
	}
	close(release)
	if err := <-reloadDone; err != nil {
		t.Fatal(err)
	}
}

func TestMarkPublishedIsIdempotentAndKeepsFallback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, base := setup(t)
	initial := bootstrap(t, s, base)
	if err := s.MarkPublished(ctx, initial.Generation); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkPublished(ctx, initial.Generation); err != nil {
		t.Fatal(err)
	}
	req := saveRequest(base, initial)
	req.Records.Budgets[0].Config.Requests++
	next, err := s.Save(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkPublished(ctx, next.Generation); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkPublished(ctx, next.Generation); err != nil {
		t.Fatal(err)
	}
	previous, err := s.PreviousPublishedGeneration(ctx)
	if err != nil || previous != initial.Generation {
		t.Fatalf("previous published generation %d, %v; want %d", previous, err, initial.Generation)
	}
	fallback, err := s.FallbackGeneration(ctx, next.Generation)
	if err != nil || fallback != initial.Generation {
		t.Fatalf("fallback generation %d, %v; want %d", fallback, err, initial.Generation)
	}
}

func TestMarkPublishedPrunesOlderSnapshots(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, base := setup(t)
	gen := bootstrap(t, s, base)
	if err := s.MarkPublished(ctx, gen.Generation); err != nil {
		t.Fatal(err)
	}
	gens := []int64{gen.Generation}
	for i := 0; i < 2; i++ {
		req := saveRequest(base, gen)
		req.Records.Budgets[0].Config.Requests++
		next, err := s.Save(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.MarkPublished(ctx, next.Generation); err != nil {
			t.Fatal(err)
		}
		gen = next
		gens = append(gens, next.Generation)
	}
	if _, err := s.LoadGeneration(ctx, base, gens[0]); err == nil || !strings.Contains(err.Error(), "no stored snapshot") {
		t.Fatalf("generation %d snapshot kept after two newer publishes: %v", gens[0], err)
	}
	var raw string
	if err := s.db.Reader().QueryRowContext(ctx, "SELECT snapshot FROM settings_generation WHERE generation = ?",
		gens[0]).Scan(&raw); err != nil || raw != "{}" {
		t.Fatalf("pruned snapshot %q, %v", raw, err)
	}
	for _, g := range gens[1:] {
		if _, err := s.LoadGeneration(ctx, base, g); err != nil {
			t.Fatalf("published generation %d: %v", g, err)
		}
	}
}

type afterPublishProbe struct {
	current func() *Runtime
	seen    chan int64
}

func (p afterPublishProbe) AfterPublish() { p.seen <- p.current().Generation }

func TestRuntimeAfterPublishRunsAfterAtomicPublication(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, base := setup(t)
	initial := bootstrap(t, s, base)
	var runtime *RuntimeCoordinator
	seen := make(chan int64, 1)
	runtime, err := NewRuntimeCoordinator(s, base, initial, nil, func(_ context.Context, snap *Snapshot, _ *Runtime) (any, error) {
		return afterPublishProbe{seen: seen, current: runtime.Current}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	req := saveRequest(base, initial)
	req.Records.Budgets[0].Config.Requests++
	saved, err := runtime.SaveAndApply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if got := <-seen; got != saved.Generation {
		t.Fatalf("AfterPublish saw generation %d, want %d", got, saved.Generation)
	}
}

func sameModes(a, b map[string]*policy.VersionPolicy) bool {
	if len(a) != len(b) {
		return false
	}
	for key, version := range a {
		other := b[key]
		if other == nil || version.Mode != other.Mode {
			return false
		}
	}
	return true
}

// With the database as the settings source the import base is the YAML without the DB-owned sections
// (ReadBase): the versions' and library's references to indexers, providers and budgets resolve only against the
// imported records, so the base alone must not be validated (an import preview over the demo answered 422).
func TestPreviewImportOverDBModeBase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, p := setup(t)
	bootstrap(t, s, p)
	dir := copyFixture(t)
	base, err := policy.ReadSettingsOrEmpty(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	req := ImportRequest{Dir: dir, Base: base, Actor: "admin", ExpectedGeneration: 1, Lookup: fixtureLookup}
	if _, err := s.PreviewImport(ctx, req); err != nil {
		t.Fatalf("preview over the db-mode base: %v", err)
	}
	// The merged result is still validated: a version naming an indexer the import lacks is refused.
	editFixture(t, dir, "versions.yaml", "prowlarr-public", "no-such-indexer")
	var v *policy.Errors
	if _, err := s.PreviewImport(ctx, req); !errors.As(err, &v) {
		t.Fatalf("bad reference: %v", err)
	}
}

func confirmedImport(t *testing.T, s *Store, req ImportRequest) (*Snapshot, error) {
	t.Helper()
	current, err := s.Load(context.Background(), req.Base)
	if err != nil {
		return nil, err
	}
	manager := versionCoordinator(t, s, req.Base, current)
	approval, err := manager.ConfirmImport(context.Background(), req)
	if err != nil {
		return nil, err
	}
	req.ConfirmationToken = approval.Token
	return manager.ImportAndApply(context.Background(), req)
}
