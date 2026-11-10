package golive

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// parallelExample is the example policy with movie-remux-theatarr following movie-remux (active, its own root).
func parallelExample(t *testing.T) Options {
	t.Helper()
	pol := examplePolicy(t,
		[3]string{"versions.yaml", "versions:\n", "versions:\n  movie-remux-theatarr:\n    follows: movie-remux\n" +
			"    mode: active\n    kind: movie\n    anime: false\n    profile: remux-2160p\n" +
			"    library: /media/library/movies-remux\n    storage: debrid\n    indexers: [prowlarr-public]\n" +
			"    providers: [rd]\n    client: decypharr\n    naming: {from_version: movie-remux}\n"},
		[3]string{"library.yaml", "\n  versions:\n", "\n  versions:\n" +
			"    movie-remux-theatarr: {roots: [/media/library/movies-remux], plex_section: 22}\n"})
	return Options{Policy: pol, Version: "movie-remux-theatarr", FS: fakeFS{}, Env: allEnv, Health: okHealth()}
}

func TestParallelVersion(t *testing.T) {
	t.Parallel()
	o := parallelExample(t)
	mount := t.TempDir()
	o.Policy.Library.Mount.Path, o.Policy.Library.Mount.LinkPath = mount, "/plex/zurg"
	if err := os.Mkdir(filepath.Join(mount, "__all__"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mount, "__all__", "movie.mkv"), []byte("movie"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := testStore(t)
	o.DB = st.Reader()
	// Two arr files of movie-remux, one already adopted into movie-remux-theatarr.
	exec(t, st, `INSERT INTO arr_instance (name, flavour, policy_key, created_at, updated_at) VALUES ('radarr_4k_debrid', 'radarr', 'movie-remux', 0, 0)`)
	for i := 1; i <= 2; i++ {
		exec(t, st, `INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (?, 'movie', 'M', 0, 0)`, i)
		exec(t, st, `INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (?, ?, 'movie-remux', 'cutoff_met', 0, 0)`, i*10, i)
		exec(t, st, `INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (?, ?, 'movie-remux-theatarr', 'wanted', 0, 0)`, i*10+1, i)
		exec(t, st, `INSERT INTO version_source (version_id, instance, arr_id) VALUES (?, 'radarr_4k_debrid', ?)`, i*10, i)
		exec(t, st, `INSERT INTO version_file (instance, arr_file_id, version_id, title_id, path) VALUES ('radarr_4k_debrid', ?, ?, ?, ?)`,
			i, i*10, i, "/arr/m"+string(rune('0'+i)))
	}
	exec(t, st, `INSERT INTO link (id, path, target, title_id, state, created_at, updated_at) VALUES (1, '/media/library/movies-remux/M/m.mkv', '/plex/zurg/__all__/movie.mkv', 1, 'active', 0, 0)`)
	exec(t, st, `INSERT INTO link_version (link_id, version_id) VALUES (1, 11)`)
	r := run(t, o)
	want(t, r, SecParallel, "follows", Pass, "fed by radarr_4k_debrid")
	want(t, r, SecParallel, "arr grabbing", Info, "keeps grabbing for movie-remux")
	want(t, r, SecParallel, "adopt", Warn, "1 of 2 arr files")
	want(t, r, SecHandover, "owner", Pass, "parallel version")
	want(t, r, SecPolicy, "library root", Pass, "/media/library/movies-remux")
	want(t, r, SecPolicy, "naming", Pass, "naming comes from version movie-remux")
	if len(find(r, SecPolicy, "naming")) != 1 {
		t.Fatalf("follower naming should not warn: %+v", find(r, SecPolicy, "naming"))
	}
	if len(find(r, SecShadow, "agreement")) != 0 || len(find(r, SecHandover, "arr grabbing")) != 0 {
		t.Fatal("a parallel version got the shadow window or the arr handover")
	}
	exec(t, st, `INSERT INTO link (id, path, target, title_id, state, created_at, updated_at) VALUES (2, '/media/library/movies-remux/N/n.mkv', '/plex/zurg/__all__/movie.mkv', 2, 'active', 0, 0)`)
	exec(t, st, `INSERT INTO link_version (link_id, version_id) VALUES (2, 21)`)
	want(t, run(t, o), SecParallel, "adopt", Pass, "every one of the 2 arr files")

	// The followed shadow version's arr still grabbing is information, never a failure.
	o.Version = "movie-remux"
	o.Arr = func(string) (ArrGetter, error) { return movieArr(0, 0, true), nil }
	want(t, run(t, o), SecHandover, "arr grabbing", Info, "grabs today")
}

func TestFollowedShadowCutover(t *testing.T) {
	t.Parallel()
	o := parallelExample(t)
	o.Version = "movie-remux"
	o.FS = fakeFS{readOnly: map[string]bool{"/media/movies": true}}
	r := run(t, o)
	want(t, r, SecPolicy, "cutover", Info, "cut over through its follower movie-remux-theatarr (docs/migration.md#5-hand-over-clients-and-plex-retire-the-arr)")
	if len(find(r, SecPolicy, "switched policy")) != 0 {
		t.Fatal("followed library simulated an in-place switch")
	}
	after, err := o.Policy.Clone()
	if err != nil {
		t.Fatal(err)
	}
	after.Versions[o.Version].Mode = policy.ModeActive
	if err := after.Validate(); err == nil || !strings.Contains(err.Error(), "must stay shadow") {
		t.Fatalf("activation guard lost: %v", err)
	}
	want(t, r, SecPolicy, "library root", Info, "read-only by design: followed by movie-remux-theatarr")
	want(t, r, SecHandover, "cutover", Info, "clients and Plex move to movie-remux-theatarr's folders")
	for _, x := range r.Results {
		if x.Section == SecPolicy && x.Check == "library root" && x.Status == Fail && strings.Contains(x.Message, "not writable") {
			t.Fatalf("followed shadow root must not require writability: %+v", x)
		}
	}
}

func TestParallelEpisodeCoverage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, state            string
		full, dangling, outage bool
		status                 Status
		message                string
	}{
		{"partial pack", "active", false, false, false, Warn, "1 missing episodes, 1 partial packs"},
		{"full across links", "active", true, false, false, Pass, "every one of the 1 arr files"},
		{"dangling", "active", true, true, false, Warn, "2 missing episodes, 0 partial packs"},
		{"inactive", "dead", true, false, false, Warn, "2 missing episodes, 0 partial packs"},
		{"mount outage", "active", true, false, true, Warn, "coverage unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := testStore(t)
			mount := t.TempDir()
			if !tc.outage {
				if err := os.Mkdir(filepath.Join(mount, "__all__"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if !tc.dangling && !tc.outage {
				if err := os.WriteFile(filepath.Join(mount, "__all__", "episode.mkv"), []byte("episode"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			exec(t, st, `INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (1, 'series', 'Show', 0, 0)`)
			exec(t, st, `INSERT INTO version (id, title_id, policy_key, created_at, updated_at) VALUES (1, 1, 'F', 0, 0), (2, 1, 'K', 0, 0)`)
			exec(t, st, `INSERT INTO version_source (version_id, instance, arr_id) VALUES (1, 'sonarr', 1)`)
			exec(t, st, `INSERT INTO version_file (id, instance, arr_file_id, version_id, title_id, path) VALUES (1, 'sonarr', 1, 1, 1, '/arr/Show.S01E01E02.mkv')`)
			exec(t, st, `INSERT INTO episode (id, title_id, season, number, created_at, updated_at) VALUES (1, 1, 1, 1, 0, 0), (2, 1, 1, 2, 0, 0)`)
			exec(t, st, `INSERT INTO version_file_episode (version_file_id, episode_id) VALUES (1, 1), (1, 2)`)
			n := 1
			if tc.full {
				n = 2
			}
			for ep := 1; ep <= n; ep++ {
				exec(t, st, `INSERT INTO link (id, path, target, title_id, state, created_at, updated_at) VALUES (?, ?, '/plex/zurg/__all__/episode.mkv', 1, ?, 0, 0)`,
					ep, filepath.Join("/theatarr", string(rune('0'+ep))), tc.state)
				exec(t, st, `INSERT INTO link_version (link_id, version_id) VALUES (?, 2)`, ep)
				exec(t, st, `INSERT INTO link_episode (link_id, episode_id) VALUES (?, ?)`, ep, ep)
			}
			p := &policy.Policy{Library: &policy.Library{Mount: policy.LibraryMount{Path: mount, LinkPath: "/plex/zurg"}}}
			c := &checker{o: Options{Policy: p, DB: st.Reader(), Version: "K"}, r: &Report{}}
			c.adopted(context.Background(), "F")
			want(t, c.r, SecParallel, "adopt", tc.status, tc.message)
			if tc.name == "partial pack" && !strings.Contains(c.r.Results[0].Message, "S01E02") {
				t.Fatalf("missing episode not sampled: %s", c.r.Results[0].Message)
			}
		})
	}
}

func TestParallelPhysicalRoots(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, target, suffix string
		status               Status
	}{
		{"distinct", "", "", Pass},
		{"alias", "arr", "", Fail},
		{"nested via symlink", "arr", "new", Fail},
		{"trash alias", "trash", "", Fail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			arr, mine, trash := filepath.Join(dir, "arr"), filepath.Join(dir, "theatarr"), filepath.Join(dir, "trash")
			for _, path := range []string{arr, trash} {
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if tc.target == "" {
				if err := os.Mkdir(mine, 0o755); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink(filepath.Join(dir, tc.target), mine); err != nil {
				t.Fatal(err)
			}
			mine = filepath.Join(mine, tc.suffix)
			p := &policy.Policy{Versions: map[string]*policy.VersionPolicy{"F": {Library: arr}, "K": {Follows: "F", Library: mine}},
				Library: &policy.Library{Trash: policy.LibraryTrash{Dir: trash}}}
			c := &checker{o: Options{Policy: p, Version: "K"}, v: p.Versions["K"], r: &Report{}}
			c.parallel(context.Background())
			want(t, c.r, SecParallel, "roots", tc.status, "resolved")
		})
	}
}

// followerLibrary is parallelExample with a database: one movie title, adopted into the follower by one link
// (download_id NULL, "adopted from" action) and one of Theatarr's own links (with a download), plus a dangling link.
func followerLibrary(t *testing.T, edits ...[3]string) (Options, *store.Store) {
	t.Helper()
	o := parallelExample(t)
	if len(edits) > 0 {
		pol := examplePolicy(t, append([][3]string{
			{"versions.yaml", "versions:\n", "versions:\n  movie-remux-theatarr:\n    follows: movie-remux\n" +
				"    mode: active\n    kind: movie\n    anime: false\n    profile: remux-2160p\n" +
				"    library: /media/library/movies-remux\n    storage: debrid\n    indexers: [prowlarr-public]\n" +
				"    providers: [rd]\n    client: decypharr\n    naming: {from_version: movie-remux}\n"},
			{"library.yaml", "\n  versions:\n", "\n  versions:\n" +
				"    movie-remux-theatarr: {roots: [/media/library/movies-remux], plex_section: 22}\n"}}, edits...)...)
		o.Policy = pol
	}
	st := testStore(t)
	o.DB = st.Reader()
	exec(t, st, `INSERT INTO arr_instance (name, flavour, policy_key, created_at, updated_at) VALUES ('radarr_4k_debrid', 'radarr', 'movie-remux', 0, 0)`)
	for i := 1; i <= 3; i++ {
		exec(t, st, `INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (?, 'movie', 'M', 0, 0)`, i)
		exec(t, st, `INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (?, ?, 'movie-remux', 'cutoff_met', 0, 0)`, i*10, i)
		exec(t, st, `INSERT INTO version (id, title_id, policy_key, state, monitored, created_at, updated_at) VALUES (?, ?, 'movie-remux-theatarr', ?, 1, 0, 0)`,
			i*10+1, i, map[bool]string{true: "wanted", false: "cutoff_met"}[i == 3])
	}
	exec(t, st, `INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, provider,
		provider_torrent_id, state, created_at, updated_at) VALUES (5, 2, 21, 'movie-remux-theatarr', ?, 'B', 'pub', 'rd', 'T', 'imported', 0, 0)`,
		strings.Repeat("b", 40))
	exec(t, st, `INSERT INTO link (id, path, target, title_id, state, created_at, updated_at) VALUES (1, '/media/library/movies-remux/A/a.mkv', '/t/a', 1, 'active', 0, 0)`)
	exec(t, st, `INSERT INTO link (id, path, target, title_id, download_id, state, created_at, updated_at) VALUES (2, '/media/library/movies-remux/B/b.mkv', '/t/b', 2, 5, 'active', 0, 0)`)
	exec(t, st, `INSERT INTO link_version (link_id, version_id) VALUES (1, 11), (2, 21)`)
	exec(t, st, `INSERT INTO library_action (at, kind, mode, version_key, link_id, path, target, detail) VALUES (?, 'link', 'applied', 'movie-remux-theatarr', 1, '/media/library/movies-remux/A/a.mkv', '/t/a', 'adopted from radarr_4k_debrid /arr/a.mkv')`,
		now.Add(-48*time.Hour).UnixMilli())
	exec(t, st, `INSERT INTO library_dangling_scan (at, roots, links, dangling, took_ms) VALUES (?, '[]', 10, 1, 1)`, now.Add(-time.Hour).UnixMilli())
	exec(t, st, `INSERT INTO library_dangling (path, target, root, version_key, folder, first_seen_at, last_seen_at)
		VALUES ('/media/library/movies-remux/C/c.mkv', '/t/c', '/media/library/movies-remux', 'movie-remux-theatarr', 'x', 0, 0)`)
	return o, st
}

// With wanted.missing false the review lines may not promise a missing search, and "our links" counts only
// links of Theatarr's own downloads (link.adopted is 0 for every row).
func TestFollowerReviewTextsWithMissingOff(t *testing.T) {
	t.Parallel()
	o, _ := followerLibrary(t, [3]string{"router.yaml", "    missing: true", "    missing: false"})
	r := run(t, o)
	d := find(r, SecLibrary, "dangling")
	if len(d) != 1 || strings.Contains(d[0].Fix, "searched again by the wanted loop") || !strings.Contains(d[0].Fix, "RSS or a manual search") {
		t.Fatalf("dangling fix with missing off: %+v", d)
	}
	w := find(r, SecLibrary, "wanted")
	if len(w) != 1 || strings.Contains(w[0].Message, "starts with 1 missing") || !strings.Contains(w[0].Message, "missing search is off") {
		t.Fatalf("wanted line with missing off: %+v", w)
	}
	want(t, r, SecLibrary, "our links", Info, "1 active links of movie-remux-theatarr are Theatarr's own downloads; 1 adopted")

	// With the missing search on, the old wording stays.
	o, _ = followerLibrary(t)
	r = run(t, o)
	if d := find(r, SecLibrary, "dangling"); len(d) != 1 || !strings.Contains(d[0].Fix, "searched again by the wanted loop") {
		t.Fatalf("dangling fix with missing on: %+v", d)
	}
	want(t, r, SecLibrary, "wanted", Info, "starts with 1 missing")
}

// A follower whose last adopt is older than its arr's newest import warns before grabbing.
func TestFollowerAdoptFreshness(t *testing.T) {
	t.Parallel()
	o, st := followerLibrary(t)
	// No arr imports recorded: nothing to compare.
	want(t, run(t, o), SecParallel, "adopt and refresh", Info, "no arr imports of movie-remux")
	exec(t, st, `INSERT INTO import_run (instance, started_at, finished_at) VALUES ('radarr_4k_debrid', ?, ?)`, now.Add(-2*time.Hour).UnixMilli(), now.Add(-2*time.Hour).UnixMilli())
	exec(t, st, `INSERT INTO arr_history (instance, arr_id, event_type, date_at, version_id) VALUES ('radarr_4k_debrid', 1, 'downloadFolderImported', ?, 10)`, now.Add(-72*time.Hour).UnixMilli())
	want(t, run(t, o), SecParallel, "adopt and refresh", Pass, "after movie-remux's newest arr import")
	exec(t, st, `INSERT INTO arr_history (instance, arr_id, event_type, date_at, version_id) VALUES ('radarr_4k_debrid', 2, 'downloadFolderImported', ?, 20), ('radarr_4k_debrid', 3, 'grabbed', ?, 30)`,
		now.Add(-24*time.Hour).UnixMilli(), now.Add(-3*time.Hour).UnixMilli())
	r := run(t, o)
	want(t, r, SecParallel, "adopt and refresh", Warn, "1 arr imports of movie-remux since movie-remux-theatarr's last adopt or refresh")
	if f := find(r, SecParallel, "adopt and refresh")[0].Fix; !strings.Contains(f, "theatarr library adopt -version movie-remux-theatarr") || !strings.Contains(f, "-refresh") || !strings.Contains(f, "RSS or a manual search") {
		t.Errorf("fix = %q", f)
	}
	// A refresh after the import clears it.
	exec(t, st, `INSERT INTO library_action (at, kind, mode, version_key, link_id, detail) VALUES (?, 'replace', 'applied', 'movie-remux-theatarr', 1, 'refresh: the arr''s file is now /arr/x')`, now.Add(-time.Hour).UnixMilli())
	want(t, run(t, o), SecParallel, "adopt and refresh", Pass, "after movie-remux's newest arr import")
}
