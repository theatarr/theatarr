package web

// The database-owned policy sections (metadata, library, notify, clients, egress, ...) and the arr-compatible views
// (docs/design/settings.md "Sections" and "Views"). A section is edited whole, as the YAML it was in the policy files;
// its credentials are write-only, by path. Saves go through SaveAndApply like every other settings write: the same
// validation as the policy files, confirmations for risky changes, and one published generation.

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/theatarr/theatarr/internal/settings"
)

// SettingsSection is one section as GET /settings/sections shows it.
type SettingsSection struct {
	Key string `json:"key"`
	// Live: the change applies without a restart. Otherwise services built at startup keep the old value until
	// the service restarts.
	Live bool `json:"live"`
	// Config is the section with policy field names; null is a section left out. YAML is the same as text.
	Config  json.RawMessage `json:"config"`
	YAML    string          `json:"yaml"`
	Secrets []SectionSecret `json:"secrets"`
}

// SectionSecret is one credential field of a section: its path, whether a value is stored, and the environment
// variable that is the fallback while none is.
type SectionSecret struct {
	Path string `json:"path"`
	Set  bool   `json:"set"`
	Env  string `json:"env,omitempty"`
}

func (w *Web) apiSettingsSections(rw http.ResponseWriter, r *http.Request) {
	out := []SettingsSection{}
	if w.refuseUnavailable(rw) {
		return
	}
	snap, ok := w.settingsHead(rw, r)
	if !ok {
		return
	}
	for _, spec := range settings.Sections {
		var config json.RawMessage
		var set []string
		found := false
		for _, s := range snap.Records.Sections {
			if s.Key == spec.Key {
				config, set, found = s.Config, s.SecretsSet, true
			}
		}
		if !found {
			// Not seeded yet (a database the serve bootstrap has not reached): the policy files still hold it.
			var err error
			if config, err = settings.SectionConfig(snap.Policy, spec.Key); err != nil {
				w.settingsError(rw, r, err, nil)
				return
			}
		}
		out = append(out, settingsSection(spec, config, set))
	}
	rw.Header().Set("ETag", generationETag(snap.Generation))
	writeJSON(rw, http.StatusOK, map[string]any{"source": "db", "generation": snap.Generation, "sections": out})
}

func settingsSection(spec settings.SectionSpec, config json.RawMessage, set []string) SettingsSection {
	// Notify target URLs are write-only like the other credentials: reads show scheme and host only.
	config = settings.MaskSectionConfig(spec.Key, config)
	out := SettingsSection{Key: spec.Key, Live: spec.Live, Config: config, Secrets: []SectionSecret{}}
	var value any
	if json.Unmarshal(config, &value) == nil && value != nil {
		if b, err := yaml.Marshal(value); err == nil {
			out.YAML = string(b)
		}
	}
	paths, _ := settings.SectionSecretPaths(spec.Key, config)
	envs := map[string]string{}
	collectEnvs(value, "", envs)
	for _, path := range paths {
		out.Secrets = append(out.Secrets, SectionSecret{Path: path, Set: slices.Contains(set, path), Env: envs[path]})
	}
	return out
}

// collectEnvs maps each *_env value of a section by its credential path.
func collectEnvs(v any, path string, out map[string]string) {
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	for key, child := range m {
		next := key
		if path != "" {
			next = path + "." + key
		}
		if s, ok := child.(string); ok && strings.HasSuffix(key, "_env") {
			out[strings.TrimSuffix(next, "_env")] = s
			continue
		}
		collectEnvs(child, next, out)
	}
}

// sectionBody replaces one section. Config or YAML gives the whole section (both absent keeps it, an empty YAML or
// a null config leaves the section out); secrets set credentials by path and clear_secrets removes them.
type sectionBody struct {
	Generation        *int64            `json:"generation"`
	Config            json.RawMessage   `json:"config"`
	YAML              *string           `json:"yaml"`
	Secrets           map[string]string `json:"secrets"`
	ClearSecrets      []string          `json:"clear_secrets"`
	ConfirmationToken string            `json:"confirmation_token"`
}

func (w *Web) apiSettingsSectionWrite(rw http.ResponseWriter, r *http.Request) {
	if w.refuseUnavailable(rw) {
		return
	}
	key := r.PathValue("key")
	live, known := settings.SectionLive(key)
	if !known {
		notFound(rw)
		return
	}
	var body sectionBody
	if !decodeSettingsJSON(rw, r, &body) {
		return
	}
	scrub := make([]string, 0, len(body.Secrets))
	for _, v := range body.Secrets {
		scrub = append(scrub, v)
	}
	snap, ok := w.settingsAt(rw, r, body.Generation)
	if !ok {
		return
	}
	var section *settings.Section
	for _, s := range snap.Records.Sections {
		if s.Key == key {
			s := s
			section = &s
		}
	}
	if section == nil {
		// A fresh database, or an older head stored without this section: it runs on the default, so the save
		// starts from that, and the generation it creates stores every other missing section at its default too.
		config, err := settings.DefaultSection(key)
		if err != nil {
			w.internal(rw, err)
			return
		}
		section = &settings.Section{Record: settings.Record{Key: key}, Config: config}
	}
	stored := section.Config
	switch {
	case body.YAML != nil && body.Config != nil:
		badRequest(rw, "send config or yaml, not both")
		return
	case body.YAML != nil:
		var value any
		if err := yaml.Unmarshal([]byte(*body.YAML), &value); err != nil {
			writeBodyError(rw, bodyError("yaml: not valid YAML"), scrub)
			return
		}
		if value != nil {
			if _, ok := value.(map[string]any); !ok {
				writeBodyError(rw, bodyError("yaml: the section must be a mapping"), scrub)
				return
			}
		}
		b, err := json.Marshal(value)
		if err != nil {
			writeBodyError(rw, bodyError("yaml: unsupported value"), scrub)
			return
		}
		section.Config = b
	case body.Config != nil:
		section.Config = body.Config
	}
	if body.YAML != nil || body.Config != nil {
		// A target URL sent back as its mask keeps the stored URL.
		config, err := settings.UnmaskSectionConfig(key, section.Config, stored)
		if err != nil {
			w.settingsError(rw, r, err, scrub)
			return
		}
		// An optional field sent unset is left out, as an untouched section leaves it out.
		section.Config = settings.DropUnsetOptional(key, config)
	}
	if err := settings.SectionOwnedElsewhere(key, section.Config); err != nil {
		w.settingsError(rw, r, err, scrub)
		return
	}
	writes := map[string]settings.Secret{}
	for path, value := range body.Secrets {
		if value == "" {
			writeBodyError(rw, bodyError("secrets: an empty value is ambiguous; use clear_secrets"), scrub)
			return
		}
		writes[path] = settings.NewSecret(value)
	}
	for _, path := range body.ClearSecrets {
		if _, ok := writes[path]; ok {
			writeBodyError(rw, bodyError("secrets: "+path+" is both set and cleared"), scrub)
			return
		}
		writes[path] = settings.NewSecret("")
	}
	if key == "cardigann" && prowlarrKeyLeft(snap, section.Config, writes) {
		writeJSON(rw, http.StatusUnprocessableEntity, apiError{Error: "Enter the API key again for the new address"})
		return
	}
	req := settings.SaveRequest{ExpectedGeneration: snap.Generation, Actor: settingsActor(r), Records: cloneRecords(snap.Records),
		ConfirmationToken: confirmationToken(r, body.ConfirmationToken)}
	req.Records.Sections = []settings.Section{*section}
	if len(writes) > 0 {
		req.SectionSecretWrites = map[string]map[string]settings.Secret{key: writes}
	}
	saved, err := w.settings.SaveAndApply(r.Context(), req)
	if err != nil {
		w.settingsError(rw, r, err, scrub)
		return
	}
	rw.Header().Set("ETag", generationETag(saved.Generation))
	writeJSON(rw, http.StatusOK, map[string]any{"generation": saved.Generation, "revision": saved.Revision, "restart_required": !live})
}

// viewBody creates or updates a view. On update, omitted fields keep their stored values.
type viewBody struct {
	Generation        *int64               `json:"generation"`
	Key               string               `json:"key"`
	Flavour           *string              `json:"flavour"`
	Path              *string              `json:"path"`
	AliasFrom         *[]string            `json:"alias_from"`
	Versions          *[]string            `json:"versions"`
	Profiles          *map[string][]string `json:"profiles"`
	APIKey            *string              `json:"api_key"`
	ClearAPIKey       bool                 `json:"clear_api_key"`
	ConfirmationToken string               `json:"confirmation_token"`
}

func (w *Web) apiSettingsViewCreate(rw http.ResponseWriter, r *http.Request) {
	w.writeView(rw, r, true)
}

func (w *Web) apiSettingsViewUpdate(rw http.ResponseWriter, r *http.Request) {
	w.writeView(rw, r, false)
}

func (w *Web) writeView(rw http.ResponseWriter, r *http.Request, create bool) {
	if w.refuseUnavailable(rw) {
		return
	}
	var body viewBody
	if !decodeSettingsJSON(rw, r, &body) {
		return
	}
	var scrub []string
	if body.APIKey != nil {
		scrub = append(scrub, *body.APIKey)
	}
	snap, ok := w.settingsAt(rw, r, body.Generation)
	if !ok {
		return
	}
	key := r.PathValue("key")
	if create {
		key = body.Key
	} else if body.Key != "" && body.Key != key {
		writeBodyError(rw, bodyError("key: a view cannot be renamed; create a new one and delete this one"), scrub)
		return
	}
	records := cloneRecords(snap.Records)
	index := slices.IndexFunc(records.Views, func(v settings.ViewBinding) bool { return v.Key == key })
	switch {
	case create && key == "":
		writeBodyError(rw, bodyError("key: required"), scrub)
		return
	case create && index >= 0:
		writeJSON(rw, http.StatusConflict, apiError{Error: "a view with this key exists"})
		return
	case !create && index < 0:
		notFound(rw)
		return
	}
	var view settings.ViewBinding
	if create {
		view = settings.ViewBinding{Record: settings.Record{Key: key}, Versions: []string{}, Identity: &settings.ViewIdentity{}}
		if body.APIKey == nil {
			writeBodyError(rw, bodyError("api_key: required for a new view"), scrub)
			return
		}
	} else {
		view = records.Views[index]
		if view.Identity != nil {
			identity := *view.Identity
			view.Identity = &identity
		} else if body.Flavour != nil || body.Path != nil || body.AliasFrom != nil || body.APIKey != nil || body.ClearAPIKey {
			// Until the first start of this version seeds it, the view's identity and key are the policy files'.
			writeJSON(rw, http.StatusConflict, apiError{Error: "this view has not been seeded; restart the service"})
			return
		}
	}
	if body.Flavour != nil {
		view.Identity.Flavour = *body.Flavour
	}
	if body.Path != nil {
		view.Identity.Path = *body.Path
	}
	if body.AliasFrom != nil {
		view.Identity.AliasFrom = *body.AliasFrom
	}
	// Versions and profiles are replaced together, as the binding editor sends them.
	if body.Versions != nil || body.Profiles != nil {
		view.Versions, view.Profiles = nil, nil
	}
	if body.Versions != nil {
		view.Versions = *body.Versions
	}
	if body.Profiles != nil {
		view.Profiles = *body.Profiles
	}
	req := settings.SaveRequest{ExpectedGeneration: snap.Generation, Actor: settingsActor(r), ConfirmationToken: confirmationToken(r, body.ConfirmationToken)}
	switch {
	case body.APIKey != nil && body.ClearAPIKey:
		writeBodyError(rw, bodyError("api_key: set or clear, not both"), scrub)
		return
	case body.APIKey != nil && *body.APIKey == "":
		writeBodyError(rw, bodyError("api_key: an empty key is ambiguous; use clear_api_key"), scrub)
		return
	case body.APIKey != nil:
		req.SecretWrites = map[string]settings.Secret{"views/" + key: settings.NewSecret(*body.APIKey)}
	case body.ClearAPIKey:
		req.SecretWrites = map[string]settings.Secret{"views/" + key: settings.NewSecret("")}
	}
	if create {
		records.Views = append(records.Views, view)
	} else {
		records.Views[index] = view
	}
	req.Records = records
	saved, err := w.settings.SaveAndApply(r.Context(), req)
	if err != nil {
		w.settingsError(rw, r, err, scrub)
		return
	}
	rw.Header().Set("ETag", generationETag(saved.Generation))
	writeJSON(rw, http.StatusOK, SettingsWrite{Generation: saved.Generation, Revision: saved.Revision})
}

func (w *Web) apiSettingsViewDelete(rw http.ResponseWriter, r *http.Request) {
	if w.refuseUnavailable(rw) {
		return
	}
	gen, err := strconv.ParseInt(r.URL.Query().Get("generation"), 10, 64)
	if err != nil {
		badRequest(rw, "generation is required")
		return
	}
	snap, ok := w.settingsAt(rw, r, &gen)
	if !ok {
		return
	}
	records := cloneRecords(snap.Records)
	key := r.PathValue("key")
	before := len(records.Views)
	records.Views = slices.DeleteFunc(records.Views, func(v settings.ViewBinding) bool { return v.Key == key })
	if len(records.Views) == before {
		notFound(rw)
		return
	}
	saved, err := w.settings.SaveAndApply(r.Context(), settings.SaveRequest{ExpectedGeneration: snap.Generation, Actor: settingsActor(r),
		Records: records, ConfirmationToken: confirmationToken(r, "")})
	if err != nil {
		w.settingsError(rw, r, err, nil)
		return
	}
	writeJSON(rw, http.StatusOK, SettingsWrite{Generation: saved.Generation, Revision: saved.Revision})
}
