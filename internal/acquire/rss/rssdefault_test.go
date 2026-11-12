package rss

import (
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

// The RSS interval has one default, the arrs' 15 minutes, in the policy and in the runner.
func TestRSSDefaultIsThePolicyDefault(t *testing.T) {
	t.Parallel()
	pol := &policy.Policy{}
	r := New(Config{Policy: pol})
	if want := pol.ShadowSettings().RSSInterval; r.set.RSSInterval != want || want != 15*time.Minute {
		t.Fatalf("runner RSS interval %s, policy default %s; want both 15m", r.set.RSSInterval, want)
	}
}
