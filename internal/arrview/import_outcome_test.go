package arrview

import (
	"context"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/library"
)

// "Import now" says what the import did. A rejected, held, partial or dry-run import is not reported
// as imported.
func TestImportNowOutcome(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	ctx := context.Background()
	now := ms(testNow)
	for i, id := range []int{60, 61, 62, 63, 64} {
		e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
			score, episodes, state, created_at, updated_at) VALUES (?, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.WEB-DL',
			'prowlarr-public', 'WEBDL-2160p', 50, '[]', 'ready', ?, ?)`, id, strings.Repeat(string(rune('a'+i)), 40), now, now)
	}
	// What the library did, as its import leaves it: the library_import row and, for a rejection, the failed download.
	e.lib.plan = func(id int64) *library.Plan {
		p := &library.Plan{DownloadID: id}
		switch id {
		case 60:
			e.exec(`INSERT INTO library_import (download_id, state, last_error, created_at, updated_at)
				VALUES (60, 'rejected', 'no file passed the sample guard', ?, ?)`, now, now)
			e.exec(`UPDATE download SET state = 'failed' WHERE id = 60`)
			p.Rejected = []library.PlannedFile{{Rel: "sample.mkv", Reason: "sample"}}
		case 61:
			e.exec(`INSERT INTO library_import (download_id, state, last_error, created_at, updated_at)
				VALUES (61, 'rejected', 'naming collision: x', ?, ?)`, now, now)
		case 62:
			p.Links = []library.PlannedLink{{}}
			p.Rejected = []library.PlannedFile{{Rel: "b.mkv", Reason: "sample"}}
		case 63:
			p.Links = []library.PlannedLink{{}}
		case 64:
			p.DryRun, p.Refused = true, "library.enabled is false"
		}
		return p
	}
	want := map[int64]string{60: "rejected", 61: "held", 62: "partial", 63: "imported", 64: "dry_run"}
	for id, outcome := range want {
		r, err := e.svc.RetryDownload(ctx, id, "ui:retry")
		if err != nil {
			t.Fatalf("%d: %v", id, err)
		}
		if r.Outcome != outcome {
			t.Errorf("download %d: outcome %q (%s), want %s", id, r.Outcome, r.What, outcome)
		}
		if outcome != "imported" && r.What == "imported" {
			t.Errorf("download %d reported as imported", id)
		}
	}
}
