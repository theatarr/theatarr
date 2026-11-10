package router

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Re-search after a failure (docs/design/router.md "Re-search after a failure"). When a download fails for a reason the
// arrs and decypharr answer with "blocklist and search again", the router asks the acquirer to search that title
// (or those episodes) at once instead of waiting for the wanted loop: job JobResearch, handled by internal/acquire,
// which skips it when the version is not active or router.enabled is false, and counts it against
// router.wanted.per_hour. The router only enqueues; it never searches.
//
// The failures:
//   - ReasonExhausted: every candidate of a route was a miss on every provider (uncached, refused);
//   - ReasonStalled, ReasonTorrentFiles, ReasonLibrary, ReasonProvider, ReasonReadyTimeout: a download failed and
//     its hash was blocklisted for the version (stall timeout, dangerous files, the library's sample, runtime or
//     mislabel guard, provider failure, ready deadline). These are found by the poller from router_blocklist rows not
//     researched yet (researched_at), so the library needs no hook of its own.
//
// A release a person added by magnet or hash (policy.ManualIndexer) is the exception to both: its failure starts no
// search (manualAdd).
//
// ReasonFresh is the acquirer's own: the follow-ups it queues after a ReasonExhausted re-search for new content.
// ReasonDeadLink is the library's: a relink sweep marked an active version's link dead (no provider holds its file).
// Its route starts no ReasonExhausted re-search (deadLinkRoute).

// JobResearch is the acquirer's job kind for a search after a failure; the payload is a ResearchRequest.
const JobResearch = "acquire.research"

// Reasons for a re-search.
const (
	ReasonExhausted    = "exhausted"
	ReasonStalled      = BlocklistStalled
	ReasonTorrentFiles = BlocklistTorrentFiles
	ReasonLibrary      = BlocklistLibrary
	ReasonProvider     = BlocklistProvider
	ReasonReadyTimeout = BlocklistReadyTimeout
	// ReasonFresh: a follow-up search for new content that missed everywhere (acquire, "Follow-ups").
	ReasonFresh = "fresh"
	// ReasonDeadLink: one search for the title or episodes of a link relink marked dead (acquire, "Follow-ups").
	ReasonDeadLink = "dead_link"
)

// researchSources are the blocklist sources that trigger a re-search.
var researchSources = []string{BlocklistStalled, BlocklistTorrentFiles, BlocklistLibrary, BlocklistProvider,
	BlocklistReadyTimeout}

// ResearchRequest asks the acquirer to search a title's version again after a failure.
type ResearchRequest struct {
	TitleID   int64  `json:"title_id"`
	VersionID int64  `json:"version_id"`
	Version   string `json:"version"`
	// Episodes are the series episodes the failed download held (empty: the whole movie, or unknown).
	Episodes []int64 `json:"episodes,omitempty"`
	Reason   string  `json:"reason"`
	// DownloadID is the failed download, when one.
	DownloadID int64 `json:"download_id,omitempty"`
	// Step and MissedAt place a ReasonFresh follow-up in its schedule: the nth, from the miss at MissedAt.
	Step     int       `json:"step,omitempty"`
	MissedAt time.Time `json:"missed_at,omitzero"`
	// RetryNew marks a request of a follow-up chain started by an empty search under router.wanted.retry_new
	// (internal/acquire): a step of the chain, or one of its searches asked for again after no indexer answered.
	// It does nothing unless that switch is on, and turning the switch off cancels it while queued. A chain started
	// by a route that missed everywhere has it unset. Left out when unset.
	RetryNew bool `json:"retry_new,omitempty"`
}

// research enqueues a re-search, unless the router may not write for the version (shadow, or the kill switch off) or
// has no job queue. Failures are logged: a re-search is an optimisation, the wanted loop searches later anyway.
func (r *Router) research(ctx context.Context, titleID, versionID int64, version string, episodes []int64,
	reason string, dl int64) bool {
	if _, err := r.Gate(version); err != nil || r.q == nil || titleID == 0 || versionID == 0 {
		return false
	}
	p := ResearchRequest{TitleID: titleID, VersionID: versionID, Version: version, Episodes: episodes,
		Reason: reason, DownloadID: dl}
	key := JobResearch + "/" + version + "/" + strconv.FormatInt(titleID, 10) + "/" + reason + "/" +
		strconv.FormatInt(dl, 10) + "/" + strconv.FormatInt(store.Millis(r.now()), 10)
	if _, _, err := r.q.Enqueue(ctx, JobResearch, p, jobs.EnqueueOptions{DedupeKey: key, MaxAttempts: 1}); err != nil {
		r.log.Warn("re-search not queued", "title", titleID, "version", version, "reason", reason, "err", err)
		return false
	}
	r.log.Info("re-search queued", "title", titleID, "version", version, "reason", reason, "download", dl,
		"episodes", len(episodes))
	return true
}

// researchBlocklisted queues a re-search for each blocklist row of a failure not researched yet, and marks it. Rows
// of a version the router may not write for are marked too: no backlog of searches when it is switched on.
func (r *Router) researchBlocklisted(ctx context.Context) (int, error) {
	type row struct {
		version, eps         string
		hash                 string
		title, versionID, dl int64
		source, indexer      string
	}
	q := `SELECT b.version_key, b.info_hash, b.source, COALESCE(b.title_id, 0), COALESCE(d.version_id, 0),
		COALESCE(b.download_id, 0), COALESCE(d.episodes, '[]'), COALESCE(d.indexer, '') FROM router_blocklist b
		LEFT JOIN download d ON d.id = b.download_id WHERE b.researched_at IS NULL AND b.source IN (?, ?, ?, ?, ?)`
	rows, err := r.st.Writer().QueryContext(ctx, q, researchSources[0], researchSources[1], researchSources[2],
		researchSources[3], researchSources[4])
	if err != nil {
		return 0, fmt.Errorf("router: re-search: %w", err)
	}
	var todo []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.version, &x.hash, &x.source, &x.title, &x.versionID, &x.dl, &x.eps,
			&x.indexer); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, x := range todo {
		var eps []int64
		_ = json.Unmarshal([]byte(x.eps), &eps)
		// The failed download of a release added by hand starts no search (manualAdd); its row is marked all the same.
		if !policy.IsManualAdd(x.indexer) && r.research(ctx, x.title, x.versionID, x.version, eps, x.source, x.dl) {
			n++
		}
		if _, err := r.st.Writer().ExecContext(ctx, `UPDATE router_blocklist SET researched_at = ?
			WHERE version_key = ? AND info_hash = ?`, store.Millis(r.now()), x.version, x.hash); err != nil {
			return n, fmt.Errorf("router: re-search: %w", err)
		}
	}
	return n, nil
}

// deadLinkRoute reads, for a route from an acquire run (source "acquire:<run>"), whether that run is a dead link's
// search (ReasonDeadLink), and the run's episodes. Such a route adds only for a target still missing (freshCandidates)
// and starts no exhausted re-search: the dead link gets one search, not a chain.
func (r *Router) deadLinkRoute(ctx context.Context, source string) (bool, []int64, error) {
	id, ok := strings.CutPrefix(source, "acquire:")
	if !ok {
		return false, nil, nil
	}
	var eps string
	err := r.st.Writer().QueryRowContext(ctx, `SELECT episodes FROM acquire_run WHERE id = ? AND research = ?`, id,
		ReasonDeadLink).Scan(&eps)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("router: route source: %w", err)
	}
	var out []int64
	_ = json.Unmarshal([]byte(eps), &out)
	return true, out, nil
}

// manualAdd reports whether a route's candidates are a release a person added by magnet or hash (arrview.AddRelease,
// policy.ManualIndexer). Its failure starts no search: the person chose that release, not "any release of the title".
func manualAdd(cs []Candidate) bool {
	return len(cs) > 0 && !slices.ContainsFunc(cs, func(c Candidate) bool { return !policy.IsManualAdd(c.Release.Indexer) })
}

// missedEverywhere reports whether a route ended with at least one release missed on every provider, or not cached
// on a client-routed version's client (rather than, say, only hash-less candidates, or misses already known
// within miss_ttl).
func missedEverywhere(steps []Step) bool {
	return slices.ContainsFunc(steps, func(s Step) bool {
		return s.Provider == "" && (s.Outcome == "miss" && s.Code == "miss" || s.Code == codeClientMiss)
	})
}

// candidateEpisodes is the union of the candidates' episodes, in order.
func candidateEpisodes(cs []Candidate) []int64 {
	var out []int64
	for _, c := range cs {
		for _, e := range c.Episodes {
			if !slices.Contains(out, e) {
				out = append(out, e)
			}
		}
	}
	return out
}
