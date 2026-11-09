package preflight

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/golive"
	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/uipages"
)

// readOnlyFS is the real filesystem with every directory refused for writing (tests run as root, where access(2)
// says yes to everything).
type readOnlyFS struct{ golive.OSFS }

func (readOnlyFS) Writable(string) error { return errors.New("read-only file system") }

func items(r *Report) map[string]Item {
	out := map[string]Item{}
	for _, it := range r.Items {
		out[it.Area+"/"+it.Check] = it
	}
	return out
}

func want(t *testing.T, r *Report, key string, st Status, why string) Item {
	t.Helper()
	it, ok := items(r)[key]
	if !ok {
		t.Fatalf("no item %s in %+v", key, r.Items)
	}
	if it.Status != st || !strings.Contains(it.Why, why) {
		t.Fatalf("%s = %s %q, want %s containing %q", key, it.Status, it.Why, st, why)
	}
	if st != OK && it.Next == "" {
		t.Fatalf("%s has no next step", key)
	}
	if st == OK && it.Next != "" {
		t.Fatalf("%s is ok with a next step %q", key, it.Next)
	}
	return it
}

// good is a complete install on temp directories: one active debrid version, a provider and an indexer that test
// fine, TMDb, Plex and the egress.
func good(t *testing.T) (Options, string) {
	dir := t.TempDir()
	mount, root, trash := filepath.Join(dir, "mount"), filepath.Join(dir, "media", "movies"), filepath.Join(dir, "trash")
	for _, d := range []string{filepath.Join(mount, policy.DefaultLibraryAllDir), root, trash} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p := &policy.Policy{
		Versions:  map[string]*policy.VersionPolicy{"movies": {Mode: policy.ModeActive, Kind: policy.KindMovie, Storage: policy.StorageDebrid, Import: "symlink"}},
		Providers: map[string]*policy.Provider{"rd": {Kind: "realdebrid", APIKeyEnv: "RD_KEY"}},
		Indexers:  map[string]*policy.Indexer{"feed": {Kind: "torznab", APIKeyEnv: "FEED_KEY", Privacy: policy.PrivacyPublic}},
		Metadata:  &policy.Metadata{Sources: map[string]*policy.MetadataSource{"tmdb": {APIKeyEnv: "TMDB_KEY"}}},
		Egress:    &policy.Egress{},
		Library: &policy.Library{Mount: policy.LibraryMount{Path: mount}, Trash: policy.LibraryTrash{Dir: trash},
			Plex:     &policy.Plex{URL: "http://plex.invalid:32400", TokenEnv: "PLEX_TOKEN"},
			Versions: map[string]*policy.LibraryVersion{"movies": {Roots: []string{root}}}},
	}
	if err := os.WriteFile(filepath.Join(mount, policy.DefaultLibraryAllDir, "Movie.2024.1080p"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	results := 7
	o := Options{Policy: p, FS: golive.OSFS{}, Device: OSDevice, APIKeys: 1,
		Credentials: settings.Credentials{Lookup: func(name string) (string, bool) { return "invented", name != "" }},
		Mount:       func(ctx context.Context, d string) health.Result { return health.Mount(d, time.Second)(ctx) },
		Sources: func(_ context.Context, entity string) []Source {
			if entity == "providers" {
				return []Source{{Key: "rd", Status: "ok", Premium: true, Expires: time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC)}}
			}
			return []Source{{Key: "feed", Status: "ok", Results: &results, Caps: []string{"rss", "movie", "text"}}}
		},
		Views: func(context.Context) []View {
			return []View{{Path: "/radarr", Code: 401}, {Path: "/v/movies/radarr", Code: 401}}
		},
		Plex:   func(context.Context) error { return nil },
		Health: []health.Result{{Source: "VpnEgressCheck", Type: health.OK, Message: "VPN egress ok"}},
	}
	return o, dir
}

func TestRunGoodInstall(t *testing.T) {
	o, _ := good(t)
	r := Run(context.Background(), o)
	if r.Fail != 0 || r.Warn != 0 {
		var b bytes.Buffer
		Print(&b, r)
		t.Fatalf("a complete install has %d fail, %d warn:\n%s", r.Fail, r.Warn, b.String())
	}
	want(t, r, "versions/versions", OK, "1 library; active: movies")
	want(t, r, "library/mount", OK, "visible and readable")
	want(t, r, "library/root movies", OK, "writable")
	want(t, r, "library/trash", OK, "writable")
	want(t, r, "providers/rd (realdebrid)", OK, "premium until 2027-01-02")
	want(t, r, "indexers/feed (torznab)", OK, "7 release(s); answers rss, movie, text queries")
	want(t, r, "metadata/tmdb", OK, "TMDb key set")
	want(t, r, "views//v/movies/radarr", OK, "HTTP 401 without a key")
	want(t, r, "views/api keys", OK, "1 active")
	want(t, r, "plex/plex", OK, "answers")
	want(t, r, "egress/vpn egress", OK, "VPN egress ok")
	if !strings.Contains(r.Items[len(r.Items)-1].Why, "off") {
		t.Fatalf("library.enabled reported as on: %+v", r.Items[len(r.Items)-1])
	}
	if got := Missing(o.Policy, len(o.Policy.Indexers), len(o.Policy.Providers)); len(got) != 0 {
		t.Fatalf("Missing = %v", got)
	}
}

// Settings that have a form are pointed at it, not at their YAML keys.
func TestNextStepsNameTheForms(t *testing.T) {
	p := &policy.Policy{
		Versions: map[string]*policy.VersionPolicy{"tv": {Kind: policy.KindSeries, Storage: policy.StorageDebrid}},
		Indexers: map[string]*policy.Indexer{"feed": {Kind: "torznab", URL: "http://prowlarr:9696/1/api"}},
	}
	r := Run(context.Background(), Options{Policy: p, APIKeys: 0})
	for key, page := range map[string]string{"library/mount": uipages.FileManagement, "library/trash": uipages.FileManagement,
		"metadata/tvdb": uipages.Metadata, "egress/vpn egress": uipages.Indexers} {
		it, ok := items(r)[key]
		if !ok {
			t.Fatalf("no item %s in %+v", key, r.Items)
		}
		if !strings.Contains(it.Next, page) || strings.Contains(it.Next, "YAML") {
			t.Errorf("%s next %q, want it to name %s", key, it.Next, page)
		}
	}
}

func TestRunEmptyInstall(t *testing.T) {
	r := Run(context.Background(), Options{Policy: &policy.Policy{}, APIKeys: 0})
	want(t, r, "versions/versions", Warn, "no libraries")
	want(t, r, "library/mount", Warn, "library.mount.path is not set")
	want(t, r, "library/trash", Warn, "library.trash.dir is not set")
	want(t, r, "providers/providers", Warn, "no debrid provider")
	want(t, r, "indexers/indexers", Warn, "no indexers")
	want(t, r, "metadata/tmdb", Fail, "no TMDb source")
	want(t, r, "views/views", Warn, "no libraries")
	want(t, r, "views/api keys", Warn, "no API keys")
	want(t, r, "plex/plex", Warn, "no Plex server")
	want(t, r, "egress/vpn egress", OK, "no egress section")
	want(t, r, "switches/router.enabled", OK, "off")
	forced := Run(context.Background(), Options{Policy: &policy.Policy{Library: &policy.Library{Enabled: false}, ForcedOff: []string{"library"}}, APIKeys: 0})
	want(t, forced, "switches/library.enabled", OK, "forced off by environment")
	want(t, forced, "switches/router.enabled", OK, "off: nothing is added")
	typo := Run(context.Background(), Options{Policy: &policy.Policy{ForceOffUnknown: []string{"THEATARR_FORCE_OFF: routr"}}, APIKeys: 0})
	want(t, typo, "switches/force off", Warn, "THEATARR_FORCE_OFF: routr is not a switch")
	if r.Fail != 1 {
		t.Fatalf("fail = %d, want only the TMDb key", r.Fail)
	}
	if got := Missing(nil, 0, 0); len(got) != 5 {
		t.Fatalf("Missing(empty) = %v", got)
	}
}

func TestRunBadInstall(t *testing.T) {
	ctx := context.Background()
	t.Run("debrid version without mount, provider or root", func(t *testing.T) {
		o, _ := good(t)
		o.Policy.Library.Mount.Path = ""
		o.Policy.Library.Versions = nil
		o.Policy.Providers = nil
		r := Run(ctx, o)
		want(t, r, "library/mount", Fail, "downloads for movies can never be imported")
		want(t, r, "library/roots movies", Fail, "every library write for it is refused")
		want(t, r, "providers/providers", Fail, "no debrid provider")
	})
	t.Run("mount not answering", func(t *testing.T) {
		o, dir := good(t)
		o.Policy.Library.Mount.Path = filepath.Join(dir, "gone")
		want(t, Run(ctx, o), "library/mount", Fail, "gone")
	})
	t.Run("root missing, not writable, inside the mount", func(t *testing.T) {
		o, dir := good(t)
		o.Policy.Library.Versions["movies"].Roots = []string{filepath.Join(dir, "nowhere")}
		want(t, Run(ctx, o), "library/root movies", Fail, "nowhere")

		o, _ = good(t)
		o.FS = readOnlyFS{}
		want(t, Run(ctx, o), "library/root movies", Fail, "not writable")
		want(t, Run(ctx, o), "library/trash", Fail, "not writable")

		o, _ = good(t)
		inside := filepath.Join(o.Policy.Library.Mount.Path, "movies")
		if err := os.Mkdir(inside, 0o755); err != nil {
			t.Fatal(err)
		}
		o.Policy.Library.Versions["movies"].Roots = []string{inside}
		r := Run(ctx, o)
		found := false
		for _, it := range r.Items {
			found = found || it.Check == "root movies" && it.Status == Fail && strings.Contains(it.Why, "inside the debrid mount")
		}
		if !found {
			t.Fatalf("a root inside the mount is not a fail: %+v", r.Items)
		}
	})
	t.Run("hardlinks across filesystems", func(t *testing.T) {
		o, _ := good(t)
		o.Policy.Versions["movies"].Import = "hardlink"
		root := o.Policy.Library.Versions["movies"].Roots[0]
		o.Device = func(p string) (uint64, error) {
			if p == root {
				return 1, nil
			}
			return 2, nil
		}
		r := Run(ctx, o)
		found := false
		for _, it := range r.Items {
			found = found || it.Check == "root movies" && it.Status == Fail && strings.Contains(it.Why, "different filesystems")
		}
		if !found {
			t.Fatalf("hardlinks across filesystems are not a fail: %+v", r.Items)
		}
		o.Device = func(string) (uint64, error) { return 1, nil }
		r = Run(ctx, o)
		for _, it := range r.Items {
			if it.Status == Fail {
				t.Fatalf("one filesystem fails: %+v", it)
			}
		}
	})
	t.Run("sources", func(t *testing.T) {
		o, _ := good(t)
		o.Sources = func(_ context.Context, entity string) []Source {
			if entity == "providers" {
				return []Source{{Key: "rd", Status: "failed", Category: "auth", Message: "the provider refused the API key"}}
			}
			return []Source{{Key: "feed", Status: "refused", Category: "vpn", Message: "the VPN check has not passed"}}
		}
		r := Run(ctx, o)
		it := want(t, r, "providers/rd (realdebrid)", Fail, "refused the API key")
		if !strings.Contains(it.Next, "check the API key") {
			t.Fatalf("next = %q", it.Next)
		}
		want(t, r, "indexers/feed (torznab)", Warn, "VPN check")

		o.Sources = func(_ context.Context, entity string) []Source {
			if entity == "providers" {
				return []Source{{Key: "rd", Status: "ok"}}
			}
			return nil
		}
		r = Run(ctx, o)
		want(t, r, "providers/rd (realdebrid)", Warn, "no premium plan")
		want(t, r, "indexers/feed (torznab)", Warn, "disabled")
	})
	t.Run("missing credentials and keys", func(t *testing.T) {
		o, _ := good(t)
		o.Credentials.Lookup = nil
		r := Run(ctx, o)
		want(t, r, "providers/rd (realdebrid)", Fail, "no API key")
		want(t, r, "indexers/feed (torznab)", Fail, "no API key")
		want(t, r, "metadata/tmdb", Fail, "TMDB_KEY is not set")
		want(t, r, "plex/plex", Fail, "PLEX_TOKEN is not set")
	})
	t.Run("views, Plex, egress", func(t *testing.T) {
		o, _ := good(t)
		o.Views = func(context.Context) []View { return []View{{Path: "/radarr", Code: 404}} }
		o.Plex = func(context.Context) error { return errors.New("connection refused") }
		o.Health = []health.Result{{Source: "VpnEgressCheck", Type: health.Error, Message: "VPN egress failed"},
			{Source: "IndexerStatusCheck", Type: health.Warning, Message: "feed backed off"}}
		o.APIKeys = 0
		r := Run(ctx, o)
		want(t, r, "views//radarr", Fail, "HTTP 404")
		want(t, r, "views/api keys", Warn, "no API keys")
		want(t, r, "plex/plex", Fail, "connection refused")
		want(t, r, "egress/vpn egress", Fail, "VPN egress failed")
		want(t, r, "indexers/back-offs", Warn, "feed backed off")
	})
	t.Run("nothing to check with", func(t *testing.T) {
		o, _ := good(t)
		o.FS, o.Mount, o.Sources, o.Views, o.Plex, o.Health = nil, nil, nil, nil, nil, nil
		r := Run(ctx, o)
		// Unchecked is never ok.
		want(t, r, "library/mount", Warn, "not checked")
		want(t, r, "library/root movies", Warn, "not checked")
		want(t, r, "providers/rd (realdebrid)", Warn, "not tested")
		want(t, r, "views/views", Warn, "not asked")
		want(t, r, "plex/plex", Warn, "not asked")
		want(t, r, "egress/vpn egress", Warn, "not checked")
	})
}

func TestPrint(t *testing.T) {
	var b bytes.Buffer
	Print(&b, Run(context.Background(), Options{Policy: &policy.Policy{}}))
	out := b.String()
	if !strings.Contains(out, "FAIL  metadata") || !strings.Contains(out, "next: add a metadata service named tmdb ") ||
		!strings.HasSuffix(out, "-- 1 FAIL, 8 WARN, 4 OK\n") {
		t.Fatalf("print:\n%s", out)
	}
}

// Stored section credentials have derived names that never exist in the process environment.
func TestStoredSectionCredentials(t *testing.T) {
	o, _ := good(t)
	o.Credentials.Lookup = nil
	tmdb := policy.SettingsSecretPrefix + "TMDB"
	tvdb := policy.SettingsSecretPrefix + "TVDB"
	plex := policy.SettingsSecretPrefix + "PLEX"
	o.Policy.Metadata.Sources["tmdb"].APIKeyEnv = tmdb
	o.Policy.Metadata.Sources["tvdb"] = &policy.MetadataSource{APIKeyEnv: tvdb}
	o.Policy.Library.Plex.TokenEnv = plex
	o.Policy.WithSettingsSecrets(map[string]string{tmdb: "invented-tmdb", tvdb: "invented-tvdb", plex: "invented-plex"})
	r := Run(context.Background(), o)
	want(t, r, "metadata/tmdb", OK, "stored in settings")
	want(t, r, "metadata/tvdb", OK, "stored in settings")
	want(t, r, "plex/plex", OK, "stored in settings")
	for _, it := range r.Items {
		if strings.Contains(it.Why, policy.SettingsSecretPrefix) || strings.Contains(it.Why, "invented-") {
			t.Fatalf("credential detail leaked: %+v", it)
		}
	}
}

func TestPreflightVPNLimits(t *testing.T) {
	o, _ := good(t)
	for _, live := range []bool{true, false} {
		if !live {
			o.Health = nil
			o.Policy.Egress = nil
		}
		r := Run(context.Background(), o)
		it := items(r)["egress/vpn egress"]
		for _, text := range []string{"does not cover DNS", "Prowlarr", "Zilean", "trawl"} {
			if !strings.Contains(it.Why, text) {
				t.Errorf("missing %q: %+v", text, it)
			}
		}
		if !live && !strings.Contains(it.Why, "VPN guard off") {
			t.Errorf("unconfigured guard: %+v", it)
		}
	}
}

func TestFollowedRootReadOnly(t *testing.T) {
	o, _ := good(t)
	o.FS = readOnlyFS{}
	o.Policy.Versions["movies"].Mode = policy.ModeShadow
	o.Policy.Versions["follower"] = &policy.VersionPolicy{Mode: policy.ModeActive, Follows: "movies"}
	r := Run(context.Background(), o)
	infos := 0
	for _, it := range r.Items {
		if it.Check != "root movies" {
			continue
		}
		if it.Status == Fail {
			t.Fatalf("followed root failed: %+v", it)
		}
		if string(it.Status) == "info" && strings.Contains(it.Why, "read-only by design: followed by follower") {
			infos++
		}
	}
	if infos != 1 {
		t.Fatalf("got %d read-only INFO lines", infos)
	}
	delete(o.Policy.Versions, "follower")
	want(t, Run(context.Background(), o), "library/root movies", Fail, "not writable")
}

type unreadableFS struct{ golive.OSFS }

func (unreadableFS) Readable(string) error { return errors.New("permission denied") }

func TestFollowedRootMustBeReadable(t *testing.T) {
	o, _ := good(t)
	o.Policy.Versions["movies"].Mode = policy.ModeShadow
	o.Policy.Versions["follower"] = &policy.VersionPolicy{Mode: policy.ModeActive, Follows: "movies"}
	o.FS = unreadableFS{}
	r := Run(context.Background(), o)
	for _, it := range r.Items {
		if it.Check == "root movies" && it.Status == Fail && strings.Contains(it.Why, "not readable") {
			return
		}
	}
	t.Fatal("unreadable followed root did not fail")
}
