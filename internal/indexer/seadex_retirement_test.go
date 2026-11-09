package indexer

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Regression tests for SeaDex record retirement and standing.

// N: snapshot quality uses the same exact-hash inventory fallback as releases.
func TestSnapshotQualityUsesInventoryFallback(t *testing.T) {
	n := newNet(t)
	body := `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[{"alID":500,"trs":["pick1"],"expand":{"trs":[{"id":"pick1","infoHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","tracker":"Nyaa","isBest":true,"files":[{"name":"Show - 01.mkv","length":3000000000},{"name":"Show - 02.mkv","length":3000000000}]}]}}]}`
	n.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
	h := newHarness(t, n, testPolicy(), false)
	h.svc.SetMetadata(Metadata{Torrents: fixtureTorrentNames{strings.Repeat("a", 40): {"Show (BD 720p)"}}})
	res := search(t, h, "seadex", Query{Type: QuerySeries, AniList: 500, Title: "Show", Season: 1})
	if len(res.SeaDex) != 1 || len(res.Releases) != 1 {
		t.Fatalf("bad setup %+v", res)
	}
	if res.SeaDex[0].Torrents[0].Quality != "Bluray-720p" {
		t.Fatalf("release=%s, snapshot quality=%s", res.Releases[0].Title, res.SeaDex[0].Torrents[0].Quality)
	}
}

// Retirement needs two complete omissions 12 h apart; an incomplete answer never advances; reappearance clears.
func TestRetirementClockAndReappearance(t *testing.T) {
	n := newNet(t)
	body := `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[{"alID":500,"trs":["pick1"],"expand":{"trs":[{"id":"pick1","infoHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","tracker":"Nyaa","isBest":true,"files":[{"name":"Show BD 720p.mkv","length":3000000000}]}]}}]}`
	present := body
	empty := `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[{"alID":500,"trs":[]}]}`
	n.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
	p := testPolicy()
	h := newHarness(t, n, p, false)
	st := openStore(t)
	if _, err := st.Writer().Exec(`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1,'movie','Held',0,0); INSERT INTO title_external_id VALUES(1,'anilist','500')`); err != nil {
		t.Fatal(err)
	}
	h.svc.SetEvidence(NewEvidence(EvidenceConfig{Store: st, Policy: p, Now: h.clock}))
	observe := func(want int, missing bool) {
		t.Helper()
		search(t, h, "seadex", Query{Type: QueryRSS})
		var raw []byte
		if err := st.Reader().QueryRowContext(context.Background(), `SELECT body FROM metadata_cache WHERE source='seadex-standing' AND key='500'`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var s SeaDexSnapshot
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		got := ConfigureSeaDex(s, nil).Torrents[0]
		if got.Preference != want || (got.MissingSince != 0) != missing {
			t.Fatalf("got %+v want rank %d missing %v", got, want, missing)
		}
	}
	observe(3, false)
	h.advance(time.Hour)
	body = empty
	observe(3, true)
	h.advance(13 * time.Hour)
	body = `{"items":[]}`
	observe(3, true)
	h.advance(time.Hour)
	body = present
	observe(3, false)
	h.advance(time.Hour)
	body = empty
	observe(3, true)
	h.advance(-2 * time.Hour)
	observe(3, true)
	h.advance(13 * time.Hour)
	observe(3, true)
	h.advance(time.Hour)
	observe(0, true)
	h.advance(time.Hour)
	body = present
	observe(3, false)
}

// J: positive presence inside an otherwise incomplete answer clears a pending retirement.
func TestPartialPositiveAnswerClearsRetirement(t *testing.T) {
	n := newNet(t)
	body := `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[{"alID":500,"trs":["pick1"],"expand":{"trs":[{"id":"pick1","infoHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","tracker":"Nyaa","isBest":true,"files":[{"name":"Show BD 720p.mkv","length":3000000000}]}]}}]}`
	present := body
	empty := `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[{"alID":500,"trs":[]}]}`
	n.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
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
		return ConfigureSeaDex(s, nil).Torrents[0]
	}
	read()
	h.advance(time.Hour)
	body = empty
	read()
	h.advance(6 * time.Hour)
	body = strings.Replace(present, `"trs":["pick1"]`, `"trs":["pick1","other"]`, 1)
	middle := read()
	h.advance(6 * time.Hour)
	body = empty
	last := read()
	if middle.MissingSince != 0 || last.Preference == 0 {
		t.Fatalf("positive partial response left pending=%d; six hours later rank=%d", middle.MissingSince, last.Preference)
	}
}

// I: an expanded record without hash or tracker makes the answer incomplete, not an omission.
func TestRecordWithoutIdentityIsIncomplete(t *testing.T) {
	n := newNet(t)
	body := `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[{"alID":1,"trs":["6vdej49pr26gca0"],"expand":{"trs":[{"id":"6vdej49pr26gca0"}]}}]}`
	n.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
	h := newHarness(t, n, testPolicy(), false)
	got := search(t, h, "seadex", Query{Type: QueryMovie, AniList: 1})
	if len(got.SeaDex) > 0 {
		t.Fatalf("partial torrent lacks tracker and hash, yet considered a complete omission: %+v", got.SeaDex)
	}
}

// K: the present set is scored without the pending-omitted torrent.
func TestPendingOmissionDoesNotSuppressNewWinner(t *testing.T) {
	old := SeaDexSnapshot{AniList: 1, Torrents: []SeaDexStanding{{InfoHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Preference: 2, IsBest: true, Facts: &sdTorrent{IsBest: true}}}}
	next := SeaDexSnapshot{AniList: 1, Torrents: []SeaDexStanding{{InfoHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Preference: 1, Facts: &sdTorrent{}}}}
	merged := ConfigureSeaDex(confirmSeaDex(old, next, time.Unix(100000, 0)), nil)
	for _, v := range merged.Torrents {
		if v.InfoHash == "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" && v.Preference != 3 {
			t.Fatalf("the only currently listed torrent did not become configured winner while old membership was retained: %+v", merged.Torrents)
		}
	}
}

// M: a SeaDex answer is cached only after its standing is committed; while the write waits, no caller can be served
// the fresh answer from the cache.
func TestCachePublishedAfterStanding(t *testing.T) {
	n := newNet(t)
	body := `{"page":1,"perPage":100,"totalItems":1,"totalPages":1,"items":[{"alID":1,"trs":["6vdej49pr26gca0"],"expand":{"trs":[{"id":"6vdej49pr26gca0","infoHash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","tracker":"Nyaa","isBest":true,"files":[{"name":"Film BD 1080p.mkv","length":5000000000}]}]}}]}`
	n.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
	p := testPolicy()
	h := newHarness(t, n, p, false)
	st := openStore(t)
	if _, err := st.Writer().Exec(`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1,'movie','Film',0,0); INSERT INTO title_external_id VALUES(1,'anilist','1')`); err != nil {
		t.Fatal(err)
	}
	h.svc.SetEvidence(NewEvidence(EvidenceConfig{Store: st, Policy: p, Now: h.clock}))
	tx, err := st.Writer().Begin()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	q := Query{Type: QueryMovie, AniList: 1, Title: "Film"}
	go func() { _, err := h.svc.Search(context.Background(), "seadex", q); done <- err }()
	time.Sleep(300 * time.Millisecond)
	h.svc.mu.Lock()
	cached := len(h.svc.cache)
	h.svc.mu.Unlock()
	_ = tx.Rollback()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if cached != 0 {
		t.Fatal("the answer was cached before its standing was stored")
	}
	var stored string
	if err := st.Reader().QueryRow(`SELECT body FROM metadata_cache WHERE source='seadex-standing' AND key='1'`).Scan(&stored); err != nil || !strings.Contains(stored, "bbbbbbbb") {
		t.Fatalf("standing not stored: %q %v", stored, err)
	}
	h.svc.mu.Lock()
	cached = len(h.svc.cache)
	h.svc.mu.Unlock()
	if cached != 1 {
		t.Fatalf("stored answer not cached: %d", cached)
	}
}
