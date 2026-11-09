package gateway

import (
	"context"
	"database/sql"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// Purpose is why an indexer request was sent. The caller that starts a search tags its context (WithPurpose); the
// gateway counts every indexer request it sends per indexer, purpose and UTC day. Accounting only: no purpose changes
// what a request may do.
type Purpose string

// Purposes.
const (
	// PurposeInteractive: a person's release search (the releases list, the arrs' interactive search).
	PurposeInteractive Purpose = "interactive"
	// PurposeWanted: a search for something missing (wanted runs, search commands, add-time and follow-up searches).
	PurposeWanted Purpose = "wanted"
	// PurposeUpgrade: a search for a better copy of something held (cutoff unmet, a search of a movie or of episodes
	// that all have a file).
	PurposeUpgrade Purpose = "upgrade"
	// PurposeRSS: the RSS sync.
	PurposeRSS Purpose = "rss"
	// PurposeShadow: the shadow comparison's own search for an arr grab.
	PurposeShadow Purpose = "shadow"
	// PurposeTest: a source test from the settings pages.
	PurposeTest Purpose = "test"
	// PurposeOther: an untagged request.
	PurposeOther Purpose = "other"
)

// Purposes lists every purpose in display order.
var Purposes = []Purpose{PurposeInteractive, PurposeWanted, PurposeUpgrade, PurposeRSS, PurposeShadow, PurposeTest,
	PurposeOther}

type purposeKey struct{}

// WithPurpose tags the indexer requests sent with the returned context with p.
func WithPurpose(ctx context.Context, p Purpose) context.Context {
	return context.WithValue(ctx, purposeKey{}, p)
}

// PurposeOf is the purpose ctx is tagged with, PurposeOther when none.
func PurposeOf(ctx context.Context) Purpose {
	if p, ok := ctx.Value(purposeKey{}).(Purpose); ok && p != "" {
		return p
	}
	return PurposeOther
}

// requestDays is how many UTC days of request counts are kept (today included).
const requestDays = 30

type requestKey struct {
	indexer string
	purpose Purpose
	day     int64 // UTC day start, Unix ms
}

// IndexerRequests is one indexer's requests by purpose: today (UTC) and the last 7 UTC days, today included. Purposes
// with no request are left out.
type IndexerRequests struct {
	Today map[Purpose]int `json:"today"`
	Week  map[Purpose]int `json:"week"`
}

func dayStart(t time.Time) int64 {
	u := t.UTC()
	return store.Millis(time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC))
}

// countRequest counts the hops of one request sent to an indexer target: each redirect hop the client sent is one
// request. A source probe counts only a tagged request: the quality sync and the cardigann definitions fetch are probes
// of indexer-class targets that are no indexer.
func (g *Gateway) countRequest(ctx context.Context, ti *targetInfo, hops int) {
	if ti.class != ClassIndexer || hops <= 0 {
		return
	}
	if _, tagged := ctx.Value(purposeKey{}).(Purpose); g.probe != nil && !tagged {
		return
	}
	g.state.countRequest(ti.key, PurposeOf(ctx), hops, g.now())
}

// hopsKey carries a request's count of the hops its client sent (guard.RoundTrip adds one per hop).
type hopsKey struct{}

// requestFlushInterval is how often the request counts are written to the database (RunRequestFlush).
const requestFlushInterval = time.Minute

// countRequest counts n requests in memory only; flushRequests writes them. The first count of a new day drops days
// older than requestDays, and the next flush drops their rows.
func (s *state) countRequest(indexer string, p Purpose, n int, now time.Time) {
	day := dayStart(now)
	cut := day - (requestDays-1)*24*time.Hour.Milliseconds()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.requests == nil {
		s.requests = map[requestKey]int{}
	}
	if s.pending == nil {
		s.pending = map[requestKey]int{}
	}
	k := requestKey{indexer, p, day}
	s.requests[k] += n
	s.pending[k] += n
	if day > s.requestDay {
		s.requestDay = day
		s.requestCut = cut
		for k := range s.requests {
			if k.day < cut {
				delete(s.requests, k)
			}
		}
	}
}

// flushRequests adds the counts made since the last flush to the stored ones and prunes the days no longer kept. Counts
// whose write fails stay for the next flush.
func (s *state) flushRequests(ctx context.Context) error {
	if s.st == nil {
		return nil
	}
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	s.mu.Lock()
	pending, cut := s.pending, s.requestCut
	s.pending, s.requestCut = nil, 0
	s.mu.Unlock()
	if len(pending) == 0 && cut == 0 {
		return nil
	}
	err := s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		for k, n := range pending {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO gateway_indexer_request (indexer, purpose, day_start_at, count) VALUES (?, ?, ?, ?)
				ON CONFLICT (indexer, purpose, day_start_at) DO UPDATE SET count = count + excluded.count`,
				k.indexer, string(k.purpose), k.day, n); err != nil {
				return err
			}
		}
		if cut != 0 {
			_, err := tx.ExecContext(ctx, `DELETE FROM gateway_indexer_request WHERE day_start_at < ?`, cut)
			return err
		}
		return nil
	})
	if err != nil {
		s.mu.Lock()
		if s.pending == nil {
			s.pending = map[requestKey]int{}
		}
		for k, n := range pending {
			s.pending[k] += n
		}
		s.requestCut = max(s.requestCut, cut)
		s.mu.Unlock()
	}
	return err
}

// FlushRequests writes the indexer request counts made since the last flush (RunRequestFlush does it every minute and
// at its end). A one-shot command that sends indexer requests with a store calls it before it exits.
func (g *Gateway) FlushRequests(ctx context.Context) error {
	return g.state.flushRequests(ctx)
}

// RunRequestFlush writes the indexer request counts every minute until ctx ends, and once more then. A crash loses
// at most the last minute's counts.
func (g *Gateway) RunRequestFlush(ctx context.Context) {
	t := time.NewTicker(requestFlushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if err := g.FlushRequests(fctx); err != nil {
				g.log.Warn("gateway: saving indexer request counts", "err", err)
			}
			return
		case <-t.C:
			if err := g.FlushRequests(ctx); err != nil && ctx.Err() == nil {
				g.log.Warn("gateway: saving indexer request counts", "err", err)
			}
		}
	}
}

// loadRequests reads the kept request counts.
func (s *state) loadRequests(ctx context.Context, now time.Time) error {
	s.requests = map[requestKey]int{}
	cut := dayStart(now) - (requestDays-1)*24*time.Hour.Milliseconds()
	rows, err := s.st.Reader().QueryContext(ctx,
		`SELECT indexer, purpose, day_start_at, count FROM gateway_indexer_request WHERE day_start_at >= ?`, cut)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var k requestKey
		var p string
		var n int
		if err := rows.Scan(&k.indexer, &p, &k.day, &n); err != nil {
			return err
		}
		k.purpose = Purpose(p)
		s.requests[k] = n
	}
	return rows.Err()
}

// indexerRequests sums the counts of today and the last 7 days per indexer.
func (s *state) indexerRequests(now time.Time) map[string]IndexerRequests {
	today := dayStart(now)
	week := today - 6*24*time.Hour.Milliseconds()
	out := map[string]IndexerRequests{}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, n := range s.requests {
		if k.day < week || k.day > today {
			continue
		}
		r, ok := out[k.indexer]
		if !ok {
			r = IndexerRequests{Today: map[Purpose]int{}, Week: map[Purpose]int{}}
			out[k.indexer] = r
		}
		r.Week[k.purpose] += n
		if k.day == today {
			r.Today[k.purpose] += n
		}
	}
	return out
}
