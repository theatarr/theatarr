package indexer

import (
	"context"

	"github.com/theatarr/theatarr/internal/policy"
)

// Hooks for the external tests of this package (playing_guard_test.go), which also import the router.

// RecordFeedForTest records an RSS read's cache evidence, as a sync does (recordFeed).
func RecordFeedForTest(ctx context.Context, e *Evidence, rs []Release) { e.recordFeed(ctx, rs, true) }

// EvidencePolicyForTest is the evidence tests' policy.
func EvidencePolicyForTest() *policy.Policy { return evidencePolicy() }
