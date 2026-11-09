package indexer

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestSharedSeaDexWaiterOutlivesFirstCaller(t *testing.T) {
	st := lockMappedStore(t)
	holder := NewEvidence(EvidenceConfig{Store: st, Policy: testPolicy()})
	unlock, err := holder.lockSeaDex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	entered := make(chan struct{})
	svc := lockService(st, "seadex", func(context.Context, Query) (*Result, error) { close(entered); return lockAnswer("a"), nil })
	q := Query{Type: QueryMovie, AniList: 1}
	firstCtx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := svc.Search(firstCtx, "seadex", q); first <- err }()
	for i := 0; i < 1000; i++ {
		svc.mu.Lock()
		n := len(svc.inflight)
		svc.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	second := make(chan error, 1)
	go func() { _, err := svc.Search(context.Background(), "seadex", q); second <- err }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-first
	select {
	case err := <-second:
		t.Fatalf("live shared caller failed when first caller cancelled before lock released: %v", err)
	case <-time.After(80 * time.Millisecond):
	}
	unlock()
	unlock = nil
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("remaining caller never fetched")
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
}

func TestFailedRedirectCountsSentRequest(t *testing.T) {
	n := newNet(t)
	var sent atomic.Int32
	n.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Add(1)
		http.Redirect(w, r, "http://unlisted.test/", http.StatusFound)
	}))
	h := newHarness(t, n, testPolicy(), false)
	_, err := h.svc.Search(context.Background(), "seadex", Query{Type: QueryMovie, AniList: 1})
	if err == nil {
		t.Fatal("expected blocked redirect")
	}
	if got := RequestsSpent(err); got != int(sent.Load()) {
		t.Fatalf("count=%d, initial SeaDex requests sent=%d, err=%v", got, sent.Load(), err)
	}
}
