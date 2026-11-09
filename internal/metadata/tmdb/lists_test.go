package tmdb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCollectionAndListRefresh(t *testing.T) {
	calls := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		if r.URL.Query().Get("api_key") != key {
			t.Error("missing TMDb key")
		}
		switch r.URL.Path {
		case "/3/collection/7":
			fmt.Fprintf(w, `{"id":7,"name":"Collection","parts":[{"id":%d,"title":"Part"}]}`, calls[r.URL.Path])
		case "/3/list/9":
			fmt.Fprintf(w, `{"id":9,"items":[{"id":%d,"title":"Movie","media_type":"movie"}]}`, calls[r.URL.Path])
		default:
			http.Error(w, `{}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := newClient(t, srv, sqlCache(t))
	ctx := context.Background()
	for i := 1; i <= 2; i++ {
		collection, err := c.Collection(ctx, 7)
		if err != nil {
			t.Fatal(err)
		}
		list, err := c.List(ctx, 9)
		if err != nil {
			t.Fatal(err)
		}
		if collection.Name != "Collection" || len(collection.Parts) != 1 || collection.Parts[0].ID != i || len(list.Items) != 1 || list.Items[0].ID != i {
			t.Fatalf("refresh used stale membership: %+v %+v", collection, list)
		}
	}
	if _, err := c.Collection(ctx, 0); err == nil {
		t.Fatal("invalid collection id accepted")
	}
	if _, err := c.List(ctx, -1); err == nil {
		t.Fatal("invalid list id accepted")
	}
	if _, err := c.Collection(ctx, 404); !errors.Is(err, ErrNotFound) {
		t.Fatalf("collection error=%v", err)
	}
	if _, err := c.List(ctx, 404); !errors.Is(err, ErrNotFound) {
		t.Fatalf("list error=%v", err)
	}
}
