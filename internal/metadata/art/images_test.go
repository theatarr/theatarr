package art

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The proxy serves from the cache after the first fetch, one fetch per file however many ask at once.
func TestImagesCache(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	const file = "abcdefghij0123456789.jpg"
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.images.Get(ctx, "w185", file); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	p, err := e.images.Get(ctx, "w185", file)
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join(e.dir, "w185", "ab", file) {
		t.Errorf("path %s", p)
	}
	if n := e.fake.count("img.test/t/p/w185/" + file); n != 1 {
		t.Errorf("%d fetches, want 1", n)
	}
	if got := e.images.Size(); got != int64(len(e.fake.png)) {
		t.Errorf("size %d, want %d", got, len(e.fake.png))
	}
}

// Nothing outside TMDb's path pattern and size list is fetched.
func TestImagesRefuse(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	for _, tc := range []struct{ size, file string }{
		{"w185", "../../../etc/passwd"}, {"w185", "abc.jpg"}, {"w185", "abcdefghij0123456789.gif"},
		{"w185", "abcdefghij0123456789.svg"}, {"w185", "abcdef/hij0123456789.jpg"}, {"w185", "abcdefghij0123456789.jpg?x=1"},
		{"w185", strings.Repeat("a", 41) + ".jpg"}, {"w9999", "abcdefghij0123456789.jpg"}, {"original", "abcdefghij0123456789.jpg"},
		{"", "abcdefghij0123456789.jpg"}, {"w185/../w342", "abcdefghij0123456789.jpg"},
	} {
		if _, err := e.images.Get(ctx, tc.size, tc.file); !errors.Is(err, ErrBadImage) {
			t.Errorf("%s/%s: %v", tc.size, tc.file, err)
		}
		rec := httptest.NewRecorder()
		e.svc.ServeTMDb(rec, httptest.NewRequest("GET", "/img/tmdb/x/y", nil), tc.size, tc.file)
		if rec.Code != http.StatusNotFound {
			t.Errorf("serve %s/%s: %d", tc.size, tc.file, rec.Code)
		}
	}
	if n := e.fake.count(""); n != 0 {
		t.Errorf("%d requests sent", n)
	}
	// original is served only when the policy allows it.
	e.images.set.Original = true
	if _, err := e.images.Get(ctx, "original", "abcdefghij0123456789.jpg"); err != nil {
		t.Errorf("original allowed: %v", err)
	}
}

// The cache stays under max_bytes, evicting the least recently used files.
func TestImagesEvict(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	size := int64(len(e.fake.png))
	e.images.set.MaxBytes = size*3 + size/2 // room for three
	names := []string{"aaaaaaaaaa1.jpg", "bbbbbbbbbb2.jpg", "cccccccccc3.jpg", "dddddddddd4.jpg"}
	for i, n := range names[:3] {
		p, err := e.images.Get(ctx, "w92", n)
		if err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(time.Duration(i-10) * time.Minute)
		os.Chtimes(p, at, at)
	}
	// The first is read again: a hit more than an hour after its last use counts as a use.
	old := time.Now().Add(-2 * time.Hour)
	p0 := filepath.Join(e.dir, "w92", "aa", names[0])
	os.Chtimes(p0, old, old)
	if _, err := e.images.Get(ctx, "w92", names[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := e.images.Get(ctx, "w92", names[3]); err != nil {
		t.Fatal(err)
	}
	if got := e.images.Size(); got > e.images.set.MaxBytes {
		t.Errorf("cache %d bytes over %d", got, e.images.set.MaxBytes)
	}
	exists := func(n string) bool {
		_, err := os.Stat(filepath.Join(e.dir, "w92", n[:2], n))
		return err == nil
	}
	if !exists(names[0]) || exists(names[1]) || !exists(names[3]) {
		t.Errorf("after eviction: a %v b %v c %v d %v", exists(names[0]), exists(names[1]), exists(names[2]), exists(names[3]))
	}
}

func TestServeTitle(t *testing.T) {
	e := newTestEnv(t)
	e.seed(t)
	ctx := context.Background()
	if _, err := e.svc.RunBatch(ctx); err != nil {
		t.Fatal(err)
	}
	hash := e.meta(t)[1].ArtHash
	serve := func(id int64, kind, size, v string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		e.svc.ServeTitle(rec, httptest.NewRequest("GET", "/img/t/x?v="+v, nil), id, kind, size)
		return rec
	}
	rec := serve(1, KindPoster, "w342", hash)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" ||
		rec.Header().Get("Cache-Control") != CacheImmutable || rec.Body.Len() != len(e.fake.png) ||
		rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("poster: %d %v %d", rec.Code, rec.Header(), rec.Body.Len())
	}
	before := e.fake.count("img.test")
	if rec := serve(1, KindPoster, "w342", "old"); rec.Code != 200 || rec.Header().Get("Cache-Control") != CacheCurrent {
		t.Errorf("old v: %d %v", rec.Code, rec.Header())
	}
	if n := e.fake.count("img.test"); n != before {
		t.Errorf("cached poster fetched again (%d)", n-before)
	}
	if rec := serve(1, KindLogo, "w500", hash); rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("logo: %d %v", rec.Code, rec.Header())
	}
	// No art yet (title 3 has no poster; title 6 has no row): generated art, short-lived.
	for _, id := range []int64{3, 6} {
		rec := serve(id, KindPoster, "w342", "")
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/svg+xml" ||
			rec.Header().Get("Cache-Control") != CacheFallback || !strings.Contains(rec.Body.String(), "<svg") {
			t.Errorf("fallback %d: %d %v", id, rec.Code, rec.Header())
		}
	}
	if rec := serve(1, "banner", "w342", ""); rec.Code != 404 {
		t.Errorf("bad kind: %d", rec.Code)
	}
	if rec := serve(1, KindPoster, "w9", ""); rec.Code != 404 {
		t.Errorf("bad size: %d", rec.Code)
	}
	if rec := serve(77, KindPoster, "w342", ""); rec.Code != 404 {
		t.Errorf("no title: %d", rec.Code)
	}
	// TMDb failing: generated art, not an error.
	e.fake.mu.Lock()
	e.fake.png = nil // an empty body is refused
	e.fake.mu.Unlock()
	if rec := serve(2, KindBackdrop, "w1280", ""); rec.Code != 200 || rec.Header().Get("Content-Type") != "image/svg+xml" {
		t.Errorf("fetch failure: %d %v", rec.Code, rec.Header())
	}
	// Without an image cache (no tmdb-images source) everything is generated.
	svc, _ := New(Config{Store: e.st, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	rec = httptest.NewRecorder()
	svc.ServeTitle(rec, httptest.NewRequest("GET", "/img/t/1/poster/w342", nil), 1, KindPoster, "w342")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/svg+xml" {
		t.Errorf("no cache: %d %v", rec.Code, rec.Header())
	}
}

func TestFallbackSVG(t *testing.T) {
	s := FallbackSVG(KindPoster, 7, `Tom & Jerry <script>alert(1)</script> and a very long title that goes on`, 1999)
	if strings.Contains(s, "<script") || !strings.Contains(s, "Tom &amp; Jerry") || !strings.Contains(s, ">1999<") ||
		!strings.HasPrefix(s, "<svg") {
		t.Errorf("svg: %s", s)
	}
	if FallbackSVG(KindPoster, 1, "x", 0) == FallbackSVG(KindPoster, 2, "x", 0) {
		t.Error("same art for two titles")
	}
	if got := wrap("one two three four five six seven eight nine ten eleven twelve", 9, 3); len(got) != 3 ||
		!strings.HasSuffix(got[2], "…") {
		t.Errorf("wrap: %q", got)
	}
}
