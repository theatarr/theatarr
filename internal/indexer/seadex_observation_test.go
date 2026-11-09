package indexer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
)

// preferSeaDexPolicy is testPolicy with its anime movie version preferring SeaDex.
func preferSeaDexPolicy() *policy.Policy {
	p := testPolicy()
	v := *p.Versions["movie-anime"]
	v.PreferSeaDex = true
	p.Versions["movie-anime"] = &v
	return p
}

// storedStanding reads the stored snapshot of an entry.
func storedStanding(t *testing.T, db *sql.DB, key string) SeaDexSnapshot {
	t.Helper()
	var body []byte
	if err := db.QueryRow(`SELECT body FROM metadata_cache WHERE source='seadex-standing' AND key=?`, key).Scan(&body); err != nil {
		t.Fatalf("standing %s: %v", key, err)
	}
	var s SeaDexSnapshot
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// An expanded record the entry's own relation list does not name is unrelated to the
// entry; it is never filed under the requested title (search) or one of ours (RSS), nor ranked against the listed
// ones. An answer for another entry gives no release either.
func TestUnlistedSeaDexRecordNotOffered(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	n := newNet(t)
	n.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("filter") {
		case "alID=2":
			// A wrong-entry answer: asked for 2, answers 3.
			fmt.Fprint(w, `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[{"alID":3,"trs":["own"],"expand":{"trs":[{"id":"own","infoHash":"`+a+`","tracker":"Nyaa","isBest":true,"files":[{"name":"Film BD 1080p.mkv","length":5000000000}]}]}}]}`)
			return
		}
		// Entry 47 (Akira, TMDb 149) lists "own" only; "unrelated" is expanded without being listed.
		fmt.Fprint(w, `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[{"alID":47,"trs":["own"],"expand":{"trs":[`+
			`{"id":"own","infoHash":"`+a+`","tracker":"Nyaa","isBest":false,"files":[{"name":"Akira BD 1080p.mkv","length":5000000000}]},`+
			`{"id":"unrelated","infoHash":"`+b+`","tracker":"Nyaa","isBest":true,"files":[{"name":"Other Film BD 1080p.mkv","length":9000000000}]}]}}]}`)
	}))
	h := newHarness(t, n, testPolicy(), false)
	h.svc.SetMetadata(Metadata{Targets: fakeTargets, Names: fakeNames{}})
	for _, q := range []Query{
		{Type: QueryMovie, AniList: 47, Title: "Akira", Year: 1988},
		{Type: QueryRSS},
	} {
		res := search(t, h, "seadex", q)
		if len(res.Releases) != 1 || res.Releases[0].InfoHash != a {
			t.Fatalf("%s: releases %+v, want only the listed record", q.Type, res.Releases)
		}
		// Ranked among the listed records only: the unlisted best record does not take the configured win.
		if SeaDexPreference(&res.Releases[0]) != 3 {
			t.Errorf("%s: listed record flags %v, want the configured winner of its entry", q.Type, res.Releases[0].Flags)
		}
		for _, s := range res.SeaDex {
			for _, tor := range s.Torrents {
				if tor.InfoHash == b {
					t.Errorf("%s: the unlisted record entered standing: %+v", q.Type, s)
				}
			}
		}
	}
	res := search(t, h, "seadex", Query{Type: QueryMovie, AniList: 2, Title: "Wanted Film", Year: 2020})
	if len(res.Releases) != 0 || len(res.SeaDex) != 0 {
		t.Fatalf("a wrong-entry answer gave releases %+v / standing %+v", res.Releases, res.SeaDex)
	}
}

// A pending torrent's rank is computed within its cohort as it was when last
// listed. A cohort member's facts changing in the same answer that first omits the torrent does not move it.
func TestPendingRankKeepsCohortFacts(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	at := time.Unix(100000, 0)
	rank := func(s SeaDexSnapshot) int {
		for _, tor := range ConfigureSeaDex(s, nil).Torrents {
			if tor.InfoHash == a {
				return tor.Preference
			}
		}
		return -1
	}
	roundTrip := func(s SeaDexSnapshot) SeaDexSnapshot {
		body, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		var out SeaDexSnapshot
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	old, _ := ApplySeaDexObservation(nil, SeaDexSnapshot{AniList: 1, Torrents: []SeaDexStanding{
		{InfoHash: a, Facts: &sdTorrent{}}, {InfoHash: b, Facts: &sdTorrent{}}}}, at)
	old = roundTrip(old)
	next, _ := ApplySeaDexObservation(&old, SeaDexSnapshot{AniList: 1, Torrents: []SeaDexStanding{
		{InfoHash: b, IsBest: true, Facts: &sdTorrent{IsBest: true}}}}, at.Add(time.Hour))
	next = roundTrip(next)
	if before, after := rank(old), rank(next); before != 3 || after != before {
		t.Fatalf("pending torrent's rank %d -> %d on its first omission, want 3 kept", before, after)
	}
	// A later answer (still within the delay) changing the cohort again does not move it either.
	later, _ := ApplySeaDexObservation(&next, SeaDexSnapshot{AniList: 1, Torrents: []SeaDexStanding{
		{InfoHash: b, IsBest: true, Size: 9, Facts: &sdTorrent{IsBest: true, DualAudio: true}}}}, at.Add(2*time.Hour))
	if got := rank(roundTrip(later)); got != 3 {
		t.Fatalf("pending rank moved to %d before its retirement was confirmed", got)
	}
	// The sole torrent omitted keeps its configured win against a torrent listed since.
	solo, _ := ApplySeaDexObservation(nil, SeaDexSnapshot{AniList: 1, Torrents: []SeaDexStanding{{InfoHash: a, Facts: &sdTorrent{}}}}, at)
	solo = roundTrip(solo)
	replaced, _ := ApplySeaDexObservation(&solo, SeaDexSnapshot{AniList: 1, Torrents: []SeaDexStanding{
		{InfoHash: b, IsBest: true, Facts: &sdTorrent{IsBest: true}}}}, at.Add(time.Hour))
	if got := rank(roundTrip(replaced)); got != 3 {
		t.Fatalf("sole pending torrent ranked %d against a newcomer, want 3", got)
	}
}

// Existing rows: a row pending since before cohort facts were kept is ranked against the facts the
// row holds; the next observation captures exactly those, so a member's facts changing does not move it.
func TestPendingRowWithoutCohortFacts(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	at := time.Unix(100000, 0)
	rank := func(s SeaDexSnapshot) int {
		for _, tor := range ConfigureSeaDex(s, nil).Torrents {
			if tor.InfoHash == a {
				return tor.Preference
			}
		}
		return -1
	}
	for name, cohort := range map[string][]string{"no cohort": nil, "cohort hashes": {a, b}} {
		stored := SeaDexSnapshot{AniList: 1, Torrents: []SeaDexStanding{
			{InfoHash: a, Facts: &sdTorrent{}, Preference: 1, MissingSince: at.UnixMilli(), Cohort: cohort},
			{InfoHash: b, Facts: &sdTorrent{}, Preference: 1}}}
		before := rank(stored)
		next, _ := ApplySeaDexObservation(&stored, SeaDexSnapshot{AniList: 1, Torrents: []SeaDexStanding{
			{InfoHash: b, IsBest: true, Facts: &sdTorrent{IsBest: true}}}}, at.Add(time.Hour))
		if after := rank(next); before != 3 || after != before {
			t.Errorf("%s: pending rank %d -> %d when a cohort member changed, want 3 kept", name, before, after)
		}
	}
}

// The observation order is shared by every Evidence on one database (a settings
// reload builds a new one while the old one's searches finish).
func TestObservationOrderAcrossEvidence(t *testing.T) {
	st := openStore(t)
	if _, err := st.Writer().Exec(`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1,'movie','Film',0,0); INSERT INTO title_external_id VALUES(1,'anilist','1')`); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100000, 0)
	oldGen := NewEvidence(EvidenceConfig{Store: st, Policy: testPolicy(), Now: func() time.Time { return now }})
	newGen := NewEvidence(EvidenceConfig{Store: st, Policy: testPolicy(), Now: func() time.Time { return now }})
	snap := func(h string) []SeaDexSnapshot {
		return []SeaDexSnapshot{{AniList: 1, Torrents: []SeaDexStanding{{InfoHash: strings.Repeat(h, 40), Facts: &sdTorrent{}}}}}
	}
	unlock, err := oldGen.lockSeaDex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	if release, err := newGen.lockSeaDex(ctx); err == nil {
		release()
		t.Error("new Evidence bypassed old generation lock")
	}
	cancel()
	if err := oldGen.observeSeaDex(context.Background(), snap("a")); err != nil {
		t.Fatal(err)
	}
	unlock()
	unlock, err = newGen.lockSeaDex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := newGen.observeSeaDex(context.Background(), snap("b")); err != nil {
		t.Fatal(err)
	}
	got := storedStanding(t, st.Reader(), "1")
	if len(got.Torrents) != 2 || got.Torrents[1].InfoHash != strings.Repeat("b", 40) || got.Torrents[1].MissingSince != 0 {
		t.Fatalf("new generation standing lost: %+v", got)
	}
}

// Two services (runtime generations) on one database; the older answer commits before the next fetch.
func TestObservationOrderAcrossServices(t *testing.T) {
	n := newNet(t)
	answer := func(hash string) string {
		return `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[{"alID":1,"trs":["r` + hash[:14] + `"],"expand":{"trs":[{"id":"r` + hash[:14] +
			`","infoHash":"` + hash + `","tracker":"Nyaa","isBest":true,"files":[{"name":"Film BD 1080p.mkv","length":5000000000}]}]}}]}`
	}
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	var calls atomic.Int32
	firstIn, release := make(chan struct{}), make(chan struct{})
	n.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(firstIn)
			<-release
			fmt.Fprint(w, answer(a))
			return
		}
		fmt.Fprint(w, answer(b))
	}))
	h := newHarness(t, n, testPolicy(), false)
	st := openStore(t)
	if _, err := st.Writer().Exec(`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1,'movie','Film',0,0); INSERT INTO title_external_id VALUES(1,'anilist','1')`); err != nil {
		t.Fatal(err)
	}
	h.svc.SetEvidence(NewEvidence(EvidenceConfig{Store: st, Policy: testPolicy(), Now: h.clock}))
	done := make(chan error, 1)
	var older *Result
	go func() {
		var err error
		older, err = h.svc.Search(context.Background(), "seadex", Query{Type: QueryMovie, AniList: 1})
		done <- err
	}()
	<-firstIn
	next := newHarness(t, n, testPolicy(), false)
	next.advance(time.Minute)
	next.svc.SetEvidence(NewEvidence(EvidenceConfig{Store: st, Policy: testPolicy(), Now: next.clock}))
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := next.svc.Search(context.Background(), "seadex", Query{Type: QueryMovie, AniList: 1, Title: "Film"}); err != nil {
		t.Fatal(err)
	}
	got := storedStanding(t, st.Reader(), "1")
	if len(got.Torrents) != 2 || got.Torrents[0].MissingSince == 0 || got.Torrents[1].InfoHash != b || got.Torrents[1].MissingSince != 0 {
		t.Fatalf("the older answer superseded the newer one: %+v", got)
	}
	// The first answer committed before the second fetch; it may be cached without replaying transitions.
	if len(older.Releases) != 1 {
		t.Fatalf("first serialized answer not returned: %+v", older)
	}
	h.svc.mu.Lock()
	cached := len(h.svc.cache)
	h.svc.mu.Unlock()
	if cached != 1 {
		t.Fatal("the serialized first answer was not cached")
	}
}

// Each entry of a multi-entry search is dated by its own fetch, not by the search's start: a
// later fetch of entry 2 is not discarded because the search that made it started before another search of 2.
func TestPerEntryObservationTime(t *testing.T) {
	n := newNet(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var count atomic.Int32
	answer := func(id int, hash string) string {
		return fmt.Sprintf(`{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[{"alID":%d,"trs":["pick"],"expand":{"trs":[{"id":"pick","infoHash":"%s","tracker":"Nyaa","isBest":true,"files":[{"name":"Film BD 1080p.mkv","length":5000000000}]}]}}]}`, id, hash)
	}
	n.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter") == "alID=1" {
			close(entered)
			<-release
			fmt.Fprint(w, answer(1, strings.Repeat("a", 40)))
			return
		}
		hash := strings.Repeat("b", 40)
		if count.Add(1) > 1 {
			hash = strings.Repeat("c", 40)
		}
		fmt.Fprint(w, answer(2, hash))
	}))
	h := newHarness(t, n, testPolicy(), false)
	st := openStore(t)
	for _, q := range []string{`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1,'movie','One',0,0),(2,'movie','Two',0,0)`,
		`INSERT INTO title_external_id VALUES(1,'anilist','1'),(2,'anilist','2')`} {
		if _, err := st.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	h.svc.SetMetadata(Metadata{AniList: AniListFunc(func(context.Context, Query) ([]int, error) { return []int{1, 2}, nil })})
	h.svc.SetEvidence(NewEvidence(EvidenceConfig{Store: st, Policy: testPolicy(), Now: h.clock}))
	done := make(chan error, 1)
	go func() {
		_, err := h.svc.Search(context.Background(), "seadex", Query{Type: QueryMovie, TMDb: 1})
		done <- err
	}()
	<-entered
	h.advance(time.Minute)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Search(context.Background(), "seadex", Query{Type: QueryMovie, AniList: 2}); err != nil {
		t.Fatal(err)
	}
	h.advance(time.Minute)
	got := storedStanding(t, st.Reader(), "2")
	found := false
	for _, tor := range got.Torrents {
		found = found || tor.InfoHash == strings.Repeat("c", 40) && tor.MissingSince == 0
	}
	if !found {
		t.Fatalf("the later per-entry fetch was discarded as older: %+v", got)
	}
}

// A commit that fails does not advance the order, so an older observation that then
// commits is stored.
func TestFailedCommitDoesNotAdvanceOrder(t *testing.T) {
	st := openStore(t)
	for _, q := range []string{
		`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1,'movie','Film',0,0)`,
		`INSERT INTO title_external_id VALUES(1,'anilist','1')`,
		`CREATE TABLE defer_parent (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE defer_child (p INTEGER REFERENCES defer_parent(id) DEFERRABLE INITIALLY DEFERRED)`,
		// Fails at commit (deferred foreign key), after the statements succeeded.
		`CREATE TRIGGER fail_standing AFTER INSERT ON metadata_cache WHEN NEW.source='seadex-standing' BEGIN INSERT INTO defer_child(p) VALUES(1); END`,
	} {
		if _, err := st.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Unix(100000, 0)
	e := NewEvidence(EvidenceConfig{Store: st, Policy: testPolicy(), Now: func() time.Time { return now }})
	snap := []SeaDexSnapshot{{AniList: 1, Torrents: []SeaDexStanding{{InfoHash: strings.Repeat("a", 40), Facts: &sdTorrent{}}}}}
	if err := e.observeSeaDex(context.Background(), snap); err == nil {
		t.Fatal("setup: the deferred foreign key did not fail the commit")
	}
	if _, err := st.Writer().Exec(`DROP TRIGGER fail_standing`); err != nil {
		t.Fatal(err)
	}
	if err := e.observeSeaDex(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	if got := storedStanding(t, st.Reader(), "1"); len(got.Torrents) != 1 {
		t.Fatalf("a rolled-back observation suppressed an older one: %+v", got)
	}
}

// A legacy row mapped only through its stored target, refreshed by an AniList-only search
// with no reverse mapping, keeps its target and takes the answer's facts (it is not left unmapped and eligible).
func TestTargetOnlyLegacyRowRefreshes(t *testing.T) {
	n := newNet(t)
	n.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[{"alID":1,"trs":["pick1"],"expand":{"trs":[{"id":"pick1","infoHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","tracker":"Nyaa","isBest":true,"files":[{"name":"Film BD 1080p.mkv","length":5000000000}]}]}}]}`)
	}))
	h := newHarness(t, n, testPolicy(), false)
	st := openStore(t)
	if _, err := st.Writer().Exec(`INSERT INTO title(id,kind,name,tmdb_id,created_at,updated_at) VALUES(1,'movie','Film',1,0,0)`); err != nil {
		t.Fatal(err)
	}
	old := SeaDexSnapshot{AniList: 1, Targets: []AniListTarget{{TMDb: 1}}, Torrents: []SeaDexStanding{{InfoHash: strings.Repeat("a", 40), Preference: 3}}}
	body, _ := json.Marshal(old)
	if _, err := st.Writer().Exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','1',200,?,0,9999999999999)`, body); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	h.svc.SetEvidence(NewEvidence(EvidenceConfig{Store: st, Policy: testPolicy(), Now: h.clock}))
	if _, err := h.svc.Search(ctx, "seadex", Query{Type: QueryMovie, AniList: 1}); err != nil {
		t.Fatal(err)
	}
	got := storedStanding(t, st.Reader(), "1")
	if len(got.Torrents) != 1 || got.Torrents[0].Facts == nil || len(got.Targets) != 1 || got.Targets[0] != (AniListTarget{TMDb: 1}) {
		t.Fatalf("legacy row not refreshed with its provenance kept: %+v", got)
	}
}

// With no version preferring SeaDex a failed standing write does not fail the
// search (the answer is returned, not cached); with one it fails, and the failed search still reports the upstream
// request it made.
func TestStandingWriteFailure(t *testing.T) {
	for _, prefer := range []bool{false, true} {
		t.Run(fmt.Sprintf("prefer=%v", prefer), func(t *testing.T) {
			n := newNet(t)
			n.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[{"alID":1,"trs":["pick1"],"expand":{"trs":[{"id":"pick1","infoHash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","tracker":"Nyaa","isBest":true,"files":[{"name":"Film BD 1080p.mkv","length":5000000000}]}]}}]}`)
			}))
			h := newHarness(t, n, testPolicy(), false)
			st := openStore(t)
			for _, q := range []string{`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1,'movie','Film',0,0)`, `INSERT INTO title_external_id VALUES(1,'anilist','1')`,
				`CREATE TRIGGER fail_standing BEFORE INSERT ON metadata_cache WHEN NEW.source='seadex-standing' BEGIN SELECT RAISE(ABORT,'injected persistence failure'); END`} {
				if _, err := st.Writer().Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			pol := testPolicy()
			if prefer {
				pol = preferSeaDexPolicy()
			}
			h.svc.SetEvidence(NewEvidence(EvidenceConfig{Store: st, Policy: pol, Now: h.clock}))
			res, err := h.svc.Search(context.Background(), "seadex", Query{Type: QueryMovie, AniList: 1})
			switch {
			case prefer && err == nil:
				t.Fatal("a prefer-on policy's search succeeded with its standing unstored")
			case prefer && RequestsSpent(err) != 1:
				t.Fatalf("failed search reports %d upstream requests, want 1 (%v)", RequestsSpent(err), err)
			case !prefer && (err != nil || len(res.Releases) != 1 || res.Requests != 1):
				t.Fatalf("prefer-off search failed on a standing write: %+v %v", res, err)
			}
			h.svc.mu.Lock()
			cached := len(h.svc.cache)
			h.svc.mu.Unlock()
			if cached != 0 {
				t.Fatal("an answer whose standing was not stored was cached")
			}
		})
	}
}

// The standing write has its own deadline: an answer that arrives close to the search
// timeout, whose write waits for the write lock past it, is still stored and returned.
func TestStandingWriteOutlivesSearchTimeout(t *testing.T) {
	answered := make(chan struct{})
	n := newNet(t)
	n.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[{"alID":1,"trs":["pick1"],"expand":{"trs":[{"id":"pick1","infoHash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","tracker":"Nyaa","isBest":true,"files":[{"name":"Film BD 1080p.mkv","length":5000000000}]}]}}]}`)
		close(answered)
	}))
	const timeout = 400 * time.Millisecond
	h := newHarness(t, n, testPolicy(), false, func(_ *gateway.Options, o *Options) { o.SearchTimeout = timeout })
	st := openStore(t)
	for _, q := range []string{`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1,'movie','Film',0,0)`, `INSERT INTO title_external_id VALUES(1,'anilist','1')`} {
		if _, err := st.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	h.svc.SetEvidence(NewEvidence(EvidenceConfig{Store: st, Policy: preferSeaDexPolicy(), Now: h.clock}))
	// Hold the only writer connection, so the standing write waits past the search timeout.
	blocker, err := st.Writer().Begin()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := h.svc.Search(context.Background(), "seadex", Query{Type: QueryMovie, AniList: 1})
		done <- err
	}()
	<-answered
	time.Sleep(timeout + 300*time.Millisecond)
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("the search failed because its standing write ran on the search deadline: %v", err)
	}
	if got := storedStanding(t, st.Reader(), "1"); len(got.Torrents) != 1 {
		t.Fatalf("standing not stored: %+v", got)
	}
}

// Coverage precedence is per season. In the split-cour layout (the title's own id 11 as
// cour 1, al:11 = S01E01-03, al:12 = S01E04-06) neither entry covers the other cour; a stored exact-episode
// summary target never covers a whole season, a whole-season target does.
func TestSplitCourCovers(t *testing.T) {
	st := openStore(t)
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, tvdb_id, created_at, updated_at) VALUES (1, 'series', 'Show', 100, 0, 0)`,
		`INSERT INTO title_external_id (title_id, source, external_id) VALUES (1, 'anilist', '11')`,
		`INSERT INTO anime_franchise (slug, name, seed_anilist, created_at, updated_at) VALUES ('f', 'F', 11, 0, 0)`,
		`INSERT INTO anime_entry (franchise, key, origin, anilist_id, tmdb_movie, body) VALUES ('f', 'al:11', 'anilist', 11, '[]', '{}')`,
		`INSERT INTO anime_entry (franchise, key, origin, anilist_id, tmdb_movie, body) VALUES ('f', 'al:12', 'anilist', 12, '[]', '{}')`,
		`INSERT INTO anime_mapping (franchise, entry_key, status, tvdb_series, episodes, updated_at) VALUES ('f', 'al:11', 'auto', 100, '[[1,1],[1,2],[1,3]]', 0)`,
		`INSERT INTO anime_mapping (franchise, entry_key, status, tvdb_series, episodes, updated_at) VALUES ('f', 'al:12', 'auto', 100, '[[1,4],[1,5],[1,6]]', 0)`,
	} {
		if _, err := st.Writer().Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// Stored targets: entry 12's exact summary as the lookup now records it, a whole-season one for 13, and an
	// exact summary for 14 whose franchise mapping is gone.
	c, err := LoadSeaDexCatalog(context.Background(), st.Reader(), []SeaDexSnapshot{
		{AniList: 12, Targets: []AniListTarget{{TVDB: 100, Season: 1, Exact: true}}},
		{AniList: 13, Targets: []AniListTarget{{TVDB: 100, Season: 2}}},
		{AniList: 14, Targets: []AniListTarget{{TVDB: 100, Season: 2, Exact: true}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	byEntry := map[int]*SeaDexEntryMapping{}
	for _, m := range c.Titles()[1] {
		byEntry[m.Entry] = m
	}
	for _, x := range []struct {
		entry int
		ep    [2]int
		want  bool
	}{
		{11, [2]int{1, 2}, true}, {11, [2]int{1, 5}, false}, {11, [2]int{1, 13}, false},
		{12, [2]int{1, 5}, true}, {12, [2]int{1, 2}, false},
		{13, [2]int{2, 7}, true}, {13, [2]int{1, 1}, false},
		{14, [2]int{2, 7}, false},
	} {
		m := byEntry[x.entry]
		if m == nil {
			t.Fatalf("entry %d not mapped to the title: %+v", x.entry, byEntry)
		}
		if got := m.Covers([][2]int{x.ep}); got != x.want {
			t.Errorf("entry %d Covers(%v) = %v, want %v", x.entry, x.ep, got, x.want)
		}
	}
}
