package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
)

func newQualityEnv(t *testing.T) *settingsEnv {
	t.Helper()
	return newQualityEnvBase(t, nil)
}

// newQualityEnvBase is newQualityEnv with an edit of the base policy: the production state, the quality records
// bootstrapped over the source and version records.
func newQualityEnvBase(t *testing.T, base func(*policy.Policy)) *settingsEnv {
	t.Helper()
	var coord *settings.RuntimeCoordinator
	se := newSettingsEnvBase(t, nil, base, func(c *Config) {
		st, err := settings.New(c.Store)
		if err != nil {
			t.Fatal(err)
		}
		st.SetTrashRoot("../settings/testdata/trash")
		snap, err := st.BootstrapQuality(context.Background(), c.Policy, "upgrade")
		if err != nil {
			t.Fatal(err)
		}
		coord, err = settings.NewRuntimeCoordinator(st, c.Policy, snap, fakeRuntime{}, func(context.Context, *settings.Snapshot, *settings.Runtime) (any, error) { return fakeRuntime{}, nil })
		if err != nil {
			t.Fatal(err)
		}
		c.Settings = coord
		c.QualitySync = &settings.QualitySync{Store: st, Coordinator: coord, TrashRoot: "../settings/testdata/trash", Impact: func(context.Context, *policy.Policy, *policy.Policy) (settings.DecisionImpact, error) {
			return settings.DecisionImpact{Examples: []settings.ImpactExample{}}, nil
		}}
	})
	se.coord = coord
	return se
}

func qualityID(t *testing.T, se *settingsEnv, entity, key string) int64 {
	t.Helper()
	res := se.call("GET", "/"+entity, nil)
	se.want(res, 200, "list "+entity)
	var body struct {
		Records []QualityRecord `json:"records"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, r := range body.Records {
		if r.Key == key {
			return r.ID
		}
	}
	t.Fatal("record absent", entity, key)
	return 0
}

func TestQualitySettingsCRUDValidationNamingAndExport(t *testing.T) {
	t.Parallel()
	se := newQualityEnv(t)
	profile := map[string]any{"qualities": []string{"WEBDL-1080p"}, "cutoff": "WEBDL-1080p", "upgrades": true, "formats": map[string]int{}}
	se.want(se.call("POST", "/profiles", map[string]any{"generation": se.generation(), "key": "editor", "config": profile}), 200, "create profile")
	profileID := qualityID(t, se, "profiles", "editor")
	old := se.generation()
	profile["cutoff"] = "missing"
	invalid := se.want(se.call("PUT", fmt.Sprintf("/profiles/%d", profileID), map[string]any{"generation": old, "config": profile}), 422, "invalid whole candidate")
	if invalid["detail"] == nil || se.generation() != old {
		t.Fatal("validation was not typed or committed invalid settings")
	}
	profile["cutoff"] = "WEBDL-1080p"
	profile["min_score"] = 20
	profile["cutoff_score"] = 20
	se.want(se.call("PUT", fmt.Sprintf("/profiles/%d", profileID), map[string]any{"generation": old, "config": profile}), 200, "update profile")
	se.want(se.call("PUT", fmt.Sprintf("/profiles/%d", profileID), map[string]any{"generation": old, "config": profile}), 409, "stale editor")
	spec := json.RawMessage(`{"name":"Editor format","trash_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","includeCustomFormatWhenRenaming":true,"specifications":[{"name":"Group","implementation":"ReleaseTitleSpecification","negate":false,"required":false,"fields":{"value":"NTb"}}]}`)
	se.want(se.call("POST", "/formats/import", map[string]any{"generation": se.generation(), "key": "Editor format", "config": map[string]any{"flavour": "radarr", "include_when_renaming": false}, "specification": spec}), 200, "import arr format")
	formatID := qualityID(t, se, "formats", "Editor format")
	se.want(se.call("PUT", fmt.Sprintf("/formats/%d", formatID), map[string]any{"generation": se.generation(), "config": map[string]any{"flavour": "radarr", "trash_id": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}, "specification": spec}), 422, "conflicting TRaSH ids")
	exported := se.want(se.call("GET", fmt.Sprintf("/formats/%d/export", formatID), nil), 200, "export arr format")
	if exported["name"] != "Editor format" || exported["trash_id"] != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || exported["includeCustomFormatWhenRenaming"] != false || len(exported["specifications"].([]any)) != 1 {
		t.Fatal("arr JSON lost fields", exported)
	}
	fields, ok := exported["specifications"].([]any)[0].(map[string]any)["fields"].([]any)
	if !ok || len(fields) != 1 || fields[0].(map[string]any)["name"] != "value" || fields[0].(map[string]any)["value"] != "NTb" {
		t.Fatal("export fields are not arr resources", exported)
	}
	spec = json.RawMessage(strings.ReplaceAll(string(spec), `"name":"Editor format"`, `"name":"Edited display name"`))
	se.want(se.call("PUT", fmt.Sprintf("/formats/%d", formatID), map[string]any{"generation": se.generation(), "config": map[string]any{"flavour": "radarr"}, "specification": spec}), 200, "edit format display name")
	exported = se.want(se.call("GET", fmt.Sprintf("/formats/%d/export", formatID), nil), 200, "export renamed format")
	if exported["name"] != "Edited display name" {
		t.Fatal("export replaced editable display name with stable key", exported)
	}
	se.want(se.call("POST", "/format_sets", map[string]any{"generation": se.generation(), "key": "editor-set", "config": map[string]any{"flavour": "radarr", "formats": map[string]any{"Alias": map[string]string{"ref": "Editor format"}}}}), 200, "create format set")
	uses := se.want(se.call("DELETE", fmt.Sprintf("/formats/%d", formatID), map[string]any{"generation": se.generation()}), 422, "in-use format")
	if !strings.Contains(fmt.Sprint(uses["uses"]), "editor-set") {
		t.Fatal("missing format user", uses)
	}
	setID := qualityID(t, se, "format_sets", "editor-set")
	se.want(se.call("DELETE", fmt.Sprintf("/format_sets/%d", setID), map[string]any{"generation": se.generation()}), 200, "delete set")
	se.want(se.call("DELETE", fmt.Sprintf("/formats/%d", formatID), map[string]any{"generation": se.generation()}), 200, "delete format")
	snap, err := se.coord.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	version := ""
	for key, v := range snap.Policy.Versions {
		if v.Kind == policy.KindMovie {
			version = key
			break
		}
	}
	if version == "" {
		t.Fatal("movie version absent")
	}
	inUse := qualityID(t, se, "profiles", snap.Policy.Versions[version].Profile)
	uses = se.want(se.call("DELETE", fmt.Sprintf("/profiles/%d", inUse), map[string]any{"generation": se.generation()}), 422, "in-use profile")
	if !strings.Contains(fmt.Sprint(uses["uses"]), version) {
		t.Fatal("missing profile user")
	}
	namingID := qualityID(t, se, "naming", version)
	config := map[string]any{"file": "{Movie Title} ({Release Year})", "folder": "{Movie Title}"}
	preview := se.want(se.call("POST", "/naming/preview", map[string]any{"generation": se.generation(), "version": version, "naming": config}), 200, "unsaved naming preview")
	if !strings.Contains(fmt.Sprint(preview["file"]), "Example Movie (2026)") {
		t.Fatal("preview did not use naming port", preview)
	}
	se.want(se.call("PUT", fmt.Sprintf("/naming/%d", namingID), map[string]any{"generation": se.generation(), "config": config}), 200, "save naming")
	preview = se.want(se.call("POST", "/naming/preview", map[string]any{"generation": se.generation(), "version": version, "naming": map[string]any{"file": "{Movie Title}"}}), 200, "preview replacement naming")
	if preview["folder"] != "Example Movie (2026)" {
		t.Fatal("preview retained fields the replacement save would clear", preview)
	}
	bundle := se.want(se.call("GET", "/export/files", nil), 200, "export bundle")
	if len(bundle["files"].(map[string]any)) == 0 || !strings.Contains(bundle["yaml"].(string), "file:") {
		t.Fatal("missing materialized export files")
	}
	se.want(se.call("DELETE", fmt.Sprintf("/profiles/%d", profileID), map[string]any{"generation": se.generation()}), 200, "delete unused profile")
}

func TestQualitySettingsAdminCSRFAndIfMatchSync(t *testing.T) {
	t.Parallel()
	se := newQualityEnv(t)
	user := se.account("quality-viewer", auth.RoleUser, auth.StatusActive, true)
	userCookie, userCSRF := se.session(user)
	for _, path := range []string{"/profiles", "/formats", "/format_sets", "/naming", "/sync_sources", "/sync/reviews"} {
		r := httptest.NewRequest("GET", APIBase+"/settings"+path, nil)
		if rec := se.do(r); rec.Code != http.StatusUnauthorized {
			t.Fatalf("unprotected read %s: %d", path, rec.Code)
		}
		r = httptest.NewRequest("GET", APIBase+"/settings"+path, nil)
		r.AddCookie(userCookie)
		if rec := se.do(r); rec.Code != http.StatusForbidden {
			t.Fatalf("nonadmin read %s: %d", path, rec.Code)
		}
		writePath := path
		if path == "/sync/reviews" {
			writePath += "/1/apply"
		}
		r = httptest.NewRequest("POST", APIBase+"/settings"+writePath, strings.NewReader(`{}`))
		r.AddCookie(userCookie)
		r.Header.Set(auth.CSRFHeader, userCSRF)
		if rec := se.do(r); rec.Code != http.StatusForbidden && rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("nonadmin write %s: %d", path, rec.Code)
		}
		r = httptest.NewRequest("POST", APIBase+"/settings"+writePath, strings.NewReader(`{}`))
		r.AddCookie(se.cookie)
		r.Header.Set("Content-Type", "application/json")
		if rec := se.do(r); rec.Code != http.StatusForbidden && rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("missing CSRF accepted %s: %d", path, rec.Code)
		}
	}
	// If-Match is a complete generation precondition; a body generation is optional for previews and approval.
	r := httptest.NewRequest("POST", APIBase+"/settings/sync/preview", strings.NewReader(`{"key":"local","config":{"kind":"trash","flavour":"radarr"}}`))
	r.AddCookie(se.cookie)
	r.Header.Set(auth.CSRFHeader, se.csrf)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("If-Match", generationETag(se.generation()))
	se.want(se.do(r), 200, "If-Match sync preview")
	se.want(se.call("POST", "/sync_sources", map[string]any{"generation": se.generation(), "key": "daily", "config": map[string]any{"kind": "trash", "flavour": "radarr"}}), 200, "create sync source")
	id := qualityID(t, se, "sync_sources", "daily")
	se.want(se.call("POST", fmt.Sprintf("/sync_sources/%d/run", id), map[string]any{"generation": se.generation()}), 200, "run sync source")
	res := se.call("GET", "/sync/runs", nil)
	se.want(res, 200, "sync runs")
	if !strings.Contains(res.Body.String(), "unchanged") {
		t.Fatal("run not recorded")
	}
	res = se.call("GET", "/formats/schema", nil)
	se.want(res, 200, "format fields schema")
	if !strings.Contains(res.Body.String(), "ReleaseTitleSpecification") {
		t.Fatal("missing specification schema")
	}
	r = httptest.NewRequest("DELETE", fmt.Sprintf("%s/settings/sync_sources/%d", APIBase, id), strings.NewReader(`{}`))
	r.AddCookie(se.cookie)
	r.Header.Set(auth.CSRFHeader, se.csrf)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("If-Match", generationETag(se.generation()))
	se.want(se.do(r), 200, "If-Match delete sync source")
}

func TestQualitySettingsYAMLImportPreviewAndApply(t *testing.T) {
	t.Parallel()
	se := newQualityEnv(t)
	se.want(se.call("POST", "/sync_sources", map[string]any{"generation": se.generation(), "key": "keep-sync", "config": map[string]any{"kind": "trash", "flavour": "radarr"}}), 200, "create sync source")
	before, err := se.coord.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	const imported = `profiles:
  imported-profile:
    qualities: [WEBDL-1080p]
    cutoff: WEBDL-1080p
    format_set: imported-set
    formats: {Imported: 42}
formats:
  imported-format: {file: formats/imported.json, flavour: radarr}
format_sets:
  imported-set:
    flavour: radarr
    formats:
      Imported: {ref: imported-format}
`
	if err := os.WriteFile(filepath.Join(se.dir, "quality-import.yaml"), []byte(imported), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := []byte(`{"name":"Imported","trash_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","includeCustomFormatWhenRenaming":true,"specifications":[{"name":"Group","implementation":"ReleaseTitleSpecification","negate":false,"required":false,"fields":{"value":"NTb"}}]}`)
	if err := os.WriteFile(filepath.Join(se.dir, "formats", "imported.json"), spec, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(se.dir, "versions.yaml")
	versions, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	versions = []byte(strings.Replace(string(versions), `folder: "{Movie CleanTitle} ({Release Year}) {imdb-{ImdbId}}"`, `folder: "{Movie Title} ({Release Year})"`, 1))
	if err := os.WriteFile(path, versions, 0o600); err != nil {
		t.Fatal(err)
	}
	res := se.call("POST", "/import/preview", map[string]any{"generation": before.Generation})
	se.want(res, 200, "preview quality import")
	var preview SettingsImportPreview
	if err := json.Unmarshal(res.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	for _, entity := range []string{"profiles", "formats", "format_sets", "naming"} {
		found := false
		for _, change := range preview.Changes {
			found = found || change.Entity == entity
		}
		if !found {
			t.Fatalf("preview missing %s: %+v", entity, preview.Changes)
		}
	}
	if len(preview.Files) == 0 || !strings.Contains(res.Body.String(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") || se.generation() != before.Generation {
		t.Fatal("preview lost specifications or saved a generation")
	}
	body := map[string]any{"generation": before.Generation, "orphan_active_downloads": false}
	proof := se.want(se.call("POST", "/import/confirm", body), 200, "confirm import")
	body["confirmation_token"] = proof["token"]

	se.want(se.call("POST", "/import", body), 200, "apply quality import")
	after, err := se.coord.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after.Generation != before.Generation+1 || after.Policy.Profiles["imported-profile"].Formats["Imported"] != 42 || after.Policy.Versions["movie-remux"].Naming.Folder != "{Movie Title} ({Release Year})" {
		t.Fatal("import did not publish all quality changes in one generation")
	}
	if !reflect.DeepEqual(before.Records.SyncSources, after.Records.SyncSources) || before.Policy.Router.Enabled != after.Policy.Router.Enabled || before.Policy.Library.Enabled != after.Policy.Library.Enabled || !reflect.DeepEqual(before.Policy.Notify, after.Policy.Notify) {
		t.Fatal("import changed sync sources or write gates")
	}
	for key, version := range before.Policy.Versions {
		if after.Policy.Versions[key].Mode != version.Mode {
			t.Fatalf("import changed version %s mode", key)
		}
	}
	if err := os.Remove(filepath.Join(se.dir, "formats", "imported.json")); err != nil {
		t.Fatal(err)
	}
	exported := se.call("GET", fmt.Sprintf("/formats/%d/export", qualityID(t, se, "formats", "imported-format")), nil)
	se.want(exported, 200, "export imported specification after file removal")
	if !strings.Contains(exported.Body.String(), "NTb") {
		t.Fatal("import retained a file reference instead of JSON")
	}
}

func TestQualitySettingsTrashLinkPreviewAndApply(t *testing.T) {
	t.Parallel()
	se := newQualityEnv(t)
	raw, err := os.ReadFile("../settings/testdata/trash/radarr/b8cd450cbfa689c0259a01d9e29ba3d6.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	delete(spec, "trash_id")
	spec["name"] = "3D - Notifiarr"
	own := map[string]any{"name": "Ours", "specifications": []any{map[string]any{"name": "x", "implementation": "ReleaseTitleSpecification", "fields": map[string]any{"value": "ours-only"}}}}
	se.want(se.call("POST", "/format_sets", map[string]any{"generation": se.generation(), "key": "arr-set", "config": map[string]any{"flavour": "radarr", "formats": map[string]any{"3D - Notifiarr": map[string]any{}, "Ours": map[string]any{}}}, "specifications": map[string]any{"3D - Notifiarr": spec, "Ours": own}}), 200, "create format set")
	se.want(se.call("POST", "/sync_sources", map[string]any{"generation": se.generation(), "key": "trash-radarr", "config": map[string]any{"kind": "trash", "flavour": "radarr"}}), 200, "create sync source")
	id := qualityID(t, se, "sync_sources", "trash-radarr")
	gen := se.generation()
	res := se.call("POST", fmt.Sprintf("/sync_sources/%d/link/preview", id), map[string]any{"generation": gen})
	se.want(res, 200, "link preview")
	var p settings.TrashLinkPreview
	if err := json.Unmarshal(res.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, item := range p.Items {
		if item.Set == "arr-set" {
			status[item.Key] = item.Status + " " + item.TrashID
		}
	}
	if status["3D - Notifiarr"] != "matched b8cd450cbfa689c0259a01d9e29ba3d6" || status["Ours"] != "unmatched " || se.generation() != gen {
		t.Fatalf("link preview: %v", status)
	}
	bad := se.call("POST", fmt.Sprintf("/sync_sources/%d/link/apply", id), map[string]any{"generation": gen, "commit": p.Commit, "links": []any{map[string]any{"set": "arr-set", "key": "Ours", "trash_id": "b8cd450cbfa689c0259a01d9e29ba3d6"}}})
	se.want(bad, 422, "own format link refused")
	res = se.call("POST", fmt.Sprintf("/sync_sources/%d/link/apply", id), map[string]any{"generation": gen, "commit": p.Commit, "links": []any{map[string]any{"set": "arr-set", "key": "3D - Notifiarr", "trash_id": "b8cd450cbfa689c0259a01d9e29ba3d6"}}})
	se.want(res, 200, "link apply")
	if !strings.Contains(res.Body.String(), `"linked":1`) || se.generation() != gen+1 {
		t.Fatalf("link apply: %s", res.Body.String())
	}
	snap, err := se.coord.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ref := snap.Policy.FormatSets["arr-set"].Formats["3D - Notifiarr"]; ref == nil || ref.TrashID != "b8cd450cbfa689c0259a01d9e29ba3d6" {
		t.Fatal("link not stored on the set format")
	}
	se.want(se.call("POST", fmt.Sprintf("/sync_sources/%d/link/apply", id), map[string]any{"generation": gen, "commit": p.Commit, "links": []any{}}), 409, "stale link apply")
}

// A held score of a bound profile is taken or kept through its own endpoint, never approved as a
// whole, and a sync source edit neither sets nor drops the score state Theatarr keeps.
func TestQualitySyncHeldScoresAPI(t *testing.T) {
	t.Parallel()
	se := newQualityEnv(t)
	profileID := qualityID(t, se, "profiles", "remux-2160p")
	var profile struct {
		Config map[string]any `json:"config"`
	}
	res := se.call("GET", fmt.Sprintf("/profiles/%d", profileID), nil)
	se.want(res, 200, "read profile")
	if err := json.Unmarshal(res.Body.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	profile.Config["formats"].(map[string]any)["Remux Tier 02"] = 1800 // ours; TRaSH's default is 1900
	se.want(se.call("PUT", fmt.Sprintf("/profiles/%d", profileID), map[string]any{"generation": se.generation(), "config": profile.Config}), 200, "our score")
	config := map[string]any{"kind": "trash", "flavour": "radarr", "mode": "auto", "profiles": map[string]string{"remux-2160p": "default"}, "score_state": map[string]any{"remux-2160p": map[string]any{"set": "default", "scores": map[string]any{"Remux Tier 02": map[string]any{"trash": 1800}}}},
		"field_state": map[string]any{"remux-2160p": map[string]any{"min_score": map[string]any{"upstream": 5}}}}
	se.want(se.call("POST", "/sync_sources", map[string]any{"generation": se.generation(), "key": "guides", "config": config}), 200, "create sync source")
	id := qualityID(t, se, "sync_sources", "guides")
	se.want(se.call("POST", fmt.Sprintf("/sync_sources/%d/run", id), map[string]any{"generation": se.generation()}), 200, "run")
	var reviews []settings.QualityPreview
	res = se.call("GET", "/sync/reviews", nil)
	se.want(res, 200, "reviews")
	if err := json.Unmarshal(res.Body.Bytes(), &reviews); err != nil {
		t.Fatal(err)
	}
	// The client's score_state was ignored: the first run asks about our differing score.
	if len(reviews) != 1 || reviews[0].Kind != settings.ReviewScores || len(reviews[0].Held) != 1 || reviews[0].Held[0].Format != "Remux Tier 02" || reviews[0].Held[0].Trash != 1900 {
		t.Fatalf("held review: %+v", reviews)
	}
	held := reviews[0]
	se.want(se.call("POST", fmt.Sprintf("/sync/reviews/%d/apply", held.ID), map[string]any{"generation": se.generation()}), 422, "approve held scores as a whole")
	path := fmt.Sprintf("/sync/reviews/%d/scores", held.ID)
	se.want(se.call("POST", path, map[string]any{"generation": se.generation(), "decisions": []map[string]string{{"profile": "remux-2160p", "format": "Remux Tier 02", "choice": "maybe"}}}), 422, "unknown choice")
	se.want(se.call("POST", "/sync/reviews/999999/scores", map[string]any{"generation": se.generation(), "decisions": []map[string]string{}}), 404, "missing review")
	se.want(se.call("POST", path, map[string]any{"generation": se.generation(), "decisions": []map[string]string{{"profile": "remux-2160p", "format": "Remux Tier 02", "choice": "keep"}}}), 200, "keep ours")
	pinned := settings.TrashScoreState{Trash: 1900, Pinned: true}
	state := func() settings.TrashScoreState {
		snap, err := se.coord.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return snap.Records.SyncSources[0].Config.ScoreState["remux-2160p"].Scores["Remux Tier 02"]
	}
	if st := state(); st != pinned {
		t.Fatalf("keep not remembered: %+v", st)
	}
	// An edit of the source keeps the stored state, whatever the client sends.
	config["score_state"] = map[string]any{}
	config["mode"] = "review"
	se.want(se.call("PUT", fmt.Sprintf("/sync_sources/%d", id), map[string]any{"generation": se.generation(), "config": config}), 200, "edit source")
	if st := state(); st != pinned {
		t.Fatalf("edit dropped the score state: %+v", st)
	}
	// The field state is Theatarr's too: the client's never lands.
	if snap, _ := se.coord.Snapshot(context.Background()); snap.Records.SyncSources[0].Config.FieldState != nil {
		t.Fatalf("client field_state stored: %+v", snap.Records.SyncSources[0].Config.FieldState)
	}
}

// An unknown config field in a quality record write (or a naming preview) is the caller's mistake: 400, not 500.
func TestQualityRecordUnknownFieldIsBadRequest(t *testing.T) {
	t.Parallel()
	se := newQualityEnv(t)
	profile := map[string]any{"qualities": []string{"WEBDL-1080p"}, "cutoff": "WEBDL-1080p", "formats": map[string]int{}, "bogus": 1}
	res := se.call("POST", "/profiles", map[string]any{"generation": se.generation(), "key": "editor", "config": profile})
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "bogus") {
		t.Fatalf("unknown profile field: %d %s", res.Code, res.Body.String())
	}
	snap, err := se.coord.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	version := ""
	for key := range snap.Policy.Versions {
		version = key
		break
	}
	res = se.call("POST", "/naming/preview", map[string]any{"generation": se.generation(), "version": version, "naming": map[string]any{"bogus": "x"}})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("unknown naming field: %d %s", res.Code, res.Body.String())
	}
}

// An imported format's own input (its flavour, trash_id or file) is checked before it is materialized: a wrong
// value is a field-path refusal that neither echoes nor logs it, and a format naming both a TRaSH id and a file is
// refused rather than silently taking the TRaSH one. A format that cannot be materialized is refused at its path too.
func TestQualityImportRefusesInvalidFormatInput(t *testing.T) {
	t.Parallel()
	const marker = "secret-marker-7f3a"
	for _, tc := range []struct {
		name, to, want string
	}{
		{"flavour", "{trash_id: 3a3ff47579026e76d6504ebea39390de, flavour: " + marker + "}",
			`{"detail":["formats[Remux Tier 01].flavour: invalid policy value"],"error":"settings refused: formats[Remux Tier 01].flavour: invalid policy value","fields":[{"message":"invalid policy value","path":"formats[Remux Tier 01].flavour"}]}`},
		{"trash id and file", "{trash_id: 3a3ff47579026e76d6504ebea39390de, flavour: radarr, file: formats/seadex.json}",
			`{"detail":["formats[Remux Tier 01]: invalid policy value"],"error":"settings refused: formats[Remux Tier 01]: invalid policy value","fields":[{"message":"invalid policy value","path":"formats[Remux Tier 01]"}]}`},
		// Valid input the TRaSH tree cannot materialize: refused at the format's path, not answered 500.
		{"trash id not in the tree", "{trash_id: ffffffffffffffffffffffffffffffff, flavour: radarr}",
			`{"detail":["formats[Remux Tier 01]: TRaSH format is missing"],"error":"settings refused: formats[Remux Tier 01]: TRaSH format is missing","fields":[{"message":"TRaSH format is missing","path":"formats[Remux Tier 01]"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			se := newQualityEnv(t)
			path := filepath.Join(se.dir, "formats.yaml")
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			edited := strings.Replace(string(b), "{trash_id: 3a3ff47579026e76d6504ebea39390de, flavour: radarr}", tc.to, 1)
			if edited == string(b) {
				t.Fatal("formats.yaml has no Remux Tier 01")
			}
			if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
				t.Fatal(err)
			}
			gen := se.generation()
			res := se.call("POST", "/import/preview", map[string]any{"generation": gen})
			if res.Code != http.StatusUnprocessableEntity || strings.TrimSpace(res.Body.String()) != tc.want {
				t.Fatalf("preview: %d %s", res.Code, res.Body.String())
			}
			if strings.Contains(se.logs.String(), marker) {
				t.Fatalf("the supplied value was logged: %s", se.logs.String())
			}
			if se.generation() != gen {
				t.Fatal("a refused import saved a generation")
			}
		})
	}
}
