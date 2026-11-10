package golive

import (
	"bytes"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

func TestHomepageCutover(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, version, instance, flavour string
		mode                             policy.Mode
		router, done, noOwner            bool
		status                           Status
	}{
		{"shadow", "movie-web", "radarr_4k_web", "radarr", policy.ModeShadow, true, false, false, Info},
		{"active", "movie-web", "radarr_4k_web", "radarr", policy.ModeActive, true, false, false, Warn},
		{"router off", "movie-web", "radarr_4k_web", "radarr", policy.ModeActive, false, false, false, Info},
		{"acknowledged", "movie-web", "radarr_4k_web", "radarr", policy.ModeActive, true, true, false, Pass},
		{"series", "tv-2160p", "sonarr_4k_debrid", "sonarr", policy.ModeActive, true, false, false, Warn},
		{"owner removed", "movie-web", "the arr for movie-web", "radarr", policy.ModeActive, true, false, true, Warn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ack := "[]"
			if tc.done {
				ack = "[homepage]"
			}
			p := examplePolicy(t, [3]string{"versions.yaml", "    profile: web-2160p\n",
				"    profile: web-2160p\n    golive_done: " + ack + "\n"})
			p.Versions[tc.version].Mode = tc.mode
			p.Router.Enabled = tc.router
			// Neither a shadow view nor a mixed view should be the widget's cutover target.
			p.Views["aaa-shadow"] = &policy.View{Path: "/shadow/radarr", Versions: []string{"movie-remux"}}
			if tc.noOwner {
				p.Import = nil
				p.Views = nil
			}
			r := run(t, Options{Policy: p, Version: tc.version, PublicURL: "http://theatarr:7700/"})
			want(t, r, SecHandover, "client Homepage", tc.status, "Homepage")
			lines := find(r, SecHandover, "client Homepage")
			if len(lines) != 1 {
				t.Fatalf("Homepage lines = %+v", lines)
			}
			fix := "at cutover, point the Homepage widget for " + tc.instance + " at http://theatarr:7700/v/" +
				tc.version + "/" + tc.flavour + " with a view key"
			if tc.done {
				fix = ""
				if !strings.Contains(lines[0].Message, "versions.movie-web.golive_done") {
					t.Errorf("acknowledgement = %q", lines[0].Message)
				}
				// Completing one version must not acknowledge any other version's tiles.
				p.Versions["movie-remux"].Mode = policy.ModeActive
				want(t, run(t, Options{Policy: p, Version: "movie-remux"}), SecHandover, "client Homepage", Warn, "Homepage")
			}
			if lines[0].Fix != fix {
				t.Errorf("fix = %q, want %q", lines[0].Fix, fix)
			}
			var out bytes.Buffer
			Print(&out, r)
			if !strings.Contains(out.String(), string(tc.status)+"  handover  client Homepage") ||
				(fix != "" && !strings.Contains(out.String(), "fix: "+fix)) {
				t.Errorf("Homepage check missing from printed report:\n%s", out.String())
			}
		})
	}
}

func TestHomepageFollowerCutover(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"movie-remux", "movie-remux-theatarr"} {
		t.Run(version, func(t *testing.T) {
			o := parallelExample(t)
			o.Version, o.PublicURL = version, "http://theatarr:7700"
			o.Policy.Router.Enabled = true
			r := run(t, o)
			status := Info
			if version == "movie-remux-theatarr" {
				status = Warn
			}
			want(t, r, SecHandover, "client Homepage", status, "radarr_4k_debrid")
			lines := find(r, SecHandover, "client Homepage")
			fix := "at cutover, point the Homepage widget for radarr_4k_debrid at http://theatarr:7700/v/movie-remux-theatarr/radarr with a view key"
			if len(lines) != 1 || lines[0].Fix != fix {
				t.Fatalf("Homepage lines = %+v, want fix %q", lines, fix)
			}
			o.Policy.Versions[version].GoliveDone = []string{"homepage"}
			want(t, run(t, o), SecHandover, "client Homepage", Pass, "acknowledged")
		})
	}
}

// A push needs an API key and is decided like a search result (privacy is a label only): the check only says so.
func TestReleasePushCutover(t *testing.T) {
	t.Parallel()
	for _, mode := range []policy.Mode{policy.ModeShadow, policy.ModeActive} {
		p := examplePolicy(t)
		v := p.Versions["movie-web"]
		v.Mode = mode
		p.Router.Enabled = true
		r := run(t, Options{Policy: p, Version: "movie-web"})
		want(t, r, SecPolicy, "release push", Info, "matched by name")
		want(t, r, SecHandover, "client autobrr", Info, "its indexer must be a configured one this version lists")
	}
}

// A private-labelled indexer in a debrid version is like any other: privacy is a label, so no policy line names it.
func TestPrivateIndexerInDebridVersion(t *testing.T) {
	t.Parallel()
	p := examplePolicy(t)
	v := p.Versions["movie-web"]
	v.Indexers = append(v.Indexers, "tracker-example")
	p.Indexers["tracker-example"].Providers = []string{"tb"}
	r := run(t, Options{Policy: p, Version: "movie-web"})
	for _, c := range r.Results {
		if c.Section == SecPolicy && (strings.Contains(c.Message, "tracker-example") || strings.Contains(c.Check, "private")) {
			t.Errorf("a check names the private-labelled indexer: %+v", c)
		}
	}
}

// A view whose key the settings database holds has a derived env name; the Seerr hint names the page with the key,
// never that name.
func TestHandoverViewKeyFromSettings(t *testing.T) {
	t.Parallel()
	p := examplePolicy(t)
	p.Views["radarr"].APIKeyEnv = policy.SettingsSecretPrefix + "VIEWS_RADARR"
	r := run(t, Options{Policy: p, Version: "movie-web", PublicURL: "http://theatarr:7700"})
	lines := find(r, SecHandover, "client Seerr")
	if len(lines) == 0 {
		t.Fatal("no Seerr hint")
	}
	for _, l := range lines {
		if strings.Contains(l.Message, policy.SettingsSecretPrefix) || !strings.Contains(l.Message, "Settings › Apps") {
			t.Errorf("Seerr hint = %q", l.Message)
		}
	}
}

func TestSecretFixNamesNoHostTool(t *testing.T) {
	t.Parallel()
	p := examplePolicy(t)
	seen := false
	for _, x := range run(t, Options{Policy: p, Version: "movie-web", Env: func(string) string { return "" }}).Results {
		seen = seen || strings.Contains(x.Fix, "container's environment")
		if strings.Contains(x.Fix+x.Message, "Dockhand") {
			t.Errorf("result names Dockhand: %+v", x)
		}
	}
	if !seen {
		t.Error("no unset env secret reported")
	}
}
