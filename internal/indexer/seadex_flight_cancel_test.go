package indexer

import (
	"context"
	"testing"
	"time"
)

func TestLastWaiterCancelsFlight(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	svc := lockService(nil, "test", func(ctx context.Context, _ Query) (*Result, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _, _ = svc.Search(ctx, "test", Query{Type: QueryMovie, Title: "Movie"}) }()
	<-started
	cancel()
	<-done
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("flight survived its last waiter")
	}
}
