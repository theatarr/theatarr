package migration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/arr"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// Invented keys: the fake stack checks them, and no output may hold one.
const (
	radarrKey   = "radarr-key-0123456789abcdef"
	sonarrKey   = "sonarr-key-fedcba9876543210"
	prowlarrKey = "prowlarr-key-aaaabbbbccccdddd"
	seerrKey    = "seerr-key-1111222233334444"
	torboxKey   = "torbox-key-5555666677778888"
)

// secretValues are every credential the fixtures hold, the services' own and those they hand back.
var secretValues = []string{radarrKey, sonarrKey, prowlarrKey, seerrKey, torboxKey, "old-radarr-key-99999999",
	"lidarr-key-notinspected", "cf_clearance=abc123def456", "plex-token-zzzz9999yyyy8888", "discord-hook-token-abcabcabc",
	"hookpass-4242", "webhook-key-31313131", "list-key-7070707070", "plex-rss-secret-5a5a5a5a", "trakt-token-6161616161",
	"sigpass-8484", "sigUser", "mailpass-2323"}

var testNow = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func testKeys() Keys {
	return MapKeys(map[string]string{"RADARR_UHD_KEY": radarrKey, "SONARR_ANIME_KEY": sonarrKey,
		"PROWLARR_KEY": prowlarrKey, "SEERR_KEY": seerrKey, "TB_KEY": torboxKey})
}

// fakeStack serves recorded answers of two arrs, Prowlarr and Seerr under one address: /radarr_uhd, /sonarr_anime,
// /prowlarr and /seerr. The answers are testdata/stack/<service>/<endpoint>.json; the arrs' profiles, formats and
// naming are the decide and naming fixtures of the instances they stand for.
type fakeStack struct {
	t   *testing.T
	srv *httptest.Server
	mu  sync.Mutex
	// seen is every request, "METHOD /path".
	seen []string
	// override replaces one answer by "service/endpoint".
	override map[string]string
}

var stackKeys = map[string]string{"radarr_uhd": radarrKey, "sonarr_anime": sonarrKey, "prowlarr": prowlarrKey,
	"seerr": seerrKey}

var stackSnapshots = map[string]string{
	"radarr_uhd":   filepath.Join("..", "decide", "testdata", "arr", "radarr_4k_debrid", "snapshot"),
	"sonarr_anime": filepath.Join("..", "decide", "testdata", "packs", "arr", "sonarr_anime_debrid", "snapshot"),
}

var stackNaming = map[string]string{
	"radarr_uhd":   filepath.Join("..", "library", "naming", "testdata", "golden", "radarr_4k_debrid.json"),
	"sonarr_anime": filepath.Join("..", "library", "naming", "testdata", "golden", "sonarr_anime_debrid.json"),
}

func newFakeStack(t *testing.T) *fakeStack {
	t.Helper()
	fs := &fakeStack{t: t, override: map[string]string{}}
	fs.srv = httptest.NewServer(http.HandlerFunc(fs.serve))
	t.Cleanup(fs.srv.Close)
	return fs
}

func (fs *fakeStack) serve(w http.ResponseWriter, r *http.Request) {
	fs.mu.Lock()
	fs.seen = append(fs.seen, r.Method+" "+r.URL.Path)
	fs.mu.Unlock()
	if r.Method != http.MethodGet {
		fs.t.Errorf("the stack got %s %s: the old services must never be written", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 4)
	if len(parts) != 4 || parts[1] != "api" {
		http.NotFound(w, r)
		return
	}
	svc, ep := parts[0], parts[3]
	if key, ok := stackKeys[svc]; !ok || r.Header.Get("X-Api-Key") != key {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	body, err := fs.answer(svc, ep)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	u, _ := url.Parse(fs.srv.URL)
	body = strings.NewReplacer("{{BASE}}", fs.srv.URL, "{{HOST}}", u.Hostname(), "{{PORT}}", u.Port()).Replace(body)
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, body)
}

func (fs *fakeStack) answer(svc, ep string) (string, error) {
	if b, ok := fs.override[svc+"/"+ep]; ok {
		return b, nil
	}
	switch ep {
	case "qualityprofile", "customformat", "qualitydefinition":
		b, err := os.ReadFile(filepath.Join(stackSnapshots[svc], ep+".json"))
		return string(b), err
	case "config/naming":
		b, err := os.ReadFile(stackNaming[svc])
		if err != nil {
			return "", err
		}
		var g struct {
			Naming json.RawMessage `json:"naming"`
		}
		err = json.Unmarshal(b, &g)
		return string(g.Naming), err
	}
	b, err := os.ReadFile(filepath.Join("testdata", "stack", svc, strings.ReplaceAll(ep, "/", "_")+".json"))
	return string(b), err
}

func (fs *fakeStack) requests() []string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]string(nil), fs.seen...)
}

func (fs *fakeStack) sources() Sources {
	return Sources{
		Arr: []ArrSource{
			{Name: "radarr_uhd", Flavour: "radarr", URL: fs.srv.URL + "/radarr_uhd", Key: "RADARR_UHD_KEY"},
			{Name: "sonarr_anime", Flavour: "sonarr", URL: fs.srv.URL + "/sonarr_anime/", Key: "SONARR_ANIME_KEY"},
		},
		Prowlarr:  &ServiceSource{URL: fs.srv.URL + "/prowlarr", Key: "PROWLARR_KEY"},
		Seerr:     &ServiceSource{URL: fs.srv.URL + "/seerr", Key: "SEERR_KEY"},
		Recyclarr: filepath.Join("testdata", "stack", "recyclarr.yml"),
	}
}

// testDialer is the default dialer without the request spacing.
var testDialer = NewDialer(arr.Options{Timeout: 5 * time.Second, MinInterval: time.Millisecond, Retries: 1,
	Backoff: time.Millisecond})

func (fs *fakeStack) inspect(t *testing.T) *Inventory {
	t.Helper()
	inv, err := Inspect(context.Background(), fs.sources(), testKeys(), testDialer, testNow)
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

// assertNoSecrets fails when any credential of the fixtures is in b.
func assertNoSecrets(t *testing.T, what string, b []byte) {
	t.Helper()
	for _, s := range secretValues {
		if bytes.Contains(b, []byte(s)) {
			t.Errorf("%s holds the credential %q", what, s)
		}
	}
}

// settingsEnv is a store with the example policy bootstrapped into settings and a coordinator whose prepare builds
// nothing, as the web settings tests use.
type settingsEnv struct {
	st    *store.Store
	coord *settings.RuntimeCoordinator
	dir   string
	media string
}

func newSettingsEnv(t *testing.T) *settingsEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Load(filepath.Join("..", "..", "examples", "policy"))
	if err != nil {
		t.Fatal(err)
	}
	media := t.TempDir()
	pol.Library.Dangling.Roots = []string{media}
	ss, err := settings.New(st)
	if err != nil {
		t.Fatal(err)
	}
	ss.SetTrashRoot("../settings/testdata/trash")
	lookup := func(name string) (string, bool) { return "fixture-" + name, true }
	if _, err := ss.Bootstrap(ctx, pol, settings.ImportRequest{Dir: pol.Dir, Actor: "test", Lookup: lookup}); err != nil {
		t.Fatal(err)
	}
	snap, err := ss.BootstrapQuality(ctx, pol, "test")
	if err != nil {
		t.Fatal(err)
	}
	coord, err := settings.NewRuntimeCoordinator(ss, pol, snap, struct{}{},
		func(context.Context, *settings.Snapshot, *settings.Runtime) (any, error) { return struct{}{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	return &settingsEnv{st: st, coord: coord, dir: t.TempDir(), media: media}
}

// mkdirs creates the version libraries the plan maps to.
func (se *settingsEnv) mkdirs(t *testing.T) {
	t.Helper()
	for _, d := range []string{"movies-uhd", "anime"} {
		if err := os.MkdirAll(filepath.Join(se.media, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func (se *settingsEnv) head(t *testing.T) *settings.Snapshot {
	t.Helper()
	snap, err := se.coord.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// checkpointer writes the settings export, as serve's checkpoint does next to its backup set, and counts calls.
func (se *settingsEnv) checkpointer(calls *int) Checkpointer {
	return func(ctx context.Context) (Checkpoint, error) {
		*calls++
		snap, err := se.coord.Snapshot(ctx)
		if err != nil {
			return Checkpoint{}, err
		}
		b, err := snap.Export()
		if err != nil {
			return Checkpoint{}, err
		}
		base := filepath.Join(se.dir, "theatarr-checkpoint")
		if err := os.WriteFile(base+".settings.yaml", b, 0o600); err != nil {
			return Checkpoint{}, err
		}
		return Checkpoint{At: testNow, Backup: base, Settings: base + ".settings.yaml", Generation: snap.Generation}, nil
	}
}

// testOptions map the arrs' roots under media, where Theatarr sees them.
func testOptions(media string) PlanOptions {
	return PlanOptions{Name: "test", ProwlarrURL: "http://prowlarr:9696",
		Providers: []ProviderChoice{{Kind: "realdebrid", Secret: "RD_KEY"}, {Kind: "torbox", Key: "tb-migrated", Secret: "TB_KEY"}},
		PathMap:   []PathMapping{{From: "/media/", To: media}, {From: "/data/anime", To: filepath.Join(media, "anime")}}}
}

// plan inspects the stack and saves a plan built against the settings' policy. The libraries are under se.media,
// which mkdirs creates.
func (se *settingsEnv) plan(t *testing.T, fs *fakeStack) (*Plan, *Inventory) {
	t.Helper()
	inv := fs.inspect(t)
	p, err := Build(BuildInput{Inventory: inv, Options: testOptions(se.media), Policy: se.head(t).Policy, Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(context.Background(), se.st, p, inv); err != nil {
		t.Fatal(err)
	}
	return p, inv
}
