package router

import (
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// Candidate is one accepted release, in rank order. The release's links (GUID, Link, Magnet) never marshal, so a
// Request stored as a job payload holds none; debrid adds are built from the info hash only.
type Candidate struct {
	Search   bool                   `json:"search,omitempty"`
	Criteria []*decide.SeriesSearch `json:"criteria,omitempty"`
	// Identity is the identity validation the candidate passed when a producer handed it to the router
	// (RouteIdentityVersion), and Origin the decision path it was decided on (OriginRSS or OriginSearch, with
	// Criteria for a series search). Both are set once, at the producer boundary (stampFresh), and travel with the
	// candidate through route jobs, download rows and upgrade holds; 0 and "" mean an older build queued it.
	Identity int             `json:"identity,omitempty"`
	Origin   string          `json:"origin,omitempty"`
	Release  indexer.Release `json:"release"`
	Quality  string          `json:"quality,omitempty"`
	Score    int             `json:"score"`
	// Episodes are the episode ids a series release fills.
	Episodes []int64 `json:"episodes,omitempty"`
	// Formats are the decision's matched custom formats with their scores, stored on the download for the library's
	// file names. nil when unknown (not from a decision).
	Formats []decide.FormatScore `json:"formats"`
	// EpisodeMap is the decision's own mapping of a series release (anime franchise entry or alternate cut), stored
	// on the download so the import maps its files the same way; nil for Sonarr's mapping.
	EpisodeMap *decide.EpisodeMap `json:"episode_map,omitempty"`
	// QualityRank is the quality's place in the version's profile (decide.Decision.QualityRank; 0 unknown) and
	// UpgradeReason the decision's reason why the release upgrades the file: what an upgrade hold compares and shows.
	QualityRank   int    `json:"quality_rank,omitempty"`
	UpgradeReason string `json:"upgrade_reason,omitempty"`
}

// RouteIdentityVersion is the validation a candidate's identity passed when it was queued. Version 1:
// one normalised torrent identity from the reported hash and the magnet, checked while the links were still in
// memory (they are never stored), and an explicit origin. A candidate queued by an older build carries none, so its
// identity and criteria cannot be proved from what was stored; `theatarr router reconcile` inventories such rows.
const RouteIdentityVersion = 1

// Origins of a candidate: decided on the RSS path (no search criteria: feeds, release pushes, movie offers, grabs by
// hand) or on a search (its Criteria for a series).
const (
	OriginRSS    = "rss"
	OriginSearch = "search"
)

// stampFresh marks candidates a producer just handed to the router (a new request: no download resumed, no approved
// hold) with the identity validation they passed (identityCandidates runs on them while their links are in memory)
// and the decision path they came from. A request rebuilt from a stored row or payload keeps what was stored. The
// caller passes a copy of the candidates it may change.
func (r *Router) stampFresh(req *Request) {
	if req.DownloadID != 0 || req.Hold != 0 {
		return
	}
	var kind policy.Kind
	if vp := r.pol.Versions[req.Version]; vp != nil {
		kind = vp.Kind
	}
	for i := range req.Candidates {
		c := &req.Candidates[i]
		c.Identity = RouteIdentityVersion
		if c.Origin == "" {
			c.Origin = OriginRSS
			if searchedBy(req.Source, kind, c.Search) {
				c.Origin = OriginSearch
			}
		}
	}
}

// searchedBy says whether a candidate queued by source with the search flag came from a search rather than a feed:
// its own flag, or the source (a wanted or re-search producer, a series offer, which keeps the series decider).
func searchedBy(src string, kind policy.Kind, search bool) bool {
	return search || src == "wanted" || src == "job" || src == "research" ||
		src == "offer" && kind == policy.KindSeries || strings.HasPrefix(src, "acquire:")
}

// Request asks the router to place ranked, disjoint episode releases for one version of a title (one for movies).
type Request struct {
	TitleID   int64  `json:"title_id"`
	VersionID int64  `json:"version_id"`
	Version   string `json:"version"`
	// Candidates are accepted releases, best first (decide.Rank). Rejected ones are left out by Candidates.
	Candidates []Candidate `json:"candidates"`
	// Source says what asked: automatic jobs and feeds, or the explicit grab source.
	Source string `json:"source,omitempty"`
	// DownloadID resumes a queued download (a deferred route).
	DownloadID int64 `json:"download_id,omitempty"`
	// Hold is the approved upgrade hold this route places (hold.go): its candidate is routed without being held
	// again. Every other rule applies.
	Hold int64 `json:"hold,omitempty"`
	// DecisionRev is the policy revision the candidates were decided under, set when the route is queued. An
	// automatic route that runs under another revision decides its candidates again.
	DecisionRev string `json:"decision_rev,omitempty"`
	// deferrals is how often the route job running this request was deferred already after an add, and
	// waitingSince since when it waits (routePayload).
	deferrals    int
	waitingSince time.Time
}

// Candidates turns ranked decisions into candidates: the accepted ones only, in order, each with its release.
// releases maps a decision to the release it was made for (decide.Decision does not carry it); a decision with no
// release is left out.
func Candidates(ranked []*decide.Decision, releases map[*decide.Decision]indexer.Release) []Candidate {
	var out []Candidate
	for _, d := range ranked {
		if d == nil || !d.Accepted {
			continue
		}
		rel, ok := releases[d]
		if !ok {
			continue
		}
		out = append(out, Candidate{Release: rel, Quality: d.Quality, Score: d.Score, Episodes: d.EpisodeIDs,
			Formats: append([]decide.FormatScore{}, d.Formats...), EpisodeMap: d.EpisodeMapping(),
			QualityRank: d.QualityRank(), UpgradeReason: upgradeReason(d)})
	}
	return out
}

// upgradeReason is the decision's upgrade reason when it replaces a file, "" otherwise.
func upgradeReason(d *decide.Decision) string {
	if !d.Upgrade {
		return ""
	}
	return d.UpgradeReason
}

// Outcome is how a route ended.
type Outcome string

const (
	// OutcomeAdded: a provider accepted a release (or already held it: Reused).
	OutcomeAdded Outcome = "added"
	// OutcomeWouldAdd: a dry run found a release and a provider to try.
	OutcomeWouldAdd Outcome = "would_add"
	// OutcomeDeferred: no provider took the release yet because some were paced or backing off; retry at RetryAt.
	OutcomeDeferred Outcome = "deferred"
	// OutcomeExhausted: every candidate was a miss on every provider (or could not go to debrid at all).
	OutcomeExhausted Outcome = "exhausted"
	// OutcomeCancelled: current catalog or file state no longer wants any candidate in the route.
	OutcomeCancelled Outcome = "cancelled"
	// OutcomeInFlight: the version already has a download of this release in flight; nothing was added.
	OutcomeInFlight Outcome = "in_flight"
	// OutcomeHeld: the best release would replace a file of a version with upgrades: approve, and was held for a
	// manager's approval instead (hold.go); nothing was added for it.
	OutcomeHeld Outcome = "held"
	// OutcomeWouldHold: the dry run of OutcomeHeld (a shadow version, or the kill switch off): nothing was held.
	OutcomeWouldHold Outcome = "would_hold"
)

// Mode says whether the router acted or only planned.
type Mode string

const (
	ModeActive   Mode = "active"   // writes allowed: router.enabled is true and the version is active with grab on
	ModeShadow   Mode = "shadow"   // the version is in shadow mode: dry run
	ModeDisabled Mode = "disabled" // router.enabled is false, or the active version's grab is off: dry run
)

// Step is one release considered at one provider (or the release as a whole when Provider is empty).
type Step struct {
	Release  string `json:"release"`
	Hash     string `json:"hash,omitempty"`
	Provider string `json:"provider,omitempty"`
	// Client is the download client of a client-routed version (versions[*].add_via: client).
	Client  string    `json:"client,omitempty"`
	Outcome string    `json:"outcome"` // accepted, would_add, refused, skipped, error, deferred, miss
	Code    string    `json:"code,omitempty"`
	Message string    `json:"message,omitempty"`
	RetryAt time.Time `json:"retry_at,omitzero"`
}

// Result is what Route did, or would do. Steps records every placement in a series batch. The release fields name
// the first deferred release when one needs a retry, otherwise the first placement.
type Result struct {
	Version  string  `json:"version"`
	Mode     Mode    `json:"mode"`
	Why      string  `json:"why,omitempty"` // why a dry run
	Outcome  Outcome `json:"outcome"`
	Release  string  `json:"release,omitempty"`
	Hash     string  `json:"hash,omitempty"`
	Indexer  string  `json:"indexer,omitempty"`
	Provider string  `json:"provider,omitempty"`
	// Client is the download client of a client-routed version (versions[*].add_via: client).
	Client string `json:"client,omitempty"`
	// TorrentID is the provider's id (active only); Reused is true when the provider already held the hash.
	TorrentID  string `json:"torrent_id,omitempty"`
	Reused     bool   `json:"reused,omitempty"`
	DownloadID int64  `json:"download_id,omitempty"`
	PlanID     int64  `json:"plan_id,omitempty"`
	// Hold is the pending upgrade hold the route made or found for its best upgrade (hold.go); 0 when none.
	Hold    int64     `json:"hold,omitempty"`
	RetryAt time.Time `json:"retry_at,omitzero"`
	Steps   []Step    `json:"steps"`
}
