package web

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/library/naming"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
)

var qualityEntities = []string{"profiles", "formats", "format_sets", "naming", "sync_sources"}

func (w *Web) qualityAt(rw http.ResponseWriter, r *http.Request, gen *int64) (*settings.Snapshot, bool) {
	if tag := r.Header.Get("If-Match"); tag != "" {
		value := strings.TrimSuffix(strings.TrimPrefix(tag, `"settings-`), `"`)
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || generationETag(n) != tag || (gen != nil && *gen != n) {
			badRequest(rw, "If-Match must name the same settings generation as the request")
			return nil, false
		}
		gen = &n
	}
	return w.settingsAt(rw, r, gen)
}

type QualityRecord struct {
	settings.Record
	Config         map[string]any             `json:"config"`
	Specification  json.RawMessage            `json:"specification,omitempty"`
	Specifications map[string]json.RawMessage `json:"specifications,omitempty"`
	// Origin is a profile's TRaSH preset (display only): what the profile pages compare it with.
	Origin *settings.ProfileOrigin `json:"origin,omitempty"`
}

type qualityBody struct {
	Generation     *int64                     `json:"generation"`
	Key            string                     `json:"key"`
	Config         json.RawMessage            `json:"config"`
	Specification  json.RawMessage            `json:"specification"`
	Specifications map[string]json.RawMessage `json:"specifications"`
	// Origin sets a profile's preset origin; null clears it, absent keeps the stored one.
	Origin json.RawMessage `json:"origin"`
	// ConfirmationToken confirms the effects a previous 422 listed for this same request (a profile that widens
	// an active version).
	ConfirmationToken string `json:"confirmation_token"`
}

func qualityRecords(rs settings.Records, entity string) []QualityRecord {
	out := []QualityRecord{}
	switch entity {
	case "profiles":
		for _, r := range rs.Profiles {
			out = append(out, QualityRecord{Record: r.Record, Config: configMap(r.Config), Origin: r.Origin})
		}
	case "formats":
		for _, r := range rs.Formats {
			out = append(out, QualityRecord{Record: r.Record, Config: configMap(r.Config), Specification: r.Specification})
		}
	case "format_sets":
		for _, r := range rs.FormatSets {
			out = append(out, QualityRecord{Record: r.Record, Config: configMap(r.Config), Specifications: r.Specifications})
		}
	case "naming":
		for _, r := range rs.Naming {
			out = append(out, QualityRecord{Record: r.Record, Config: configMap(r.Config)})
		}
	case "sync_sources":
		for _, r := range rs.SyncSources {
			out = append(out, QualityRecord{Record: r.Record, Config: configMap(r.Config)})
		}
	}
	return out
}

// withoutScoreState drops a sync source config's score_state and field_state, which reads return and an editor sends
// back: they are Theatarr's own (runs and decisions keep them), never input.
func withoutScoreState(raw json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return raw
	}
	_, scores := m["score_state"]
	_, fields := m["field_state"]
	if !scores && !fields {
		return raw
	}
	delete(m, "score_state")
	delete(m, "field_state")
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
}

func (w *Web) mountQualitySettingsAPI(mux *http.ServeMux) {
	b := APIBase + "/settings"
	h := func(pattern string, fn http.HandlerFunc) { w.handle(mux, pattern, SignedIn, auth.PermAdmin, fn) }
	for _, entity := range qualityEntities {
		p := b + "/" + entity
		h("GET "+p, w.apiQualityRead(entity, false))
		h("GET "+p+"/{id}", w.apiQualityRead(entity, true))
		h("POST "+p, w.apiQualityWrite(entity, true, false))
		h("PUT "+p+"/{id}", w.apiQualityWrite(entity, false, false))
		h("DELETE "+p+"/{id}", w.apiQualityWrite(entity, false, true))
	}
	h("POST "+b+"/naming/preview", w.apiNamingPreview)
	h("GET "+b+"/formats/schema", w.apiFormatSchema)
	h("POST "+b+"/formats/import", w.apiQualityWrite("formats", true, false))
	h("GET "+b+"/formats/{id}/export", w.apiFormatExport)
	h("GET "+b+"/export/files", w.apiQualityExportFiles)
	h("POST "+b+"/sync/preview", w.apiQualitySyncPreview)
	h("GET "+b+"/sync/reviews", w.apiQualityReviews)
	h("POST "+b+"/sync/reviews/{id}/apply", w.apiQualityReviewApply)
	h("POST "+b+"/sync/reviews/{id}/scores", w.apiQualityReviewScores)
	h("POST "+b+"/sync_sources/{id}/run", w.apiQualitySyncRun)
	h("POST "+b+"/sync_sources/{id}/link/preview", w.apiTrashLinkPreview)
	h("POST "+b+"/sync_sources/{id}/link/apply", w.apiTrashLinkApply)
	h("GET "+b+"/sync/runs", w.apiQualitySyncRuns)
}

func (w *Web) apiQualityRead(entity string, one bool) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		snap, ok := w.settingsHead(rw, r)
		if !ok {
			return
		}
		recs := qualityRecords(snap.Records, entity)
		rw.Header().Set("ETag", generationETag(snap.Generation))
		if one {
			id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
			for _, rec := range recs {
				if rec.ID == id {
					writeJSON(rw, 200, rec)
					return
				}
			}
			writeJSON(rw, 404, apiError{Error: "settings record not found"})
			return
		}
		out := map[string]any{"generation": snap.Generation, "records": recs}
		if entity == "naming" && snap.Policy != nil {
			// The built-in templates each library falls back to, so the editor can copy a naming exactly.
			defaults := map[string]map[string]string{}
			for k, v := range snap.Policy.Versions {
				if v != nil {
					defaults[k] = library.DefaultTemplates(v.Kind)
				}
			}
			out["defaults"] = defaults
		}
		writeJSON(rw, 200, out)
	}
}

// addScoredFormatsToSet makes a profile score of a standalone custom format count when the profile evaluates a format
// set: only the set's formats are in scope, so a scored standalone format the set lacks joins it as a reference
// ({ref: key}, as a preset's set refers to one), when the flavours agree. Other scores are left to validation.
func addScoredFormatsToSet(rs *settings.Records, pr policy.Profile) {
	if pr.FormatSet == "" {
		return
	}
	i := slices.IndexFunc(rs.FormatSets, func(s settings.FormatSet) bool { return s.Key == pr.FormatSet })
	if i < 0 {
		return
	}
	set := rs.FormatSets[i]
	var added map[string]*policy.FormatRef
	for _, name := range slices.Sorted(maps.Keys(pr.Formats)) {
		if _, in := set.Config.Formats[name]; in {
			continue
		}
		j := slices.IndexFunc(rs.Formats, func(f settings.CustomFormat) bool { return f.Key == name })
		if j < 0 {
			continue
		}
		flavour := rs.Formats[j].Config.Flavour
		if flavour == "" {
			flavour = "radarr"
		}
		if flavour != set.Config.Flavour {
			continue
		}
		if added == nil {
			added = maps.Clone(set.Config.Formats)
			if added == nil {
				added = map[string]*policy.FormatRef{}
			}
		}
		added[name] = &policy.FormatRef{Ref: name}
	}
	if added != nil {
		set.Config.Formats = added
		rs.FormatSets[i] = set
	}
}

func qualityApply(rs *settings.Records, entity, key string, id int64, body qualityBody, remove bool) error {
	record := settings.Record{ID: id, Key: key}
	switch entity {
	case "profiles":
		var c policy.Profile
		if !remove {
			if err := decodeConfig(body.Config, &c); err != nil {
				return err
			}
		}
		var origin *settings.ProfileOrigin
		out := []settings.QualityProfile{}
		for _, v := range rs.Profiles {
			if v.Key != key {
				out = append(out, v)
			} else {
				record, origin = v.Record, v.Origin
			}
		}
		if len(body.Origin) > 0 {
			origin = nil
			if string(body.Origin) != "null" {
				dec := json.NewDecoder(strings.NewReader(string(body.Origin)))
				dec.DisallowUnknownFields()
				if err := dec.Decode(&origin); err != nil {
					return &policy.Errors{List: []policy.FieldError{{Path: "origin", Msg: "must be a preset origin (preset, name, flavour, score_set, modifiers)"}}}
				}
			}
		}
		if !remove {
			out = append(out, settings.QualityProfile{Record: record, Config: c, Origin: origin})
			addScoredFormatsToSet(rs, c)
		}
		rs.Profiles = out
	case "formats":
		var c policy.FormatRef
		if !remove {
			if err := decodeConfig(body.Config, &c); err != nil {
				return err
			}
			if len(body.Specification) == 0 || string(body.Specification) == "null" {
				return &policy.Errors{List: []policy.FieldError{{Path: "specification", Msg: "full arr custom format JSON is required"}}}
			}
			if c.File != "" {
				return bodyError("file paths are import-only; send specification JSON")
			}
			if len(body.Specification) > 0 {
				var spec format.FormatJSON
				if err := json.Unmarshal(body.Specification, &spec); err != nil {
					return bodyError("invalid arr custom format JSON")
				}
				if c.TrashID == "" {
					c.TrashID = spec.TrashID
				} else if spec.TrashID != "" && c.TrashID != spec.TrashID {
					return &policy.Errors{List: []policy.FieldError{{Path: "config.trash_id", Msg: "must match specification.trash_id"}}}
				}
			}
		}
		out := []settings.CustomFormat{}
		for _, v := range rs.Formats {
			if v.Key != key {
				out = append(out, v)
			} else {
				record = v.Record
			}
		}
		if !remove {
			out = append(out, settings.CustomFormat{Record: record, Config: c, Specification: body.Specification})
		}
		rs.Formats = out
	case "format_sets":
		var c policy.FormatSet
		if !remove {
			if err := decodeConfig(body.Config, &c); err != nil {
				return err
			}
			for key, ref := range c.Formats {
				if ref != nil && ref.File != "" {
					return bodyError("file paths are import-only")
				}
				if ref != nil && ref.Ref == "" && len(body.Specifications[key]) == 0 {
					return &policy.Errors{List: []policy.FieldError{{Path: "specifications[" + key + "]", Msg: "full arr custom format JSON is required"}}}
				}
			}
		}
		out := []settings.FormatSet{}
		for _, v := range rs.FormatSets {
			if v.Key != key {
				out = append(out, v)
			} else {
				record = v.Record
			}
		}
		if !remove {
			out = append(out, settings.FormatSet{Record: record, Config: c, Specifications: body.Specifications})
		}
		rs.FormatSets = out
	case "naming":
		var c policy.Naming
		if !remove {
			if err := decodeConfig(body.Config, &c); err != nil {
				return err
			}
		}
		out := []settings.VersionNaming{}
		for _, v := range rs.Naming {
			if v.Key != key {
				out = append(out, v)
			} else {
				record = v.Record
			}
		}
		if !remove {
			out = append(out, settings.VersionNaming{Record: record, Config: c})
		}
		rs.Naming = out
	case "sync_sources":
		var c settings.QualitySyncConfig
		if !remove {
			if err := decodeConfig(withoutScoreState(body.Config), &c); err != nil {
				return err
			}
		}
		// The score and field states are Theatarr's own memory of the managed values: an edit never sets them, and keeps
		// the stored ones.
		c.ScoreState, c.FieldState = nil, nil
		out := []settings.QualitySyncSource{}
		for _, v := range rs.SyncSources {
			if v.Key != key {
				out = append(out, v)
			} else {
				record = v.Record
				c.ScoreState, c.FieldState = v.Config.ScoreState, v.Config.FieldState
			}
		}
		if !remove {
			out = append(out, settings.QualitySyncSource{Record: record, Config: c})
		}
		rs.SyncSources = out
	}
	return nil
}

func (w *Web) apiQualityWrite(entity string, create, remove bool) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		var body qualityBody
		if !decodeSettingsJSON(rw, r, &body) {
			return
		}
		if remove && body.Generation == nil {
			if v := r.URL.Query().Get("generation"); v != "" {
				n, err := strconv.ParseInt(v, 10, 64)
				if err == nil {
					body.Generation = &n
				}
			}
		}
		snap, ok := w.qualityAt(rw, r, body.Generation)
		if !ok {
			return
		}
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		key := body.Key
		exists := false
		for _, rec := range qualityRecords(snap.Records, entity) {
			if create && rec.Key == key {
				badRequest(rw, "record key already exists")
				return
			}
			if !create && rec.ID == id {
				if key != "" && key != rec.Key {
					badRequest(rw, "keys cannot be renamed")
					return
				}
				key = rec.Key
				exists = true
			}
		}
		if !create && !exists {
			writeJSON(rw, 404, apiError{Error: "settings record not found"})
			return
		}
		if strings.TrimSpace(key) == "" {
			badRequest(rw, "key is required")
			return
		}
		if remove {
			if uses := qualityUses(snap.Policy, entity, key); len(uses) > 0 {
				writeJSON(rw, 422, map[string]any{"error": "record is still in use", "uses": uses})
				return
			}
		}
		records := cloneRecords(snap.Records)
		if err := qualityApply(&records, entity, key, id, body, remove); err != nil {
			w.settingsError(rw, r, err, nil)
			return
		}
		saved, err := w.settings.SaveAndApply(r.Context(), settings.SaveRequest{ExpectedGeneration: snap.Generation, Actor: settingsActor(r), Records: records,
			ConfirmationToken: confirmationToken(r, body.ConfirmationToken)})
		if err != nil {
			w.settingsError(rw, r, err, nil)
			return
		}
		rw.Header().Set("ETag", generationETag(saved.Generation))
		writeJSON(rw, 200, map[string]any{"generation": saved.Generation, "revision": saved.Revision, "records": qualityRecords(saved.Records, entity)})
	}
}

func qualityUses(p *policy.Policy, entity, key string) []string {
	out := []string{}
	for name, v := range p.Versions {
		if entity == "profiles" && v.Profile == key {
			out = append(out, "versions["+name+"].profile")
		}
		// A version's naming goes with the version (a version delete); deleting it alone would drop the version's new
		// imports to the arr default names without a word.
		if entity == "naming" && name == key {
			out = append(out, "versions["+name+"]")
		}
	}
	for name, pr := range p.Profiles {
		if entity == "format_sets" && pr.FormatSet == key {
			out = append(out, "profiles["+name+"].format_set")
		}
		if entity == "formats" && pr.FormatSet == "" {
			for score := range pr.Formats {
				resolved, _ := p.ProfileFormat(pr, score)
				if resolved == key {
					out = append(out, "profiles["+name+"].formats["+score+"]")
				}
			}
		}
	}
	if entity == "formats" {
		for name, set := range p.FormatSets {
			for display, ref := range set.Formats {
				if ref != nil && ref.Ref == key {
					out = append(out, "format_sets["+name+"].formats["+display+"]")
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func (w *Web) apiFormatExport(rw http.ResponseWriter, r *http.Request) {
	snap, ok := w.settingsHead(rw, r)
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	for _, rec := range snap.Records.Formats {
		if rec.ID == id {
			if len(rec.Specification) == 0 {
				badRequest(rw, "format specification has not been imported")
				return
			}
			var spec format.FormatJSON
			if err := json.Unmarshal(rec.Specification, &spec); err != nil {
				w.internal(rw, err)
				return
			}
			spec.ID = 0
			// The policy key is stable; the arr display name is editable independently.
			if spec.Name == "" {
				spec.Name = rec.Key
			}
			if rec.Config.IncludeWhenRenaming != nil {
				spec.IncludeCustomFormatWhenRenaming = *rec.Config.IncludeWhenRenaming
			}
			for i := range spec.Specifications {
				var fields map[string]any
				if json.Unmarshal(spec.Specifications[i].Fields, &fields) == nil && fields != nil {
					names := make([]string, 0, len(fields))
					for name := range fields {
						names = append(names, name)
					}
					sort.Strings(names)
					list := []map[string]any{}
					for _, name := range names {
						list = append(list, map[string]any{"name": name, "value": fields[name]})
					}
					spec.Specifications[i].Fields, _ = json.Marshal(list)
				}
			}
			rw.Header().Set("ETag", generationETag(snap.Generation))
			writeJSON(rw, 200, spec)
			return
		}
	}
	writeJSON(rw, 404, apiError{Error: "settings record not found"})
}

func (w *Web) apiQualityExportFiles(rw http.ResponseWriter, r *http.Request) {
	snap, ok := w.settingsHead(rw, r)
	if !ok {
		return
	}
	files, _, err := snap.ExportFiles()
	if err != nil {
		w.internal(rw, err)
		return
	}
	document, err := snap.Export()
	if err != nil {
		w.internal(rw, err)
		return
	}
	rw.Header().Set("ETag", generationETag(snap.Generation))
	writeJSON(rw, 200, map[string]any{"generation": snap.Generation, "yaml": string(document), "files": files})
}

type namingPreviewBody struct {
	Generation  *int64             `json:"generation"`
	Version     string             `json:"version"`
	Naming      *json.RawMessage   `json:"naming"`
	Movie       naming.Movie       `json:"movie"`
	MovieFile   naming.MovieFile   `json:"movie_file"`
	Series      naming.Series      `json:"series"`
	Episodes    []naming.Episode   `json:"episodes"`
	EpisodeFile naming.EpisodeFile `json:"episode_file"`
}

func (w *Web) apiNamingPreview(rw http.ResponseWriter, r *http.Request) {
	var body namingPreviewBody
	if !decodeSettingsJSON(rw, r, &body) {
		return
	}
	snap, ok := w.qualityAt(rw, r, body.Generation)
	if !ok {
		return
	}
	p, err := snap.Policy.Clone()
	if err != nil {
		w.internal(rw, err)
		return
	}
	vp := p.Versions[body.Version]
	if vp == nil {
		badRequest(rw, "unknown library")
		return
	}
	if body.Naming != nil {
		var config policy.Naming
		if err := decodeConfig(*body.Naming, &config); err != nil {
			w.settingsError(rw, r, err, nil)
			return
		}
		vp.Naming = config
	}
	if err := p.Validate(); err != nil {
		w.settingsError(rw, r, err, nil)
		return
	}
	vn, err := library.NamingFor(r.Context(), w.st.Reader(), p, body.Version)
	if err != nil {
		badRequest(rw, err.Error())
		return
	}
	var file, folder string
	if vp.Kind == policy.KindMovie {
		if body.Movie.Title == "" {
			body.Movie = naming.Movie{Title: "Example Movie", Year: 2026, TmdbID: 123}
		}
		if body.MovieFile.Quality.Title == "" {
			body.MovieFile.Quality = naming.Quality{Title: vn.QualityTitle("WEBDL-1080p"), Version: 1}
		}
		file, err = naming.MovieFileName(&vn.Config, &body.Movie, &body.MovieFile)
		if err == nil {
			folder, err = naming.MovieFolder(&vn.Config, &body.Movie)
		}
	} else {
		if body.Series.Title == "" {
			body.Series = naming.Series{Title: "Example Series", Year: 2026, TvdbID: 123}
		}
		if len(body.Episodes) == 0 {
			body.Episodes = []naming.Episode{{Season: 1, Number: 1, Title: "Pilot"}}
		}
		if body.EpisodeFile.Quality.Title == "" {
			body.EpisodeFile.Quality = naming.Quality{Title: vn.QualityTitle("WEBDL-1080p"), Version: 1}
		}
		file, err = naming.SeriesFileName(&vn.Config, &body.Series, body.Episodes, &body.EpisodeFile, ".mkv", "")
		if err == nil {
			folder, err = naming.SeriesFolder(&vn.Config, &body.Series)
		}
	}
	if err != nil {
		badRequest(rw, err.Error())
		return
	}
	writeJSON(rw, 200, map[string]any{"generation": snap.Generation, "file": file, "folder": folder, "source": vn.Source})
}

func (w *Web) apiFormatSchema(rw http.ResponseWriter, r *http.Request) {
	writeJSON(rw, 200, format.SpecificationSchemas())
}
