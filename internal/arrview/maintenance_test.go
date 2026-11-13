package arrview

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/router"
)

type commandMetadata struct {
	q      *jobs.Queue
	titles []int64
}

func (m *commandMetadata) Refresh(ctx context.Context, id int64) (int64, error) {
	m.titles = append(m.titles, id)
	job, _, err := m.q.Enqueue(ctx, "meta.art", map[string]int64{"title_id": id}, jobs.EnqueueOptions{
		DedupeKey: fmt.Sprintf("meta.art:title:%d", id)})
	return job, err
}

func TestMetadataCommandsFollowJobs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		base, name, key string
		id, title       int64
	}{
		{"radarr", "RefreshMovie", "movieId", IDBase + 1, 1},
		{"sonarr", "RefreshSeries", "seriesId", IDBase + 10, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			q := jobs.NewQueue(e.st)
			m := &commandMetadata{q: q}
			e.svc.SetMaintenance(q, m)
			w := jobs.NewWorker(q, jobs.WorkerOptions{})
			fail := false
			var runningID string
			w.Handle("meta.art", func(ctx context.Context, j *jobs.Job) error {
				var p struct {
					Title int64 `json:"title_id"`
				}
				if err := j.Decode(&p); err != nil || p.Title != tc.title {
					t.Fatalf("payload %+v: %v", p, err)
				}
				var running map[string]any
				e.call("GET", "/"+tc.base+"/api/v3/command/"+runningID, nil, 200, &running)
				if running["status"] != "started" || running["started"] == nil {
					t.Errorf("running command: %v", running)
				}
				if fail {
					return jobs.Permanent(errors.New("metadata unavailable"))
				}
				return nil
			})
			post := func(body map[string]any) string {
				var c map[string]any
				e.call("POST", "/"+tc.base+"/api/v3/command", body, 201, &c)
				if c["status"] != "queued" || c["result"] != "unknown" {
					t.Fatalf("queued command: %v", c)
				}
				if _, ok := c["body"].(map[string]any)["_mmJobs"]; ok {
					t.Fatal("internal job references exposed")
				}
				return strconv.Itoa(int(c["id"].(float64)))
			}
			for _, shouldFail := range []bool{false, true} {
				fail = shouldFail
				runningID = post(map[string]any{"name": tc.name, tc.key: tc.id, "_MMJOBS": []int{999}})
				other := post(map[string]any{"name": tc.name, tc.key: tc.id})
				if !slices.Equal(m.titles, []int64{tc.title, tc.title}) && !shouldFail {
					t.Fatalf("resolved titles: %v", m.titles)
				}
				if ran, err := w.RunOne(ctx); !ran || err != nil {
					t.Fatalf("worker: %v %v", ran, err)
				}
				for _, id := range []string{runningID, other} {
					var c map[string]any
					e.call("GET", "/"+tc.base+"/api/v3/command/"+id, nil, 200, &c)
					want := "completed"
					if shouldFail {
						want = "failed"
						if c["message"] != "metadata unavailable" || c["result"] != "unsuccessful" {
							t.Errorf("failed: %v", c)
						}
					}
					if c["status"] != want || c["ended"] == nil {
						t.Errorf("finished: %v", c)
					}
				}
			}
			// A failed command stays terminal across service restarts and job cleanup.
			e.exec(`DELETE FROM job`)
			e.start()
			var c map[string]any
			e.call("GET", "/"+tc.base+"/api/v3/command/"+runningID, nil, 200, &c)
			if c["status"] != "failed" {
				t.Fatalf("lost terminal status: %v", c)
			}
		})
	}
}

func TestCommandDedupesQueuedAndTrimsFinished(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var first, second map[string]any
	q := jobs.NewQueue(e.st)
	e.svc.SetMaintenance(q, &commandMetadata{q: q})
	e.call("POST", "/radarr/api/v3/command", map[string]any{"name": "RefreshMovie", "movieId": IDBase + 1}, 201, &first)
	e.call("POST", "/radarr/api/v3/command", map[string]any{"name": "RefreshMovie", "movieId": IDBase + 1}, 201, &second)
	if first["id"] != second["id"] {
		t.Fatalf("dedupe ids: %v %v", first["id"], second["id"])
	}
	e.exec(`UPDATE arrview_command SET status = 'completed', ended_at = ? WHERE id = ?`, ms(testNow.Add(-25*time.Hour)), int64(first["id"].(float64)))
	if err := e.svc.TrimCommands(context.Background(), testNow); err != nil {
		t.Fatal(err)
	}
	if got := e.one(`SELECT COUNT(*) FROM arrview_command`); got != 0 {
		t.Fatalf("trim count %d", got)
	}
}

func TestMetadataCommandWaitsForAllTitlesAndRetries(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	q := jobs.NewQueue(e.st)
	now := time.Now()
	q.SetClock(func() time.Time { return now })
	m := &commandMetadata{q: q}
	e.svc.SetMaintenance(q, m)
	w := jobs.NewWorker(q, jobs.WorkerOptions{})
	w.Handle("meta.art", func(ctx context.Context, j *jobs.Job) error {
		if j.Attempt == 1 {
			return errors.New("temporary metadata failure")
		}
		return nil
	})
	var c map[string]any
	e.call("POST", "/radarr/api/v3/command", map[string]any{"name": "RefreshMovie"}, 201, &c)
	if !slices.Equal(m.titles, []int64{1, 2, 3}) {
		t.Fatalf("view titles: %v", m.titles)
	}
	id := strconv.Itoa(int(c["id"].(float64)))
	for i := range 6 {
		if ran, err := w.RunOne(ctx); !ran || err != nil {
			t.Fatalf("worker: %v %v", ran, err)
		}
		e.call("GET", "/radarr/api/v3/command/"+id, nil, 200, &c)
		want := "started"
		if i == 5 {
			want = "completed"
		}
		if c["status"] != want {
			t.Fatalf("after run %d: %v", i, c)
		}
		now = now.Add(time.Hour)
	}
}

type commandPoller struct {
	*router.Router
	calls int
	err   error
}

func (p *commandPoller) Poll(context.Context) (router.PollSummary, error) {
	p.calls++
	return router.PollSummary{}, p.err
}

func TestDownloadPollCommand(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	q := jobs.NewQueue(e.st)
	e.svc.SetMaintenance(q, nil)
	p := &commandPoller{}
	e.svc.router = p
	w := jobs.NewWorker(q, jobs.WorkerOptions{})
	e.svc.RegisterJobs(w)
	for _, fail := range []bool{false, true} {
		if fail {
			p.err = errors.New("poll failed")
			e.now = e.now.Add(31 * time.Second)
		}
		var c map[string]any
		e.call("POST", "/radarr/api/v3/command", map[string]any{"name": "RefreshMonitoredDownloads"}, 201, &c)
		if c["status"] != "queued" || p.calls != map[bool]int{false: 0, true: 1}[fail] {
			t.Fatalf("not queued: %v, polls %d", c, p.calls)
		}
		if ran, err := w.RunOne(context.Background()); !ran || err != nil {
			t.Fatalf("worker: %v %v", ran, err)
		}
		id := strconv.Itoa(int(c["id"].(float64)))
		e.call("GET", "/radarr/api/v3/command/"+id, nil, 200, &c)
		want := "completed"
		if fail {
			want = "failed"
		}
		if c["status"] != want {
			t.Fatalf("poll result: %v", c)
		}
	}
	if p.calls != 2 {
		t.Fatalf("polls %d", p.calls)
	}
}

func TestImportPassCommands(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		base, name, key, path string
		id                    int
		want                  []string
		removed               bool
	}{
		{"radarr", "RescanMovie", "movieId", "", IDBase + 1, []string{"1", "2"}, false},
		{"sonarr", "RescanSeries", "seriesId", "", IDBase + 10, []string{"3"}, false},
		{"radarr", "DownloadedMoviesScan", "", "/mount/alpha", 0, []string{"1"}, false},
		{"sonarr", "DownloadedEpisodesScan", "", "/mount/show", 0, []string{"3"}, false},
		{"radarr", "DownloadedMoviesScan", "", "/mount/alph", 0, nil, false}, // directory boundary
		{"radarr", "RescanMovie", "movieId", "", 12345, nil, false},
		{"radarr", "RescanMovie", "movieId", "", IDBase + 1, []string{"1"}, true},
	} {
		t.Run(tc.name+tc.path+strconv.Itoa(tc.id), func(t *testing.T) {
			e := newEnv(t)
			e.seedDownloads()
			e.exec(`UPDATE download SET state = 'ready' WHERE id IN (1, 2, 3)`)
			if tc.removed {
				e.exec(`INSERT INTO arrview_removed_version (version_id, view, removed_at)
					SELECT version_id, 'radarr', 0 FROM download WHERE id = 2`)
			}
			e.exec(`INSERT INTO library_import (download_id, state, mount_folder, created_at, updated_at)
				VALUES (1, 'waiting_mount', '/mount/alpha', 0, 0), (2, 'waiting_mount', '/mount/alpha-other', 0, 0),
				(3, 'waiting_mount', '/mount/show/season', 0, 0)`)
			q := jobs.NewQueue(e.st)
			e.svc.SetMaintenance(q, nil)
			lib := &fakeLibrary{plan: func(id int64) *library.Plan {
				return &library.Plan{DownloadID: id, Links: []library.PlannedLink{{Done: true}}}
			}}
			e.svc.library = lib
			w := jobs.NewWorker(q, jobs.WorkerOptions{})
			e.svc.RegisterJobs(w)
			body := map[string]any{"name": tc.name}
			if tc.key != "" {
				body[tc.key] = tc.id
			}
			if tc.path != "" {
				body["path"] = tc.path
			}
			var c map[string]any
			e.call("POST", "/"+tc.base+"/api/v3/command", body, 201, &c)
			id := strconv.Itoa(int(c["id"].(float64)))
			if tc.id != 12345 {
				if ran, err := w.RunOne(context.Background()); !ran || err != nil {
					t.Fatalf("worker: %v %v", ran, err)
				}
			}
			e.call("GET", "/"+tc.base+"/api/v3/command/"+id, nil, 200, &c)
			want := "completed"
			if len(tc.want) == 0 {
				want = "failed"
			}
			if c["status"] != want || !slices.Equal(lib.imports, tc.want) {
				t.Fatalf("command %v, imports %v, want %v", c, lib.imports, tc.want)
			}
		})
	}
}

func TestUnsupportedMaintenanceCommands(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for _, tc := range []struct{ base, name, message string }{
		{"radarr", "RenameMovie", "theatarr does not rename existing files"},
		{"sonarr", "RenameSeries", "theatarr does not rename existing files"},
		{"radarr", "RenameFiles", "theatarr does not rename existing files"},
		{"sonarr", "RenameFiles", "theatarr does not rename existing files"},
		{"radarr", "RefreshMovie", "metadata refresh is off"},
	} {
		var c map[string]any
		e.call("POST", "/"+tc.base+"/api/v3/command", map[string]any{"name": tc.name}, 201, &c)
		if c["status"] != "failed" || c["result"] != "unsuccessful" || !strings.Contains(c["message"].(string), tc.message) || c["ended"] == nil {
			t.Errorf("command: %v", c)
		}
	}
}

func TestImportPassFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, message string
		plan          library.Plan
	}{
		{"refused", "library disabled", library.Plan{Refused: "library disabled"}},
		{"waiting", "waiting for the library mount", library.Plan{Waiting: true}},
		{"rejected", "no files matched or passed the library guards", library.Plan{
			Rejected: []library.PlannedFile{{Reason: "sample"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.seedDownloads()
			e.exec(`UPDATE download SET state = 'ready' WHERE id = 1`)
			q := jobs.NewQueue(e.st)
			e.svc.SetMaintenance(q, nil)
			e.svc.library = &fakeLibrary{plan: func(id int64) *library.Plan {
				return &tc.plan
			}}
			w := jobs.NewWorker(q, jobs.WorkerOptions{})
			e.svc.RegisterJobs(w)
			var c map[string]any
			e.call("POST", "/radarr/api/v3/command", map[string]any{"name": "RescanMovie", "movieId": IDBase + 1}, 201, &c)
			id := strconv.Itoa(int(c["id"].(float64)))
			if ran, err := w.RunOne(context.Background()); !ran || err != nil {
				t.Fatalf("worker: %v %v", ran, err)
			}
			e.call("GET", "/radarr/api/v3/command/"+id, nil, 200, &c)
			if c["status"] != "failed" || !strings.Contains(c["message"].(string), tc.message) {
				t.Fatalf("refused import: %v", c)
			}
		})
	}
}

func TestDownloadPollCommandCooldownAcrossViewsAndRestart(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		t.Run(strconv.FormatBool(fail), func(t *testing.T) {
			e := newEnv(t)
			q := jobs.NewQueue(e.st)
			p := &commandPoller{}
			if fail {
				p.err = errors.New("poll failed")
			}
			start := func() *jobs.Worker {
				e.svc.SetMaintenance(q, nil)
				e.svc.router = p
				w := jobs.NewWorker(q, jobs.WorkerOptions{})
				e.svc.RegisterJobs(w)
				return w
			}
			w := start()
			command := map[string]any{"name": "RefreshMonitoredDownloads"}
			e.call("POST", "/radarr/api/v3/command", command, 201, nil)
			e.call("POST", "/sonarr/api/v3/command", command, 201, nil)
			if e.one(`SELECT COUNT(*) FROM job WHERE dedupe_key='arrview.download-poll'`) != 1 {
				t.Fatal("queued poll not deduplicated")
			}
			if ran, err := w.RunOne(context.Background()); !ran || err != nil {
				t.Fatalf("worker: %v %v", ran, err)
			}
			if p.calls != 1 {
				t.Fatalf("poll calls: %d", p.calls)
			}
			e.start()
			w = start()
			for _, view := range []string{"radarr", "sonarr", "v/movie-web/radarr"} {
				e.call("POST", "/"+view+"/api/v3/command", command, 201, nil)
			}
			if e.one(`SELECT COUNT(*) FROM job WHERE dedupe_key='arrview.download-poll'`) != 1 {
				t.Fatal("finished poll was not cooled down across restart")
			}
			if ran, err := w.RunOne(context.Background()); ran || err != nil {
				t.Fatalf("cooldown worker: %v %v", ran, err)
			}
			e.now = e.now.Add(30 * time.Second)
			e.call("POST", "/sonarr/api/v3/command", command, 201, nil)
			if ran, err := w.RunOne(context.Background()); !ran || err != nil {
				t.Fatalf("worker after cooldown: %v %v", ran, err)
			}
			if p.calls != 2 {
				t.Fatalf("poll calls after cooldown: %d", p.calls)
			}
		})
	}
}
