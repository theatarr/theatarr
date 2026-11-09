package provider

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/store"
)

// Per-provider back-off. A sync job lists every provider; one provider failing (AllDebrid's AUTH_BLOCKED) must not
// fail the job and make its retries list the healthy ones again. So each provider has its own retry state, read
// from its provider_sync_run rows: the failures since its last complete listing, and when the last one ended. Its
// next attempt is RetryBase × 2^(failures-1) after that, capped at RetryMax. A scheduled sync skips a provider until
// then, and a failed provider gets its own retry job (payload Retry, one provider) at that time.

const (
	RetryBase = 2 * time.Minute
	RetryMax  = time.Hour
)

// Backoff is the wait after the given number of consecutive failures (0 for none).
func Backoff(failures int) time.Duration {
	if failures <= 0 {
		return 0
	}
	d := RetryBase
	for i := 1; i < failures && d < RetryMax; i++ {
		d *= 2
	}
	return min(d, RetryMax)
}

// RetryState is a provider's back-off.
type RetryState struct {
	// Failures counts the failed runs since the last complete one.
	Failures int
	// LastFailure is when the last of them ended; zero when Failures is 0.
	LastFailure time.Time
	// RetryAt is when the provider is due again: LastFailure + Backoff(Failures). Zero when it is not backing off.
	RetryAt time.Time
}

// ReadRetryState reads a provider's back-off from its sync runs.
func ReadRetryState(ctx context.Context, db *sql.DB, key string) (RetryState, error) {
	var rs RetryState
	var last int64
	err := db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(finished_at), 0) FROM provider_sync_run
		WHERE provider = ? AND ok = 0 AND id > COALESCE((SELECT MAX(id) FROM provider_sync_run
			WHERE provider = ? AND ok = 1), 0)`, key, key).Scan(&rs.Failures, &last)
	if err != nil || rs.Failures == 0 {
		return RetryState{}, err
	}
	rs.LastFailure = store.FromMillis(last)
	rs.RetryAt = rs.LastFailure.Add(Backoff(rs.Failures))
	return rs, nil
}

// retryKey is the dedupe key of a provider's retry job. It holds the failure count, so a retry job that fails again
// can enqueue the next one while it still runs.
func retryKey(key string, failures int) string {
	return fmt.Sprintf("%s/%s/%d", JobKind, key, failures)
}

func (s *Syncer) queue() *jobs.Queue {
	if s.Queue != nil {
		return s.Queue
	}
	return jobs.NewQueue(s.Store)
}

// scheduleRetry enqueues a provider's own retry at its next attempt.
func (s *Syncer) scheduleRetry(ctx context.Context, key string) error {
	rs, err := ReadRetryState(ctx, s.Store.Reader(), key)
	if err != nil || rs.Failures == 0 {
		return err
	}
	id, created, err := s.queue().Enqueue(ctx, JobKind, SyncPayload{Providers: []string{key}, Retry: true},
		jobs.EnqueueOptions{RunAt: rs.RetryAt, DedupeKey: retryKey(key, rs.Failures), MaxAttempts: 1})
	if err != nil {
		return fmt.Errorf("provider %q: scheduling its retry: %w", key, err)
	}
	if created {
		s.log().Info("provider sync failed; retrying it on its own", "provider", key, "failures", rs.Failures,
			"retry_at", rs.RetryAt.UTC(), "job", id)
	}
	return nil
}
