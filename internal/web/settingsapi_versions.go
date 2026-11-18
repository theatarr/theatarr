package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/settings"
)

// SettingsVersionRecord uses policy field names; naming remains owned by its separate editor.
type SettingsVersionRecord struct {
	settings.Record
	Config map[string]any `json:"config"`
	Roots  []string       `json:"roots"`
}

type versionSettingsBody struct {
	Generation        *int64          `json:"generation"`
	Key               string          `json:"key"`
	Config            json.RawMessage `json:"config"`
	Roots             *[]string       `json:"roots"`
	ConfirmationToken string          `json:"confirmation_token"`
	Delete            bool            `json:"delete"`
	// SyncWith, when given, sets the library's sync group: the other libraries kept in sync with it (empty: none).
	SyncWith *[]string `json:"sync_with"`
}

func (w *Web) mountVersionSettingsAPI(mux *http.ServeMux, base string) {
	h := func(pattern string, handler http.HandlerFunc) {
		w.handle(mux, pattern, SignedIn, auth.PermAdmin, handler)
	}
	h("GET "+base+"/versions", w.apiSettingsVersions)
	h("POST "+base+"/versions", w.apiSettingsVersionWrite)
	h("GET "+base+"/versions/{key}", w.apiSettingsVersion)
	h("PUT "+base+"/versions/{key}", w.apiSettingsVersionWrite)
	h("DELETE "+base+"/versions/{key}", w.apiSettingsVersionDelete)
	h("POST "+base+"/versions/confirm", w.apiSettingsVersionConfirm)
	h("POST "+base+"/versions/validate-root", w.apiSettingsValidateRoot)
	h("GET "+base+"/versions/{key}/bring-in-sync", w.apiBringInSyncPreview)
	h("POST "+base+"/versions/{key}/bring-in-sync", w.apiBringInSync)
	h("GET "+base+"/view-bindings", w.apiSettingsViewBindings)
	h("POST "+base+"/view-bindings", w.apiSettingsViewCreate)
	h("PUT "+base+"/view-bindings/{key}", w.apiSettingsViewUpdate)
	h("DELETE "+base+"/view-bindings/{key}", w.apiSettingsViewDelete)
	h("GET "+base+"/sections", w.apiSettingsSections)
	h("PUT "+base+"/sections/{key}", w.apiSettingsSectionWrite)
}

func settingsVersionRecord(record settings.Version) SettingsVersionRecord {
	config := configMap(record.Config)
	delete(config, "naming")
	for _, field := range []string{"indexers", "providers"} {
		if config[field] == nil {
			config[field] = []string{}
		}
	}
	return SettingsVersionRecord{Record: record.Record, Config: config, Roots: append([]string(nil), record.Roots...)}
}

func (w *Web) apiSettingsVersions(rw http.ResponseWriter, r *http.Request) {
	snap, ok := w.settingsHead(rw, r)
	if !ok {
		return
	}
	pol := snap.Policy
	rw.Header().Set("ETag", generationETag(snap.Generation))
	records := []SettingsVersionRecord{}
	for _, record := range snap.Records.Versions {
		records = append(records, settingsVersionRecord(record))
	}
	profiles := map[string]string{}
	clients, indexers, providers, formatSets := []string{}, []string{}, []string{}, []string{}
	if pol != nil {
		for key, profile := range pol.Profiles {
			profiles[key] = profile.FormatSet
		}
		clients, indexers, providers, formatSets = sortedKeys(pol.Clients), sortedKeys(pol.Indexers), sortedKeys(pol.Providers), sortedKeys(pol.FormatSets)
		sort.SliceStable(providers, func(i, j int) bool { return pol.Providers[providers[i]].Order < pol.Providers[providers[j]].Order })
	}
	gen, source := int64(0), "db"
	if snap != nil {
		gen, source = snap.Generation, "db"
	}
	syncGroups := [][]string{}
	if pol != nil && pol.Library != nil && pol.Library.SyncGroups != nil {
		syncGroups = pol.Library.SyncGroups
	}
	writeJSON(rw, http.StatusOK, map[string]any{"source": source, "generation": gen, "versions": records,
		"profiles": profiles, "format_sets": formatSets, "clients": clients, "indexers": indexers, "providers": providers,
		"switches": w.settingsSwitches(), "sync_groups": syncGroups})
}

func (w *Web) apiSettingsVersion(rw http.ResponseWriter, r *http.Request) {
	if w.refuseUnavailable(rw) {
		return
	}
	snap, ok := w.settingsHead(rw, r)
	if !ok {
		return
	}
	for _, record := range snap.Records.Versions {
		if record.Key == r.PathValue("key") {
			rw.Header().Set("ETag", generationETag(snap.Generation))
			writeJSON(rw, http.StatusOK, map[string]any{"generation": snap.Generation, "record": settingsVersionRecord(record)})
			return
		}
	}
	notFound(rw)
}

func versionSettingsRequest(snap *settings.Snapshot, body versionSettingsBody, create bool) (settings.SaveRequest, error) {
	req, _, err := versionSettingsSave(snap, body, create)
	return req, err
}

// versionSettingsSave is versionSettingsRequest that also returns the libraries the saved library was kept in sync
// with before the save and is not after it (the group's pruning or its sync_with), sorted.
func versionSettingsSave(snap *settings.Snapshot, body versionSettingsBody, create bool) (settings.SaveRequest, []string, error) {
	req := settings.SaveRequest{ExpectedGeneration: snap.Generation, Records: cloneRecords(snap.Records), ConfirmationToken: body.ConfirmationToken}
	index := -1
	var record settings.Version
	for i, existing := range req.Records.Versions {
		if existing.Key == body.Key {
			index, record = i, existing
		}
	}
	if create && index >= 0 {
		return req, nil, bodyError("library already exists")
	}
	if !create && index < 0 {
		return req, nil, errNoRecord
	}
	if body.Delete {
		req.Records.Versions = append(req.Records.Versions[:index:index], req.Records.Versions[index+1:]...)
		// Its naming record (a quality record) goes with it.
		if req.Records.Naming != nil {
			req.Records.Naming = slices.DeleteFunc(req.Records.Naming, func(n settings.VersionNaming) bool { return n.Key == body.Key })
		}
		// And its place in a sync group.
		_, err := syncGroupsFollow(snap, &req, body)
		return req, nil, err
	}
	if create {
		record.Record = settings.Record{Key: body.Key}
	}
	if len(body.Config) == 0 {
		return req, nil, bodyError("config is required")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body.Config, &fields); err != nil {
		return req, nil, bodyError("config must be a JSON object")
	}
	if _, exists := fields["naming"]; exists {
		return req, nil, bodyError("naming is edited in the naming editor")
	}
	naming := record.Config.Naming
	if err := decodeConfig(body.Config, &record.Config); err != nil {
		return req, nil, err
	}
	record.Config.Naming = naming
	stored := record.Roots
	if body.Roots != nil && (len(*body.Roots) > 0 || len(stored) > 0) {
		record.Roots = append([]string(nil), (*body.Roots)...)
	}
	// A library without roots writes under its library folder, as the editor shows it, or every import it grabs
	// would be refused.
	if len(record.Roots) == 0 && record.Config.Library != "" {
		record.Roots = []string{record.Config.Library}
	}
	if index < 0 {
		req.Records.Versions = append(req.Records.Versions, record)
		// With the quality records owning naming, a new version gets its naming record: empty, the arr defaults,
		// editable in the naming editor.
		if req.Records.Naming != nil && !slices.ContainsFunc(req.Records.Naming, func(n settings.VersionNaming) bool { return n.Key == body.Key }) {
			req.Records.Naming = append(req.Records.Naming, settings.VersionNaming{Record: settings.Record{Key: body.Key}})
		}
	} else {
		req.Records.Versions[index] = record
	}
	dropped, err := syncGroupsFollow(snap, &req, body)
	return req, dropped, err
}

// syncGroupsFollow keeps the stored sync groups (library.sync_groups) in step with a library save: a deleted library
// leaves its group, as does one whose new kind or anime setting no longer matches the group; then sync_with, when
// given, sets the library's group. The save carries the library section only when its groups change. It returns the
// libraries the saved library was in sync with before the save and is not after it, whatever the cause, sorted.
func syncGroupsFollow(snap *settings.Snapshot, req *settings.SaveRequest, body versionSettingsBody) ([]string, error) {
	i := slices.IndexFunc(snap.Records.Sections, func(s settings.Section) bool { return s.Key == "library" })
	if i < 0 && body.SyncWith == nil {
		return nil, nil
	}
	records := settings.Records{Versions: req.Records.Versions}
	var before []byte
	if i >= 0 {
		records.Sections = []settings.Section{snap.Records.Sections[i]}
		before = snap.Records.Sections[i].Config
	}
	partners := func() []string {
		for _, g := range settings.SyncGroupsOf(&records) {
			if slices.Contains(g, body.Key) {
				return slices.DeleteFunc(slices.Clone(g), func(k string) bool { return k == body.Key })
			}
		}
		return nil
	}
	had := partners()
	if err := settings.PruneSyncGroups(&records); err != nil {
		return nil, err
	}
	if body.SyncWith != nil && !body.Delete {
		if err := settings.SetSyncGroup(&records, body.Key, *body.SyncWith); err != nil {
			return nil, err
		}
	}
	var dropped []string
	if !body.Delete {
		kept := partners()
		dropped = slices.DeleteFunc(had, func(k string) bool { return slices.Contains(kept, k) })
		slices.Sort(dropped)
	}
	if len(records.Sections) == 1 && !bytes.Equal(records.Sections[0].Config, before) {
		req.Records.Sections = append(req.Records.Sections, records.Sections[0])
	}
	return dropped, nil
}

func (w *Web) versionBody(rw http.ResponseWriter, r *http.Request) (settings.SaveRequest, []string, bool) {
	if w.refuseUnavailable(rw) {
		return settings.SaveRequest{}, nil, false
	}
	var body versionSettingsBody
	if !decodeSettingsJSON(rw, r, &body) {
		return settings.SaveRequest{}, nil, false
	}
	if key := r.PathValue("key"); key != "" {
		if body.Key != "" && body.Key != key {
			badRequest(rw, "key does not match path")
			return settings.SaveRequest{}, nil, false
		}
		body.Key = key
	}
	if body.Key == "" {
		badRequest(rw, "key is required")
		return settings.SaveRequest{}, nil, false
	}
	snap, ok := w.settingsAt(rw, r, body.Generation)
	if !ok {
		return settings.SaveRequest{}, nil, false
	}
	create := r.Method == http.MethodPost && r.PathValue("key") == "" && !body.Delete
	if r.URL.Path == APIBase+"/settings/versions/confirm" {
		create = true
		for _, record := range snap.Records.Versions {
			if record.Key == body.Key {
				create = false
			}
		}
	}
	req, dropped, err := versionSettingsSave(snap, body, create)
	if err != nil {
		if errors.Is(err, errNoRecord) {
			notFound(rw)
		} else {
			writeBodyError(rw, err, nil)
		}
		return settings.SaveRequest{}, nil, false
	}
	req.Actor = settingsActor(r)
	return req, dropped, true
}

func (w *Web) apiSettingsVersionWrite(rw http.ResponseWriter, r *http.Request) {
	req, dropped, ok := w.versionBody(rw, r)
	if !ok {
		return
	}
	w.saveVersionSettings(rw, r, req, dropped)
}

// saveVersionSettings saves a library; syncDropped (the libraries it is no longer kept in sync with) goes into the answer.
func (w *Web) saveVersionSettings(rw http.ResponseWriter, r *http.Request, req settings.SaveRequest, syncDropped []string) {
	req.ConfirmationToken = confirmationToken(r, req.ConfirmationToken)
	saved, err := w.settings.SaveAndApply(r.Context(), req)
	if err != nil {
		w.settingsError(rw, r, err, nil)
		return
	}
	rw.Header().Set("ETag", generationETag(saved.Generation))
	writeJSON(rw, http.StatusOK, SettingsWrite{Generation: saved.Generation, Revision: saved.Revision, SyncDropped: syncDropped})
}

func (w *Web) apiSettingsVersionDelete(rw http.ResponseWriter, r *http.Request) {
	if w.refuseUnavailable(rw) {
		return
	}
	gen, err := strconv.ParseInt(r.URL.Query().Get("generation"), 10, 64)
	if err != nil {
		badRequest(rw, "generation is required")
		return
	}
	// A token in the URL ends up in access logs and browser history: it goes in the X-Confirmation-Token header.
	if r.URL.Query().Has("confirmation_token") {
		badRequest(rw, "send confirmation_token in the "+ConfirmationHeader+" header, not the URL")
		return
	}
	snap, ok := w.settingsAt(rw, r, &gen)
	if !ok {
		return
	}
	req, err := versionSettingsRequest(snap, versionSettingsBody{Key: r.PathValue("key"), Delete: true,
		ConfirmationToken: r.Header.Get(ConfirmationHeader)}, false)
	if err != nil {
		notFound(rw)
		return
	}
	req.Actor = settingsActor(r)
	w.saveVersionSettings(rw, r, req, nil)
}

func (w *Web) apiSettingsVersionConfirm(rw http.ResponseWriter, r *http.Request) {
	req, _, ok := w.versionBody(rw, r)
	if !ok {
		return
	}
	confirmation, err := w.settings.Confirm(r.Context(), req)
	if err != nil {
		w.settingsError(rw, r, err, nil)
		return
	}
	writeJSON(rw, http.StatusOK, confirmation)
}

func (w *Web) apiSettingsImportConfirm(rw http.ResponseWriter, r *http.Request) {
	if w.refuseUnavailable(rw) {
		return
	}
	var body struct {
		Generation            *int64 `json:"generation"`
		OrphanActiveDownloads bool   `json:"orphan_active_downloads"`
	}
	if !decodeSettingsJSON(rw, r, &body) {
		return
	}
	if _, ok := w.settingsAt(rw, r, body.Generation); !ok {
		return
	}
	confirmation, err := w.settings.ConfirmImport(r.Context(), settings.ImportRequest{ExpectedGeneration: *body.Generation,
		Dir: w.policyDir, Lookup: w.settingsLookup, Actor: settingsActor(r), OrphanActiveDownloads: body.OrphanActiveDownloads})
	if err != nil {
		w.settingsError(rw, r, err, nil)
		return
	}
	writeJSON(rw, http.StatusOK, confirmation)
}

func (w *Web) apiSettingsValidateRoot(rw http.ResponseWriter, r *http.Request) {
	if w.refuseUnavailable(rw) {
		return
	}
	var body struct {
		Generation *int64 `json:"generation"`
		Key        string `json:"key"`
		Root       string `json:"root"`
	}
	if !decodeSettingsJSON(rw, r, &body) {
		return
	}
	if _, ok := w.settingsAt(rw, r, body.Generation); !ok {
		return
	}
	checked, err := w.settings.ValidateRoot(r.Context(), body.Key, body.Root)
	if err != nil {
		w.settingsError(rw, r, err, nil)
		return
	}
	writeJSON(rw, http.StatusOK, checked)
}

func (w *Web) apiSettingsViewBindings(rw http.ResponseWriter, r *http.Request) {
	if w.refuseUnavailable(rw) {
		return
	}
	snap, ok := w.settingsHead(rw, r)
	if !ok {
		return
	}
	rw.Header().Set("ETag", generationETag(snap.Generation))
	writeJSON(rw, http.StatusOK, map[string]any{"source": "db", "generation": snap.Generation, "views": snap.Records.Views})
}
