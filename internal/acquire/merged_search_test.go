package acquire

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// addSeries adds series title id with version vid of library "series": seasons 1..n of two aired, wanted episodes
// each (ids id*100 + 10*season + number).
func (e *env) addSeries(id, vid int64, n int) {
	e.t.Helper()
	aired := store.Millis(t0.Add(-30 * 24 * time.Hour))
	e.exec(`INSERT INTO title (id, kind, name, year, tvdb_id, created_at, updated_at) VALUES (?, 'series', ?, 2020, ?, 0, 0)`,
		id, fmt.Sprintf("Series%d", id), id)
	e.exec(`INSERT INTO version (id, title_id, policy_key, state, created_at, updated_at) VALUES (?, ?, 'series', 'wanted', 0, 0)`,
		vid, id)
	for s := 1; s <= n; s++ {
		for k := 1; k <= 2; k++ {
			ep := id*100 + int64(10*s+k)
			e.exec(`INSERT INTO episode (id, title_id, season, number, air_date_utc_at, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, 0, 0)`, ep, id, s, k, aired)
			e.exec(`INSERT INTO episode_version_state (version_id, episode_id, state, updated_at) VALUES (?, ?, 'wanted', 0)`,
				vid, ep)
		}
	}
}

// mergedFake is a fake shared search: a merged target gets one season pack per member whose season accept takes;
// any other target nothing. It records the targets it was given.
type mergedFake struct {
	mu      sync.Mutex
	targets [][]router.SearchTarget
	accept  map[int]bool
}

func (f *mergedFake) search(_ context.Context, t decide.Title, targets []router.SearchTarget) (map[string]*router.Searched, error) {
	f.mu.Lock()
	f.targets = append(f.targets, targets)
	f.mu.Unlock()
	out := map[string]*router.Searched{}
	for _, tg := range targets {
		s := &router.Searched{Releases: map[*decide.Decision]indexer.Release{}}
		s.Request = router.Request{TitleID: t.ID, VersionID: tg.Version.ID, Version: tg.Version.Key, Merged: len(tg.Members) > 0}
		for _, m := range tg.Members {
			rel := indexer.Release{Indexer: "pub", Kind: "torznab", Title: fmt.Sprintf("%s.S%02d.2160p.WEB-DL-GRP", t.Name, m.Season),
				InfoHash: h(string(rune('a' + m.Season))), Privacy: policy.PrivacyPublic, Size: 10 << 30}
			d := &decide.Decision{Release: rel.Title, Version: tg.Version.Key, Accepted: f.accept[m.Season], EpisodeIDs: m.Episodes}
			if !d.Accepted {
				d.Rejections = []decide.Rejection{{Reason: "QualityNotWanted", Message: "no"}}
			}
			s.Ranked = append(s.Ranked, d)
			s.Releases[d] = rel
			s.Members = append(s.Members, router.MemberSearched{Ranked: []*decide.Decision{d}})
			if d.Accepted {
				s.Request.Candidates = append(s.Request.Candidates, router.Candidate{Release: rel, Quality: "WEBDL-2160p",
					Episodes: m.Episodes})
			}
		}
		out[tg.ID] = s
	}
	return out, nil
}

func (f *mergedFake) calls() [][]router.SearchTarget {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.targets)
}

// Which season targets are one merged search: two or more seasons above 0 of one standard or daily series version
// with the same reason and why, from a selection naming no season or episodes; nothing else.
func TestMergeGroups(t *testing.T) {
	t.Parallel()
	s := func(n int) *int { return &n }
	tg := func(version int64, name string, season int, mod func(*target)) target {
		x := target{titleID: 1, versionID: version, key: fmt.Sprint(version), name: name, season: s(season), whole: true}
		if mod != nil {
			mod(&x)
		}
		return x
	}
	shape := func(ts ...target) string {
		var out []string
		for _, g := range mergeGroups(groupTargets(ts)) {
			var names []string
			for _, x := range g {
				names = append(names, fmt.Sprintf("%d:%s", x.versionID, x.name))
			}
			out = append(out, strings.Join(names, "+"))
		}
		return strings.Join(out, " ")
	}
	for _, c := range []struct {
		name string
		ts   []target
		want string
	}{
		{"two seasons", []target{tg(1, "s01", 1, nil), tg(1, "s02", 2, nil)}, "1:s01+1:s02"},
		{"one season", []target{tg(1, "s01", 1, nil)}, "1:s01"},
		{"different reason", []target{tg(1, "s01", 1, nil), tg(1, "s02", 2, func(x *target) { x.reason = OnlyCutoff })}, "1:s01 1:s02"},
		{"different why", []target{tg(1, "s01", 1, nil), tg(1, "s02", 2, func(x *target) { x.why = whyAsked })}, "1:s01 1:s02"},
		{"dead link", []target{tg(1, "s01", 1, func(x *target) { x.why = router.ReasonDeadLink }),
			tg(1, "s02", 2, func(x *target) { x.why = router.ReasonDeadLink })}, "1:s01 1:s02"},
		{"season 0", []target{tg(1, "s00", 0, nil), tg(1, "s01", 1, nil)}, "1:s00 1:s01"},
		{"anime", []target{tg(1, "s01", 1, func(x *target) { x.anime = true }), tg(1, "s02", 2, func(x *target) { x.anime = true })}, "1:s01 1:s02"},
		{"named season", []target{tg(1, "s01", 1, func(x *target) { x.whole = false }), tg(1, "s02", 2, func(x *target) { x.whole = false })}, "1:s01 1:s02"},
		{"one missing episode", []target{tg(1, "s01", 1, nil), tg(1, "s02e01", 2, nil)}, "1:s01 1:s02e01"},
		{"two versions", []target{tg(1, "s01", 1, nil), tg(2, "s01", 1, nil), tg(1, "s02", 2, nil), tg(2, "s02", 2, nil)},
			"1:s01+1:s02 2:s01+2:s02"},
		{"another version's lone season shares as today", []target{tg(1, "s01", 1, nil), tg(2, "s01", 1, nil), tg(1, "s02", 2, nil)},
			"1:s01+1:s02 2:s01"},
	} {
		if got := shape(c.ts...); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

// A title search of a series asks its seasons together: one root and a child, one search with both members and
// the query without a season, one route on the root that the routed member carries; each member keeps its own
// stored search under the route's source. The special is searched alone.
func TestMergedSearchRuns(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	f := &mergedFake{accept: map[int]bool{1: true}}
	e.a.searchBatch = f.search
	queued, found, err := e.a.Submit(e.ctx, 0, Selection{Versions: []string{"series"}, TitleIDs: []int64{10}})
	if err != nil || queued != 3 || found != 3 {
		t.Fatalf("submit: queued %d found %d %v", queued, found, err)
	}
	var s01, s02 int64
	var parent *int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM acquire_run WHERE target = 's01'`).Scan(&s01); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Reader().QueryRow(`SELECT id, search_run FROM acquire_run WHERE target = 's02'`).Scan(&s02, &parent); err != nil {
		t.Fatal(err)
	}
	if parent == nil || *parent != s01 || e.count(`SELECT COUNT(*) FROM acquire_run WHERE target = 's00' AND search_run IS NULL`) != 1 {
		t.Fatalf("rows: s02's root %v, want %d", parent, s01)
	}
	e.drain()
	var merged *router.SearchTarget
	for _, c := range f.calls() {
		for i := range c {
			if len(c[i].Members) > 0 {
				if merged != nil {
					t.Fatal("two merged searches")
				}
				merged = &c[i]
			}
		}
	}
	if merged == nil || len(merged.Members) != 2 || merged.Series.Kind != profile.SearchSeries ||
		fmt.Sprint(merged.Series.Seasons) != "[1 2]" || fmt.Sprint(merged.Members[1].Seasons) != "[1 2]" ||
		len(merged.Series.Episodes) != 5 {
		t.Fatalf("merged target %+v", merged)
	}
	var job1 *int64
	var state1, state2 string
	var job2 *int64
	if err := e.st.Reader().QueryRow(`SELECT state, route_job FROM acquire_run WHERE id = ?`, s01).Scan(&state1, &job1); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Reader().QueryRow(`SELECT state, route_job FROM acquire_run WHERE id = ?`, s02).Scan(&state2, &job2); err != nil {
		t.Fatal(err)
	}
	if state1 != "routed" || job1 == nil || state2 != "nothing" || job2 != nil {
		t.Fatalf("outcomes s01 %s %v, s02 %s %v", state1, job1, state2, job2)
	}
	var payload string
	if err := e.st.Reader().QueryRow(`SELECT payload FROM job WHERE id = ?`, *job1).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"merged":true`) || !strings.Contains(payload, fmt.Sprintf(`"source":"acquire:%d"`, s01)) {
		t.Errorf("route payload %s", payload)
	}
	if n := e.count(`SELECT COUNT(*) FROM journal_decision WHERE source = ? AND run_id IN (?, ?)`, Source(s01), s01, s02); n != 2 {
		t.Errorf("stored searches under the route's source: %d", n)
	}
}

// Two members both routed carry the one route job.
func TestMergedSearchRoutedMembersShareTheRoute(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	e.addSeries(40, 401, 2)
	f := &mergedFake{accept: map[int]bool{1: true, 2: true}}
	e.a.searchBatch = f.search
	if _, _, err := e.a.Submit(e.ctx, 0, Selection{Versions: []string{"series"}, TitleIDs: []int64{40}}); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if n := e.count(`SELECT COUNT(DISTINCT route_job) FROM acquire_run WHERE title_id = 40 AND state = 'routed'`); n != 1 ||
		e.count(`SELECT COUNT(*) FROM acquire_run WHERE title_id = 40 AND state = 'routed' AND route_job IS NOT NULL`) != 2 {
		t.Fatalf("route jobs %d", n)
	}
}

// The wanted loop: a merged search is never split by router.wanted.per_hour: eight due seasons with three searches
// left start as one search of eight members, overdraw the hour, and the hour that follows counts all eight. A
// season with a download in flight is left out of it.
func TestMergedSearchBudget(t *testing.T) {
	t.Parallel()
	for _, busy := range []bool{false, true} {
		e := newEnv(t, true, &policy.Wanted{PerHour: 3})
		e.addSeries(30, 301, 8)
		e.exec(`UPDATE version SET monitored = 0 WHERE id <> 301`)
		want := 8
		if busy {
			e.exec(`INSERT INTO download (title_id, version_id, version_key, info_hash, release_title, indexer, episodes, state,
				created_at, updated_at) VALUES (30, 301, 'series', ?, 'x', 'pub', '[3031,3032]', 'downloading', 0, 0)`, h("f"))
			want = 7
		}
		sum, err := e.a.Round(e.ctx, nil)
		if err != nil || sum.Queued != want || sum.Due != 8 {
			t.Fatalf("busy=%v: round %+v %v", busy, sum, err)
		}
		if n := e.count(`SELECT COUNT(*) FROM acquire_run WHERE title_id = 30`); n != want ||
			e.count(`SELECT COUNT(*) FROM acquire_run WHERE title_id = 30 AND search_run IS NULL`) != 1 {
			t.Fatalf("busy=%v: %d runs", busy, n)
		}
		if busy && e.count(`SELECT COUNT(*) FROM acquire_run WHERE target = 's03'`) != 0 {
			t.Fatal("the busy season was searched")
		}
		used, err := e.a.wantedUsed(e.ctx, e.clock())
		if err != nil || used != want {
			t.Fatalf("busy=%v: used %d %v", busy, used, err)
		}
		e.advance(10 * time.Minute)
		if sum, _ := e.a.Round(e.ctx, nil); sum.Queued != 0 || sum.SearchedLastHour != want {
			t.Fatalf("busy=%v: same hour %+v", busy, sum)
		}
	}
}

// Each member is finished and rescheduled from its own answer: its schedule row moves on and its stored search
// holds its own results, one season filled and the other not.
func TestMergedSearchMemberOutcomes(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, &policy.Wanted{PerHour: 10, AgainAfter: policy.Duration(6 * time.Hour)})
	e.addSeries(40, 401, 2)
	e.exec(`UPDATE version SET monitored = 0 WHERE id <> 401`)
	f := &mergedFake{accept: map[int]bool{1: true}}
	e.a.searchBatch = f.search
	if sum, err := e.a.Round(e.ctx, nil); err != nil || sum.Queued != 2 {
		t.Fatalf("round %+v %v", sum, err)
	}
	e.drain()
	type row struct {
		target, state, message string
		results, accepted      int
	}
	var rows []row
	r, err := e.st.Reader().Query(`SELECT target, state, message, results, accepted FROM acquire_run WHERE title_id = 40 ORDER BY target`)
	if err != nil {
		t.Fatal(err)
	}
	for r.Next() {
		var x row
		if err := r.Scan(&x.target, &x.state, &x.message, &x.results, &x.accepted); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, x)
	}
	r.Close()
	if len(rows) != 2 || rows[0].state != "routed" || rows[0].accepted != 1 || !strings.Contains(rows[0].message, "Series40.S01") ||
		rows[1].state != "nothing" || rows[1].results != 1 || rows[1].accepted != 0 {
		t.Fatalf("runs %+v", rows)
	}
	next := store.Millis(e.clock().Add(6 * time.Hour))
	if n := e.count(`SELECT COUNT(*) FROM search_request WHERE title_id = 40 AND next_at = ?`, next); n != 2 {
		t.Errorf("schedule rows moved to again_after: %d", n)
	}
	for _, c := range []struct {
		target   string
		accepted int
	}{{"s01", 1}, {"s02", 0}} {
		var n, acc int
		if err := e.st.Reader().QueryRow(`SELECT COUNT(*), COALESCE(MAX(accepted), -1) FROM journal_decision
			WHERE title_id = 40 AND target = ?`, c.target).Scan(&n, &acc); err != nil {
			t.Fatal(err)
		}
		if n != 1 || acc != c.accepted {
			t.Errorf("%s: stored searches %d, accepted %d", c.target, n, acc)
		}
	}
}

// A series-level interactive search of a standard series asks only the query without a season, over every aired
// episode above season 0, monitored or not; an anime series keeps today's search.
func TestInteractiveWholeSeries(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true, nil)
	f := &mergedFake{}
	e.a.searchBatch = f.search
	e.exec(`UPDATE episode_version_state SET monitored = 0 WHERE episode_id = 1001`)
	if _, err := e.a.Interactive(e.ctx, 10, "series", nil, nil); err != nil {
		t.Fatal(err)
	}
	c := f.calls()
	if len(c) != 1 || !c[0][0].WholeOnly || c[0][0].Series.Kind != profile.SearchSeries || !c[0][0].Series.Interactive ||
		c[0][0].Series.MonitoredOnly || fmt.Sprint(c[0][0].Series.Episodes) != "[1001 1002 1003 1004]" {
		t.Fatalf("series-level target %+v %+v", c, c[0][0].Series)
	}
	e.exec(`UPDATE title SET anime = 1 WHERE id = 10`)
	if _, err := e.a.Interactive(e.ctx, 10, "series", nil, nil); err != nil {
		t.Fatal(err)
	}
	if c := f.calls(); len(c) != 2 || c[1][0].WholeOnly || c[1][0].Series.Kind == profile.SearchSeries {
		t.Fatalf("anime: %+v", c[1][0])
	}
}
