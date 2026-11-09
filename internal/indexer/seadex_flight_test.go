package indexer

import (
	"context"
	"errors"
	"testing"
)

func TestCanceledCallerCreatesNoFlight(t *testing.T) {
	svc := lockService(nil, "test", func(context.Context, Query) (*Result, error) { return &Result{}, nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := svc.Search(ctx, "test", Query{Type: QueryMovie, Title: "Movie"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation: %v", err)
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if len(svc.inflight)+len(svc.cache) != 0 {
		t.Fatal("canceled caller created a flight")
	}
}
