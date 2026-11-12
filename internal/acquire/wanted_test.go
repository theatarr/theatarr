package acquire

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

func TestWantedListReadOnlyAndRound(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 100, AgainAfter: policy.Duration(6 * time.Hour), CutoffUnmet: policy.CutoffScore})
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := e.st.Writer().ExecContext(e.ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Keep just one missing series episode, and leave the parent filled: the list must not reflag it.
	exec(`UPDATE episode_version_state SET monitored = 0 WHERE episode_id <> 1001`)
	exec(`UPDATE version SET state = 'filled' WHERE id = 201`)
	exec(`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(4,'movie','Recent',0,0)`)
	exec(`INSERT INTO title_release_date(title_id,source,type,date) VALUES(4,'fixture','digital','2024-01-01')`)
	exec(`INSERT INTO version(id,title_id,policy_key,state,created_at,updated_at) VALUES(104,4,'movie','wanted',0,0)`)
	exec(`INSERT INTO acquire_run(id,source,title_id,version_id,version_key,target,state,results,accepted,message,created_at,updated_at)
 VALUES(7,'wanted',4,104,'movie','movie','nothing',3,0,'No acceptable results',?,?)`, store.Millis(t0.Add(-time.Hour)), store.Millis(t0.Add(-time.Hour)))
	exec(`INSERT INTO journal_decision(id,source,run_id,title_id,version_key,created_at) VALUES(19,'acquire:7',7,4,'movie',?)`, store.Millis(t0.Add(-time.Hour)))
	exec(`INSERT INTO download(title_id,version_id,version_key,info_hash,release_title,indexer,state,created_at,updated_at)
 VALUES(2,102,'movie',?,'x','pub','downloading',0,0)`, h("f"))
	runs, jobs := e.count(`SELECT COUNT(*) FROM acquire_run`), e.count(`SELECT COUNT(*) FROM job`)
	states := e.count(`SELECT SUM(updated_at) + SUM(state = 'wanted') FROM version`)
	missing, err := e.a.WantedList(e.ctx, WantedQuery{Only: OnlyMissing})
	if err != nil {
		t.Fatal(err)
	}
	if missing.Limit != 50 || missing.Offset != 0 || missing.SearchedLastHour != 0 || !missing.Enabled || !missing.DownloadsEnabled {
		t.Fatalf("settings: %+v", missing)
	}
	expected := map[string]string{"1/movie": "due", "2/movie": "in_flight", "4/movie": "searched_recently", "10/series": "due"}
	due := []string{}
	pos := 0
	for _, row := range missing.Items {
		key := fmt.Sprintf("%d/%s", row.TitleID, row.Version)
		if row.Waiting != expected[key] {
			t.Errorf("%s: waiting %s, want %s", key, row.Waiting, expected[key])
		}
		delete(expected, key)
		if row.Waiting == "due" {
			pos++
			if row.Position != pos {
				t.Errorf("position: %+v", row)
			}
			for _, target := range row.Targets {
				due = append(due, fmt.Sprintf("%d/%s/%s", row.TitleID, row.Version, target))
			}
		}
		if row.TitleID == 4 {
			if row.NextAt == nil || !row.NextAt.Equal(t0.Add(5*time.Hour)) || row.LastRun == nil || row.LastRun.ID != 7 || row.LastRun.Results != 3 || row.LastRun.Accepted != 0 || row.LastRun.State != "nothing" || row.LastRun.Message != "No acceptable results" || !row.LastRun.At.Equal(t0.Add(-time.Hour)) || row.DecisionID == nil || *row.DecisionID != 19 {
				t.Errorf("recent: %+v", row)
			}
		} else if row.LastRun != nil || row.NextAt != nil || row.DecisionID != nil {
			t.Errorf("never searched: %+v", row)
		}
	}
	if len(expected) > 0 {
		t.Fatalf("missing rows: %v", expected)
	}
	cutoff, err := e.a.WantedList(e.ctx, WantedQuery{Only: OnlyCutoff})
	if err != nil {
		t.Fatal(err)
	}
	if cutoff.Total != 1 {
		t.Fatalf("cutoff: %+v", cutoff)
	}
	for _, row := range cutoff.Items {
		if row.TitleID != 3 || row.Reason != "cutoff" {
			t.Errorf("cutoff row: %+v", row)
		}
		if row.Version == "movie" {
			if row.Waiting != "due" || row.Position != 3 {
				t.Errorf("cutoff due: %+v", row)
			}
			due = append(due, "3/movie/movie")
		}
	}
	page, err := e.a.WantedList(e.ctx, WantedQuery{Only: OnlyMissing, Waiting: "due", Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Items) != 1 || page.Items[0].Position != 2 || !reflect.DeepEqual(page.Counts, missing.Counts) {
		t.Errorf("page: %+v", page)
	}
	if runs != e.count(`SELECT COUNT(*) FROM acquire_run`) || jobs != e.count(`SELECT COUNT(*) FROM job`) || states != e.count(`SELECT SUM(updated_at) + SUM(state = 'wanted') FROM version`) {
		t.Fatal("WantedList wrote state")
	}
	sum, err := e.a.Wanted(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Queued != len(due) {
		t.Fatalf("round %+v, due %v", sum, due)
	}
	rows, err := e.st.Reader().QueryContext(e.ctx, `SELECT title_id,version_key,target FROM acquire_run WHERE id <> 7`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := []string{}
	for rows.Next() {
		var title int64
		var key, target string
		if err := rows.Scan(&title, &key, &target); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%d/%s/%s", title, key, target))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	sort.Strings(due)
	if !reflect.DeepEqual(got, due) {
		t.Fatalf("round queued %v, list due %v", got, due)
	}
}

func TestWantedListGatesAndPartialRecency(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{AgainAfter: policy.Duration(6 * time.Hour)})
	list := func(only string) *WantedList {
		t.Helper()
		l, err := e.a.WantedList(e.ctx, WantedQuery{Only: only, Version: "movie", Limit: 999})
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	assert := func(only, waiting string) {
		t.Helper()
		l := list(only)
		if l.Total == 0 || l.Limit != 200 {
			t.Fatalf("empty: %+v", l)
		}
		for _, r := range l.Items {
			if r.Waiting != waiting {
				t.Fatalf("%s want %s", r.Waiting, waiting)
			}
		}
	}
	e.pol.Router.Enabled = false
	assert(OnlyMissing, "downloads_off")
	e.a.set.Enabled = false
	assert(OnlyMissing, "loop_off")
	e.a.set.Enabled = true
	e.a.set.Missing = false
	assert(OnlyMissing, "loop_off")
	e.a.set.CutoffUnmet = policy.CutoffOff
	assert(OnlyCutoff, "loop_off")
	e.pol.Router.Enabled = true
	e.a.set.Missing = true
	at := t0.Add(-10 * time.Minute)
	if _, err := e.st.Writer().Exec(`INSERT INTO acquire_run(source,title_id,version_id,version_key,target,state,infra,created_at,updated_at)
 VALUES('wanted',1,101,'movie','movie','nothing',2,?,?)`, store.Millis(at), store.Millis(at)); err != nil {
		t.Fatal(err)
	}
	l := list(OnlyMissing)
	for _, r := range l.Items {
		if r.TitleID == 1 && (r.Waiting != "searched_recently" || r.NextAt == nil || !r.NextAt.Equal(at.Add(min(partialAgain, e.a.set.AgainAfter)))) {
			t.Errorf("partial: %+v", r)
		}
	}
	if l.SearchedLastHour != 1 {
		t.Errorf("last hour %d", l.SearchedLastHour)
	}
	if _, err := e.a.WantedList(e.ctx, WantedQuery{Only: "bad"}); err == nil {
		t.Fatal("bad only accepted")
	}
	unknown, err := e.a.WantedList(e.ctx, WantedQuery{Only: OnlyMissing, Version: "unknown"})
	if err != nil || unknown.Total != 0 {
		t.Fatalf("unknown version %+v %v", unknown, err)
	}
}

func TestWantedListMergedQueuePositions(t *testing.T) {
	e := newEnv(t, true, &policy.Wanted{PerHour: 100, AgainAfter: policy.Duration(time.Hour), CutoffUnmet: policy.CutoffScore})
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := e.st.Writer().Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE version SET monitored = 0 WHERE policy_key LIKE 'series%'`)
	// A was searched after B.
	for _, r := range []struct {
		id, vid, title int
		age            time.Duration
	}{{1, 101, 1, 2 * time.Hour}, {2, 102, 2, 3 * time.Hour}} {
		exec(`INSERT INTO acquire_run(id,source,title_id,version_id,version_key,target,state,created_at,updated_at) VALUES(?,'wanted',?,?,'movie','movie','nothing',?,0)`, r.id, r.title, r.vid, store.Millis(t0.Add(-r.age)))
	}
	positions := map[int64]int{}
	for _, only := range []string{OnlyMissing, OnlyCutoff} {
		list, err := e.a.WantedList(e.ctx, WantedQuery{Only: only})
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range list.Items {
			if row.Waiting == "due" {
				positions[row.TitleID] = row.Position
			}
		}
	}
	// Never-searched cutoff C comes first, then B, then A across both tabs.
	if !reflect.DeepEqual(positions, map[int64]int{3: 1, 2: 2, 1: 3}) {
		t.Fatalf("positions %v", positions)
	}
	if _, err := e.a.Wanted(e.ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := e.st.Reader().Query(`SELECT title_id FROM acquire_run WHERE id > 2 ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	pos := 0
	for rows.Next() {
		var title int64
		if err := rows.Scan(&title); err != nil {
			t.Fatal(err)
		}
		pos++
		if positions[title] != pos {
			t.Fatalf("round position %d title %d, list %v", pos, title, positions)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestWantedListPendingDownloadedAndPositionJSON(t *testing.T) {
	e := newEnv(t, true, nil)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := e.st.Writer().Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO acquire_run(id,source,title_id,version_id,version_key,target,state,accepted,created_at,updated_at) VALUES(91,'wanted',1,101,'movie','movie','routed',1,?,0),(92,'wanted',2,102,'movie','movie','routed',1,?,0)`, store.Millis(t0), store.Millis(t0))
	exec(`INSERT INTO download(title_id,version_id,version_key,info_hash,release_title,indexer,state,source,created_at,updated_at) VALUES(2,102,'movie',?,'release','pub','failed','acquire:92',0,0)`, h("f"))
	list, err := e.a.WantedList(e.ctx, WantedQuery{Only: OnlyMissing, Version: "movie"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	for _, row := range wire.Items {
		if _, ok := row["position"]; ok {
			t.Errorf("position on waiting row: %s", raw)
		}
	}
	for _, row := range wire.Items {
		run, ok := row["last_run"].(map[string]any)
		if !ok || run["downloaded"] != (row["title_id"] == float64(2)) {
			t.Errorf("downloaded: %+v", row)
		}
	}
	// A download that ended before anything was sent to a provider (Activity's "Not started") is not a grab.
	exec(`UPDATE download SET error = 'route stopped: router: paused' WHERE source = 'acquire:92'`)
	list, err = e.a.WantedList(e.ctx, WantedQuery{Only: OnlyMissing, Version: "movie"})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range list.Items {
		if it.LastRun != nil && it.LastRun.Downloaded {
			t.Errorf("not started counted as grabbed: %+v", it.LastRun)
		}
	}
}

func TestWantedListSharedPositions(t *testing.T) {
	e := newEnv(t, true, nil)
	e.pol.Versions["movie-shadow"].Mode = policy.ModeActive
	list, err := e.a.WantedList(e.ctx, WantedQuery{Only: OnlyMissing})
	if err != nil {
		t.Fatal(err)
	}
	positions := map[int64]int{}
	for _, row := range list.Items {
		if row.Waiting != "due" || row.Version == "series" {
			continue
		}
		if pos := positions[row.TitleID]; pos != 0 && pos != row.Position {
			t.Fatalf("shared title positions: %+v", list.Items)
		}
		positions[row.TitleID] = row.Position
	}
}

func TestWantedListTwoThousandTargetsOneFlightRead(t *testing.T) {
	e := newEnv(t, true, nil)
	tx, err := e.st.Writer().Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 1000; i < 3000; i++ {
		for _, q := range []string{
			`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(?,'movie','Fixture',0,0)`,
			`INSERT INTO title_release_date(title_id,source,type,date) VALUES(?,'fixture','digital','2024-01-01')`,
			`INSERT INTO version(title_id,policy_key,state,created_at,updated_at) VALUES(?,'movie','wanted',0,0)`,
		} {
			if _, err := tx.Exec(q, i); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Writer().Exec(`UPDATE version SET monitored=0 WHERE title_id < 1000`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Writer().Exec(`INSERT INTO download(title_id,version_id,version_key,info_hash,release_title,indexer,state,created_at,updated_at) SELECT title_id,id,policy_key,'flight','release','pub','downloading',0,0 FROM version WHERE title_id=1000`); err != nil {
		t.Fatal(err)
	}
	// No store query hook exists. A view counts reads of the single flight's two lookup columns.
	var reads atomic.Int64
	function := fmt.Sprintf("wanted_flight_read_%d", time.Now().UnixNano())
	if err := sqlite.RegisterScalarFunction(function, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		reads.Add(1)
		return args[0], nil
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := e.st.Reader().Query(`PRAGMA table_info(download)`)
	if err != nil {
		t.Fatal(err)
	}
	cols := []string{}
	for rows.Next() {
		var cid, nn, pk int
		var name, typ string
		var def any
		if err := rows.Scan(&cid, &name, &typ, &nn, &def, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "title_id" || name == "version_id" {
			cols = append(cols, function+"("+name+") AS "+name)
		} else {
			cols = append(cols, name)
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`ALTER TABLE download RENAME TO fixture_download`, `CREATE VIEW download AS SELECT ` + strings.Join(cols, ",") + ` FROM fixture_download`} {
		if _, err := e.st.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	// Reopen reader connections so the newly registered SQLite function is installed.
	e.st.Reader().SetMaxIdleConns(0)
	e.st.Reader().SetMaxIdleConns(2)
	list, err := e.a.WantedList(e.ctx, WantedQuery{Only: OnlyMissing, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 2000 || list.Counts["due"] != 1999 || list.Counts["in_flight"] != 1 || len(list.Items) != 200 {
		t.Fatalf("counts: %+v", list.Counts)
	}
	if reads.Load() != 2 {
		t.Fatalf("in-flight lookup column reads %d, want 2 (one download read)", reads.Load())
	}
}

// The list names each row's season and its place in the Search now queue, and "Search all" queues exactly the rows
// the list shows for the same filter, in the list's order.
func TestWantedListSearchNowQueue(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{Enabled: new(bool), PerHour: 1})
	e.accept = false
	before, err := e.a.WantedList(e.ctx, WantedQuery{Only: OnlyMissing, Version: "series"})
	if err != nil || before.Total != 3 || before.SearchNowPending != 0 || before.SearchNowNextAt != nil {
		t.Fatalf("before %+v %v", before, err)
	}
	for _, row := range before.Items {
		if row.Season == nil || row.SearchQueued != 0 || row.Targets[0][:3] != fmt.Sprintf("s%02d", *row.Season) {
			t.Errorf("row %+v", row)
		}
	}
	res, err := e.a.RequestSearchNowAll(e.ctx, WantedQuery{Only: OnlyMissing, Version: "series", Limit: 1})
	if err != nil || res.Queued != 3 || res.Started != 1 || res.Pending != 2 {
		t.Fatalf("all %+v %v", res, err)
	}
	var queued []int
	for _, r := range mustRequests(t, e) {
		if r.key != "series" || r.reason != OnlyMissing {
			t.Errorf("request %+v", r)
		}
		queued = append(queued, r.season)
	}
	// The first row's search started; the other two wait in the list's order.
	if want := []int{*before.Items[1].Season, *before.Items[2].Season}; !reflect.DeepEqual(queued, want) {
		t.Fatalf("queued seasons %v, want %v", queued, want)
	}
	after, err := e.a.WantedList(e.ctx, WantedQuery{Only: OnlyMissing})
	if err != nil || after.SearchNowPending != 2 || after.SearchNowNextAt == nil {
		t.Fatalf("after %+v %v", after, err)
	}
	places := map[int]int{}
	for _, row := range after.Items {
		switch {
		case row.Version == "movie" && (row.Season != nil || row.SearchQueued != 0):
			t.Errorf("movie row %+v", row)
		case row.Version == "series":
			places[*row.Season] = row.SearchQueued
		}
	}
	if places[queued[0]] != 1 || places[queued[1]] != 2 || places[*before.Items[0].Season] != 0 {
		t.Fatalf("places %v for queue %v", places, queued)
	}
	raw, _ := json.Marshal(after.Items[0])
	if after.Items[0].Version == "movie" && (strings.Contains(string(raw), `"season"`) || strings.Contains(string(raw), `"search_queued"`)) {
		t.Errorf("movie row JSON %s", raw)
	}
}

func mustRequests(t *testing.T, e *env) []searchRequest {
	t.Helper()
	reqs, err := e.a.searchRequests(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	return reqs
}
