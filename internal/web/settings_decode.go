package web

// Strict settings request decoding and candidate record construction.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
)

type bodyError string

func (e bodyError) Error() string { return string(e) }

// writeBodyError answers a refused request body. Every message is scrubbed of the request's credential values: a
// field name or a validation path could otherwise repeat one.
func writeBodyError(rw http.ResponseWriter, err error, scrub []string) {
	if errors.Is(err, errNoRecord) {
		notFound(rw)
		return
	}
	var be bodyError
	if errors.As(err, &be) {
		badRequest(rw, scrubText(string(be), scrub))
		return
	}
	var verr *policy.Errors
	if errors.As(err, &verr) && len(verr.List) > 0 {
		writeJSON(rw, http.StatusUnprocessableEntity, apiError{Error: "settings refused: " + scrubText(verr.List[0].Error(), scrub)})
		return
	}
	badRequest(rw, "invalid request")
}

// decodeSettingsJSON decodes strictly: an unknown field (router, mode, notify, ...) is refused, so nothing outside
// this API's records can be set through it. Decoder errors are reduced to the field name, never a value.
func decodeSettingsJSON(rw http.ResponseWriter, r *http.Request, v any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err != nil {
		badRequest(rw, "invalid JSON: unreadable or over 1 MiB")
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	err = dec.Decode(v)
	if errors.Is(err, io.EOF) {
		return true // an empty body is {}
	}
	if err == nil {
		// The object must be the whole body: Decoder.More does not see a stray closing } or ], a second decode does.
		if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
			badRequest(rw, "invalid JSON: one object expected")
			return false
		}
		return true
	}
	msg := "invalid JSON"
	if s := err.Error(); strings.HasPrefix(s, "json: unknown field ") {
		msg = "unknown field " + strings.TrimPrefix(s, "json: unknown field ") + ": this settings endpoint does not accept it"
	} else {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) && te.Field != "" {
			msg = "invalid JSON: wrong type for " + te.Field
		}
	}
	// A field name is the caller's text: remove the body's credential from it, read leniently.
	var cred struct {
		Secret *string `json:"secret"`
	}
	var scrub []string
	if json.Unmarshal(raw, &cred) == nil && cred.Secret != nil {
		scrub = append(scrub, *cred.Secret)
	}
	badRequest(rw, scrubText(msg, scrub))
	return false
}

func decodeSettingsBody(rw http.ResponseWriter, r *http.Request) (settingsBody, bool) {
	var body settingsBody
	ok := decodeSettingsJSON(rw, r, &body)
	return body, ok
}

func recordKey(snap *settings.Snapshot, entity string, id int64) string {
	switch entity {
	case entityIndexers:
		for _, r := range snap.Records.Indexers {
			if r.ID == id {
				return r.Key
			}
		}
	case entityProviders:
		for _, r := range snap.Records.Providers {
			if r.ID == id {
				return r.Key
			}
		}
	case entityBudgets:
		for _, r := range snap.Records.Budgets {
			if r.ID == id {
				return r.Key
			}
		}
	}
	return ""
}

func cloneRecords(in settings.Records) settings.Records {
	var switches *settings.GlobalSwitches
	if in.Switches != nil {
		copy := *in.Switches
		switches = &copy
	}
	return settings.Records{Switches: switches, Indexers: append([]settings.Indexer(nil), in.Indexers...),
		Providers: append([]settings.Provider(nil), in.Providers...), Budgets: append([]settings.Budget(nil), in.Budgets...),
		Versions: append([]settings.Version(nil), in.Versions...), Views: append([]settings.ViewBinding(nil), in.Views...),
		Profiles: append(in.Profiles[:0:0], in.Profiles...), Formats: append(in.Formats[:0:0], in.Formats...),
		FormatSets: append(in.FormatSets[:0:0], in.FormatSets...), Naming: append(in.Naming[:0:0], in.Naming...),
		SyncSources: append(in.SyncSources[:0:0], in.SyncSources...)}
}

// applyBody turns a create (id 0, create true) or an update of record id into a complete save request at the
// snapshot's generation. It returns the request's credential values so error logging can remove them.
func applyBody(snap *settings.Snapshot, entity string, id int64, body settingsBody, create bool) (settings.SaveRequest, []string, error) {
	req := settings.SaveRequest{ExpectedGeneration: snap.Generation, Records: cloneRecords(snap.Records)}
	if body.OrphanActiveDownloads != nil {
		if entity != entityProviders {
			return req, nil, bodyError("orphan_active_downloads applies to providers only")
		}
		req.OrphanActiveDownloads = *body.OrphanActiveDownloads
	}
	if entity == entityBudgets && (body.Enabled != nil || body.Secret != nil || body.ClearSecret) {
		return req, nil, bodyError("budgets have no enabled flag or credential")
	}
	if len(body.Versions) > 0 && (entity != entityIndexers || !create) {
		return req, nil, bodyError("versions applies to a new indexer only")
	}
	if entity != entityIndexers && body.Priority != nil {
		return req, nil, bodyError("priority applies to indexers only; providers use config.order")
	}
	var scrub []string
	if body.Secret != nil {
		if *body.Secret == "" {
			return req, nil, bodyError("secret must not be empty: omit it to keep the stored credential, or send clear_secret")
		}
		if body.ClearSecret {
			return req, nil, bodyError("send either secret or clear_secret")
		}
		scrub = append(scrub, *body.Secret)
	}
	key := body.Key
	if !create {
		key = recordKey(snap, entity, id)
		if key == "" {
			return req, scrub, errNoRecord
		}
		if body.Key != "" && body.Key != key {
			return req, scrub, bodyError("keys cannot be renamed; delete the record and create a new one")
		}
	} else if keyExists(snap, entity, key) {
		return req, scrub, bodyError(fmt.Sprintf("%s %q already exists", strings.TrimSuffix(entity, "s"), key))
	}
	switch entity {
	case entityIndexers:
		var rec settings.Indexer
		idx := -1
		for i := range req.Records.Indexers {
			if req.Records.Indexers[i].Key == key {
				rec, idx = req.Records.Indexers[i], i
			}
		}
		if create {
			rec = settings.Indexer{Record: settings.Record{Key: key}}
		}
		if len(body.Config) > 0 {
			env := rec.Config.APIKeyEnv
			if err := decodeConfig(body.Config, &rec.Config); err != nil {
				return req, scrub, err
			}
			rec.Config.APIKeyEnv = env
		}
		if body.Enabled != nil {
			rec.Enabled = *body.Enabled
		}
		if body.Priority != nil {
			rec.Priority = *body.Priority
		}
		if idx < 0 {
			req.Records.Indexers = append(req.Records.Indexers, rec)
		} else {
			req.Records.Indexers[idx] = rec
		}
		if err := addToVersions(&req, body.Versions, key); err != nil {
			return req, scrub, err
		}
	case entityProviders:
		var rec settings.Provider
		idx := -1
		for i := range req.Records.Providers {
			if req.Records.Providers[i].Key == key {
				rec, idx = req.Records.Providers[i], i
			}
		}
		if create {
			rec = settings.Provider{Record: settings.Record{Key: key}}
		}
		if len(body.Config) > 0 {
			env := rec.Config.APIKeyEnv
			if err := decodeConfig(body.Config, &rec.Config); err != nil {
				return req, scrub, err
			}
			rec.Config.APIKeyEnv = env
		}
		if body.Enabled != nil {
			rec.Enabled = *body.Enabled
		}
		if idx < 0 {
			req.Records.Providers = append(req.Records.Providers, rec)
		} else {
			req.Records.Providers[idx] = rec
		}
	case entityBudgets:
		var rec settings.Budget
		idx := -1
		for i := range req.Records.Budgets {
			if req.Records.Budgets[i].Key == key {
				rec, idx = req.Records.Budgets[i], i
			}
		}
		if create {
			rec = settings.Budget{Record: settings.Record{Key: key}}
		}
		if len(body.Config) > 0 {
			if err := decodeConfig(body.Config, &rec.Config); err != nil {
				return req, scrub, err
			}
		}
		if idx < 0 {
			req.Records.Budgets = append(req.Records.Budgets, rec)
		} else {
			req.Records.Budgets[idx] = rec
		}
	}
	ref := entity + "/" + key
	switch {
	case body.Secret != nil:
		req.SecretWrites = map[string]settings.Secret{ref: settings.NewSecret(*body.Secret)}
	case body.ClearSecret:
		req.SecretWrites = map[string]settings.Secret{ref: settings.NewSecret("")}
	}
	return req, scrub, nil
}

var errNoRecord = bodyError("no such record")

// addToVersions lists the indexer keys in each named library (version) of the request, keeping its order and
// skipping keys it already lists. An unknown library is refused; a library named twice is added to once.
func addToVersions(req *settings.SaveRequest, versions []string, keys ...string) error {
	for _, name := range versions {
		i := slices.IndexFunc(req.Records.Versions, func(v settings.Version) bool { return v.Key == name })
		if i < 0 {
			return bodyError("versions: no library " + name)
		}
		v := &req.Records.Versions[i]
		// cloneRecords copies the version structs but shares their Indexers arrays with the snapshot.
		list := slices.Clone(v.Config.Indexers)
		for _, key := range keys {
			if !slices.Contains(list, key) {
				list = append(list, key)
			}
		}
		v.Config.Indexers = list
	}
	return nil
}

func keyExists(snap *settings.Snapshot, entity, key string) bool {
	switch entity {
	case entityIndexers:
		for _, r := range snap.Records.Indexers {
			if r.Key == key {
				return true
			}
		}
	case entityProviders:
		for _, r := range snap.Records.Providers {
			if r.Key == key {
				return true
			}
		}
	case entityBudgets:
		for _, r := range snap.Records.Budgets {
			if r.Key == key {
				return true
			}
		}
	}
	return false
}

// decodeConfig reads a JSON config object with the policy's YAML field names into a policy struct, strictly: unknown
// fields are refused. Credential environment names are import-only. A top-level field sent replaces the stored one
// whole (null resets it to its default); one left out keeps its stored value. into may share maps, slices and
// pointers with the live snapshot, so the result is built from a fresh decode and a deep copy and never writes
// through them.
func decodeConfig(raw json.RawMessage, into any) error {
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil || generic == nil {
		return bodyError("config must be a JSON object")
	}
	for k := range generic {
		if strings.HasSuffix(k, "_env") {
			return bodyError("config." + k + ": credential environment names are import-only; send secret instead")
		}
	}
	// Check every field against the target type first, so a refusal is a fixed field/type message built here and
	// never parser text, which can quote the submitted value.
	if err := checkConfigFields("config", generic, reflect.TypeOf(into).Elem()); err != nil {
		return err
	}
	b, err := yaml.Marshal(generic)
	if err != nil {
		return bodyError("config is not valid")
	}
	target := reflect.ValueOf(into).Elem()
	fresh := reflect.New(target.Type())
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(fresh.Interface()); err != nil {
		return bodyError("config is not valid")
	}
	out := deepCopy(target)
	for i := range target.NumField() {
		name, _, _ := strings.Cut(target.Type().Field(i).Tag.Get("yaml"), ",")
		if _, sent := generic[name]; sent && name != "" && name != "-" {
			out.Field(i).Set(fresh.Elem().Field(i))
		}
	}
	target.Set(out)
	return nil
}

// deepCopy copies v with fresh maps, slices and pointers, so the copy shares no storage with v.
func deepCopy(v reflect.Value) reflect.Value {
	out := reflect.New(v.Type()).Elem()
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			p := reflect.New(v.Type().Elem())
			p.Elem().Set(deepCopy(v.Elem()))
			out.Set(p)
		}
	case reflect.Map:
		if !v.IsNil() {
			m := reflect.MakeMapWithSize(v.Type(), v.Len())
			for it := v.MapRange(); it.Next(); {
				m.SetMapIndex(it.Key(), deepCopy(it.Value()))
			}
			out.Set(m)
		}
	case reflect.Slice:
		if !v.IsNil() {
			sl := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
			for i := range v.Len() {
				sl.Index(i).Set(deepCopy(v.Index(i)))
			}
			out.Set(sl)
		}
	case reflect.Struct:
		out.Set(v)
		for i := range v.NumField() {
			if out.Field(i).CanSet() {
				out.Field(i).Set(deepCopy(v.Field(i)))
			}
		}
	case reflect.Interface:
		if !v.IsNil() {
			out.Set(deepCopy(v.Elem()))
		}
	default:
		out.Set(v)
	}
	return out
}

var yamlUnmarshaler = reflect.TypeOf((*yaml.Unmarshaler)(nil)).Elem()

// checkConfigFields checks a decoded JSON object against a policy struct by YAML field name: unknown fields and
// values of the wrong type are refused with the field's path and the expected type, in sorted field order.
func checkConfigFields(path string, m map[string]any, t reflect.Type) error {
	fields := map[string]reflect.Type{}
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name != "" && name != "-" && f.IsExported() {
			fields[name] = f.Type
		}
	}
	for _, k := range sortedKeys(m) {
		ft, ok := fields[k]
		if !ok {
			return bodyError("unknown field " + path + "." + k)
		}
		v := m[k]
		if v == nil {
			continue
		}
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && !reflect.PointerTo(ft).Implements(yamlUnmarshaler) {
			sub, ok := v.(map[string]any)
			if !ok {
				return bodyError(path + "." + k + ": must be an object")
			}
			if err := checkConfigFields(path+"."+k, sub, ft); err != nil {
				return err
			}
			continue
		}
		b, err := yaml.Marshal(v)
		if err == nil {
			err = yaml.Unmarshal(b, reflect.New(ft).Interface())
		}
		if err != nil {
			return bodyError(path + "." + k + ": must be " + configTypeName(ft))
		}
	}
	return nil
}

// configTypeName names what a config field takes, for the fixed type errors.
func configTypeName(t reflect.Type) string {
	if t == reflect.TypeOf(policy.Duration(0)) {
		return "a duration such as 90s or 5m"
	}
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "an integer"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "a list"
	case reflect.Map:
		return "an object"
	}
	return "a valid value"
}
