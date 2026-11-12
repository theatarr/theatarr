package library

import (
	"strings"
	"testing"
	"time"
)

// TestLegacyMismatchHoldBecomesWait: a hold an earlier build wrote because zurg did not confirm the files is planned
// again by the periodic pass and becomes a mismatch wait; every other hold is left alone as before.
func TestLegacyMismatchHoldBecomesWait(t *testing.T) {
	t.Parallel()
	e, _ := mismatchEnv(t)
	e.release("Other.Movie.2001", map[string]float64{"other.mkv": 7000})
	e.download(2, "movie-remux", 1, 10, "Other.Movie.2001", "Other.Movie.2001", "[]")
	e.exec(`UPDATE download SET info_hash = '1111111111111111111111111111111111111111' WHERE id = 2`)
	old := e.now.Add(-time.Hour).UnixMilli()
	e.exec(`INSERT INTO library_import (download_id, state, last_error, attempts, created_at, updated_at) VALUES
		(1, 'rejected', 'zurg does not confirm the files are this download''s (zurg: the file is 6 bytes, the mount shows 5)', 7, ?, ?),
		(2, 'rejected', 'naming collision: /x: the path holds link 7 of another title', 3, ?, ?)`, old, old, old, old)
	e.run()
	if s := e.importState(); !strings.HasPrefix(s, "waiting_mount 1 mount mismatch: zurg does not confirm") {
		t.Errorf("legacy mismatch hold: %s", s)
	}
	if s := e.one(`SELECT state || ' ' || attempts || ' ' || updated_at FROM library_import WHERE download_id = 2`); s !=
		"rejected 3 "+e.one(`SELECT ?`, old) {
		t.Errorf("kept hold touched: %s", s)
	}
	if s := e.one(`SELECT state FROM download WHERE id = 2`); s != "ready" {
		t.Errorf("kept hold's download %s", s)
	}
}
