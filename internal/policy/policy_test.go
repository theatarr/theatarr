package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"go.yaml.in/yaml/v3"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const exampleDir = "../../examples/policy"

// copyExample copies the example policy into a temp dir and returns it.
func copyExample(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(exampleDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(exampleDir, path)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// egressOn uncomments the VPN egress section of the example copied into dir: the example ships it commented out,
// off by default.
func egressOn(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "egress.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i := strings.Index(s, "# egress:")
	if i < 0 {
		t.Fatal("egress.yaml has no commented egress section")
	}
	on := s[:i]
	for _, line := range strings.SplitAfter(s[i:], "\n") {
		on += strings.TrimPrefix(strings.TrimPrefix(line, "#"), " ")
	}
	if err := os.WriteFile(path, []byte(on), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestExampleIsValid(t *testing.T) {
	t.Parallel()
	p, err := Load(exampleDir)
	if err != nil {
		t.Fatalf("example policy: %v", err)
	}
	for _, k := range []string{"movie-remux", "movie-web", "anime-movie", "tv-2160p", "anime-tv"} {
		v, ok := p.Versions[k]
		if !ok {
			t.Errorf("version %s missing", k)
			continue
		}
		if v.Mode != ModeShadow {
			t.Errorf("version %s: mode %q, want the shadow default", k, v.Mode)
		}
	}
	if p.Views["radarr"].Path != "/radarr" || p.Views["sonarr"].Path != "/sonarr" {
		t.Error("views /radarr and /sonarr expected")
	}
	if got := p.Versions["movie-remux"].Providers; strings.Join(got, ",") != "rd,ad,tb" {
		t.Errorf("movie-remux providers %v", got)
	}
	g := p.Profiles["remux-2160p"].Qualities[1]
	if !g.Group || g.Name != "WEB 2160p" || len(g.Qualities) != 2 {
		t.Errorf("quality group decoded as %+v", g)
	}
	for k, want := range map[string]string{"movie-remux": PacksAll, "tv-2160p": PacksAnyUpgradable, "anime-tv": PacksAnyUpgradable} {
		if got := p.Versions[k].Packs; got != want {
			t.Errorf("version %s packs %q, want %q", k, got, want)
		}
	}
	for k, want := range map[string]string{"movie-remux": "", "tv-2160p": SonarrV4Plus, "anime-tv": SonarrV4Plus} {
		if got := p.Versions[k].Sonarr; got != want {
			t.Errorf("version %s sonarr %q, want %q", k, got, want)
		}
	}
	for k, want := range map[string]string{"movie-remux": UpgradesApprove, "movie-web": UpgradesAuto, "tv-2160p": UpgradesAuto} {
		if got := p.Versions[k].Upgrades; got != want {
			t.Errorf("version %s upgrades %q, want %q", k, got, want)
		}
	}
	if p.Budgets["dmm-key"].Per.D().Seconds() != 60 {
		t.Errorf("budget duration %v", p.Budgets["dmm-key"].Per.D())
	}
	hosts := p.OutboundHosts()
	if len(hosts) == 0 {
		t.Fatal("no outbound hosts")
	}
	for _, h := range hosts {
		if strings.Contains(h.Host, "@") {
			t.Errorf("host with credentials: %s", h.Host)
		}
	}
	if a := p.Import.Arr["radarr_4k_debrid"]; a == nil || a.Version != "movie-remux" || a.Flavour != "radarr" {
		t.Errorf("import.arr[radarr_4k_debrid] decoded as %+v", a)
	}
	if users := p.SecretEnvs()["THEATARR_ARR_RADARR_4K_WEB_KEY"]; len(users) != 1 {
		t.Errorf("THEATARR_ARR_RADARR_4K_WEB_KEY users %v", users)
	}
	if users := p.SecretEnvs()["THEATARR_RD_KEY"]; len(users) != 1 {
		t.Errorf("THEATARR_RD_KEY users %v", users)
	}
}

// Each case edits one file of a copy of the example and expects these error lines (path-prefixed).
func TestBadPolicies(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		file string
		old  string // replaced by new; empty old means "append new to the file", and a missing file is created
		new  string
		want []string
	}{
		{
			name: "unknown profile",
			file: "versions.yaml", old: "profile: web-2160p", new: "profile: x",
			want: []string{`versions[movie-web].profile: unknown profile "x"`},
		},
		{
			name: "unknown upgrade rule",
			file: "versions.yaml", old: "upgrades: approve", new: "upgrades: ask",
			want: []string{`versions[movie-remux].upgrades: must be auto, approve or off, got "ask"`},
		},
		{
			name: "unknown provider preference",
			file: "versions.yaml", old: "provider_preference: ordered", new: "provider_preference: first",
			want: []string{`versions[movie-web].provider_preference: must be ordered or none, got "first"`},
		},
		{
			name: "unknown minimum availability",
			file: "versions.yaml", old: "provider_preference: ordered", new: "provider_preference: ordered\n    minimum_availability: tba",
			want: []string{`versions[movie-web].minimum_availability: must be announced, inCinemas or released, got "tba"`},
		},
		{
			name: "availability settings on a series library",
			file: "versions.yaml", old: "plain Sonarr: every episode's file must be upgraded.\n    packs: any_upgradable", new: "plain Sonarr: every episode's file must be upgraded.\n    packs: any_upgradable\n    minimum_availability: released\n    availability_delay: -2",
			want: []string{
				`versions[tv-2160p].availability_delay: is for movie libraries only`,
				`versions[tv-2160p].minimum_availability: is for movie libraries only`,
			},
		},
		{
			name: "unknown pack rule",
			file: "versions.yaml", old: "plain Sonarr: every episode's file must be upgraded.\n    packs: any_upgradable", new: "plain Sonarr: every episode's file must be upgraded.\n    packs: some",
			want: []string{`versions[tv-2160p].packs: must be all, any_upgradable or threshold, got "some"`},
		},
		{
			name: "threshold without a percentage",
			file: "versions.yaml", old: "plain Sonarr: every episode's file must be upgraded.\n    packs: any_upgradable", new: "plain Sonarr: every episode's file must be upgraded.\n    packs: threshold",
			want: []string{`versions[tv-2160p].packs_threshold: required with packs: threshold (a percentage, 1 to 100)`},
		},
		{
			name: "threshold out of range",
			file: "versions.yaml", old: "plain Sonarr: every episode's file must be upgraded.\n    packs: any_upgradable", new: "plain Sonarr: every episode's file must be upgraded.\n    packs: threshold\n    packs_threshold: 0",
			want: []string{`versions[tv-2160p].packs_threshold: must be a percentage from 1 to 100, got 0`},
		},
		{
			name: "threshold above 100",
			file: "versions.yaml", old: "plain Sonarr: every episode's file must be upgraded.\n    packs: any_upgradable", new: "plain Sonarr: every episode's file must be upgraded.\n    packs: threshold\n    packs_threshold: 100.5",
			want: []string{`versions[tv-2160p].packs_threshold: must be a percentage from 1 to 100, got 100.5`},
		},
		{
			name: "percentage without threshold",
			file: "versions.yaml", old: "plain Sonarr: every episode's file must be upgraded.\n    packs: any_upgradable", new: "plain Sonarr: every episode's file must be upgraded.\n    packs: any_upgradable\n    packs_threshold: 80",
			want: []string{`versions[tv-2160p].packs_threshold: only taken with packs: threshold`},
		},
		{
			name: "threshold on a movie version",
			file: "versions.yaml", old: "    profile: web-2160p\n", new: "    profile: web-2160p\n    packs: threshold\n    packs_threshold: 80\n",
			want: []string{`versions[movie-web].packs: only series libraries take season packs`},
		},
		{
			name: "pack rule on a movie version",
			file: "versions.yaml", old: "    profile: web-2160p\n", new: "    profile: web-2160p\n    packs: any_upgradable\n",
			want: []string{`versions[movie-web].packs: only series libraries take season packs`},
		},
		{
			name: "unknown Sonarr version",
			file: "versions.yaml", old: "    sonarr: v4plus\n", new: "    sonarr: v6\n",
			want: []string{`versions[tv-2160p].sonarr: must be v4, v4plus or v5, got "v6"`},
		},
		{
			name: "Sonarr version on a movie version",
			file: "versions.yaml", old: "    profile: web-2160p\n", new: "    profile: web-2160p\n    sonarr: v5\n",
			want: []string{`versions[movie-web].sonarr: only series libraries take a Sonarr version`},
		},
		{
			name: "unknown field",
			file: "versions.yaml", old: "    import: symlink\n", new: "    import: symlink\n    colour: blue\n",
			want: []string{"versions.yaml", "field colour not found"},
		},
		{
			name: "inline secret",
			file: "providers.yaml", old: "api_key_env: THEATARR_RD_KEY", new: "api_key: abc123",
			want: []string{"field api_key not found", "never inline"},
		},
		{
			name: "secret env not a name",
			file: "providers.yaml", old: "api_key_env: THEATARR_RD_KEY", new: "api_key_env: sk-123abc",
			want: []string{`providers[rd].api_key_env: "sk-123abc" is not an environment variable name`},
		},
		{
			name: "two documents",
			file: "budgets.yaml", old: "", new: "---\nbudgets: {}\n",
			want: []string{"budgets.yaml: more than one YAML document"},
		},
		{
			name: "duplicate key across files",
			file: "extra.yaml", new: "budgets:\n  dmm-key: {requests: 1, per: 1m}\n",
			want: []string{"budgets[dmm-key]: defined in both budgets.yaml and extra.yaml"},
		},
		{
			name: "seedbox storage but a debrid provider",
			file: "versions.yaml",
			old:  "    storage: debrid\n    indexers: [prowlarr-public, seadex, dmm]\n    providers: [rd, ad, tb]\n    client: decypharr\n\n  tv-2160p:",
			new:  "    storage: seedbox\n    indexers: [tracker-example]\n    providers: [rd]\n    client: seedbox\n\n  tv-2160p:",
			want: []string{
				"versions[anime-movie].providers: only for debrid storage",
			},
		},
		{
			name: "bad privacy",
			file: "indexers.yaml", old: "privacy: private", new: "privacy: secret",
			want: []string{`indexers[tracker-example].privacy: must be public, private or semi-private, got "secret"`},
		},
		{
			name: "bad mode",
			file: "versions.yaml", old: "    mode: shadow\n    kind: series\n    anime: false", new: "    mode: live\n    kind: series\n    anime: false",
			want: []string{`versions[tv-2160p].mode: must be shadow or active, got "live"`},
		},
		{
			name: "series view with a movie version",
			file: "views.yaml", old: "versions: [tv-2160p, anime-tv]", new: "versions: [tv-2160p, anime-tv, movie-web]",
			want: []string{`views[sonarr].versions[2]: library "movie-web" is a movie library; a sonarr view serves series`},
		},
		{
			name: "view profile outside the view",
			file: "views.yaml", old: "WEB only: [movie-web]", new: "WEB only: [tv-2160p]",
			want: []string{`views[radarr].profiles[WEB only]: library "tv-2160p" is not served by this view`},
		},
		{
			name: "duplicate view path",
			file: "views.yaml", old: "path: /sonarr", new: "path: /radarr",
			want: []string{`views[sonarr].path: "/radarr" is also used by views[radarr]`},
		},
		{
			name: "unknown format in profile",
			file: "profiles.yaml", old: "HDR10+ Boost: 100\n      DV", new: "HDR10+ Bost: 100\n      DV",
			want: []string{`profiles[remux-2160p].formats[HDR10+ Bost]: unknown format "HDR10+ Bost"; declare it under formats`},
		},
		{
			name: "from_arr with other profile fields",
			file: "extra.yaml", new: "profiles:\n  shadow:\n    from_arr: radarr_4k_debrid/Remux + WEB 2160p\n    cutoff: Remux-2160p\n    min_score: 5\n",
			want: []string{"profiles[shadow]: from_arr takes the whole profile from the arr; remove cutoff, min_score"},
		},
		{
			name: "from_arr unknown instance and bad reference",
			file: "extra.yaml", new: "profiles:\n  a:\n    from_arr: radarr_x/Any\n  b:\n    from_arr: radarr_4k_debrid\n",
			want: []string{
				`profiles[a].from_arr: unknown arr instance "radarr_x"; declare it under import.arr`,
				`profiles[b].from_arr: "radarr_4k_debrid" must be <instance>/<profile name>`,
			},
		},
		{
			name: "from_arr profile on a version of the other kind",
			file: "profiles.yaml", old: "  tv-2160p:\n    qualities:\n      - Bluray-2160p Remux\n      - group: WEB 2160p\n        qualities: [WEBDL-2160p, WEBRip-2160p]\n    cutoff: Bluray-2160p Remux\n    min_score: 0\n    cutoff_score: 10000\n    formats:\n      WEB Tier 01 (tv): 1700\n      WEB Tier 02 (tv): 1650\n      BR-DISK (tv): -10000\n      LQ (tv): -10000\n",
			new:  "  tv-2160p:\n    from_arr: radarr_4k_debrid/Remux + WEB 2160p\n",
			want: []string{`versions[tv-2160p].profile: profile comes from radarr instance "radarr_4k_debrid", but the library is a series`},
		},
		{
			name: "bad propers and multi languages",
			file: "extra.yaml", new: "profiles:\n  shadow:\n    from_arr: radarr_4k_debrid/Remux + WEB 2160p\n    propers: always\n" +
				"indexers:\n  multi:\n    kind: torznab\n    url: http://prowlarr:9696/1/api\n    privacy: public\n    multi_languages: [French, Klingon, Any, French]\n",
			want: []string{
				`profiles[shadow].propers: must be prefer_and_upgrade, do_not_upgrade or do_not_prefer, got "always"`,
				`indexers[multi].multi_languages[1]: unknown language "Klingon"`,
				`indexers[multi].multi_languages[2]: "Any" is not a language a release can hold`,
				`indexers[multi].multi_languages[3]: "French" listed twice`,
			},
		},
		{
			name: "language on a series profile",
			file: "profiles.yaml", old: "  tv-2160p:\n    qualities:", new: "  tv-2160p:\n    language: original\n    qualities:",
			want: []string{`versions[tv-2160p].profile.language: a series library uses this profile, and Sonarr v4 profiles have no language; remove it`},
		},
		{
			name: "unknown profile language and language with from_arr",
			file: "extra.yaml", new: "profiles:\n  shadow:\n    from_arr: radarr_4k_debrid/Remux + WEB 2160p\n    language: original\n" +
				"  klingon:\n    language: Klingon\n    qualities: [Remux-2160p]\n",
			want: []string{
				`profiles[klingon].language: unknown language "Klingon" (any, original, or the arr's name, e.g. English, French)`,
				`profiles[shadow]: from_arr takes the whole profile from the arr; remove language`,
			},
		},
		{
			name: "cutoff outside the profile",
			file: "profiles.yaml", old: "cutoff: Remux-2160p", new: "cutoff: Bluray-1080p",
			want: []string{`profiles[remux-2160p].cutoff: "Bluray-1080p" is not one of the profile's qualities`},
		},
		{
			name: "unknown quality",
			file: "profiles.yaml", old: "      - Remux-2160p\n      - group: WEB 2160p", new: "      - Remux-4320p\n      - group: WEB 2160p",
			want: []string{`profiles[remux-2160p].qualities[0]: unknown quality "Remux-4320p"`},
		},
		{
			name: "sonarr quality in a movie profile",
			file: "versions.yaml", old: "profile: web-2160p", new: "profile: tv-2160p",
			want: []string{
				`versions[movie-web].profile: profile quality "Bluray-2160p Remux" is not a movie quality`,
				`versions[movie-web].profile: profile format "WEB Tier 01 (tv)" is a sonarr format, but the library is a movie`,
			},
		},
		{
			name: "providers out of order",
			file: "versions.yaml", old: "providers: [rd, ad, tb]\n    client: decypharr\n    import: symlink", new: "providers: [tb, rd]\n    client: decypharr\n    import: symlink",
			want: []string{`versions[movie-remux].providers[1]: "rd" listed out of fallback order`},
		},
		{
			name: "duplicate provider order",
			file: "providers.yaml", old: "order: 3", new: "order: 2",
			want: []string{"providers[tb].order: order 2 is also used by ad"},
		},
		{
			name: "debrid version with hardlinks",
			file: "versions.yaml", old: "import: symlink", new: "import: hardlink",
			want: []string{`versions[movie-remux].import: debrid libraries symlink into the mount, got "hardlink"`},
		},
		{
			name: "client storage mismatch",
			file: "versions.yaml", old: "    providers: [rd, ad, tb]\n    client: decypharr\n\n  anime-tv:", new: "    providers: [rd, ad, tb]\n    client: seedbox\n\n  anime-tv:",
			want: []string{`versions[tv-2160p].client: client "seedbox" (qbittorrent) does not serve debrid storage`},
		},
		{
			name: "missing URL",
			file: "indexers.yaml", old: "    url: https://torrentio.strem.fun\n", new: "",
			want: []string{"indexers[torrentio].url: required: every outbound URL is configured"},
		},
		{
			name: "secret in URL",
			file: "indexers.yaml", old: "url: http://zilean:8181", new: "url: http://zilean:8181/?apikey=abc",
			want: []string{"indexers[zilean].url: secrets in the URL are not allowed"},
		},
		{
			name: "bad trash id",
			file: "formats.yaml", old: "3a3ff47579026e76d6504ebea39390de", new: "not-a-trash-id",
			want: []string{`formats[Remux Tier 01].trash_id: "not-a-trash-id" is not a TRaSH id`},
		},
		{
			name: "format file missing",
			file: "formats.yaml", old: "formats/seadex.json", new: "formats/nope.json",
			want: []string{"formats[Seadex].file:"},
		},
		{
			name: "bad budget",
			file: "budgets.yaml", old: "dmm-key:     {requests: 16, per: 1m}", new: "dmm-key:     {requests: 0}",
			want: []string{"budgets[dmm-key].per: required", "budgets[dmm-key].requests: must be greater than 0"},
		},
		{
			name: "bad duration",
			file: "budgets.yaml", old: "per: 1s", new: "per: soon",
			want: []string{"budgets.yaml", "invalid duration"},
		},
		{
			name: "unknown budget",
			file: "indexers.yaml", old: "budget: torrentio", new: "budget: nope",
			want: []string{`indexers[torrentio].budget: unknown budget "nope"`},
		},
		{
			name: "relative library",
			file: "versions.yaml", old: "library: /media/anime\n", new: "library: anime\n",
			want: []string{`versions[anime-tv].library: "anime" must be an absolute path`},
		},
		{
			name: "decypharr without callback view",
			file: "clients.yaml", old: "    callback_view: radarr\n", new: "",
			want: []string{"clients[decypharr].callback_view: required for decypharr"},
		},
		{
			name: "overrides dir missing",
			file: "metadata.yaml", old: "overrides_dir: overrides", new: "overrides_dir: nowhere",
			want: []string{"metadata.overrides_dir:"},
		},
		{
			name: "import version of the wrong kind",
			file: "import.yaml", old: "version: movie-web", new: "version: tv-2160p",
			want: []string{`import.arr[radarr_4k_web].version: library "tv-2160p" is a series library; a radarr instance holds movies`},
		},
		{
			name: "import unknown version and flavour",
			file: "import.yaml", old: "flavour: radarr\n      url: http://192.168.1.10:7878", new: "flavour: lidarr\n      url: http://192.168.1.10:7878",
			want: []string{`import.arr[radarr_4k_debrid].flavour: must be radarr or sonarr, got "lidarr"`},
		},
		{
			name: "import without key env",
			file: "import.yaml", old: "      api_key_env: THEATARR_ARR_RADARR_4K_WEB_KEY\n", new: "",
			want: []string{"import.arr[radarr_4k_web].api_key_env: required"},
		},
		{
			name: "import key in URL",
			file: "import.yaml", old: "url: http://192.168.1.10:7879", new: "url: http://192.168.1.10:7879/?apikey=abc",
			want: []string{"import.arr[radarr_4k_web].url: secrets in the URL are not allowed"},
		},
		{
			name: "import host off the LAN",
			file: "import.yaml", old: "url: http://192.168.1.10:7879", new: "url: http://203.0.113.9:7881",
			want: []string{"import.arr[radarr_4k_web].url: host 203.0.113.9: arr: not a LAN address"},
		},
		{
			name: "import host public IPv6",
			file: "import.yaml", old: "url: http://192.168.1.10:7879", new: "url: http://[2001:db8::1]:7881",
			want: []string{"import.arr[radarr_4k_web].url: host 2001:db8::1: arr: not a LAN address"},
		},
		{
			name: "import missing version",
			file: "import.yaml", old: "version: movie-remux", new: "version: movie-nope",
			want: []string{`import.arr[radarr_4k_debrid].version: unknown library "movie-nope"`},
		},
		{
			name: "seedbox version without a client",
			file: "versions.yaml",
			old:  "    storage: debrid\n    indexers: [prowlarr-public, seadex, dmm]\n    providers: [rd, ad, tb]\n    client: decypharr\n\n  tv-2160p:",
			new:  "    storage: seedbox\n    indexers: [tracker-example]\n\n  tv-2160p:",
			want: []string{"versions[anime-movie].client: required"},
		},
		{
			name: "version without indexers",
			file: "versions.yaml", old: "indexers: [prowlarr-public, seadex, dmm]\n    providers: [rd, ad, tb]\n    client: decypharr\n\n  tv-2160p:", new: "providers: [rd, ad, tb]\n    client: decypharr\n\n  tv-2160p:",
			want: []string{"versions[anime-movie].indexers: required"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := copyExample(t)
			path := filepath.Join(dir, c.file)
			b, err := os.ReadFile(path)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			s := string(b)
			switch {
			case c.old == "":
				s += c.new
			case !strings.Contains(s, c.old):
				t.Fatalf("test setup: %q not found in %s", c.old, c.file)
			default:
				s = strings.Replace(s, c.old, c.new, 1)
			}
			if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err = Load(dir)
			if err == nil {
				t.Fatal("bad policy accepted")
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("missing %q in:\n%v", w, err)
				}
			}
		})
	}
}

func TestEmptyDir(t *testing.T) {
	t.Parallel()
	if _, err := Load(t.TempDir()); err == nil || !strings.Contains(err.Error(), "no .yaml files") {
		t.Errorf("got %v", err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("missing dir accepted")
	}
}

// A fresh install: a missing or empty policy directory reads as an empty policy, which validates (no versions is
// allowed; each missing section turns its feature off). Read and Load keep refusing an empty directory, and a bad
// file still fails.
func TestReadOrEmpty(t *testing.T) {
	t.Parallel()
	for _, dir := range []string{t.TempDir(), filepath.Join(t.TempDir(), "missing")} {
		p, err := ReadOrEmpty(dir)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		if len(p.Versions) != 0 || p.Revision == "" || !filepath.IsAbs(p.Dir) {
			t.Fatalf("%s: %+v", dir, p)
		}
		if err := p.Validate(); err != nil {
			t.Fatalf("empty policy invalid: %v", err)
		}
		if _, err := Read(dir); !errors.Is(err, ErrNoPolicy) && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Read(%s) = %v", dir, err)
		}
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "versions.yaml"), []byte("versions: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadOrEmpty(dir); err == nil {
		t.Fatal("a broken file read as empty")
	}
}

// A debrid version needs no download client: the router adds to the providers itself.
func TestDebridVersionWithoutClient(t *testing.T) {
	t.Parallel()
	dir := copyExample(t)
	path := filepath.Join(dir, "versions.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Replace(string(b), "    providers: [rd, ad, tb]\n    client: decypharr\n    import: symlink", "    providers: [rd, ad, tb]\n    import: symlink", 1)
	if s == string(b) {
		t.Fatal("test setup: client line not found")
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err != nil {
		t.Fatal(err)
	}
}

func TestPrivacy(t *testing.T) {
	t.Parallel()
	for p, want := range map[Privacy]bool{"public": false, "private": true, "semi-private": true, "": true, "unknown": true} {
		if got := p.IsPrivate(); got != want {
			t.Errorf("Privacy(%q).IsPrivate() = %v", p, got)
		}
	}
}

// Keys removed when privacy became a label only still load: they are dropped and named in Ignored.
func TestRemovedKeysIgnored(t *testing.T) {
	t.Parallel()
	dir := copyExample(t)
	appendTo := func(name, text string) {
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(text); err != nil {
			t.Fatal(err)
		}
	}
	appendTo("views.yaml", "release_push:\n  public: [autobrr]\n")
	b, err := os.ReadFile(filepath.Join(dir, "indexers.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Replace(string(b), "    privacy: private\n", "    privacy: private\n    private_delivery: torrent\n", 1)
	if s == string(b) {
		t.Fatal("test setup: no private indexer")
	}
	if err := os.WriteFile(filepath.Join(dir, "indexers.yaml"), []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(p.Ignored, "\n")
	for _, want := range []string{"release_push", "indexers[tracker-example].private_delivery"} {
		if !strings.Contains(got, want) {
			t.Errorf("Ignored %q lacks %s", p.Ignored, want)
		}
	}
	// Any other unknown key still fails the strict decode.
	appendTo("views.yaml", "bogus: 1\n")
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("an unknown key loaded: %v", err)
	}
}

func TestIndexerProviders(t *testing.T) {
	t.Parallel()
	p, err := Load(exampleDir)
	if err != nil {
		t.Fatal(err)
	}
	var v *VersionPolicy
	var vk, ik string
	for k, vp := range p.Versions {
		if vp.Storage == StorageDebrid && len(vp.Providers) > 1 && len(vp.Indexers) > 0 {
			v, vk, ik = vp, k, vp.Indexers[0]
			break
		}
	}
	if v == nil {
		t.Fatal("the example has no debrid version with two providers")
	}
	if got := p.ReleaseProviders(v, ik); !slices.Equal(got, v.Providers) {
		t.Errorf("no allowlist: %v, want %v", got, v.Providers)
	}
	p.Indexers[ik].Providers = []string{v.Providers[1]}
	if got := p.ReleaseProviders(v, ik); !slices.Equal(got, []string{v.Providers[1]}) {
		t.Errorf("allowlist: %v", got)
	}
	if got := p.ReleaseProviders(v, ManualIndexer); !slices.Equal(got, v.Providers) {
		t.Errorf("a manual add: %v, want %v", got, v.Providers)
	}
	if keyRE.MatchString(ManualIndexer) {
		t.Errorf("%q could be an indexer's key", ManualIndexer)
	}
	for _, tc := range []struct{ list, err string }{
		{"[nope]", "unknown provider"},
		{"[" + v.Providers[0] + ", " + v.Providers[0] + "]", "listed twice"},
	} {
		dir := copyExample(t)
		path := filepath.Join(dir, "indexers.yaml")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		s := strings.Replace(string(b), "  "+ik+":\n", "  "+ik+":\n    providers: "+tc.list+"\n", 1)
		if s == string(b) {
			t.Fatalf("test setup: indexer %s of %s not found", ik, vk)
		}
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Errorf("providers %s: want %q, got %v", tc.list, tc.err, err)
		}
	}
}

// Privacy is a label only: a debrid version may list a private-labelled indexer (the user's choice).
func TestPrivateIndexerInDebridVersionLoads(t *testing.T) {
	t.Parallel()
	dir := copyExample(t)
	path := filepath.Join(dir, "versions.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	old := "indexers: [prowlarr-public, dmm, torrentio, zilean]\n    providers: [rd, ad, tb]"
	s := strings.Replace(string(b), old, "indexers: [prowlarr-public, tracker-example]\n    providers: [rd, ad, tb]", 1)
	if s == string(b) {
		t.Fatal("test setup: version not found")
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(p.Versions["movie-remux"].Indexers, "tracker-example") {
		t.Error("the private-labelled indexer was dropped")
	}
}

func TestIndexerByName(t *testing.T) {
	t.Parallel()
	p := &Policy{Indexers: map[string]*Indexer{
		"prowlarr-torrentgalaxy": {}, "dmm": {}, "nyaa-si": {}, "nyaa": {},
	}}
	for name, want := range map[string]string{
		"TorrentGalaxy (Prowlarr)": "prowlarr-torrentgalaxy", "torrentgalaxy": "prowlarr-torrentgalaxy",
		"DMM": "dmm", "dmm": "dmm", "Nyaa.si": "nyaa-si", "Nyaa": "nyaa", "unknown": "", "": "",
	} {
		if got := p.IndexerByName(name); got != want {
			t.Errorf("IndexerByName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestCeilingDiscOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "versions.yaml"), []byte("versions:\n  normal: {}\n  disc:\n    ceiling:\n      disc_only: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.Versions["normal"].Ceiling.DiscOnly || !p.Versions["disc"].Ceiling.DiscOnly {
		t.Fatal("disc_only default or explicit value lost")
	}
}

func TestGoliveDone(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		value, err string
	}{
		{"[]", ""},
		{"[homepage]", ""},
		{"[autobrr]", `versions[movie-web].golive_done[0]: must be homepage`},
		{"['']", `versions[movie-web].golive_done[0]: must be homepage`},
		{"[' homepage ']", `versions[movie-web].golive_done[0]: must be homepage`},
		{"[homepage, homepage]", `versions[movie-web].golive_done[1]: "homepage" listed twice`},
		{"homepage", "cannot unmarshal"},
	} {
		t.Run(tc.value, func(t *testing.T) {
			dir := copyExample(t)
			path := filepath.Join(dir, "versions.yaml")
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			b = bytes.Replace(b, []byte("  movie-web:\n"), []byte("  movie-web:\n    golive_done: "+tc.value+"\n"), 1)
			if err := os.WriteFile(path, b, 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := Load(dir)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("want error containing %q, got %v", tc.err, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Join(p.Versions["movie-web"].GoliveDone, ",")
			if want := strings.Trim(tc.value, "[]"); got != want {
				t.Errorf("golive_done = %q, want %q", got, want)
			}
			if len(p.Versions["movie-remux"].GoliveDone) != 0 {
				t.Error("acknowledgement applied to another version")
			}
		})
	}
}

func TestRevision(t *testing.T) {
	t.Parallel()
	dir := copyExample(t)
	a, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Revision) != 12 {
		t.Fatalf("revision %q: want 12 hex digits", a.Revision)
	}
	b, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a.Revision != b.Revision {
		t.Fatalf("same files, revisions %s and %s", a.Revision, b.Revision)
	}
	if r, err := RevisionOf(dir); err != nil || r != a.Revision {
		t.Fatalf("RevisionOf: %s %v, want %s", r, err, a.Revision)
	}
	// A comment changes the bytes, so the revision; a file outside the policy (a subdirectory, a dotfile) does not.
	if err := os.WriteFile(filepath.Join(dir, ".notes.yaml"), []byte("# x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if c, err := Load(dir); err != nil || c.Revision != a.Revision {
		t.Fatalf("a dotfile changed the revision: %v %s", err, c.Revision)
	}
	f := filepath.Join(dir, a.Files[0])
	body, _ := os.ReadFile(f)
	if err := os.WriteFile(f, append(body, []byte("\n# edited\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Revision == a.Revision {
		t.Fatal("an edited file kept the revision")
	}
}

func TestFrozenProfileValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		step    int
		quality string
		want    string
	}{
		{"zero upgrade step and disabled size", 0, "BR-DISK", ""},
		{"negative upgrade step", -1, "BR-DISK", "must not be negative"},
		{"unknown size quality", 1, "imaginary", "unknown quality"},
		{"wrong flavour size quality", 1, "Bluray-2160p Remux", "not a movie quality"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load(exampleDir)
			if err != nil {
				t.Fatal(err)
			}
			p.Profiles["remux-2160p"].MinUpgradeStep = &tc.step
			p.Profiles["remux-2160p"].Sizes = map[string]SizeLimit{tc.quality: {Min: 1}}
			err = p.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
}

// A credential query parameter is refused whatever its case or separators, and only those are.
func TestCheckURLQueryCredentialNames(t *testing.T) {
	t.Parallel()
	for _, u := range []string{"http://plex.lan:32400/?X-Plex-Token=s", "http://10.1.2.3:8989/?apiKey=s", "http://10.1.2.3:8989/?APIKEY=s",
		"http://t.lan/rss?passkey=s", "http://t.lan/rss?rss_key=s", "http://t.lan/?key=s", "http://t.lan/?access_token=s"} {
		var e Errors
		checkURL(&e, "x.url", u)
		if len(e.List) != 1 {
			t.Errorf("%s accepted: %v", u, e.List)
		}
	}
	for _, u := range []string{"http://t.lan/api?t=search&cat=2000", "http://t.lan/?keywords=a&monkey=b&tokens_left=1"} {
		var e Errors
		checkURL(&e, "x.url", u)
		if len(e.List) != 0 {
			t.Errorf("%s refused: %v", u, e.List)
		}
	}
}

// A movie library's optional minimum availability and availability delay: every value of the three, and any number
// of days either way, load; unset they are left out of the policy as it is marshalled.
func TestVersionAvailabilitySettings(t *testing.T) {
	t.Parallel()
	p, err := Load(filepath.Join("..", "..", "examples", "policy"))
	if err != nil {
		t.Fatal(err)
	}
	v := p.Versions["movie-web"]
	if v.MinimumAvailability != "" || v.AvailabilityDelay != 0 {
		t.Fatalf("the example sets them: %q %d", v.MinimumAvailability, v.AvailabilityDelay)
	}
	for _, format := range []func(any) ([]byte, error){json.Marshal, yaml.Marshal} {
		raw, err := format(v)
		if err != nil {
			t.Fatal(err)
		}
		if s := strings.ToLower(string(raw)); strings.Contains(s, "availab") {
			t.Errorf("unset availability settings are written: %s", raw)
		}
	}
	for _, minimum := range []string{"announced", "inCinemas", "released"} {
		for _, delay := range []int{0, 14, -14} {
			v.MinimumAvailability, v.AvailabilityDelay = minimum, delay
			if err := p.Validate(); err != nil {
				t.Errorf("%s, %d days: %v", minimum, delay, err)
			}
		}
	}
	raw, err := yaml.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var back VersionPolicy
	if err := yaml.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.MinimumAvailability != "released" || back.AvailabilityDelay != -14 {
		t.Errorf("read back %q %d from %s", back.MinimumAvailability, back.AvailabilityDelay, raw)
	}
}
