package settings

import (
	"context"
	"testing"
)

// Measure a complete example-policy candidate, rather than extrapolating from a hand-built JSON blob.
func TestQualityReviewCandidateStorageGrowth(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _, _ := syncService(t)
	editUpstream(t, svc.TrashRoot, syncFormatID, func(f map[string]any) { f["includeCustomFormatWhenRenaming"] = true })
	p, err := svc.Preview(ctx, QualitySyncSource{Record: Record{Key: "fixture"}, Config: QualitySyncConfig{Kind: "trash", Flavour: "radarr"}})
	if err != nil {
		t.Fatal(err)
	}
	p.draft = false // Each weekly review replaces the saved source's prior review.
	for range 52 {
		if err := svc.storePreview(ctx, p, true, false); err != nil {
			t.Fatal(err)
		}
	}
	var rows, pending, bytes, previewBytes int64
	if err := svc.Store.db.Reader().QueryRowContext(ctx, `SELECT COUNT(*), SUM(state='pending'), SUM(length(CAST(candidate AS BLOB))), SUM(length(CAST(preview AS BLOB))) FROM settings_quality_review`).Scan(&rows, &pending, &bytes, &previewBytes); err != nil {
		t.Fatal(err)
	}
	if rows != 52 || pending != 1 {
		t.Fatalf("rows=%d pending=%d", rows, pending)
	}
	t.Logf("profiles=%d formats=%d sets=%d naming=%d; candidate=%d bytes, preview=%d bytes; 52 weekly reviews=%d bytes; daily annual candidates=%d bytes", len(p.candidate.Profiles), len(p.candidate.Formats), len(p.candidate.FormatSets), len(p.candidate.Naming), bytes/rows, previewBytes/rows, bytes+previewBytes, bytes/rows*365)
}
