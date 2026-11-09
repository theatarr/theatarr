package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/journal"
	"github.com/theatarr/theatarr/internal/store"
)

func TestJournalAPI(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	now := store.Millis(testNow)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		score, state, error, source, created_at, updated_at) VALUES (1, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.WEB-DL-GRP',
		'dmm', 'WEBDL-2160p', 1500, 'failed', 'miss on every provider', 'acquire:1', ?, ?)`, hashA, now-5000, now-4000)
	key := e.apiKey()

	var page journal.Page
	if code := e.getKey(APIBase+"/journal?kind=grab,failure&title=alp&version=movie-web", key, &page); code != 200 ||
		len(page.Items) != 2 || page.Items[0].ID != "dl:1:fail" || page.Items[1].ID != "dl:1" {
		t.Fatalf("list: %d %+v", code, page.Items)
	}
	for _, q := range []string{"kind=nope", "severity=high", "suspect=maybe", "since=yesterday", "cursor=bm9wZQ"} {
		if code := e.getKey(APIBase+"/journal?"+q, key, nil); code != 400 {
			t.Errorf("%s: %d", q, code)
		}
	}
	var en journal.Entry
	if code := e.getKey(APIBase+"/journal/dl:1", key, &en); code != 200 || en.Kind != journal.KindGrab ||
		en.Title.Name != "Alpha" {
		t.Errorf("entry: %d %+v", code, en)
	}
	if code := e.getKey(APIBase+"/journal/dl:99", key, nil); code != 404 {
		t.Errorf("missing: %d", code)
	}

	post := func(id, body string, c *http.Cookie, withKey bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", APIBase+"/journal/"+id+"/verdict", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if withKey {
			r.Header.Set("X-Api-Key", key)
		}
		if c != nil {
			r.AddCookie(c)
		}
		return e.do(r)
	}
	if rec := post("dl:1", `{"verdict":"wrong","reason":"x"}`, nil, false); rec.Code != 401 {
		t.Errorf("no auth: %d", rec.Code)
	}
	if rec := post("dl:1", `{"verdict":"wrong","reason":"x"}`, e.login(), false); rec.Code != 403 {
		t.Errorf("session without CSRF: %d", rec.Code)
	}
	if rec := post("dl:1", `{"verdict":"meh","reason":"x"}`, nil, true); rec.Code != 400 {
		t.Errorf("bad verdict: %d", rec.Code)
	}
	if rec := post("dl:99", `{"verdict":"ok","reason":"x"}`, nil, true); rec.Code != 404 {
		t.Errorf("missing entry: %d", rec.Code)
	}
	rec := post("dl:1", `{"verdict":"wrong","reason":"not the movie"}`, nil, true)
	var v journal.Verdict
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &v) != nil || v.Action != journal.ActionRecorded ||
		v.By != "key:test-client" || !strings.Contains(v.Note, "shadow mode") {
		t.Fatalf("verdict: %d %s", rec.Code, rec.Body.String())
	}
	var n int
	e.st.Reader().QueryRow(`SELECT COUNT(*) FROM router_blocklist`).Scan(&n)
	if n != 0 {
		t.Error("a shadow verdict blocklisted")
	}
	if code := e.getKey(APIBase+"/journal/dl:1", key, &en); code != 200 || en.Verdict == nil || en.Verdict.ID != v.ID {
		t.Errorf("entry verdict: %+v", en.Verdict)
	}
}
