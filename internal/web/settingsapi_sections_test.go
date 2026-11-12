package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

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

	// Moving where links point is a folder edit: it saves at once.
	yaml := strings.Replace(library["yaml"].(string), "link_path: /mnt/debrid", "link_path: /mnt/other", 1)
	se.want(se.call("PUT", "/sections/library", map[string]any{"generation": gen, "yaml": yaml}), 200, "link path")
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

	// A new key saves at once and is never shown.
	update := map[string]any{"generation": gen, "api_key": "another-" + testSecret}
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
	se.want(se.call("PUT", "/sections/notify", body), 200, "new target")

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

// Saving the router section as it was read, as config and as YAML, stores no retry_new key, so a build from before
// the option (it decodes sections strictly) still loads the settings, and the switch is on by default; turned on it
// is stored, and turned off the key is stored as false (an explicit Off is kept).
func TestSettingsRouterSaveAddsNoRetryNewKey(t *testing.T) {
	t.Parallel()
	se := seededSectionsEnvBase(t, func(p *policy.Policy) {
		if p.Router == nil {
			p.Router = &policy.Router{}
		}
		p.Router.Wanted = &policy.Wanted{PerHour: 20}
	})
	stored := func() string {
		t.Helper()
		var raw string
		if err := se.st.Reader().QueryRow(`SELECT config FROM setting_section WHERE key = 'router'`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	retryNew := func() bool {
		t.Helper()
		snap, err := se.coord.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return snap.Policy.WantedSettings().RetryNew
	}
	if raw := stored(); strings.Contains(raw, "retry_new") || !strings.Contains(raw, `"per_hour":20`) {
		t.Fatalf("seeded: %s", raw)
	}
	router := findSection(t, se.want(se.call("GET", "/sections", nil), 200, "read"), "router")
	if shown, _ := json.Marshal(router); strings.Contains(string(shown), "retry_new") {
		t.Fatalf("read shows a retry_new key: %s", shown)
	}
	config := router["config"].(map[string]any)
	wanted := config["wanted"].(map[string]any)
	se.want(se.call("PUT", "/sections/router", map[string]any{"generation": se.generation(), "config": config}), 200, "config as read")
	wanted["per_hour"] = 30
	se.want(se.call("PUT", "/sections/router", map[string]any{"generation": se.generation(), "config": config}), 200, "another field")
	router = findSection(t, se.want(se.call("GET", "/sections", nil), 200, "reread"), "router")
	se.want(se.call("PUT", "/sections/router", map[string]any{"generation": se.generation(), "yaml": router["yaml"]}), 200, "yaml as read")
	if raw := stored(); strings.Contains(raw, "retry_new") || !strings.Contains(raw, `"per_hour":30`) {
		t.Fatalf("stored after untouched saves: %s", raw)
	}
	// The generation snapshots gain no key either; the newest one is checked after each save below.
	snapshots := func(where string) int {
		t.Helper()
		var n int
		if err := se.st.Reader().QueryRow(`SELECT COUNT(*) FROM settings_generation WHERE (snapshot LIKE '%retry_new%'
			OR snapshot LIKE '%RetryNew%')` + where).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	const newest = ` AND generation = (SELECT MAX(generation) FROM settings_generation)`
	if n := snapshots(""); n != 0 {
		t.Fatalf("%d generation snapshots name retry_new after untouched saves", n)
	}
	if !retryNew() {
		t.Fatal("retry_new is off by default")
	}

	// A build whose Wanted has no retry_new field decodes the stored section strictly.
	fields := reflect.VisibleFields(reflect.TypeOf(policy.Wanted{}))
	fields = slices.DeleteFunc(fields, func(f reflect.StructField) bool { return f.Name == "RetryNew" })
	if len(fields) != reflect.TypeOf(policy.Wanted{}).NumField()-1 {
		t.Fatal("policy.Wanted has no RetryNew field")
	}
	older := reflect.StructOf(fields)
	olderLoads := func() error {
		section := map[string]any{}
		if err := json.Unmarshal([]byte(stored()), &section); err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(section["wanted"])
		if err != nil {
			t.Fatal(err)
		}
		dec := yaml.NewDecoder(bytes.NewReader(b))
		dec.KnownFields(true)
		return dec.Decode(reflect.New(older).Interface())
	}
	if err := olderLoads(); err != nil {
		t.Fatalf("an older build does not load the untouched section: %v", err)
	}

	wanted["retry_new"] = true
	se.want(se.call("PUT", "/sections/router", map[string]any{"generation": se.generation(), "config": config}), 200, "retry_new on")
	if raw := stored(); strings.Count(raw, `"retry_new":true`) != 1 || !retryNew() {
		t.Fatalf("retry_new on is not stored or applied: %s", raw)
	}
	if err := olderLoads(); err == nil {
		t.Fatal("the older type accepts a retry_new key, so it proves nothing")
	}
	if n := snapshots(newest); n != 1 {
		t.Fatal("the generation snapshot with retry_new on does not name it, so the snapshot checks prove nothing")
	}
	off := func(what string) {
		t.Helper()
		raw := stored()
		if strings.Count(raw, `"retry_new":false`) != 1 || !strings.Contains(raw, `"per_hour":30`) {
			t.Fatalf("stored after retry_new off (%s): %s", what, raw)
		}
		if retryNew() {
			t.Fatalf("retry_new still on (%s)", what)
		}
		if n := snapshots(newest); n != 1 {
			t.Fatalf("the newest generation snapshot does not name retry_new after it was turned off (%s)", what)
		}
	}
	wanted["retry_new"] = false
	se.want(se.call("PUT", "/sections/router", map[string]any{"generation": se.generation(), "config": config}), 200, "retry_new off")
	off("config")
	wanted["retry_new"] = true
	se.want(se.call("PUT", "/sections/router", map[string]any{"generation": se.generation(), "config": config}), 200, "retry_new on again")
	text := findSection(t, se.want(se.call("GET", "/sections", nil), 200, "read"), "router")["yaml"].(string)
	if !strings.Contains(text, "retry_new: true") {
		t.Fatalf("yaml does not show retry_new on: %s", text)
	}
	text = strings.Replace(text, "retry_new: true", "retry_new: false", 1)
	se.want(se.call("PUT", "/sections/router", map[string]any{"generation": se.generation(), "yaml": text}), 200, "retry_new off as yaml")
	off("yaml")
}

// A library created through the libraries editor is not named in the stored library section (its roots live with
// the library), so Plex & playback sends versions.<key>.plex_section for a key the section never held. The save
// is accepted and the running policy scans that Plex section, with the library's roots untouched.
func TestSettingsLibrarySectionTakesPlexSectionForAnUnlistedLibrary(t *testing.T) {
	t.Parallel()
	se := seededSectionsEnv(t)
	ctx := context.Background()
	put := func(config map[string]any, gen int64, what string) int64 {
		t.Helper()
		body := map[string]any{"generation": gen, "config": config}
		rec := se.call("PUT", "/sections/library", body)
		if rec.Code == 422 {
			var refused map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &refused); err != nil {
				t.Fatal(err)
			}
			if _, ok := refused["confirmation"]; ok {
				body["confirmation_token"] = confirmRefusal(t, refused, "")
				rec = se.call("PUT", "/sections/library", body)
			}
		}
		return int64(se.want(rec, 200, what)["generation"].(float64))
	}
	read := se.want(se.call("GET", "/sections", nil), 200, "read")
	config := findSection(t, read, "library")["config"].(map[string]any)
	// The stored section of a fresh install: no versions at all.
	delete(config, "versions")
	gen := put(config, int64(read["generation"].(float64)), "drop the versions")
	snap, err := se.coord.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var key string
	for _, k := range sortedKeys(snap.Policy.Versions) {
		if lv := snap.Policy.Library.Versions[k]; lv != nil && len(lv.Roots) > 0 {
			key = k
			break
		}
	}
	if key == "" {
		t.Fatal("the fixture has no library with roots")
	}
	roots := slices.Clone(snap.Policy.Library.Versions[key].Roots)
	if snap.Policy.Library.Versions[key].PlexSection != 0 {
		t.Fatalf("%s still has a Plex section after the section dropped it", key)
	}
	if stored := findSection(t, se.want(se.call("GET", "/sections", nil), 200, "reread"), "library")["config"].(map[string]any); stored["versions"] != nil {
		t.Fatalf("the stored section still lists versions: %v", stored["versions"])
	}

	config["versions"] = map[string]any{key: map[string]any{"plex_section": 7}}
	put(config, gen, "set the Plex section")
	if snap, err = se.coord.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	lv := snap.Policy.Library.Versions[key]
	if lv == nil || lv.PlexSection != 7 || !slices.Equal(lv.Roots, roots) {
		t.Fatalf("%s after the save: %+v, want Plex section 7 and roots %v", key, lv, roots)
	}
}

// librarySectionRow is the stored library section, as the database holds it.
func librarySectionRow(t *testing.T, se *settingsEnv) string {
	t.Helper()
	var raw string
	if err := se.st.Reader().QueryRow(`SELECT config FROM setting_section WHERE key = 'library'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

// newestSnapshotLibrary is the library section in the newest generation snapshot. It fails the test when the snapshot
// holds a Go-named Decypharr key anywhere (a policy struct encoded as JSON).
func newestSnapshotLibrary(t *testing.T, se *settingsEnv) string {
	t.Helper()
	var raw string
	if err := se.st.Reader().QueryRow(`SELECT snapshot FROM settings_generation
		WHERE generation = (SELECT MAX(generation) FROM settings_generation)`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, `"Decypharr"`) {
		t.Fatalf("the generation snapshot holds a Decypharr key: %s", raw)
	}
	var snap struct {
		Records struct {
			Sections []struct {
				Key    string          `json:"key"`
				Config json.RawMessage `json:"config"`
			} `json:"sections"`
		} `json:"records"`
	}
	if err := json.Unmarshal([]byte(raw), &snap); err != nil {
		t.Fatal(err)
	}
	for _, s := range snap.Records.Sections {
		if s.Key == "library" {
			return string(s.Config)
		}
	}
	t.Fatalf("no library section in the generation snapshot: %s", raw)
	return ""
}

// olderMountLoads decodes the stored mount strictly into the mount type of a build from before
// library.mount.decypharr existed.
func olderMountLoads(t *testing.T, stored string) error {
	t.Helper()
	fields := reflect.VisibleFields(reflect.TypeOf(policy.LibraryMount{}))
	fields = slices.DeleteFunc(fields, func(f reflect.StructField) bool { return f.Name == "Decypharr" })
	if len(fields) != reflect.TypeOf(policy.LibraryMount{}).NumField()-1 {
		t.Fatal("policy.LibraryMount has no Decypharr field")
	}
	section := map[string]any{}
	if err := json.Unmarshal([]byte(stored), &section); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(section["mount"])
	if err != nil {
		t.Fatal(err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	return dec.Decode(reflect.New(reflect.StructOf(fields)).Interface())
}

// A library section saved without library.mount.decypharr is stored exactly as it was sent, key order included, as
// before the block existed: no decypharr key in the row, the read or the generation snapshot, and a build from before
// the block (it decodes sections strictly) still loads it.
func TestSettingsLibrarySaveWithoutDecypharrKeepsBytes(t *testing.T) {
	t.Parallel()
	se := seededSectionsEnv(t)
	// The seeded section with its keys in reverse order and one value changed (trash.keep_days 30 to 31). The
	// expected row was taken from a build without the block: the row is the config as sent.
	const sent = `{"versions":{"tv-2160p":{"plex_section":0},"movie-web":{"plex_section":2},"movie-remux":{"plex_section":1},"anime-tv":{"plex_section":0}},"trash":{"keep_days":31,"dir":"/media/.trash"},"sample":{"resolution":null,"mislabel":null,"min_runtime_ratio":0.5},"relink_interval":"0s","probe":{"max_bytes":16777216},"plex":{"url":"http://plex:32400","token_env":"THEATARR_PLEX_TOKEN","prewarm_rate":0,"prewarm_max_size":0,"prewarm":null,"now_playing":null,"debounce":"0s","budget":"plex"},"mount":{"zurg_log_interval":"1m0s","zurg_log_budget":"zurg-log","zurg_log":"http://zurg:9999/logs","zurg_api_repair_grace":"2h0m0s","zurg_api_interval":"1m0s","zurg_api_budget":"zurg-api","zurg_api":"http://zurg:9999","wait_timeout":"6m0s","stat_timeout":"0s","rclone_rc":null,"poll_interval":"0s","path":"/mnt/debrid","link_path":"/mnt/debrid","all_dir":"__all__"},"dangling":{"roots":["/media","/downloads"],"interval":"6h0m0s"}}`
	if seeded := librarySectionRow(t, se); strings.Contains(seeded, "decypharr") || !strings.Contains(seeded, `"keep_days":30`) {
		t.Fatalf("seeded: %s", seeded)
	}
	se.want(se.call("PUT", "/sections/library", `{"generation":`+strconv.FormatInt(se.generation(), 10)+`,"config":`+sent+`}`), 200, "save")
	if got := librarySectionRow(t, se); got != sent {
		t.Fatalf("stored row differs from what was sent:\n got %s\nwant %s", got, sent)
	}
	if got := newestSnapshotLibrary(t, se); got != sent {
		t.Fatalf("the generation snapshot's library section differs from what was sent:\n got %s\nwant %s", got, sent)
	}
	library := findSection(t, se.want(se.call("GET", "/sections", nil), 200, "read"), "library")
	if shown, _ := json.Marshal(library); strings.Contains(string(shown), "decypharr") {
		t.Fatalf("read shows a decypharr key: %s", shown)
	}
	if err := olderMountLoads(t, librarySectionRow(t, se)); err != nil {
		t.Fatalf("an older build does not load the section: %v", err)
	}
	snap, err := se.coord.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Policy.Library.Mount.Decypharr != nil || snap.Policy.Library.Trash.KeepDays != 31 {
		t.Fatalf("applied: %+v", snap.Policy.Library)
	}
}

// A library section with library.mount.decypharr set round-trips: it is stored, applied and read back with the block,
// a save of what was read stores the same bytes, and with the key removed again the row has no decypharr key and a
// build from before the block loads it.
func TestSettingsLibraryDecypharrRoundTrip(t *testing.T) {
	t.Parallel()
	se := seededSectionsEnv(t)
	save := func(label string, config map[string]any) {
		t.Helper()
		body := map[string]any{"generation": se.generation(), "config": config}
		rec := se.call("PUT", "/sections/library", body)
		if rec.Code == 422 {
			// A new outbound host or credential name is confirmed, as for any other section.
			body["confirmation_token"] = confirmRefusal(t, se.want(rec, 422, label), "")
			rec = se.call("PUT", "/sections/library", body)
		}
		se.want(rec, 200, label)
	}
	read := func() map[string]any {
		t.Helper()
		return findSection(t, se.want(se.call("GET", "/sections", nil), 200, "read"), "library")
	}
	config := read()["config"].(map[string]any)
	mount := config["mount"].(map[string]any)
	// The block is refused beside zurg's log and API, which the seeded section sets.
	mount["decypharr"] = map[string]any{"url": "http://decypharr:8282"}
	if rec := se.call("PUT", "/sections/library", map[string]any{"generation": se.generation(), "config": config}); rec.Code != 422 ||
		!strings.Contains(rec.Body.String(), "not together with zurg_api or zurg_log") {
		t.Fatalf("decypharr beside zurg: %d %s", rec.Code, rec.Body.String())
	}
	for _, key := range []string{"zurg_api", "zurg_api_budget", "zurg_api_interval", "zurg_api_repair_grace", "zurg_log", "zurg_log_budget", "zurg_log_interval"} {
		delete(mount, key)
	}
	mount["decypharr"] = map[string]any{"url": "http://decypharr:8282/base", "api_token_env": "THEATARR_DECYPHARR_TOKEN",
		"username_env": "THEATARR_DECYPHARR_USER", "password_env": "THEATARR_DECYPHARR_PASS", "interval": "2m", "budget": "plex"}
	save("decypharr set", config)

	const block = `"decypharr":{"api_token_env":"THEATARR_DECYPHARR_TOKEN","budget":"plex","interval":"2m","password_env":"THEATARR_DECYPHARR_PASS","url":"http://decypharr:8282/base","username_env":"THEATARR_DECYPHARR_USER"}`
	stored := librarySectionRow(t, se)
	if !strings.Contains(stored, block) {
		t.Fatalf("stored without the block: %s", stored)
	}
	if err := olderMountLoads(t, stored); err == nil {
		t.Fatal("the older mount type accepts a decypharr key, so it proves nothing")
	}
	if got := newestSnapshotLibrary(t, se); got != stored {
		t.Fatalf("the generation snapshot's library section differs from the row:\n got %s\nwant %s", got, stored)
	}
	snap, err := se.coord.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := policy.LibraryDecypharr{URL: "http://decypharr:8282/base", APITokenEnv: "THEATARR_DECYPHARR_TOKEN",
		UsernameEnv: "THEATARR_DECYPHARR_USER", PasswordEnv: "THEATARR_DECYPHARR_PASS", Interval: policy.Duration(2 * time.Minute), Budget: "plex"}
	if d := snap.Policy.Library.Mount.Decypharr; d == nil || *d != want {
		t.Fatalf("applied: %+v", d)
	}
	hosted := false
	for _, h := range snap.Policy.OutboundHosts() {
		hosted = hosted || h.Host == "decypharr:8282"
	}
	if _, named := snap.Policy.SecretEnvs()["THEATARR_DECYPHARR_TOKEN"]; !hosted || !named {
		t.Fatalf("outbound host %v, secret name %v", hosted, named)
	}

	// Read back: the block and its three credentials are shown, and saving what was read changes nothing.
	library := read()
	shown := library["config"].(map[string]any)["mount"].(map[string]any)["decypharr"].(map[string]any)
	if shown["url"] != "http://decypharr:8282/base" || shown["api_token_env"] != "THEATARR_DECYPHARR_TOKEN" || shown["interval"] != "2m" {
		t.Fatalf("read: %v", shown)
	}
	if !strings.Contains(library["yaml"].(string), "decypharr:\n") {
		t.Fatalf("yaml without the block: %s", library["yaml"])
	}
	paths := map[string]bool{}
	for _, s := range library["secrets"].([]any) {
		paths[s.(map[string]any)["path"].(string)] = true
	}
	for _, path := range []string{"mount.decypharr.api_token", "mount.decypharr.username", "mount.decypharr.password"} {
		if !paths[path] {
			t.Errorf("no credential %s in %v", path, library["secrets"])
		}
	}
	save("as read", library["config"].(map[string]any))
	if again := librarySectionRow(t, se); again != stored {
		t.Fatalf("stored after a save of what was read:\n got %s\nwant %s", again, stored)
	}

	// Removed again: no key in the row, and the older build loads it.
	config = read()["config"].(map[string]any)
	delete(config["mount"].(map[string]any), "decypharr")
	save("decypharr removed", config)
	stored = librarySectionRow(t, se)
	if strings.Contains(stored, "decypharr") {
		t.Fatalf("stored after the block was removed: %s", stored)
	}
	if err := olderMountLoads(t, stored); err != nil {
		t.Fatalf("an older build does not load the section after the block was removed: %v", err)
	}
	if snap, err = se.coord.Snapshot(context.Background()); err != nil || snap.Policy.Library.Mount.Decypharr != nil {
		t.Fatalf("still applied: %v", err)
	}
}
