package web

import (
	"context"
	"database/sql"
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
	// The failure is on the grab's line; by its own kind it is listed alone.
	if code := e.getKey(APIBase+"/journal?kind=grab,failure&title=alp&version=movie-web", key, &page); code != 200 ||
		len(page.Items) != 1 || page.Items[0].ID != "dl:1" ||
		page.Items[0].Summary != "Grabbed Alpha.2020.2160p.WEB-DL-GRP · failed: miss on every provider" {
		t.Fatalf("list: %d %+v", code, page.Items)
	}
	if code := e.getKey(APIBase+"/journal?kind=failure", key, &page); code != 200 || len(page.Items) != 1 ||
		page.Items[0].ID != "dl:1:fail" {
		t.Fatalf("failures: %d %+v", code, page.Items)
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

	// Three failures in 30 days flag the latest; the flagged view and the badge's count read the stored flags.
	for id := 2; id <= 3; id++ {
		e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
			score, state, error, source, created_at, updated_at) VALUES (?, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.WEB-DL-GRP',
			'dmm', 'WEBDL-2160p', 1500, 'failed', 'miss on every provider', 'acquire:1', ?, ?)`, id, hashA[:39]+string(rune('0'+id)),
			now-3000+int64(id), now-2000+int64(id))
	}
	if code := e.getKey(APIBase+"/journal?suspect=1", key, &page); code != 200 || len(page.Items) != 1 ||
		page.Items[0].ID != "dl:3:fail" || !page.Items[0].Suspect {
		t.Fatalf("flagged: %d %+v", code, page.Items)
	}
	var count struct{ Count int }
	if code := e.getKey(APIBase+"/journal/flagged", key, &count); code != 200 || count.Count != 1 {
		t.Errorf("flagged count: %d %+v", code, count)
	}
	// Verdicts are gone.
	r := httptest.NewRequest("POST", APIBase+"/journal/dl:1/verdict", strings.NewReader(`{"verdict":"ok","reason":"x"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Api-Key", key)
	if rec := e.do(r); rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("verdict: %d", rec.Code)
	}
}

// TestJournalUnblockWords: an unblock's line in the journal says why the release was blocked in the Blocklist's words,
// for rows stored before this wording too (the raw reason stays in the database).
func TestJournalUnblockWords(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	key := e.apiKey()
	const cf = "Not a Custom Format upgrade for existing file(s). New: [A] (1202) do not improve on Existing: [B] (6200)"
	err := e.st.WriteTx(context.Background(), func(tx *sql.Tx) error {
		_, err := journal.RecordAction(context.Background(), tx, journal.Action{Action: journal.ActUnblock, TitleID: 1,
			Version: "movie-web", InfoHash: hashA, Release: "Alpha.Release", Target: hashA,
			Detail: "was blocked by library: no file passed the sample guard (" + cf + ")", By: "admin", At: testNow})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	const want = "Unblocked Alpha.Release · by admin · was blocked: Failed the library check: Not imported: these files do not improve on the files you have. This release scores 1,202, yours 6,200. Nothing was replaced."
	var page journal.Page
	if code := e.getKey(APIBase+"/journal?kind=action", key, &page); code != 200 || len(page.Items) != 1 ||
		page.Items[0].Summary != want {
		t.Fatalf("list: %d %+v", code, page.Items)
	}
	var en journal.Entry
	if code := e.getKey(APIBase+"/journal/"+page.Items[0].ID, key, &en); code != 200 || en.Summary != want {
		t.Errorf("entry: %d %q", code, en.Summary)
	}
}
