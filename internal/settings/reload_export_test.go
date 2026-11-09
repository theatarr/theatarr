package settings

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
	"go.yaml.in/yaml/v3"
)

// transitionCommitter is a prepared runtime whose commit hook is the provider transition: it records that it ran
// inside the commit transaction, refuses when told to, and advances the epoch for an account change.
type transitionCommitter struct {
	epoch  int64
	bump   bool
	refuse error
	ran    *int
}

func (c transitionCommitter) PreparedProviderEpoch() int64 { return c.epoch }

func (c transitionCommitter) CommitSettings(ctx context.Context, tx *sql.Tx) error {
	*c.ran++
	if c.refuse != nil {
		return c.refuse
	}
	if c.bump {
		return IncrementProviderEpoch(ctx, tx)
	}
	return nil
}

// A reload of the stored head over an older active runtime (a startup fallback) is a provider transition like a
// save: its commit hook runs in a transaction, can refuse, and advances the epoch for an account change.
func TestReloadCommitsProviderTransition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db, p := setup(t)
	initial := bootstrap(t, s, p)
	if err := s.MarkPublished(ctx, initial.Generation); err != nil {
		t.Fatal(err)
	}
	req := saveRequest(p, initial)
	req.SecretWrites = map[string]Secret{"providers/rd": NewSecret("account-b")}
	head, err := s.Save(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	ran := 0
	var refuse error
	manager, err := NewRuntimeCoordinator(s, p, initial, "fallback", func(_ context.Context, snap *Snapshot, active *Runtime) (any, error) {
		// The transition is computed against the active runtime: the account differs from the fallback's.
		bump := active != nil && active.Generation == initial.Generation && !snap.SameSecret(initial, "providers", "rd")
		epoch := snap.ProviderEpoch
		if bump {
			epoch++
		}
		return transitionCommitter{epoch: epoch, bump: bump, refuse: refuse, ran: &ran}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	refuse = &policy.Errors{List: []policy.FieldError{{Path: "providers[rd]", Msg: "removal would orphan 1 active download(s)"}}}
	if err := manager.Reload(ctx, head.Generation); err == nil || !strings.Contains(err.Error(), "orphan") {
		t.Fatalf("refused reload err=%v (commit hook ran %d times)", err, ran)
	}
	if manager.Current().Generation != initial.Generation {
		t.Fatal("a reload refused by its commit hook was published")
	}
	refuse = nil
	if err := manager.Reload(ctx, head.Generation); err != nil {
		t.Fatal(err)
	}
	var epoch int64
	if err := db.Reader().QueryRow("SELECT provider_epoch FROM settings_runtime_state WHERE id = 1").Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	current := manager.Current()
	if epoch != 1 || current.Generation != head.Generation || current.ProviderEpoch != 1 ||
		current.Components.(transitionCommitter).epoch != 1 {
		t.Fatalf("account change through reload: db epoch=%d runtime=%+v", epoch, current)
	}
	stored, err := s.Load(ctx, p)
	if err != nil || stored.Generation != head.Generation {
		t.Fatalf("reload wrote a generation: %v %v", stored, err)
	}
	if last, err := s.LastPublishedGeneration(ctx); err != nil || last != head.Generation {
		t.Fatalf("published marker %d %v", last, err)
	}
}

// A reload whose head moved after it was loaded is refused by the commit, not published.
func TestReloadRefusesHeadChangedBeforeCommit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, p := setup(t)
	initial := bootstrap(t, s, p)
	req := saveRequest(p, initial)
	req.Records.Budgets[0].Config.Requests++
	head, err := s.Save(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	var manager *RuntimeCoordinator
	manager, err = NewRuntimeCoordinator(s, p, initial, "fallback", func(_ context.Context, snap *Snapshot, _ *Runtime) (any, error) {
		if snap.Generation == head.Generation {
			// Another process saves while this reload prepares.
			next := saveRequest(p, head)
			next.Records.Budgets[0].Config.Requests++
			if _, err := s.Save(ctx, next); err != nil {
				return nil, err
			}
		}
		return snap.Generation, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Reload(ctx, head.Generation); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale reload err=%v", err)
	}
	if manager.Current().Generation != initial.Generation {
		t.Fatal("stale reload was published")
	}
}

func credential(t *testing.T, snap *Snapshot, entity, key string, lookup LookupSecret) string {
	t.Helper()
	var got string
	if err := snap.WithSecret(entity, key, lookup, func(v string) error { got = v; return nil }); err != nil {
		t.Fatal(err)
	}
	return got
}

// An explicit clear means no credential: the record's retained environment name is not consulted again, in the
// committed snapshot, a later load and a stored fallback generation. Only a record that never had a DB credential
// (set or cleared) keeps the one-release environment fallback.
func TestClearedCredentialNeverFallsBackToEnvironment(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db, p := setup(t)
	initial := bootstrap(t, s, p)
	env := func(name string) (string, bool) { return "environment-" + name, true }
	req := saveRequest(p, initial)
	req.SecretWrites = map[string]Secret{"providers/rd": NewSecret("")}
	cleared, err := s.Save(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkPublished(ctx, cleared.Generation); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := s.LoadGeneration(ctx, p, cleared.Generation)
	if err != nil {
		t.Fatal(err)
	}
	for name, snap := range map[string]*Snapshot{"saved": cleared, "loaded": loaded, "generation": stored} {
		if got := credential(t, snap, "providers", "rd", env); got != "" {
			t.Fatalf("%s: a cleared credential resolved through its environment fallback", name)
		}
		if snap.CredentialSet("providers", "rd", env) {
			t.Fatalf("%s: a cleared credential reported as set", name)
		}
	}
	// A bootstrap-era record with no DB credential row at all still uses its named fallback.
	if _, err := db.Writer().Exec(`UPDATE setting_indexer SET secret_id = NULL WHERE key = 'prowlarr-public'`); err != nil {
		t.Fatal(err)
	}
	legacy, err := s.Load(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if got := credential(t, legacy, "indexers", "prowlarr-public", env); got != "environment-THEATARR_PROWLARR_KEY" {
		t.Fatal("a never-set credential lost its environment fallback")
	}
	if !legacy.CredentialSet("indexers", "prowlarr-public", env) || legacy.CredentialSet("indexers", "prowlarr-public", nil) {
		t.Fatal("effective credential presence of a fallback record is wrong")
	}
	// Clearing that record makes it explicit too.
	req = saveRequest(p, legacy)
	req.SecretWrites = map[string]Secret{"indexers/prowlarr-public": NewSecret("")}
	again, err := s.Save(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if got := credential(t, again, "indexers", "prowlarr-public", env); got != "" {
		t.Fatal("clearing a fallback record kept the environment credential")
	}
	reloaded, err := s.Load(ctx, p)
	if err != nil || credential(t, reloaded, "indexers", "prowlarr-public", env) != "" {
		t.Fatalf("clear of a fallback record not persisted: %v", err)
	}
}

// Webhook URLs (often token-bearing) never leave through the review export or the import preview.
func TestExportRedactsWebhookURLs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, p := setup(t)
	initial := bootstrap(t, s, p)
	base, err := p.Clone()
	if err != nil {
		t.Fatal(err)
	}
	base.Notify.Targets = map[string]*policy.NotifyTarget{
		"hook": {URL: "https://hooks.example.net/api/webhooks/123/tok-path-secret?token=tok-query-secret", Native: true},
	}
	saved, err := s.Load(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	b, err := saved.Export()
	if err != nil {
		t.Fatal(err)
	}
	// The stored generation has no notify section, so the import takes it, with the same target, from the files.
	dir := copyFixture(t)
	editFixture(t, dir, "notify.yaml", "notify:\n", "notify:\n  targets:\n    hook: {url: \"https://hooks.example.net/api/webhooks/123/tok-path-secret?token=tok-query-secret\", native: true}\n")
	preview, err := s.PreviewImport(ctx, ImportRequest{Dir: dir, Base: base, Actor: "admin",
		ExpectedGeneration: initial.Generation, Lookup: fixtureLookup})
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"export": string(b), "preview": preview.YAML} {
		if strings.Contains(text, "tok-") {
			t.Errorf("%s keeps a webhook token:\n%s", name, text)
		}
		if !strings.Contains(text, "hooks.example.net") {
			t.Errorf("%s dropped the webhook host a reviewer needs", name)
		}
	}
}

// Any other URL in the export loses its user information and query values: validation refuses most of them
// already, and the export does not depend on that.
func TestExportRedactsURLCredentialsAnywhere(t *testing.T) {
	t.Parallel()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(`a:
  url: http://user:tok-password@host.example:8080/path?apikey=tok-query&x=1
  list:
    - https://other.example/feed?passkey=tok-list
  name: not a url
  plain: https://plain.example/api
`), &doc); err != nil {
		t.Fatal(err)
	}
	redactExport(&doc)
	out, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, want := range []string{"host.example:8080/path", "other.example/feed", "not a url", "https://plain.example/api"} {
		if !strings.Contains(text, want) {
			t.Errorf("redaction lost %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "tok-") || strings.Contains(text, "user") {
		t.Errorf("redaction kept a URL credential:\n%s", text)
	}
}

// An import keeps each existing source's enabled state and indexer priority: the policy files have no fields for
// them, so an import can never set them explicitly. New sources are enabled, as at bootstrap.
func TestImportKeepsEnabledAndPriority(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, p := setup(t)
	initial := bootstrap(t, s, p)
	req := saveRequest(p, initial)
	for i := range req.Records.Indexers {
		if req.Records.Indexers[i].Key == "dmm" {
			req.Records.Indexers[i].Enabled, req.Records.Indexers[i].Priority = false, 7
		}
		if req.Records.Indexers[i].Key == "torrentio" {
			req.Records.Indexers[i].Priority = 3
		}
	}
	for i := range req.Records.Providers {
		if req.Records.Providers[i].Key == "ad" {
			req.Records.Providers[i].Enabled = false
		}
	}
	edited, err := s.Save(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	dir := copyFixture(t)
	editFixture(t, dir, "indexers.yaml", "page_cap: 200", "page_cap: 99")
	imp := ImportRequest{Dir: dir, Base: p, Actor: "admin", ExpectedGeneration: edited.Generation, Lookup: fixtureLookup}
	check := func(name string, records Records) {
		t.Helper()
		for _, r := range records.Indexers {
			switch r.Key {
			case "dmm":
				if r.Enabled || r.Priority != 7 {
					t.Errorf("%s: dmm enabled=%t priority=%d, want disabled at 7", name, r.Enabled, r.Priority)
				}
			case "torrentio":
				if !r.Enabled || r.Priority != 3 {
					t.Errorf("%s: torrentio enabled=%t priority=%d, want enabled at 3", name, r.Enabled, r.Priority)
				}
			}
		}
		for _, r := range records.Providers {
			if r.Key == "ad" && r.Enabled {
				t.Errorf("%s: import re-enabled provider ad", name)
			}
		}
	}
	preview, err := s.PreviewImport(ctx, imp)
	if err != nil {
		t.Fatal(err)
	}
	check("preview", preview.After)
	imported, err := confirmedImport(t, s, imp)
	if err != nil {
		t.Fatal(err)
	}
	check("import", imported.Records)
	if imported.Policy.Indexers["dmm"].PageCap != 99 {
		t.Fatal("import did not apply the file's change")
	}
}
