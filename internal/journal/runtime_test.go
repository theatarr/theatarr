package journal

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

// The journal follows the current settings generation. A version activated after startup is shown and judged
// as active, not as the startup policy's shadow version.
func TestJournalFollowsCurrentPolicy(t *testing.T) {
	e := newEnv(t)
	e.seedRoute()
	ctx := context.Background()
	later, err := e.pol.Clone()
	if err != nil {
		t.Fatal(err)
	}
	later.Versions["movie-remux"].Mode = policy.ModeActive
	j := New(Config{Store: e.st, Policy: e.pol, Queue: e.q, Now: func() time.Time { return testNow },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), PolicyFor: func(context.Context) *policy.Policy { return later }})
	entry, err := j.Get(ctx, "dl:1")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Version != "movie-remux" || entry.Mode != "active" {
		t.Fatalf("journaled under the startup policy: version %s mode %s", entry.Version, entry.Mode)
	}
	v, err := j.Judge(ctx, "dl:1", VerdictRequest{Verdict: "wrong", Reason: "bad"}, "admin")
	if err != nil || !strings.Contains(v.Note, "router.enabled is false") {
		t.Fatalf("verdict judged under the startup policy: %+v %v", v, err)
	}
}
