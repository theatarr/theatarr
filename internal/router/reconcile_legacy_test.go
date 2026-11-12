package router

import (
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
)

// `router reconcile` lists every queued row an older build queued (no identity validation version): route jobs,
// and queued downloads. A dry run writes nothing. Apply ends the automatic rows (job failed and
// dismissed, download failed) and queues one re-search per title version with the union of their
// episodes; manual rows and running jobs are only listed; rows carrying the current identity are not listed; nothing
// is deleted and no provider is called. A second apply finds nothing more and queues nothing.
func TestReconcileLegacyQueue(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, policy.ModeActive)
	q := jobs.NewQueue(e.st)
	q.SetClock(func() time.Time { return e.clock })
	e.r.RegisterJobs(jobs.NewWorker(q, jobs.WorkerOptions{}), q)
	legacy := func(req Request) int64 {
		t.Helper()
		id, _, err := enqueueRoute(e.ctx, q, nil, routePayload{Request: req}, time.Time{}) // as an older build queued it
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	wanted := movieReq(cand(h("a"), "Movie.2024.2160p.WEB-DL-GRP"))
	wanted.Source = "wanted"
	autoJob := legacy(wanted)
	manualJob := legacy(grabReq(cand(h("b"), "Movie.2024.1080p.WEB-DL-GRP")))
	running := legacy(grabReq(cand(h("c"), "Movie.2024.720p.WEB-DL-GRP")))
	e.exec(`UPDATE job SET state = 'running' WHERE id = ?`, running)
	fresh := Request{TitleID: 2, VersionID: 20, Version: "series", Source: "shadow.rss",
		Candidates: []Candidate{cand(h("d"), "Show.S01E09.2160p.WEB-DL-GRP")}}
	freshJob, err := e.r.Enqueue(e.ctx, fresh, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
		episodes, source, release_data, created_at, updated_at) VALUES
		(7, 2, 20, 'series', ?, 'Show.S01E07E08', 'pub', 'queued', '[7,8]', 'wanted', '{}', 0, 0),
		(8, 2, 20, 'series', ?, 'Show.S01E10', 'pub', 'queued', '[10]', 'wanted',
			'{"route_identity":1,"route_origin":"search"}', 0, 0),
		(9, 2, 20, 'series', ?, 'Show.S01E11', 'pub', 'added', '[11]', 'wanted', '{}', 0, 0)`, h("e"), h("f"), h("g"))

	rep, err := ReconcileLegacyQueue(e.ctx, e.st, q, false, e.clock)
	if err != nil {
		t.Fatal(err)
	}
	job := func(id int64) string { return "job/" + strconv.FormatInt(id, 10) }
	want := map[string]string{job(autoJob): "cancel", job(manualJob): "list", job(running): "leave",
		"download/7": "cancel"}
	got := map[string]string{}
	for _, x := range rep.Rows {
		got[x.Kind+"/"+strconv.FormatInt(x.ID, 10)] = x.Action
	}
	if len(got) != len(want) {
		t.Fatalf("rows %v, want %v (fresh job %d, proved download 8 and added download 9 not listed)", got, want, freshJob)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("row %s action %q, want %q (all %v)", k, got[k], v, got)
		}
	}
	if rep.Ended != 2 || len(rep.Researches) != 2 || rep.Queued != 0 {
		t.Fatalf("dry run: ended %d researches %+v queued %d", rep.Ended, rep.Researches, rep.Queued)
	}
	if n := e.count(`SELECT COUNT(*) FROM job WHERE kind = ?`, JobResearch); n != 0 ||
		e.count(`SELECT COUNT(*) FROM job WHERE state = 'queued' AND kind = ?`, JobRoute) != 3 ||
		e.count(`SELECT COUNT(*) FROM download WHERE state = 'queued'`) != 2 {
		t.Fatal("dry run wrote")
	}

	rep, err = ReconcileLegacyQueue(e.ctx, e.st, q, true, e.clock)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Ended != 2 || rep.Queued != 2 {
		t.Fatalf("apply: ended %d queued %d", rep.Ended, rep.Queued)
	}
	state := func(q string, id int64) string {
		t.Helper()
		var s string
		if err := e.st.Reader().QueryRow(q, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	for _, c := range []struct {
		q    string
		id   int64
		want string
	}{
		{`SELECT state || COALESCE(dismissed_by, '') FROM job WHERE id = ?`, autoJob, "failedreconcile"},
		{`SELECT state FROM job WHERE id = ?`, manualJob, "queued"},
		{`SELECT state FROM job WHERE id = ?`, running, "running"},
		{`SELECT state FROM job WHERE id = ?`, freshJob, "queued"},
		{`SELECT state FROM download WHERE id = ?`, 7, "failed"},
		{`SELECT state FROM download WHERE id = ?`, 8, "queued"},
		{`SELECT state FROM download WHERE id = ?`, 9, "added"},
	} {
		if s := state(c.q, c.id); s != c.want {
			t.Fatalf("%s [%d] = %q, want %q", c.q, c.id, s, c.want)
		}
	}
	rs := researchJobs(e)
	if len(rs) != 2 {
		t.Fatalf("re-searches %+v", rs)
	}
	for _, r := range rs {
		if r.Reason != ReasonReconcile {
			t.Fatalf("reason %q", r.Reason)
		}
		if r.Version == "series" && (r.TitleID != 2 || !slices.Equal(r.Episodes, []int64{7, 8})) {
			t.Fatalf("series re-search %+v, want title 2 episodes 7 8", r)
		}
		if r.Version == "movie" && (r.TitleID != 1 || r.VersionID != 10) {
			t.Fatalf("movie re-search %+v", r)
		}
	}
	if e.writes() != 0 {
		t.Fatalf("reconcile called a provider: %d writes", e.writes())
	}
	if n := e.count(`SELECT COUNT(*) FROM download`); n != 3 {
		t.Fatalf("download rows %d, want 3 kept", n)
	}

	rep, err = ReconcileLegacyQueue(e.ctx, e.st, q, true, e.clock)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Ended != 0 || rep.Queued != 0 || len(researchJobs(e)) != 2 {
		t.Fatalf("second apply: ended %d queued %d", rep.Ended, rep.Queued)
	}
}
