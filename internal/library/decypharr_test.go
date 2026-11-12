package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/library/decypharrtest"
	"github.com/theatarr/theatarr/internal/library/probe"
	"github.com/theatarr/theatarr/internal/policy"
)

// decyFolder is a folder name decypharr gives the test download's torrent: no name the download records leads to it.
const decyFolder = "Marrow.Lantern.2014.2160p.UHD.BluRay.REMUX-FERNWOOD"

// withDecypharr starts a fake decypharr holding torrents and points e's library at it through a real gateway built
// from a policy with library.mount.decypharr set (target decypharr:api).
func (e *env) withDecypharr(torrents ...decypharrtest.Torrent) *decypharrtest.Server {
	e.t.Helper()
	srv := decypharrtest.New(e.t)
	srv.Set(func(s *decypharrtest.Server) { s.Providers, s.Torrents = []string{"realdebrid", "torbox"}, torrents })
	p := &policy.Policy{
		Budgets: map[string]*policy.Budget{"decy": {Requests: 6000, Per: policy.Duration(time.Minute)}},
		Library: &policy.Library{Mount: policy.LibraryMount{
			Decypharr: &policy.LibraryDecypharr{URL: srv.URL(), Budget: "decy"}}},
	}
	gw, err := gateway.New(e.ctx, p, gateway.Options{})
	if err != nil {
		e.t.Fatal(err)
	}
	e.lib.decy = &Decypharr{Config: DecypharrConfig{URL: srv.URL(), Interval: time.Minute}, GW: gw,
		Now: func() time.Time { return e.now }}
	return srv
}

// listFiles makes the provider's file list of the test download: files, read by provider "rd" and torrent id "T1".
func (e *env) listFiles(files ...TorrentFile) *int {
	calls := new(int)
	e.exec(`UPDATE download SET provider_torrent_id = 'T1' WHERE id = 1`)
	e.lib.files = func(_ context.Context, provider, id, hash string) ([]TorrentFile, bool) {
		*calls++
		if provider != "rd" || id != "T1" || hash != hashA {
			e.t.Errorf("file list asked for %s %s %s", provider, id, hash)
		}
		return files, len(files) > 0
	}
	return calls
}

func (e *env) movieTarget() string {
	tg, _ := os.Readlink(filepath.Join(e.movies, "Amelie The Movie (2001)",
		"Amelie The Movie (2001) {imdb-tt0211915} - [Remux-2160p][TrueHD Atmos 7.1][HDR10][HEVC]-GRP.mkv"))
	return tg
}

// TestImportDecypharrFolder: on decypharr's mount the release folder is decypharr's name for the download's hash and
// no other; a hash decypharr does not list is looked for under the names any mount is tried with. A folder the mount
// does not show yet waits; so does an index that cannot be read, even when a folder of the download's own name is
// there.
func TestImportDecypharrFolder(t *testing.T) {
	t.Parallel()
	torrent := decypharrtest.Torrent{Hash: hashA, Name: decyFolder, Provider: "realdebrid"}

	t.Run("decypharr's name for the hash", func(t *testing.T) {
		e := newEnv(t, true, policy.ModeActive)
		e.release(decyFolder, map[string]float64{"movie.mkv": 7000})
		e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
		e.withDecypharr(torrent)
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		if tg := e.movieTarget(); tg != filepath.Join(e.mount, "__all__", decyFolder, "movie.mkv") {
			t.Errorf("linked %q", tg)
		}
	})

	t.Run("not listed: the names of any mount", func(t *testing.T) {
		e := newEnv(t, true, policy.ModeActive)
		e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
		e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
		// The registry records the bare name for another torrent: on decypharr's mount it is still the folder.
		e.exec(`UPDATE download SET original_name = ? WHERE id = 1`, movieRelease)
		e.exec(`INSERT INTO content (info_hash, name, state, first_seen_at, last_seen_at) VALUES (?, ?, 'present', 0, 0)`,
			hashB, movieRelease)
		e.withDecypharr()
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		if tg := e.movieTarget(); tg != filepath.Join(e.mount, "__all__", movieRelease, "movie.mkv") {
			t.Errorf("linked %q", tg)
		}
	})

	t.Run("folder not on the mount yet", func(t *testing.T) {
		e := newEnv(t, true, policy.ModeActive)
		e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
		var forgot int
		e.lib.forget = &rootForget{call: func(context.Context, string) error { forgot++; return nil }}
		e.withDecypharr(torrent)
		plan, err := e.lib.Import(e.ctx, 1, false)
		if err != nil || !plan.Waiting {
			t.Fatalf("plan %+v, err %v", plan, err)
		}
		if s := e.one(`SELECT last_error FROM library_import WHERE download_id = 1`); s != notOnMount {
			t.Errorf("waits with %q", s)
		}
		if forgot != 1 {
			t.Errorf("decypharr lists the hash: the mount's cached root listing is cleared once, got %d", forgot)
		}
	})

	t.Run("listed: never another folder of the download's name", func(t *testing.T) {
		e := newEnv(t, true, policy.ModeActive)
		e.release(movieRelease, map[string]float64{"movie.mkv": 7000}) // an older torrent's folder
		e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
		e.withDecypharr(torrent)
		plan, err := e.lib.Import(e.ctx, 1, false)
		if err != nil || !plan.Waiting {
			t.Fatalf("plan %+v, err %v", plan, err)
		}
		if s := e.one(`SELECT last_error FROM library_import WHERE download_id = 1`); s != notOnMount {
			t.Errorf("waits with %q", s)
		}
		if e.movieTarget() != "" {
			t.Error("imported from a folder decypharr does not name for the hash")
		}
	})

	t.Run("index unknown", func(t *testing.T) {
		e := newEnv(t, true, policy.ModeActive)
		e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
		e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
		srv := e.withDecypharr(torrent)
		srv.Set(func(s *decypharrtest.Server) { s.Fail = 500 })
		plan, err := e.lib.Import(e.ctx, 1, false)
		if err != nil || !plan.Waiting {
			t.Fatalf("plan %+v, err %v", plan, err)
		}
		if e.movieTarget() != "" {
			t.Error("imported by name while decypharr's index is unknown")
		}
		if s := e.one(`SELECT state FROM download WHERE id = 1`); s != "ready" {
			t.Errorf("download %s", s)
		}
		// The router's ready deadline pauses as for a mount that is down.
		if err := e.lib.MountListed(e.ctx); !errors.Is(err, ErrMountDown) ||
			!strings.Contains(err.Error(), "decypharr") {
			t.Errorf("mount listed with the index unknown: %v", err)
		}
	})

	t.Run("a path import does not read decypharr", func(t *testing.T) {
		e := newEnv(t, true, policy.ModeActive)
		e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
		e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
		e.exec(`UPDATE download SET source_path = ? WHERE id = 1`, filepath.Join(e.mount, "__all__", movieRelease))
		srv := e.withDecypharr(torrent)
		calls := e.listFiles(TorrentFile{Name: "other.mkv", Size: 1})
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		if e.movieTarget() == "" {
			t.Error("the chosen path was not imported")
		}
		// Only WebDAV's loading check (mountHealthy) is sent.
		for _, r := range srv.Requests() {
			if !strings.HasPrefix(r, "PROPFIND ") {
				t.Errorf("request %s", r)
			}
		}
		if *calls != 0 {
			t.Errorf("file list read %d times for a chosen path", *calls)
		}
	})
}

// TestImportDecypharrFileList: the download's own file list filters the files of decypharr's folder by name and exact
// size. A file that is not in it is skipped; without a list the import goes by name; only a list none of the folder's
// files is in holds the import for review, never failed or blocklisted.
func TestImportDecypharrFileList(t *testing.T) {
	t.Parallel()
	torrent := decypharrtest.Torrent{Hash: hashA, Name: decyFolder, Provider: "realdebrid"}
	setup := func(t *testing.T) *env {
		e := newEnv(t, true, policy.ModeActive)
		e.release(decyFolder, map[string]float64{"movie.mkv": 7000})
		e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
		e.withDecypharr(torrent)
		return e
	}

	t.Run("listed", func(t *testing.T) {
		e := setup(t)
		// The provider's path, with its folder; the file on the mount is 5 bytes.
		calls := e.listFiles(TorrentFile{Name: "/" + decyFolder + "/movie.mkv", Size: 5}, TorrentFile{Name: "/notes.txt", Size: 9})
		plan, err := e.lib.Import(e.ctx, 1, false)
		if err != nil {
			t.Fatal(err)
		}
		if e.movieTarget() == "" || len(plan.Rejected) != 0 {
			t.Errorf("plan %+v", plan)
		}
		if *calls != 1 {
			t.Errorf("file list read %d times in one attempt", *calls)
		}
	})

	t.Run("no list", func(t *testing.T) {
		e := setup(t)
		e.listFiles()
		if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
			t.Fatal(err)
		}
		if e.movieTarget() == "" {
			t.Error("without a list the import goes by name")
		}
	})

	t.Run("no file matches", func(t *testing.T) {
		e := setup(t)
		e.listFiles(TorrentFile{Name: "movie.mkv", Size: 6})
		plan, err := e.lib.Import(e.ctx, 1, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Links) != 0 || len(plan.Rejected) != 0 || !plan.Waiting {
			t.Fatalf("plan %+v", plan)
		}
		if s := e.one(`SELECT last_error FROM library_import WHERE download_id = 1`); !strings.HasPrefix(s, "mount mismatch: decypharr's folder") ||
			!strings.Contains(s, "holds none of the download's files") {
			t.Errorf("waiting with %q", s)
		}
		// As a file zurg does not confirm: the import waits, the download neither failed nor blocklisted.
		if s := e.one(`SELECT state FROM download WHERE id = 1`); s != "ready" {
			t.Errorf("download %s: waiting, not failed", s)
		}
		if n := e.one(`SELECT COUNT(*) FROM router_blocklist`); n != "0" {
			t.Errorf("blocklisted %s", n)
		}
		if s := e.one(`SELECT state FROM library_import WHERE download_id = 1`); s != "waiting_mount" {
			t.Errorf("library import %s", s)
		}
	})

	t.Run("not listed: another torrent's folder of the same name waits", func(t *testing.T) {
		e := newEnv(t, true, policy.ModeActive)
		e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
		e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
		e.withDecypharr()
		e.listFiles(TorrentFile{Name: "movie.mkv", Size: 6})
		plan, err := e.lib.Import(e.ctx, 1, false)
		if err != nil || !plan.Waiting || len(plan.Links) != 0 {
			t.Fatalf("plan %+v, err %v", plan, err)
		}
		// The one stop: a list, a folder, no file of the list in it. Waits as a zurg mismatch does, until the ready
		// deadline fails it without blocklist.
		if s := e.one(`SELECT li.state || ' ' || d.state FROM library_import li JOIN download d ON d.id = li.download_id`); s != "waiting_mount ready" {
			t.Errorf("import and download: %s", s)
		}
		if n := e.one(`SELECT COUNT(*) FROM router_blocklist`); n != "0" {
			t.Errorf("blocklisted %s", n)
		}
	})

	t.Run("pack: a skipped file is no verdict on the release", func(t *testing.T) {
		// Grabbed for E02. E01 is the download's file but not an episode it was grabbed for; E02 in the shared folder
		// is another torrent's (another size). No file failed a guard: held as unmatched, not failed or blocklisted.
		e := newEnv(t, true, policy.ModeActive)
		folder := "Pale.Ferry.S01.2160p.WEB-DL.DDP5.1.H.265-TILLER"
		e.release(folder, map[string]float64{"Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2700,
			"Show.S01E02.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2650})
		rel := "Show.S01.2160p.WEB-DL.DDP5.1.H.265-GRP"
		e.download(1, "tv", 2, 20, rel, rel, "[202]")
		e.withDecypharr(decypharrtest.Torrent{Hash: hashA, Name: folder, Provider: "realdebrid"})
		e.listFiles(TorrentFile{Name: "Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv", Size: 5},
			TorrentFile{Name: "Show.S01E02.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv", Size: 50})
		plan, err := e.lib.Import(e.ctx, 1, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Links) != 0 || len(plan.Rejected) != 0 {
			t.Fatalf("plan %+v", plan)
		}
		if s := e.one(`SELECT li.state || ' ' || d.state || ' ' || li.last_error FROM library_import li JOIN download d ON d.id = li.download_id`); s != "rejected ready no file matched the download's episodes" {
			t.Errorf("import and download: %s", s)
		}
		if n := e.one(`SELECT COUNT(*) FROM router_blocklist`); n != "0" {
			t.Errorf("blocklisted %s", n)
		}
	})

	t.Run("dry run of a download that is not ready reads no file", func(t *testing.T) {
		e := newEnv(t, true, policy.ModeActive)
		e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
		e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
		e.withDecypharr()
		e.exec(`UPDATE download SET state = 'downloading' WHERE id = 1`)
		var probed int
		read := e.lib.probe
		e.lib.probe = func(p string, max int64) (*probe.Result, error) { probed++; return read(p, max) }
		plan, err := e.lib.Import(e.ctx, 1, true)
		if err != nil {
			t.Fatal(err)
		}
		// The preview is the plan by name, with the file marked as not read; nothing is recorded.
		if probed != 0 || len(plan.Files) != 1 || !strings.Contains(plan.Files[0].Reason, "not ready") ||
			len(plan.Links) != 1 || len(plan.Rejected) != 0 {
			t.Errorf("probed %d, plan %+v", probed, plan)
		}
		if s := e.one(`SELECT d.state || ' ' || (SELECT COUNT(*) FROM library_import) || ' ' || (SELECT COUNT(*) FROM router_blocklist)
			FROM download d WHERE d.id = 1`); s != "downloading 0 0" {
			t.Errorf("after the dry run: %s", s)
		}
		// A download that failed before it was ready is not read either; one that failed after it was is.
		e.exec(`UPDATE download SET state = 'failed', ready_at = NULL WHERE id = 1`)
		if _, err := e.lib.Import(e.ctx, 1, true); err != nil || probed != 0 {
			t.Errorf("failed before ready: probed %d, %v", probed, err)
		}
		e.exec(`UPDATE download SET ready_at = 1 WHERE id = 1`)
		if _, err := e.lib.Import(e.ctx, 1, true); err != nil || probed != 1 {
			t.Errorf("failed after ready: probed %d, %v", probed, err)
		}
		probed = 0
		e.exec(`UPDATE download SET state = 'downloading' WHERE id = 1`)
		// Without decypharr the dry run reads the file as before.
		e.lib.decy = nil
		if _, err := e.lib.Import(e.ctx, 1, true); err != nil || probed != 1 {
			t.Errorf("without decypharr: probed %d, %v", probed, err)
		}
	})

	t.Run("pack: the listed files link, the rest is skipped", func(t *testing.T) {
		e := newEnv(t, true, policy.ModeActive)
		folder := "Pale.Ferry.S01.2160p.WEB-DL.DDP5.1.H.265-TILLER"
		e.release(folder, map[string]float64{"Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2700,
			"Show.S01E02.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv": 2650})
		rel := "Show.S01.2160p.WEB-DL.DDP5.1.H.265-GRP"
		e.download(1, "tv", 2, 20, rel, rel, "[201, 202]")
		e.withDecypharr(decypharrtest.Torrent{Hash: hashA, Name: folder, Provider: "realdebrid"})
		// E02 in the shared folder is another torrent's: same name, another size.
		e.listFiles(TorrentFile{Name: "Show.S01E01.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv", Size: 5},
			TorrentFile{Name: "Show.S01E02.2160p.WEB-DL.DDP5.1.H.265-GRP.mkv", Size: 50})
		plan, err := e.lib.Import(e.ctx, 1, false)
		if err != nil {
			t.Fatal(err)
		}
		var paths []string
		for _, l := range plan.Links {
			paths = append(paths, strings.TrimPrefix(l.Path, e.shows+"/"))
		}
		sort.Strings(paths)
		if len(paths) != 1 || !strings.Contains(paths[0], "S01E01") || len(plan.Rejected) != 0 {
			t.Errorf("links %q, rejected %+v", paths, plan.Rejected)
		}
		if n := e.one(`SELECT COUNT(*) FROM router_blocklist`); n != "0" {
			t.Errorf("blocklisted %s", n)
		}
	})

	t.Run("client add: decypharr's own list", func(t *testing.T) {
		for _, c := range []struct {
			name   string
			queued bool
			size   int64
			links  bool
		}{
			{"listed", true, 5, true},
			{"another size", true, 6, false},
			{"queue entry gone: by name", false, 6, true},
		} {
			t.Run(c.name, func(t *testing.T) {
				e := newEnv(t, true, policy.ModeActive)
				e.release(decyFolder, map[string]float64{"movie.mkv": 7000})
				e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
				e.exec(`UPDATE download SET provider = NULL, client = 'decy' WHERE id = 1`)
				e.withDecypharr(decypharrtest.Torrent{Hash: hashA, Name: decyFolder, Provider: "realdebrid",
					Queued: c.queued, Files: []decypharrtest.File{{Name: "movie.mkv", Size: c.size}}})
				e.lib.files = func(context.Context, string, string, string) ([]TorrentFile, bool) {
					t.Error("a client add reads decypharr's list, not a provider's")
					return nil, false
				}
				if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
					t.Fatal(err)
				}
				if linked := e.movieTarget() != ""; linked != c.links {
					t.Errorf("linked %v, want %v", linked, c.links)
				}
			})
		}
	})
}

// TestRelinkDecypharr: a dangling link is repointed to the folder decypharr now lists its hash under, whatever the
// registry records; with the download's file list, only when the file there is the download's own.
func TestRelinkDecypharr(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		list    []TorrentFile
		relinks bool
	}{
		{"no list: by name", nil, true},
		{"listed", []TorrentFile{{Name: "movie.mkv", Size: 5}}, true},
		{"another torrent's file", []TorrentFile{{Name: "movie.mkv", Size: 6}}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			e.release(decyFolder, map[string]float64{"movie.mkv": 7000})
			e.exec(`INSERT INTO content (id, info_hash, name, state, first_seen_at, last_seen_at) VALUES (1, ?, 'Movie.Release', 'present', 0, 0)`, hashA)
			e.download(1, "movie-remux", 1, 10, movieRelease, "Movie.Release", "[]")
			link := filepath.Join(e.movies, "Movie (2001)", "Movie (2001).mkv")
			os.MkdirAll(filepath.Dir(link), 0o755)
			target := filepath.Join(e.mount, "__all__", "Movie.Release", "movie.mkv")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			e.exec(`INSERT INTO link (path, target, title_id, state, download_id, created_at, updated_at) VALUES (?, ?, 1, 'active', 1, 0, 0)`,
				link, target)
			e.withDecypharr(decypharrtest.Torrent{Hash: hashA, Name: decyFolder, Provider: "realdebrid"})
			calls := e.listFiles(c.list...)
			scans, err := e.lib.Relink(e.ctx, "movie-remux", false)
			if err != nil {
				t.Fatal(err)
			}
			tg, _ := os.Readlink(link)
			moved := strings.Contains(tg, decyFolder)
			if moved != c.relinks || (len(scans[0].Relinked) == 1) != c.relinks {
				t.Errorf("relinked %v (target %q), want %v: %+v", moved, tg, c.relinks, scans[0])
			}
			if *calls != 1 {
				t.Errorf("file list read %d times in the sweep", *calls)
			}
		})
	}
}

// TestRelinkDecypharrListPerTorrent: a sweep reads a file list once per provider torrent, not once per hash: two
// downloads of one hash under two torrent ids each get their own.
func TestRelinkDecypharrListPerTorrent(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(decyFolder, map[string]float64{"movie.mkv": 7000, "extra.mkv": 7000})
	e.exec(`INSERT INTO content (id, info_hash, name, state, first_seen_at, last_seen_at) VALUES (1, ?, 'Movie.Release', 'present', 0, 0)`, hashA)
	e.download(1, "movie-remux", 1, 10, movieRelease, "Movie.Release", "[]")
	e.download(2, "movie-remux", 1, 10, movieRelease, "Movie.Release", "[]")
	e.exec(`UPDATE download SET provider_torrent_id = 'T' || id`)
	for i, f := range []string{"movie.mkv", "extra.mkv", "movie.mkv"} {
		link := filepath.Join(e.movies, "Movie (2001)", "Movie "+string(rune('a'+i))+".mkv")
		os.MkdirAll(filepath.Dir(link), 0o755)
		target := filepath.Join(e.mount, "__all__", "Movie.Release", f)
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		e.exec(`INSERT INTO link (path, target, title_id, state, download_id, created_at, updated_at) VALUES (?, ?, 1, 'active', ?, 0, 0)`,
			link, target, i/2+1)
	}
	e.withDecypharr(decypharrtest.Torrent{Hash: hashA, Name: decyFolder, Provider: "realdebrid"})
	calls := map[string]int{}
	e.lib.files = func(_ context.Context, _, id, _ string) ([]TorrentFile, bool) {
		calls[id]++
		return nil, false
	}
	if _, err := e.lib.Relink(e.ctx, "movie-remux", false); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls["T1"] != 1 || calls["T2"] != 1 {
		t.Errorf("file lists read: %v", calls)
	}
}

// TestDecypharrAcrossGenerations: a settings generation keeps the client (WithPolicy), and a client built for the
// same decypharr by a later generation takes over its index: neither reads decypharr again. While decypharr loads
// the mount is down for the library.
func TestDecypharrAcrossGenerations(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	srv := e.withDecypharr(decypharrtest.Torrent{Hash: hashA, Name: decyFolder, Provider: "realdebrid"})
	if names, known := e.lib.decypharrNames(e.ctx, hashA, false); !known || len(names) != 1 {
		t.Fatalf("names %q, known %v", names, known)
	}
	if err := e.lib.mountHealthy(e.ctx); err != nil {
		t.Fatal(err)
	}
	srv.Reset()

	next := e.lib.WithPolicy(e.pol, nil, nil, nil)
	if next.Decypharr() != e.lib.Decypharr() || next.files == nil != (e.lib.files == nil) {
		t.Fatal("the generation lost the decypharr client")
	}
	fresh := New(Options{Store: e.st, Policy: e.pol, Now: func() time.Time { return e.now },
		Decypharr: &Decypharr{Config: e.lib.decy.Config, GW: e.lib.decy.GW, Now: e.lib.decy.Now}})
	t.Cleanup(fresh.mount.closeHelpers)
	fresh.InheritMount(e.lib)
	if names, known := fresh.decypharrNames(e.ctx, hashA, false); !known || len(names) != 1 || names[0] != decyFolder {
		t.Errorf("inherited index: names %q, known %v", names, known)
	}
	if err := fresh.mountHealthy(e.ctx); err != nil {
		t.Error(err)
	}
	if reqs := srv.Requests(); len(reqs) != 0 {
		t.Errorf("requests after a settings generation: %q", reqs)
	}
	other := &Decypharr{Config: DecypharrConfig{URL: "http://other:8282"}}
	other.inherit(e.lib.decy)
	if other.ix != nil {
		t.Error("index carried to a different decypharr")
	}
	var none *Decypharr
	none.inherit(e.lib.decy) // no decypharr configured: a no-op

	// Loading: the mount is down for imports, sweeps and the trash purge; the health check says so.
	e.now = e.now.Add(2 * time.Minute)
	srv.Set(func(s *decypharrtest.Server) { s.Loading = true })
	if err := e.lib.DecypharrState(e.ctx); err != ErrDecypharrLoading {
		t.Errorf("state while loading: %v", err)
	}
	if err := e.lib.MountHealthy(e.ctx); err == nil || !strings.Contains(err.Error(), "loading") {
		t.Errorf("mount while decypharr loads: %v", err)
	}
	if _, err := e.lib.PurgeTrash(e.ctx); err == nil {
		t.Error("trash purge ran while decypharr loads")
	}
}
