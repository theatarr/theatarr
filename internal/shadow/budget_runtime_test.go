package shadow

import (
	"context"
	"strings"
	"testing"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
)

type capturedBudgets struct{}

func (capturedBudgets) State() gateway.State { return gateway.State{} }
func (capturedBudgets) StateFor(ctx context.Context) gateway.State {
	if settings.ContextGeneration(ctx) == 1 {
		return gateway.State{Budgets: []gateway.BudgetState{{Key: "shared", HourCapped: true}}}
	}
	return gateway.State{}
}

func TestEligibilityUsesCapturedBudgetState(t *testing.T) {
	t.Parallel()
	r := &Runner{pol: &policy.Policy{Indexers: map[string]*policy.Indexer{"feed": {Budget: "shared"}}}, budgets: capturedBudgets{}}
	ctx := settings.WithRuntime(context.Background(), &settings.Runtime{Generation: 1})
	if why := r.budgetBlocked(ctx, "feed"); !strings.Contains(why, "spent") {
		t.Fatalf("captured budget ignored: %q", why)
	}
	if why := r.budgetBlocked(context.Background(), "feed"); why != "" {
		t.Fatalf("current budget blocked: %q", why)
	}
}
