package web

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/store"
)

// seedDanglingScan records one dangling-link report summary.
func seedDanglingScan(e *env, links, dangling int) {
	e.exec(`INSERT INTO library_dangling_scan (at, roots, links, dangling, took_ms) VALUES (?, '[]', ?, ?, 5)`,
		store.Millis(testNow), links, dangling)
}

func TestHealthDanglingAPI(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()

	// No report yet: at is null and the list is empty, not null.
	req := httptest.NewRequest("GET", APIBase+"/health/dangling", nil)
	req.Header.Set(auth.APIKeyHeader, key)
	rec := e.do(req)
	if rec.Code != 200 || rec.Body.String() != `{"at":null,"links":0,"dangling":0,"truncated":false,"items":[]}`+"\n" {
		t.Fatalf("no report: %d %s", rec.Code, rec.Body.String())
	}

	// Three dangling links: a dead link of title 1, an active link of title 2, and a path Theatarr never linked
	// under an extra root. The oldest comes first.
	seedDanglingScan(e, 40, 3)
	old, mid, late := store.Millis(testNow.Add(-72*time.Hour)), store.Millis(testNow.Add(-48*time.Hour)),
		store.Millis(testNow.Add(-time.Hour))
	now := store.Millis(testNow)
	e.exec(`INSERT INTO library_dangling (path, target, root, version_key, folder, cause, first_seen_at, last_seen_at)
		VALUES ('/media/web/Beta (2021)/Beta.mkv', '/mnt/zurg/Beta.2021/Beta.mkv', '/media/web', 'movie-web',
			'Beta.2021', NULL, ?, ?),
		('/media/remux/Alpha (2020)/Alpha.mkv', '/mnt/zurg/Alpha.2020/Alpha.mkv', '/media/remux', 'movie-remux',
			'Alpha.2020', 'no_video', ?, ?),
		('/media/other/Stray.mkv', '/mnt/zurg/Stray/Stray.mkv', '/media/other', NULL, 'Stray', NULL, ?, ?)`,
		mid, now, old, now, late, now)
	e.exec(`INSERT INTO link (path, target, title_id, state, created_at, updated_at) VALUES
		('/media/remux/Alpha (2020)/Alpha.mkv', '/mnt/zurg/Alpha.2020/Alpha.mkv', 1, 'dead', ?, ?),
		('/media/web/Beta (2021)/Beta.mkv', '/mnt/zurg/Beta.2021/Beta.mkv', 2, 'active', ?, ?)`, now, now, now, now)
	// The path under the extra root is not one of the libraries' and is left out (D9).
	var got DanglingList
	if code := e.getKey(APIBase+"/health/dangling", key, &got); code != 200 || got.At == nil ||
		!got.At.Equal(testNow) || got.Links != 40 || got.Dangling != 2 || got.Truncated || len(got.Items) != 2 {
		t.Fatalf("list: %d %+v", code, got)
	}
	a, b := got.Items[0], got.Items[1]
	if a.Title != "Alpha" || a.TitleID != 1 || a.LinkState != "dead" || a.Cause != "no_video" ||
		a.Library != "movie-remux" || a.Label != e.web.versionLabel("movie-remux") || a.Label == "" ||
		a.Folder != "Alpha.2020" || a.Target != "/mnt/zurg/Alpha.2020/Alpha.mkv" ||
		!a.FirstSeen.Equal(testNow.Add(-72*time.Hour)) {
		t.Errorf("dead link: %+v", a)
	}
	if b.Title != "Beta" || b.TitleID != 2 || b.LinkState != "active" || b.Cause != "" || b.Library != "movie-web" {
		t.Errorf("active link: %+v", b)
	}

	// Admins only.
	uc, _ := e.session(e.account("viewer", "user", "active", false))
	if code := e.get(APIBase+"/health/dangling", uc).Code; code != 403 {
		t.Errorf("user: %d", code)
	}
	if code := e.get(APIBase+"/health/dangling", nil).Code; code != 401 {
		t.Errorf("signed out: %d", code)
	}
}

// The answer holds at most danglingListMax links, the oldest, and says when there are more.
func TestHealthDanglingAPITruncates(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	n := danglingListMax + 3
	seedDanglingScan(e, 2*n, n)
	now := store.Millis(testNow)
	for i := 0; i < n; i++ {
		// Path order is the reverse of age, so the cap must follow first_seen.
		e.exec(`INSERT INTO library_dangling (path, target, root, version_key, folder, first_seen_at, last_seen_at)
			VALUES (?, '/mnt/zurg/x', '/media/other', 'movie-web', 'x', ?, ?)`, fmt.Sprintf("/media/other/%04d.mkv", n-i),
			store.Millis(testNow.Add(time.Duration(i-n)*time.Minute)), now)
	}
	var got DanglingList
	if code := e.getKey(APIBase+"/health/dangling", e.apiKey(), &got); code != 200 || !got.Truncated ||
		got.Dangling != n || len(got.Items) != danglingListMax {
		t.Fatalf("list: %d truncated=%v dangling=%d items=%d", code, got.Truncated, got.Dangling, len(got.Items))
	}
	if first, last := got.Items[0].Path, got.Items[danglingListMax-1].Path; first != fmt.Sprintf("/media/other/%04d.mkv", n) ||
		last != "/media/other/0004.mkv" {
		t.Errorf("oldest first: %s … %s", first, last)
	}
}
