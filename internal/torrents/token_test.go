package torrents

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/settings"
)

// confirmers are the callers a preview's token is tried with: the one that previewed comes first.
func confirmers() (preview context.Context, others map[string]context.Context) {
	bg := context.Background()
	return settings.WithKeyConfirmer(bg, 1, "api key agent"), map[string]context.Context{
		"another key": settings.WithKeyConfirmer(bg, 2, "api key other"),
		"a session":   settings.WithConfirmer(bg, "user:1"),
		"the CLI":     bg,
	}
}

// A restore confirmation is the preview's, bound to the caller: another key, a session or a plain digest of the plan
// is refused; the same caller's token goes through.
func TestRestoreTokenBoundToCaller(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	old := e.now.Add(-72 * time.Hour)
	e.rd.list = []provider.Torrent{tor("1", hA, "A", 1, old, provider.StateDownloaded),
		tor("2", hB, "B", 2, old, provider.StateDownloaded)}
	b, _, err := e.m.TakeBackup(context.Background(), BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	e.rd.list = e.rd.list[:1]
	req := RestoreRequest{BackupID: b.ID}
	ctx, others := confirmers()
	plan, err := e.m.PlanRestore(ctx, req)
	if err != nil || plan.Readd != 1 {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	digest := restoreToken(plan)
	if plan.Token == digest || !strings.HasPrefix(plan.Token, "r2-") {
		t.Fatalf("a key's token is not the plan digest: %q", plan.Token)
	}
	for name, other := range others {
		if _, err := e.m.CheckRestore(other, req, plan.Token); !errors.Is(err, ErrConfirmation) {
			t.Errorf("%s with the key's token: %v", name, err)
		}
	}
	if _, err := e.m.CheckRestore(ctx, req, digest); !errors.Is(err, ErrConfirmation) {
		t.Errorf("the plain digest: %v", err)
	}
	if _, err := e.m.CheckRestore(ctx, req, plan.Token); err != nil {
		t.Errorf("the same key: %v", err)
	}
	// The CLI previews and confirms in two processes: it gets the plan digest.
	cli, err := e.m.PlanRestore(context.Background(), req)
	if err != nil || cli.Token != digest {
		t.Fatalf("CLI token %q, want %q (%v)", cli.Token, digest, err)
	}
	// A caller that may not confirm gets no digest either.
	if no, err := e.m.PlanRestore(settings.WithoutConfirmation(context.Background()), req); err != nil ||
		no.Token == digest {
		t.Fatalf("a non-confirming caller's token %q (%v)", no.Token, err)
	}
}

// A delete confirmation is the preview's, bound to the caller, as with restore. Nothing is deleted on a refusal.
func TestDeleteTokenBoundToCaller(t *testing.T) {
	t.Parallel()
	e := deleteFixture(t)
	sel := []DeleteSelection{{Provider: "rd", ID: "6", Hash: hF}}
	ctx, others := confirmers()
	plan, err := e.m.PlanDelete(ctx, sel)
	if err != nil || plan.Count != 1 {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	digest, plain := deleteToken(plan)
	if plan.Digest != digest || plan.Token == plain || !strings.HasPrefix(plan.Token, "d2-") {
		t.Fatalf("a key's token is not the plan digest: %q", plan.Token)
	}
	for name, other := range others {
		if _, err := e.m.Delete(other, sel, plan.Token, "admin"); !errors.Is(err, ErrConfirmation) {
			t.Errorf("%s with the key's token: %v", name, err)
		}
	}
	if _, err := e.m.Delete(ctx, sel, plain, "admin"); !errors.Is(err, ErrConfirmation) {
		t.Errorf("the plain digest: %v", err)
	}
	if e.ard.deleted != 0 {
		t.Fatalf("a refused confirmation deleted %d", e.ard.deleted)
	}
	if done, err := e.m.Delete(ctx, sel, plan.Token, "admin"); err != nil || done.Deleted != 1 {
		t.Fatalf("the same key: %+v %v", done, err)
	}
}
