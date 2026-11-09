package web

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
)

// Reload carries the orphan confirmation to the coordinator, as save and import do; omitted, it is not confirmed.
func TestSettingsAPIReloadPassesOrphanConfirmation(t *testing.T) {
	t.Parallel()
	var seen []bool
	se := newSettingsEnv(t, nil, func(c *Config) {
		c.ReloadSettings = func(ctx context.Context, _ int64) error {
			seen = append(seen, settings.OrphanActiveDownloads(ctx))
			return nil
		}
	})
	gen := se.generation()
	se.want(se.call("POST", "/reload", map[string]any{"generation": gen}), http.StatusOK, "reload")
	se.want(se.call("POST", "/reload", map[string]any{"generation": gen, "orphan_active_downloads": true}), http.StatusOK,
		"confirmed reload")
	if len(seen) != 2 || seen[0] || !seen[1] {
		t.Fatalf("orphan confirmation seen by the coordinator: %v", seen)
	}
}

func recordByKey(t *testing.T, out map[string]any, entity, key string) map[string]any {
	t.Helper()
	list, _ := out[entity].([]any)
	for _, r := range list {
		if m := r.(map[string]any); m["key"] == key {
			return m
		}
	}
	t.Fatalf("no %s %s in %v", entity, key, out)
	return nil
}

// secret_set reports the credential a record actually resolves to: a never-set record with its environment fallback
// available is set; an explicit clear is unset and stays unset, whatever the environment holds.
func TestSettingsAPISecretSetReportsEffectiveCredential(t *testing.T) {
	t.Parallel()
	se := newSettingsEnv(t, nil, nil)
	if _, err := se.st.Writer().Exec(`UPDATE setting_indexer SET secret_id = NULL WHERE key = 'prowlarr-public'`); err != nil {
		t.Fatal(err)
	}
	out := se.want(se.call("GET", "", nil), http.StatusOK, "overview")
	if rec := recordByKey(t, out, entityIndexers, "prowlarr-public"); rec["secret_set"] != true {
		t.Fatalf("fallback credential reported as %v", rec["secret_set"])
	}
	gen := se.generation()
	cleared := se.want(se.call("PUT", fmt.Sprintf("/indexers/%d", se.id(entityIndexers, "prowlarr-public")),
		map[string]any{"generation": gen, "clear_secret": true}), http.StatusOK, "clear")
	if rec := cleared["record"].(map[string]any); rec["secret_set"] != false {
		t.Fatalf("cleared credential reported as %v", rec["secret_set"])
	}
	out = se.want(se.call("GET", "", nil), http.StatusOK, "overview after clear")
	if rec := recordByKey(t, out, entityIndexers, "prowlarr-public"); rec["secret_set"] != false {
		t.Fatalf("cleared credential reported as %v after reload", rec["secret_set"])
	}
	snap, err := se.coord.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := snap.WithSecret(entityIndexers, "prowlarr-public", func(string) (string, bool) { return "from-environment", true },
		func(v string) error {
			if v != "" {
				t.Error("cleared credential resolved through the environment")
			}
			return nil
		}); err != nil {
		t.Fatal(err)
	}
}

// The import preview keeps existing enabled state and priority, and names the fields each update changes.
func TestSettingsAPIImportPreviewKeepsStateAndNamesFields(t *testing.T) {
	t.Parallel()
	se := newSettingsEnv(t, nil, nil)
	gen := se.generation()
	se.want(se.call("PUT", fmt.Sprintf("/indexers/%d", se.id(entityIndexers, "dmm")), map[string]any{"generation": gen,
		"enabled": false, "priority": 4}), http.StatusOK, "disable dmm")
	gen++
	path := filepath.Join(se.dir, "indexers.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "page_cap: 200") {
		t.Fatal("fixture has no page_cap: 200")
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(b), "page_cap: 200", "page_cap: 99", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	out := se.want(se.call("POST", "/import/preview", map[string]any{"generation": gen}), http.StatusOK, "preview")
	after := out["after"].(map[string]any)
	if rec := recordByKey(t, after, entityIndexers, "dmm"); rec["enabled"] != false || rec["priority"] != float64(4) {
		t.Fatalf("preview resets dmm: %v", rec)
	}
	var dmm map[string]any
	for _, c := range out["changes"].([]any) {
		if m := c.(map[string]any); m["entity"] == entityIndexers && m["key"] == "dmm" {
			dmm = m
		}
	}
	if dmm == nil || fmt.Sprint(dmm["fields"]) != "[config]" {
		t.Fatalf("dmm change does not name its fields: %v", out["changes"])
	}
	body := map[string]any{"generation": gen, "orphan_active_downloads": false}
	proof := se.want(se.call("POST", "/import/confirm", body), 200, "confirm import")
	body["confirmation_token"] = proof["token"]

	se.want(se.call("POST", "/import", body), http.StatusOK, "import")
	snap, err := se.coord.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range snap.Records.Indexers {
		if r.Key == "dmm" && (r.Enabled || r.Priority != 4 || r.Config.PageCap != 99) {
			t.Fatalf("import reset dmm: %+v", r)
		}
	}
}

// GET /settings/export and the import preview never carry a webhook URL's token.
func TestSettingsAPIExportRedactsWebhookURL(t *testing.T) {
	t.Parallel()
	se := newSettingsEnvBase(t, nil, func(p *policy.Policy) {
		p.Notify.Targets = map[string]*policy.NotifyTarget{
			"hook": {URL: "https://hooks.example.net/api/webhooks/123/tok-path-secret?token=tok-query-secret", Native: true},
		}
	}, nil)
	rec := se.call("GET", "/export", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body.String())
	}
	// The stored generation has no notify section, so the import takes it, with the same target, from the files.
	path := filepath.Join(se.dir, "notify.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b = []byte(strings.Replace(string(b), "notify:\n", "notify:\n  targets:\n    hook: {url: \"https://hooks.example.net/api/webhooks/123/tok-path-secret?token=tok-query-secret\", native: true}\n", 1))
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	se.want(se.call("POST", "/import/preview", map[string]any{"generation": se.generation()}), http.StatusOK, "preview")
	for i, body := range se.bodies {
		if strings.Contains(body, "tok-") || !strings.Contains(body, "hooks.example.net") {
			t.Fatalf("response %d keeps a webhook token or lost its host:\n%s", i, body)
		}
	}
}
