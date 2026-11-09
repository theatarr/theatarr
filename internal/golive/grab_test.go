package golive

import (
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// golive check shows the version's grab switch, and the planned offers the first sweep after the
// flip would route (fresh ones, capped a pass) or decline (expired) are counted.
func TestGrabAndPlannedOffers(t *testing.T) {
	t.Parallel()
	pol := examplePolicy(t)
	st := testStore(t)
	seed(t, st, "movie-web", "movie", "radarr_4k_web", 3, 0, 0, 0)
	fresh, old := store.Millis(now.Add(-24*time.Hour)), store.Millis(now.Add(-10*24*time.Hour))
	for i, at := range []int64{fresh, fresh, fresh, old} {
		exec(t, st, `INSERT INTO router_offer (version_key, title_id, info_hash, from_version, outcome, first_at,
			updated_at) VALUES ('movie-web', (SELECT MIN(id) FROM title), ?, 'movie-remux', 'planned', ?, ?)`,
			string(rune('a'+i)), at, at)
	}
	exec(t, st, `INSERT INTO router_offer (version_key, title_id, info_hash, from_version, outcome, first_at,
		updated_at) VALUES ('tv-2160p', (SELECT MIN(id) FROM title), 'z', 'tv', 'planned', ?, ?)`, fresh, fresh)
	r := run(t, Options{Policy: pol, Version: "movie-web", DB: st.Reader(), Env: allEnv})
	want(t, r, SecPolicy, "grab", Info, "grab is off")
	want(t, r, SecLibrary, "planned offers", Info, "4 offers were planned for movie-web")
	want(t, r, SecLibrary, "planned offers", Info, "routes up to 3 of the 3 fresh ones")
	want(t, r, SecLibrary, "planned offers", Info, "4 fresh planned in all) and declines 1 older")
	pol.Versions["movie-web"].Grab = true
	r = run(t, Options{Policy: pol, Version: "movie-web", DB: st.Reader(), Env: allEnv})
	want(t, r, SecPolicy, "grab", Info, "grab is on")
}

// An active version with grab off while router.enabled is true adds nothing and gets no automatic search: a WARN,
// since that is what going live without turning grab on leaves behind.
func TestActiveGrabOffWarns(t *testing.T) {
	t.Parallel()
	pol := examplePolicy(t)
	st := testStore(t)
	pol.Versions["movie-web"].Mode = "active"
	pol.Router.Enabled = true
	r := run(t, Options{Policy: pol, Version: "movie-web", DB: st.Reader(), Env: allEnv})
	want(t, r, SecPolicy, "grab", Warn, "grab is off")
	pol.Router.Enabled = false
	r = run(t, Options{Policy: pol, Version: "movie-web", DB: st.Reader(), Env: allEnv})
	want(t, r, SecPolicy, "grab", Info, "grab is off")
}

func TestProfileUsesLoadedSettings(t *testing.T) {
	t.Parallel()
	pol := examplePolicy(t)
	// A DB-owned setting can differ from the YAML directory retained for diagnostics.
	pol.Versions["movie-web"].Profile = "missing-db-profile"
	r := run(t, Options{Policy: pol, Version: "movie-web", DB: testStore(t).Reader(), Env: allEnv})
	want(t, r, SecPolicy, "profile", Fail, "missing-db-profile")
}

func TestAllDebridUncachedDeletePolicy(t *testing.T) {
	t.Parallel()
	pol := examplePolicy(t)
	pol.Versions["movie-web"].Providers = []string{"ad"}
	st := testStore(t)
	for _, enabled := range []bool{false, true} {
		pol.Router.AllDebridDeleteUncached = enabled
		r := run(t, Options{Policy: pol, Version: "movie-web", DB: st.Reader(), Env: allEnv})
		if !enabled {
			want(t, r, SecPolicy, "cached_only", Info, "alldebrid_delete_uncached off")
		} else {
			want(t, r, SecPolicy, "cached_only", Info, "alldebrid_delete_uncached on; list/upload race accepted")
		}
	}
}
