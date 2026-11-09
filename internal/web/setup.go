package web

// The first-run screen and the install preflight (docs/quickstart.md, internal/preflight):
//
//	GET  setup       what a fresh install still lacks: versions, sources, roots, the first-start password
//	POST preflight   run the install checks; {"sources": false} skips the provider and indexer tests
//
// Both are admin only. Neither writes settings: the setup screen creates everything through the settings API, with
// its confirmations, and the preflight's source tests are the settings tests (bounded, audited, through the gateway).

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/golive"
	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/preflight"
	"github.com/theatarr/theatarr/internal/settings"
)

// PreflightHooks are the preflight checks that need the running service; each may be nil.
type PreflightHooks struct {
	// Mount checks the debrid mount's directory; nil uses health.Mount with the library's stat timeout.
	Mount func(ctx context.Context, dir string) health.Result
	// Plex asks the Plex server for its identity through the gateway.
	Plex func(ctx context.Context) error
	// Views asks each arr view on the service's own listener.
	Views func(ctx context.Context) []preflight.View
	// Caps lists the query types an indexer answers.
	Caps func(key string) []string
}

// SetupState is GET /setup.
type SetupState struct {
	VersionStates []VersionGrabSummary `json:"version_states"`
	// FirstRun: the install has no versions yet; the UI leads through the setup screen.
	FirstRun bool `json:"first_run"`
	// SettingsSource is db (the setup screen can save) or yaml (it can only show what is missing).
	SettingsSource string `json:"settings_source"`
	// AdminPasswordGenerated: the caller still signs in with the password the first start generated.
	AdminPasswordGenerated bool     `json:"admin_password_generated"`
	Missing                []string `json:"missing"`
	Versions               int      `json:"versions"`
	VersionsWithRoots      int      `json:"versions_with_roots"`
	// Providers and Indexers count the sources that would run: enabled, with their credential set when they take one.
	Providers int `json:"providers"`
	Indexers  int `json:"indexers"`
	Profiles  int `json:"profiles"`
	APIKeys   int `json:"api_keys"`
}

func (w *Web) mountSetupAPI(mux *http.ServeMux) {
	w.handle(mux, "GET "+APIBase+"/setup", SignedIn, auth.PermAdmin, w.apiSetup)
	w.handle(mux, "POST "+APIBase+"/preflight", SignedIn, auth.PermAdmin, w.apiPreflight)
}

func (w *Web) apiSetup(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := w.pol
	// With the settings database the counts and the credentials come from one snapshot, as in the preflight.
	var snap *settings.Snapshot
	if w.settings != nil {
		var err error
		if snap, err = w.settings.Snapshot(ctx); err != nil {
			w.settingsError(rw, r, err, nil)
			return
		}
		p = snap.Policy
	}
	out := SetupState{VersionStates: versionGrabSummaries(p), SettingsSource: "db"}
	if p != nil {
		out.Versions, out.Profiles = len(p.Versions), len(p.Profiles)
		// The sources that would run (settings.Snapshot.Ready), the same count as SetupCheck's: a disabled record,
		// or a provider or keyed indexer without its credential, adds nothing.
		if snap != nil {
			out.Indexers, out.Providers = snap.Ready(w.settingsLookup)
		} else {
			out.Indexers = len(p.Indexers)
			creds := settings.Credentials{Policy: p, Lookup: w.settingsLookup}
			for _, fact := range golive.InspectProviders(p, sortedKeys(p.Providers), creds.Resolves) {
				if fact.Credential {
					out.Providers++
				}
			}
		}
		lib := p.LibrarySettings()
		for k := range p.Versions {
			if lv := lib.Versions[k]; lv != nil && len(lv.Roots) > 0 {
				out.VersionsWithRoots++
			}
		}
	}
	out.Missing = preflight.Missing(p, out.Indexers, out.Providers)
	out.FirstRun = out.Versions == 0
	if who := auth.FromContext(ctx); who != nil && who.Kind == "session" {
		var generated bool
		err := w.st.Reader().QueryRowContext(ctx, `SELECT password_changed_at = created_at FROM web_user WHERE id = ?
			AND username = ?`, who.UserID, auth.AdminUsername).Scan(&generated)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			w.internal(rw, err)
			return
		}
		out.AdminPasswordGenerated = generated
	}
	n, err := w.activeAPIKeys(ctx)
	if err != nil {
		w.internal(rw, err)
		return
	}
	out.APIKeys = n
	writeJSON(rw, http.StatusOK, out)
}

func (w *Web) activeAPIKeys(ctx context.Context) (int, error) {
	var n int
	err := w.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM api_key WHERE revoked_at IS NULL`).Scan(&n)
	return n, err
}

func (w *Web) apiPreflight(rw http.ResponseWriter, r *http.Request) {
	var body struct {
		Sources *bool `json:"sources"`
	}
	if !decodeSettingsJSON(rw, r, &body) {
		return
	}
	ctx := r.Context()
	p := w.pol
	// One settings snapshot for everything the preflight reads: with the settings database its policy,
	// credentials and sources all come from the stored head, never the startup policy mixed with the head's records.
	var snap *settings.Snapshot
	if w.settings != nil {
		var err error
		if snap, err = w.settings.Snapshot(ctx); err != nil {
			w.settingsError(rw, r, err, nil)
			return
		}
		p = snap.Policy
	}
	if p == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "no policy loaded"})
		return
	}
	hooks := w.preflight
	lib := p.LibrarySettings()
	o := preflight.Options{Policy: p, FS: golive.OSFS{}, Device: preflight.OSDevice, Mount: hooks.Mount, Plex: hooks.Plex,
		Views: hooks.Views, APIKeys: -1, Now: w.now()}
	if o.Mount == nil {
		o.Mount = func(ctx context.Context, dir string) health.Result {
			return health.Mount(dir, lib.StatTimeout)(ctx)
		}
	}
	if w.health != nil {
		o.Health = w.health.Results(ctx)
	}
	if n, err := w.activeAPIKeys(ctx); err == nil {
		o.APIKeys = n
	}
	o.Credentials = settings.Credentials{Policy: p, Snap: snap, Lookup: w.settingsLookup}
	if (body.Sources == nil || *body.Sources) && snap != nil && w.settingsTester != nil {
		actor := settingsActor(r)
		o.Sources = func(ctx context.Context, entity string) []preflight.Source {
			return w.preflightSources(ctx, actor, snap, entity)
		}
	} else if body.Sources != nil && !*body.Sources {
		o.Plex = nil
	}
	writeJSON(rw, http.StatusOK, preflight.Run(ctx, o))
}

// preflightSources runs the settings tests of every enabled source of one kind, as POST <entity>/testall does.
func (w *Web) preflightSources(ctx context.Context, actor string, snap *settings.Snapshot, entity string) []preflight.Source {
	var items []testItem
	for _, rec := range w.settingsRecords(ctx, snap, entity) {
		if rec.Enabled == nil || *rec.Enabled {
			items = append(items, savedItem(snap, entity, rec.ID, rec.Key))
		}
	}
	var out []preflight.Source
	for _, res := range w.runTests(ctx, actor, items) {
		s := preflight.Source{Key: res.Key, Status: res.Status, Category: res.Category, Message: res.Message,
			Results: res.Results}
		if res.Account != nil {
			s.Premium, s.Expires = res.Account.Premium, res.Account.Expires
		}
		if entity == entityIndexers && w.preflight.Caps != nil {
			s.Caps = w.preflight.Caps(res.Key)
		}
		out = append(out, s)
	}
	return out
}
