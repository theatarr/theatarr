package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

// Client-routed downloads (versions[*].add_via: client): the row names its client, not a provider.

// clientDownload is a ready movie download a decypharr client "dc" holds.
func (e *env) clientDownload(name, source string) {
	e.download(1, "movie-remux", 1, 10, movieRelease, name, "[]")
	e.exec(`UPDATE download SET provider = NULL, client = 'dc', source_path = NULLIF(?, '') WHERE id = 1`, source)
}

// symlinks makes dir hold one link per name → target.
func symlinks(t *testing.T, dir string, links map[string]string) {
	t.Helper()
	for name, target := range links {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
}

func TestImportClientDownloadFromMount(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	// import_from: mount: the client's torrent name is the mount folder.
	e.clientDownload(movieRelease, "")
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Links) != 1 || plan.Folder != filepath.Join(e.mount, "__all__", movieRelease) {
		t.Fatalf("plan %+v", plan)
	}
	if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "imported" {
		t.Errorf("download %s", got)
	}
}

func TestImportClientDownloadFolder(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000, "Extras/featurette.mkv": 30})
	// The client's folder (already mapped to local_path by the poller): a link per file, a sub-folder as the
	// torrent has it, and a file that is not a link (passed over).
	dir := filepath.Join(e.outside, "decypharr", "theatarr", "Amelie.2001.2160p")
	rel := filepath.Join(e.mount, "__all__", movieRelease)
	symlinks(t, dir, map[string]string{"movie.mkv": filepath.Join(rel, "movie.mkv"),
		"Extras/featurette.mkv": filepath.Join(rel, "Extras", "featurette.mkv")})
	if err := os.WriteFile(filepath.Join(dir, "info.nfo"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.clientDownload("Amelie.2001.2160p", dir)
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Folder != rel || len(plan.Links) != 1 {
		t.Fatalf("plan %+v", plan)
	}
	target, err := os.Readlink(plan.Links[0].Path)
	if err != nil || target != filepath.Join(rel, "movie.mkv") {
		t.Errorf("link %q %v: it must point into the mount, not the client's folder", target, err)
	}
}

func TestImportClientDownloadFolderRefused(t *testing.T) {
	t.Parallel()
	for name, links := range map[string]func(e *env) map[string]string{
		"two releases": func(e *env) map[string]string {
			return map[string]string{"a.mkv": filepath.Join(e.mount, "__all__", movieRelease, "movie.mkv"),
				"b.mkv": filepath.Join(e.mount, "__all__", "Other.Release-GRP", "movie.mkv")}
		},
		"off the mount": func(e *env) map[string]string {
			return map[string]string{"movie.mkv": filepath.Join(e.outside, "real.mkv")}
		},
		"no links": func(*env) map[string]string { return nil },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, true, policy.ModeActive)
			e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
			e.release("Other.Release-GRP", map[string]float64{"movie.mkv": 7000})
			dir := filepath.Join(e.outside, "dl", "Amelie")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			symlinks(t, dir, links(e))
			e.clientDownload(movieRelease, dir)
			if _, err := e.lib.Import(e.ctx, 1, false); err == nil || !strings.Contains(err.Error(), "not on the debrid mount") {
				t.Fatalf("import: %v", err)
			}
			if got := e.one(`SELECT COUNT(*) FROM link`); got != "0" {
				t.Errorf("%s links made", got)
			}
		})
	}
}

// A completed download whose folder is not there (decypharr's multi-season fan-out makes one folder per season
// and none for the torrent) imports from the mount, found by hash and names as in import_from: mount.
func TestImportClientDownloadFolderMissingImportsFromMount(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.clientDownload(movieRelease, filepath.Join(e.outside, "decypharr", "Amelie"))
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil || plan.Waiting || len(plan.Links) != 1 || plan.Folder != filepath.Join(e.mount, "__all__", movieRelease) {
		t.Fatalf("plan %+v, %v", plan, err)
	}
	if got := e.one(`SELECT state FROM download WHERE id = 1`); got != "imported" {
		t.Errorf("download %s", got)
	}
	// A provider download's missing path is still an error, as before.
	e2 := newEnv(t, true, policy.ModeActive)
	e2.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	e2.exec(`UPDATE download SET source_path = ? WHERE id = 1`, filepath.Join(e2.outside, "nothing"))
	if _, err := e2.lib.Import(e2.ctx, 1, true); err == nil {
		t.Error("a provider download's missing source path imported")
	}
}

// The client's remote_path → local_path mapping applies to its links' absolute targets too: decypharr sees the
// mount under its own path.
func TestImportClientDownloadFolderMapsLinkTargets(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.pol.Clients = map[string]*policy.Client{"dc": {Kind: "decypharr", ImportFrom: policy.ImportFromDownloadFolder,
		RemotePath: "/client-data", LocalPath: filepath.Dir(e.mount)}}
	dir := filepath.Join(e.outside, "decypharr", "Amelie")
	symlinks(t, dir, map[string]string{"movie.mkv": filepath.Join("/client-data", filepath.Base(e.mount), "__all__",
		movieRelease, "movie.mkv")})
	e.clientDownload(movieRelease, dir)
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil || len(plan.Links) != 1 || plan.Folder != filepath.Join(e.mount, "__all__", movieRelease) {
		t.Fatalf("plan %+v, %v", plan, err)
	}
	// Unmapped, a target outside the mount is still refused.
	e2 := newEnv(t, true, policy.ModeActive)
	e2.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	dir2 := filepath.Join(e2.outside, "decypharr", "Amelie")
	symlinks(t, dir2, map[string]string{"movie.mkv": filepath.Join("/client-data", filepath.Base(e2.mount), "__all__",
		movieRelease, "movie.mkv")})
	e2.clientDownload(movieRelease, dir2)
	if _, err := e2.lib.Import(e2.ctx, 1, false); err == nil || !strings.Contains(err.Error(), "not on the debrid mount") {
		t.Fatalf("unmapped: %v", err)
	}
}

// A client folder mapped to a path under the mount root that is not there (a multi-season fan-out under a mount
// path) imports from the mount by hash and names, as for a missing folder elsewhere.
func TestImportClientDownloadFolderMissingUnderTheMount(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.clientDownload(movieRelease, filepath.Join(e.mount, "__all__", "Amelie.Not.There"))
	plan, err := e.lib.Import(e.ctx, 1, false)
	if err != nil || plan.Waiting || len(plan.Links) != 1 || plan.Folder != filepath.Join(e.mount, "__all__", movieRelease) {
		t.Fatalf("plan %+v, %v", plan, err)
	}
}
