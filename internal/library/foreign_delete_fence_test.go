package library

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/torrents/reffence"
)

// foreignDelete puts another process's torrent delete lease in place.
func foreignDelete(t *testing.T, e *env) {
	t.Helper()
	e.exec(`INSERT INTO library_lease (name, owner, until) VALUES (?, 'pid 1 at 1', ?)`, reffence.LeaseName,
		store.Millis(e.now.Add(time.Minute)))
}

const adoptedAmelie = "Amelie The Movie (2001) {imdb-tt0211915} - [Remux-2160p]-GRP.mkv"

// While another process deletes a torrent, adopt makes no symlink (the old code made it, then had its
// row refused, leaving an unrecorded link at what the delete takes).
func TestAdoptNoSymlinkDuringForeignDelete(t *testing.T) {
	t.Parallel()
	e := newParallelEnv(t, true)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.arrFile(1, "radarr", 10, 1, filepath.Join(e.movies, "Amelie The Movie (2001)", "Amelie.mkv"), movieRelease+"/movie.mkv", "Remux-2160p")
	foreignDelete(t, e.env)
	rep, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "movie-theatarr"})
	want := filepath.Join(e.theatarrMovies, "Amelie The Movie (2001)", adoptedAmelie)
	if tg, rerr := os.Readlink(want); rerr == nil {
		t.Fatalf("link made during another process's delete: -> %s (report %+v, err %v)", tg, rep, err)
	}
	if err != nil || rep.Linked != 0 || rep.Skipped[AdoptConflict] != 1 {
		t.Fatalf("adopt %+v %v", rep, err)
	}
	e.exec(`DELETE FROM library_lease`)
	if rep, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "movie-theatarr"}); err != nil || rep.Linked != 1 {
		t.Fatalf("adopt after the delete %+v %v", rep, err)
	}
}

// An adopt that waits for a delete of this process makes its link only after it, and checks the
// target again: the delete took it, so no link to deleted content is made.
func TestAdoptWaitsForDeleteAndRechecksTarget(t *testing.T) {
	e := newParallelEnv(t, true)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.arrFile(1, "radarr", 10, 1, filepath.Join(e.movies, "Amelie The Movie (2001)", "Amelie.mkv"), movieRelease+"/movie.mkv", "Remux-2160p")
	type res struct {
		rep *AdoptReport
		err error
	}
	done := make(chan res, 1)
	t.Cleanup(func() { <-done }) // after release (cleanups run last first)
	hold, err := reffence.Hold(e.ctx, e.st, e.now)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() { once.Do(hold) }
	t.Cleanup(release)
	go func() {
		rep, err := e.lib.Adopt(e.ctx, AdoptOptions{Version: "movie-theatarr"})
		done <- res{rep, err}
	}()
	want := filepath.Join(e.theatarrMovies, "Amelie The Movie (2001)", adoptedAmelie)
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Lstat(want); err == nil {
		t.Fatal("link made while a delete of this process held the fence")
	}
	select {
	case r := <-done:
		t.Fatalf("adopt finished during the delete: %+v %v", r.rep, r.err)
	default:
	}
	// The delete takes the torrent, then lets go.
	if err := os.RemoveAll(filepath.Join(e.mount, "__all__", movieRelease)); err != nil {
		t.Fatal(err)
	}
	release()
	r := <-done
	done <- r
	if r.err != nil || r.rep.Linked != 0 || r.rep.Skipped[AdoptDangling] != 1 {
		t.Fatalf("adopt after the delete %+v %v", r.rep, r.err)
	}
	if _, err := os.Lstat(want); err == nil {
		t.Fatal("link made to the deleted torrent")
	}
	if n := e.one(`SELECT COUNT(*) FROM library_lease WHERE name LIKE 'torrents:%'`); n != "0" {
		t.Fatalf("%s fence leases left", n)
	}
}

// An import during another process's delete makes no link before its commit is refused.
func TestImportNoSymlinkDuringForeignDelete(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release(movieRelease, map[string]float64{"movie.mkv": 7000})
	e.download(1, "movie-remux", 1, 10, movieRelease, movieRelease, "[]")
	plan, err := e.lib.Import(e.ctx, 1, true)
	if err != nil || len(plan.Links) != 1 {
		t.Fatalf("dry run %+v %v", plan, err)
	}
	foreignDelete(t, e)
	if _, err := e.lib.Import(e.ctx, 1, false); err == nil {
		t.Fatal("import committed during another process's delete")
	}
	if tg, err := os.Readlink(plan.Links[0].Path); err == nil {
		t.Fatalf("link made during another process's delete: -> %s", tg)
	}
	e.exec(`DELETE FROM library_lease`)
	if _, err := e.lib.Import(e.ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Readlink(plan.Links[0].Path); err != nil {
		t.Fatalf("no link after the delete: %v", err)
	}
}

// A relink during another process's delete leaves the symlink on its old target (the old code
// repointed it on disk, then had its row refused).
func TestRelinkNoRepointDuringForeignDelete(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	e.release("Name {abcdef}", map[string]float64{"episode.mkv": 7000})
	p := filepath.Join(e.movies, "Movie (2001)", "Movie.mkv")
	old := filepath.Join(e.mount, "__all__", "Name", "episode.mkv")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(old, p); err != nil {
		t.Fatal(err)
	}
	foreignDelete(t, e)
	if _, err := e.lib.RelinkRenamed(e.ctx, map[string]string{"Name": "Name {abcdef}"}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.Readlink(p); err != nil || got != old {
		t.Fatalf("relinked during another process's delete: %q %v", got, err)
	}
	e.exec(`DELETE FROM library_lease`)
	if _, err := e.lib.RelinkRenamed(e.ctx, map[string]string{"Name": "Name {abcdef}"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(p); got != filepath.Join(e.mount, "__all__", "Name {abcdef}", "episode.mkv") {
		t.Fatalf("not relinked after the delete: %q", got)
	}
}
