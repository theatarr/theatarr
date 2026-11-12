package settings

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGitHub serves the two requests a tracked source makes: the commits API (sha media type) and the codeload
// archive of one commit. Each commit's archive is the TRaSH tree as it was when the commit was published.
type fakeGitHub struct {
	server   *httptest.Server
	mu       sync.Mutex
	head     string
	archives map[string][]byte
	lookups  int
	fetches  int
	failHead bool
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{archives: map[string][]byte{}}
	g.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/repos/TRaSH-Guides/Guides/commits/master"):
			g.lookups++
			if g.failHead || r.Header.Get("Accept") != "application/vnd.github.sha" {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Write([]byte(g.head))
		case strings.HasPrefix(r.URL.Path, "/codeload/TRaSH-Guides/Guides/tar.gz/"):
			g.fetches++
			b, ok := g.archives[strings.TrimPrefix(r.URL.Path, "/codeload/TRaSH-Guides/Guides/tar.gz/")]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Write(b)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(g.server.Close)
	oldAPI, oldLoad := githubAPI, githubCodeload
	githubAPI, githubCodeload = g.server.URL+"/api", g.server.URL+"/codeload"
	t.Cleanup(func() { githubAPI, githubCodeload = oldAPI, oldLoad })
	return g
}

// publish makes root's current tree the branch's newest commit.
func (g *fakeGitHub) publish(t *testing.T, root, commit string) {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": commit}}); err != nil {
		t.Fatal(err)
	}
	if err := fs.WalkDir(os.DirFS(root), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: "Guides-" + commit[:7] + "/" + path, Mode: 0644, Size: int64(len(raw))}); err != nil {
			return err
		}
		_, err = tw.Write(raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.head, g.archives[commit] = commit, b.Bytes()
}

func (g *fakeGitHub) get(ctx context.Context, rawURL string, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	res, err := g.server.Client().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("fake github: " + res.Status)
	}
	return io.ReadAll(res.Body)
}

func (g *fakeGitHub) counts() (int, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lookups, g.fetches
}

// trackedService is syncService with its TRaSH tree served by a fake GitHub, and one enabled tracked source saved
// with the given config (defaults filled in by the save).
func trackedService(t *testing.T, config QualitySyncConfig) (*QualitySync, *fakeGitHub, func() QualitySyncSource, *time.Time) {
	t.Helper()
	ctx := context.Background()
	svc, base, initial := syncService(t)
	gh := newFakeGitHub(t)
	svc.FetchCommit = func(ctx context.Context, c QualitySyncConfig) (string, error) {
		b, err := gh.get(ctx, TrackedCommitURL(c), "application/vnd.github.sha")
		return string(b), err
	}
	svc.FetchArchive = func(ctx context.Context, c QualitySyncConfig) ([]byte, error) {
		if c.ArchiveURL != TrackedArchiveURL(c, c.Commit) {
			return nil, errors.New("archive not pinned to the resolved commit")
		}
		return gh.get(ctx, c.ArchiveURL, "")
	}
	// The local tree is no longer read: only the archives are.
	tree := svc.TrashRoot
	svc.TrashRoot = t.TempDir()
	gh.publish(t, tree, strings.Repeat("a", 40))
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	svc.Store.SetClock(func() time.Time { return now })
	req := saveRequest(base, initial)
	config.Origin, config.Enabled = OriginGitHub, true
	req.Records.SyncSources = []QualitySyncSource{{Record: Record{Key: "trash-guides"}, Config: config}}
	if _, err := svc.Coordinator.SaveAndApply(ctx, req); err != nil {
		t.Fatal(err)
	}
	source := func() QualitySyncSource {
		snap, err := svc.Coordinator.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return snap.Records.SyncSources[0]
	}
	t.Cleanup(func() { svc.TrashRoot = tree })
	return svc, gh, source, &now
}

func lastRunOf(t *testing.T, svc *QualitySync) QualitySyncRun {
	t.Helper()
	runs, err := svc.Runs(context.Background())
	if err != nil || len(runs) == 0 {
		t.Fatal("no run recorded", err)
	}
	return runs[0]
}

func TestQualitySyncTrackedDefaultsAndUnchangedCommit(t *testing.T) {
	ctx := context.Background()
	svc, gh, source, now := trackedService(t, QualitySyncConfig{Kind: "trash", Flavour: "radarr"})
	c := source().Config
	if c.Schedule != "168h" || c.Repo != "TRaSH-Guides/Guides" || c.Branch != "master" {
		t.Fatalf("tracked defaults: %+v", c)
	}
	gen := svc.Coordinator.Current().Generation
	if err := svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if r := lastRunOf(t, svc); r.Outcome != "unchanged" || r.Commit != strings.Repeat("a", 40) {
		t.Fatalf("first run: %+v", r)
	}
	if lookups, fetches := gh.counts(); lookups != 1 || fetches != 1 {
		t.Fatalf("first run requests: %d lookups, %d fetches", lookups, fetches)
	}
	// Weekly: a day later nothing is asked.
	*now = now.Add(24 * time.Hour)
	if err := svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if lookups, _ := gh.counts(); lookups != 1 {
		t.Fatal("ran before the weekly schedule")
	}
	// A week later the branch has not moved: one lookup, no archive, no log entry, no generation.
	*now = now.Add(7 * 24 * time.Hour)
	if err := svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	runs, _ := svc.Runs(ctx)
	if lookups, fetches := gh.counts(); lookups != 2 || fetches != 1 || len(runs) != 2 || runs[0].Outcome != "unchanged" || runs[0].Commit != strings.Repeat("a", 40) {
		t.Fatalf("unchanged commit fetched or recorded wrongly: %d lookups %d fetches %+v", lookups, fetches, runs)
	}
	if changes, _ := svc.Changes(ctx, 50); len(changes) != 0 || svc.Coordinator.Current().Generation != gen {
		t.Fatal("unchanged commit created a log entry or generation")
	}
	// A manual run (force) skips the schedule, not the commit check.
	if err := svc.RunSource(ctx, source(), true); err != nil {
		t.Fatal(err)
	}
	if _, fetches := gh.counts(); fetches != 1 {
		t.Fatal("manual run refetched an unchanged commit")
	}
	// A config change (here the schedule) previews the same commit again.
	snap, _ := svc.Coordinator.Snapshot(ctx)
	req := SaveRequest{ExpectedGeneration: snap.Generation, Actor: "admin", Records: snap.Records, Base: svc.Coordinator.base}
	req.Records.SyncSources[0].Config.Schedule = "336h"
	if _, err := svc.Coordinator.SaveAndApply(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := svc.RunSource(ctx, source(), true); err != nil {
		t.Fatal(err)
	}
	if _, fetches := gh.counts(); fetches != 2 {
		t.Fatal("config change did not preview again")
	}
}

func TestQualitySyncTrackedAppliesDefinitionChanges(t *testing.T) {
	ctx := context.Background()
	svc, gh, source, now := trackedService(t, QualitySyncConfig{Kind: "trash", Flavour: "radarr"})
	tree := t.TempDir()
	copyTree(t, tree)
	if err := svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	gen := svc.Coordinator.Current().Generation
	editUpstream(t, tree, syncFormatID, func(f map[string]any) {
		specs := f["specifications"].([]any)
		specs[1].(map[string]any)["fields"] = map[string]any{"value": "^(3L|NEW)$"}
	})
	commit := strings.Repeat("b", 40)
	gh.publish(t, tree, commit)
	*now = now.Add(7 * 24 * time.Hour)
	if err := svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	r := lastRunOf(t, svc)
	if r.Outcome != "applied" || r.Commit != commit || svc.Coordinator.Current().Generation != gen+1 {
		t.Fatalf("spec-only change not applied: %+v", r)
	}
	snap, _ := svc.Coordinator.Snapshot(ctx)
	found := false
	for _, f := range snap.Records.Formats {
		if f.Key == "Remux Tier 01" && strings.Contains(string(f.Specification), "3L|NEW") {
			found = true
		}
	}
	if !found {
		t.Fatal("applied settings lack the new specification")
	}
	actor := false
	for _, a := range audit(t, svc.Store) {
		if a.Operation == "quality-import" && a.Actor == "sync:trash-guides" {
			actor = true
		}
	}
	if !actor {
		t.Fatal("auto-apply not audited as the source")
	}
	if changes, _ := svc.Changes(ctx, 50); len(changes) != 1 || changes[0].ID != *r.ReviewID || len(changes[0].Changed) != 1 || pendingReviews(t, svc) != 0 {
		t.Fatalf("update log: %+v", changes)
	}
	if states, err := svc.SourceStates(ctx); err != nil || len(states) != 1 || states[0].LastCommit != commit || source().Key != states[0].Source {
		t.Fatalf("source state: %+v %v", states, err)
	}
}

func TestQualitySyncTrackedKeepsScoresAppliesAddsSkipsRetirements(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		config QualitySyncConfig
		edit   func(t *testing.T, tree string)
		check  func(changes []QualityPreview) bool
		// applied is how many generations the run writes.
		applied int64
	}{
		// Our own score (2000, not TRaSH's 1950) is kept when TRaSH changes it, and the log notes it; the run's
		// specification change applies.
		{"score", QualitySyncConfig{Profiles: map[string]string{"remux-2160p": "default"}}, func(t *testing.T, tree string) {
			editUpstream(t, tree, syncFormatID, func(f map[string]any) {
				f["trash_scores"] = map[string]any{"default": 777}
				f["specifications"].([]any)[1].(map[string]any)["fields"] = map[string]any{"value": "^(3L|NEW)$"}
			})
		}, func(c []QualityPreview) bool {
			return len(c) >= 1 && len(c[0].Held) == 1 && c[0].Held[0].Trash == 777 && *c[0].Held[0].Ours == 2000 && *c[0].Held[0].Last == 1950 && len(c[0].Changed) == 1
		}, 1},
		// A retirement alone is not logged and writes nothing.
		{"retired", QualitySyncConfig{}, func(t *testing.T, tree string) {
			if err := os.Remove(filepath.Join(tree, "radarr", syncFormatID+".json")); err != nil {
				t.Fatal(err)
			}
		}, func(c []QualityPreview) bool { return len(c) == 0 }, 0},
		{"renamed", QualitySyncConfig{}, func(t *testing.T, tree string) {
			editUpstream(t, tree, syncFormatID, func(f map[string]any) { f["name"] = "Remux Tier One" })
		}, func(c []QualityPreview) bool { return len(c) == 0 }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.config.Kind, tc.config.Flavour = "trash", "radarr"
			svc, gh, _, now := trackedService(t, tc.config)
			tree := t.TempDir()
			copyTree(t, tree)
			if err := svc.RunDue(ctx); err != nil {
				t.Fatal(err)
			}
			before, _ := svc.Changes(ctx, 50)
			if tc.name == "score" {
				setScore(t, svc, "remux-2160p", "Remux Tier 01", 2000)
			}
			gen := svc.Coordinator.Current().Generation
			tc.edit(t, tree)
			gh.publish(t, tree, strings.Repeat("c", 40))
			*now = now.Add(7 * 24 * time.Hour)
			if err := svc.RunDue(ctx); err != nil {
				t.Fatal(err)
			}
			r := lastRunOf(t, svc)
			changes, _ := svc.Changes(ctx, 50)
			outcome := "applied"
			if tc.applied == 0 {
				outcome = "unchanged"
			}
			if r.Outcome != outcome || !tc.check(changes[:len(changes)-len(before)]) || svc.Coordinator.Current().Generation != gen+tc.applied || pendingReviews(t, svc) != 0 {
				t.Fatalf("run %+v log %+v", r, changes)
			}
		})
	}
	// A new upstream format named in the source's TRaSH IDs is added by the run.
	const newID = "0123456789abcdef0123456789abcdef"
	svc, gh, _, _ := trackedService(t, QualitySyncConfig{Kind: "trash", Flavour: "radarr", TrashIDs: []string{newID}})
	tree := t.TempDir()
	copyTree(t, tree)
	raw, err := os.ReadFile(filepath.Join(tree, "radarr", syncFormatID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.ReplaceAll(bytes.ReplaceAll(raw, []byte(syncFormatID), []byte(newID)), []byte("Remux Tier 01"), []byte("Remux Tier 99"))
	if err := os.WriteFile(filepath.Join(tree, "radarr", newID+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	gh.publish(t, tree, strings.Repeat("e", 40))
	gen := svc.Coordinator.Current().Generation
	if err := svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	changes, _ := svc.Changes(ctx, 50)
	if r := lastRunOf(t, svc); svc.Coordinator.Current().Generation != gen+1 || r.Outcome != "applied" || len(changes) != 1 || len(changes[0].Added) != 1 {
		t.Fatalf("added format not applied: %+v %+v", r, changes)
	}
}

func TestQualitySyncTrackedFetchFailureKeepsState(t *testing.T) {
	ctx := context.Background()
	svc, gh, source, now := trackedService(t, QualitySyncConfig{Kind: "trash", Flavour: "radarr"})
	tree := t.TempDir()
	copyTree(t, tree)
	if err := svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	gen := svc.Coordinator.Current().Generation
	// The commit lookup fails.
	gh.mu.Lock()
	gh.failHead = true
	gh.mu.Unlock()
	*now = now.Add(7 * 24 * time.Hour)
	if err := svc.RunDue(ctx); err == nil {
		t.Fatal("failed lookup reported no error")
	}
	r := lastRunOf(t, svc)
	if r.Outcome != "error" || r.Error == "" || svc.Coordinator.Current().Generation != gen {
		t.Fatalf("failed lookup: %+v", r)
	}
	states, err := svc.SourceStates(ctx)
	if err != nil || len(states) != 1 || states[0].LastRun.Outcome != "error" || states[0].LastCommit != strings.Repeat("a", 40) {
		t.Fatalf("state after failure: %+v %v", states, err)
	}
	// A failed tracked run is retried after 6h, not a week.
	gh.mu.Lock()
	gh.failHead = false
	gh.mu.Unlock()
	*now = now.Add(time.Hour)
	_ = svc.RunDue(ctx)
	if runs, _ := svc.Runs(ctx); len(runs) != 2 {
		t.Fatal("retried within the hour")
	}
	// The branch moves, but its archive cannot be fetched: the run fails and the settings stay.
	editUpstream(t, tree, syncFormatID, func(f map[string]any) {
		f["specifications"].([]any)[1].(map[string]any)["fields"] = map[string]any{"value": "^(3L|NEW)$"}
	})
	gh.publish(t, tree, strings.Repeat("d", 40))
	gh.mu.Lock()
	delete(gh.archives, strings.Repeat("d", 40))
	gh.mu.Unlock()
	*now = now.Add(6 * time.Hour)
	if err := svc.RunDue(ctx); err == nil {
		t.Fatal("failed archive reported no error")
	}
	if r := lastRunOf(t, svc); r.Outcome != "error" || svc.Coordinator.Current().Generation != gen {
		t.Fatalf("failed archive: %+v", r)
	}
	// Once the archive is there, the next run fetches it (a failure is never taken as processed) and applies.
	gh.publish(t, tree, strings.Repeat("d", 40))
	if err := svc.RunSource(ctx, source(), true); err != nil {
		t.Fatal(err)
	}
	if r := lastRunOf(t, svc); r.Outcome != "applied" || svc.Coordinator.Current().Generation != gen+1 {
		t.Fatalf("recovery: %+v", r)
	}
}

func TestQualitySyncTrackedConfigValidation(t *testing.T) {
	t.Parallel()
	for _, c := range []QualitySyncConfig{
		{Kind: "trash", Flavour: "radarr", Origin: "gitlab"},
		{Kind: "trash", Flavour: "radarr", Origin: OriginGitHub, Repo: "../x"},
		{Kind: "trash", Flavour: "radarr", Origin: OriginGitHub, Repo: "a/b/c"},
		{Kind: "trash", Flavour: "radarr", Origin: OriginGitHub, Branch: "a/../b"},
		{Kind: "trash", Flavour: "radarr", Origin: OriginGitHub, Branch: "x?y"},
		{Kind: "trash", Flavour: "radarr", Origin: OriginGitHub, ArchiveURL: "https://codeload.github.com/a/b/tar.gz/" + strings.Repeat("a", 40), Commit: strings.Repeat("a", 40)},
		{Kind: "trash", Flavour: "radarr", Repo: "a/b"},
	} {
		if err := c.validate(); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
	c := QualitySyncConfig{Kind: "trash", Flavour: "radarr"}
	if err := c.validate(); err != nil || c.Schedule != "24h" {
		t.Fatal("untracked defaults changed", c, err)
	}
	c = QualitySyncConfig{Kind: "trash", Flavour: "sonarr", Origin: OriginGitHub, Branch: "feature/x"}
	if err := c.validate(); err != nil || TrackedCommitURL(c) != githubAPI+"/repos/TRaSH-Guides/Guides/commits/feature/x" {
		t.Fatal("tracked branch", c, err)
	}
}

// setScore saves profile's score of format as an administrator would.
func setScore(t *testing.T, svc *QualitySync, profile, format string, score int) {
	t.Helper()
	snap, err := svc.Coordinator.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	req := SaveRequest{ExpectedGeneration: snap.Generation, Actor: "admin", Records: snap.Records, Base: svc.Coordinator.base}
	req.Records.Profiles = slices.Clone(req.Records.Profiles)
	for i := range req.Records.Profiles {
		if req.Records.Profiles[i].Key == profile {
			formats := maps.Clone(req.Records.Profiles[i].Config.Formats)
			formats[format] = score
			req.Records.Profiles[i].Config.Formats = formats
		}
	}
	if _, err := svc.Coordinator.SaveAndApply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
}

// copyTree copies the TRaSH fixture tree the fake GitHub serves into dir, for edits that become new commits.
func copyTree(t *testing.T, dir string) {
	t.Helper()
	src := syncTree(t)
	if err := os.CopyFS(dir, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
}

// An apply that fails (here the runtime cannot prepare the new generation) leaves the settings and the log as they
// were; the next run applies.
func TestQualitySyncFailedApplyKeepsState(t *testing.T) {
	ctx := context.Background()
	svc, gh, _, now := trackedService(t, QualitySyncConfig{Kind: "trash", Flavour: "radarr"})
	tree := t.TempDir()
	copyTree(t, tree)
	if err := svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	gen := svc.Coordinator.Current().Generation
	editUpstream(t, tree, syncFormatID, func(f map[string]any) {
		f["specifications"].([]any)[1].(map[string]any)["fields"] = map[string]any{"value": "^(3L|NEW)$"}
	})
	gh.publish(t, tree, strings.Repeat("d", 40))
	prepare := svc.Coordinator.prepare
	svc.Coordinator.prepare = func(context.Context, *Snapshot, *Runtime) (any, error) {
		return nil, errors.New("fixture prepare failure")
	}
	*now = now.Add(7 * 24 * time.Hour)
	if err := svc.RunDue(ctx); err == nil {
		t.Fatal("failed apply reported no error")
	}
	changes, _ := svc.Changes(ctx, 50)
	if r := lastRunOf(t, svc); r.Outcome != "error" || !strings.HasPrefix(r.Error, "the update could not be applied: ") || len(changes) != 0 || svc.Coordinator.Current().Generation != gen {
		t.Fatalf("failed apply: run %+v, log %+v", r, changes)
	}
	svc.Coordinator.prepare = prepare
	*now = now.Add(7 * time.Hour)
	if err := svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if changes, _ := svc.Changes(ctx, 50); lastRunOf(t, svc).Outcome != "applied" || len(changes) != 1 || svc.Coordinator.Current().Generation != gen+1 {
		t.Fatalf("applied run: %+v %+v", lastRunOf(t, svc), changes)
	}
}

// A bound profile's score follows TRaSH while it still equals TRaSH's last score. A score you changed stays yours when
// TRaSH changes it: the run keeps it, remembers TRaSH's new score and the log notes it; the next complete run with the
// same TRaSH score leaves it alone and notes nothing. Setting the score back to TRaSH's makes it follow again.
// Following scores and definitions apply in the same run; unbound profiles never change.
func TestQualitySyncKeepsYourScore(t *testing.T) {
	ctx := context.Background()
	const tier01, tier02, web01 = syncFormatID, "9f98181fe5a3fbeb0cc29340da2a468a", "c20f169ef63c5f40c2def54abaf4438e"
	svc, gh, source, now := trackedService(t, QualitySyncConfig{Kind: "trash", Flavour: "radarr", Profiles: map[string]string{"remux-2160p": "default"}})
	tree := t.TempDir()
	copyTree(t, tree)
	scores := func(profile string) map[string]int { return svc.Coordinator.Current().Policy.Profiles[profile].Formats }
	remembered := func(format string) TrashScoreState {
		return source().Config.ScoreState["remux-2160p"].Scores[format]
	}
	week := func(commit string) (QualitySyncRun, []QualityPreview) {
		t.Helper()
		gh.publish(t, tree, strings.Repeat(commit, 40))
		*now = now.Add(7 * 24 * time.Hour)
		if err := svc.RunDue(ctx); err != nil {
			t.Fatal(err)
		}
		changes, err := svc.Changes(ctx, 50)
		if err != nil {
			t.Fatal(err)
		}
		return lastRunOf(t, svc), changes
	}
	keptOf := func(changes []QualityPreview, r QualitySyncRun) []HeldScore {
		t.Helper()
		if r.ReviewID == nil || len(changes) == 0 || changes[0].ID != *r.ReviewID {
			t.Fatalf("run %+v has no log entry: %+v", r, changes)
		}
		return changes[0].Held
	}

	// First run after binding takes TRaSH's scores and remembers them.
	if err := svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if st := source().Config.ScoreState["remux-2160p"]; st.Set != "default" || st.Scores["Remux Tier 01"] != (TrashScoreState{Trash: 1950}) || st.Scores["Remux Tier 02"] != (TrashScoreState{Trash: 1900}) {
		t.Fatalf("first run state: %+v", st)
	}
	setScore(t, svc, "remux-2160p", "Remux Tier 02", 1800) // yours; TRaSH says 1900

	// TRaSH changes three scores and a definition: the following scores and the definition apply, yours is kept and
	// noted, TRaSH's new score is remembered, and the unbound profile keeps its score.
	for id, score := range map[string]int{tier01: 1960, tier02: 1910, web01: 1750} {
		editUpstream(t, tree, id, func(f map[string]any) { f["trash_scores"] = map[string]any{"default": score} })
	}
	editUpstream(t, tree, tier01, func(f map[string]any) {
		f["specifications"].([]any)[1].(map[string]any)["fields"] = map[string]any{"value": "^(3L|NEW)$"}
	})
	gen := svc.Coordinator.Current().Generation
	r, changes := week("c")
	kept := keptOf(changes, r)
	if r.Outcome != "applied" || len(kept) != 1 || kept[0].Format != "Remux Tier 02" || *kept[0].Ours != 1800 || *kept[0].Last != 1900 || kept[0].Trash != 1910 || svc.Coordinator.Current().Generation != gen+1 {
		t.Fatalf("your score not kept: %+v %+v", r, kept)
	}
	if s := scores("remux-2160p"); s["Remux Tier 01"] != 1960 || s["WEB Tier 01"] != 1750 || s["Remux Tier 02"] != 1800 || scores("web-2160p")["WEB Tier 01"] != 1700 {
		t.Fatalf("scores after the run: %v / %v", s, scores("web-2160p"))
	}
	if remembered("Remux Tier 02") != (TrashScoreState{Trash: 1910}) {
		t.Fatalf("TRaSH's new score not remembered: %+v", remembered("Remux Tier 02"))
	}

	// The next complete run (a definition change, TRaSH's score as before) keeps yours without noting it again.
	editUpstream(t, tree, tier01, func(f map[string]any) {
		f["specifications"].([]any)[1].(map[string]any)["fields"] = map[string]any{"value": "^(3L|NEWER)$"}
	})
	gen = svc.Coordinator.Current().Generation
	r, changes = week("d")
	if r.Outcome != "applied" || len(keptOf(changes, r)) != 0 || len(changes[0].Changed) != 1 || svc.Coordinator.Current().Generation != gen+1 || scores("remux-2160p")["Remux Tier 02"] != 1800 || remembered("Remux Tier 02") != (TrashScoreState{Trash: 1910}) {
		t.Fatalf("second run: %+v %+v", r, changes[0])
	}

	// TRaSH changes it again: still yours, noted again, the newest score remembered.
	editUpstream(t, tree, tier02, func(f map[string]any) { f["trash_scores"] = map[string]any{"default": 1920} })
	r, changes = week("e")
	if kept := keptOf(changes, r); r.Outcome != "applied" || len(kept) != 1 || *kept[0].Last != 1910 || kept[0].Trash != 1920 || scores("remux-2160p")["Remux Tier 02"] != 1800 || remembered("Remux Tier 02") != (TrashScoreState{Trash: 1920}) {
		t.Fatalf("third run: %+v %+v", r, kept)
	}

	// Set back to TRaSH's score, it follows TRaSH again.
	setScore(t, svc, "remux-2160p", "Remux Tier 02", 1920)
	editUpstream(t, tree, tier02, func(f map[string]any) { f["trash_scores"] = map[string]any{"default": 1930} })
	r, changes = week("f")
	if r.Outcome != "applied" || len(keptOf(changes, r)) != 0 || scores("remux-2160p")["Remux Tier 02"] != 1930 || remembered("Remux Tier 02") != (TrashScoreState{Trash: 1930}) {
		t.Fatalf("score set back to TRaSH's does not follow: %+v %v", r, scores("remux-2160p"))
	}
	if pendingReviews(t, svc) != 0 {
		t.Fatal("a run left a pending review")
	}
}
