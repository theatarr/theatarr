package ceiling

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

var now = time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)

func searches(days ...int) []Search {
	out := []Search{}
	for _, d := range days {
		out = append(out, Search{At: now.AddDate(0, 0, -d), Indexers: []Coverage{{Indexer: "disc-feed", Kind: "torznab", Complete: true}}})
	}
	return out
}
func evidence(tier, kind string) Evidence {
	return Evidence{Tier: tier, Kind: kind, PositiveOnly: PositiveOnly(kind), FirstSeen: now.AddDate(0, 0, -180), LastSeen: now, FetchedAt: &now}
}

func TestJudge(t *testing.T) {
	cases := []struct {
		name         string
		ev           []Evidence
		searches     []Search
		disc         bool
		override     *Override
		tier, status string
	}{
		{name: "no evidence", searches: searches(90, 45, 0), tier: "unknown", status: "unknown"},
		{name: "arr history is not searches", ev: []Evidence{evidence("bd", "arr_history")}, tier: "bd", status: "likely"},
		{name: "two searches", ev: []Evidence{evidence("bd", "torznab")}, searches: searches(90, 0), tier: "bd", status: "likely"},
		{name: "three over 89 days", ev: []Evidence{evidence("bd", "torznab")}, searches: searches(89, 45, 0), tier: "bd", status: "likely"},
		{name: "three over 90 days", ev: []Evidence{evidence("bd", "torznab")}, searches: searches(90, 45, 0), tier: "bd", status: "known"},
		{name: "SeaDex alone", ev: []Evidence{evidence("bd", "seadex")}, tier: "bd", status: "likely"},
		{name: "SeaDex plus Theatarr absence", ev: []Evidence{evidence("bd", "seadex")}, searches: searches(90, 45, 0), tier: "bd", status: "known"},
		{name: "default keeps profile web tier", ev: []Evidence{evidence("bd", "arr_history"), evidence("web_2160", "torznab")}, searches: searches(90, 45, 0), tier: "web_2160", status: "known"},
		{name: "disc only", ev: []Evidence{evidence("bd", "arr_history"), evidence("web_2160", "torznab")}, searches: searches(90, 45, 0), disc: true, tier: "bd", status: "known"},
		{name: "disc only no disc", ev: []Evidence{evidence("web_2160", "torznab")}, searches: searches(90, 45, 0), disc: true, tier: "unknown", status: "unknown"},
		{name: "override pins lower and keeps evidence", ev: []Evidence{evidence("uhd_bd", "torznab")}, override: &Override{Tier: "bd", Note: "checked manually"}, tier: "bd", status: "override"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Judge(tc.ev, tc.searches, tc.disc, tc.override, now)
			if got.Tier != tc.tier || got.Status != tc.status {
				t.Fatalf("got %s/%s, want %s/%s", got.Tier, got.Status, tc.tier, tc.status)
			}
			if len(got.Evidence) != len(tc.ev) {
				t.Fatal("evidence discarded")
			}
		})
	}
	for _, kind := range []string{"seadex", "dmm", "tmdb", "torrentio", "zilean", "thediscdb"} {
		ss := searches(90, 45, 0)
		for i := range ss {
			ss[i].Indexers[0].Kind = kind
		}
		if got := Judge([]Evidence{evidence("bd", kind)}, ss, false, nil, now); got.Status != "likely" {
			t.Errorf("%s proves absence: %+v", kind, got)
		}
	}
	for _, name := range []string{"failed", "truncated or cached", "replayed", "higher seen yesterday", "expired override"} {
		t.Run(name, func(t *testing.T) {
			ev := []Evidence{evidence("bd", "torznab")}
			ss := searches(90, 45, 0)
			var ov *Override
			switch name {
			case "failed":
				ss[1].Indexers = nil
			case "truncated or cached":
				ss[1].Indexers[0].Complete = false
			case "replayed":
				ss = searches(90, 90, 0)
			case "higher seen yesterday":
				e := evidence("uhd_bd", "torznab")
				e.FirstSeen = now.AddDate(0, 0, -1)
				ev = append(ev, e)
			case "expired override":
				until := now
				ov = &Override{Tier: "uhd_bd", Until: &until}
				ss = nil
			}
			if got := Judge(ev, ss, false, ov, now); got.Status != "likely" {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestTMDbExpiry(t *testing.T) {
	for _, months := range []int{5, 6, 7} {
		fetched := now.AddDate(0, -months, 0)
		e := evidence("uhd_bd", "tmdb")
		e.FetchedAt = &fetched
		got := Judge([]Evidence{e}, nil, false, nil, now)
		want := "likely"
		if months >= 6 {
			want = "unknown"
		}
		if got.Status != want {
			t.Errorf("%d months: %+v", months, got)
		}
	}
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().Exec(`INSERT INTO title(id,kind,name,sort_name,created_at,updated_at) VALUES(1,'movie','A','A',0,0);
 INSERT INTO version(id,title_id,policy_key,state,monitored,created_at,updated_at) VALUES(1,1,'remux','filled',1,0,0),(2,1,'disc','filled',1,0,0)`); err != nil {
		t.Fatal(err)
	}
	return st
}
func testPolicy() *policy.Policy {
	return &policy.Policy{Versions: map[string]*policy.VersionPolicy{
		"remux": {Kind: policy.KindMovie, Profile: "p"}, "disc": {Kind: policy.KindMovie, Profile: "p", Ceiling: policy.Ceiling{DiscOnly: true}}},
		Profiles: map[string]*policy.Profile{"p": {Qualities: []policy.QualityItem{{Name: "Remux-2160p"}, {Name: "WEBDL-2160p"}, {Name: "BD", Group: true, Qualities: []string{"Bluray-1080p", "Remux-1080p"}}, {Name: "WEBDL-1080p"}}, Cutoff: "Remux-2160p"}},
		Indexers: map[string]*policy.Indexer{"feed": {Kind: "torznab"}}}
}

func TestRecordEmptySearchCoverageAndVersionIsolation(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	pol := testPolicy()
	e := evidence("bd", "arr_history")
	e.Source = "arr_history"
	e.Example = "A.1080p.BluRay"
	if err := Sight(ctx, st.Writer(), 1, -1, e); err != nil {
		t.Fatal(err)
	}
	d := &decide.Decision{Release: "A.2160p.WEB-DL", Quality: "WEBDL-2160p", MatchType: decide.MatchTitle}
	for i, days := range []int{90, 45, 0} {
		var ds []*decide.Decision
		if i == 0 {
			ds = []*decide.Decision{d}
		}
		if err := st.WriteTx(ctx, func(tx *sql.Tx) error {
			return Record(ctx, tx, pol, 1, "remux", "movie", "search", ds, map[*decide.Decision]indexer.Release{d: {Indexer: "feed"}}, []Coverage{{Indexer: "feed", Kind: "torznab", Season: -1, Complete: true}}, now.AddDate(0, 0, -days))
		}); err != nil {
			t.Fatal(err)
		}
	}
	for vid, want := range map[int]string{1: "known", 2: "likely"} {
		var raw string
		if err := st.Reader().QueryRow(`SELECT ceiling FROM version_ceiling WHERE version_id=?`, vid).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var state string
		if err := st.Reader().QueryRow(`SELECT json_extract(ceiling,'$.status') FROM version_ceiling WHERE version_id=?`, vid).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != want {
			t.Errorf("version %d: %s want %s (%s)", vid, state, want, raw)
		}
	}
	var n int
	if err := st.Reader().QueryRow(`SELECT COUNT(*) FROM ceiling_search_indexer WHERE indexer='feed' AND complete=1`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("coverage count %d: %v", n, err)
	}
	// Replaying an existing search is not a fourth search.
	if err := Record(ctx, st.Writer(), pol, 1, "remux", "movie", "search", nil, nil, []Coverage{{Indexer: "feed", Kind: "torznab", Season: -1, Complete: true}}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.Reader().QueryRow(`SELECT COUNT(*) FROM ceiling_search`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("searches %d: %v", n, err)
	}
}

func TestProfileGroupsAndDisplay(t *testing.T) {
	pol := testPolicy()
	p, err := Profile(pol, "remux", "movie")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ tier, cutoff string }{{"bd", "BD"}, {"web_2160", "WEBDL-2160p"}, {"uhd_bd", "Remux-2160p"}, {"unknown", "Remux-2160p"}} {
		_, name := EffectiveCutoff(p, tc.tier)
		if name != tc.cutoff {
			t.Errorf("%s => %s", tc.tier, name)
		}
	}
	for _, tc := range []struct{ tier, status, q, want string }{{"bd", "known", "Remux-1080p", "best_available"}, {"bd", "likely", "Bluray-1080p", "best_source_seen"}, {"uhd_bd", "likely", "Remux-1080p", "higher_source_exists"}, {"unknown", "unknown", "Remux-1080p", "below_cutoff"}, {"bd", "override", "Remux-1080p", "best_available"}} {
		if got := State(&Info{Tier: tc.tier, Status: tc.status}, p, tc.q, "movie"); got != tc.want {
			t.Errorf("%+v: %s", tc, got)
		}
	}
	// A group with a higher quality cannot be capped as a lower source.
	mixed, err := profile.FromItems(format.Radarr, "mixed", []format.TrashQualityItem{{Name: "Mixed", Allowed: true, Items: []string{"Remux-2160p", "Remux-1080p"}}, {Name: "WEBDL-1080p", Allowed: true}}, "Mixed", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, name := EffectiveCutoff(mixed, "bd"); name != "WEBDL-1080p" {
		t.Fatalf("mixed group capped to %q", name)
	}
}

func TestTMDbLocalPurge(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	fetched := now.AddDate(0, -5, 0)
	if _, err := st.Writer().Exec(`INSERT INTO title_release_date(title_id,source,country,type,date,note,fetched_at) VALUES(1,'tmdb','US','physical','2020-01-01','4K Ultra HD',?)`, fetched.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := Compute(ctx, st.Writer(), testPolicy(), 1, now); err != nil {
		t.Fatal(err)
	}
	var tier, status string
	if err := st.Reader().QueryRow(`SELECT json_extract(ceiling,'$.tier'),json_extract(ceiling,'$.status') FROM version_ceiling WHERE version_id=1`).Scan(&tier, &status); err != nil {
		t.Fatal(err)
	}
	if tier != "uhd_bd" || status != "likely" {
		t.Fatalf("TMDb note: %s/%s", tier, status)
	}
	later := fetched.AddDate(0, 6, 0)
	if err := Compute(ctx, st.Writer(), testPolicy(), 1, later); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.Reader().QueryRow(`SELECT COUNT(*) FROM source_sighting WHERE source_kind='tmdb'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("stale evidence retained: %d %v", n, err)
	}
	var note string
	if err := st.Reader().QueryRow(`SELECT note FROM title_release_date WHERE source='tmdb'`).Scan(&note); err != nil || note != "" {
		t.Fatalf("stale note retained: %q %v", note, err)
	}
}

func TestEveryMatchedTierAndScope(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	pol := testPolicy()
	var ds []*decide.Decision
	rels := map[*decide.Decision]indexer.Release{}
	for i := range 20 {
		q := "WEBDL-1080p"
		if i == 19 {
			q = "Remux-2160p"
		}
		d := &decide.Decision{Release: "A.Release", Quality: q, MatchType: decide.MatchTitle}
		ds = append(ds, d)
		rels[d] = indexer.Release{Indexer: "feed"}
	}
	// Even a rejected candidate outside the journal's sample contributes its tier; unrelated titles do not.
	ds = append(ds, &decide.Decision{Release: "Other.Title", Quality: "Remux-2160p"})
	if err := Record(ctx, st.Writer(), pol, 1, "remux", "movie", "rss", ds, rels, nil, now); err != nil {
		t.Fatal(err)
	}
	var tier string
	var n int
	if err := st.Reader().QueryRow(`SELECT json_extract(ceiling,'$.tier') FROM version_ceiling WHERE version_id=1`).Scan(&tier); err != nil || tier != "uhd_bd" {
		t.Fatalf("last rejected tier lost: %s %v", tier, err)
	}
	if err := st.Reader().QueryRow(`SELECT count FROM source_sighting WHERE tier='web_1080'`).Scan(&n); err != nil || n != 19 {
		t.Fatalf("aggregated count: %d %v", n, err)
	}
	if err := st.Reader().QueryRow(`SELECT COUNT(*) FROM ceiling_search`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("RSS counted as searches: %d %v", n, err)
	}
	if _, err := st.Writer().Exec(`INSERT INTO title(id,kind,name,sort_name,created_at,updated_at) VALUES(3,'series','S','S',0,0);
 INSERT INTO version(id,title_id,policy_key,state,monitored,created_at,updated_at) VALUES(3,3,'tv','wanted',1,0,0);
 INSERT INTO episode(id,title_id,season,number,name,created_at,updated_at) VALUES(31,3,1,1,'One',0,0),(32,3,2,1,'Two',0,0)`); err != nil {
		t.Fatal(err)
	}
	d := &decide.Decision{Release: "S.S02E01", Quality: "WEBDL-2160p", MatchType: decide.MatchTitle, Episodes: []string{"S02E01"}}
	if err := Record(ctx, st.Writer(), nil, 3, "tv", "s02", "search", []*decide.Decision{d}, nil, []Coverage{{Indexer: "feed", Kind: "torznab", Season: 2, Complete: true}}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.Reader().QueryRow(`SELECT COUNT(*) FROM source_sighting WHERE title_id=3 AND season=1`).Scan(&n); err != nil || n != 0 {
		t.Fatal("series sighting crossed seasons")
	}
	if err := st.Reader().QueryRow(`SELECT COUNT(*) FROM ceiling_search WHERE title_id=3 AND season=1`).Scan(&n); err != nil || n != 0 {
		t.Fatal("series coverage crossed seasons")
	}
}

// TestCeilingKeepsStoredArrSightings: sightings the arr history left in source_sighting still count, unchanged, and
// arr history is no longer read.
func TestCeilingKeepsStoredArrSightings(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	at := time.UnixMilli(1000).UTC()
	if err := Sight(ctx, st.Writer(), 1, -1, Evidence{Source: "arr_history:arr", Kind: "arr_history", PositiveOnly: true,
		Tier: Tier("Remux-1080p"), Example: "A.1080p.BluRay", Quality: "Remux-1080p", Count: 2, FirstSeen: at,
		LastSeen: at}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Compute(ctx, st.Writer(), testPolicy(), 1, now); err != nil {
			t.Fatal(err)
		}
	}
	var tier, status string
	var n int
	if err := st.Reader().QueryRow(`SELECT json_extract(ceiling,'$.tier'),json_extract(ceiling,'$.status') FROM version_ceiling WHERE version_id=1`).Scan(&tier, &status); err != nil || tier != "bd" || status != "likely" {
		t.Fatalf("stored sighting: %s/%s %v", tier, status, err)
	}
	if err := st.Reader().QueryRow(`SELECT COUNT(*) FROM ceiling_search`).Scan(&n); err != nil || n != 0 {
		t.Fatal("arr sightings became searches")
	}
	if err := st.Reader().QueryRow(`SELECT SUM(count) FROM source_sighting WHERE source_kind='arr_history'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("stored sighting changed: %d %v", n, err)
	}
}

// A long-searched title keeps a bounded history: every search of the last week, one a day to 90 days, one a week to
// two years. The stored ceiling lists only the latest searches, and the thinned history still makes it known.
func TestSearchHistoryBounded(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	pol := testPolicy()
	e := evidence("bd", "arr_history")
	e.Source, e.FirstSeen, e.LastSeen = "arr_history", now.AddDate(0, 0, -900), now.AddDate(0, 0, -900)
	if err := Sight(ctx, st.Writer(), 1, -1, e); err != nil {
		t.Fatal(err)
	}
	for day := 1; day <= 800; day++ {
		at, complete := now.AddDate(0, 0, -day).UnixMilli(), 0
		if day%3 == 0 {
			complete = 1
		}
		res, err := st.Writer().Exec(`INSERT INTO ceiling_search(title_id,season,version_key,searched_at,source,best_tier) VALUES(1,-1,'remux',?,'search','bd')`, at)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		if _, err := st.Writer().Exec(`INSERT INTO ceiling_search_indexer(search_id,indexer,source_kind,positive_only,complete) VALUES(?,'feed','torznab',0,?)`, id, complete); err != nil {
			t.Fatal(err)
		}
	}
	if err := Record(ctx, st.Writer(), pol, 1, "remux", "movie", "search", nil, nil, []Coverage{{Indexer: "feed", Kind: "torznab", Season: -1, Complete: true}}, now); err != nil {
		t.Fatal(err)
	}
	var n, old, orphans, failedWeekly int
	q := st.Reader().QueryRow(`SELECT COUNT(*), COUNT(*) FILTER (WHERE searched_at<?),
 (SELECT COUNT(*) FROM ceiling_search_indexer WHERE search_id NOT IN (SELECT id FROM ceiling_search)),
 COUNT(*) FILTER (WHERE searched_at<? AND NOT EXISTS(SELECT 1 FROM ceiling_search_indexer i WHERE i.search_id=ceiling_search.id AND i.complete=1))
 FROM ceiling_search`, now.Add(-searchKeepMax).UnixMilli(), now.Add(-searchKeepDaily).UnixMilli())
	if err := q.Scan(&n, &old, &orphans, &failedWeekly); err != nil {
		t.Fatal(err)
	}
	// 1 now + 6 in the last week + about 83 daily + about 92 weekly.
	if n > 190 || n < 170 || old != 0 || orphans != 0 || failedWeekly != 0 {
		t.Errorf("kept %d searches, %d older than two years, %d orphan indexer rows, %d failed weekly ones", n, old, orphans, failedWeekly)
	}
	var count int
	var status string
	if err := st.Reader().QueryRow(`SELECT json_array_length(ceiling,'$.searches'), json_extract(ceiling,'$.status') FROM version_ceiling WHERE version_id=1`).Scan(&count, &status); err != nil {
		t.Fatal(err)
	}
	if count != maxInfoSearches || status != "known" {
		t.Errorf("stored ceiling: %d searches, status %s", count, status)
	}
}

// A better source the version's profile does not allow is not "better source known" (a disc on a WEB-only
// profile): the file reads by its profile alone.
func TestHigherSourceOnlyWhenProfileAllowsIt(t *testing.T) {
	web, err := profile.FromItems(format.Radarr, "web", []format.TrashQualityItem{{Name: "WEBDL-2160p", Allowed: true},
		{Name: "WEBDL-1080p", Allowed: true}, {Name: "Bluray-1080p", Allowed: false}, {Name: "HDTV-720p", Allowed: true}}, "WEBDL-2160p", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ tier, q, want string }{
		{"uhd_bd", "WEBDL-1080p", "below_cutoff"},
		{"bd", "HDTV-720p", "below_cutoff"},
		{"web_2160", "WEBDL-1080p", "higher_source_exists"},
		{"web_1080", "HDTV-720p", "higher_source_exists"},
		{"bd", "WEBDL-1080p", "best_available"},
	} {
		if got := State(&Info{Tier: tc.tier, Status: "known"}, web, tc.q, "movie"); got != tc.want {
			t.Errorf("%s holding %s: %s, want %s", tc.tier, tc.q, got, tc.want)
		}
	}
}
