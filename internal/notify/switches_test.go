package notify

import (
	"context"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
)

func TestRuntimeSwitchOffStopsNotifications(t *testing.T) {
	t.Parallel()
	e := newNEnv(t, true)
	ctx := context.Background()
	live, err := e.pol.Clone()
	if err != nil {
		t.Fatal(err)
	}
	e.n.polFor = func(context.Context) *policy.Policy { return live }
	// A delivery queued while enabled must also be dropped when the switch turns off.
	if err := e.n.record(ctx, []delivery{{target: "policy:automation", event: "Grab", body: map[string]any{"eventType": "Grab"}}}); err != nil {
		t.Fatal(err)
	}
	live.Notify.Enabled = false
	if _, err := e.n.SendDue(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if len(e.doer.reqs) != 0 {
		t.Fatal("pending delivery sent after switch off")
	}
	if err := e.n.TestNotification(ctx, "radarr", nil); err == nil {
		t.Fatal("test sent while disabled")
	}
	if err := e.n.record(ctx, []delivery{{target: "policy:automation", event: "Test", body: map[string]any{}}}); err != nil {
		t.Fatal(err)
	}
	if e.one("SELECT count(*) FROM notify_delivery WHERE state='dropped'") != 2 {
		t.Fatal("disabled deliveries not dropped")
	}
}

// The notify section follows a settings save without a restart: targets added or removed, and the retry limit, are
// read from the generation in force (Config.PolicyFor).
func TestNotifySectionFollowsSave(t *testing.T) {
	t.Parallel()
	e := newNEnv(t, true)
	ctx := context.Background()
	live, err := e.pol.Clone()
	if err != nil {
		t.Fatal(err)
	}
	e.n.polFor = func(context.Context) *policy.Policy { return live }
	if err := e.n.Test(ctx, "added"); err == nil {
		t.Fatal("a target not saved yet was tested")
	}
	live.Notify.Targets["added"] = &policy.NotifyTarget{URL: "http://192.168.1.52/hook", Native: true}
	if err := e.n.Test(ctx, "added"); err != nil {
		t.Fatalf("saved target: %v", err)
	}
	delete(live.Notify.Targets, "grabs-only")
	if err := e.n.Test(ctx, "grabs-only"); err == nil {
		t.Fatal("a removed target was tested")
	}
	if _, err := e.n.sendPolicy(ctx, "grabs-only", "theatarr.test", []byte("{}")); err != errGone {
		t.Fatalf("a removed target was sent to: %v", err)
	}
	retries := 9
	live.Notify.Retries = &retries
	if got := e.n.settings(ctx).Retries; got != 9 {
		t.Fatalf("retries %d", got)
	}
}
