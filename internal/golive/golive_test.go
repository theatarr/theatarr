package golive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

var now = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

// examplePolicy copies examples/policy into a temp dir, applies edits (file name → old → new) and loads it.
func examplePolicy(t *testing.T, edits ...[3]string) *policy.Policy {
	t.Helper()
	src, _ := filepath.Abs("../../examples/policy")
	dst := t.TempDir()
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	for _, e := range edits {
		p := filepath.Join(dst, e[0])
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(b, []byte(e[1])) {
			t.Fatalf("%s has no %q", e[0], e[1])
		}
		if err := os.WriteFile(p, bytes.Replace(b, []byte(e[1]), []byte(e[2]), 1), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pol, err := policy.Load(dst)
	if err != nil {
		t.Fatal(err)
	}
	return pol
}

// egressOn is the examplePolicy edit that turns on the example's VPN egress guard (commented out there, off by
// default), for the tests of the guard.
func egressOn(t *testing.T) [3]string {
	t.Helper()
	b, err := os.ReadFile("../../examples/policy/egress.yaml")
	if err != nil {
		t.Fatal(err)
	}
	off := string(b[bytes.Index(b, []byte("# egress:")):])
	on := ""
	for _, line := range strings.SplitAfter(off, "\n") {
		on += strings.TrimPrefix(strings.TrimPrefix(line, "#"), " ")
	}
	return [3]string{"egress.yaml", off, on}
}

// fakeFS: every path is a writable directory unless listed.
type fakeFS struct {
	missing, readOnly map[string]bool
}

type dirInfo struct{ fs.FileInfo }

func (dirInfo) IsDir() bool { return true }

func (f fakeFS) Stat(p string) (fs.FileInfo, error) {
	if f.missing[p] {
		return nil, fs.ErrNotExist
	}
	return dirInfo{}, nil
}

func (f fakeFS) Writable(p string) error {
	if f.readOnly[p] {
		return errors.New("read-only file system")
	}
	return nil
}

// fakeArr answers GETs from canned JSON per endpoint and records them.
type fakeArr struct {
	bodies map[string]string
	gets   []string
}

func (a *fakeArr) GetJSON(_ context.Context, endpoint string, q url.Values, v any) error {
	a.gets = append(a.gets, endpoint)
	b, ok := a.bodies[endpoint]
	if !ok {
		return fmt.Errorf("404 %s", endpoint)
	}
	return json.Unmarshal([]byte(b), v)
}

// allEnv answers every variable with a value that must never show in the output.
func allEnv(string) string { return "s3cr3t-value" }

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

func exec(t *testing.T, st *store.Store, q string, args ...any) {
	t.Helper()
	if _, err := st.Writer().Exec(q, args...); err != nil {
		t.Fatalf("%v\n%s", err, q)
	}
}

// seed: n movies of the version, shadow runs since days ago, agree of grabs agreeing, the arr instance and its
// titles (arr ids 1..n).
func seed(t *testing.T, st *store.Store, version, kind, instance string, n, grabs, agree int, days float64) {
	t.Helper()
	ms := store.Millis(now)
	for i := 1; i <= n; i++ {
		exec(t, st, `INSERT INTO title (kind, name, created_at, updated_at) VALUES (?, ?, 0, 0)`, kind,
			fmt.Sprintf("%s title %d", version, i))
		exec(t, st, `INSERT INTO version (title_id, policy_key, monitored, state, created_at, updated_at)
			VALUES (last_insert_rowid(), ?, 1, 'cutoff_met', 0, 0)`, version)
	}
	exec(t, st, `INSERT INTO shadow_run (kind, started_at, finished_at) VALUES ('arr.import', ?, ?)`,
		ms-int64(days*24*3600*1000), ms)
	for i := 0; i < grabs; i++ {
		bucket, reason := "agree", "same_release"
		if i >= agree {
			bucket, reason = "arr_grabbed_we_wouldnt", "size"
		}
		exec(t, st, `INSERT INTO shadow_diff (at, title_id, version_key, bucket, reason, created_at, kind)
			VALUES (?, (SELECT MIN(title_id) FROM version WHERE policy_key = ?), ?, ?, ?, ?, ?)`, ms-3600_000, version,
			version, bucket, reason, ms, kind)
	}
	if instance != "" {
		flavour := "radarr"
		if kind == "series" {
			flavour = "sonarr"
		}
		exec(t, st, `INSERT OR IGNORE INTO arr_instance (name, flavour, policy_key, created_at, updated_at)
			VALUES (?, ?, ?, 0, 0)`, instance, flavour, version)
		exec(t, st, `INSERT INTO arr_title (instance, arr_id, kind, title_id, version_id, monitored)
			SELECT ?, v.id, ?, v.title_id, v.id, 1 FROM version v WHERE v.policy_key = ?`, instance, kind, version)
	}
	exec(t, st, `INSERT INTO library_dangling_scan (at, roots, links, dangling, took_ms) VALUES (?, '[]', 10, 0, 1)`, ms)
}

func okHealth() []health.Result {
	return []health.Result{{Source: "BackupCheck", Type: health.OK, Message: "Newest backup 2h old"},
		{Source: "MountCheck", Type: health.OK, Message: "Mount readable"}}
}

func movieArr(ids int, queue int, grabbing bool) *fakeArr {
	var items []string
	for i := 1; i <= ids; i++ {
		items = append(items, fmt.Sprintf(`{"id": %d, "title": "Movie %d", "monitored": true}`, i, i))
	}
	var recs []string
	for i := 0; i < queue; i++ {
		recs = append(recs, fmt.Sprintf(`{"title": "Queued.Release.%d.2160p", "status": "downloading"}`, i))
	}
	return &fakeArr{bodies: map[string]string{
		"movie":          "[" + strings.Join(items, ",") + "]",
		"queue":          fmt.Sprintf(`{"totalRecords": %d, "records": [%s]}`, queue, strings.Join(recs, ",")),
		"indexer":        fmt.Sprintf(`[{"name": "TPB", "enableRss": %v, "enableAutomaticSearch": %v}]`, grabbing, grabbing),
		"downloadclient": fmt.Sprintf(`[{"name": "decypharr-web", "enable": %v}]`, grabbing),
	}}
}

func run(t *testing.T, o Options) *Report {
	t.Helper()
	if o.Now.IsZero() {
		o.Now = now
	}
	r, err := Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func find(r *Report, sec, check string) []Result {
	var out []Result
	for _, x := range r.Results {
		if x.Section == sec && x.Check == check {
			out = append(out, x)
		}
	}
	return out
}

func want(t *testing.T, r *Report, sec, check string, st Status, contains string) {
	t.Helper()
	for _, x := range find(r, sec, check) {
		if x.Status == st && strings.Contains(x.Message, contains) {
			return
		}
	}
	var b bytes.Buffer
	Print(&b, r)
	t.Fatalf("no %s %s/%s containing %q in:\n%s", st, sec, check, contains, b.String())
}

func TestReadyVersion(t *testing.T) {
	t.Parallel()
	pol := examplePolicy(t, egressOn(t))
	st := testStore(t)
	seed(t, st, "movie-web", "movie", "radarr_4k_web", 30, 25, 25, 20)
	exec(t, st, `INSERT INTO title_meta (title_id, refreshed_at, attempted_at, refresh_after)
		SELECT title_id, 1, 1, 1 FROM version`)
	arrc := movieArr(30, 0, true)
	r := run(t, Options{Policy: pol, Version: "movie-web", DB: st.Reader(), Env: allEnv, FS: fakeFS{},
		Arr: func(string) (ArrGetter, error) { return arrc, nil }, Health: okHealth(),
		Service: &ServiceState{VPNOK: map[string]bool{"direct": true}},
		Profile: func(string, policy.Kind) error { return nil },
		Scan: func(context.Context, string) ([]*library.LinkScan, error) {
			return []*library.LinkScan{{Version: "movie-web", Root: "/media/movies-web", Links: 30,
				IntoMnt: 30, Names: &library.NameCheck{Files: 30, Shaped: 30, Equal: 30, Naming: "policy"}}}, nil
		}})
	if r.Failed() {
		var b bytes.Buffer
		Print(&b, r)
		t.Fatalf("a ready version failed:\n%s", b.String())
	}
	want(t, r, SecPolicy, "library root", Pass, "writable by Theatarr")
	want(t, r, SecPolicy, "router fallback", Pass, "rd → ad → tb")
	want(t, r, SecSecrets, "THEATARR_RD_KEY", Pass, "provider rd")
	want(t, r, SecSecrets, "THEATARR_VIEW_RADARR_KEY", Pass, "view radarr")
	want(t, r, SecHealth, "VpnEgressCheck", Pass, "confirmed")
	want(t, r, SecHealth, "movie metadata", Pass, "all 30 movies")
	want(t, r, SecShadow, "agreement", Pass, "100.0% (25 of 25")
	want(t, r, SecShadow, "window", Pass, "20 days")
	want(t, r, SecLibrary, "dangling", Pass, "no dangling links")
	want(t, r, SecLibrary, "names", Pass, "all 30")
	want(t, r, SecHandover, "arr titles", Pass, "all 30 titles")
	want(t, r, SecHandover, "arr queue", Pass, "empty")
	want(t, r, SecHandover, "arr grabbing", Info, "1 of 1 indexers with RSS")
	want(t, r, SecHandover, "client Seerr", Info, `pick profile "WEB only"`)
	// GET only, and only the four reads.
	if got := strings.Join(arrc.gets, ","); got != "movie,queue,indexer,downloadclient" {
		t.Errorf("arr reads = %s", got)
	}
}

// A debrid version with no download client goes live: the router adds to the providers itself.
func TestDebridVersionWithoutClient(t *testing.T) {
	t.Parallel()
	pol := examplePolicy(t, [3]string{"versions.yaml", "library: /media/movies-web\n    storage: debrid\n    indexers: [prowlarr-public, dmm, torrentio, zilean]\n    providers: [rd, ad, tb]\n    client: decypharr\n",
		"library: /media/movies-web\n    storage: debrid\n    indexers: [prowlarr-public, dmm, torrentio, zilean]\n    providers: [rd, ad, tb]\n"})
	st := testStore(t)
	seed(t, st, "movie-web", "movie", "radarr_4k_web", 30, 25, 25, 20)
	r := run(t, Options{Policy: pol, Version: "movie-web", DB: st.Reader(), Env: allEnv, FS: fakeFS{}, Health: okHealth(),
		Service: &ServiceState{VPNOK: map[string]bool{"direct": true}}, Profile: func(string, policy.Kind) error { return nil }})
	want(t, r, SecPolicy, "client", Info, "adds go to the providers directly")
}

func TestBlockers(t *testing.T) {
	t.Parallel()
	// The live shadow policy's shape: a from_arr profile and naming that name the owning instance.
	pol := examplePolicy(t,
		[3]string{"versions.yaml", "    profile: web-2160p\n", "    profile: arr-web\n    naming: {from_arr: radarr_4k_web}\n"},
		[3]string{"profiles.yaml", "profiles:\n", "profiles:\n  arr-web:\n    from_arr: radarr_4k_web/WEB 2160p\n"})
	st := testStore(t)
	seed(t, st, "movie-web", "movie", "radarr_4k_web", 10, 10, 8, 3)
	exec(t, st, `INSERT INTO library_dangling (path, target, root, version_key, folder, first_seen_at, last_seen_at)
		VALUES ('/media/movies-web/A (2020)/a.mkv', '/mnt/debrid/__all__/x/a.mkv',
		'/media/movies-web', 'movie-web', 'x', 0, 0)`)
	env := func(k string) string {
		if k == "THEATARR_TMDB_KEY" {
			return "x"
		}
		return ""
	}
	arrc := movieArr(12, 2, true)
	r := run(t, Options{Policy: pol, Version: "movie-web", DB: st.Reader(), Env: env,
		FS:  fakeFS{readOnly: map[string]bool{"/media/movies-web": true}, missing: map[string]bool{"/media/.trash": true}},
		Arr: func(string) (ArrGetter, error) { return arrc, nil }, Health: []health.Result{{Source: "BackupCheck",
			Type: health.Warning, Message: "The newest backup has no policy archive or manifest beside it"}}})
	if !r.Failed() {
		t.Fatal("blockers did not fail")
	}
	want(t, r, SecPolicy, "profile", Warn, "from_arr: radarr_4k_web/WEB 2160p")
	want(t, r, SecPolicy, "profile build", Warn, "not checked")
	want(t, r, SecPolicy, "library root", Fail, "not writable by Theatarr: read-only file system")
	want(t, r, SecPolicy, "trash", Fail, "/media/.trash")
	want(t, r, SecPolicy, "naming", Warn, "holds no naming")
	want(t, r, SecPolicy, "router fallback", Fail, "none has a key")
	want(t, r, SecSecrets, "THEATARR_VIEW_RADARR_KEY", Warn, "not set (view radarr (/radarr))")
	want(t, r, SecSecrets, "THEATARR_PLEX_TOKEN", Warn, "not set")
	want(t, r, SecHealth, "BackupCheck", Warn, "no policy archive")
	want(t, r, SecHealth, "VpnEgressCheck", Warn, "VPN guard off")
	want(t, r, SecHealth, "movie metadata", Warn, "10 have no TMDb details")
	want(t, r, SecShadow, "window", Warn, "3.0 of the 14 days")
	want(t, r, SecShadow, "agreement", Fail, "80.0% (8 of 10")
	want(t, r, SecShadow, "disagreement", Warn, "arr_grabbed_we_wouldnt / size: 2")
	want(t, r, SecLibrary, "dangling", Warn, "1 dangling links in 1 folders")
	want(t, r, SecLibrary, "scan", Warn, "not checked")
	want(t, r, SecHandover, "arr titles", Warn, "2 movie titles Theatarr has not imported: Movie 11, Movie 12")
	want(t, r, SecHandover, "arr queue", Warn, "2 downloads queued")
	// The seedbox qBittorrent is not used by a debrid version: unset, it is a note, never a failure.
	want(t, r, SecSecrets, "client seedbox", Info, "THEATARR_SEEDBOX_QBIT_USER, THEATARR_SEEDBOX_QBIT_PASS not set")
	// One line per variable however many indexers share it.
	if n := len(find(r, SecSecrets, "THEATARR_DMM_KEY")); n != 1 {
		t.Errorf("THEATARR_DMM_KEY lines = %d", n)
	}
}

// golive check simulates no in-place switch: a shadow version with the arr's own profile and naming (the live
// followed libraries' shape) is not failed for what removing its arr would break.
func TestNoInPlaceSwitchSimulation(t *testing.T) {
	t.Parallel()
	pol := examplePolicy(t,
		[3]string{"versions.yaml", "    profile: web-2160p\n", "    profile: arr-web\n    naming: {from_arr: radarr_4k_web}\n"},
		[3]string{"profiles.yaml", "profiles:\n", "profiles:\n  arr-web:\n    from_arr: radarr_4k_web/WEB 2160p\n"})
	r := run(t, Options{Policy: pol, Version: "movie-web", Env: allEnv, FS: fakeFS{}, Health: okHealth(),
		Profile: func(string, policy.Kind) error { return nil }})
	if len(find(r, SecPolicy, "switched policy")) != 0 {
		t.Fatal("golive check simulated an in-place switch")
	}
	for _, x := range r.Results {
		if x.Section == SecPolicy && x.Status == Fail {
			t.Errorf("policy FAIL: %+v", x)
		}
	}
	want(t, r, SecPolicy, "profile", Warn, "from_arr: radarr_4k_web/WEB 2160p")
}

func TestSeriesMetadataAndNoOwner(t *testing.T) {
	t.Parallel()
	pol := examplePolicy(t)
	st := testStore(t)
	seed(t, st, "tv-2160p", "series", "sonarr_4k_debrid", 3, 0, 0, 20)
	exec(t, st, `INSERT INTO series_meta (title_id, source, refreshed_at, error) SELECT title_id, 'tvdb', 1, '' FROM version
		WHERE policy_key = 'tv-2160p' LIMIT 1`)
	exec(t, st, `INSERT INTO series_meta (title_id, source, refreshed_at, error) SELECT title_id, 'tvdb', 1, 'TVDB 404'
		FROM version WHERE policy_key = 'tv-2160p' ORDER BY title_id DESC LIMIT 1`)
	r := run(t, Options{Policy: pol, Version: "tv-2160p", DB: st.Reader(), Env: allEnv})
	want(t, r, SecHealth, "series metadata", Fail, "of 3 series: 1 never refreshed (tv-2160p title 2); 1 whose last refresh failed (tv-2160p title 3)")
	want(t, r, SecShadow, "agreement", Fail, "no arr grabs of tv-2160p")
	want(t, r, SecPolicy, "plex section", Warn, "no Plex section")
	want(t, r, SecSecrets, "THEATARR_TVDB_KEY", Pass, "TVDB series metadata")
	want(t, r, SecHandover, "sonarr_4k_debrid", Warn, "not checked: the arr was not read")

	// anime-movie: no import.arr instance, no library.versions entry.
	r = run(t, Options{Policy: pol, Version: "anime-movie", DB: st.Reader(), Env: allEnv})
	want(t, r, SecPolicy, "library root", Fail, "library.versions.anime-movie is missing")
	want(t, r, SecShadow, "agreement", Fail, "no import.arr instance feeds anime-movie")
	want(t, r, SecHandover, "owner", Warn, "no import.arr instance")
}

func TestActiveVersionArrStillGrabbing(t *testing.T) {
	t.Parallel()
	pol := examplePolicy(t, [3]string{"versions.yaml", "  movie-web:\n    mode: shadow\n", "  movie-web:\n    mode: active\n"})
	arrc := movieArr(0, 0, true)
	r := run(t, Options{Policy: pol, Version: "movie-web", Env: allEnv,
		Arr: func(string) (ArrGetter, error) { return arrc, nil }})
	want(t, r, SecPolicy, "mode", Info, "already mode: active")
	want(t, r, SecHandover, "arr grabbing", Fail, "still grabs")
	want(t, r, SecHandover, "arr client", Warn, "still has download clients enabled")
}

func TestServiceEgress(t *testing.T) {
	t.Parallel()
	pol := examplePolicy(t, egressOn(t))
	r := run(t, Options{Policy: pol, Version: "movie-remux",
		Service: &ServiceState{VPNOK: map[string]bool{"direct": false}, Backoffs: []string{"indexer:dmm", "budget:tmdb"}}})
	want(t, r, SecHealth, "VpnEgressCheck", Fail, "not confirmed on direct")
	want(t, r, SecHealth, "IndexerStatusCheck", Warn, "backing off now: dmm")
}

func TestServiceEgressOptional(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"off", "monitor-only", "providers"} {
		for _, service := range []*ServiceState{nil, {}, {VPNOK: map[string]bool{"direct": false}, Backoffs: []string{"indexer:dmm"}}} {
			pol := examplePolicy(t, egressOn(t))
			switch mode {
			case "off":
				pol.Egress = nil
			case "monitor-only":
				pol.Egress.RequireVPNFor = []string{}
			case "providers":
				pol.Egress.RequireVPNFor = []string{policy.EgressProviders}
			}
			r := run(t, Options{Policy: pol, Version: "movie-remux", Service: service})
			status, message := Warn, ""
			if mode == "off" {
				message = "VPN guard off"
			} else if service != nil && mode == "providers" {
				status, message = Fail, "requests for providers"
			} else if service != nil {
				message = "monitor-only"
			}
			want(t, r, SecHealth, "VpnEgressCheck", status, message)
			if mode != "providers" {
				for _, result := range find(r, SecHealth, "VpnEgressCheck") {
					if result.Status == Fail {
						t.Fatalf("%s: %+v", mode, result)
					}
				}
			}
			if service != nil && len(service.Backoffs) > 0 {
				want(t, r, SecHealth, "IndexerStatusCheck", Warn, "dmm")
			}
		}
	}
}

func TestSecretValuesNeverPrinted(t *testing.T) {
	t.Parallel()
	pol := examplePolicy(t)
	r := run(t, Options{Policy: pol, Version: "tv-2160p", Env: allEnv, FS: fakeFS{}})
	var b bytes.Buffer
	Print(&b, r)
	j, _ := json.Marshal(r)
	if strings.Contains(b.String(), "s3cr3t") || strings.Contains(string(j), "s3cr3t") {
		t.Fatal("a secret value was printed")
	}
	if !strings.Contains(b.String(), "THEATARR_RD_KEY") {
		t.Fatal("secret names missing")
	}
}

// A version that sends its grabs to decypharr is told the client owns cached-only, pacing and stalls, and needs the
// client's login when the client names one.
func TestClientRoutedVersion(t *testing.T) {
	t.Parallel()
	pol := examplePolicy(t)
	v := pol.Versions["movie-web"]
	v.AddVia, v.Providers = policy.AddViaClient, nil
	pol.Clients["decypharr"].UsernameEnv, pol.Clients["decypharr"].PasswordEnv = "THEATARR_DC_USER", "THEATARR_DC_PASS"
	r := run(t, Options{Policy: pol, Version: "movie-web"})
	want(t, r, SecPolicy, "client", Info, "grabs go to client decypharr")
	want(t, r, SecPolicy, "add via client", Warn, "not router.cached_only")
	want(t, r, SecSecrets, "THEATARR_DC_PASS", Warn, "not set (client decypharr)")
	// router.cached_only is the providers': no green pass for a version whose client decides.
	if got := find(r, SecPolicy, "cached_only"); len(got) != 0 {
		t.Errorf("cached_only checks for a client-routed version: %+v", got)
	}
}

// A library switched to a client keeps its providers list: still no provider cached_only lines (AllDebrid's either).
func TestClientRoutedVersionWithProviders(t *testing.T) {
	t.Parallel()
	pol := examplePolicy(t)
	v := pol.Versions["movie-web"]
	v.AddVia = policy.AddViaClient
	if !slices.ContainsFunc(v.Providers, func(pk string) bool { return pol.Providers[pk].Kind == "alldebrid" }) {
		t.Fatalf("test setup: movie-web lists no AllDebrid provider: %v", v.Providers)
	}
	r := run(t, Options{Policy: pol, Version: "movie-web"})
	if got := find(r, SecPolicy, "cached_only"); len(got) != 0 {
		t.Errorf("cached_only checks for a client-routed version: %+v", got)
	}
}

func TestUnknownVersion(t *testing.T) {
	t.Parallel()
	if _, err := Run(context.Background(), Options{Policy: examplePolicy(t), Version: "nope"}); err == nil {
		t.Fatal("no error for an unknown version")
	}
}

func TestSeedboxVersionNeedsClientLogin(t *testing.T) {
	t.Parallel()
	pol := examplePolicy(t)
	v := pol.Versions["movie-web"]
	v.Storage, v.Client, v.Providers = policy.StorageSeedbox, "seedbox", nil
	r := run(t, Options{Policy: pol, Version: "movie-web"})
	want(t, r, SecSecrets, "THEATARR_SEEDBOX_QBIT_USER", Fail, "not set (client seedbox)")
	if len(find(r, SecSecrets, "client seedbox")) != 0 {
		t.Error("seedbox noted as unused by a seedbox version")
	}
}

func TestProviderExpiry(t *testing.T) {
	t.Parallel()
	pol := examplePolicy(t)
	r := run(t, Options{Policy: pol, Version: "movie-web", Env: allEnv, Now: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC),
		ProviderExpiry: map[string]time.Time{"tb": time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC),
			"rd": time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)}})
	want(t, r, SecPolicy, "provider plan", Warn, "tb's plan ends 2026-11-13 (18 days)")
	want(t, r, SecPolicy, "provider plan", Pass, "rd's plan runs to 2027-06-01")
	r = run(t, Options{Policy: pol, Version: "movie-web", Env: allEnv, Now: time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC),
		ProviderExpiry: map[string]time.Time{"tb": time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)}})
	want(t, r, SecPolicy, "provider plan", Warn, "tb's plan ended 2026-11-13")
	if r.Failed() && len(find(r, SecPolicy, "provider plan")) > 0 && find(r, SecPolicy, "provider plan")[0].Status == Fail {
		t.Error("an expired plan failed the check")
	}
}

func (f fakeFS) Readable(string) error { return nil }

// An incomplete setup (SetupCheck's notice) is a WARN for going live, with the way to finish it; a complete one passes.
func TestSetupCheckIncompleteWarns(t *testing.T) {
	t.Parallel()
	pol := examplePolicy(t)
	incomplete := health.Result{Source: "SetupCheck", Type: health.Notice,
		Message: "Setup incomplete: no enabled indexer (a Torznab or DMM one also needs its key): nothing can be searched"}
	r := run(t, Options{Policy: pol, Version: "movie-remux", Health: append(okHealth(), incomplete)})
	want(t, r, SecHealth, "SetupCheck", Warn, "Setup incomplete: no enabled indexer")
	if got := find(r, SecHealth, "SetupCheck"); len(got) != 1 || !strings.Contains(got[0].Fix, "preflight") {
		t.Fatalf("SetupCheck = %+v", got)
	}
	complete := health.Result{Source: "SetupCheck", Type: health.OK, Message: "configured"}
	r = run(t, Options{Policy: pol, Version: "movie-remux", Health: append(okHealth(), complete)})
	want(t, r, SecHealth, "SetupCheck", Pass, "configured")
}

// The VPN egress line appears once: the health results' VpnEgressCheck is left to the egress check.
func TestVpnEgressCheckOnce(t *testing.T) {
	t.Parallel()
	pol := examplePolicy(t)
	pol.Egress = nil
	r := run(t, Options{Policy: pol, Version: "movie-remux", Health: append(okHealth(),
		health.Result{Source: "VpnEgressCheck", Type: health.Warning, Message: "VPN guard off"})})
	if got := find(r, SecHealth, "VpnEgressCheck"); len(got) != 1 {
		t.Fatalf("VpnEgressCheck lines: %+v", got)
	}
}
