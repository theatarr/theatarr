package provider

import (
	"context"
	"database/sql"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// ProviderStatus is what the registry knows about one provider.
type ProviderStatus struct {
	Provider string `json:"provider"`
	// LastRun is the newest sync run, LastOK the newest complete one (nil if none).
	LastRun *RunInfo `json:"last_run,omitempty"`
	LastOK  *RunInfo `json:"last_ok,omitempty"`
	Present int      `json:"present"` // content_provider rows present
	Absent  int      `json:"absent"`  // rows whose torrent left the list
	// Failures counts the failed runs since the last complete one; RetryAt is when the provider is tried again
	// (its back-off, retry.go). Both empty when it is not failing.
	Failures int        `json:"failures,omitempty"`
	RetryAt  *time.Time `json:"retry_at,omitempty"`
}

// RunInfo is one provider_sync_run row, without the summary.
type RunInfo struct {
	ID       int64     `json:"id"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	OK       bool      `json:"ok"`
	Error    string    `json:"error,omitempty"`
}

// Status is the registry view of the providers.
type Status struct {
	Providers []ProviderStatus `json:"providers"`
	Contents  map[string]int   `json:"contents"`
	// Unlisted names configured providers with no complete listing yet: until there are none, no content can be
	// absent.
	Unlisted []string `json:"unlisted,omitempty"`
}

// ReadStatus reads the registry state for the configured providers (policy keys, in fallback order).
func ReadStatus(ctx context.Context, db *sql.DB, configured []string) (*Status, error) {
	st := &Status{}
	for _, k := range configured {
		ps := ProviderStatus{Provider: k}
		var err error
		if ps.LastRun, err = lastRun(ctx, db, k, false); err != nil {
			return nil, err
		}
		if ps.LastOK, err = lastRun(ctx, db, k, true); err != nil {
			return nil, err
		}
		if ps.LastOK == nil {
			st.Unlisted = append(st.Unlisted, k)
		}
		rs, err := ReadRetryState(ctx, db, k)
		if err != nil {
			return nil, err
		}
		if rs.Failures > 0 {
			ps.Failures, ps.RetryAt = rs.Failures, &rs.RetryAt
		}
		if err := db.QueryRowContext(ctx, `SELECT
			COALESCE(SUM(state = 'present'), 0), COALESCE(SUM(state = 'absent'), 0)
			FROM content_provider WHERE provider = ?`, k).Scan(&ps.Present, &ps.Absent); err != nil {
			return nil, err
		}
		st.Providers = append(st.Providers, ps)
	}
	var err error
	st.Contents, err = ContentCounts(ctx, db)
	return st, err
}

func lastRun(ctx context.Context, db *sql.DB, key string, okOnly bool) (*RunInfo, error) {
	q := `SELECT id, started_at, finished_at, ok, COALESCE(error, '') FROM provider_sync_run WHERE provider = ?`
	if okOnly {
		q += ` AND ok = 1`
	}
	q += ` ORDER BY finished_at DESC, id DESC LIMIT 1`
	var r RunInfo
	var s, f int64
	err := db.QueryRowContext(ctx, q, key).Scan(&r.ID, &s, &f, &r.OK, &r.Error)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.Started, r.Finished = store.FromMillis(s), store.FromMillis(f)
	return &r, nil
}
