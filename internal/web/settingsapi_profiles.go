package web

// The quality profile pages: the profiles and formats overview, TRaSH presets, building a profile
// from one, the decision impact of a plain profile edit and the default profile per kind of version. Reads work under
// reads and writes use the settings database.

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
)

// trashCache keeps each flavour's local TRaSH tree for a few minutes: the pages read it on every load, and a sync
// run that updates the tree is picked up when the entry expires.
type trashCache struct {
	mu      sync.Mutex
	entries map[string]trashEntry
}

type trashEntry struct {
	root string
	tree *format.Trash
	at   time.Time
}

const trashCacheTTL = 5 * time.Minute

func (w *Web) trashTreeRoot() string {
	if w.trashRoot != "" {
		return w.trashRoot
	}
	if w.qualitySync != nil {
		return w.qualitySync.TrashRoot
	}
	return ""
}

// trashTree is flavour's local TRaSH tree, nil when none is configured or it does not load.
func (w *Web) trashTree(flavour string) *format.Trash {
	root := w.trashTreeRoot()
	if root == "" || !format.Flavour(flavour).Valid() {
		return nil
	}
	c := &w.trashCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[flavour]; ok && e.root == root && time.Since(e.at) < trashCacheTTL {
		return e.tree
	}
	tree, err := format.LoadTrash(root, format.Flavour(flavour))
	if err != nil || len(tree.Profiles) == 0 && len(tree.Formats) == 0 {
		tree = nil
	}
	if c.entries == nil {
		c.entries = map[string]trashEntry{}
	}
	c.entries[flavour] = trashEntry{root: root, tree: tree, at: time.Now()}
	return tree
}

// noTrashNote says why there is no local TRaSH tree: THEATARR_TRASH is unset, or it names a directory without a
// TRaSH-Guides checkout (the example compose file mounts an empty directory until the guides are cloned into it).
func (w *Web) noTrashNote() string {
	if w.trashTreeRoot() == "" {
		return "No local TRaSH guides tree (THEATARR_TRASH is not set)"
	}
	return "THEATARR_TRASH is set but holds no TRaSH-Guides checkout"
}

func (w *Web) trashTrees() map[string]*format.Trash {
	out := map[string]*format.Trash{}
	for _, fl := range []string{string(format.Radarr), string(format.Sonarr)} {
		if t := w.trashTree(fl); t != nil {
			out[fl] = t
		}
	}
	return out
}

func (w *Web) mountProfileSettingsAPI(mux *http.ServeMux) {
	b := APIBase + "/settings"
	h := func(pattern string, fn http.HandlerFunc) { w.handle(mux, pattern, SignedIn, auth.PermAdmin, fn) }
	h("GET "+b+"/quality/overview", w.apiQualityOverview)
	h("GET "+b+"/presets", w.apiPresets)
	h("POST "+b+"/presets/build", w.apiPresetBuild)
	h("POST "+b+"/presets/create", w.apiPresetCreate)
	h("POST "+b+"/profiles/impact", w.apiProfileImpact)
	h("GET "+b+"/profile-defaults", w.apiProfileDefaults)
	h("PUT "+b+"/profile-defaults", w.apiProfileDefaultsWrite)
}

type qualityOverviewAnswer struct {
	Source     string `json:"source"`
	Generation int64  `json:"generation"`
	settings.QualityOverview
	Defaults   settings.ProfileDefaults `json:"defaults"`
	Categories []string                 `json:"categories"`
	// TrashNote says why there is no local TRaSH tree; empty when there is one.
	TrashNote string `json:"trash_note,omitempty"`
}

func (w *Web) apiQualityOverview(rw http.ResponseWriter, r *http.Request) {
	in := settings.OverviewInput{Policy: w.pol, Trash: w.trashTrees()}
	answer := qualityOverviewAnswer{Source: "db", Defaults: settings.ProfileDefaults{}, Categories: settings.ProfileCategories}
	{
		snap, ok := w.settingsHead(rw, r)
		if !ok {
			return
		}
		in.Policy, in.Profiles, in.Formats, in.Sources = snap.Policy, snap.Records.Profiles, snap.Records.Formats, snap.Records.SyncSources
		answer.Source, answer.Generation = "db", snap.Generation
		rw.Header().Set("ETag", generationETag(snap.Generation))
		if w.qualitySync != nil {
			reviews, err := w.qualitySync.Reviews(r.Context())
			if err != nil {
				w.internal(rw, err)
				return
			}
			for _, rv := range reviews {
				in.Held = append(in.Held, rv.Held...)
			}
		}
		defaults, err := w.settings.ProfileDefaults(r.Context())
		if err != nil {
			w.settingsError(rw, r, err, nil)
			return
		}
		answer.Defaults = defaults
	}
	answer.QualityOverview = settings.QualityOverviewOf(in)
	if len(in.Trash) == 0 {
		answer.TrashNote = w.noTrashNote()
	}
	writeJSON(rw, http.StatusOK, answer)
}

// presetTree answers 400 for an unknown flavour and 503 without a local TRaSH tree.
func (w *Web) presetTree(rw http.ResponseWriter, flavour string) *format.Trash {
	if !format.Flavour(flavour).Valid() {
		badRequest(rw, "flavour must be radarr or sonarr")
		return nil
	}
	t := w.trashTree(flavour)
	if t == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "no local TRaSH guides tree for " + flavour + " (THEATARR_TRASH): presets need it"})
	}
	return t
}

// apiPresets lists flavour's presets. Without a local TRaSH tree it answers an empty list and the reason: a state of
// the install, not a server error, so the pages that read it on every visit neither retry it nor log errors.
func (w *Web) apiPresets(rw http.ResponseWriter, r *http.Request) {
	flavour := r.URL.Query().Get("flavour")
	if !format.Flavour(flavour).Valid() {
		badRequest(rw, "flavour must be radarr or sonarr")
		return
	}
	t := w.trashTree(flavour)
	if t == nil {
		writeJSON(rw, http.StatusOK, map[string]any{"flavour": flavour, "presets": []settings.Preset{},
			"unavailable": w.noTrashNote() + ": TRaSH presets need it"})
		return
	}
	presets, err := settings.Presets(t)
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"flavour": flavour, "presets": presets})
}

type presetBody struct {
	Generation *int64          `json:"generation"`
	Key        string          `json:"key"`
	Flavour    string          `json:"flavour"`
	Preset     string          `json:"preset"`
	Modifiers  []string        `json:"modifiers"`
	Config     json.RawMessage `json:"config"`
	// ConfirmationToken confirms the effects a previous 422 listed for this same request.
	ConfirmationToken string `json:"confirmation_token"`
}

func (w *Web) buildPreset(rw http.ResponseWriter, r *http.Request, body presetBody, records []settings.CustomFormat) *settings.PresetBuild {
	t := w.presetTree(rw, body.Flavour)
	if t == nil {
		return nil
	}
	build, err := settings.BuildPreset(t, body.Preset, body.Modifiers, records)
	if err != nil {
		w.settingsError(rw, r, err, nil)
		return nil
	}
	return build
}

// apiPresetBuild returns the profile a preset with modifiers builds, for the editor to start from. It saves nothing.
func (w *Web) apiPresetBuild(rw http.ResponseWriter, r *http.Request) {
	var body presetBody
	if !decodeSettingsJSON(rw, r, &body) {
		return
	}
	var globals []settings.CustomFormat
	{
		snap, ok := w.settingsHead(rw, r)
		if !ok {
			return
		}
		globals = snap.Records.Formats
	}
	build := w.buildPreset(rw, r, body, globals)
	if build == nil {
		return
	}
	groups := map[string]string{}
	byID := settings.TrashGroups(w.trashTree(body.Flavour))
	for name, id := range build.TrashIDs {
		groups[name] = byID[id]
	}
	writeJSON(rw, http.StatusOK, map[string]any{"profile": configMap(build.Profile), "origin": build.Origin, "trash_ids": build.TrashIDs,
		"groups": groups, "unscored": build.Unscored, "notes": build.Notes, "formats": len(build.Profile.Formats)})
}

// apiPresetCreate saves a new profile built from a preset, with its own format set, its origin, and its binding to
// TRaSH's score set on the flavour's TRaSH sync source. config, when sent, is the editor's version of the built
// profile. A profile no version uses changes no decision, so this needs no confirmation in practice; the token is
// passed through as for every write.
func (w *Web) apiPresetCreate(rw http.ResponseWriter, r *http.Request) {
	var body presetBody
	if !decodeSettingsJSON(rw, r, &body) {
		return
	}
	snap, ok := w.qualityAt(rw, r, body.Generation)
	if !ok {
		return
	}
	build := w.buildPreset(rw, r, body, snap.Records.Formats)
	if build == nil {
		return
	}
	var config *policy.Profile
	if len(body.Config) > 0 && string(body.Config) != "null" {
		config = &policy.Profile{}
		if err := decodeConfig(body.Config, config); err != nil {
			w.settingsError(rw, r, err, nil)
			return
		}
	}
	records := cloneRecords(snap.Records)
	source, err := settings.ApplyPreset(&records, body.Key, build, config)
	if err != nil {
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
	writeJSON(rw, http.StatusOK, map[string]any{"generation": saved.Generation, "revision": saved.Revision, "key": body.Key, "binding": source,
		"records": qualityRecords(saved.Records, "profiles")})
}

type profileImpactAnswer struct {
	Generation int64                 `json:"generation"`
	Key        string                `json:"key"`
	Versions   []settings.VersionUse `json:"versions"`
	// Available is false when no impact could be computed (no decision engine); Reason says why.
	Available bool                    `json:"available"`
	Reason    string                  `json:"reason,omitempty"`
	Impact    settings.DecisionImpact `json:"impact"`
}

// apiProfileImpact computes what a plain edit of one profile would change in the decisions on the library's files,
// before it is saved: the same DecisionImpact a sync review shows. It saves nothing; a save that widens an active
// version still asks for confirmation.
func (w *Web) apiProfileImpact(rw http.ResponseWriter, r *http.Request) {
	var body qualityBody
	if !decodeSettingsJSON(rw, r, &body) {
		return
	}
	snap, ok := w.qualityAt(rw, r, body.Generation)
	if !ok {
		return
	}
	found := false
	for _, p := range snap.Records.Profiles {
		found = found || p.Key == body.Key
	}
	if !found {
		writeJSON(rw, http.StatusNotFound, apiError{Error: "settings record not found"})
		return
	}
	records := cloneRecords(snap.Records)
	body.Origin = nil
	if err := qualityApply(&records, "profiles", body.Key, 0, body, false); err != nil {
		w.settingsError(rw, r, err, nil)
		return
	}
	candidate, err := w.settings.Candidate(r.Context(), settings.SaveRequest{ExpectedGeneration: snap.Generation, Actor: settingsActor(r), Records: records})
	if err != nil {
		w.settingsError(rw, r, err, nil)
		return
	}
	answer := profileImpactAnswer{Generation: snap.Generation, Key: body.Key, Versions: settings.VersionsUsing(snap.Policy, body.Key), Impact: settings.DecisionImpact{Examples: []settings.ImpactExample{}}}
	switch {
	case w.qualitySync == nil || w.qualitySync.Impact == nil:
		answer.Reason = "no decision engine to compare with"
	default:
		impact, err := w.qualitySync.Impact(r.Context(), snap.Policy, candidate.Policy)
		if err != nil {
			answer.Reason = "the impact could not be computed: " + safeImpactError(err)
		} else {
			answer.Available, answer.Impact = true, impact
			if answer.Impact.Examples == nil {
				answer.Impact.Examples = []settings.ImpactExample{}
			}
		}
	}
	writeJSON(rw, http.StatusOK, answer)
}

// safeImpactError keeps validation messages and hides anything else.
func safeImpactError(err error) string {
	var v *policy.Errors
	if errors.As(err, &v) {
		return v.Error()
	}
	return "see the log"
}

func (w *Web) apiProfileDefaults(rw http.ResponseWriter, r *http.Request) {
	if w.refuseUnavailable(rw) {
		return
	}
	defaults, err := w.settings.ProfileDefaults(r.Context())
	if err != nil {
		w.settingsError(rw, r, err, nil)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"source": "db", "defaults": defaults, "categories": settings.ProfileCategories})
}

func (w *Web) apiProfileDefaultsWrite(rw http.ResponseWriter, r *http.Request) {
	if w.refuseUnavailable(rw) {
		return
	}
	var body struct {
		Defaults settings.ProfileDefaults `json:"defaults"`
	}
	if !decodeSettingsJSON(rw, r, &body) {
		return
	}
	saved, err := w.settings.SetProfileDefaults(r.Context(), settingsActor(r), body.Defaults)
	if err != nil {
		w.settingsError(rw, r, err, nil)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"source": "db", "defaults": saved, "categories": settings.ProfileCategories})
}
