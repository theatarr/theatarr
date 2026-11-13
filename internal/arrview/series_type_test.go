package arrview

import (
	"context"
	"fmt"
	"testing"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/store"
)

// seriesTypeViews are the views of the seeded series once it has a copy in both libraries: the two per-library
// views and the combined one.
var seriesTypeViews = []string{"/v/tv-2160p/sonarr", "/v/anime-tv/sonarr", "/sonarr"}

// seriesTypeEnv gives the seeded series (title 10, imported into tv-2160p) a second copy in anime-tv.
func seriesTypeEnv(t *testing.T) *env {
	e := newEnv(t)
	e.exec(`INSERT INTO version (id, title_id, policy_key, monitored, state, created_at, updated_at)
		VALUES (11, 10, 'anime-tv', 1, 'wanted', 0, 0)`)
	return e
}

func (e *env) effectiveSeriesType(titleID int64) string {
	e.t.Helper()
	typ, err := store.SeriesType(context.Background(), e.st.Reader(), titleID)
	if err != nil {
		e.t.Fatal(err)
	}
	return typ
}

// The series type is one value per series: every view shows it, and a view's answer sent back unchanged changes
// neither the effective type nor what is stored, whatever the stored state.
func TestSeriesTypeEchoChangesNothing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stored any // title.series_type
		flag   int
		want   string
	}{
		{"standard with the anime flag", "standard", 1, "standard"},
		{"anime without the flag", "anime", 0, "anime"},
		{"daily", "daily", 0, "daily"},
		{"empty with the flag", nil, 1, "anime"},
		{"empty without the flag", nil, 0, "standard"},
	} {
		for _, base := range seriesTypeViews {
			t.Run(tc.name+" "+base, func(t *testing.T) {
				e := seriesTypeEnv(t)
				e.exec(`UPDATE title SET series_type = ?, anime = ? WHERE id = 10`, tc.stored, tc.flag)
				if got := e.effectiveSeriesType(10); got != tc.want {
					t.Fatalf("effective type %q, want %q", got, tc.want)
				}
				for _, b := range seriesTypeViews {
					var s map[string]any
					e.call("GET", fmt.Sprintf("%s/api/v3/series/%d", b, IDBase+10), nil, 200, &s)
					if s["seriesType"] != tc.want {
						t.Errorf("%s shows %v, want %q", b, s["seriesType"], tc.want)
					}
				}
				path := fmt.Sprintf("%s/api/v3/series/%d", base, IDBase+10)
				var s, put map[string]any
				e.call("GET", path, nil, 200, &s)
				e.call("PUT", path, s, 202, &put)
				if put["seriesType"] != s["seriesType"] {
					t.Errorf("PUT answered %v, GET %v", put["seriesType"], s["seriesType"])
				}
				if got := e.effectiveSeriesType(10); got != tc.want {
					t.Errorf("effective type after the echo %q, want %q", got, tc.want)
				}
				stored, _ := tc.stored.(string)
				if got := e.str(`SELECT COALESCE(series_type, '') FROM title WHERE id = 10`); got != stored {
					t.Errorf("the echo stored %q, was %q", got, stored)
				}
				if got := e.one(`SELECT anime FROM title WHERE id = 10`); got != int64(tc.flag) {
					t.Errorf("the echo set the anime flag to %d", got)
				}
			})
		}
	}
}

// A type other than the one shown is an edit of the title's type from any view: every view shows it and decide
// loads it for an imported copy.
func TestSeriesTypeEdit(t *testing.T) {
	for _, base := range seriesTypeViews {
		t.Run(base, func(t *testing.T) {
			e := seriesTypeEnv(t)
			e.exec(`UPDATE title SET series_type = 'standard' WHERE id = 10`)
			path := fmt.Sprintf("%s/api/v3/series/%d", base, IDBase+10)
			var s, put map[string]any
			e.call("GET", path, nil, 200, &s)
			s["seriesType"] = "daily"
			e.call("PUT", path, s, 202, &put)
			if put["seriesType"] != "daily" {
				t.Errorf("PUT answered %v", put["seriesType"])
			}
			if got := e.str(`SELECT series_type FROM title WHERE id = 10`); got != "daily" {
				t.Errorf("stored %q", got)
			}
			if got := e.one(`SELECT anime FROM title WHERE id = 10`); got != 0 {
				t.Error("the edit set the anime flag")
			}
			for _, b := range seriesTypeViews {
				e.call("GET", fmt.Sprintf("%s/api/v3/series/%d", b, IDBase+10), nil, 200, &s)
				if s["seriesType"] != "daily" {
					t.Errorf("%s shows %v", b, s["seriesType"])
				}
			}
			ctx := context.Background()
			title, err := decide.LoadTitle(ctx, e.st.Reader(), 10)
			if err != nil {
				t.Fatal(err)
			}
			// Version 10 is the imported copy (sonarr_4k_debrid).
			if err := decide.LoadSeriesVersion(ctx, e.st.Reader(), &title, 10); err != nil {
				t.Fatal(err)
			}
			if title.SeriesType != "daily" {
				t.Errorf("decide loads %q for the imported copy", title.SeriesType)
			}
		})
	}
}

// An add gives a series a type only when it has none; a later add leaves a stored type alone.
func TestSeriesTypeAddKeepsStored(t *testing.T) {
	e := newEnv(t)
	body := map[string]any{"title": "The Show", "tvdbId": 201, "monitored": true, "seriesType": "daily",
		"qualityProfileId": e.profileID("sonarr", "Rules default")}
	var s map[string]any
	e.call("POST", "/sonarr/api/v3/series", body, 201, &s)
	if got := e.str(`SELECT series_type FROM title WHERE id = 10`); got != "daily" || s["seriesType"] != "daily" {
		t.Fatalf("add to a series with no type: stored %q, answered %v", got, s["seriesType"])
	}
	body["seriesType"] = "standard"
	e.call("POST", "/sonarr/api/v3/series", body, 201, &s)
	if got := e.str(`SELECT series_type FROM title WHERE id = 10`); got != "daily" || s["seriesType"] != "daily" {
		t.Errorf("a later add: stored %q, answered %v", got, s["seriesType"])
	}
}

// searchable fails the test when a library the title has on is one acquisition skips for it: acquisition takes a
// library whose anime setting matches the title's flag or anime type (internal/acquire targets).
func (e *env) searchable(titleID int64) {
	e.t.Helper()
	anime := e.one(`SELECT anime FROM title WHERE id = ?`, titleID) == 1 || e.effectiveSeriesType(titleID) == "anime"
	for k, vp := range e.pol.Versions {
		on := e.one(`SELECT COUNT(*) FROM version v WHERE v.title_id = ? AND v.policy_key = ? AND v.monitored
			AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id)`, titleID, k) == 1
		if on && vp.Anime != nil && *vp.Anime != anime {
			e.t.Errorf("library %s is on, and acquisition skips it for a title that is anime=%v", k, anime)
		}
	}
}

// seriesTypeLibraries is the seeded series with its copy in a library that takes any title, and a profile of the
// anime library and the non-anime one, so a profile change or an add has to choose between the two by the title's
// classification.
func seriesTypeLibraries(t *testing.T, stored string) (*env, string) {
	e := newEnv(t)
	cp := *e.pol.Versions["tv-2160p"]
	cp.Anime = nil
	e.pol.Versions["tv-any"] = &cp
	v := e.pol.Views["sonarr"]
	v.Versions = []string{"tv-any", "tv-2160p", "anime-tv"}
	v.Profiles = map[string][]string{"Rules default": {"tv-any"}, "Both": {"tv-2160p", "anime-tv"}}
	e.start()
	e.exec(`UPDATE title SET series_type = ?, anime = 0 WHERE id = 10`, stored)
	e.exec(`UPDATE version SET policy_key = 'tv-any' WHERE id = 10`)
	return e, fmt.Sprintf("/sonarr/api/v3/series/%d", IDBase+10)
}

func (e *env) libraryOn(key string) bool {
	e.t.Helper()
	return e.one(`SELECT COUNT(*) FROM version WHERE title_id = 10 AND policy_key = ? AND monitored`, key) == 1
}

// The libraries a view turns on are chosen by what acquisition will take the title for once the request is
// stored: its anime flag, or its type then. A type sent back unchanged counts through the stored type; a changed
// one counts because it becomes the stored type.
func TestSeriesTypeChoosesSearchableLibraries(t *testing.T) {
	for _, tc := range []struct {
		name         string
		stored, send string // send: the type the PUT sends; "" sends the view's answer back
		want         string // the library turned on
	}{
		{"echoed anime", "anime", "", "anime-tv"},
		{"echoed standard", "standard", "", "tv-2160p"},
		{"changed to anime", "standard", "anime", "anime-tv"},
		{"changed to standard", "anime", "standard", "tv-2160p"},
	} {
		t.Run("PUT "+tc.name, func(t *testing.T) {
			e, path := seriesTypeLibraries(t, tc.stored)
			var s map[string]any
			e.call("GET", path, nil, 200, &s)
			want := tc.stored
			if tc.send != "" {
				s["seriesType"], want = tc.send, tc.send
			}
			s["qualityProfileId"] = e.profileID("sonarr", "Both")
			e.call("PUT", path, s, 202, &s)
			if s["seriesType"] != want || e.str(`SELECT series_type FROM title WHERE id = 10`) != want {
				t.Errorf("type after the PUT: answered %v", s["seriesType"])
			}
			for _, k := range []string{"tv-2160p", "anime-tv"} {
				if on := e.libraryOn(k); on != (k == tc.want) {
					t.Errorf("library %s on=%v, want %s only", k, on, tc.want)
				}
			}
			e.searchable(10)
		})
	}
	// An add never changes a stored type, so the stored type chooses, not the request's.
	for _, tc := range []struct{ stored, send, want string }{
		{"standard", "anime", "tv-2160p"}, {"anime", "standard", "anime-tv"},
	} {
		t.Run("POST "+tc.send+" on stored "+tc.stored, func(t *testing.T) {
			e, _ := seriesTypeLibraries(t, tc.stored)
			var s map[string]any
			e.call("POST", "/sonarr/api/v3/series", map[string]any{"title": "The Show", "tvdbId": 201, "monitored": true,
				"seriesType": tc.send, "qualityProfileId": e.profileID("sonarr", "Both")}, 201, &s)
			if s["seriesType"] != tc.stored {
				t.Errorf("the add answered %v", s["seriesType"])
			}
			for _, k := range []string{"tv-2160p", "anime-tv"} {
				if on := e.libraryOn(k); on != (k == tc.want) {
					t.Errorf("library %s on=%v, want %s only", k, on, tc.want)
				}
			}
			e.searchable(10)
		})
	}
	// A held series with no type takes the add's, which then chooses.
	t.Run("POST anime on no stored type", func(t *testing.T) {
		e, _ := seriesTypeLibraries(t, "")
		e.exec(`UPDATE title SET series_type = NULL WHERE id = 10`)
		e.call("POST", "/sonarr/api/v3/series", map[string]any{"title": "The Show", "tvdbId": 201, "monitored": true,
			"seriesType": "anime", "qualityProfileId": e.profileID("sonarr", "Both")}, 201, nil)
		if !e.libraryOn("anime-tv") || e.libraryOn("tv-2160p") || e.effectiveSeriesType(10) != "anime" {
			t.Errorf("anime-tv on=%v, tv-2160p on=%v, type %q", e.libraryOn("anime-tv"), e.libraryOn("tv-2160p"),
				e.effectiveSeriesType(10))
		}
		e.searchable(10)
	})
	// The editor chooses the same way.
	t.Run("editor", func(t *testing.T) {
		e, _ := seriesTypeLibraries(t, "anime")
		e.call("PUT", "/sonarr/api/v3/series/editor", map[string]any{"seriesIds": []int64{IDBase + 10},
			"qualityProfileId": e.profileID("sonarr", "Both")}, 202, nil)
		if !e.libraryOn("anime-tv") {
			t.Error("the editor did not turn on the anime library for a series of type anime")
		}
		e.searchable(10)
	})
}

// The editor is outside the echo rule: a series type it is given is stored, also when it is the one shown; without
// the field nothing changes.
func TestSeriesTypeEditor(t *testing.T) {
	e := newEnv(t)
	e.exec(`UPDATE title SET anime = 1 WHERE id = 10`)
	ids := []int64{IDBase + 10}
	e.call("PUT", "/sonarr/api/v3/series/editor", map[string]any{"seriesIds": ids, "monitored": true}, 202, nil)
	if got := e.str(`SELECT COALESCE(series_type, '') FROM title WHERE id = 10`); got != "" {
		t.Fatalf("the editor without a type stored %q", got)
	}
	e.call("PUT", "/sonarr/api/v3/series/editor", map[string]any{"seriesIds": ids, "seriesType": "anime"}, 202, nil)
	if got := e.str(`SELECT COALESCE(series_type, '') FROM title WHERE id = 10`); got != "anime" {
		t.Errorf("the editor's shown type stored %q", got)
	}
	e.call("PUT", "/sonarr/api/v3/series/editor", map[string]any{"seriesIds": ids, "seriesType": "standard"}, 202, nil)
	if got := e.effectiveSeriesType(10); got != "standard" {
		t.Errorf("the editor's type: %q", got)
	}
}

// series/lookup answers the title's type for a series Theatarr holds in a library the view does not show, as the
// views that show it do; a series it does not hold keeps standard.
func TestSeriesTypeLookupOutsideView(t *testing.T) {
	e := newEnv(t)
	e.exec(`UPDATE title SET series_type = 'daily' WHERE id = 10`)
	for _, base := range []string{"/sonarr", "/v/tv-2160p/sonarr", "/v/anime-tv/sonarr"} {
		var out []map[string]any
		e.call("GET", base+"/api/v3/series/lookup?term=tvdb:201", nil, 200, &out)
		if len(out) != 1 || out[0]["seriesType"] != "daily" {
			t.Errorf("%s lookup: %v", base, out)
		}
	}
	e.exec(`UPDATE title SET series_type = NULL, anime = 1 WHERE id = 10`)
	var out []map[string]any
	e.call("GET", "/v/anime-tv/sonarr/api/v3/series/lookup?term=tvdb:201", nil, 200, &out)
	if len(out) != 1 || out[0]["seriesType"] != "anime" {
		t.Errorf("flagged series with no type: %v", out)
	}
}
