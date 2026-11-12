package arrview

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/acquire"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/notify"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/router/routertest"
)

// devEnv is the views with the decision endpoints wired: movie-web and tv-2160p active, the router switched on over
// fake adders, an acquirer whose search returns two releases per call (one accepted, one rejected), a decider that
// decides by name, a fake library and a recording notifier.
type devEnv struct {
	*env
	q      *jobs.Queue
	w      *jobs.Worker
	rt     *routertest.Fake
	lib    *fakeLibrary
	events *recorder
	mu     sync.Mutex
	calls  []string
	// cutoff is the acquirer, as theatarr serve wires it (judges the quality cutoff only with an engine).
	cutoff Cutoff
}

type recorder struct {
	mu     sync.Mutex
	events []notify.Event
	tests  []string
}

func (r *recorder) Emit(_ context.Context, ev notify.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recorder) TestNotification(_ context.Context, view string, n json.RawMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tests = append(r.tests, view+" "+string(n))
	return nil
}

// fakeLibrary answers through a real gate: Unlink and Import are recorded, and applied only when the gate allows.
type fakeLibrary struct {
	gate     *library.Gate
	unlinks  []string
	imports  []string
	onUnlink func(int64)
	onCheck  func() // runs in each CheckUnlink
	plan     func(id int64) *library.Plan
	// during runs while a plan is made (Import and PlanNew); mountDown makes the guarded reads fail as a dead
	// mount does.
	during    func()
	mountDown bool
}

func (f *fakeLibrary) Gate() *library.Gate { return f.gate }

func (f *fakeLibrary) CheckUnlink(version, path string) error {
	if f.onCheck != nil {
		f.onCheck()
	}
	return f.gate.CheckPath(version, path)
}

func (f *fakeLibrary) Unlink(_ context.Context, version, p string, linkID int64, _ string) (*library.UnlinkResult, error) {
	res := &library.UnlinkResult{Version: version, Path: p}
	if err := f.gate.CheckVersion(version); err != nil {
		res.DryRun, res.Refused = true, err.Error()
		return res, nil
	}
	f.unlinks = append(f.unlinks, version+" "+p)
	if linkID > 0 && f.onUnlink != nil {
		f.onUnlink(linkID)
	}
	return res, nil
}

func (f *fakeLibrary) Import(_ context.Context, id int64, dry bool) (*library.Plan, error) {
	if f.during != nil {
		f.during()
	}
	f.imports = append(f.imports, strings.TrimSpace(strings.Join([]string{itoa(id), map[bool]string{true: "dry"}[dry]}, " ")))
	p := &library.Plan{DownloadID: id, DryRun: dry}
	if f.plan != nil {
		p = f.plan(id)
		p.DryRun = p.DryRun || dry // a plan the gate made a dry run stays one
	}
	return p, nil
}

// fakeMount is where the fake library's mount is: /mnt/debrid with releases under __all__.
var fakeMount = &library.Mount{Path: "/mnt/debrid", AllDir: "__all__"}

func (f *fakeLibrary) Locate(ctx context.Context, p string) (library.Located, error) {
	return fakeMount.Locate(ctx, p)
}

// PlanNew records a dry run of a new import as "new dry": no download row exists for it.
func (f *fakeLibrary) PlanNew(_ context.Context, n library.NewImport) (*library.Plan, error) {
	if f.during != nil {
		f.during()
	}
	f.imports = append(f.imports, "new dry")
	return &library.Plan{Release: n.ReleaseTitle, Version: n.Version, DryRun: true}, nil
}

func (f *fakeLibrary) ReadDir(ctx context.Context, dir string) ([]os.DirEntry, error) {
	if f.mountDown {
		return nil, library.ErrMountDown
	}
	return fakeMount.ReadDir(ctx, dir)
}

func (f *fakeLibrary) Stat(ctx context.Context, p string) (os.FileInfo, error) {
	if f.mountDown {
		return nil, library.ErrMountDown
	}
	return fakeMount.Stat(ctx, p)
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

// devDecider wraps the acquirer (Interactive: the fake search) and decides single releases by name: accepted when
// the release is 2160p and from an indexer the version lists, else rejected.
type devDecider struct {
	*acquire.Acquirer
	e *devEnv
}

func (d devDecider) Decide(ctx context.Context, rel decide.Release, titleID int64, version string) (*decide.Decision,
	error) {
	return d.e.decideOne(rel.Release, titleID, version), nil
}

func (e *devEnv) decideOne(rel indexer.Release, titleID int64, version string) *decide.Decision {
	var vid int64
	_ = e.st.Reader().QueryRow(`SELECT id FROM version WHERE title_id = ? AND policy_key = ?`, titleID, version).Scan(&vid)
	d := &decide.Decision{Release: rel.Title, Indexer: rel.Indexer, TitleID: titleID, Version: version, VersionID: vid,
		Parsed: true, Quality: "WEBDL-2160p", Revision: profile.Revision{Version: 1}, ReleaseGroup: "GRP",
		Formats: []decide.FormatScore{{Name: "x265", Score: 50}}, Score: 50, Languages: []string{"English"},
		MovieTitles: []string{"Alpha"}}
	switch {
	case e.pol.Versions[version] != nil && rel.Indexer != policy.ManualIndexer &&
		!slices.Contains(e.pol.Versions[version].Indexers, rel.Indexer):
		d.Rejections = []decide.Rejection{{Reason: "IndexerNotInVersion", Ours: true,
			Message: "Indexer " + rel.Indexer + " is not one of version " + version + "'s indexers"}}
	case !strings.Contains(rel.Title, "2160p"):
		d.Quality = "WEBDL-1080p"
		d.Rejections = []decide.Rejection{{Reason: "QualityNotWanted", Message: "WEBDL-1080p is not wanted in profile"}}
	}
	if strings.Contains(rel.Title, ".S01E02.") {
		d.EpisodeIDs = []int64{102}
		s := 1
		d.Season, d.Numbers, d.ReleaseType, d.SeriesTitle = &s, []int{2}, "singleEpisode", "The Show"
	}
	d.Accepted = len(d.Rejections) == 0
	return d
}

func newDevEnv(t *testing.T) *devEnv { return newDevEnvWith(t, true) }

// newDevEnvWith builds the dev environment; without keys the router has no adder at all, as when no provider of
// the policy has its API key set, so every route fails before a provider takes it.
func newDevEnvWith(t *testing.T, keys bool) *devEnv { return buildDevEnv(t, keys, nil) }

// buildDevEnv builds the dev environment; engine, when set, gives the acquirer a decision engine over the policy
// (after the versions are switched active).
func buildDevEnv(t *testing.T, keys bool, engine func(*policy.Policy) *decide.Engine) *devEnv {
	e := &devEnv{env: newEnv(t), events: &recorder{}}
	e.pol.Versions["movie-web"].Mode = policy.ModeActive
	e.pol.Versions["tv-2160p"].Mode = policy.ModeActive
	e.pol.Router = &policy.Router{Enabled: true}
	var eng *decide.Engine
	if engine != nil {
		eng = engine(e.pol)
	}
	now := func() time.Time { return e.now }
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e.q = jobs.NewQueue(e.st)
	e.q.SetClock(now)
	e.w = jobs.NewWorker(e.q, jobs.WorkerOptions{Logger: log})
	var err error
	if keys {
		if e.rt, err = routertest.New(e.st, e.pol, e.q, e.w, now); err != nil {
			t.Fatal(err)
		}
	} else {
		r, err := router.New(router.Config{Store: e.st, Policy: e.pol, Providers: map[string]provider.Provider{},
			Logger: log, Now: now})
		if err != nil {
			t.Fatal(err)
		}
		r.RegisterJobs(e.w, e.q)
		e.rt = &routertest.Fake{Router: r, Adders: map[string]*routertest.Adder{}}
	}
	a, err := acquire.New(acquire.Config{Store: e.st, Policy: e.pol, Queue: e.q, Router: e.rt, Search: e.search,
		Engine: eng, Logger: log, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	a.RegisterJobs(e.w)
	e.searcher, e.cutoff = a, a
	e.lib = &fakeLibrary{gate: library.NewGate(e.pol), onUnlink: func(id int64) {
		e.exec(`UPDATE link SET state = 'unlinked' WHERE id = ?`, id)
	}}
	e.startDev(devDecider{Acquirer: a, e: e})
	return e
}

func (e *devEnv) startDev(dec Decider) {
	e.t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	envs := map[string]string{"THEATARR_VIEW_RADARR_KEY": radarrViewKey, "THEATARR_VIEW_SONARR_KEY": sonarrViewKey}
	cache := NewReleaseCache(func() time.Time { return e.now })
	if e.svc != nil && e.svc.releases != nil {
		cache = e.svc.releases
	}
	svc, err := New(context.Background(), Config{Store: e.st, Auth: e.au, Policy: e.pol, Logger: log,
		Now: func() time.Time { return e.now }, Getenv: func(k string) string { return envs[k] },
		Started: time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC), Searcher: e.searcher, Decider: dec, Cutoff: e.cutoff,
		Router: e.rt, Library: e.lib, Notifier: e.events, ReleaseCache: cache})
	if err != nil {
		e.t.Fatal(err)
	}
	e.svc = svc
	e.mux = newMux(svc)
}

// search is the fake interactive search: an accepted public 2160p release and a rejected 1080p one, each decided
// for the version.
func (e *devEnv) search(_ context.Context, t decide.Title, v decide.Version, _ *decide.ExistingFile,
	ss *decide.SeriesSearch) (*router.Searched, error) {
	e.mu.Lock()
	call := v.Key + " " + t.Name
	if ss != nil {
		call += " s" + itoa(int64(ss.Season)) + "e" + itoa(int64(ss.Episode))
	}
	e.calls = append(e.calls, call)
	e.mu.Unlock()
	name := strings.ReplaceAll(t.Name, " ", ".")
	if ss != nil && ss.Episode > 0 {
		name += ".S01E02"
	}
	good := indexer.Release{Indexer: "prowlarr-public", Kind: "torznab", Privacy: policy.PrivacyPublic,
		Title: name + ".2160p.WEB-DL.H265-GRP", InfoHash: strings.Repeat("9", 40), Size: 10 << 30, Seeders: 12,
		Peers: 15, PublishDate: e.now.Add(-26 * time.Hour), Extras: map[string]string{"downloadvolumefactor": "0"}}
	bad := indexer.Release{Indexer: "dmm", Kind: "dmm", Privacy: policy.PrivacyPublic,
		Title: name + ".1080p.WEB-DL-GRP", InfoHash: strings.Repeat("8", 40), Size: 4 << 30, Seeders: -1, Peers: -1,
		Extras: map[string]string{"cached_on": "rd,ad"}}
	out := &router.Searched{Releases: map[*decide.Decision]indexer.Release{}}
	for _, rel := range []indexer.Release{good, bad} {
		d := e.decideOne(rel, t.ID, v.Key)
		out.Ranked = append(out.Ranked, d)
		out.Releases[d] = rel
	}
	out.Request = router.Request{TitleID: t.ID, VersionID: v.ID, Version: v.Key,
		Candidates: router.Candidates(out.Ranked, out.Releases)}
	return out, nil
}

func (e *devEnv) drain() {
	e.t.Helper()
	for range 100 {
		ran, err := e.w.RunOne(context.Background())
		if err != nil {
			e.t.Fatal(err)
		}
		if !ran {
			return
		}
	}
	e.t.Fatal("jobs never drained")
}

func newMux(svc *Service) *http.ServeMux {
	mux := http.NewServeMux()
	svc.Mount(mux)
	return mux
}

// adds are the hashes added on every provider, in provider order.
func (e *devEnv) adds() []string {
	var out []string
	for _, k := range sortedKeys(e.rt.Adders) {
		out = append(out, e.rt.Adders[k].Adds()...)
	}
	return out
}
