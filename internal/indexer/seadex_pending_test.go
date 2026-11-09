package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// K: a torrent SeaDex stops listing keeps its last confirmed configured rank until its retirement is confirmed, even
// when a newly listed torrent would outrank it; the new torrent is ranked among the listed ones.
func TestPendingKeepsConfirmedRank(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	old, _ := ApplySeaDexObservation(nil, SeaDexSnapshot{AniList: 1, Torrents: []SeaDexStanding{{InfoHash: a, Facts: &sdTorrent{}}}}, time.Unix(0, 0))
	if got := ConfigureSeaDex(old, nil).Torrents[0].Preference; got != 3 {
		t.Fatalf("sole listed torrent is not the configured winner: %d", got)
	}
	next := SeaDexSnapshot{AniList: 1, Torrents: []SeaDexStanding{{InfoHash: b, IsBest: true, Facts: &sdTorrent{IsBest: true}}}}
	first := time.Unix(100000, 0)
	merged, _ := ApplySeaDexObservation(&old, next, first)
	ranks := map[string]int{}
	for _, x := range ConfigureSeaDex(merged, nil).Torrents {
		ranks[x.InfoHash] = x.Preference
	}
	if ranks[a] != 3 || ranks[b] != 3 {
		t.Fatalf("first omission changed the held rank or misranked the new torrent: %v", ranks)
	}
	confirmed, _ := ApplySeaDexObservation(&merged, next, first.Add(SeaDexRetirementDelay))
	for _, x := range ConfigureSeaDex(confirmed, nil).Torrents {
		if x.InfoHash == a && x.Preference != 0 {
			t.Fatalf("confirmed omission did not retire: %+v", x)
		}
	}
}

// A row no catalog title maps but a held file references is refreshed by a complete answer.
func TestHeldOnlySnapshotRefreshes(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	h := strings.Repeat("a", 40)
	for _, q := range []string{`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1,'movie','Held',0,0)`,
		`INSERT INTO link(id,title_id,path,target,file_metadata,created_at,updated_at) VALUES(1,1,'/l','/t','{"InfoHash":"` + h + `","SeaDexPreference":3}',0,0)`} {
		if _, err := st.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for key, hash := range map[int]string{1: h, 2: strings.Repeat("c", 40)} {
		b, _ := json.Marshal(SeaDexSnapshot{AniList: key, Torrents: []SeaDexStanding{{InfoHash: hash, Preference: 3}}})
		if _, err := st.Writer().Exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing',?,200,?,1,9999999999999)`, key, b); err != nil {
			t.Fatal(err)
		}
	}
	ev := NewEvidence(EvidenceConfig{Store: st, Policy: testPolicy(), Now: func() time.Time { return time.Unix(100000, 0) }})
	if err := ev.recordSeaDex(ctx, []SeaDexSnapshot{{AniList: 1, Torrents: []SeaDexStanding{{InfoHash: h, Preference: 2, IsBest: true, Facts: &sdTorrent{IsBest: true}}}}}); err != nil {
		t.Fatal(err)
	}
	var got []byte
	if err := st.Reader().QueryRow(`SELECT body FROM metadata_cache WHERE source='seadex-standing' AND key='1'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	var s SeaDexSnapshot
	if err := json.Unmarshal(got, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Torrents) != 1 || s.Torrents[0].Facts == nil {
		t.Fatalf("held-only legacy snapshot did not refresh: %s", got)
	}
}

// M: a SeaDex answer whose standing cannot be stored fails the search: no caller decides it against stale standing.
func TestStandingWriteFailureFailsSearch(t *testing.T) {
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
	// A version prefers SeaDex (when none does, the answer is used and not cached instead).
	h.svc.SetEvidence(NewEvidence(EvidenceConfig{Store: st, Policy: preferSeaDexPolicy(), Now: h.clock}))
	res, err := h.svc.Search(context.Background(), "seadex", Query{Type: QueryMovie, AniList: 1})
	if err == nil {
		t.Fatalf("fresh answer published as successful despite failed standing commit: releases=%d", len(res.Releases))
	}
	h.svc.mu.Lock()
	cached := len(h.svc.cache)
	h.svc.mu.Unlock()
	if cached != 0 {
		t.Fatal("an unstored answer was cached")
	}
}

// M: two searches observing one entry overlap; the older answer commits last and must not supersede the newer one
// (it would mark the newer torrent missing).
func TestOlderObservationNeverSupersedes(t *testing.T) {
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
	go func() {
		_, err := h.svc.Search(context.Background(), "seadex", Query{Type: QueryMovie, AniList: 1})
		done <- err
	}()
	<-firstIn
	h.advance(time.Minute)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Search(context.Background(), "seadex", Query{Type: QueryMovie, AniList: 1, Title: "Film"}); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := st.Reader().QueryRow(`SELECT body FROM metadata_cache WHERE source='seadex-standing' AND key='1'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var s SeaDexSnapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Torrents) != 2 || s.Torrents[0].MissingSince == 0 || s.Torrents[1].InfoHash != b || s.Torrents[1].MissingSince != 0 {
		t.Fatalf("the older answer superseded the newer one: %s", raw)
	}
}

// J: an expanded record the entry's relation list does not name is no presence evidence: it does not clear a
// pending retirement.
func TestUnrelatedExpandedRecordIsNoPresence(t *testing.T) {
	n := newNet(t)
	present := `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[{"alID":500,"trs":["pick1"],"expand":{"trs":[{"id":"pick1","infoHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","tracker":"Nyaa","isBest":true,"files":[{"name":"Show BD 720p.mkv","length":3000000000}]}]}}]}`
	body := present
	n.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
	p := testPolicy()
	h := newHarness(t, n, p, false)
	st := openStore(t)
	if _, err := st.Writer().Exec(`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1,'movie','Held',0,0); INSERT INTO title_external_id VALUES(1,'anilist','500')`); err != nil {
		t.Fatal(err)
	}
	h.svc.SetEvidence(NewEvidence(EvidenceConfig{Store: st, Policy: p, Now: h.clock}))
	read := func() SeaDexStanding {
		t.Helper()
		search(t, h, "seadex", Query{Type: QueryRSS})
		var raw []byte
		if err := st.Reader().QueryRow(`SELECT body FROM metadata_cache WHERE source='seadex-standing' AND key='500'`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var s SeaDexSnapshot
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		return s.Torrents[0]
	}
	read()
	h.advance(time.Hour)
	body = `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[{"alID":500,"trs":[]}]}`
	pending := read()
	h.advance(time.Hour)
	body = strings.Replace(present, `"trs":["pick1"]`, `"trs":["different"]`, 1)
	if got := read(); pending.MissingSince == 0 || got.MissingSince != pending.MissingSince {
		t.Fatalf("an unrelated expanded record cleared the pending retirement: %d -> %d", pending.MissingSince, got.MissingSince)
	}
}

// D: an entry mapped to a series' whole season 1 and, by franchise mapping, to two season 2 episodes covers both.
func TestMixedSeasonMappingCovers(t *testing.T) {
	st := openStore(t)
	for _, q := range []string{
		`INSERT INTO title (id, kind, name, tvdb_id, created_at, updated_at) VALUES (1, 'series', 'Show', 100, 0, 0)`,
		`INSERT INTO title_external_id (title_id, source, external_id) VALUES (1, 'anilist', '11')`,
		`INSERT INTO anime_franchise (slug, name, seed_anilist, created_at, updated_at) VALUES ('f', 'F', 11, 0, 0)`,
		`INSERT INTO anime_entry (franchise, key, origin, anilist_id, tmdb_movie, body) VALUES ('f', 'al:11', 'anilist', 11, '[]', '{}')`,
		`INSERT INTO anime_mapping (franchise, entry_key, status, tvdb_series, episodes, updated_at) VALUES ('f', 'al:11', 'auto', 100, '[[2,1],[2,2]]', 0)`,
	} {
		if _, err := st.Writer().Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	c, err := LoadSeaDexCatalog(context.Background(), st.Reader(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ms := c.Titles()[1]
	if len(ms) != 1 {
		t.Fatalf("mappings: %+v", ms)
	}
	m := ms[0]
	for _, c := range []struct {
		ep   [2]int
		want bool
	}{{[2]int{1, 5}, true}, {[2]int{2, 1}, true}, {[2]int{2, 3}, false}} {
		if got := m.Covers([][2]int{c.ep}); got != c.want {
			t.Fatalf("Covers(%v) = %v, want %v (%+v)", c.ep, got, c.want, m)
		}
	}
}

// A release whose reported hash and magnet disagree is dropped by the service (and noted); a hashless release
// takes its magnet's hash.
func TestServiceDropsConflictingIdentity(t *testing.T) {
	loadFixtures(t, "prowlarr_caps.xml")
	n := newNet(t)
	a, b, c := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	serveProwlarr(n, &recorder{}, "prowlarr_caps.xml", func(r *http.Request) string {
		return `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>` +
			`<item><title>Conflict.2160p</title><guid>g1</guid><size>1000</size><torznab:attr name="infohash" value="` + a + `"/>` +
			`<torznab:attr name="magneturl" value="magnet:?xt=urn:btih:` + b + `"/><torznab:attr name="seeders" value="1"/></item>` +
			`<item><title>Hashless.2160p</title><guid>g2</guid><size>1000</size>` +
			`<torznab:attr name="magneturl" value="magnet:?xt=urn:btih:` + c + `"/><torznab:attr name="seeders" value="1"/></item>` +
			`</channel></rss>`
	})
	h := newHarness(t, n, testPolicy(), false)
	res := search(t, h, "prowlarr", Query{Type: QueryText, Title: "release"})
	if len(res.Releases) != 1 || res.Releases[0].Title != "Hashless.2160p" || res.Releases[0].InfoHash != c {
		t.Fatalf("releases after identity normalisation: %+v", res.Releases)
	}
	if !strings.Contains(strings.Join(res.Notes, "; "), "conflicting or unverifiable torrent identities") {
		t.Fatalf("dropped release not noted: %v", res.Notes)
	}
}
