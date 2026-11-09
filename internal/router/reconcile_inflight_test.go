package router

import (
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
)

// Reconcile leaves work in flight alone: the queued download a running route job works on is listed
// and left queued, and a queued legacy job continuing a download whose add is in flight (adding) is listed and left
// queued; neither gets a re-search.
func TestReconcileLeavesWorkInFlight(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, download, job string
	}{
		{"running route, queued download", StateQueued, jobs.StateRunning},
		{"queued continuation, adding download", StateAdding, jobs.StateQueued},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, true, policy.ModeActive)
			q := jobs.NewQueue(e.st)
			e.exec(`INSERT INTO download (id,title_id,version_id,version_key,info_hash,release_title,indexer,state,episodes,
				source,release_data,created_at,updated_at) VALUES (7,1,10,'movie',?,'Movie 2024','pub',?,'[]','wanted','{}',0,0)`,
				h("a"), tc.download)
			req := movieReq(cand(h("a"), "Movie.2024.2160p.WEB-DL-GRP"))
			req.Source, req.DownloadID = "wanted", 7
			id, _, err := enqueueRoute(e.ctx, q, nil, routePayload{Request: req}, time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			e.exec(`UPDATE job SET state = ? WHERE id = ?`, tc.job, id)
			rep, err := ReconcileLegacyQueue(e.ctx, e.st, q, true, e.clock)
			if err != nil {
				t.Fatal(err)
			}
			var dl, job string
			if err := e.st.Reader().QueryRow(`SELECT state FROM download WHERE id = 7`).Scan(&dl); err != nil {
				t.Fatal(err)
			}
			if err := e.st.Reader().QueryRow(`SELECT state FROM job WHERE id = ?`, id).Scan(&job); err != nil {
				t.Fatal(err)
			}
			if dl != tc.download || job != tc.job || rep.Ended != 0 || len(rep.Researches) != 0 {
				t.Fatalf("work in flight touched: download %s job %s; report %+v", dl, job, rep)
			}
			if len(rep.Rows) == 0 {
				t.Fatal("legacy row in flight not listed")
			}
			for _, r := range rep.Rows {
				if r.Action != "leave" {
					t.Fatalf("row %+v: want leave", r)
				}
			}
		})
	}
}
