package arrview

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"testing"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/library"
)

// reconcilingLibrary is a fakeLibrary that also reconciles its links (library.Library.ReconcileLinks).
type reconcilingLibrary struct {
	*fakeLibrary
	reconciled []string
}

func (f *reconcilingLibrary) ReconcileLinks(_ context.Context, version string, titleIDs []int64) ([]*library.LinkScan, error) {
	f.reconciled = append(f.reconciled, fmt.Sprint(version, titleIDs))
	return []*library.LinkScan{{Version: version, Vanished: []library.VanishedLink{{LinkID: 1}}}}, nil
}

// Rescan checks the titles' links against the disk (a deleted symlink stops counting) and succeeds with nothing to
// import; the downloaded-scan commands only import.
func TestRescanReconcilesLinks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		base, name, key string
		id              int
		reconciled      []string
		status          string
	}{
		{"radarr", "RescanMovie", "movieId", IDBase + 1, []string{"anime-movie[1]", "movie-remux[1]", "movie-web[1]"}, "completed"},
		{"radarr", "DownloadedMoviesScan", "", 0, nil, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			q := jobs.NewQueue(e.st)
			e.svc.SetMaintenance(q, nil)
			lib := &reconcilingLibrary{fakeLibrary: &fakeLibrary{}}
			e.svc.library = lib
			w := jobs.NewWorker(q, jobs.WorkerOptions{})
			e.svc.RegisterJobs(w)
			body := map[string]any{"name": tc.name}
			if tc.key != "" {
				body[tc.key] = tc.id
			}
			var c map[string]any
			e.call("POST", "/"+tc.base+"/api/v3/command", body, 201, &c)
			id := strconv.Itoa(int(c["id"].(float64)))
			if ran, err := w.RunOne(context.Background()); !ran || err != nil {
				t.Fatalf("worker: %v %v", ran, err)
			}
			e.call("GET", "/"+tc.base+"/api/v3/command/"+id, nil, 200, &c)
			slices.Sort(lib.reconciled)
			if c["status"] != tc.status || !slices.Equal(lib.reconciled, tc.reconciled) || len(lib.imports) != 0 {
				t.Fatalf("command %v, reconciled %v, imports %v", c, lib.reconciled, lib.imports)
			}
		})
	}
}
