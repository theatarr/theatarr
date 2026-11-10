package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"go.yaml.in/yaml/v3"
)

// seededSectionsEnv is a settings database the serve bootstrap has reached: the switches, sections and views are
// seeded from the example policy files.
func seededSectionsEnv(t *testing.T) *settingsEnv {
	t.Helper()
	return seededSectionsEnvBase(t, nil)
}

// seededSectionsEnvBase is seededSectionsEnv with base changing the YAML policy before the settings bootstrap.
func seededSectionsEnvBase(t *testing.T, base func(*policy.Policy)) *settingsEnv {
	t.Helper()
	return newSettingsEnvBase(t, nil, base, func(c *Config) {
		ctx := context.Background()
		st, err := settings.New(c.Store)
		if err != nil {
			t.Fatal(err)
		}
		st.SetLookup(func(name string) (string, bool) { return "fixture-" + name, true })
		if _, err := st.BootstrapSwitches(ctx, c.Policy, "test", nil); err != nil {
			t.Fatal(err)
		}
		initial, err := st.BootstrapSections(ctx, c.Policy, "test", nil)
		if err != nil {
			t.Fatal(err)
		}
		c.Settings, err = settings.NewRuntimeCoordinator(st, c.Policy, initial, fakeRuntime{},
			func(context.Context, *settings.Snapshot, *settings.Runtime) (any, error) { return fakeRuntime{}, nil })
		if err != nil {
			t.Fatal(err)
		}
	})
}

func findSection(t *testing.T, read map[string]any, key string) map[string]any {
	t.Helper()
	for _, s := range read["sections"].([]any) {
		if m := s.(map[string]any); m["key"] == key {
			return m
		}
	}
	t.Fatalf("no section %s in %v", key, read)
	return nil
}

func TestSettingsSectionsAPI(t *testing.T) {
	t.Parallel()
	se := seededSectionsEnv(t)
	read := se.want(se.call("GET", "/sections", nil), 200, "read")
	if read["source"] != "db" {
		t.Fatalf("source %v", read["source"])
	}
	library := findSection(t, read, "library")
	if library["live"] != true || !strings.Contains(library["yaml"].(string), "link_path: /mnt/debrid") {
		t.Fatalf("library section: %v", library)
	}
	if strings.Contains(library["yaml"].(string), "roots: [/media/movies]") || strings.Contains(library["yaml"].(string), "enabled") {
		t.Fatalf("library shows fields owned elsewhere: %s", library["yaml"])
	}
	secrets := library["secrets"].([]any)
	if len(secrets) != 1 || secrets[0].(map[string]any)["path"] != "plex.token" || secrets[0].(map[string]any)["set"] != false ||
		secrets[0].(map[string]any)["env"] != "THEATARR_PLEX_TOKEN" {
		t.Fatalf("library secrets: %v", secrets)
	}
	if egress := findSection(t, read, "egress"); egress["live"] != true {
		t.Fatalf("egress not live: %v", egress)
	}
	gen := int64(read["generation"].(float64))

	// A credential alone: write-only, no confirmation, and live like every section, so no restart is named.
	saved := se.want(se.call("PUT", "/sections/library", map[string]any{"generation": gen, "secrets": map[string]string{"plex.token": testSecret}}),
		200, "set token")
	if saved["restart_required"] != false {
		t.Fatalf("restart named for a live section: %v", saved)
	}
	gen = int64(saved["generation"].(float64))
	library = findSection(t, se.want(se.call("GET", "/sections", nil), 200, "reread"), "library")
	if library["secrets"].([]any)[0].(map[string]any)["set"] != true {
		t.Fatalf("token not set: %v", library["secrets"])
	}
	se.want(se.call("PUT", "/sections/library", map[string]any{"generation": gen, "secrets": map[string]string{"plex.url": testSecret}}),
		422, "not a credential path")

	// Moving where links point needs a confirmation naming it.
	yaml := strings.Replace(library["yaml"].(string), "link_path: /mnt/debrid", "link_path: /mnt/other", 1)
	body := map[string]any{"generation": gen, "yaml": yaml}
	refused := se.want(se.call("PUT", "/sections/library", body), 422, "link path needs proof")
	confirmRefusal(t, refused, "where files are read or written: library.mount.link_path /mnt/debrid → /mnt/other")
	body["confirmation_token"] = confirmRefusal(t, refused, "")
	se.want(se.call("PUT", "/sections/library", body), 200, "confirmed")
	snap, err := se.coord.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Policy.Library.Mount.LinkPath != "/mnt/other" {
		t.Fatalf("link path not applied: %q", snap.Policy.Library.Mount.LinkPath)
	}

	// The same validation as the policy files, and fields owned by other editors are refused.
	gen = snap.Generation
	se.want(se.call("PUT", "/sections/library", map[string]any{"generation": gen, "yaml": "mount: {path: relative}\n"}), 422, "invalid")
	se.want(se.call("PUT", "/sections/library", map[string]any{"generation": gen, "yaml": "enabled: true\n"}), 422, "switch")
	se.want(se.call("PUT", "/sections/library", map[string]any{"generation": gen, "yaml": "nonsense: 1\n"}), 422, "unknown field")
	se.want(se.call("PUT", "/sections/nosuch", map[string]any{"generation": gen}), 404, "unknown section")
	se.noSecretAnywhere()
}

func TestSettingsViewsAPI(t *testing.T) {
	t.Parallel()
	se := seededSectionsEnv(t)
	gen := se.generation()
	create := map[string]any{"generation": gen, "key": "radarr2", "flavour": "radarr", "path": "/radarr2",
		"versions": []string{"movie-web"}, "api_key": testSecret}
	create["confirmation_token"] = confirmRefusal(t, se.want(se.call("POST", "/view-bindings", create), 422, "create needs proof"),
		"Create view radarr2 at /radarr2")
	saved := se.want(se.call("POST", "/view-bindings", create), 200, "create")
	gen = int64(saved["generation"].(float64))
	snap, _ := se.coord.Snapshot(context.Background())
	view := snap.Policy.Views["radarr2"]
	if view == nil || view.Path != "/radarr2" || view.Flavour != "radarr" {
		t.Fatalf("view not applied: %+v", view)
	}
	if se.storedSecret("views", "radarr2") != testSecret {
		t.Fatal("view key not stored")
	}
	se.want(se.call("POST", "/view-bindings", create), 409, "duplicate")

	// A new key changes who has access: named, never shown.
	update := map[string]any{"generation": gen, "api_key": "another-" + testSecret}
	update["confirmation_token"] = confirmRefusal(t, se.want(se.call("PUT", "/view-bindings/radarr2", update), 422, "key change needs proof"),
		"answers a different API key")
	gen = int64(se.want(se.call("PUT", "/view-bindings/radarr2", update), 200, "key change")["generation"].(float64))
	se.want(se.call("PUT", "/view-bindings/radarr2", map[string]any{"generation": gen, "key": "other"}), 400, "rename")

	listed := se.call("GET", "/view-bindings", nil)
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), testSecret) {
		t.Fatalf("list: %d %s", listed.Code, listed.Body.String())
	}
	se.want(se.call("DELETE", "/view-bindings/radarr2?generation="+itoa(gen), nil), 200, "delete")
	snap, _ = se.coord.Snapshot(context.Background())
	if snap.Policy.Views["radarr2"] != nil {
		t.Fatal("view not deleted")
	}
	se.want(se.call("DELETE", "/view-bindings/radarr2?generation="+itoa(snap.Generation), nil), 404, "delete again")
	se.noSecretAnywhere()
}

// Notify target URLs carry their credential in the path or query, so reads (and MCP, which uses the same
// handler) and the audit show only scheme and host; the mask sent back unchanged keeps the stored URL.
func TestSettingsSectionsMaskWebhookURLs(t *testing.T) {
	t.Parallel()
	const hook = "https://hook.example/api/webhooks/1/webhook-path-token-0123456789"
	se := seededSectionsEnv(t)
	read := se.want(se.call("GET", "/sections", nil), 200, "read")
	notify := findSection(t, read, "notify")
	config, _ := notify["config"].(map[string]any)
	if config == nil {
		config = map[string]any{}
	}
	config["targets"] = map[string]any{"hook": map[string]any{"url": hook, "native": true}}
	body := map[string]any{"generation": se.generation(), "config": config}
	refused := se.want(se.call("PUT", "/sections/notify", body), 422, "new host needs proof")
	body["confirmation_token"] = confirmRefusal(t, refused, "notify.targets.hook.url")
	se.want(se.call("PUT", "/sections/notify", body), 200, "confirmed")

	read = se.want(se.call("GET", "/sections", nil), 200, "reread")
	notify = findSection(t, read, "notify")
	shown, _ := json.Marshal(notify)
	if strings.Contains(string(shown), "webhook-path-token") || !strings.Contains(notify["yaml"].(string), "https://hook.example/[redacted]") {
		t.Fatalf("read shows the webhook URL: %s", shown)
	}
	if mcp, _ := json.Marshal(se.mcpTool("settings_sections", map[string]any{}, 200)); strings.Contains(string(mcp), "webhook-path-token") {
		t.Fatal("MCP shows the webhook URL")
	}
	// The YAML sent back unchanged, and the config sent back with another change, keep the stored URL.
	se.want(se.call("PUT", "/sections/notify", map[string]any{"generation": se.generation(), "yaml": notify["yaml"]}), 200, "yaml round trip")
	config = notify["config"].(map[string]any)
	config["retries"] = 7
	se.want(se.call("PUT", "/sections/notify", map[string]any{"generation": se.generation(), "config": config}), 200, "config round trip")
	snap, err := se.coord.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Policy.Notify.Targets["hook"].URL; got != hook {
		t.Fatalf("stored URL replaced by %q", got)
	}
	// A mask under another target name is refused rather than stored or moved.
	config = findSection(t, se.want(se.call("GET", "/sections", nil), 200, "read"), "notify")["config"].(map[string]any)
	targets := config["targets"].(map[string]any)
	targets["other"] = targets["hook"]
	delete(targets, "hook")
	out := se.want(se.call("PUT", "/sections/notify", map[string]any{"generation": se.generation(), "config": config}), 422, "moved mask")
	if !strings.Contains(out["error"].(string), "masked") {
		t.Fatalf("moved mask: %v", out)
	}
	var trail string
	if err := se.st.Reader().QueryRow(`SELECT group_concat(before || after, ' ') FROM settings_audit WHERE entity = 'sections'`).Scan(&trail); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(trail, "webhook-path-token") || !strings.Contains(trail, "hook.example") {
		t.Fatalf("audit keeps the webhook URL: %s", trail)
	}
}

// Saving the notify section as it was read, as config and as YAML, stores no warnings key: only a target that turns
// the option on has it, so a build from before the option still loads the settings.
func TestSettingsNotifySaveAddsNoWarningsKey(t *testing.T) {
	t.Parallel()
	se := seededSectionsEnvBase(t, func(p *policy.Policy) {
		if p.Notify == nil {
			p.Notify = &policy.Notify{}
		}
		p.Notify.Targets = map[string]*policy.NotifyTarget{
			"chan": {URL: "https://discord.example/api/webhooks/1/token-0123456789", Format: policy.NotifyFormatDiscord},
			"hook": {URL: "http://192.168.1.50:8080/hook", Native: true},
		}
	})
	stored := func() string {
		t.Helper()
		var raw string
		if err := se.st.Reader().QueryRow(`SELECT config FROM setting_section WHERE key = 'notify'`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if raw := stored(); strings.Contains(raw, "warnings") {
		t.Fatalf("seeded with a warnings key: %s", raw)
	}
	notify := findSection(t, se.want(se.call("GET", "/sections", nil), 200, "read"), "notify")
	if shown, _ := json.Marshal(notify); strings.Contains(string(shown), "warnings") {
		t.Fatalf("read shows a warnings key: %s", shown)
	}
	config := notify["config"].(map[string]any)
	se.want(se.call("PUT", "/sections/notify", map[string]any{"generation": se.generation(), "config": config}), 200, "config as read")
	config["retries"] = 7
	se.want(se.call("PUT", "/sections/notify", map[string]any{"generation": se.generation(), "config": config}), 200, "another field")
	notify = findSection(t, se.want(se.call("GET", "/sections", nil), 200, "reread"), "notify")
	se.want(se.call("PUT", "/sections/notify", map[string]any{"generation": se.generation(), "yaml": notify["yaml"]}), 200, "yaml as read")
	if raw := stored(); strings.Contains(raw, "warnings") || !strings.Contains(raw, `"retries":7`) {
		t.Fatalf("stored after untouched saves: %s", raw)
	}

	// On for the Discord target; refused on the webhook target.
	config = findSection(t, se.want(se.call("GET", "/sections", nil), 200, "read"), "notify")["config"].(map[string]any)
	targets := config["targets"].(map[string]any)
	targets["hook"].(map[string]any)["warnings"] = true
	se.want(se.call("PUT", "/sections/notify", map[string]any{"generation": se.generation(), "config": config}), 422, "warnings on a webhook target")
	delete(targets["hook"].(map[string]any), "warnings")
	targets["chan"].(map[string]any)["warnings"] = true
	se.want(se.call("PUT", "/sections/notify", map[string]any{"generation": se.generation(), "config": config}), 200, "warnings on")
	snap, err := se.coord.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Policy.Notify.Targets["chan"].Warnings || snap.Policy.Notify.Targets["hook"].Warnings {
		t.Fatalf("warnings not applied: %+v", snap.Policy.Notify.Targets)
	}
	if raw := stored(); strings.Count(raw, `"warnings":true`) != 1 {
		t.Fatalf("warnings on is not stored: %s", raw)
	}

	// Off again, as config and as YAML: the key is gone rather than stored as false, and a build whose target type
	// has no warnings field decodes the stored section strictly. A field that is not optional keeps its false.
	fields := reflect.VisibleFields(reflect.TypeOf(policy.NotifyTarget{}))
	fields = slices.DeleteFunc(fields, func(f reflect.StructField) bool { return f.Name == "Warnings" })
	older := reflect.StructOf([]reflect.StructField{{Name: "Targets", Type: reflect.MapOf(reflect.TypeOf(""), reflect.StructOf(fields)), Tag: `yaml:"targets"`}})
	olderLoads := func() error {
		section := map[string]any{}
		if err := json.Unmarshal([]byte(stored()), &section); err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(map[string]any{"targets": section["targets"]})
		if err != nil {
			t.Fatal(err)
		}
		dec := yaml.NewDecoder(bytes.NewReader(b))
		dec.KnownFields(true)
		return dec.Decode(reflect.New(older).Interface())
	}
	if err := olderLoads(); err == nil {
		t.Fatal("the older target type accepts a warnings key, so it proves nothing")
	}
	off := func(what string) {
		t.Helper()
		raw := stored()
		if strings.Contains(raw, "warnings") || !strings.Contains(raw, `"native":false`) || !strings.Contains(raw, `"native":true`) {
			t.Fatalf("stored after warnings off (%s): %s", what, raw)
		}
		if err := olderLoads(); err != nil {
			t.Fatalf("an older build does not load the section after warnings off (%s): %v", what, err)
		}
		snap, err := se.coord.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if snap.Policy.Notify.Targets["chan"].Warnings {
			t.Fatalf("warnings still on (%s)", what)
		}
	}
	targets["chan"].(map[string]any)["warnings"] = false
	se.want(se.call("PUT", "/sections/notify", map[string]any{"generation": se.generation(), "config": config}), 200, "warnings off")
	off("config")
	targets["chan"].(map[string]any)["warnings"] = true
	se.want(se.call("PUT", "/sections/notify", map[string]any{"generation": se.generation(), "config": config}), 200, "warnings on again")
	text := findSection(t, se.want(se.call("GET", "/sections", nil), 200, "read"), "notify")["yaml"].(string)
	if !strings.Contains(text, "warnings: true") {
		t.Fatalf("yaml does not show warnings on: %s", text)
	}
	text = strings.Replace(text, "warnings: true", "warnings: false", 1)
	se.want(se.call("PUT", "/sections/notify", map[string]any{"generation": se.generation(), "yaml": text}), 200, "warnings off as yaml")
	off("yaml")
}
