package gateway

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// bucket is one budget: a token bucket plus hourly and daily caps.
type bucket struct {
	key           string
	rate          float64 // tokens per second
	burst         float64
	hourly, daily int

	state *bucketState
}

type bucketState struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
}

func newBucket(key string, b *policy.Budget, now time.Time) *bucket {
	burst := b.Burst
	if burst <= 0 {
		burst = 1
	}
	return &bucket{
		key: key, rate: float64(b.Requests) / b.Per.D().Seconds(), burst: float64(burst),
		hourly: b.Hourly, daily: b.Daily, state: &bucketState{tokens: float64(burst), last: now},
	}
}

func newBucketWithState(key string, b *policy.Budget, now time.Time, previous *bucket) *bucket {
	next := newBucket(key, b, now)
	if previous != nil {
		next.state = previous.state
	}
	return next
}

func (b *bucket) windows(now time.Time) []windowKey {
	var ks []windowKey
	u := now.UTC()
	if b.hourly > 0 {
		ks = append(ks, windowKey{b.key, "hour", store.Millis(u.Truncate(time.Hour))})
	}
	if b.daily > 0 {
		day := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
		ks = append(ks, windowKey{b.key, "day", store.Millis(day)})
	}
	return ks
}

// capped returns a refusal if a cap is reached.
func (b *bucket) capped(s *state, now time.Time) (detail string, retry time.Time, hit bool) {
	for _, k := range b.windows(now) {
		limit, span := b.hourly, time.Hour
		if k.period == "day" {
			limit, span = b.daily, 24*time.Hour
		}
		if s.count(k) >= limit {
			return fmt.Sprintf("%s cap %d reached for budget %s", k.period, limit, b.key),
				store.FromMillis(k.start).Add(span), true
		}
	}
	return "", time.Time{}, false
}

// limits are the caps of b.windows(now), in the same order.
func (b *bucket) limits(keys []windowKey) []int {
	out := make([]int, len(keys))
	for i, k := range keys {
		out[i] = b.hourly
		if k.period == "day" {
			out[i] = b.daily
		}
	}
	return out
}

// give returns a token take handed out (the request was refused by a cap after all).
func (b *bucket) give() {
	b.state.mu.Lock()
	defer b.state.mu.Unlock()
	b.state.tokens = min(b.burst, b.state.tokens+1)
}

// take takes a token if one is there; otherwise it returns how long until one is.
func (b *bucket) take(now time.Time) (wait time.Duration) {
	b.state.mu.Lock()
	defer b.state.mu.Unlock()
	if el := now.Sub(b.state.last).Seconds(); el > 0 {
		b.state.tokens = min(b.burst, b.state.tokens+el*b.rate)
		b.state.last = now
	}
	if b.state.tokens >= 1 {
		b.state.tokens--
		return 0
	}
	return time.Duration((1 - b.state.tokens) / b.rate * float64(time.Second))
}

// level returns the tokens the bucket holds at now, without taking any.
func (b *bucket) level(now time.Time) float64 {
	b.state.mu.Lock()
	defer b.state.mu.Unlock()
	t := b.state.tokens
	if el := now.Sub(b.state.last).Seconds(); el > 0 {
		t = min(b.burst, t+el*b.rate)
	}
	return t
}

// acquire waits for budget, or refuses. It returns the time spent waiting. All its waits together stay within
// maxWait (one deadline from the start), and a cap is checked and counted in one step.
func (g *Gateway) acquire(ctx context.Context, ti *targetInfo) (time.Duration, error) {
	b := g.buckets[ti.budget]
	if b == nil {
		return 0, nil
	}
	start := g.now()
	subject := "budget:" + b.key
	for {
		now := g.now()
		var wait time.Duration
		var reason Reason
		var detail string
		if until, why, ok := g.backedOff(subject, now); ok {
			wait, reason, detail = until.Sub(now), ReasonBackoff, fmt.Sprintf("budget %s backing off: %s", b.key, why)
		} else if d, retry, hit := b.capped(g.state, now); hit {
			g.metrics.budgetRefused.WithLabelValues(b.key, "cap").Inc()
			return 0, &Refused{Reason: ReasonBudget, Target: ti.name, Detail: d, RetryAt: retry}
		} else if wait = b.take(now); wait == 0 {
			keys := b.windows(now)
			if i := g.state.reserve(context.WithoutCancel(ctx), keys, b.limits(keys), now); i >= 0 {
				// Another request took the window's last slot since the check above.
				b.give()
				d, retry, _ := b.capped(g.state, now)
				g.metrics.budgetRefused.WithLabelValues(b.key, "cap").Inc()
				return 0, &Refused{Reason: ReasonBudget, Target: ti.name, Detail: d, RetryAt: retry}
			}
			waited := now.Sub(start)
			g.metrics.budgetWait.WithLabelValues(b.key).Observe(waited.Seconds())
			return waited, nil
		} else {
			reason, detail = ReasonBudget, fmt.Sprintf("budget %s: next token in %v", b.key, wait.Round(time.Millisecond))
		}
		if now.Add(wait).After(start.Add(g.maxWait)) || deadlineBefore(ctx, now.Add(wait)) {
			g.metrics.budgetRefused.WithLabelValues(b.key, string(reason)).Inc()
			return 0, &Refused{Reason: reason, Target: ti.name, Detail: detail, RetryAt: now.Add(wait)}
		}
		if err := g.sleep(ctx, wait); err != nil {
			return 0, err
		}
	}
}

func deadlineBefore(ctx context.Context, t time.Time) bool {
	d, ok := ctx.Deadline()
	return ok && d.Before(t)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
