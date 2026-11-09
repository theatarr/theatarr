package indexer

import (
	"context"
	"testing"
	"time"
)

func TestLockCoversCommit(t *testing.T) {
	st := lockMappedStore(t)
	tx, err := st.Writer().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	firstFetch, secondFetch := make(chan struct{}), make(chan struct{})
	first := lockService(st, "seadex", func(context.Context, Query) (*Result, error) { close(firstFetch); return lockAnswer("a"), nil })
	second := lockService(st, "seadex", func(context.Context, Query) (*Result, error) { close(secondFetch); return lockAnswer("b"), nil })
	done1, done2 := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := first.Search(context.Background(), "seadex", Query{Type: QueryMovie, AniList: 1})
		done1 <- err
	}()
	<-firstFetch
	time.Sleep(80 * time.Millisecond)
	go func() {
		_, err := second.Search(context.Background(), "seadex", Query{Type: QueryMovie, AniList: 1})
		done2 <- err
	}()
	broke := false
	select {
	case <-secondFetch:
		broke = true
	case <-time.After(100 * time.Millisecond):
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = <-done1; err != nil {
		t.Fatal(err)
	}
	if err = <-done2; err != nil {
		t.Fatal(err)
	}
	if broke {
		t.Fatal("second fetch entered while first observation was waiting for its write transaction to commit")
	}
}
