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

func TestOlderObservationNeverSupersedesConcurrent(t *testing.T) {
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
	secondDone := make(chan error, 1)
	before := calls.Load()
	go func() {
		_, err := h.svc.Search(context.Background(), "seadex", Query{Type: QueryMovie, AniList: 1, Title: "Film"})
		secondDone <- err
	}()
	time.Sleep(80 * time.Millisecond)
	blocked := calls.Load() == before
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Fatal("contending search fetched before first observation committed")
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
func TestObservationOrderAcrossServicesConcurrent(t *testing.T) {
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
	secondDone := make(chan error, 1)
	before := calls.Load()
	go func() {
		_, err := next.svc.Search(context.Background(), "seadex", Query{Type: QueryMovie, AniList: 1, Title: "Film"})
		secondDone <- err
	}()
	time.Sleep(80 * time.Millisecond)
	blocked := calls.Load() == before
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Fatal("contending search fetched before first observation committed")
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
func TestPerEntryObservationTimeConcurrent(t *testing.T) {
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
	secondDone := make(chan error, 1)
	before := count.Load()
	go func() {
		_, err := h.svc.Search(context.Background(), "seadex", Query{Type: QueryMovie, AniList: 2})
		secondDone <- err
	}()
	time.Sleep(80 * time.Millisecond)
	blocked := count.Load() == before
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Fatal("contending search fetched before first observation committed")
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
