package arrview

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/router"
)

// A retry by hand routes the release as the download stored it (release_data): cache evidence and extras the
// provider order uses survive, as on a route the router resumes itself (requestOf). It stays a new, manual route.
func TestRetryKeepsStoredReleaseData(t *testing.T) {
	t.Parallel()
	e := newDevEnv(t)
	ctx := context.Background()
	now := ms(testNow)
	hash := strings.Repeat("7", 40)
	e.exec(`INSERT INTO download (id, title_id, version_id, version_key, info_hash, release_title, indexer, quality,
		score, episodes, state, error, created_at, updated_at, release_data) VALUES
		(50, 1, 2, 'movie-web', ?, 'Alpha.2020.2160p.WEB-DL-RETRY', 'prowlarr-public', 'WEBDL-2160p', 50, '[]', 'failed',
		 'stalled', ?, ?, ?)`, hash, now, now,
		`{"title":"Alpha.2020.2160p.WEB-DL-RETRY","seeders":12,"cached_on":["ad"],"cached_via":{"ad":"dmm"},`+
			`"extras":{"cached_on":"ad"}}`)
	r, err := e.svc.RetryDownload(ctx, 50, "ui:retry:50")
	if err != nil || r.Job == 0 {
		t.Fatalf("retry: %+v %v", r, err)
	}
	raw := e.str(`SELECT payload FROM job WHERE id = ?`, r.Job)
	var req router.Request
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Candidates) != 1 || req.DownloadID != 0 || req.Source != "ui:retry:50" {
		t.Fatalf("request %+v", req)
	}
	rel := req.Candidates[0].Release
	if !slices.Equal(rel.CachedOn, []string{"ad"}) || rel.CachedVia["ad"] != "dmm" || rel.Extras["cached_on"] != "ad" ||
		rel.Seeders != 12 {
		t.Errorf("the retried release lost its stored data: %+v", rel)
	}
	if rel.InfoHash != hash || rel.Indexer != "prowlarr-public" || rel.Privacy.IsPrivate() {
		t.Errorf("release %+v", rel)
	}
}
