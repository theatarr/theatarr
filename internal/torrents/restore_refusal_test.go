package torrents

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
)

// An unrecognised refusal during restore backs the provider off without poisoning the hash.
func TestRestoreUnknownRefusalBacksProviderOff(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	old := e.now.Add(-72 * time.Hour)
	e.rd.list = []provider.Torrent{tor("4", hD, "D", 4, old, provider.StateDownloaded),
		tor("5", hE, "E", 5, old, provider.StateDownloaded)}
	b, _, err := e.m.TakeBackup(ctx, BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	e.rd.list = nil
	e.ard.addErr = map[string]error{hD: &adder.Error{Provider: "rd", Code: adder.CodeUnknown, Status: 400,
		Refused: true, Err: errors.New("infringing_file_x")}}
	req := RestoreRequest{BackupID: b.ID}
	plan, err := e.m.PlanRestore(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.CheckRestore(ctx, req, plan.Token); err != nil {
		t.Fatal(err)
	}
	// E alone first: an accepted add after D's refusal would reset the pacing and hide a back-off.
	if _, err := e.m.Restore(ctx, req, []string{"rd|" + hE}, "admin"); err != nil {
		t.Fatal(err)
	}
	got, err := e.m.Restore(ctx, req, []string{"rd|" + hD}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, it := range got.Items {
		out[it.Name] = it.Outcome
	}
	if out["D"] != OutFailed {
		t.Fatalf("outcomes %v", out)
	}
	var fails int
	var until int64
	if err := e.m.Store.Reader().QueryRow(`SELECT failures, backoff_until FROM router_pacing
		WHERE provider = 'rd'`).Scan(&fails, &until); err != nil {
		t.Fatal(err)
	}
	if fails == 0 || until <= e.now.UnixMilli() {
		t.Errorf("an unknown refusal did not back rd off: fails %d until %d", fails, until)
	}
	var n int
	if err := e.m.Store.Reader().QueryRow(`SELECT COUNT(*) FROM provider_cache WHERE provider = 'rd'
		AND info_hash = ? AND state = 'miss' AND expires_at > ?`, hD, e.now.UnixMilli()).Scan(&n); err != nil || n != 0 {
		t.Errorf("the unrecognised refusal poisoned a hash on rd: %d %v", n, err)
	}
}
