package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
)

// startEnv is a settings database the serve bootstrap has reached (switches, sections and quality records seeded,
// the switches off), with or without the example's import.arr instances: without them Setup offers the one-step
// start. area is a directory library roots may live under.
func startEnv(t *testing.T, arr bool) (se *settingsEnv, area string) {
	t.Helper()
	area = t.TempDir()
	var coord *settings.RuntimeCoordinator
	se = newSettingsEnvBase(t, nil, func(p *policy.Policy) {
		if !arr {
			p.Import = nil
		}
		p.Library.Dangling.Roots = []string{area}
	}, func(c *Config) {
		ctx := context.Background()
		st, err := settings.New(c.Store)
		if err != nil {
			t.Fatal(err)
		}
		st.SetLookup(func(name string) (string, bool) { return "fixture-" + name, true })
		st.SetTrashRoot("../settings/testdata/trash")
		if _, err := st.BootstrapSwitches(ctx, c.Policy, "test", nil); err != nil {
			t.Fatal(err)
		}
		if _, err := st.BootstrapSections(ctx, c.Policy, "test", nil); err != nil {
			t.Fatal(err)
		}
		initial, err := st.BootstrapQuality(ctx, c.Policy, "test")
		if err != nil {
			t.Fatal(err)
		}
		coord, err = settings.NewRuntimeCoordinator(st, c.Policy, initial, fakeRuntime{},
			func(context.Context, *settings.Snapshot, *settings.Runtime) (any, error) { return fakeRuntime{}, nil })
		if err != nil {
			t.Fatal(err)
		}
		c.Settings = coord
	})
	se.coord = coord
	return se, area
}

// startBody is what Setup sends for a new library key: a movie library on root with the example's sources.
func (se *settingsEnv) startBody(key, root string) map[string]any {
	se.t.Helper()
	config := se.want(se.call("GET", "/versions/movie-web", nil), 200, "template")["record"].(map[string]any)["config"].(map[string]any)
	config["library"], config["mode"], config["grab"] = root, "shadow", false
	delete(config, "golive_done")
	return map[string]any{"generation": se.generation(), "key": key, "roots": []string{root}, "config": config}
}

func (se *settingsEnv) start(body map[string]any) map[string]any {
	se.t.Helper()
	rec := se.setupCall("POST", "/setup/start", se.csrf, mustJSON(se.t, body))
	out := map[string]any{"status": rec.Code}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		se.t.Fatalf("POST setup/start: %d %s", rec.Code, rec.Body.String())
	}
	out["status"] = rec.Code
	return out
}

func (se *settingsEnv) setupState() SetupState {
	se.t.Helper()
	rec := se.setupCall("GET", "/setup", "", "")
	if rec.Code != http.StatusOK {
		se.t.Fatalf("GET setup: %d %s", rec.Code, rec.Body.String())
	}
	return decodeAs[SetupState](se.t, rec.Body.Bytes())
}

// switchesOn reads the stored global switches router and library.
func (se *settingsEnv) switchesOn() (router, library bool) {
	se.t.Helper()
	configured := se.want(se.call("GET", "/switches", nil), 200, "switches")["configured"].(map[string]any)
	return configured["router_enabled"] == true, configured["library_enabled"] == true
}

// POST setup/start on an install no arr feeds: one refusal that lists the activation, the grab switch and both global
// switches with one token, then one generation that holds all of it, whatever mode and grab the body asked for.
func TestSetupStart(t *testing.T) {
	t.Parallel()
	se, area := startEnv(t, false)
	if s := se.setupState(); s.ArrInstances != 0 {
		t.Fatalf("arr_instances %d on an install without import.arr", s.ArrInstances)
	}
	if router, library := se.switchesOn(); router || library {
		t.Fatal("the switches start on: the test proves nothing")
	}
	root := filepath.Join(area, "films")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	body := se.startBody("films", root)
	gen := body["generation"].(int64)
	refused := se.start(body)
	if effects, _ := refused["effects"].([]any); refused["status"] != 422 || len(effects) != 4 {
		t.Fatalf("unconfirmed start: %v", refused)
	}
	// Each thing the save turns on is its own line.
	for _, want := range []string{"Library films becomes active", "Active library films grabs", "Turn on Allow new downloads (router.enabled)", "Turn on Write library links (library.enabled)"} {
		confirmRefusal(t, refused, want)
	}
	if text := fmt.Sprint(refused["effects"]); strings.Contains(text, "notify.enabled") {
		t.Fatalf("the start touches notify: %s", text)
	}
	if router, library := se.switchesOn(); se.generation() != gen || router || library {
		t.Fatal("the refused start changed something")
	}
	body["confirmation_token"] = confirmRefusal(t, refused, root)
	started := se.start(body)
	if started["status"] != 200 || int64(started["generation"].(float64)) != gen+1 || se.generation() != gen+1 ||
		fmt.Sprint(started["forced_off"]) != "[]" {
		t.Fatalf("confirmed start: %v (generation %d, was %d)", started, se.generation(), gen)
	}
	config := se.want(se.call("GET", "/versions/films", nil), 200, "the library")["record"].(map[string]any)["config"].(map[string]any)
	if config["mode"] != "active" || config["grab"] != true || config["profile"] != "web-2160p" {
		t.Fatalf("stored library: %v", config)
	}
	if router, library := se.switchesOn(); !router || !library {
		t.Fatalf("switches after the start: router %v library %v", router, library)
	}
	if on := se.want(se.call("GET", "/switches", nil), 200, "switches")["configured"].(map[string]any)["notify_enabled"]; on != false {
		t.Fatalf("notify_enabled %v: the start must leave it alone", on)
	}
	// The other libraries are as they were.
	if other := se.want(se.call("GET", "/versions/movie-web", nil), 200, "another library")["record"].(map[string]any)["config"].(map[string]any); other["mode"] != "shadow" {
		t.Fatalf("another library changed: %v", other)
	}

	// The same key again, a stale generation, and a request without one.
	again := se.startBody("films", root)
	if out := se.start(again); out["status"] != 400 || !strings.Contains(fmt.Sprint(out["error"]), "already exists") {
		t.Fatalf("existing key: %v", out)
	}
	stale := se.startBody("shows", root)
	stale["generation"] = gen
	if out := se.start(stale); out["status"] != 409 || out["generation"] == nil {
		t.Fatalf("stale generation: %v", out)
	}
	delete(stale, "generation")
	if out := se.start(stale); out["status"] != 400 {
		t.Fatalf("no generation: %v", out)
	}
	// A root that cannot be written (below a regular file, so root may run the test) is named in the refusal.
	file := filepath.Join(area, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(file, "library")
	gen = se.generation()
	if out := se.start(se.startBody("shows", bad)); out["status"] != 422 || out["confirmation"] != nil || !strings.Contains(fmt.Sprint(out), bad) || se.generation() != gen {
		t.Fatalf("unwritable root: %v", out)
	}
}

// The preset variant: the profile and its format set are created with the library, in the same generation and under
// the library's key, from the bundled TRaSH defaults.
func TestSetupStartWithPreset(t *testing.T) {
	t.Parallel()
	se, area := startEnv(t, false)
	root := filepath.Join(area, "films")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	body := se.startBody("films", root)
	body["config"].(map[string]any)["profile"] = ""
	body["preset"] = map[string]any{"flavour": "radarr", "preset": webPreset, "modifiers": []string{}}
	gen := se.generation()
	refused := se.start(body)
	if effects, _ := refused["effects"].([]any); refused["status"] != 422 || len(effects) != 4 {
		t.Fatalf("unconfirmed start: %v", refused)
	}
	body["confirmation_token"] = confirmRefusal(t, refused, "router.enabled")
	if out := se.start(body); out["status"] != 200 || se.generation() != gen+1 {
		t.Fatalf("confirmed start: %v (generation %d, was %d)", out, se.generation(), gen)
	}
	config := se.want(se.call("GET", "/versions/films", nil), 200, "the library")["record"].(map[string]any)["config"].(map[string]any)
	if config["mode"] != "active" || config["grab"] != true || config["profile"] != "films" {
		t.Fatalf("stored library: %v", config)
	}
	snap, err := se.coord.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	profile := snap.Policy.Profiles["films"]
	if profile == nil || profile.FormatSet != "films" || snap.Policy.FormatSets["films"] == nil || len(profile.Formats) == 0 {
		t.Fatalf("profile from the preset: %+v", profile)
	}
	// An unknown preset is refused before anything is saved.
	other := se.startBody("shows", root)
	other["preset"] = map[string]any{"flavour": "radarr", "preset": "no-such-preset"}
	gen = se.generation()
	if out := se.start(other); out["status"] != 422 || out["confirmation"] != nil || se.generation() != gen {
		t.Fatalf("unknown preset: %v", out)
	}
}

// An install that migrates from an arr is refused by the endpoint itself, and nothing is written.
func TestSetupStartRefusedWithArr(t *testing.T) {
	t.Parallel()
	se, area := startEnv(t, true)
	if s := se.setupState(); s.ArrInstances == 0 {
		t.Fatal("arr_instances 0 with import.arr in the example policy")
	}
	gen := se.generation()
	out := se.start(se.startBody("films", area))
	if out["status"] != 409 || !strings.Contains(fmt.Sprint(out["error"]), "migrates from an arr") {
		t.Fatalf("start with import.arr: %v", out)
	}
	if router, library := se.switchesOn(); se.generation() != gen || router || library {
		t.Fatal("the refused start wrote settings")
	}
	if rec := se.call("GET", "/versions/films", nil); rec.Code != 404 {
		t.Fatalf("the refused start created the library: %d", rec.Code)
	}
}

// THEATARR_FORCE_OFF does not refuse the start: the save happens and the answer names the switches it keeps off.
func TestSetupStartForcedOff(t *testing.T) {
	t.Setenv("THEATARR_FORCE_OFF", "router,notify")
	se, area := startEnv(t, false)
	body := se.startBody("films", area)
	refused := se.start(body)
	if refused["status"] != 422 {
		t.Fatalf("unconfirmed start: %v", refused)
	}
	body["confirmation_token"] = confirmRefusal(t, refused, "router.enabled")
	out := se.start(body)
	forced, _ := out["forced_off"].([]any)
	if out["status"] != 200 || !slices.Equal(forced, []any{"router"}) {
		t.Fatalf("forced off: %v", out)
	}
	if router, library := se.switchesOn(); !router || !library {
		t.Fatalf("stored switches: router %v library %v", router, library)
	}
}

// Without the settings service there is nothing to save to: 409, as for an install that migrates from an arr.
func TestSetupStartWithoutSettings(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	r := httptest.NewRequest("POST", APIBase+"/setup/start", strings.NewReader(`{"generation":1,"key":"films","config":{}}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(auth.APIKeyHeader, e.apiKey())
	if rec := e.do(r); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "not stored in the database") {
		t.Fatalf("start without settings: %d %s", rec.Code, rec.Body.String())
	}
}

// The start is refused while none of the library's own indexers, or none of its own providers, would run: the
// install-wide counts are not enough. GET /setup names the ready ones and whether the trash folder is set.
func TestSetupStartNeedsUsableSources(t *testing.T) {
	t.Parallel()
	se, area := startEnv(t, false)
	s := se.setupState()
	if !s.Trash || !slices.Contains(s.ReadyProviders, "rd") || !slices.Contains(s.ReadyIndexers, "torrentio") ||
		len(s.ReadyProviders) != s.Providers || len(s.ReadyIndexers) != s.Indexers {
		t.Fatalf("setup state: %+v", s)
	}
	se.want(se.call("PUT", fmt.Sprintf("/providers/%d", se.id(entityProviders, "rd")),
		map[string]any{"generation": se.generation(), "enabled": false, "orphan_active_downloads": false}), 200, "disable rd")
	se.want(se.call("PUT", fmt.Sprintf("/indexers/%d", se.id(entityIndexers, "torrentio")),
		map[string]any{"generation": se.generation(), "enabled": false}), 200, "disable torrentio")
	if s := se.setupState(); slices.Contains(s.ReadyProviders, "rd") || slices.Contains(s.ReadyIndexers, "torrentio") {
		t.Fatalf("disabled sources still ready: %+v", s)
	}
	gen := se.generation()
	body := se.startBody("films", area)
	body["config"].(map[string]any)["providers"] = []string{"rd"}
	if out := se.start(body); out["status"] != 422 || out["confirmation"] != nil || !strings.Contains(fmt.Sprint(out["error"]), "debrid providers") {
		t.Fatalf("only a disabled provider: %v", out)
	}
	body = se.startBody("films", area)
	body["config"].(map[string]any)["indexers"] = []string{"torrentio"}
	if out := se.start(body); out["status"] != 422 || out["confirmation"] != nil || !strings.Contains(fmt.Sprint(out["error"]), "indexers") {
		t.Fatalf("only a disabled indexer: %v", out)
	}
	if se.generation() != gen {
		t.Fatal("a refused start wrote settings")
	}
	// One usable pick beside the disabled ones is enough.
	body = se.startBody("films", area)
	body["config"].(map[string]any)["providers"] = []string{"rd", "ad"}
	if out := se.start(body); out["status"] != 422 || out["confirmation"] == nil {
		t.Fatalf("a usable provider beside a disabled one: %v", out)
	}
}
