package web

import (
	"bytes"
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
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
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
	gen := se.generation()
	body := map[string]any{"generation": gen, "key": "test-version", "config": config, "roots": []string{root}}
	se.want(se.call("POST", "/versions", body), 200, "create")
	if se.generation() != gen+1 {
		t.Fatal("create not saved")
	}
	se.want(se.call("PUT", "/versions/test-version", body), 409, "stale")
	body["generation"] = se.generation()
	config["upgrades"] = "off"
	se.want(se.call("PUT", "/versions/test-version", body), 200, "edit")
	if got := se.coord.Current().Policy.Versions["test-version"]; got == nil || got.Upgrades != "off" {
		t.Fatalf("edit not applied: %+v", got)
	}
	if se.coord.Current().Policy.Router.Enabled || se.coord.Current().Policy.Library.Enabled || se.coord.Current().Policy.Notify.Enabled {
		t.Fatal("changed YAML gates")
	}
	path := fmt.Sprintf("/versions/test-version?generation=%d", se.generation())
	token := confirmRefusal(t, se.want(se.call("DELETE", path, nil), 422, "unconfirmed delete unlinked"), "Library test-version is removed")
	se.want(confirmedDelete(se, path, token), 200, "delete unlinked")
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
	data = bytes.ReplaceAll(data, []byte("upgrades: auto"), []byte("upgrades: off"))
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
	// The fixture's movie-web link would stay at the old root: this test moves an empty library.
	se.exec(`DELETE FROM link WHERE id = 902`)
	p, err := se.coord.Current().Policy.Clone()
	if err != nil {
		t.Fatal(err)
	}
	p.Versions["movie-web"].Library = root
	p.Library.Versions["movie-web"].Roots = []string{root}
	// The import also drops anime-movie: removing a library asks for confirmation.
	delete(p.Versions, "anime-movie")
	delete(p.Library.Versions, "anime-movie")
	for _, v := range p.Views {
		v.Versions = slices.DeleteFunc(v.Versions, func(k string) bool { return k == "anime-movie" })
		for name, ks := range v.Profiles {
			if ks = slices.DeleteFunc(ks, func(k string) bool { return k == "anime-movie" }); len(ks) == 0 {
				delete(v.Profiles, name)
			} else {
				v.Profiles[name] = ks
			}
		}
	}
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
	se.want(se.call("POST", "/import", body), 422, "ordinary import refuses the removal")
	proof := se.want(se.call("POST", "/import/confirm", body), 200, "import proof")
	if !strings.Contains(fmt.Sprint(proof["effects"]), "Library anime-movie is removed") {
		t.Fatal("import confirmation omitted the removal")
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
	body := map[string]any{"generation": se.generation(), "key": "test-version", "config": config, "roots": []string{root}}
	se.want(se.call("POST", "/versions", body), 200, "create")
	naming := qualityID(t, se, "naming", "test-version")
	refusal := se.want(se.call("DELETE", fmt.Sprintf("/naming/%d?generation=%d", naming, se.generation()), nil), 422, "naming of a live version")
	if fmt.Sprint(refusal["uses"]) != "[versions[test-version]]" {
		t.Fatalf("refusal: %+v", refusal)
	}
	path := fmt.Sprintf("/versions/test-version?generation=%d", se.generation())
	token := confirmRefusal(t, se.want(se.call("DELETE", path, nil), 422, "unconfirmed delete with its naming"), "Library test-version is removed")
	se.want(confirmedDelete(se, path, token), 200, "delete with its naming")
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

// An edit that sends empty roots ends with the library folder as its root, as a create does; an edit that sends no
// roots keeps the stored ones.
func TestSettingsVersionEditEmptyRootsUsesLibraryFolder(t *testing.T) {
	t.Parallel()
	area := t.TempDir()
	se := newSettingsEnvBase(t, nil, func(p *policy.Policy) { p.Library.Dangling.Roots = []string{area} }, nil)
	config := se.want(se.call("GET", "/versions/movie-web", nil), 200, "get")["record"].(map[string]any)["config"].(map[string]any)
	root, wide := filepath.Join(area, "lib"), filepath.Join(area, "lib-wide")
	for _, d := range []string{root, wide} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	config["library"] = root
	se.want(se.call("POST", "/versions", map[string]any{"generation": se.generation(), "key": "test-version", "config": config, "roots": []string{root}}), 200, "create")
	roots := func() []any {
		return se.want(se.call("GET", "/versions/test-version", nil), 200, "get")["record"].(map[string]any)["roots"].([]any)
	}
	se.want(se.call("PUT", "/versions/test-version", map[string]any{"generation": se.generation(), "key": "test-version", "config": config, "roots": []string{}}), 200, "edit with empty roots")
	if got := roots(); len(got) != 1 || got[0] != root {
		t.Fatalf("roots after an edit with empty roots: %v", got)
	}
	// Explicit roots are kept by an edit that does not send any.
	se.want(se.call("PUT", "/versions/test-version", map[string]any{"generation": se.generation(), "key": "test-version", "config": config, "roots": []string{root, wide}}), 200, "two roots")
	se.want(se.call("PUT", "/versions/test-version", map[string]any{"generation": se.generation(), "key": "test-version", "config": config}), 200, "edit without roots")
	if got := roots(); len(got) != 2 {
		t.Fatalf("stored roots changed by an edit without roots: %v", got)
	}
}

// A library stored without roots gets its library folder as its root on its next edit, as a create does: without one
// every import it grabs would be refused (not listed under library.versions).
func TestSettingsVersionEditFillsStoredNilRoots(t *testing.T) {
	t.Parallel()
	area := t.TempDir()
	se := newSettingsEnvBase(t, nil, func(p *policy.Policy) { p.Library.Dangling.Roots = []string{area} }, nil)
	se.exec(`DELETE FROM link WHERE id = 901`) // the fixture's movie-remux link would stay at the old root
	folder := filepath.Join(area, "remux")
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	snap, err := se.coord.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	req := settings.SaveRequest{ExpectedGeneration: snap.Generation, Actor: "test", Records: cloneRecords(snap.Records)}
	for i := range req.Records.Versions {
		if req.Records.Versions[i].Key == "movie-remux" {
			req.Records.Versions[i].Roots = nil
		}
	}
	if _, err := se.coord.SaveAndApply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	record := se.want(se.call("GET", "/versions/movie-remux", nil), 200, "get")["record"].(map[string]any)
	if record["roots"] != nil {
		t.Fatalf("fixture roots: %v", record["roots"])
	}
	config := record["config"].(map[string]any)
	config["library"] = folder
	body := map[string]any{"generation": se.generation(), "key": "movie-remux", "config": config, "roots": []string{}}
	se.want(se.call("PUT", "/versions/movie-remux", body), 200, "edit")
	after := se.want(se.call("GET", "/versions/movie-remux", nil), 200, "get")["record"].(map[string]any)
	if roots, _ := after["roots"].([]any); len(roots) != 1 || roots[0] != config["library"] {
		t.Fatalf("roots after the edit: %v (library %v)", after["roots"], config["library"])
	}
	if roots := library.NewGate(se.coord.Current().Policy).Roots("movie-remux"); len(roots) != 1 || roots[0] != folder {
		t.Fatalf("gate roots: %v", roots)
	}
}

// Keep in sync, saved with a library: sync_with sets the library's group and the list carries the groups; a
// library of another kind is refused; deleting a library drops it from its group, and the group left with one.
func TestSettingsVersionSyncWith(t *testing.T) {
	t.Parallel()
	area := t.TempDir()
	se := newSettingsEnvBase(t, nil, func(p *policy.Policy) { p.Library.Dangling.Roots = []string{area} }, nil)
	if groups := se.want(se.call("GET", "/versions", nil), 200, "list")["sync_groups"]; fmt.Sprint(groups) != "[]" {
		t.Fatalf("no groups: %v", groups)
	}
	config := se.want(se.call("GET", "/versions/movie-web", nil), 200, "get")["record"].(map[string]any)["config"].(map[string]any)
	root := filepath.Join(area, "test-version")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	config["library"] = root
	se.want(se.call("POST", "/versions", map[string]any{"generation": se.generation(), "key": "test-version", "config": config,
		"roots": []string{root}, "sync_with": []string{"movie-web"}}), 200, "create in sync")
	if groups := se.want(se.call("GET", "/versions", nil), 200, "list")["sync_groups"]; fmt.Sprint(groups) != "[[test-version movie-web]]" {
		t.Fatalf("groups: %v", groups)
	}
	if got := se.coord.Current().Policy.SyncGroup("movie-web"); !slices.Equal(got, []string{"test-version", "movie-web"}) {
		t.Fatalf("runtime policy group %v", got)
	}
	se.want(se.call("PUT", "/versions/test-version", map[string]any{"generation": se.generation(), "key": "test-version",
		"config": config, "sync_with": []string{"tv-2160p"}}), 422, "series library refused")
	// A save without sync_with keeps the group.
	se.want(se.call("PUT", "/versions/test-version", map[string]any{"generation": se.generation(), "key": "test-version",
		"config": config}), 200, "edit")
	if got := se.coord.Current().Policy.SyncGroup("test-version"); len(got) != 2 {
		t.Fatalf("edit dropped the group: %v", got)
	}
	path := fmt.Sprintf("/versions/test-version?generation=%d", se.generation())
	refusal := se.want(se.call("DELETE", path, nil), 422, "unconfirmed delete")
	token := confirmRefusal(t, refusal, "Library test-version is removed")
	se.want(confirmedDelete(se, path, token), 200, "delete")
	if lib := se.coord.Current().Policy.Library; len(lib.SyncGroups) != 0 {
		t.Fatalf("deleted library's group kept: %v", lib.SyncGroups)
	}
}
