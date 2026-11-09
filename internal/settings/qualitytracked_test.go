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
	if c.Mode != ModeAutoDefinitions || c.Schedule != "168h" || c.Repo != "TRaSH-Guides/Guides" || c.Branch != "master" {
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
	// A week later the branch has not moved: one lookup, no archive, no review, no generation.
	*now = now.Add(7 * 24 * time.Hour)
	if err := svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	runs, _ := svc.Runs(ctx)
	if lookups, fetches := gh.counts(); lookups != 2 || fetches != 1 || len(runs) != 2 || runs[0].Outcome != "unchanged" || runs[0].Commit != strings.Repeat("a", 40) {
		t.Fatalf("unchanged commit fetched or recorded wrongly: %d lookups %d fetches %+v", lookups, fetches, runs)
	}
	if reviews, _ := svc.Reviews(ctx); len(reviews) != 0 || svc.Coordinator.Current().Generation != gen {
		t.Fatal("unchanged commit created a review or generation")
	}
	// A manual run (force) skips the schedule, not the commit check.
	if err := svc.RunSource(ctx, source(), true); err != nil {
		t.Fatal(err)
	}
	if _, fetches := gh.counts(); fetches != 1 {
		t.Fatal("manual run refetched an unchanged commit")
	}
	// A config change (here the mode) previews the same commit again.
	snap, _ := svc.Coordinator.Snapshot(ctx)
	req := SaveRequest{ExpectedGeneration: snap.Generation, Actor: "admin", Records: snap.Records, Base: svc.Coordinator.base}
	req.Records.SyncSources[0].Config.Mode = "review"
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
	if reviews, _ := svc.Reviews(ctx); len(reviews) != 0 {
		t.Fatal("applied run left a pending review")
	}
	if states, err := svc.SourceStates(ctx); err != nil || len(states) != 1 || states[0].LastCommit != commit || source().Key != states[0].Source {
		t.Fatalf("source state: %+v %v", states, err)
	}
}

func TestQualitySyncTrackedHoldsScoresAddsAndRetirements(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		config QualitySyncConfig
		edit   func(t *testing.T, tree string)
		check  func(p QualityPreview) bool
		// applied is how many generations the run writes for what it does not hold.
		applied int64
	}{
		// Our own score (2000, not TRaSH's 1950) is held when TRaSH changes it; the run's specification change applies.
		{"score", QualitySyncConfig{Profiles: map[string]string{"remux-2160p": "default"}}, func(t *testing.T, tree string) {
			editUpstream(t, tree, syncFormatID, func(f map[string]any) {
				f["trash_scores"] = map[string]any{"default": 777}
				f["specifications"].([]any)[1].(map[string]any)["fields"] = map[string]any{"value": "^(3L|NEW)$"}
			})
		}, func(p QualityPreview) bool {
			return p.Kind == ReviewScores && len(p.Held) == 1 && p.Held[0].Trash == 777 && *p.Held[0].Ours == 2000 && *p.Held[0].Last == 1950
		}, 1},
		{"retired", QualitySyncConfig{}, func(t *testing.T, tree string) {
			if err := os.Remove(filepath.Join(tree, "radarr", syncFormatID+".json")); err != nil {
				t.Fatal(err)
			}
		}, func(p QualityPreview) bool { return len(p.Retired) == 1 }, 0},
		{"renamed", QualitySyncConfig{}, func(t *testing.T, tree string) {
			editUpstream(t, tree, syncFormatID, func(f map[string]any) { f["name"] = "Remux Tier One" })
		}, func(p QualityPreview) bool { return len(p.Retired) == 1 }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.config.Kind, tc.config.Flavour = "trash", "radarr"
			svc, gh, _, now := trackedService(t, tc.config)
			tree := t.TempDir()
			copyTree(t, tree)
			if err := svc.RunDue(ctx); err != nil {
				t.Fatal(err)
			}
			// A first run that already differs (the bound score set) is approved as the starting point.
			if reviews, _ := svc.Reviews(ctx); len(reviews) == 1 {
				if _, err := svc.Apply(ctx, reviews[0].ID, reviews[0].Generation, "admin"); err != nil {
					t.Fatal(err)
				}
			}
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
			reviews, _ := svc.Reviews(ctx)
			if r.Outcome != "review" || len(reviews) != 1 || !tc.check(reviews[0]) || svc.Coordinator.Current().Generation != gen+tc.applied {
				t.Fatalf("not held for review: run %+v reviews %+v", r, reviews)
			}
		})
	}
	// A new upstream format named in the source's TRaSH IDs is held as well.
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
	reviews, _ := svc.Reviews(ctx)
	if r := lastRunOf(t, svc); svc.Coordinator.Current().Generation != gen || r.Outcome != "review" || len(reviews) != 1 || len(reviews[0].Added) != 1 {
		t.Fatalf("added format not held: %+v %+v", r, reviews)
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
		{Kind: "trash", Flavour: "radarr", Mode: "sometimes"},
	} {
		if err := c.validate(); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
	c := QualitySyncConfig{Kind: "trash", Flavour: "radarr"}
	if err := c.validate(); err != nil || c.Mode != "review" || c.Schedule != "24h" {
		t.Fatal("untracked defaults changed", c, err)
	}
	c = QualitySyncConfig{Kind: "trash", Flavour: "sonarr", Origin: OriginGitHub, Branch: "feature/x", Mode: "review"}
	if err := c.validate(); err != nil || c.Mode != "review" || TrackedCommitURL(c) != githubAPI+"/repos/TRaSH-Guides/Guides/commits/feature/x" {
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

// Review fix: an automatic apply that fails keeps the reviews already pending for the source (it supersedes them only
// when it lands), and leaves no stale review of its own.
func TestQualitySyncFailedAutoApplyKeepsPendingReview(t *testing.T) {
	ctx := context.Background()
	svc, gh, _, now := trackedService(t, QualitySyncConfig{Kind: "trash", Flavour: "radarr", Profiles: map[string]string{"remux-2160p": "default"}})
	tree := t.TempDir()
	copyTree(t, tree)
	if err := svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if reviews, _ := svc.Reviews(ctx); len(reviews) == 1 {
		if _, err := svc.Apply(ctx, reviews[0].ID, reviews[0].Generation, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	// Our own score: TRaSH's change of it is held.
	setScore(t, svc, "remux-2160p", "Remux Tier 01", 2000)
	var score any
	editUpstream(t, tree, syncFormatID, func(f map[string]any) {
		score = f["trash_scores"]
		f["trash_scores"] = map[string]any{"default": 777}
	})
	gh.publish(t, tree, strings.Repeat("c", 40))
	*now = now.Add(7 * 24 * time.Hour)
	if err := svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	held, _ := svc.Reviews(ctx)
	if r := lastRunOf(t, svc); r.Outcome != "review" || len(held) != 1 {
		t.Fatalf("score change not held: %+v %+v", r, held)
	}
	// Upstream moves on to a definition-only change, and its automatic apply fails.
	editUpstream(t, tree, syncFormatID, func(f map[string]any) {
		f["trash_scores"] = score
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
	reviews, _ := svc.Reviews(ctx)
	if r := lastRunOf(t, svc); r.Outcome != "error" || len(reviews) != 1 || reviews[0].ID != held[0].ID {
		t.Fatalf("failed apply: run %+v, pending %+v, want review %d kept", r, reviews, held[0].ID)
	}
	// Once the apply lands, the earlier review is superseded.
	svc.Coordinator.prepare = prepare
	*now = now.Add(7 * time.Hour)
	if err := svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if reviews, _ := svc.Reviews(ctx); lastRunOf(t, svc).Outcome != "applied" || len(reviews) != 0 {
		t.Fatalf("applied run: %+v %+v", lastRunOf(t, svc), reviews)
	}
}

// A draft preview (the editor's unsaved config) never replaces or supersedes the saved source's pending
// review, so a later run that finds the branch unchanged still leaves it offered and health still counts it.
func TestQualitySyncDraftPreviewKeepsPendingReview(t *testing.T) {
	ctx := context.Background()
	svc, gh, source, now := trackedService(t, QualitySyncConfig{Kind: "trash", Flavour: "radarr", Mode: "review"})
	tree := t.TempDir()
	copyTree(t, tree)
	if err := svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	editUpstream(t, tree, syncFormatID, func(f map[string]any) {
		f["specifications"].([]any)[1].(map[string]any)["fields"] = map[string]any{"value": "^(3L|NEW)$"}
	})
	gh.publish(t, tree, strings.Repeat("c", 40))
	*now = now.Add(7 * 24 * time.Hour)
	if err := svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	held, _ := svc.Reviews(ctx)
	if len(held) != 1 || len(held[0].Changed) != 1 {
		t.Fatalf("setup: want one pending review, got %+v", held)
	}
	// The admin edits the source, clicks "Preview draft import", then cancels the edit.
	draft := source()
	draft.Config.Mode = "auto"
	head, _ := svc.Coordinator.Snapshot(ctx)
	if _, err := svc.PreviewAndStoreAt(ctx, QualitySyncSource{Record: Record{Key: draft.Key}, Config: draft.Config}, head.Generation); err != nil {
		t.Fatal(err)
	}
	if err := svc.RunSource(ctx, source(), true); err != nil {
		t.Fatal(err)
	}
	reviews, _ := svc.Reviews(ctx)
	kept := false
	for _, p := range reviews {
		kept = kept || p.ID == held[0].ID
	}
	if !kept {
		t.Fatalf("draft preview dropped the pending review: run %+v, pending %+v", lastRunOf(t, svc), reviews)
	}
}

// A bound profile's score that still equals TRaSH's last score follows TRaSH; ours is held only when
// TRaSH changes it, with take (follows from then on) or keep (pinned, asked again on TRaSH's next change). Only the
// held scores wait, a pending one does not block later definition changes, unbound profiles never change, and review
// mode still holds everything.
func TestQualitySyncBoundScoresFollowOrHold(t *testing.T) {
	ctx := context.Background()
	const tier01, tier02, web01 = syncFormatID, "9f98181fe5a3fbeb0cc29340da2a468a", "c20f169ef63c5f40c2def54abaf4438e"
	svc, gh, source, now := trackedService(t, QualitySyncConfig{Kind: "trash", Flavour: "radarr", Profiles: map[string]string{"remux-2160p": "default"}})
	tree := t.TempDir()
	copyTree(t, tree)
	setScore(t, svc, "remux-2160p", "Remux Tier 02", 1800) // ours; TRaSH says 1900
	scores := func(profile string) map[string]int { return svc.Coordinator.Current().Policy.Profiles[profile].Formats }
	heldReview := func(want int) QualityPreview {
		t.Helper()
		reviews, err := svc.Reviews(ctx)
		if err != nil || len(reviews) != want {
			t.Fatalf("want %d pending reviews, got %+v %v", want, reviews, err)
		}
		if want == 0 {
			return QualityPreview{}
		}
		return reviews[0]
	}
	week := func(commit string) {
		t.Helper()
		gh.publish(t, tree, strings.Repeat(commit, 40))
		*now = now.Add(7 * 24 * time.Hour)
		if err := svc.RunDue(ctx); err != nil {
			t.Fatal(err)
		}
	}

	// First run after binding: the differing score is asked once; the others are remembered as following.
	if err := svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	p := heldReview(1)
	if r := lastRunOf(t, svc); r.Outcome != "review" || p.Kind != ReviewScores || len(p.Held) != 1 || p.Held[0].Format != "Remux Tier 02" || *p.Held[0].Ours != 1800 || p.Held[0].Last != nil || p.Held[0].Trash != 1900 {
		t.Fatalf("first run: %+v %+v", r, p)
	}
	if st := source().Config.ScoreState["remux-2160p"]; st.Set != "default" || st.Scores["Remux Tier 01"] != (TrashScoreState{Trash: 1950}) {
		t.Fatalf("first run state: %+v", st)
	}
	if _, err := svc.Apply(ctx, p.ID, p.Generation, "admin"); err == nil {
		t.Fatal("held scores approved as a whole")
	}
	gen := svc.Coordinator.Current().Generation
	if _, err := svc.ResolveScores(ctx, p.ID, gen, []ScoreDecision{{Profile: "remux-2160p", Format: "LQ", Choice: "take"}}, "admin"); err == nil {
		t.Fatal("decision for a score that is not held accepted")
	}
	if _, err := svc.ResolveScores(ctx, p.ID, gen, []ScoreDecision{{Profile: "remux-2160p", Format: "Remux Tier 02", Choice: "keep"}}, "admin"); err != nil {
		t.Fatal(err)
	}
	heldReview(0)
	if scores("remux-2160p")["Remux Tier 02"] != 1800 || source().Config.ScoreState["remux-2160p"].Scores["Remux Tier 02"] != (TrashScoreState{Trash: 1900, Pinned: true}) {
		t.Fatalf("keep: %+v", source().Config.ScoreState)
	}

	// TRaSH changes three scores and a definition: the following scores and the definition apply, ours is held, and
	// the unbound profile keeps its score.
	for id, score := range map[string]int{tier01: 1960, tier02: 1910, web01: 1750} {
		editUpstream(t, tree, id, func(f map[string]any) { f["trash_scores"] = map[string]any{"default": score} })
	}
	editUpstream(t, tree, tier01, func(f map[string]any) {
		f["specifications"].([]any)[1].(map[string]any)["fields"] = map[string]any{"value": "^(3L|NEW)$"}
	})
	gen = svc.Coordinator.Current().Generation
	week("c")
	p = heldReview(1)
	if r := lastRunOf(t, svc); r.Outcome != "review" || len(p.Held) != 1 || *p.Held[0].Ours != 1800 || *p.Held[0].Last != 1900 || p.Held[0].Trash != 1910 || svc.Coordinator.Current().Generation != gen+1 {
		t.Fatalf("pinned score not held alone: %+v %+v", r, p)
	}
	if s := scores("remux-2160p"); s["Remux Tier 01"] != 1960 || s["WEB Tier 01"] != 1750 || s["Remux Tier 02"] != 1800 || scores("web-2160p")["WEB Tier 01"] != 1700 {
		t.Fatalf("scores after the run: %v / %v", s, scores("web-2160p"))
	}

	// The pending held score does not block the next definition change.
	editUpstream(t, tree, tier01, func(f map[string]any) {
		f["specifications"].([]any)[1].(map[string]any)["fields"] = map[string]any{"value": "^(3L|NEWER)$"}
	})
	gen = svc.Coordinator.Current().Generation
	week("d")
	p = heldReview(1)
	if svc.Coordinator.Current().Generation != gen+1 || len(p.Held) != 1 || p.Held[0].Trash != 1910 {
		t.Fatalf("definition change blocked by the held score: %+v", p)
	}

	// Take: TRaSH's score, which follows from then on.
	if _, err := svc.ResolveScores(ctx, p.ID, svc.Coordinator.Current().Generation, []ScoreDecision{{Profile: "remux-2160p", Format: "Remux Tier 02", Choice: "take"}}, "admin"); err != nil {
		t.Fatal(err)
	}
	if scores("remux-2160p")["Remux Tier 02"] != 1910 {
		t.Fatal("take did not set TRaSH's score")
	}
	editUpstream(t, tree, tier02, func(f map[string]any) { f["trash_scores"] = map[string]any{"default": 1920} })
	week("e")
	heldReview(0)
	if r := lastRunOf(t, svc); r.Outcome != "applied" || scores("remux-2160p")["Remux Tier 02"] != 1920 {
		t.Fatalf("taken score does not follow: %+v %v", r, scores("remux-2160p"))
	}
	audited := false
	for _, a := range audit(t, svc.Store) {
		audited = audited || (a.Operation == "quality-scores" && a.Actor == "admin")
	}
	if !audited {
		t.Fatal("score decisions not audited")
	}

	// Review mode still holds everything, a following score included.
	snap, _ := svc.Coordinator.Snapshot(ctx)
	req := SaveRequest{ExpectedGeneration: snap.Generation, Actor: "admin", Records: snap.Records, Base: svc.Coordinator.base}
	req.Records.SyncSources = slices.Clone(req.Records.SyncSources)
	req.Records.SyncSources[0].Config.Mode = "review"
	if _, err := svc.Coordinator.SaveAndApply(ctx, req); err != nil {
		t.Fatal(err)
	}
	editUpstream(t, tree, tier02, func(f map[string]any) { f["trash_scores"] = map[string]any{"default": 1930} })
	gen = svc.Coordinator.Current().Generation
	week("f")
	p = heldReview(1)
	if svc.Coordinator.Current().Generation != gen || len(p.Scores) != 1 || p.Scores[0].After != 1930 || len(p.Held) != 0 {
		t.Fatalf("review mode applied a score: %+v", p)
	}
	if _, err := svc.Apply(ctx, p.ID, p.Generation, "admin"); err != nil {
		t.Fatal(err)
	}
	if source().Config.ScoreState["remux-2160p"].Scores["Remux Tier 02"] != (TrashScoreState{Trash: 1930}) {
		t.Fatal("approved review did not remember TRaSH's score")
	}
}
