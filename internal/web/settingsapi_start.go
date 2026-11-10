package web

import (
	"encoding/json"
	"net/http"
	"slices"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
)

// The one-step start of a fresh install (docs/quickstart.md):
//
//	POST setup/start   create a library that downloads at once: the version in mode active with grab on, the
//	                   optional TRaSH preset as its profile, and the global switches router and library on
//
// It is one settings save: one generation and one confirmation, which lists the activation, the grab switch and
// each global switch it turns on. An install that migrates from an arr (import.arr holds an instance) is refused:
// there the library starts in shadow and goes live through the go-live checks.

type setupStartBody struct {
	Generation        *int64          `json:"generation"`
	Key               string          `json:"key"`
	Config            json.RawMessage `json:"config"`
	Roots             *[]string       `json:"roots"`
	ConfirmationToken string          `json:"confirmation_token"`
	// Preset, when sent, builds the library's profile from a TRaSH preset in the same save; the profile and its
	// format set take the library's key.
	Preset *struct {
		Flavour   string   `json:"flavour"`
		Preset    string   `json:"preset"`
		Modifiers []string `json:"modifiers"`
	} `json:"preset"`
}

// SetupStarted is the answer of POST setup/start.
type SetupStarted struct {
	Generation int64  `json:"generation"`
	Revision   string `json:"revision"`
	// ForcedOff names the global switches (router, library) the environment keeps off although this save turned
	// them on: nothing downloads, or no link is written, until the override is removed.
	ForcedOff []string `json:"forced_off"`
}

func (w *Web) mountSetupStartAPI(mux *http.ServeMux) {
	w.handle(mux, "POST "+APIBase+"/setup/start", SignedIn, auth.PermAdmin, w.apiSetupStart)
}

func (w *Web) apiSetupStart(rw http.ResponseWriter, r *http.Request) {
	if w.settings == nil {
		writeJSON(rw, http.StatusConflict, apiError{Error: "settings are not stored in the database: the one-step start cannot save"})
		return
	}
	var body setupStartBody
	if !decodeSettingsJSON(rw, r, &body) {
		return
	}
	if body.Key == "" {
		badRequest(rw, "key is required")
		return
	}
	snap, ok := w.settingsAt(rw, r, body.Generation)
	if !ok {
		return
	}
	if arrInstances(snap.Policy) > 0 {
		writeJSON(rw, http.StatusConflict, apiError{Error: "this install migrates from an arr: create the library in shadow"})
		return
	}
	if snap.Records.Switches == nil {
		writeJSON(rw, http.StatusConflict, apiError{Error: "global switches have not been seeded; restart the service"})
		return
	}
	req, err := versionSettingsRequest(snap, versionSettingsBody{Generation: body.Generation, Key: body.Key, Config: body.Config,
		Roots: body.Roots}, true)
	if err != nil {
		writeBodyError(rw, err, nil)
		return
	}
	version := &req.Records.Versions[len(req.Records.Versions)-1]
	if body.Preset != nil {
		build := w.buildPreset(rw, r, presetBody{Flavour: body.Preset.Flavour, Preset: body.Preset.Preset, Modifiers: body.Preset.Modifiers},
			snap.Records.Formats)
		if build == nil {
			return
		}
		if _, err := settings.ApplyPreset(&req.Records, body.Key, build, nil); err != nil {
			w.settingsError(rw, r, err, nil)
			return
		}
		version.Config.Profile = body.Key
	}
	version.Config.Mode, version.Config.Grab = policy.ModeActive, true
	// A library that starts downloading needs something to search and something to add to among its own picks: an
	// enabled indexer and an enabled provider with their credentials, as GET /setup counts them.
	indexers, providers := snap.ReadyKeys(w.settingsLookup)
	usable := func(picked, ready []string) bool {
		return slices.ContainsFunc(picked, func(k string) bool { return slices.Contains(ready, k) })
	}
	switch {
	case !usable(version.Config.Indexers, indexers):
		writeJSON(rw, http.StatusUnprocessableEntity, apiError{Error: "none of the library's indexers is enabled and ready: it could not search; enable one, or pick one that is"})
		return
	case !usable(version.Config.Providers, providers):
		writeJSON(rw, http.StatusUnprocessableEntity, apiError{Error: "none of the library's debrid providers is enabled with an API key: nothing could be added; enable one, or pick one that is"})
		return
	}
	switches := *snap.Records.Switches
	switches.RouterEnabled, switches.LibraryEnabled = true, true
	req.Records.Switches = &switches
	req.Actor, req.ConfirmationToken = settingsActor(r), confirmationToken(r, body.ConfirmationToken)
	saved, err := w.settings.SaveAndApply(r.Context(), req)
	if err != nil {
		w.settingsError(rw, r, err, nil)
		return
	}
	out := SetupStarted{Generation: saved.Generation, Revision: saved.Revision, ForcedOff: []string{}}
	if saved.Policy != nil {
		for _, key := range []string{"router", "library"} {
			if slices.Contains(saved.Policy.ForcedOff, key) {
				out.ForcedOff = append(out.ForcedOff, key)
			}
		}
	}
	rw.Header().Set("ETag", generationETag(saved.Generation))
	writeJSON(rw, http.StatusOK, out)
}

// arrInstances counts the arr instances the install migrates from (import.arr).
func arrInstances(p *policy.Policy) int {
	if p == nil || p.Import == nil {
		return 0
	}
	return len(p.Import.Arr)
}
