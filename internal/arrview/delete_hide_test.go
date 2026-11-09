package arrview

import (
	"strconv"
	"testing"
)

// TestDeleteUnlinksOnlyAfterTheHide: DELETE ?deleteFiles=true moved the view's links to trash before
// the transaction that hides the versions and ends their downloads, so a failed hide left them unlinked with the
// version still wanted. Links are only checked before the hide now, and moved after it commits.
func TestDeleteUnlinksOnlyAfterTheHide(t *testing.T) {
	t.Parallel()
	e := removeEnv(t)
	var hiddenAt []int64
	e.lib.onUnlink = func(id int64) {
		hiddenAt = append(hiddenAt, e.one(`SELECT COUNT(*) FROM arrview_removed_version WHERE version_id = 2`))
		e.exec(`UPDATE link SET state = 'unlinked' WHERE id = ?`, id)
	}
	alpha := strconv.Itoa(IDBase + 1)
	if rec := e.do("DELETE", "/v/movie-web/radarr/api/v3/movie/"+alpha+"?deleteFiles=true", e.key, nil); rec.Code != 200 {
		t.Fatalf("DELETE: %d %s", rec.Code, rec.Body.String())
	}
	if len(hiddenAt) == 0 {
		t.Fatal("nothing unlinked")
	}
	for i, n := range hiddenAt {
		if n != 1 {
			t.Fatalf("unlink %d ran before the version was hidden (unlinked %v)", i, e.lib.unlinks)
		}
	}
	if n := e.one(`SELECT COUNT(*) FROM link l JOIN link_version lv ON lv.link_id = l.id WHERE lv.version_id = 2
		AND l.state = 'active'`); n != 0 {
		t.Fatalf("%d active links left", n)
	}
}
