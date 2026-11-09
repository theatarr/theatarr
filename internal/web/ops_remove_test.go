package web

import (
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/store"
)

// TestRemoveEndsDownloadsWithTheHide: the web remove hid the copy, then ended its downloads in a
// second transaction; a failure there answered 500 with the copy removed (a retry found nothing to remove) and its
// download free to import. Both happen in one transaction now.
func TestRemoveEndsDownloadsWithTheHide(t *testing.T) {
	t.Parallel()
	o := newOpsEnv(t)
	now := store.Millis(testNow)
	o.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, state,
		created_at, updated_at) VALUES (41, 2, 3, 'movie-web', ?, 'Beta.WEB', 'dmm', 'importing', ?, ?)`,
		strings.Repeat("1", 40), now, now)
	o.exec(`CREATE TRIGGER zz_no_end BEFORE UPDATE OF state ON download WHEN NEW.state = 'failed'
		BEGIN SELECT RAISE(ABORT, 'end refused'); END`)
	if code := o.jsonCall("POST", APIBase+"/titles/2/remove", RemoveRequest{}, o.mc, o.mcsrf, nil); code != 500 {
		t.Fatalf("remove: %d", code)
	}
	if n := o.count(`SELECT COUNT(*) FROM arrview_removed_version WHERE version_id = 3`); n != 0 {
		t.Fatal("the copy was removed although its downloads were not ended")
	}
	o.exec(`DROP TRIGGER zz_no_end`)
	var got RemoveResult
	if code := o.jsonCall("POST", APIBase+"/titles/2/remove", RemoveRequest{}, o.mc, o.mcsrf, &got); code != 200 ||
		got.DownloadsEnded != 1 {
		t.Fatalf("retry: %d %+v", code, got)
	}
}
