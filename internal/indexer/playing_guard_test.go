package indexer_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// Regression guard (FACTS 5): the miss of a release that broke while playing (router:playing, on the account it
// broke on) survives the router's later all-provider miss and then a DMM listing that names the release cached there:
// the all-provider miss does not replace it, so the listing, which reopens only that miss, finds nothing to reopen.
func TestPlayingMissSurvivesMissAndFeed(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	p := indexer.EvidencePolicyForTest()
	p.Indexers["dmm-rd"] = &policy.Indexer{Kind: "dmm", URL: "http://dmm.test/api/torznab/rd/cached",
		APIKeyEnv: "THEATARR_DMM_KEY", Privacy: policy.PrivacyPublic, Budget: "dmm-key"}
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	hash := strings.Repeat("a", 40)
	tx, err := st.Writer().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := router.RecordPlayingMiss(ctx, tx, hash, "rd", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// The router's all-provider miss of the same release (its recordMiss write).
	if err := router.RecordCacheEvidence(ctx, st.Writer(), hash, "rd", "miss", "router:miss", now.Add(time.Hour),
		now.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	e := indexer.NewEvidence(indexer.EvidenceConfig{Store: st, Policy: p, Now: func() time.Time { return now }})
	indexer.RecordFeedForTest(ctx, e, []indexer.Release{{Indexer: "dmm-rd", Kind: "dmm", InfoHash: hash,
		PublishDate: now.Add(-time.Minute), Extras: map[string]string{"cached_on": "rd"}}})
	var state, source string
	if err := st.Reader().QueryRow(`SELECT state, source FROM provider_cache WHERE info_hash = ? AND provider = 'rd'`,
		hash).Scan(&state, &source); err != nil {
		t.Fatal(err)
	}
	if state != "miss" || source != router.PlayingMissSource {
		t.Errorf("rd row %s %s, want the playing miss", state, source)
	}
}
