package rss

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConflictFilteringDoesNotEndRSSPage(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	items := filler(205, 0)
	e.prow.setRSS(items...)
	e.net.serve("prowlarr.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		e.prow.ServeHTTP(rec, r)
		body := rec.Body.String()
		if r.URL.Query().Get("t") == "search" && r.URL.Query().Get("offset") == "" {
			body = strings.Replace(body, `</item>`, `<torznab:attr name="magneturl" value="magnet:?xt=urn:btih:`+strings.Repeat("a", 40)+`" /></item>`, 1)
		}
		fmt.Fprint(w, body)
	}))
	res, end, _, err := e.r.rssPages(e.ctx, "prowlarr", []int{2000}, rssCursor{key: "h:" + items[150].hash})
	if err != nil {
		t.Fatal(err)
	}
	if end != rssReached || len(res.Releases) != 199 {
		t.Fatalf("filtering a conflict ended catch-up early: end=%v releases=%d requests=%d; want reached with 199 releases", end, len(res.Releases), res.Requests)
	}
}

func TestEntireFilteredPageContinues(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	items := filler(205, 0)
	e.prow.setRSS(items...)
	e.net.serve("prowlarr.test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		e.prow.ServeHTTP(rec, r)
		body := rec.Body.String()
		if r.URL.Query().Get("t") == "search" && r.URL.Query().Get("offset") == "" {
			body = strings.Replace(body, `</item>`, `<torznab:attr name="magneturl" value="magnet:?xt=urn:btih:`+strings.Repeat("a", 40)+`" /></item>`, -1)
		}
		fmt.Fprint(w, body)
	}))
	res, end, newest, err := e.r.rssPages(e.ctx, "prowlarr", []int{2000}, rssCursor{key: "h:" + items[150].hash})
	if err != nil {
		t.Fatal(err)
	}
	if end != rssReached || len(res.Releases) != 100 {
		t.Fatalf("filtering a conflict ended catch-up early: end=%v releases=%d requests=%d; want reached with 100 releases", end, len(res.Releases), res.Requests)
	}
	if newest == nil || newest.key != "h:"+items[0].hash {
		t.Fatalf("lost raw head: %+v", newest)
	}
}
