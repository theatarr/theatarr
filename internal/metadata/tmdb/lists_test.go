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

// A list as TMDb answers it: the id as a string, and the items in pages. Every page is read (total_pages, or
// item_count where TMDb sends no total_pages), and a page that fails fails the read.
func TestListStringIDAndPages(t *testing.T) {
	var pages []string
	failPage := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		pages = append(pages, r.URL.Path+" "+page)
		if page == failPage {
			http.Error(w, `{"status_message":"refused"}`, http.StatusBadRequest)
			return
		}
		switch r.URL.Path + " " + page {
		case "/3/list/1 1":
			fmt.Fprint(w, `{"created_by":"someone","description":"Two pages","favorite_count":0,"id":"1","item_count":3,"iso_639_1":"en","name":"Long","poster_path":null,
				"items":[{"id":11,"media_type":"movie","title":"One"},{"id":12,"media_type":"movie","title":"Two"}],"page":1,"total_pages":2,"total_results":3}`)
		case "/3/list/1 2":
			fmt.Fprint(w, `{"id":"1","item_count":3,"name":"Long","items":[{"id":13,"media_type":"movie","title":"Three"}],"page":2,"total_pages":2,"total_results":3}`)
		case "/3/list/2 1":
			fmt.Fprint(w, `{"id":"2","item_count":2,"name":"Counted","items":[{"id":21}]}`)
		case "/3/list/2 2":
			fmt.Fprint(w, `{"id":"2","item_count":2,"name":"Counted","items":[{"id":22}]}`)
		case "/3/list/3 1":
			fmt.Fprint(w, `{"id":3,"name":"Short","items":[{"id":31}]}`)
		default:
			http.Error(w, `{}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := newClient(t, srv, sqlCache(t))
	ctx := context.Background()
	l, err := c.List(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if l.ID != 1 || l.Name != "Long" || l.Description != "Two pages" || len(l.Items) != 3 || l.Items[2].ID != 13 || len(pages) != 2 {
		t.Fatalf("paged list %+v after %v", l, pages)
	}
	// No total_pages: item_count says there is more.
	if l, err = c.List(ctx, 2); err != nil || len(l.Items) != 2 || l.Items[1].ID != 22 {
		t.Fatalf("counted list %+v %v", l, err)
	}
	// A numeric id and no paging fields: one request.
	pages = nil
	if l, err = c.List(ctx, 3); err != nil || l.ID != 3 || len(l.Items) != 1 || len(pages) != 1 {
		t.Fatalf("short list %+v %v after %v", l, err, pages)
	}
	// The second page fails: no partial list.
	failPage = "2"
	if l, err = c.List(ctx, 1); err == nil || l != nil {
		t.Fatalf("a list with a failed page: %+v %v", l, err)
	}
}
