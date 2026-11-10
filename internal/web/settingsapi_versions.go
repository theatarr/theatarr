package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/policy"
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
	for _, field := range []string{"indexers", "providers", "golive_done"} {
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
	writeJSON(rw, http.StatusOK, map[string]any{"source": source, "generation": gen, "versions": records,
		"profiles": profiles, "format_sets": formatSets, "clients": clients, "indexers": indexers, "providers": providers,
		"switches": w.settingsSwitches()})
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
	req := settings.SaveRequest{ExpectedGeneration: snap.Generation, Records: cloneRecords(snap.Records), ConfirmationToken: body.ConfirmationToken}
	index := -1
	var record settings.Version
	for i, existing := range req.Records.Versions {
		if existing.Key == body.Key {
			index, record = i, existing
		}
	}
	if create && index >= 0 {
		return req, bodyError("library already exists")
	}
	if !create && index < 0 {
		return req, errNoRecord
	}
	if body.Delete {
		req.Records.Versions = append(req.Records.Versions[:index:index], req.Records.Versions[index+1:]...)
		// Its naming record (a quality record) goes with it; a version naming it in from_version still refuses the delete.
		if req.Records.Naming != nil {
			req.Records.Naming = slices.DeleteFunc(req.Records.Naming, func(n settings.VersionNaming) bool { return n.Key == body.Key })
		}
		return req, nil
	}
	if create {
		record.Record = settings.Record{Key: body.Key}
		record.Config.Mode = policy.ModeShadow
	}
	if len(body.Config) == 0 {
		return req, bodyError("config is required")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body.Config, &fields); err != nil {
		return req, bodyError("config must be a JSON object")
	}
	if _, exists := fields["naming"]; exists {
		return req, bodyError("naming is edited in the naming editor")
	}
	naming := record.Config.Naming
	if err := decodeConfig(body.Config, &record.Config); err != nil {
		return req, err
	}
	record.Config.Naming = naming
	if body.Roots != nil {
		record.Roots = append([]string(nil), (*body.Roots)...)
	}
	// A new library, or an edit that sends empty roots, writes under its library folder, as the editor shows it. An
	// edit that sends no roots keeps the stored ones.
	if (create || body.Roots != nil) && len(record.Roots) == 0 && record.Config.Library != "" {
		record.Roots = []string{record.Config.Library}
	}
	if index < 0 {
		req.Records.Versions = append(req.Records.Versions, record)
		// With the quality records owning naming, a new version gets its naming record: a follower names its files
		// after the followed version (from_version), any other version starts with the arr defaults, editable in the
		// naming editor.
		if req.Records.Naming != nil && !slices.ContainsFunc(req.Records.Naming, func(n settings.VersionNaming) bool { return n.Key == body.Key }) {
			naming := settings.VersionNaming{Record: settings.Record{Key: body.Key}}
			naming.Config.FromVersion = record.Config.Follows
			req.Records.Naming = append(req.Records.Naming, naming)
		}
	} else {
		req.Records.Versions[index] = record
	}
	return req, nil
}

func (w *Web) versionBody(rw http.ResponseWriter, r *http.Request) (*settings.Snapshot, settings.SaveRequest, bool) {
	if w.refuseUnavailable(rw) {
		return nil, settings.SaveRequest{}, false
	}
	var body versionSettingsBody
	if !decodeSettingsJSON(rw, r, &body) {
		return nil, settings.SaveRequest{}, false
	}
	if key := r.PathValue("key"); key != "" {
		if body.Key != "" && body.Key != key {
			badRequest(rw, "key does not match path")
			return nil, settings.SaveRequest{}, false
		}
		body.Key = key
	}
	if body.Key == "" {
		badRequest(rw, "key is required")
		return nil, settings.SaveRequest{}, false
	}
	snap, ok := w.settingsAt(rw, r, body.Generation)
	if !ok {
		return nil, settings.SaveRequest{}, false
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
	req, err := versionSettingsRequest(snap, body, create)
	if err != nil {
		if errors.Is(err, errNoRecord) {
			notFound(rw)
		} else {
			writeBodyError(rw, err, nil)
		}
		return nil, settings.SaveRequest{}, false
	}
	req.Actor = settingsActor(r)
	return snap, req, true
}

func (w *Web) apiSettingsVersionWrite(rw http.ResponseWriter, r *http.Request) {
	_, req, ok := w.versionBody(rw, r)
	if !ok {
		return
	}
	w.saveVersionSettings(rw, r, req)
}

func (w *Web) saveVersionSettings(rw http.ResponseWriter, r *http.Request, req settings.SaveRequest) {
	req.ConfirmationToken = confirmationToken(r, req.ConfirmationToken)
	saved, err := w.settings.SaveAndApply(r.Context(), req)
	if err != nil {
		w.settingsError(rw, r, err, nil)
		return
	}
	rw.Header().Set("ETag", generationETag(saved.Generation))
	writeJSON(rw, http.StatusOK, SettingsWrite{Generation: saved.Generation, Revision: saved.Revision})
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
	w.saveVersionSettings(rw, r, req)
}

func (w *Web) apiSettingsVersionConfirm(rw http.ResponseWriter, r *http.Request) {
	_, req, ok := w.versionBody(rw, r)
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
