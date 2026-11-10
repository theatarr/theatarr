package arrview

import (
	"context"
	"fmt"
	"maps"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/availability"
)

// libraryRules is each library's minimum availability rule for a title, by library key.
func (e *env) libraryRules(titleID int64) map[string]availability.Rule {
	e.t.Helper()
	rules, err := availability.Rules(context.Background(), e.st.Reader(), e.pol, []int64{titleID})
	if err != nil {
		e.t.Fatal(err)
	}
	rows, err := e.st.Reader().Query(`SELECT id, policy_key FROM version WHERE title_id = ?`, titleID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]availability.Rule{}
	for rows.Next() {
		var id int64
		var key string
		if err := rows.Scan(&id, &key); err != nil {
			e.t.Fatal(err)
		}
		out[key] = rules[id]
	}
	return out
}

// choiceRows is every stored minimum availability of a title with the time that orders it.
func (e *env) choiceRows(titleID int64) map[string]string {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT view, minimum_availability, updated_at FROM arrview_title
		WHERE title_id = ? AND minimum_availability <> ''`, titleID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var view, value string
		var at int64
		if err := rows.Scan(&view, &value, &at); err != nil {
			e.t.Fatal(err)
		}
		out[view] = fmt.Sprintf("%s@%d", value, at)
	}
	return out
}

// tick moves the clock, so a write after it is newer than every row before.
func (e *env) tick() { e.now = e.now.Add(time.Minute) }

// minAvailStates are the stored states of Alpha (title 1: movie-remux and movie-web, both on) the views are checked
// over. Each starts from no imported value.
var minAvailStates = []struct {
	name  string
	setup func(e *env)
	// want is each library's rule in the state.
	remux, web availability.Rule
}{
	{"nothing", func(e *env) {},
		availability.Rule{Minimum: "released", Source: availability.SourceDefault},
		availability.Rule{Minimum: "released", Source: availability.SourceDefault}},
	{"imported announced", func(e *env) {
		e.exec(`UPDATE arr_title SET minimum_availability = 'announced' WHERE version_id = 1`)
	},
		availability.Rule{Minimum: "announced", Source: availability.SourceImported},
		availability.Rule{Minimum: "released", Source: availability.SourceDefault}},
	{"library row", func(e *env) {
		e.exec(`INSERT INTO arrview_title (view, title_id, minimum_availability, added_at, updated_at)
			VALUES ('v/movie-web', 1, 'inCinemas', 0, 5)`)
	},
		availability.Rule{Minimum: "released", Source: availability.SourceDefault},
		availability.Rule{Minimum: "inCinemas", Source: availability.SourceChoice}},
	{"combined row older than a library row", func(e *env) {
		e.exec(`INSERT INTO arrview_title (view, title_id, minimum_availability, added_at, updated_at)
			VALUES ('radarr', 1, 'announced', 0, 5), ('v/movie-remux', 1, 'inCinemas', 0, 6)`)
	},
		availability.Rule{Minimum: "inCinemas", Source: availability.SourceChoice},
		availability.Rule{Minimum: "announced", Source: availability.SourceChoice}},
	{"combined row newer than a library row", func(e *env) {
		e.exec(`INSERT INTO arrview_title (view, title_id, minimum_availability, added_at, updated_at)
			VALUES ('radarr', 1, 'inCinemas', 0, 6), ('v/movie-remux', 1, 'announced', 0, 5)`)
	},
		availability.Rule{Minimum: "inCinemas", Source: availability.SourceChoice},
		availability.Rule{Minimum: "inCinemas", Source: availability.SourceChoice}},
	{"library default", func(e *env) {
		e.pol.Versions["movie-web"].MinimumAvailability = "announced"
		e.pol.Versions["movie-web"].AvailabilityDelay = 4
	},
		availability.Rule{Minimum: "released", Source: availability.SourceDefault},
		availability.Rule{Minimum: "announced", Delay: 4, Source: availability.SourceDefault}},
	{"libraries differ", func(e *env) {
		e.exec(`UPDATE arr_title SET minimum_availability = 'inCinemas' WHERE version_id = 1`)
		e.exec(`INSERT INTO arrview_title (view, title_id, minimum_availability, added_at, updated_at)
			VALUES ('v/movie-web', 1, 'announced', 0, 5)`)
		e.pol.Versions["movie-remux"].MinimumAvailability = "announced"
	},
		availability.Rule{Minimum: "inCinemas", Source: availability.SourceImported},
		availability.Rule{Minimum: "announced", Source: availability.SourceChoice}},
}

// minAvailViews are the views Alpha is read and written through, with the libraries each shows for it.
var minAvailViews = []struct {
	base  string
	shows []string
}{
	{"/v/movie-remux/radarr", []string{"movie-remux"}},
	{"/v/movie-web/radarr", []string{"movie-web"}},
	{"/radarr", []string{"movie-remux", "movie-web"}},
}

func minAvailEnv(t *testing.T, setup func(e *env)) *env {
	e := newEnv(t)
	e.exec(`UPDATE arr_title SET minimum_availability = '' WHERE title_id = 1`)
	setup(e)
	e.start()
	return e
}

// The acceptance test of the Theatarr-owned minimum availability: for every view and stored state, a movie read
// from the view and PUT back unchanged leaves every library's value and where it comes from as they were, and the
// PUT answers what the GET did. So does a PUT that changes only tags, and a bulk edit without the field. The view
// shows the earliest value among the libraries it shows, the same in the list and by id.
func TestMinimumAvailabilityEchoChangesNothing(t *testing.T) {
	t.Parallel()
	for _, st := range minAvailStates {
		for _, view := range minAvailViews {
			t.Run(st.name+" on "+view.base, func(t *testing.T) {
				t.Parallel()
				e := minAvailEnv(t, st.setup)
				before := e.libraryRules(1)
				if before["movie-remux"] != st.remux || before["movie-web"] != st.web {
					t.Fatalf("state: %+v", before)
				}
				rows := e.choiceRows(1)
				var shown []string
				for _, k := range view.shows {
					shown = append(shown, before[k].Minimum)
				}
				want := availability.Earliest(shown...)
				path := fmt.Sprintf("%s/api/v3/movie/%d", view.base, IDBase+1)
				var movie map[string]any
				e.call("GET", path, nil, 200, &movie)
				if movie["minimumAvailability"] != want {
					t.Fatalf("GET shows %v, want %s", movie["minimumAvailability"], want)
				}
				var list []map[string]any
				e.call("GET", view.base+"/api/v3/movie", nil, 200, &list)
				for _, m := range list {
					if m["id"] == movie["id"] && (m["minimumAvailability"] != want || m["isAvailable"] != movie["isAvailable"]) {
						t.Errorf("list shows %v available %v", m["minimumAvailability"], m["isAvailable"])
					}
				}
				same := func(what string, out map[string]any) {
					t.Helper()
					if out != nil && (out["minimumAvailability"] != want || out["isAvailable"] != movie["isAvailable"] ||
						out["releaseDate"] != movie["releaseDate"]) {
						t.Errorf("%s answers %v available %v released %v, GET %v %v %v", what, out["minimumAvailability"],
							out["isAvailable"], out["releaseDate"], want, movie["isAvailable"], movie["releaseDate"])
					}
					if got := e.libraryRules(1); !maps.Equal(got, before) {
						t.Errorf("%s changed the libraries' rules:\n%+v\n%+v", what, before, got)
					}
					if got := e.choiceRows(1); !maps.Equal(got, rows) {
						t.Errorf("%s changed the stored choices: %v, were %v", what, got, rows)
					}
				}
				var out map[string]any
				e.tick()
				e.call("PUT", path, movie, 202, &out)
				same("PUT", out)
				e.tick()
				e.call("PUT", path, out, 202, &out)
				same("second PUT", out)
				tg := e.call("POST", view.base+"/api/v3/tag", map[string]any{"label": "t"}, 201, nil)
				movie["tags"] = []any{decode(t, tg.Body.Bytes()).(map[string]any)["id"]}
				e.tick()
				e.call("PUT", path, movie, 202, &out)
				same("PUT with a tag", out)
				var edited []map[string]any
				e.tick()
				e.call("PUT", view.base+"/api/v3/movie/editor", map[string]any{"movieIds": []int64{IDBase + 1},
					"monitored": true}, 202, &edited)
				if len(edited) != 1 {
					t.Fatalf("editor answered %d movies", len(edited))
				}
				same("editor without the field", edited[0])
			})
		}
	}
}

// A PUT with another value than the view shows is a choice for each library the view shows, and no other; reading
// it back and sending it again changes nothing more.
func TestMinimumAvailabilityPutWritesShownLibraries(t *testing.T) {
	t.Parallel()
	for _, st := range minAvailStates {
		for _, view := range minAvailViews {
			t.Run(st.name+" on "+view.base, func(t *testing.T) {
				t.Parallel()
				e := minAvailEnv(t, st.setup)
				before := e.libraryRules(1)
				path := fmt.Sprintf("%s/api/v3/movie/%d", view.base, IDBase+1)
				var movie, out map[string]any
				e.call("GET", path, nil, 200, &movie)
				value := "inCinemas"
				if movie["minimumAvailability"] == value {
					value = "released"
				}
				movie["minimumAvailability"] = value
				e.tick()
				e.call("PUT", path, movie, 202, &out)
				if out["minimumAvailability"] != value {
					t.Errorf("PUT answers %v, want %s", out["minimumAvailability"], value)
				}
				want := maps.Clone(before)
				for _, k := range view.shows {
					want[k] = availability.Rule{Minimum: value, Delay: before[k].Delay, Source: availability.SourceChoice}
				}
				if got := e.libraryRules(1); !maps.Equal(got, want) {
					t.Errorf("rules after the PUT:\n%+v\nwant\n%+v", got, want)
				}
				if n := e.one(`SELECT count(*) FROM arrview_title WHERE title_id = 1 AND view NOT LIKE 'v/%'
					AND minimum_availability = ? AND updated_at = ?`, value, ms(e.now)); n != 0 {
					t.Errorf("the PUT wrote a combined view's row")
				}
				rows := e.choiceRows(1)
				e.call("GET", path, nil, 200, &movie)
				e.tick()
				e.call("PUT", path, movie, 202, &out)
				if got := e.libraryRules(1); !maps.Equal(got, want) || !maps.Equal(e.choiceRows(1), rows) {
					t.Errorf("the echo after it changed the rules: %+v", got)
				}
			})
		}
	}
}

// A copy the view does not show (removed from the views) is not written by a PUT through the view.
func TestMinimumAvailabilityPutSkipsRemovedCopy(t *testing.T) {
	t.Parallel()
	e := minAvailEnv(t, func(e *env) {})
	e.call("DELETE", fmt.Sprintf("/v/movie-web/radarr/api/v3/movie/%d", IDBase+1), nil, 200, nil)
	path := fmt.Sprintf("/radarr/api/v3/movie/%d", IDBase+1)
	var movie map[string]any
	e.call("GET", path, nil, 200, &movie)
	movie["minimumAvailability"] = "announced"
	e.tick()
	e.call("PUT", path, movie, 202, nil)
	got := e.libraryRules(1)
	if got["movie-remux"] != (availability.Rule{Minimum: "announced", Source: availability.SourceChoice}) ||
		got["movie-web"] != (availability.Rule{Minimum: "released", Source: availability.SourceDefault}) {
		t.Errorf("rules %+v", got)
	}
}

// movie/editor is outside the echo guarantee: a supplied minimumAvailability is a deliberate edit of every library
// the view shows, even when it is the value shown; without the field nothing is written.
func TestMinimumAvailabilityEditor(t *testing.T) {
	t.Parallel()
	differ := minAvailStates[len(minAvailStates)-1]
	e := minAvailEnv(t, differ.setup)
	before := e.libraryRules(1)
	rows := e.choiceRows(1)
	var out []map[string]any
	e.tick()
	e.call("PUT", "/radarr/api/v3/movie/editor", map[string]any{"movieIds": []int64{IDBase + 1},
		"rootFolderPath": "/media/movies"}, 202, &out)
	if got := e.libraryRules(1); !maps.Equal(got, before) || !maps.Equal(e.choiceRows(1), rows) {
		t.Fatalf("editor without the field changed the rules: %+v", got)
	}
	if out[0]["minimumAvailability"] != "announced" {
		t.Fatalf("combined view shows %v", out[0]["minimumAvailability"])
	}
	// The value shown, supplied: both libraries take it as their choice.
	e.tick()
	e.call("PUT", "/radarr/api/v3/movie/editor", map[string]any{"movieIds": []int64{IDBase + 1},
		"minimumAvailability": "announced"}, 202, &out)
	choice := availability.Rule{Minimum: "announced", Source: availability.SourceChoice}
	if got := e.libraryRules(1); got["movie-remux"] != choice || got["movie-web"] != choice {
		t.Errorf("editor with the value shown: %+v", got)
	}
	// A per-library view's editor writes its library only.
	e.tick()
	e.call("PUT", "/v/movie-web/radarr/api/v3/movie/editor", map[string]any{"movieIds": []int64{IDBase + 1},
		"minimumAvailability": "released"}, 202, &out)
	got := e.libraryRules(1)
	if got["movie-remux"] != choice || got["movie-web"] != (availability.Rule{Minimum: "released", Source: availability.SourceChoice}) {
		t.Errorf("per-library editor: %+v", got)
	}
	if out[0]["minimumAvailability"] != "released" {
		t.Errorf("per-library editor answers %v", out[0]["minimumAvailability"])
	}
}

// A client add is decided per library it turns on: a library with its own minimum_availability uses it and the
// client's value is not stored for it; one without stores the client's value as its choice. A copy already on keeps
// its rule, and nothing is stored on the combined view's row.
func TestMinimumAvailabilityClientAdd(t *testing.T) {
	t.Parallel()
	add := func(e *env, tmdb int, minimum string) int64 {
		var m map[string]any
		e.tick()
		e.call("POST", "/radarr/api/v3/movie", map[string]any{"title": "Epsilon", "year": 2024, "tmdbId": tmdb,
			"qualityProfileId": e.profileID("radarr", "Rules default"), "rootFolderPath": "/media/movies",
			"minimumAvailability": minimum, "monitored": true,
			"addOptions": map[string]any{"searchForMovie": false}}, 201, &m)
		return int64(m["id"].(float64)) - IDBase
	}
	noCombined := func(e *env, tid int64) {
		t.Helper()
		if n := e.one(`SELECT count(*) FROM arrview_title WHERE title_id = ? AND view = 'radarr'
			AND minimum_availability <> ''`, tid); n != 0 {
			t.Errorf("the add stored a minimum availability on the combined view's row")
		}
	}
	choice := func(v string) availability.Rule {
		return availability.Rule{Minimum: v, Source: availability.SourceChoice}
	}
	t.Run("no library default", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		tid := add(e, 105, "announced")
		if got := e.libraryRules(tid); got["movie-remux"] != choice("announced") || got["movie-web"] != choice("announced") {
			t.Errorf("rules %+v", got)
		}
		noCombined(e, tid)
		// The same add again turns nothing on, so its value is not stored.
		add(e, 105, "inCinemas")
		if got := e.libraryRules(tid); got["movie-remux"] != choice("announced") || got["movie-web"] != choice("announced") {
			t.Errorf("rules after a second add %+v", got)
		}
		noCombined(e, tid)
	})
	t.Run("every library has a default", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		e.pol.Versions["movie-remux"].MinimumAvailability = "inCinemas"
		e.pol.Versions["movie-web"].MinimumAvailability = "announced"
		e.start()
		tid := add(e, 105, "released")
		got := e.libraryRules(tid)
		if got["movie-remux"] != (availability.Rule{Minimum: "inCinemas", Source: availability.SourceDefault}) ||
			got["movie-web"] != (availability.Rule{Minimum: "announced", Source: availability.SourceDefault}) {
			t.Errorf("rules %+v", got)
		}
		if n := e.one(`SELECT count(*) FROM arrview_title WHERE title_id = ? AND minimum_availability <> ''`, tid); n != 0 {
			t.Errorf("%d stored values", n)
		}
		var m map[string]any
		e.call("GET", fmt.Sprintf("/radarr/api/v3/movie/%d", IDBase+tid), nil, 200, &m)
		if m["minimumAvailability"] != "announced" {
			t.Errorf("view shows %v", m["minimumAvailability"])
		}
	})
	t.Run("mixed libraries", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		e.pol.Versions["movie-remux"].MinimumAvailability = "announced"
		e.start()
		tid := add(e, 105, "inCinemas")
		got := e.libraryRules(tid)
		if got["movie-remux"] != (availability.Rule{Minimum: "announced", Source: availability.SourceDefault}) ||
			got["movie-web"] != choice("inCinemas") {
			t.Errorf("rules %+v", got)
		}
		noCombined(e, tid)
	})
	t.Run("a held movie with one copy off", func(t *testing.T) {
		t.Parallel()
		e := minAvailEnv(t, func(e *env) {
			e.exec(`UPDATE arr_title SET minimum_availability = 'inCinemas' WHERE version_id = 1`)
			e.exec(`UPDATE version SET monitored = 0 WHERE id = 2`)
		})
		if tid := add(e, 101, "announced"); tid != 1 {
			t.Fatalf("added title %d", tid)
		}
		got := e.libraryRules(1)
		if got["movie-remux"] != (availability.Rule{Minimum: "inCinemas", Source: availability.SourceImported}) ||
			got["movie-web"] != choice("announced") {
			t.Errorf("rules %+v", got)
		}
		noCombined(e, 1)
	})
}

// isAvailable on a view is true when any library it shows is available under its own minimum and delay.
func TestViewIsAvailableWithDelays(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// Alpha came out at home two days ago.
	e.exec(`UPDATE title_release_date SET date = ? WHERE title_id = 1 AND type = 'digital'`,
		testNow.AddDate(0, 0, -2).Format(time.DateOnly))
	available := func(base string) any {
		t.Helper()
		var m map[string]any
		e.call("GET", fmt.Sprintf("%s/api/v3/movie/%d", base, IDBase+1), nil, 200, &m)
		return m["isAvailable"]
	}
	check := func(what string, remux, web, combined bool) {
		t.Helper()
		e.start()
		if a, b, c := available("/v/movie-remux/radarr"), available("/v/movie-web/radarr"), available("/radarr"); a != remux || b != web || c != combined {
			t.Errorf("%s: remux %v web %v combined %v, want %v %v %v", what, a, b, c, remux, web, combined)
		}
	}
	check("no delay", true, true, true)
	e.pol.Versions["movie-remux"].AvailabilityDelay = 5
	check("remux waits 5 days", false, true, true)
	e.pol.Versions["movie-web"].AvailabilityDelay = 3
	check("both wait", false, false, false)
	e.pol.Versions["movie-web"].AvailabilityDelay = 2
	check("web waits 2 days", false, true, true)
	e.exec(`UPDATE title_release_date SET date = ? WHERE title_id = 1 AND type = 'digital'`,
		testNow.AddDate(0, 0, 3).Format(time.DateOnly))
	check("not out", false, false, false)
	e.pol.Versions["movie-remux"].AvailabilityDelay = -3
	check("remux 3 days early", true, false, true)
}

// A DELETE through a view clears the minimum availability choice of the libraries it removes the copy from, and no
// other: an add afterwards takes the library's default again instead of the choice left behind.
func TestMinimumAvailabilityDeleteClearsChoice(t *testing.T) {
	t.Parallel()
	e := minAvailEnv(t, func(e *env) {
		e.exec(`INSERT INTO arrview_title (view, title_id, quality_profile_id, root_folder_path, minimum_availability,
			added_at, updated_at) VALUES ('v/movie-web', 1, 0, '/media/movies-web', 'announced', 7, 5),
			('v/movie-remux', 1, 0, '', 'inCinemas', 0, 5)`)
		e.pol.Versions["movie-web"].MinimumAvailability = "inCinemas"
		// No arr owns the remux copy any more, so it can be removed.
		e.exec(`DELETE FROM version_source WHERE version_id = 1`)
	})
	// Through the remux library's own view: its choice goes, the other library's stays.
	e.call("DELETE", fmt.Sprintf("/v/movie-remux/radarr/api/v3/movie/%d", IDBase+1), nil, 200, nil)
	got := e.libraryRules(1)
	if got["movie-remux"] != (availability.Rule{Minimum: "released", Source: availability.SourceDefault}) ||
		got["movie-web"] != (availability.Rule{Minimum: "announced", Source: availability.SourceChoice}) {
		t.Fatalf("after the remux delete: %+v", got)
	}
	// Through the combined view: the web library's choice goes, its row's other fields stay.
	e.call("DELETE", fmt.Sprintf("/radarr/api/v3/movie/%d", IDBase+1), nil, 200, nil)
	if n := e.one(`SELECT count(*) FROM arrview_title WHERE view = 'v/movie-web' AND title_id = 1
		AND minimum_availability = '' AND root_folder_path = '/media/movies-web' AND added_at = 7`); n != 1 {
		t.Errorf("the web library's row was not kept with its choice cleared")
	}
	// Added again: the library with a default uses it, the other stores the client's value.
	e.tick()
	e.call("POST", "/radarr/api/v3/movie", map[string]any{"title": "Alpha", "tmdbId": 101,
		"qualityProfileId": e.profileID("radarr", "Rules default"), "rootFolderPath": "/media/movies",
		"minimumAvailability": "announced", "monitored": true}, 201, nil)
	got = e.libraryRules(1)
	if got["movie-remux"] != (availability.Rule{Minimum: "announced", Source: availability.SourceChoice}) ||
		got["movie-web"] != (availability.Rule{Minimum: "inCinemas", Source: availability.SourceDefault}) {
		t.Errorf("after the add: %+v", got)
	}
}

// A library the add turns on through the anime check asked again (after a TMDb outage at the first add) takes its
// minimum availability as any library the add turns on: its own default, else the client's value.
func TestMinimumAvailabilityClientAddAnimeRetry(t *testing.T) {
	for _, def := range []string{"", "inCinemas"} {
		t.Run("library default "+def, func(t *testing.T) {
			e := newEnv(t)
			e.pol.Versions["anime-movie"].MinimumAvailability = def
			f := &fakeTMDb{fail: true, movies: map[int]string{
				557: `{"id":557,"title":"Anime","original_language":"ja","genres":[{"id":16,"name":"Animation"}]}`,
			}}
			e.withTMDb(f)
			body := map[string]any{"title": "Anime", "tmdbId": 557, "qualityProfileId": e.profileID("radarr", "Rules default"),
				"rootFolderPath": "/media/movies", "monitored": true, "minimumAvailability": "announced"}
			var got map[string]any
			e.call("POST", "/radarr/api/v3/movie", body, 201, &got)
			tid := int64(got["id"].(float64)) - IDBase
			if e.versionsOf(tid)["anime-movie"] {
				t.Fatal("fixture: anime version created without an answer")
			}
			f.mu.Lock()
			f.fail = false
			f.mu.Unlock()
			e.tick()
			e.call("POST", "/radarr/api/v3/movie", body, 201, &got)
			want := availability.Rule{Minimum: "announced", Source: availability.SourceChoice}
			if def != "" {
				want = availability.Rule{Minimum: def, Source: availability.SourceDefault}
			}
			rules := e.libraryRules(tid)
			if rule, ok := rules["anime-movie"]; !ok || rule != want {
				t.Errorf("anime library: %+v, want %+v (all %+v)", rule, want, rules)
			}
		})
	}
}
