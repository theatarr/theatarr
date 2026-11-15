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

// The journal follows the current settings generation: an entry is shown and judged under the current policy, not
// the startup one.
func TestJournalFollowsCurrentPolicy(t *testing.T) {
	e := newEnv(t)
	e.seedRoute()
	ctx := context.Background()
	later, err := e.pol.Clone()
	if err != nil {
		t.Fatal(err)
	}
	// The startup policy would act on a verdict; the current one has the router off.
	e.pol.Router = &policy.Router{Enabled: true}
	later.Router = &policy.Router{Enabled: false}
	j := New(Config{Store: e.st, Policy: e.pol, Queue: e.q, Now: func() time.Time { return testNow },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), PolicyFor: func(context.Context) *policy.Policy { return later }})
	entry, err := j.Get(ctx, "dl:1")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Version != "movie-remux" {
		t.Fatalf("journaled under the startup policy: version %s", entry.Version)
	}
	v, err := j.Judge(ctx, "dl:1", VerdictRequest{Verdict: "wrong", Reason: "bad"}, "admin")
	if err != nil || !strings.Contains(v.Note, "router.enabled is false") {
		t.Fatalf("verdict judged under the startup policy: %+v %v", v, err)
	}
}
