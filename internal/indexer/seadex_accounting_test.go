package indexer

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

func TestFailedSeaDexRequestCount(t *testing.T) {
	n := newNet(t)
	n.serve("seadex.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter") == "alID=1" {
			fmt.Fprint(w, `{"page":1,"perPage":100,"totalItems":0,"totalPages":0,"items":[]}`)
			return
		}
		fmt.Fprint(w, `not-json`)
	}))
	h := newHarness(t, n, testPolicy(), false)
	h.svc.SetMetadata(Metadata{AniList: AniListFunc(func(context.Context, Query) ([]int, error) { return []int{1, 2}, nil })})
	_, err := h.svc.Search(context.Background(), "seadex", Query{Type: QueryMovie, TMDb: 1})
	if err == nil || RequestsSpent(err) != 2 {
		t.Fatalf("failed two-request search: count=%d err=%v", RequestsSpent(err), err)
	}
}
