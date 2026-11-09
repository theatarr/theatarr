package library

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
)

// An unlink queues one Plex partial scan of the link's folder, so Plex drops the file; a refused unlink queues none.
func TestUnlinkQueuesPlexScan(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{true, false} {
		e := newEnv(t, enabled, policy.ModeActive)
		e.release("Name", map[string]float64{"episode.mkv": 7000})
		p := filepath.Join(e.movies, "Movie (2001)", "Movie.mkv")
		tgt := filepath.Join(e.mount, "__all__", "Name", "episode.mkv")
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.Symlink(tgt, p); err != nil {
			t.Fatal(err)
		}
		e.exec(`INSERT INTO link (id, path, target, title_id, release_title, quality, state, created_at, updated_at)
			VALUES (1, ?, ?, 1, 'Name', 'WEBDL-1080p', 'active', 0, 0)`, p, tgt)
		e.exec(`INSERT INTO link_version (link_id, version_id) VALUES (1, 10)`)
		res, err := e.lib.Unlink(e.ctx, "movie-remux", p, 1, "deleted")
		if err != nil {
			t.Fatal(err)
		}
		w := jobs.NewWorker(e.queue, jobs.WorkerOptions{Owner: "t"})
		fs := &fakeScanner{}
		e.lib.RegisterJobs(w, fs)
		e.now = e.now.Add(time.Minute)
		for {
			ran, err := w.RunOne(e.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !ran {
				break
			}
		}
		if enabled {
			if res.DryRun || len(fs.calls) != 1 || fs.calls[0] != "18 "+filepath.Dir(p) {
				t.Errorf("unlink %+v: scans %v", res, fs.calls)
			}
		} else if !res.DryRun || len(fs.calls) != 0 || e.one(`SELECT COUNT(*) FROM job WHERE kind = 'plex.scan'`) != "0" {
			t.Errorf("refused unlink %+v: scans %v", res, fs.calls)
		}
	}
}
