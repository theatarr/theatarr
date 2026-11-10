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
		name     string
		stored   any // title.series_type
		flag     int
		imported string // arr_title.series_type
		want     string
	}{
		{"standard with the anime flag", "standard", 1, "", "standard"},
		{"anime without the flag", "anime", 0, "", "anime"},
		{"daily", "daily", 0, "", "daily"},
		{"daily over another imported type", "daily", 0, "anime", "daily"},
		{"empty with the flag", nil, 1, "", "anime"},
		{"empty without the flag", nil, 0, "", "standard"},
		{"empty with an imported type", nil, 0, "daily", "daily"},
	} {
		for _, base := range seriesTypeViews {
			t.Run(tc.name+" "+base, func(t *testing.T) {
				e := seriesTypeEnv(t)
				e.exec(`UPDATE title SET series_type = ?, anime = ? WHERE id = 10`, tc.stored, tc.flag)
				e.exec(`UPDATE arr_title SET series_type = ? WHERE title_id = 10`, tc.imported)
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
// loads it for an imported copy, whose arr had another type.
func TestSeriesTypeEdit(t *testing.T) {
	for _, base := range seriesTypeViews {
		t.Run(base, func(t *testing.T) {
			e := seriesTypeEnv(t)
			e.exec(`UPDATE title SET series_type = 'standard' WHERE id = 10`)
			e.exec(`UPDATE arr_title SET series_type = 'standard' WHERE title_id = 10`)
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
			// Version 10 is the imported copy (sonarr_4k_debrid, type standard there).
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

// A profile change in a PUT that sends back the anime type the view showed does not read it as an anime hint: the
// title's flag chooses the profile's libraries, as for any other type.
func TestSeriesTypeEchoIsNoAnimeHint(t *testing.T) {
	e := newEnv(t)
	v := e.pol.Views["sonarr"]
	v.Profiles = map[string][]string{"Rules default": {"tv-2160p"}, "Both": {"tv-2160p", "anime-tv"}}
	e.start()
	e.exec(`UPDATE title SET series_type = 'anime', anime = 0 WHERE id = 10`)
	e.exec(`UPDATE version SET monitored = 0 WHERE id = 10`)
	path := fmt.Sprintf("/sonarr/api/v3/series/%d", IDBase+10)
	var s map[string]any
	e.call("GET", path, nil, 200, &s)
	if s["seriesType"] != "anime" {
		t.Fatal("fixture", s["seriesType"])
	}
	s["qualityProfileId"] = e.profileID("sonarr", "Both")
	s["monitored"] = true
	e.call("PUT", path, s, 202, &s)
	if n := e.one(`SELECT COUNT(*) FROM version WHERE title_id = 10 AND policy_key = 'anime-tv'`); n != 0 {
		t.Error("the echoed anime type turned on the anime library")
	}
	if n := e.one(`SELECT monitored FROM version WHERE id = 10`); n != 1 {
		t.Error("the profile's library for a title without the anime flag was not turned on")
	}
	if s["seriesType"] != "anime" {
		t.Errorf("PUT answered %v", s["seriesType"])
	}
	// A type the user changes to anime in the same PUT is a hint, as before.
	e.exec(`UPDATE title SET series_type = 'standard' WHERE id = 10`)
	e.exec(`UPDATE version SET monitored = 0 WHERE id = 10`)
	e.call("GET", path, nil, 200, &s)
	s["qualityProfileId"] = e.profileID("sonarr", "Rules default")
	e.call("PUT", path, s, 202, &s)
	s["qualityProfileId"] = e.profileID("sonarr", "Both")
	s["seriesType"] = "anime"
	e.call("PUT", path, s, 202, &s)
	if n := e.one(`SELECT COUNT(*) FROM version WHERE title_id = 10 AND policy_key = 'anime-tv'`); n != 1 {
		t.Error("an edit to anime with a profile change did not choose the anime library")
	}
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
