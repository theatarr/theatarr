package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/preflight"
	"github.com/theatarr/theatarr/internal/settings"
)

func (se *settingsEnv) setupCall(method, path, csrf string, body string) *httptest.ResponseRecorder {
	se.t.Helper()
	r := httptest.NewRequest(method, APIBase+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(se.cookie)
	if csrf != "" {
		r.Header.Set(auth.CSRFHeader, csrf)
	}
	return se.do(r)
}

// GET /setup and POST /preflight on a configured install: the counts, the generated admin password, and the
// preflight running the settings tests through the tester, the hooks for views and Plex, and never writing settings.
func TestSetupAndPreflight(t *testing.T) {
	t.Parallel()
	var asked []string
	se := newSettingsEnv(t, nil, func(c *Config) {
		c.Preflight = PreflightHooks{
			Views: func(context.Context) []preflight.View {
				asked = append(asked, "views")
				return []preflight.View{{Path: "/radarr", Code: http.StatusUnauthorized}}
			},
			Plex: func(context.Context) error { asked = append(asked, "plex"); return nil },
			Caps: func(key string) []string { return []string{"rss", "text"} },
		}
	})
	rec := se.setupCall("GET", "/setup", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET setup: %d %s", rec.Code, rec.Body.String())
	}
	var s SetupState
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if s.FirstRun || s.SettingsSource != "db" || s.Versions == 0 || s.Providers == 0 || s.Indexers == 0 || len(s.Missing) != 0 {
		t.Fatalf("setup = %+v", s)
	}
	// A complete install sends an empty list, not null: the setup screen reads its length.
	if !strings.Contains(rec.Body.String(), `"missing":[]`) {
		t.Fatalf("missing is not an empty list: %s", rec.Body.String())
	}
	// The first-start password is still in use until the admin changes it.
	if !s.AdminPasswordGenerated {
		t.Fatalf("admin_password_generated = false")
	}
	gen := se.generation()
	if s.LastPreflight != nil {
		t.Fatalf("last_preflight before any run = %+v", s.LastPreflight)
	}

	// A session write needs the CSRF token.
	if rec := se.setupCall("POST", "/preflight", "", `{}`); rec.Code != http.StatusForbidden {
		t.Fatalf("preflight without CSRF: %d", rec.Code)
	}
	rec = se.setupCall("POST", "/preflight", se.csrf, `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("preflight: %d %s", rec.Code, rec.Body.String())
	}
	var r preflight.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	byKey := map[string]preflight.Item{}
	for _, it := range r.Items {
		byKey[it.Area+"/"+it.Check] = it
	}
	if it := byKey["indexers/zilean (zilean)"]; it.Status != preflight.OK || !strings.Contains(it.Why, "answers rss, text queries") {
		t.Fatalf("zilean = %+v", it)
	}
	if it := byKey["views//radarr"]; it.Status != preflight.OK {
		t.Fatalf("view = %+v", it)
	}
	if it := byKey["plex/plex"]; it.Status != preflight.OK {
		t.Fatalf("plex = %+v (%+v)", it, r.Items)
	}
	se.tester.mu.Lock()
	tested := len(se.tester.seen)
	se.tester.mu.Unlock()
	if tested == 0 {
		t.Fatal("the preflight tested no source")
	}
	if se.generation() != gen {
		t.Fatal("the preflight wrote settings")
	}
	// GET /setup carries the run's summary, so the setup screen's readiness survives a reload. The UI compares its
	// generation with GET /settings/versions', so both must name the same one.
	var listed struct {
		Generation int64 `json:"generation"`
	}
	if rec := se.setupCall("GET", "/settings/versions", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("GET settings/versions: %d %s", rec.Code, rec.Body.String())
	} else if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil || listed.Generation != gen {
		t.Fatalf("settings/versions generation = %d (%v), want %d", listed.Generation, err, gen)
	}
	last := se.lastPreflight()
	if last == nil || last.Generation != gen || !last.Sources || last.OK != r.OK || last.Warn != r.Warn ||
		last.Fail != r.Fail || !last.At.Equal(r.At) {
		t.Fatalf("last_preflight = %+v, report ok %d warn %d fail %d at %v, generation %d", last, r.OK, r.Warn, r.Fail, r.At, gen)
	}

	// sources:false makes no outbound call.
	asked = nil
	rec = se.setupCall("POST", "/preflight", se.csrf, `{"sources":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("preflight: %d", rec.Code)
	}
	se.tester.mu.Lock()
	again := len(se.tester.seen)
	se.tester.mu.Unlock()
	if again != tested || strings.Contains(strings.Join(asked, ","), "plex") {
		t.Fatalf("sources:false still tested: %d → %d, asked %v", tested, again, asked)
	}
	if !strings.Contains(rec.Body.String(), `"not tested"`) {
		t.Fatalf("untested sources not reported as such: %s", rec.Body.String())
	}
	// The latest run wins: one without the source tests does not count as a full preflight.
	if last := se.lastPreflight(); last == nil || last.Sources || last.Generation != gen {
		t.Fatalf("last_preflight after sources:false = %+v", last)
	}
}

func (se *settingsEnv) lastPreflight() *LastPreflight {
	se.t.Helper()
	rec := se.setupCall("GET", "/setup", "", "")
	if rec.Code != http.StatusOK {
		se.t.Fatalf("GET setup: %d %s", rec.Code, rec.Body.String())
	}
	var s SetupState
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		se.t.Fatal(err)
	}
	return s.LastPreflight
}

func TestPreflightStoredCredentials(t *testing.T) {
	t.Parallel()
	se := seededSectionsEnv(t)
	// Model a real process lookup: derived names and the provider sentinel are never environment variables.
	se.web.settingsLookup = func(name string) (string, bool) {
		if strings.HasPrefix(name, policy.SettingsSecretPrefix) || name == settings.CredentialSentinel {
			return "", false
		}
		return "fixture-" + name, true
	}
	for section, secrets := range map[string]map[string]string{
		"library":  {"plex.token": "invented-preflight-plex"},
		"metadata": {"sources.tmdb.api_key": "invented-preflight-tmdb"},
	} {
		se.want(se.call("PUT", "/sections/"+section, map[string]any{"generation": se.generation(), "secrets": secrets}), 200, "store section credential")
	}
	read := se.want(se.call("GET", "", nil), 200, "read")
	var provider map[string]any
	for _, raw := range read["providers"].([]any) {
		if row := raw.(map[string]any); row["key"] == "rd" {
			provider = row
			break
		}
	}
	if provider == nil {
		t.Fatal("missing rd fixture")
	}
	se.want(se.call("PUT", fmt.Sprintf("/providers/%.0f", provider["id"].(float64)), map[string]any{
		"generation": se.generation(), "secret": "invented-preflight-provider", "orphan_active_downloads": false,
	}), 200, "store provider credential")
	// The web layer keeps its startup policy (the fixture has no runtime HTTP rebuild): the preflight still reads one
	// snapshot, the stored head, for both the policy and its credentials.
	rec := se.setupCall("POST", "/preflight", se.csrf, `{"sources":false}`)
	if rec.Code != 200 {
		t.Fatalf("preflight: %d %s", rec.Code, rec.Body.String())
	}
	var r preflight.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	for _, check := range []string{"tmdb", "plex", "rd (realdebrid)"} {
		found := false
		for _, it := range r.Items {
			if it.Check == check {
				found = true
				if it.Status == preflight.Fail || !strings.Contains(it.Why, "stored in settings") {
					t.Errorf("%s: %+v", check, it)
				}
			}
		}
		if !found {
			t.Errorf("missing check %s", check)
		}
	}
	if strings.Contains(rec.Body.String(), "invented-preflight-") || strings.Contains(rec.Body.String(), policy.SettingsSecretPrefix) {
		t.Fatal("preflight disclosed a credential or internal name")
	}
}

// GET /setup tmdb: true while the tmdb source has its address and a key that resolves, from the environment variable
// it names or from settings. The setup screen's one-field save (both sources and the key in one section PUT, one
// confirmation) turns it on from nothing.
func TestSetupTMDb(t *testing.T) {
	t.Parallel()
	state := func(se *settingsEnv) SetupState {
		t.Helper()
		rec := se.setupCall("GET", "/setup", "", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET setup: %d %s", rec.Code, rec.Body.String())
		}
		var s SetupState
		if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	// A real process lookup: derived names are never environment variables.
	env := map[string]string{"THEATARR_TMDB_KEY": "fixture-env-key"}
	lookup := func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}

	// The example's tmdb source names an environment variable: done while it is set, with nothing stored.
	se := seededSectionsEnv(t)
	se.web.settingsLookup = lookup
	if !state(se).TMDb {
		t.Fatal("tmdb with its key in the environment is not done")
	}
	delete(env, "THEATARR_TMDB_KEY")
	if state(se).TMDb {
		t.Fatal("tmdb without its environment variable is done")
	}

	// No metadata sources at all, as on a fresh database.
	se = seededSectionsEnvBase(t, func(p *policy.Policy) { p.Metadata, p.UI = nil, nil })
	se.web.settingsLookup = lookup
	if s := state(se); s.TMDb || !slices.ContainsFunc(s.Missing, func(m string) bool { return strings.Contains(m, "no TMDb source") }) {
		t.Fatalf("no sources: %+v", s)
	}
	// What the setup screen sends: both sources with their public addresses and the one key, in one save.
	gen := se.generation()
	body := map[string]any{"generation": gen,
		"config": map[string]any{"sources": map[string]any{
			"tmdb":        map[string]any{"url": "https://api.themoviedb.org/3"},
			"tmdb-images": map[string]any{"url": "https://image.tmdb.org/t/p"},
		}},
		"secrets": map[string]string{"sources.tmdb.api_key": "invented-setup-tmdb"}}
	refused := se.want(se.call("PUT", "/sections/metadata", body), 422, "new hosts need proof")
	confirmRefusal(t, refused, "metadata.sources.tmdb.url")
	body["confirmation_token"] = confirmRefusal(t, refused, "metadata.sources.tmdb-images.url")
	if state(se).TMDb || se.generation() != gen {
		t.Fatal("the refused save changed something")
	}
	se.want(se.call("PUT", "/sections/metadata", body), 200, "confirmed")
	if se.generation() != gen+1 {
		t.Fatalf("the save took %d generations, want 1", se.generation()-gen)
	}
	s := state(se)
	if !s.TMDb || slices.ContainsFunc(s.Missing, func(m string) bool { return strings.Contains(m, "TMDb") }) {
		t.Fatalf("after the save: %+v", s)
	}
	read := findSection(t, se.want(se.call("GET", "/sections", nil), 200, "read"), "metadata")
	if shown, _ := json.Marshal(read); strings.Contains(string(shown), "invented-setup-tmdb") ||
		!strings.Contains(string(shown), "https://api.themoviedb.org/3") || !strings.Contains(string(shown), "https://image.tmdb.org/t/p") {
		t.Fatalf("stored metadata section: %s", shown)
	}
	// The address alone is not enough: clearing the key reopens the step.
	se.want(se.call("PUT", "/sections/metadata", map[string]any{"generation": se.generation(),
		"clear_secrets": []string{"sources.tmdb.api_key"}}), 200, "clear the key")
	if state(se).TMDb {
		t.Fatal("tmdb without a key is done")
	}
}

// Setup counts a provider once its credential is set (its env variable, or a stored key): a keyless provider does
// not finish the providers step.
func TestSetupCountsProvidersWithCredential(t *testing.T) {
	t.Parallel()
	se := newSettingsEnv(t, nil, nil)
	state := func() SetupState {
		t.Helper()
		rec := se.setupCall("GET", "/setup", "", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET setup: %d %s", rec.Code, rec.Body.String())
		}
		var s SetupState
		if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	// The example's providers name env variables the fixture sets.
	if s := state(); s.Providers != 3 || s.Indexers == 0 {
		t.Fatalf("providers with their env variable: %+v", s)
	}
	put := func(key string, body map[string]any) {
		t.Helper()
		body["generation"], body["orphan_active_downloads"] = se.generation(), false
		se.want(se.call("PUT", fmt.Sprintf("/providers/%d", se.id(entityProviders, key)), body), 200, "provider "+key)
	}
	// A cleared key is not looked up in the environment: the providers are keyless.
	for _, key := range []string{"rd", "ad", "tb"} {
		put(key, map[string]any{"clear_secret": true})
	}
	if s := state(); s.Providers != 0 || s.Indexers == 0 {
		t.Fatalf("keyless providers counted: %+v", s)
	}
	put("rd", map[string]any{"secret": "invented-setup-provider"})
	if s := state(); s.Providers != 1 {
		t.Fatalf("provider with a stored key: %+v", s)
	}
}

// Setup counts the indexers that would run, and its "Off until set up" list agrees with the count: a disabled indexer
// is not done, a Torznab feed without its key is not, and an enabled Torrentio (no key) is.
func TestSetupCountsIndexersThatWouldRun(t *testing.T) {
	t.Parallel()
	se := newSettingsEnv(t, nil, nil)
	state := func() SetupState {
		t.Helper()
		rec := se.setupCall("GET", "/setup", "", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET setup: %d %s", rec.Code, rec.Body.String())
		}
		var s SetupState
		if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	listsIndexers := func(s SetupState) bool {
		for _, m := range s.Missing {
			if strings.HasPrefix(m, "no enabled indexer") {
				return true
			}
		}
		return false
	}
	put := func(key string, body map[string]any) {
		t.Helper()
		body["generation"] = se.generation()
		se.want(se.call("PUT", fmt.Sprintf("/indexers/%d", se.id(entityIndexers, key)), body), http.StatusOK, "indexer "+key)
	}
	for _, key := range []string{"dmm", "torrentio", "zilean", "seadex", "tracker-example"} {
		put(key, map[string]any{"enabled": false})
	}
	put("prowlarr-public", map[string]any{"clear_secret": true})
	if s := state(); s.Indexers != 0 || !listsIndexers(s) {
		t.Fatalf("a disabled indexer or a Torznab feed without its key counted: %+v", s)
	}
	put("torrentio", map[string]any{"enabled": true})
	if s := state(); s.Indexers != 1 || listsIndexers(s) {
		t.Fatalf("an enabled Torrentio not counted: %+v", s)
	}
}
