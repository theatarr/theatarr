package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

// A service built at startup keeps its first gateway handle; a budget edited through settings still applies to it
// without a restart, because every handle sends through the active generation's buckets.
func TestStartupGatewayHandleFollowsReloadedBudget(t *testing.T) {
	e := newEnv(t)
	e.serve("dmm.test", okHandler("ok"))
	first := testPolicy()
	first.Budgets["dmm-key"] = &policy.Budget{Requests: 100, Per: policy.Duration(time.Second), Hourly: 10}
	first.Indexers["dmm"].Budget = "dmm-key"
	startup := e.gateway(first, nil)
	startup.CheckEgress(context.Background())
	edited := testPolicy()
	edited.Budgets["dmm-key"] = &policy.Budget{Requests: 100, Per: policy.Duration(time.Second), Hourly: 1}
	edited.Indexers["dmm"].Budget = "dmm-key"
	next, err := New(context.Background(), edited, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test", Previous: startup, Generation: 2, DialContext: e.dial, MaxWait: time.Minute, now: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	next.Activate()
	if _, err := get(startup, dmm, "http://dmm.test/a"); err != nil {
		t.Fatal(err)
	}
	_, err = get(startup, dmm, "http://dmm.test/b")
	var refused *Refused
	if !errors.As(err, &refused) || refused.Reason != ReasonBudget {
		t.Fatalf("second request through the startup handle under the edited hourly cap of 1: %v", err)
	}
}
