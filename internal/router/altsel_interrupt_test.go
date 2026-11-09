package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The second selection's add (altRetry) whose answer is cut short lands on Real-Debrid; reconciliation
// adopts it and the poller selects the largest video alone, as the route meant, never the set RD just answered
// uncached for. The intent is recorded with the add attempt before the add is sent.
func TestAltSelectionSurvivesInterruptedAddAnswer(t *testing.T) {
	t.Parallel()
	e, f, _ := altEnv(t)
	f.rdCachedSel[h("a")+"|2"] = true
	lost := false
	f.serve("rd.test", func(w http.ResponseWriter, r *http.Request) {
		if !lost && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/addMagnet") && f.nextID == 1001 {
			lost = true
			// RD creates the second torrent, but its answer is cut short.
			f.realDebrid(httptest.NewRecorder(), r)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":`))
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/torrents") {
			writeJSON(w, http.StatusOK, []map[string]any{{"id": "RD1002", "hash": h("a"),
				"status": "waiting_files_selection"}})
			return
		}
		f.realDebrid(w, r)
	})
	res, err := e.r.Route(e.ctx, grabReq(cand(h("a"), "Movie.2024.2160p.WEB.H265-GRP")))
	if err != nil {
		t.Fatal(err)
	}
	if !lost {
		t.Fatalf("the second add was not sent: %+v", res)
	}
	e.clock = e.clock.Add(6 * time.Minute)
	got, err := e.r.reconcileAdding(e.ctx, res.DownloadID)
	if err != nil || got.rec != recFound {
		t.Fatalf("reconcile: %+v %v", got, err)
	}
	for range 2 {
		if _, err := e.r.Poll(e.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.rd["RD1002"].sel; got != "2" {
		state, _ := e.r.downloadState(e.ctx, res.DownloadID)
		t.Fatalf("the adopted second add selected %q, want 2 (state %s)", got, state)
	}
}
