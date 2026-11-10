package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/policy"
	"go.yaml.in/yaml/v3"
)

func TestSettingsVersionsCRUDAndConfirmation(t *testing.T) {
	t.Parallel()
	area := t.TempDir()
	se := newSettingsEnvBase(t, nil, func(p *policy.Policy) { p.Library.Dangling.Roots = []string{area} }, nil)
	list := se.want(se.call("GET", "/versions", nil), 200, "list")
	if list["source"] != "db" || len(list["versions"].([]any)) != 5 {
		t.Fatalf("list: %+v", list)
	}
	get := se.call("GET", "/versions/movie-web", nil)
	record := se.want(get, 200, "get")["record"].(map[string]any)
	if get.Header().Get("ETag") == "" {
		t.Fatal("missing ETag")
	}
	config := record["config"].(map[string]any)
	root := filepath.Join(area, "test-version")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	config["library"] = root
	delete(config, "mode")
	gen := se.generation()
	body := map[string]any{"generation": gen, "key": "test-version", "config": config, "roots": []string{root}}
	se.want(se.call("POST", "/versions", body), 200, "create shadow by default")
	got := se.want(se.call("GET", "/versions/test-version", nil), 200, "new get")["record"].(map[string]any)
	if got["config"].(map[string]any)["mode"] != "shadow" {
		t.Fatal("create inferred activation")
	}
	se.want(se.call("PUT", "/versions/test-version", body), 409, "stale")
	body["generation"] = se.generation()
	config["mode"] = "active"
	refusal := se.want(se.call("PUT", "/versions/test-version", body), 422, "unconfirmed activation")
	effects := refusal["effects"].([]any)
	text := fmt.Sprint(effects)
	if !strings.Contains(text, root) || !strings.Contains(text, "rd, ad, tb") || !strings.Contains(text, "router.enabled") {
		t.Fatalf("effects not specific: %s", text)
	}
	proof := se.want(se.call("POST", "/versions/confirm", body), 200, "confirm")
	if se.generation() != gen+1 {
		t.Fatal("confirm wrote settings")
	}
	body["confirmation_token"] = proof["token"]
	se.want(se.call("PUT", "/versions/test-version", body), 200, "confirmed activation")
	se.want(se.call("PUT", "/versions/test-version", body), 409, "stale token")
	if se.coord.Current().Policy.Router.Enabled || se.coord.Current().Policy.Library.Enabled || se.coord.Current().Policy.Notify.Enabled {
		t.Fatal("changed YAML gates")
	}
	se.want(se.call("DELETE", fmt.Sprintf("/versions/test-version?generation=%d", se.generation()), nil), 200, "delete unlinked")
	se.want(se.call("GET", "/versions/test-version", nil), 404, "deleted")
}

func TestSettingsVersionRootBindingsAndGuards(t *testing.T) {
	t.Parallel()
	area := t.TempDir()
	root := filepath.Join(area, "root")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	se := newSettingsEnvBase(t, nil, func(p *policy.Policy) { p.Library.Dangling.Roots = []string{area} }, nil)
	gen := se.generation()
	se.want(se.call("POST", "/versions/validate-root", map[string]any{"generation": gen, "key": "new-version", "root": root}), 200, "root")
	refused := se.want(se.call("POST", "/versions/validate-root", map[string]any{"generation": gen, "key": "new-version", "root": "relative"}), 422, "invalid root")
	if len(refused["fields"].([]any)) == 0 {
		t.Fatal("missing typed fields")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 || se.generation() != gen {
		t.Fatal("root validation wrote")
	}
	bindings := se.want(se.call("GET", "/view-bindings", nil), 200, "bindings")
	raw, _ := json.Marshal(bindings)
	if strings.Contains(string(raw), "api_key_env") {
		t.Fatal("view credential exposed")
	}
	body := map[string]any{"generation": gen, "versions": []string{"movie-web"}, "profiles": map[string][]string{"WEB": {"movie-web"}}}
	se.want(se.call("PUT", "/view-bindings/radarr", body), 200, "bindings update")
	current := se.coord.Current().Policy.Views["radarr"]
	if len(current.Versions) != 1 || current.APIKeyEnv != "THEATARR_VIEW_RADARR_KEY" || current.Path != "/radarr" {
		t.Fatal("binding edit changed identity")
	}
	se.want(se.call("PUT", "/view-bindings/radarr", body), 409, "stale binding")
	uid := se.account("version-user", "user", auth.StatusActive, false)
	for _, path := range []string{"/versions", "/view-bindings"} {
		r := httptest.NewRequest("GET", APIBase+"/settings"+path, nil)
		se.want(se.do(r), 401, "anonymous")
	}
	for _, path := range []string{"/versions", "/versions/confirm", "/versions/validate-root", "/view-bindings/radarr", "/import/confirm"} {
		method := "POST"
		if path == "/view-bindings/radarr" {
			method = "PUT"
		}
		r := httptest.NewRequest(method, APIBase+"/settings"+path, bytes.NewBufferString(`{}`))
		r.AddCookie(se.cookie)
		se.want(se.do(r), http.StatusForbidden, "CSRF")
		r = httptest.NewRequest(method, APIBase+"/settings"+path, bytes.NewBufferString(`{}`))
		cookie, csrf := se.session(uid)
		r.AddCookie(cookie)
		r.Header.Set(auth.CSRFHeader, csrf)
		se.want(se.do(r), http.StatusForbidden, "admin only")
	}

}

func TestSettingsImportPreviewIncludesE3(t *testing.T) {
	t.Parallel()
	se := newSettingsEnv(t, nil, nil)
	file := filepath.Join(se.dir, "versions.yaml")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.ReplaceAll(data, []byte("upgrades: approve"), []byte("upgrades: off"))
	if err = os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	preview := se.want(se.call("POST", "/import/preview", map[string]any{"generation": se.generation()}), 200, "preview")
	found := false
	for _, value := range preview["changes"].([]any) {
		change := value.(map[string]any)
		if change["entity"] == "versions" && change["key"] == "movie-remux" {
			found = true
		}
	}
	if !found {
		t.Fatal("import hid version change")
	}
	se.want(se.call("POST", "/import/confirm", map[string]any{"generation": se.generation(), "orphan_active_downloads": false}), 200, "import confirmation")
}

func TestSettingsVersionImportRequiresExactConfirmation(t *testing.T) {
	t.Parallel()
	area := t.TempDir()
	root := filepath.Join(area, "movie-root")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	se := newSettingsEnvBase(t, nil, func(p *policy.Policy) { p.Library.Dangling.Roots = []string{area} }, nil)
	p, err := se.coord.Current().Policy.Clone()
	if err != nil {
		t.Fatal(err)
	}
	p.Versions["movie-web"].Mode, p.Versions["movie-web"].Grab = policy.ModeActive, true
	p.Versions["movie-web"].Library = root
	p.Library.Versions["movie-web"].Roots = []string{root}
	write := func() {
		t.Helper()
		files, err := filepath.Glob(filepath.Join(se.dir, "*.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
		}
		data, err := yaml.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(se.dir, "policy.yaml"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	gen := se.generation()
	body := map[string]any{"generation": gen, "orphan_active_downloads": false}
	se.want(se.call("POST", "/import", body), 422, "ordinary import refuses activation")
	proof := se.want(se.call("POST", "/import/confirm", body), 200, "import proof")
	if !strings.Contains(fmt.Sprint(proof["effects"]), root) {
		t.Fatal("import confirmation omitted root")
	}
	p.Versions["movie-web"].Upgrades = "off"
	write()
	body["confirmation_token"] = proof["token"]
	se.want(se.call("POST", "/import", body), 422, "changed import invalidates token")
	delete(body, "confirmation_token")
	proof = se.want(se.call("POST", "/import/confirm", body), 200, "new proof")
	body["confirmation_token"] = proof["token"]
	se.want(se.call("POST", "/import", body), 200, "confirmed import")
	if se.generation() != gen+1 || se.coord.Current().Policy.Router.Enabled || se.coord.Current().Policy.Library.Enabled || se.coord.Current().Policy.Notify.Enabled {
		t.Fatal("import changed global gates or wrong generation")
	}
}

func TestSettingsVersionIfMatch(t *testing.T) {
	t.Parallel()
	se := newSettingsEnv(t, nil, nil)
	body := fmt.Sprintf(`{"generation":%d,"key":"new","root":"relative"}`, se.generation())
	r := httptest.NewRequest("POST", APIBase+"/settings/versions/validate-root", strings.NewReader(body))
	r.AddCookie(se.cookie)
	r.Header.Set(auth.CSRFHeader, se.csrf)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("If-Match", `"settings-0"`)
	se.want(se.do(r), 409, "If-Match must match generation")
}

// With the quality records owning naming (production: every version has a naming record after BootstrapQuality), a
// new version gets its naming record, that record cannot be deleted on its own, and deleting the version takes it
// along.
func TestSettingsVersionsCreateDeleteWithQualityNaming(t *testing.T) {
	t.Parallel()
	area := t.TempDir()
	se := newQualityEnvBase(t, func(p *policy.Policy) { p.Library.Dangling.Roots = []string{area} })
	qualityID(t, se, "naming", "movie-web")
	record := se.want(se.call("GET", "/versions/movie-web", nil), 200, "get")["record"].(map[string]any)
	config := record["config"].(map[string]any)
	root := filepath.Join(area, "test-version")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	config["library"] = root
	delete(config, "mode")
	body := map[string]any{"generation": se.generation(), "key": "test-version", "config": config, "roots": []string{root}}
	se.want(se.call("POST", "/versions", body), 200, "create")
	naming := qualityID(t, se, "naming", "test-version")
	refusal := se.want(se.call("DELETE", fmt.Sprintf("/naming/%d?generation=%d", naming, se.generation()), nil), 422, "naming of a live version")
	if fmt.Sprint(refusal["uses"]) != "[versions[test-version]]" {
		t.Fatalf("refusal: %+v", refusal)
	}
	se.want(se.call("DELETE", fmt.Sprintf("/versions/test-version?generation=%d", se.generation()), nil), 200, "delete with its naming")
	se.want(se.call("GET", "/versions/test-version", nil), 404, "deleted")
	if list := se.call("GET", "/naming", nil); list.Code != 200 || strings.Contains(list.Body.String(), `"key":"test-version"`) {
		t.Fatal("naming record left behind")
	}
	if _, ok := se.coord.Current().Policy.Versions["test-version"]; ok {
		t.Fatal("version still in the runtime policy")
	}
}

// TestSettingsVersionAvailabilityFields: the library editor's save of what it loaded leaves the stored library
// byte-identical and stores neither minimum_availability nor availability_delay; set, they are stored and read
// back, and sent empty and 0 they are dropped again. A series library takes neither.
func TestSettingsVersionAvailabilityFields(t *testing.T) {
	t.Parallel()
	se := newSettingsEnv(t, nil, nil)
	stored := func(key string) string {
		t.Helper()
		var raw string
		if err := se.st.Reader().QueryRow(`SELECT config FROM setting_version WHERE key = ?`, key).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	loaded := func(key string) map[string]any {
		t.Helper()
		return se.want(se.call("GET", "/versions/"+key, nil), 200, "get")["record"].(map[string]any)["config"].(map[string]any)
	}
	save := func(key string, config map[string]any, code int, label string) {
		t.Helper()
		se.want(se.call("PUT", "/versions/"+key, map[string]any{"generation": se.generation(), "key": key, "config": config}), code, label)
	}
	before := stored("movie-web")
	config := loaded("movie-web")
	if _, ok := config["minimum_availability"]; ok || strings.Contains(before, "availab") {
		t.Fatalf("unset fields are present: %v / %s", config, before)
	}
	if _, ok := config["availability_delay"]; ok {
		t.Fatalf("an unset delay is sent: %v", config)
	}
	save("movie-web", config, 200, "untouched save")
	if got := stored("movie-web"); got != before {
		t.Fatalf("an untouched save changed the stored library:\n%s\n%s", before, got)
	}
	config["minimum_availability"], config["availability_delay"] = "inCinemas", -3
	save("movie-web", config, 200, "set")
	config = loaded("movie-web")
	if config["minimum_availability"] != "inCinemas" || config["availability_delay"] != float64(-3) {
		t.Fatalf("read back: %v %v", config["minimum_availability"], config["availability_delay"])
	}
	set := stored("movie-web")
	save("movie-web", config, 200, "untouched save of set fields")
	if got := stored("movie-web"); got != set || !strings.Contains(got, `"availability_delay":-3`) {
		t.Fatalf("an untouched save changed the set fields:\n%s\n%s", set, got)
	}
	// The editor clears a stored value by sending it empty or 0.
	config["minimum_availability"], config["availability_delay"] = "", 0
	save("movie-web", config, 200, "clear")
	if got := stored("movie-web"); got != before {
		t.Fatalf("cleared fields left a trace:\n%s\n%s", before, got)
	}
	for field, value := range map[string]any{"minimum_availability": "announced", "availability_delay": 2} {
		series := loaded("tv-2160p")
		series[field] = value
		save("tv-2160p", series, 422, "series library "+field)
	}
	config["minimum_availability"] = "tba"
	save("movie-web", config, 422, "tba as a library default")
}
