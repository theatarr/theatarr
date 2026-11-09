package gateway

import (
	"context"
	"sort"
	"time"
)

// State is a read-only snapshot of the gateway, from memory (the source of truth while running; the 00002 tables
// only mirror it).
type State struct {
	// At is when the snapshot was taken.
	At time.Time `json:"at"`
	// Egress is the egress summary, as /readyz shows it (see [Gateway.EgressSummary]).
	Egress map[string]any `json:"egress"`
	// Paths is the latest egress check per path, sorted by path.
	Paths []EgressState `json:"paths"`
	// Budgets are all policy budgets, sorted by key.
	Budgets []BudgetState `json:"budgets"`
	// Backoffs are every back-off row, active or not (a row with failures but no back-off yet, or an expired one),
	// sorted by subject.
	Backoffs []BackoffState `json:"backoffs"`
	// Requests are the indexer requests by purpose, per indexer key (IndexerRequests).
	Requests map[string]IndexerRequests `json:"indexer_requests"`
}

// BudgetState is a budget's configuration and current use.
type BudgetState struct {
	Key      string        `json:"key"`
	Requests int           `json:"requests"`
	Per      time.Duration `json:"per"`
	Burst    int           `json:"burst"`
	Hourly   int           `json:"hourly,omitempty"`
	Daily    int           `json:"daily,omitempty"`
	// Tokens is what the bucket holds now (up to Burst).
	Tokens float64 `json:"tokens"`
	// UsedHour and UsedDay count requests in the current UTC hour and day; only capped periods are counted.
	UsedHour   int  `json:"used_hour"`
	UsedDay    int  `json:"used_day"`
	HourCapped bool `json:"hour_capped"`
	DayCapped  bool `json:"day_capped"`
	// UsedBy lists the targets (class:key) on this budget, sorted.
	UsedBy []string `json:"used_by"`
	// Backoff is the budget's own back-off row (subject budget:<key>), if any.
	Backoff *BackoffState `json:"backoff,omitempty"`
}

// BackoffState is one back-off row.
type BackoffState struct {
	// Subject is budget:<key> or a target, class:key.
	Subject string `json:"subject"`
	// Until is zero when only failures are counted.
	Until     time.Time `json:"until"`
	Active    bool      `json:"active"`
	Failures  int       `json:"failures"`
	Reason    string    `json:"reason"`
	UpdatedAt time.Time `json:"updated_at"`
}

// State returns a snapshot of egress, budgets and back-offs.
func (g *Gateway) State() State {
	return g.current().stateSnapshot()
}

// StateFor returns the gateway snapshot for the generation pinned to ctx.
func (g *Gateway) StateFor(ctx context.Context) State {
	return g.forContext(ctx).stateSnapshot()
}

func (g *Gateway) stateSnapshot() State {
	now := g.now()
	backoffs, windows := g.state.snapshot()
	s := State{At: now, Egress: g.egressSummary(), Paths: g.egressStates(), Budgets: []BudgetState{},
		Backoffs: make([]BackoffState, 0, len(backoffs)), Requests: g.state.indexerRequests(now)}
	for subj, b := range backoffs {
		s.Backoffs = append(s.Backoffs, BackoffState{Subject: subj, Until: b.until, Active: b.until.After(now),
			Failures: b.failures, Reason: b.reason, UpdatedAt: b.updated})
	}
	sort.Slice(s.Backoffs, func(i, j int) bool { return s.Backoffs[i].Subject < s.Backoffs[j].Subject })
	keys := make([]string, 0, len(g.buckets))
	for k := range g.buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b := g.buckets[k]
		pb := g.pol.Budgets[k]
		if pb == nil {
			continue // a probe's own budget (Probe)
		}
		bs := BudgetState{Key: k, Requests: pb.Requests, Per: pb.Per.D(), Burst: int(b.burst), Hourly: b.hourly,
			Daily: b.daily, Tokens: b.level(now), UsedBy: append([]string{}, g.users[k]...)}
		for _, w := range b.windows(now) {
			if w.period == "hour" {
				bs.UsedHour = windows[w]
				bs.HourCapped = bs.UsedHour >= b.hourly
			} else {
				bs.UsedDay = windows[w]
				bs.DayCapped = bs.UsedDay >= b.daily
			}
		}
		for i := range s.Backoffs {
			if s.Backoffs[i].Subject == "budget:"+k {
				bo := s.Backoffs[i]
				bs.Backoff = &bo
			}
		}
		s.Budgets = append(s.Budgets, bs)
	}
	return s
}
