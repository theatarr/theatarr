package web

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
)

type grabStateSettings struct {
	SettingsAdmin
	runtime *settings.Runtime
}

func (s grabStateSettings) Current() *settings.Runtime { return s.runtime }

func TestVersionGrabStateSettingsRuntime(t *testing.T) {
	t.Parallel()
	stored := &policy.Policy{Versions: map[string]*policy.VersionPolicy{"v": {Mode: policy.ModeActive, Grab: true}},
		Router: &policy.Router{Enabled: true}}
	running := &policy.Policy{Versions: map[string]*policy.VersionPolicy{"v": {Mode: policy.ModeShadow}},
		ForcedOff: []string{"router"}}
	w := &Web{pol: stored, settings: grabStateSettings{runtime: &settings.Runtime{Policy: running}}}
	switches := w.settingsSwitches()
	want := VersionGrabState{Mode: "shadow", RouterForcedOff: true}
	if len(switches.Versions) != 1 || switches.Versions[0].GrabState != want {
		t.Fatalf("settings must report the runtime, not the stored head: %+v", switches)
	}
}

func TestVersionGrabState(t *testing.T) {
	t.Parallel()
	for _, mode := range []policy.Mode{"off", policy.ModeShadow, policy.ModeActive} {
		for _, grab := range []bool{false, true} {
			for _, router := range []bool{false, true} {
				for _, forced := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/grab=%t/router=%t/forced=%t", mode, grab, router, forced), func(t *testing.T) {
						p := &policy.Policy{Versions: map[string]*policy.VersionPolicy{"v": {Mode: mode, Grab: grab}},
							Router: &policy.Router{Enabled: router}}
						if forced {
							p.ForcedOff = []string{"router"}
						}
						got := versionGrabState(p, "v")
						want := VersionGrabState{Mode: string(mode), Grab: grab, RouterEnabled: router, RouterForcedOff: forced, WillGrab: mode == policy.ModeActive && grab && router && !forced}
						if got != want {
							t.Fatalf("state = %+v, want %+v", got, want)
						}
					})
				}
			}
		}
	}
	for _, p := range []*policy.Policy{nil, {}, {Versions: map[string]*policy.VersionPolicy{"other": {Mode: policy.ModeActive, Grab: true}}}} {
		if got := versionGrabState(p, "missing"); got != (VersionGrabState{Mode: "off"}) {
			t.Fatalf("missing policy/version: %+v", got)
		}
	}
	p := &policy.Policy{Versions: map[string]*policy.VersionPolicy{"v": {}}, ForcedOff: []string{"library", "notify"}}
	if got := versionGrabState(p, "v"); got != (VersionGrabState{Mode: "shadow"}) {
		t.Fatalf("default mode, nil router, unrelated overrides: %+v", got)
	}
	// library_writes is the library's own write gate: the global switch, listed with roots, mode active, not followed.
	lib := &policy.Policy{Library: &policy.Library{Enabled: true, Versions: map[string]*policy.LibraryVersion{
		"a": {Roots: []string{"/lib/a"}}, "s": {Roots: []string{"/lib/s"}}}},
		Versions: map[string]*policy.VersionPolicy{"a": {Mode: policy.ModeActive}, "s": {Mode: policy.ModeShadow},
			"u": {Mode: policy.ModeActive}}}
	for key, want := range map[string]bool{"a": true, "s": false, "u": false} {
		if got := versionGrabState(lib, key); !got.LibraryEnabled || got.LibraryWrites != want {
			t.Errorf("%s: %+v, want library_writes %t", key, got, want)
		}
	}
	lib.Library.Enabled = false
	if got := versionGrabState(lib, "a"); got.LibraryWrites {
		t.Errorf("global switch off still writes: %+v", got)
	}
	if got := versionGrabSummaries(nil); got == nil || len(got) != 0 {
		t.Fatalf("empty summaries must marshal as an array: %+v", got)
	}
}

func TestVersionGrabStateAPI(t *testing.T) {
	t.Parallel()
	for _, forced := range []bool{false, true} {
		t.Run(fmt.Sprintf("forced=%t", forced), func(t *testing.T) {
			e := newAddEnv(t, func(p *policy.Policy) {
				p.Router.Enabled = !forced
				if forced {
					p.ForcedOff = []string{"router"}
				}
				for _, v := range p.Versions {
					v.Mode, v.Grab = policy.ModeActive, true
				}
			})
			want := VersionGrabState{Mode: "active", Grab: true, RouterEnabled: !forced, RouterForcedOff: forced, WillGrab: !forced}
			check := func(got VersionGrabState) {
				t.Helper()
				if got != want {
					t.Fatalf("grab_state = %+v, want %+v", got, want)
				}
			}
			var status Status
			e.call("GET", APIBase+"/status", nil, 200, &status)
			if len(status.Versions) != len(e.pol.Versions) {
				t.Fatalf("status versions = %+v", status.Versions)
			}
			for _, v := range status.Versions {
				check(v.GrabState)
			}
			var setup SetupState
			e.call("GET", APIBase+"/setup", nil, 200, &setup)
			if len(setup.VersionStates) != len(e.pol.Versions) {
				t.Fatalf("setup versions = %+v", setup.VersionStates)
			}
			for _, v := range setup.VersionStates {
				check(v.GrabState)
			}
			var pol PolicySummary
			e.call("GET", APIBase+"/policy", nil, 200, &pol)
			if len(pol.Versions) != len(e.pol.Versions) {
				t.Fatalf("policy versions = %+v", pol.Versions)
			}
			for _, v := range pol.Versions {
				check(v.GrabState)
			}
			var options AddOptions
			e.call("GET", APIBase+"/add/options?kind=movie&tmdb_id=101", nil, 200, &options)
			if len(options.Versions) == 0 {
				t.Fatal("add options missing versions")
			}
			for _, v := range options.Versions {
				check(v.GrabState)
			}
			var grid TitlePage
			e.call("GET", APIBase+"/titles", nil, 200, &grid)
			for _, title := range grid.Items {
				for _, v := range title.Versions {
					if v.Control == nil {
						t.Fatal("library copy missing controls")
					}
					check(v.Control.GrabState)
				}
			}
			var title TitleDetail
			e.call("GET", APIBase+"/titles/1", nil, 200, &title)
			for _, v := range title.Versions {
				if v.Control == nil {
					t.Fatal("title copy missing controls")
				}
				check(v.Control.GrabState)
			}
			for _, path := range []string{"/titles/1/releases?version=movie-remux", "/titles/10/episodes/101/releases?version=tv-2160p"} {
				var releases Releases
				e.call("GET", APIBase+path, nil, 200, &releases)
				check(releases.GrabState)
			}
			submits, searches := e.acq.counts()
			if submits != 0 || searches != 0 {
				t.Fatalf("status reads triggered acquisition: submits=%d, searches=%d", submits, searches)
			}
		})
	}
}

// An account without "see files" still gets the grab gates on the title page: its request card says "on its way"
// only when a copy is actually acquiring (web/src/lib/request-state.ts versionFetching).
func TestRedactVersionKeepsGrabState(t *testing.T) {
	t.Parallel()
	gs := VersionGrabState{Mode: "shadow", Grab: true, RouterEnabled: true}
	v := Version{Key: "tv-2160p", State: "wanted", Control: &VersionControl{GrabState: gs, Mode: "shadow", ManagedBy: "sonarr_4k", CanSearch: true}}
	redactVersion(&v)
	if v.Control == nil || v.Control.GrabState != gs {
		t.Fatalf("control after redaction = %+v, want grab_state %+v kept", v.Control, gs)
	}
	if v.Control.ManagedBy != "" || v.Control.CanSearch {
		t.Fatalf("redaction kept manager-only fields: %+v", v.Control)
	}
}

func TestVersionGrabWarnsWhenLibraryOff(t *testing.T) {
	t.Parallel()
	p := &policy.Policy{Versions: map[string]*policy.VersionPolicy{"v": {Mode: policy.ModeActive, Grab: true}}, Router: &policy.Router{Enabled: true}}
	got := versionGrabState(p, "v")
	raw, _ := json.Marshal(got)
	if !got.WillGrab || !strings.Contains(string(raw), `"library_enabled":false`) {
		t.Fatalf("must report debrid adds and the closed library switch: %s", raw)
	}
}
