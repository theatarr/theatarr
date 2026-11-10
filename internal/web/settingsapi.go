package web

// Settings API routing, coordinator contract, import and reload handlers.

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/settings"
)

// SettingsAdmin is the settings coordinator behind the admin settings API (*settings.RuntimeCoordinator).
type SettingsAdmin interface {
	// Snapshot is the stored head generation, which writes edit.
	Snapshot(ctx context.Context) (*settings.Snapshot, error)
	// Current is the published runtime (older than the head after a startup fallback).
	Current() *settings.Runtime
	Candidate(ctx context.Context, req settings.SaveRequest) (*settings.Snapshot, error)
	Confirm(ctx context.Context, req settings.SaveRequest) (settings.Confirmation, error)
	ConfirmImport(ctx context.Context, req settings.ImportRequest) (settings.Confirmation, error)
	ValidateRoot(ctx context.Context, key, root string) (settings.RootCheck, error)
	SaveAndApply(ctx context.Context, req settings.SaveRequest) (*settings.Snapshot, error)
	PreviewImport(ctx context.Context, req settings.ImportRequest) (*settings.Preview, error)
	ImportAndApply(ctx context.Context, req settings.ImportRequest) (*settings.Snapshot, error)
	Audit(ctx context.Context, after int64, limit int) ([]settings.Audit, error)
	AuditBefore(ctx context.Context, before int64, limit int) ([]settings.Audit, error)
	RecordAction(ctx context.Context, actor, entity, key, operation string, detail any) error
	// ProfileDefaults are the profiles "Add version" preselects per kind of version (a UI default).
	ProfileDefaults(ctx context.Context) (settings.ProfileDefaults, error)
	SetProfileDefaults(ctx context.Context, actor string, defaults settings.ProfileDefaults) (settings.ProfileDefaults, error)
}

const (
	entityIndexers  = "indexers"
	entityProviders = "providers"
	entityBudgets   = "budgets"
)

// yamlOwned lists what the settings API can never change; the overview repeats it for the UI. Everything else a
// user configures is a settings record or section (docs/design/settings.md "Sections").
var yamlOwned = []string{"the database path, policy directory and listen address (command line or environment)",
	"credentials supplied as environment variables (a stored credential wins)"}

func (w *Web) mountSettingsAPI(mux *http.ServeMux) {
	w.mountQualitySettingsAPI(mux)
	w.mountProfileSettingsAPI(mux)
	b := APIBase + "/settings"
	h := func(pattern string, fn http.HandlerFunc) { w.handle(mux, pattern, SignedIn, auth.PermAdmin, fn) }
	h("GET "+b, w.apiSettingsOverview)
	h("GET "+b+"/switches", w.apiSettingsSwitches)
	h("PUT "+b+"/switches", w.apiSettingsSwitchesWrite(false))
	h("POST "+b+"/switches/confirm", w.apiSettingsSwitchesWrite(true))
	h("GET "+b+"/schema", w.apiSettingsSchema)
	h("GET "+b+"/export", w.apiSettingsExport)
	h("GET "+b+"/audit", w.apiSettingsAudit)
	h("POST "+b+"/import/preview", w.apiSettingsImport(true))
	h("POST "+b+"/import", w.apiSettingsImport(false))
	h("POST "+b+"/import/confirm", w.apiSettingsImportConfirm)
	h("POST "+b+"/reload", w.apiSettingsReload)
	h("POST "+b+"/notify/targets/{name}/test", w.apiNotifyTargetTest)
	w.mountVersionSettingsAPI(mux, b)
	w.mountSetupStartAPI(mux)
	for _, entity := range []string{entityIndexers, entityProviders, entityBudgets} {
		p := b + "/" + entity
		h("GET "+p, w.apiSettingsList(entity))
		h("POST "+p, w.apiSettingsCreate(entity))
		h("GET "+p+"/{id}", w.apiSettingsGet(entity))
		h("PUT "+p+"/{id}", w.apiSettingsUpdate(entity))
		h("DELETE "+p+"/{id}", w.apiSettingsDelete(entity))
		if entity == entityBudgets {
			continue
		}
		h("POST "+p+"/test", w.apiSettingsTestCandidate(entity))
		h("POST "+p+"/testall", w.apiSettingsTestAll(entity))
		h("POST "+p+"/{id}/test", w.apiSettingsTest(entity))
	}
	h("PUT "+b+"/indexers/priority", w.apiSettingsPriority)
	h("PUT "+b+"/providers/order", w.apiSettingsOrder)
}

// ConfirmationHeader carries a confirmation token on writes without a JSON body (deletes). Tokens never go in the
// URL, where access logs and browser history keep them.
const ConfirmationHeader = "X-Confirmation-Token"

// confirmationToken is the request's confirmation: the body's, else the header's.
func confirmationToken(r *http.Request, body string) string {
	if body != "" {
		return body
	}
	return r.Header.Get(ConfirmationHeader)
}

// refuseUnavailable handles a missing settings service, not an alternate settings source.
func (w *Web) refuseUnavailable(rw http.ResponseWriter) bool {
	if w.settings != nil {
		return false
	}
	writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "settings service unavailable"})
	return true
}

func settingsActor(r *http.Request) string {
	id := auth.FromContext(r.Context())
	if id == nil {
		return "unknown"
	}
	name := id.Username
	if name == "" {
		name = "user:" + strconv.FormatInt(id.UserID, 10)
	}
	if id.Kind == "apikey" {
		name += " (api key " + id.KeyName + ")"
	}
	if r.Context().Value(mcpContextKey{}) != nil {
		name += " via MCP"
	}
	return name
}

// apiSettingsImport previews or applies an explicit import of the configured policy directory: its records replace
// the stored ones, stored settings sections and switches are kept, and sections the database lacks are filled from
// the files (settings.ImportRequest).
func (w *Web) apiSettingsImport(preview bool) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if w.refuseUnavailable(rw) {
			return
		}
		var body struct {
			Generation            *int64 `json:"generation"`
			OrphanActiveDownloads *bool  `json:"orphan_active_downloads"`
			ConfirmationToken     string `json:"confirmation_token"`
		}
		if !decodeSettingsJSON(rw, r, &body) {
			return
		}
		if body.Generation == nil {
			badRequest(rw, "generation is required: the settings generation this import replaces")
			return
		}
		if !preview && body.OrphanActiveDownloads == nil {
			badRequest(rw, "orphan_active_downloads is required for an import: false refuses an import that would fail active downloads, true confirms it")
			return
		}
		if w.policyDir == "" {
			writeJSON(rw, http.StatusConflict, apiError{Error: "no policy directory configured"})
			return
		}
		req := settings.ImportRequest{Dir: w.policyDir, ExpectedGeneration: *body.Generation, Actor: settingsActor(r), Lookup: w.settingsLookup,
			ConfirmationToken: confirmationToken(r, body.ConfirmationToken)}
		if preview {
			p, err := w.settings.PreviewImport(r.Context(), req)
			if err != nil {
				w.settingsError(rw, r, err, nil)
				return
			}
			writeJSON(rw, http.StatusOK, importPreview(p))
			return
		}
		req.OrphanActiveDownloads = *body.OrphanActiveDownloads
		saved, err := w.settings.ImportAndApply(r.Context(), req)
		if err != nil {
			w.settingsError(rw, r, err, nil)
			return
		}
		writeJSON(rw, http.StatusOK, SettingsWrite{Generation: saved.Generation, Revision: saved.Revision})
	}
}

// SettingsImportPreview is the dry-run diff of an import.
type SettingsImportPreview struct {
	Generation int64                       `json:"generation"`
	Changes    []SettingsChange            `json:"changes"`
	Before     map[string][]SettingsRecord `json:"before"`
	After      map[string][]SettingsRecord `json:"after"`
	// YAML is the review export of the imported result (credentials and environment names stripped).
	YAML  string                     `json:"yaml"`
	Files map[string]json.RawMessage `json:"files"`
	// SectionsNote says in one line that settings sections are kept as stored and which the files fill.
	SectionsNote string `json:"sections_note"`
	// SectionsFromFiles lists the sections the stored settings lack, which the import takes from the files.
	SectionsFromFiles []string `json:"sections_from_files"`
}

// SettingsChange is one record an import would create, update or delete.
type SettingsChange struct {
	Entity    string `json:"entity"`
	Key       string `json:"key"`
	Operation string `json:"operation"`
	// CredentialChanged: the import replaces the record's resolved credential (computed by the settings layer
	// before redaction; never a value or hash).
	CredentialChanged bool `json:"credential_changed,omitempty"`
	// Fields names what an update changes, of kind, enabled, priority, order and config (secret_set changes show as
	// credential_changed). An import keeps enabled and priority, so they appear only if that ever changes.
	Fields []string `json:"fields,omitempty"`
}

func importPreview(p *settings.Preview) SettingsImportPreview {
	views := func(rs settings.Records) map[string][]SettingsRecord {
		out := map[string][]SettingsRecord{entityIndexers: {}, entityProviders: {}, entityBudgets: {}, "versions": {}, "views": {}}
		for _, r := range rs.Indexers {
			out[entityIndexers] = append(out[entityIndexers], indexerRecord(r))
		}
		for _, r := range rs.Providers {
			out[entityProviders] = append(out[entityProviders], providerRecord(r))
		}
		for _, r := range rs.Budgets {
			out[entityBudgets] = append(out[entityBudgets], budgetRecord(r))
		}
		for _, r := range rs.Versions {
			v := settingsVersionRecord(r)
			out["versions"] = append(out["versions"], SettingsRecord{ID: r.ID, Key: r.Key,
				Config: map[string]any{"version": v.Config, "roots": v.Roots}})
		}
		for _, r := range rs.Views {
			config := map[string]any{"versions": r.Versions, "profiles": r.Profiles}
			if r.Identity != nil {
				config["flavour"], config["path"], config["alias_from"] = r.Identity.Flavour, r.Identity.Path, r.Identity.AliasFrom
			}
			out["views"] = append(out["views"], SettingsRecord{ID: r.ID, Key: r.Key, Config: config})
		}
		for _, entity := range qualityEntities {
			if entity == "sync_sources" {
				continue
			}
			out[entity] = []SettingsRecord{}
			for _, r := range qualityRecords(rs, entity) {
				out[entity] = append(out[entity], SettingsRecord{ID: r.ID, Key: r.Key, Config: r.Config, Specification: r.Specification, Specifications: r.Specifications})
			}
		}
		return out
	}
	before, after := views(p.Before), views(p.After)
	out := SettingsImportPreview{Generation: p.Generation, Changes: []SettingsChange{}, Before: before, After: after, YAML: p.YAML, Files: p.Files,
		SectionsNote: p.SectionsNote(), SectionsFromFiles: p.SectionsFromFiles}
	if out.SectionsFromFiles == nil {
		out.SectionsFromFiles = []string{}
	}
	for _, entity := range []string{entityIndexers, entityProviders, entityBudgets, "profiles", "formats", "format_sets", "naming", "versions", "views"} {
		old := map[string]SettingsRecord{}
		for _, r := range before[entity] {
			old[r.Key] = r
		}
		for _, r := range after[entity] {
			prev, ok := old[r.Key]
			delete(old, r.Key)
			switch {
			case !ok:
				out.Changes = append(out.Changes, SettingsChange{Entity: entity, Key: r.Key, Operation: "create"})
			case !sameView(prev, r) || p.CredentialChanged[entity+"/"+r.Key]:
				out.Changes = append(out.Changes, SettingsChange{Entity: entity, Key: r.Key, Operation: "update",
					CredentialChanged: p.CredentialChanged[entity+"/"+r.Key], Fields: changedFields(prev, r)})
			}
		}
		for _, k := range sortedKeys(old) {
			out.Changes = append(out.Changes, SettingsChange{Entity: entity, Key: k, Operation: "delete"})
		}
	}
	return out
}

// changedFields lists the record fields that differ between two views of one record.
func changedFields(a, b SettingsRecord) []string {
	var out []string
	differ := func(x, y any) bool {
		p, _ := json.Marshal(x)
		q, _ := json.Marshal(y)
		return string(p) != string(q)
	}
	// The config view repeats kind and order; they are named on their own.
	config := func(m map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range m {
			if k != "kind" && k != "order" {
				out[k] = v
			}
		}
		return out
	}
	for _, f := range []struct {
		name string
		x, y any
	}{{"kind", a.Kind, b.Kind}, {"enabled", a.Enabled, b.Enabled}, {"priority", a.Priority, b.Priority},
		{"order", a.Order, b.Order}, {"config", config(a.Config), config(b.Config)}, {"specification", a.Specification, b.Specification}, {"specifications", a.Specifications, b.Specifications}} {
		if differ(f.x, f.y) {
			out = append(out, f.name)
		}
	}
	return out
}

func sameView(a, b SettingsRecord) bool {
	a.ID, b.ID, a.CreatedAt, b.CreatedAt, a.UpdatedAt, b.UpdatedAt = 0, 0, nil, nil, nil, nil
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// apiSettingsReload republishes the stored head generation, for example after a startup fallback left an older
// generation active: {"generation": n} must be the head. A reload is a provider transition like a save: when it
// disables or removes a provider with active downloads it is refused unless orphan_active_downloads is true
// (omitted is false; nothing else changes for a reload of the active generation). Write-enabling effects of the head
// over the active runtime need confirmation_token, as for a save.
func (w *Web) apiSettingsReload(rw http.ResponseWriter, r *http.Request) {
	if w.refuseUnavailable(rw) {
		return
	}
	var body struct {
		Generation            *int64 `json:"generation"`
		OrphanActiveDownloads bool   `json:"orphan_active_downloads"`
		ConfirmationToken     string `json:"confirmation_token"`
	}
	if !decodeSettingsJSON(rw, r, &body) {
		return
	}
	if body.Generation == nil {
		badRequest(rw, "generation is required")
		return
	}
	if w.reloadSettings == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "settings reload unavailable"})
		return
	}
	// After a startup fallback the head may enable writes the active runtime does not: a 422 lists them with a
	// token, sent back as confirmation_token.
	ctx := settings.WithConfirmationToken(settings.WithOrphanActiveDownloads(r.Context(), body.OrphanActiveDownloads),
		confirmationToken(r, body.ConfirmationToken))
	if err := w.reloadSettings(ctx, *body.Generation); err != nil {
		w.settingsError(rw, r, err, nil)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]int64{"generation": *body.Generation})
}
