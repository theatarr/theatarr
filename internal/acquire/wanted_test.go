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
	if missing.Limit != 50 || missing.Offset != 0 || missing.SearchedLastHour != 0 || !missing.DownloadsEnabled {
		t.Fatalf("settings: %+v", missing)
	}
	expected := map[string]string{"1/movie": "due", "2/movie": "in_flight", "4/movie": "scheduled", "10/series": "due"}
	due := []string{}
	for _, row := range missing.Items {
		key := fmt.Sprintf("%d/%s", row.TitleID, row.Version)
		if row.Waiting != expected[key] {
			t.Errorf("%s: waiting %s, want %s", key, row.Waiting, expected[key])
		}
		delete(expected, key)
		if row.Waiting == "due" {
			if row.NextAt == nil || !row.NextAt.Equal(t0) {
				t.Errorf("due from now: %+v", row)
			}
			for _, target := range row.Targets {
				due = append(due, fmt.Sprintf("%d/%s/%s", row.TitleID, row.Version, target))
			}
		}
		if row.TitleID == 4 {
			if row.NextAt == nil || !row.NextAt.Equal(t0.Add(5*time.Hour)) || row.LastRun == nil || row.LastRun.ID != 7 || row.LastRun.Results != 3 || row.LastRun.Accepted != 0 || row.LastRun.State != "nothing" || row.LastRun.Message != "No acceptable results" || !row.LastRun.At.Equal(t0.Add(-time.Hour)) || row.DecisionID == nil || *row.DecisionID != 19 {
				t.Errorf("recent: %+v", row)
			}
		} else if row.LastRun != nil || row.DecisionID != nil || (row.Waiting == "in_flight") != (row.NextAt == nil) {
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
			if row.Waiting != "due" {
				t.Errorf("cutoff due: %+v", row)
			}
			due = append(due, "3/movie/movie")
		}
	}
	page, err := e.a.WantedList(e.ctx, WantedQuery{Only: OnlyMissing, Waiting: "due", Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Items) != 1 || !reflect.DeepEqual(page.Counts, missing.Counts) {
		t.Errorf("page: %+v", page)
	}
	if runs != e.count(`SELECT COUNT(*) FROM acquire_run`) || jobs != e.count(`SELECT COUNT(*) FROM job`) || states != e.count(`SELECT SUM(updated_at) + SUM(state = 'wanted') FROM version`) {
		t.Fatal("WantedList wrote state")
	}
	sum, err := e.a.Round(e.ctx, nil)
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
	e.a.set.Missing = false
	assert(OnlyMissing, "downloads_off")
	e.pol.Router.Enabled = true
	assert(OnlyMissing, "not_scheduled")
	e.a.set.CutoffUnmet = policy.CutoffOff
	assert(OnlyCutoff, "not_scheduled")
	e.a.set.Missing = true
	at := t0.Add(-10 * time.Minute)
	if _, err := e.st.Writer().Exec(`INSERT INTO acquire_run(source,title_id,version_id,version_key,target,state,infra,created_at,updated_at)
 VALUES('wanted',1,101,'movie','movie','nothing',2,?,?)`, store.Millis(at), store.Millis(at)); err != nil {
		t.Fatal(err)
	}
	l := list(OnlyMissing)
	for _, r := range l.Items {
		if r.TitleID == 1 && (r.Waiting != "scheduled" || r.NextAt == nil || !r.NextAt.Equal(at.Add(min(partialAgain, e.a.set.AgainAfter)))) {
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

// A round starts the backlog never searched first, then the items searched longest ago, across both tabs.
func TestRoundBacklogOrder(t *testing.T) {
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
	if _, err := e.a.Round(e.ctx, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := e.st.Reader().Query(`SELECT title_id FROM acquire_run WHERE id > 2 ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []int64
	for rows.Next() {
		var title int64
		if err := rows.Scan(&title); err != nil {
			t.Fatal(err)
		}
		got = append(got, title)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// Never-searched cutoff C comes first, then B, then A.
	if !reflect.DeepEqual(got, []int64{3, 2, 1}) {
		t.Fatalf("round order %v", got)
	}
}

func TestWantedListDownloaded(t *testing.T) {
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
	for _, row := range list.Items {
		if row.LastRun == nil || row.LastRun.Downloaded != (row.TitleID == 2) {
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

// The list names each row's season and the due Search now rows, and "Search all" asks for exactly
// the rows the list shows for the same filter; what the hour does not allow stays due.
func TestWantedListSearchNowQueue(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{Missing: new(false), CutoffUnmet: policy.CutoffOff, PerHour: 1})
	e.accept = false
	before, err := e.a.WantedList(e.ctx, WantedQuery{Only: OnlyMissing, Version: "series"})
	if err != nil || before.Total != 3 || before.SearchNowPending != 0 {
		t.Fatalf("before %+v %v", before, err)
	}
	for _, row := range before.Items {
		if row.Season == nil || row.Why != "" || row.Waiting != "not_scheduled" || row.Targets[0][:3] != fmt.Sprintf("s%02d", *row.Season) {
			t.Errorf("row %+v", row)
		}
	}
	res, err := e.a.RequestSearchNowAll(e.ctx, WantedQuery{Only: OnlyMissing, Version: "series", Limit: 1})
	if err != nil || res.Queued != 3 || res.Started != 1 || res.Pending != 2 {
		t.Fatalf("all %+v %v", res, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM search_request WHERE version_key = 'series' AND reason = 'missing' AND why = 'asked'`); n != 3 {
		t.Fatalf("%d asked rows", n)
	}
	// Asking again counts nothing new: two rows are due already, the third's search is queued.
	if again, err := e.a.RequestSearchNowAll(e.ctx, WantedQuery{Only: OnlyMissing, Version: "series"}); err != nil ||
		again.Queued != 0 || again.Started != 0 || len(again.Skipped) != 1 || again.Skipped[0].Reason != skipSearching {
		t.Fatalf("again %+v %v", again, err)
	}
	after, err := e.a.WantedList(e.ctx, WantedQuery{Only: OnlyMissing})
	if err != nil || after.SearchNowPending != 2 {
		t.Fatalf("after %+v %v", after, err)
	}
	asked := 0
	for _, row := range after.Items {
		switch {
		case row.Version == "movie" && (row.Season != nil || row.Why != ""):
			t.Errorf("movie row %+v", row)
		case row.Version == "series" && row.Waiting == WaitDue && row.Why == whyAsked:
			asked++
		}
	}
	if asked != 2 {
		t.Fatalf("%d due asked rows: %+v", asked, after.Items)
	}
	raw, _ := json.Marshal(after.Items[0])
	if after.Items[0].Version == "movie" && (strings.Contains(string(raw), `"season"`) || strings.Contains(string(raw), `"why"`)) {
		t.Errorf("movie row JSON %s", raw)
	}
}

// Each waiting state of the Wanted list with its next search time, why and tries: a scheduled retry of a new release,
// a due failure re-search (its stored time), a regular row due later, a download in flight. With the switch off only
// rows with a why stay scheduled; while paused no row has a time.
func TestWantedListNextSearch(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 10})
	for _, id := range []int{4, 5} {
		e.exec(`INSERT INTO title (id, kind, name, created_at, updated_at) VALUES (?, 'movie', 'M', 0, 0)`, id)
		e.exec(`INSERT INTO title_release_date (title_id, source, type, date) VALUES (?, 'fixture', 'digital', '2024-01-01')`, id)
		e.exec(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (?, ?, 'movie', 'wanted', 0, 0)`, 100+id, id)
	}
	ms := func(d time.Duration) int64 { return store.Millis(t0.Add(d)) }
	e.exec(`INSERT INTO search_request (title_id, version_key, reason, season, created_at, next_at, tries, why) VALUES
		(1, 'movie', 'missing', -1, 0, ?, 2, 'new'), (4, 'movie', 'missing', -1, 0, ?, 0, 'stalled'),
		(5, 'movie', 'missing', -1, 0, ?, 0, NULL)`, ms(2*time.Hour), ms(-10*time.Minute), ms(3*time.Hour))
	e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, state, created_at, updated_at)
		VALUES (2, 102, 'movie', ?, 'x', 'pub', 'downloading', 0, 0)`, h("f"))
	type want struct {
		waiting, why string
		tries        int
		at           *time.Duration
	}
	at := func(d time.Duration) *time.Duration { return &d }
	check := func(name string, wants map[int64]want) {
		t.Helper()
		l, err := e.a.WantedList(e.ctx, WantedQuery{Only: OnlyMissing, Version: "movie"})
		if err != nil {
			t.Fatal(err)
		}
		if l.Total != len(wants) {
			t.Fatalf("%s: %d rows", name, l.Total)
		}
		for _, r := range l.Items {
			w := wants[r.TitleID]
			ok := r.Waiting == w.waiting && r.Why == w.why && r.Tries == w.tries && (r.NextAt == nil) == (w.at == nil)
			if ok && w.at != nil {
				ok = r.NextAt.Equal(t0.Add(*w.at))
			}
			if !ok {
				t.Errorf("%s: title %d: %+v, want %+v", name, r.TitleID, r, w)
			}
		}
	}
	check("on", map[int64]want{
		1: {WaitScheduled, whyNew, 2, at(2 * time.Hour)},
		2: {WaitInFlight, "", 0, nil},
		4: {WaitDue, "stalled", 0, at(-10 * time.Minute)},
		5: {WaitScheduled, "", 0, at(3 * time.Hour)},
	})
	// One title (the title page): its rows only, the same as in the whole list.
	if one, err := e.a.WantedList(e.ctx, WantedQuery{Only: OnlyMissing, TitleID: 4}); err != nil || one.Total != 1 ||
		one.Items[0].Waiting != WaitDue || one.Items[0].Why != "stalled" {
		t.Fatalf("title 4: %+v %v", one, err)
	}
	e.a.set.Missing = false
	check("switch off", map[int64]want{
		1: {WaitScheduled, whyNew, 2, at(2 * time.Hour)},
		2: {WaitInFlight, "", 0, nil},
		4: {WaitDue, "stalled", 0, at(-10 * time.Minute)},
		5: {WaitNotScheduled, "", 0, nil},
	})
	e.pol.Router.Enabled = false
	check("paused", map[int64]want{
		1: {WaitDownloadsOff, whyNew, 2, nil},
		2: {WaitDownloadsOff, "", 0, nil},
		4: {WaitDownloadsOff, "stalled", 0, nil},
		5: {WaitDownloadsOff, "", 0, nil},
	})
}

// A season row's last run is the newest run of that library and season, whatever its target: an episode search of
// the season counts, as it does for the schedule.
func TestWantedListLastRunBySeason(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	vid := e.count(`SELECT id FROM version WHERE title_id = 10 AND policy_key = 'series'`)
	e.exec(`INSERT INTO acquire_run (id, source, title_id, version_id, version_key, target, season, state, results, created_at, updated_at)
		VALUES (77, 'command', 10, ?, 'series', 's01e01', 1, 'nothing', 2, ?, ?)`, vid, store.Millis(t0.Add(-time.Hour)), store.Millis(t0.Add(-time.Hour)))
	l, err := e.a.WantedList(e.ctx, WantedQuery{Only: OnlyMissing, Version: "series"})
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, r := range l.Items {
		switch {
		case r.Season != nil && *r.Season == 1:
			seen = true
			if r.LastRun == nil || r.LastRun.ID != 77 {
				t.Errorf("season 1: %+v %+v", r, r.LastRun)
			}
		case r.LastRun != nil:
			t.Errorf("other season with a run: %+v", r)
		}
	}
	if !seen {
		t.Fatalf("no season 1 row: %+v", l.Items)
	}
}
