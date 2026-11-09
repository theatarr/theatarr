package torrents

import (
	"context"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/provider/adder"
)

// TorBox answers a delete of an id that is already gone with HTTP 500 DATABASE_ERROR
// (https://notes.debridmediamanager.com/zurg/internals/torrent-lifecycle/). After a 5xx the list is read again: gone
// is deleted; still listed stays a failure and stops the provider, as any error does.
func TestTorBoxDeleteServerErrorSettledByList(t *testing.T) {
	t.Parallel()
	for _, gone := range []bool{true, false} {
		e := deleteFixture(t)
		ctx := context.Background()
		first, err := e.m.PlanDelete(ctx, []DeleteSelection{{Provider: "tb", ID: "90", Hash: hF}})
		if err != nil {
			t.Fatal(err)
		}
		sel := []DeleteSelection{{Provider: "tb", ID: "90", Hash: hF}}
		if first.Count == 0 {
			sel[0].Override, sel[0].OverrideToken, sel[0].AllowRecent = true, first.Items[0].ExpectedOverride, true
		}
		plan, err := e.m.PlanDelete(ctx, sel)
		if err != nil || plan.Count != 1 {
			t.Fatalf("plan: %+v %v", plan.Items, err)
		}
		e.atb.deleteErr = &adder.Error{Provider: "tb", Code: adder.CodeUnavailable, Status: 500,
			Message: "DATABASE_ERROR"}
		if gone {
			e.atb.onDelete = func() { e.tb.list = nil }
		}
		done, err := e.m.Delete(ctx, sel, plan.Token, "admin")
		if err != nil {
			t.Fatal(err)
		}
		it := done.Items[0]
		if gone {
			if it.Outcome != "deleted" || done.Deleted != 1 || done.Failed != 0 || !strings.Contains(it.Note, "500") {
				t.Errorf("gone after a 500: %+v (deleted %d failed %d)", it, done.Deleted, done.Failed)
			}
			continue
		}
		if it.Outcome != OutFailed || done.Failed != 1 || done.Deleted != 0 {
			t.Errorf("still listed after a 500: %+v", it)
		}
	}
}

// A Real-Debrid 5xx is not settled from the list: only TorBox answers a gone id that way.
func TestNonTorBoxServerErrorStaysFailed(t *testing.T) {
	t.Parallel()
	e := deleteFixture(t)
	ctx := context.Background()
	sel := []DeleteSelection{{Provider: "rd", ID: "6", Hash: hF}}
	plan, _ := e.m.PlanDelete(ctx, sel)
	e.ard.deleteErr = &adder.Error{Provider: "rd", Code: adder.CodeUnavailable, Status: 503}
	e.ard.onDelete = func() { e.rd.list = nil }
	done, err := e.m.Delete(ctx, sel, plan.Token, "admin")
	if err != nil || done.Items[0].Outcome != OutFailed {
		t.Fatalf("rd 503: %+v %v", done.Items, err)
	}
}
