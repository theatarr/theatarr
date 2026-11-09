package settings

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

func TestUnknownCatalogDoesNotExposePasswordSetting(t *testing.T) {
	t.Parallel()
	s, _, p := setup(t)
	p.Cardigann = &policy.Cardigann{Definitions: &policy.CardigannDefinitions{}}
	initial := bootstrap(t, s, p)
	s.SetCardigannSecrets(func(string) (map[string]bool, bool) { return nil, false })
	m, err := NewRuntimeCoordinator(s, p, initial, nil, func(context.Context, *Snapshot, *Runtime) (any, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	req := saveRequest(p, initial)
	req.Records.Indexers = append(req.Records.Indexers, Indexer{Record: Record{Key: "newsite"}, Enabled: true,
		Config: policy.Indexer{Kind: "cardigann", Definition: "site", URL: "https://site.example/", Privacy: policy.PrivacyPublic,
			Budget: "newsite", Settings: map[string]string{"pass": "invented-password"}}})
	req.Records.Budgets = append(req.Records.Budgets, Budget{Record: Record{Key: "newsite"}, Config: policy.Budget{Requests: 10, Per: policy.Duration(time.Minute)}})
	saved, err := m.SaveAndApply(context.Background(), req)
	if err != nil {
		var validation *policy.Errors
		if !errors.As(err, &validation) || !strings.Contains(err.Error(), "indexers.newsite.settings.pass") || strings.Contains(err.Error(), "invented-password") {
			t.Fatalf("wrong refusal: %v", err)
		}
		if _, err := s.Save(context.Background(), req); err == nil || !strings.Contains(err.Error(), "indexers.newsite.settings.pass") {
			t.Fatalf("direct save accepted unverified settings: %v", err)
		}
		loaded, err := s.Load(context.Background(), p)
		if err != nil || loaded.Generation != initial.Generation {
			t.Fatalf("refusal changed stored generation: %v", err)
		}
		return
	}
	// Loading the definition afterwards does not reject or scrub the existing value.
	s.SetCardigannSecrets(func(string) (map[string]bool, bool) { return map[string]bool{"pass": true}, true })
	saved, err = m.SaveAndApply(context.Background(), saveRequest(p, saved))
	if err != nil {
		t.Fatalf("catalog-loaded resave: %v", err)
	}
	exported, err := saved.Export()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := s.Audit(context.Background(), 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	auditJSON, _ := json.Marshal(entries)
	if strings.Contains(string(exported), "invented-password") || strings.Contains(string(auditJSON), "invented-password") {
		t.Fatalf("password-type setting saved without definition verification: export exposes=%t, audit exposes=%t",
			strings.Contains(string(exported), "invented-password"), strings.Contains(string(auditJSON), "invented-password"))
	}
	t.Fatal("unverified plain setting was persisted")
}

// Simulate values persisted by an older build while the definition was unavailable.
func TestLegacyCardigannSettingsMasked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db, p := setup(t)
	p.Cardigann = &policy.Cardigann{Definitions: &policy.CardigannDefinitions{}}
	initial := bootstrap(t, s, p)
	req := saveRequest(p, initial)
	req.Records.Indexers = append(req.Records.Indexers, Indexer{Record: Record{Key: "legacy"}, Config: policy.Indexer{Kind: "cardigann", Definition: "site", URL: "https://site.example/", Privacy: policy.PrivacyPublic, Budget: "legacy"}})
	req.Records.Budgets = append(req.Records.Budgets, Budget{Record: Record{Key: "legacy"}, Config: policy.Budget{Requests: 10, Per: policy.Duration(time.Minute)}})
	if _, err := s.Save(ctx, req); err != nil {
		t.Fatal(err)
	}
	password := "invented-legacy-pass"
	if _, err := db.Writer().ExecContext(ctx, `UPDATE setting_indexer SET config=json_set(config,'$.Settings',json(?)) WHERE key='legacy'`, `{"pass":"invented-legacy-pass","sort":"seeders"}`); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(row{Entity: "indexers", Kind: "cardigann", Record: Record{Key: "legacy"}, Config: json.RawMessage(`{"Definition":"site","Settings":{"pass":"invented-legacy-pass","sort":"seeders"}}`)})
	if _, err := db.Writer().ExecContext(ctx, `INSERT INTO settings_audit(actor,created_at,generation,entity,key,operation,before,after) VALUES('old',0,1,'indexers','legacy','create','null',?)`, string(raw)); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	check := func(snap *Snapshot, known bool) {
		t.Helper()
		exported, err := snap.Export()
		if err != nil {
			t.Fatal(err)
		}
		_, filesPolicy, err := snap.ExportFiles()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(exported), password) || filesPolicy.Indexers["legacy"].Settings["pass"] != "[redacted]" {
			t.Fatal("legacy credential exposed in export")
		}
		if known && filesPolicy.Indexers["legacy"].Settings["sort"] != "seeders" {
			t.Fatal("verified non-secret value lost")
		}
		entries, err := s.Audit(ctx, 0, 1000)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := json.Marshal(entries)
		if strings.Contains(string(out), password) || strings.Contains(string(out), "seeders") {
			t.Fatal("legacy settings exposed in audit")
		}
		if snap.Policy.Indexers["legacy"].Settings["pass"] != password {
			t.Fatal("masking changed the runtime setting")
		}
	}
	check(loaded, false)
	s.SetCardigannSecrets(func(string) (map[string]bool, bool) { return map[string]bool{"pass": true, "sort": false}, true })
	check(loaded, true) // Catalog arrival also protects a snapshot already loaded.
	again, err := s.Save(ctx, saveRequest(p, loaded))
	if err != nil {
		t.Fatalf("unchanged legacy settings must remain saveable: %v", err)
	}
	check(again, true)
	req = saveRequest(p, again)
	editIndexer(&req.Records, "legacy", func(ix *Indexer) {
		ix.Config.Settings = map[string]string{"pass": "new-invented-pass", "sort": "seeders"}
	})
	if _, err := s.Save(ctx, req); err == nil || !strings.Contains(err.Error(), "settings.pass") {
		t.Fatalf("changed password accepted: %v", err)
	}
}

func TestCardigannNewSettingsNeedDeclaration(t *testing.T) {
	t.Parallel()
	s := &Store{}
	next := &policy.Policy{Indexers: map[string]*policy.Indexer{"site": {Kind: "cardigann", Definition: "site", Settings: map[string]string{"unlisted": "invented-value"}}}}
	for _, known := range []bool{false, true} {
		s.SetCardigannSecrets(func(string) (map[string]bool, bool) { return map[string]bool{"sort": false}, known })
		err := s.refuseCardigannSecretSettings(nil, next)
		if err == nil || !strings.Contains(err.Error(), "settings.unlisted") || strings.Contains(err.Error(), "invented-value") {
			t.Fatalf("unverified setting: %v", err)
		}
	}
}
