package web

// The quality profile pages: the profiles and formats overview, TRaSH presets, building a profile
// from one, the decision impact of a plain profile edit and the default profile per kind of version. Reads work under
// reads and writes use the settings database.

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/trashbundle"
)

// The TRaSH trees the profile pages read: the local checkout (THEATARR_TRASH) when it loads, otherwise the snapshot
// bundled with the binary (internal/trashbundle). A quality sync preview or a run started from the page
// without a checkout uses the bundle too (a scheduled run does not); the recyclarr
// import and the policy build read the configured checkout alone.
const (
	trashFromCheckout = "checkout"
	trashFromBundle   = "bundled"
)

// trashState is what the pages read: the tree of each flavour that loads, where they come from, and the commit when
// it is known.
type trashState struct {
	trees  map[string]*format.Trash
	source string // trashFromCheckout or trashFromBundle; empty when there is no tree at all
	commit string
	date   string // the bundled commit's date
	// note says what is wrong with the configured checkout, or why there is no tree at all; empty otherwise.
	note string
}

// trashCache keeps the local checkout's trees for a few minutes: the pages read them on every load, and a sync run
// that updates the checkout is picked up when the entry expires.
type trashCache struct {
	mu    sync.Mutex
	root  string
	trees map[string]*format.Trash
	// commit is the checkout's git commit, empty for an exported tree.
	commit string
	at     time.Time
	loaded bool
}

const trashCacheTTL = 5 * time.Minute

var trashFlavours = []string{string(format.Radarr), string(format.Sonarr)}

// bundledTrash is the bundled snapshot's trees, parsed once per process.
var bundledTrash = sync.OnceValues(func() (map[string]*format.Trash, error) {
	fsys, err := trashbundle.FS()
	if err != nil {
		return nil, err
	}
	out := map[string]*format.Trash{}
	for _, fl := range trashFlavours {
		t, err := format.LoadTrashFS(fsys, format.Flavour(fl))
		if err != nil {
			return nil, err
		}
		out[fl] = t
	}
	return out, nil
})

func (w *Web) trashTreeRoot() string {
	if w.trashRoot != "" {
		return w.trashRoot
	}
	if w.qualitySync != nil {
		return w.qualitySync.TrashRoot
	}
	return ""
}

// checkoutTrash is the trees that load from the checkout at root, and its commit.
func (w *Web) checkoutTrash(root string) (map[string]*format.Trash, string) {
	c := &w.trashCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded && c.root == root && time.Since(c.at) < trashCacheTTL {
		return c.trees, c.commit
	}
	trees := map[string]*format.Trash{}
	for _, fl := range trashFlavours {
		if tree, err := format.LoadTrash(root, format.Flavour(fl)); err == nil && (len(tree.Profiles) > 0 || len(tree.Formats) > 0) {
			trees[fl] = tree
		}
	}
	c.root, c.trees, c.commit, c.at, c.loaded = root, trees, "", time.Now(), true
	if len(trees) > 0 {
		c.commit = settings.LocalTrashCommit(root)
	}
	return c.trees, c.commit
}

// trash is the trees the pages read. A checkout that loads for at least one flavour is used alone, as before there
// was a bundle: a flavour it lacks stays unavailable, so two commits are never mixed. Without one, the bundle.
func (w *Web) trash() trashState {
	root := w.trashTreeRoot()
	note := ""
	if root != "" {
		if trees, commit := w.checkoutTrash(root); len(trees) > 0 {
			return trashState{trees: trees, source: trashFromCheckout, commit: commit}
		}
		note = "THEATARR_TRASH is set but holds no TRaSH-Guides checkout"
	}
	trees, err := bundledTrash()
	if err != nil {
		// Cannot happen with the archive the tests check; the pages then say presets are unavailable.
		if note == "" {
			note = "No local TRaSH guides tree (THEATARR_TRASH is not set)"
		}
		return trashState{trees: map[string]*format.Trash{}, note: note + ", and the bundled TRaSH defaults could not be read"}
	}
	if note != "" {
		note += ": the TRaSH defaults bundled with this version are used instead"
	}
	return trashState{trees: trees, source: trashFromBundle, commit: trashbundle.Commit(), date: trashbundle.Date(), note: note}
}

// trashTree is flavour's TRaSH tree, nil when none loads.
func (w *Web) trashTree(flavour string) *format.Trash {
	if !format.Flavour(flavour).Valid() {
		return nil
	}
	return w.trash().trees[flavour]
}

// noTrashNote says why a flavour has no tree: the checkout in THEATARR_TRASH lacks it, or nothing could be read.
func noTrashNote(st trashState) string {
	if st.source == trashFromCheckout {
		return "THEATARR_TRASH is set but holds no TRaSH-Guides checkout"
	}
	return st.note
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
	// TrashNote says what is wrong with the TRaSH tree: THEATARR_TRASH is set but holds no checkout (the bundled
	// defaults are then used), or no tree could be read at all. Empty otherwise.
	TrashNote string `json:"trash_note,omitempty"`
	// TrashSource is where the trees in trash come from: "checkout" (THEATARR_TRASH) or "bundled" (the snapshot in
	// the binary). TrashCommit is their TRaSH-Guides commit when known, TrashCommitDate the bundled commit's date.
	TrashSource     string `json:"trash_source,omitempty"`
	TrashCommit     string `json:"trash_commit,omitempty"`
	TrashCommitDate string `json:"trash_commit_date,omitempty"`
}

func (w *Web) apiQualityOverview(rw http.ResponseWriter, r *http.Request) {
	trash := w.trash()
	in := settings.OverviewInput{Policy: w.pol, Trash: trash.trees}
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
	if trash.source != trashFromCheckout {
		// The bundled defaults serve the presets and the profile comparisons only: the format summaries (the
		// Custom Formats page) stay what they are without a checkout.
		in.Trash = nil
		answer.Formats = settings.QualityOverviewOf(in).Formats
	}
	answer.TrashNote, answer.TrashSource, answer.TrashCommit, answer.TrashCommitDate = trash.note, trash.source, trash.commit, trash.date
	writeJSON(rw, http.StatusOK, answer)
}

// presetTree answers 400 for an unknown flavour and 503 when no TRaSH tree loads for it: the checkout in
// THEATARR_TRASH lacks the flavour, or (never, in a tested build) the bundled defaults cannot be read.
func (w *Web) presetTree(rw http.ResponseWriter, flavour string) *format.Trash {
	if !format.Flavour(flavour).Valid() {
		badRequest(rw, "flavour must be radarr or sonarr")
		return nil
	}
	t := w.trashTree(flavour)
	if t == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "no TRaSH guides tree for " + flavour + " (THEATARR_TRASH): presets need it"})
	}
	return t
}

// presetsAnswer is the presets of one flavour and where they come from.
type presetsAnswer struct {
	Flavour string            `json:"flavour"`
	Presets []settings.Preset `json:"presets"`
	// Unavailable is set, with an empty list, when no TRaSH tree loads for the flavour.
	Unavailable string `json:"unavailable,omitempty"`
	// Source is "checkout" (THEATARR_TRASH) or "bundled" (the snapshot in the binary); Commit is the TRaSH-Guides
	// commit when known and CommitDate the bundled commit's date.
	Source     string `json:"source,omitempty"`
	Commit     string `json:"commit,omitempty"`
	CommitDate string `json:"commit_date,omitempty"`
}

// apiPresets lists flavour's presets, from the checkout in THEATARR_TRASH or else the bundled TRaSH defaults. When
// no tree loads for the flavour it answers an empty list and the reason: a state of the install, not a server error,
// so the pages that read it on every visit neither retry it nor log errors.
func (w *Web) apiPresets(rw http.ResponseWriter, r *http.Request) {
	flavour := r.URL.Query().Get("flavour")
	if !format.Flavour(flavour).Valid() {
		badRequest(rw, "flavour must be radarr or sonarr")
		return
	}
	st := w.trash()
	answer := presetsAnswer{Flavour: flavour, Presets: []settings.Preset{}, Source: st.source, Commit: st.commit, CommitDate: st.date}
	t := st.trees[flavour]
	if t == nil {
		answer.Unavailable = noTrashNote(st) + ": TRaSH presets need it"
		writeJSON(rw, http.StatusOK, answer)
		return
	}
	presets, err := w.presets(st, flavour)
	if err != nil {
		w.internal(rw, err)
		return
	}
	answer.Presets = presets
	writeJSON(rw, http.StatusOK, answer)
}

// bundledPresets is the preset list of each bundled flavour, built once: the snapshot never changes.
var bundledPresets sync.Map // flavour -> func() ([]settings.Preset, error)

func (w *Web) presets(st trashState, flavour string) ([]settings.Preset, error) {
	t := st.trees[flavour]
	if st.source != trashFromBundle {
		return settings.Presets(t)
	}
	once, _ := bundledPresets.LoadOrStore(flavour, sync.OnceValues(func() ([]settings.Preset, error) { return settings.Presets(t) }))
	presets, err := once.(func() ([]settings.Preset, error))()
	if err != nil {
		return nil, err
	}
	// Copies: the handler's answer must not share the cached slices.
	out := make([]settings.Preset, len(presets))
	for i, p := range presets {
		p.Qualities, p.Modifiers = slices.Clone(p.Qualities), slices.Clone(p.Modifiers)
		out[i] = p
	}
	return out, nil
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
