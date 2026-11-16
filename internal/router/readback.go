package router

import (
	"context"
	"database/sql"
	"time"

	"github.com/theatarr/theatarr/internal/provider"
	"github.com/theatarr/theatarr/internal/provider/adder"
)

// AddVerdict is what ReadBackAdd found out about an add made outside a route.
type AddVerdict int

const (
	AddNoVerdict       AddVerdict = iota // the reads could not tell (a failed read, an unknown status)
	AddCached                            // the provider holds it downloaded (at once or within the grace)
	AddNotCached                         // still converting, queued or downloading after the grace
	AddNameRefused                       // Real-Debrid: a video to select has a name it refuses; nothing selected
	AddNothingPlayable                   // Real-Debrid: no playable file to select; nothing selected
)

// ReadBackAdd reads add a on provider rp (of kind) back the way a route's cached-only settle does (readBack): on
// Real-Debrid it waits through the magnet conversion and selects every playable file (PlayableIDs, never the nfo,
// text or images), unless a selected video's own name is one Real-Debrid refuses (rdRefusedPick); it always waits one
// grace before calling an add not cached. The torrent manager's "Add to other providers" uses it (internal/torrents).
// The returned words say why.
func ReadBackAdd(ctx context.Context, rp provider.Provider, ad adder.Adder, kind string, a *adder.Added,
	grace time.Duration, sleep func(ctx context.Context, d time.Duration) error) (AddVerdict, *provider.Torrent, string) {
	if kind == provider.KindAllDebrid && a.Ready {
		return AddCached, nil, "AllDebrid answered ready"
	}
	if sleep == nil {
		sleep = sleepCtx
	}
	nothing := false
	cv, t, why := readBack(ctx, rp, kind, a, grace, sleep, func(t *provider.Torrent) (string, error) {
		if len(t.Files) == 0 {
			return "the provider did not list the files yet", nil
		}
		ids := PlayableIDs(t.Files)
		if len(ids) == 0 {
			nothing = true
			return "no playable file in the torrent", nil
		}
		if name := rdRefusedPick(t.Files, ids); name != "" {
			return "", &nameRefusedError{file: name}
		}
		wctx, cancel := context.WithTimeout(ctx, providerWriteLimit)
		defer cancel()
		return "", ad.SelectFiles(wctx, t.ID, ids)
	})
	switch {
	case nothing:
		return AddNothingPlayable, t, why
	case cv == cvCached:
		return AddCached, t, why
	case cv == cvUncached:
		return AddNotCached, t, why
	case cv == cvNameRefused:
		return AddNameRefused, t, why
	}
	return AddNoVerdict, t, why
}

// RecordCacheEvidence writes a provider_cache row as the router's own writer does (setCacheSource, with its rules on
// which live rows a new one may replace): for adds made outside a route.
func RecordCacheEvidence(ctx context.Context, db *sql.DB, hash, provider, state, source string, expires,
	now time.Time) error {
	return writeCache(ctx, db, hash, provider, state, source, expires, now)
}
