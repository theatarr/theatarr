package router

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/store"
)

// Step outcome and code for a provider order changed by cache evidence.
const (
	outcomeOrdered  = "ordered"
	codeCachedFirst = "cached_first"
)

// providerOrder puts the providers that evidence says hold the release cached first, each group in the version's
// order. Evidence is the release's own (DMM's cached_on, the same hash in another feed of the search, TorBox's check
// at search time) and live provider_cache rows in state cached. A provider with no evidence keeps its place among
// the rest and is tried exactly as before: unknown is never uncached. why is "" when the order is unchanged.
func (r *Router) providerOrder(ctx context.Context, providers []string, rel *indexer.Release, hash string,
	now time.Time) (order []string, why string, err error) {
	codes := rel.CachedCodes()
	var first, rest, reasons []string
	for _, pk := range providers {
		via := ""
		if pv := r.pol.Providers[pk]; pv != nil {
			if v, ok := codes[indexer.CacheCode(pv.Kind)]; ok {
				via = v
			}
		}
		if via == "" {
			state, source, err := r.cacheRow(ctx, hash, pk, now)
			if err != nil {
				return nil, "", err
			}
			if state == "cached" {
				via = source
			}
		}
		if via == "" {
			rest = append(rest, pk)
			continue
		}
		first = append(first, pk)
		reasons = append(reasons, pk+" ("+via+")")
	}
	order = append(first, rest...)
	if slices.Equal(order, providers) {
		return providers, "", nil
	}
	return order, fmt.Sprintf("cached on %s: tried first; order %s (version order %s)", strings.Join(reasons, ", "),
		strings.Join(order, ", "), strings.Join(providers, ", ")), nil
}

// cacheRow reads the live provider_cache row for a hash at a provider: state and source, or "" for none.
func (r *Router) cacheRow(ctx context.Context, hash, provider string, now time.Time) (string, string, error) {
	var state, source string
	err := r.st.Writer().QueryRowContext(ctx, `SELECT state, source FROM provider_cache WHERE info_hash = ? AND
		provider = ? AND (expires_at IS NULL OR expires_at > ?)`, hash, provider, store.Millis(now)).Scan(&state, &source)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("router: cache state: %w", err)
	}
	return state, source, nil
}
