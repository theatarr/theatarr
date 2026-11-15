package journal

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

// The journal follows the current settings generation: an entry is shown under the current policy, not the startup
// one.
func TestJournalFollowsCurrentPolicy(t *testing.T) {
	e := newEnv(t)
	e.seedRoute()
	ctx := context.Background()
	later, err := e.pol.Clone()
	if err != nil {
		t.Fatal(err)
	}
	j := New(Config{Store: e.st, Policy: e.pol, Now: func() time.Time { return testNow },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), PolicyFor: func(context.Context) *policy.Policy { return later }})
	entry, err := j.Get(ctx, "dl:1")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Version != "movie-remux" {
		t.Fatalf("journaled under the startup policy: version %s", entry.Version)
	}
}
