// Package snapshot records what a search decided between when it hands work to the router: the best accepted
// releases in rank order, the rejected releases with the highest scores, and a count of every rejection reason
// (table journal_decision, migration 00024). It is the one part of the grab journal that is written at decision
// time; everything else the journal reads from the tables that already record it (internal/journal).
//
// It is a leaf: the acquirer and the shadow runner call Record, and the journal reads the rows back. A failure to
// record never stops a route; callers log it.
package snapshot

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// Kept per search: the accepted releases in rank order up to KeepAccepted, then the rejected ones with the highest
// scores up to KeepRejected.
const (
	KeepAccepted = 8
	KeepRejected = 4
	// maxRejectionText caps one rejection message.
	maxRejectionText = 200
)

// MergedRunSQL is true for acquire_run alias r when it is a member of a merged search of several seasons: its
// search group (root and children) holds a run of the same version for another season. Its stored search and its
// downloads carry the root's route source (acquire:<root>).
func MergedRunSQL(r string) string {
	return `EXISTS (SELECT 1 FROM acquire_run mr WHERE COALESCE(mr.search_run, mr.id) = COALESCE(` + r + `.search_run, ` +
		r + `.id) AND mr.version_id = ` + r + `.version_id AND COALESCE(mr.season, -1) <> COALESCE(` + r + `.season, -1))`
}

// RunDownloadsSQL is true for a download (alias d) that acquire run r's search opened: one of its own route, or for
// a member of a merged search one of the root's route that holds a member's episode.
func RunDownloadsSQL(d, r string) string {
	// The route's source first (download.source is indexed), then a merged member's episode overlap.
	return `(` + d + `.source = 'acquire:' || CASE WHEN ` + MergedRunSQL(r) + ` THEN COALESCE(` + r + `.search_run, ` + r +
		`.id) ELSE ` + r + `.id END AND (NOT ` + MergedRunSQL(r) + ` OR EXISTS (SELECT 1 FROM json_each(` + d + `.episodes) de
		JOIN json_each(` + r + `.episodes) re ON de.value = re.value)))`
}

// Candidate is one release as the decision saw it. Links (GUID, download URLs, magnets) are never kept.
type Candidate struct {
	Rank       int                  `json:"rank"`
	Release    string               `json:"release"`
	Indexer    string               `json:"indexer,omitempty"`
	InfoHash   string               `json:"info_hash,omitempty"`
	Size       int64                `json:"size,omitempty"`
	Seeders    int                  `json:"seeders,omitempty"`
	Published  *time.Time           `json:"published,omitempty"`
	Accepted   bool                 `json:"accepted"`
	Rejections []decide.Rejection   `json:"rejections,omitempty"`
	Quality    string               `json:"quality,omitempty"`
	Revision   int                  `json:"revision,omitempty"`
	Formats    []decide.FormatScore `json:"formats,omitempty"`
	Score      int                  `json:"score"`
	Languages  []string             `json:"languages,omitempty"`
	CachedOn   []string             `json:"cached_on,omitempty"`
	Episodes   []string             `json:"episodes,omitempty"`
	// MultiSeason: the release is a pack of several seasons (decide.Decision.SeveralSeasons: an anime release by its
	// claim, any other by its name).
	MultiSeason bool `json:"multi_season,omitempty"`
	// Kept is Decision.Kept: the episodes whose file the accepted pack does not upgrade.
	Kept     []string         `json:"kept,omitempty"`
	Existing *decide.Existing `json:"existing,omitempty"`
	// UpgradeFile is Decision.UpgradeFile: the file UpgradeWhy compares with, and its episode.
	UpgradeFile *decide.Existing `json:"upgrade_file,omitempty"`
	Upgrade     bool             `json:"upgrade,omitempty"`
	UpgradeWhy  string           `json:"upgrade_reason,omitempty"`
	// Source is the indexer kind (torznab, cardigann, dmm, ...); Privacy the release's privacy label.
	Source  string `json:"source,omitempty"`
	Privacy string `json:"privacy,omitempty"`
	// Leechers is peers minus seeders; nil when the source gave no peer count.
	Leechers *int `json:"leechers,omitempty"`
	// Flags are the indexer flags (Release.Flags) plus, for a release that is not public, those the volume factors
	// give, as the decision engine reads them: freeleech, halfleech, freeleech25, freeleech75, doubleupload.
	Flags []string `json:"flags,omitempty"`
	// QualityRank is Decision.ProfileRank: 1 is the profile's best quality, 0 not allowed by the profile.
	QualityRank int `json:"quality_rank,omitempty"`
	// Over (an accepted release with another accepted torrent ranked after it): why the comparer put this one first,
	// recorded when the search decides, since the stored sample lacks SeaDex preference and indexer priority.
	Over *Over `json:"over,omitempty"`
}

// Over is why the search ranked an accepted release before the next accepted torrent of its episodes: that
// release's name and the comparer's own words (decide.Decision.ChosenOver), recorded when the search decided.
type Over struct {
	Release string `json:"release"`
	Reason  string `json:"reason"`
}

// Search is one decided search.
type Search struct {
	Policy *policy.Policy
	// Source is the router request source: acquire:<run id>, rss.
	Source    string
	RunID     int64
	TitleID   int64
	Version   string
	Target    string
	Ranked    []*decide.Decision
	Releases  map[*decide.Decision]indexer.Release
	Notes     []string
	CreatedAt time.Time
	// PolicyRev is the policy revision the search decided under; empty takes Policy's.
	PolicyRev string
}

// Execer is a *sql.DB or *sql.Tx.
type Execer interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
}

// Candidates picks and summarises the kept candidates of a ranked list, and counts every rejection reason.
func Candidates(ranked []*decide.Decision, rels map[*decide.Decision]indexer.Release) ([]Candidate, map[string]int) {
	var out, rejected []Candidate
	counts := map[string]int{}
	for i, d := range ranked {
		if d == nil {
			continue
		}
		if !d.Accepted {
			seen := map[string]bool{}
			for _, r := range d.Rejections {
				if !seen[r.Reason] {
					seen[r.Reason] = true
					counts[r.Reason]++
				}
			}
		}
		c := summarize(i+1, d, rels[d])
		if d.Accepted {
			if len(out) < KeepAccepted {
				if o := d.ChosenOver(); o != nil {
					c.Over = &Over{Release: o.Release, Reason: o.Reason}
				}
				out = append(out, c)
			}
			continue
		}
		rejected = append(rejected, c)
	}
	sort.SliceStable(rejected, func(i, j int) bool { return rejected[i].Score > rejected[j].Score })
	if len(rejected) > KeepRejected {
		rejected = rejected[:KeepRejected]
	}
	return append(out, rejected...), counts
}

func summarize(rank int, d *decide.Decision, rel indexer.Release) Candidate {
	c := Candidate{Rank: rank, Release: d.Release, Indexer: rel.Indexer, InfoHash: strings.ToLower(rel.InfoHash),
		Size: rel.Size, Seeders: rel.Seeders, Accepted: d.Accepted, Quality: d.Quality, Revision: d.Revision.Version,
		Formats: d.Formats, Score: d.Score, Languages: d.Languages, CachedOn: rel.CachedList(), Episodes: d.Episodes,
		Kept: d.Kept, Existing: d.Existing, Upgrade: d.Upgrade, UpgradeWhy: d.UpgradeReason, UpgradeFile: d.UpgradeFile, Source: rel.Kind,
		Privacy: string(rel.Privacy), Flags: releaseFlags(rel), QualityRank: d.ProfileRank}
	if rel.Seeders >= 0 && rel.Peers >= rel.Seeders {
		n := rel.Peers - rel.Seeders
		c.Leechers = &n
	}
	if c.Release == "" {
		c.Release = rel.Title
	}
	c.MultiSeason = d.SeveralSeasons()
	if c.Indexer == "" {
		c.Indexer = d.Indexer
	}
	if !rel.PublishDate.IsZero() {
		t := rel.PublishDate.UTC()
		c.Published = &t
	}
	for _, r := range d.Rejections {
		if len(r.Message) > maxRejectionText {
			r.Message = r.Message[:maxRejectionText] + "…"
		}
		c.Rejections = append(c.Rejections, r)
	}
	return c
}

// releaseFlags is a copy of the release's flags plus the ones its volume factors give, read as the decision engine
// reads them (decide's indexerFlags: the Extras downloadvolumefactor and uploadvolumefactor, as float32). A public
// release gets none from its factors: public feeds (DMM, Prowlarr, public cardigann sites) send
// downloadvolumefactor=0 for every torrent, which says nothing. Display only; the decision engine reads the factors
// itself.
func releaseFlags(rel indexer.Release) []string {
	out := slices.Clone(rel.Flags)
	if rel.Privacy == policy.PrivacyPublic {
		return out
	}
	add := func(f string) {
		if !slices.ContainsFunc(out, func(s string) bool { return strings.EqualFold(s, f) }) {
			out = append(out, f)
		}
	}
	factor := func(k string) float64 {
		if s, ok := rel.Extras[k]; ok {
			if f, err := strconv.ParseFloat(strings.TrimSpace(s), 32); err == nil {
				return float64(float32(f))
			}
		}
		return 1
	}
	switch factor("downloadvolumefactor") {
	case 0:
		add(flagFreeleech)
	case 0.5:
		add(flagHalfleech)
	case 0.75:
		add(flagFreeleech25)
	case 0.25:
		add(flagFreeleech75)
	}
	if factor("uploadvolumefactor") == 2 {
		add(flagDoubleUpload)
	}
	return out
}

// The flags releaseFlags gives from the volume factors.
const (
	flagFreeleech    = "freeleech"
	flagHalfleech    = "halfleech"
	flagFreeleech25  = "freeleech25"
	flagFreeleech75  = "freeleech75"
	flagDoubleUpload = "doubleupload"
)

// volumeFlags are the flags releaseFlags gives from the volume factors.
var volumeFlags = []string{flagFreeleech, flagHalfleech, flagFreeleech25, flagFreeleech75, flagDoubleUpload}

// DropPublicVolumeFlags removes the volume-factor flags from the public candidates of a stored list: decisions
// stored before public releases stopped getting them still carry them. Their other flags stay.
func DropPublicVolumeFlags(cs []Candidate) {
	for i := range cs {
		if cs[i].Privacy != string(policy.PrivacyPublic) || len(cs[i].Flags) == 0 {
			continue
		}
		cs[i].Flags = slices.DeleteFunc(cs[i].Flags, func(f string) bool {
			return slices.ContainsFunc(volumeFlags, func(v string) bool { return strings.EqualFold(v, f) })
		})
	}
}

// Record writes one search. An empty search has no journal candidate sample.
func Record(ctx context.Context, db Execer, s Search) error {
	if s.TitleID == 0 || s.Version == "" {
		return nil
	}
	at := s.CreatedAt
	if at.IsZero() {
		at = time.Now()
	}
	if len(s.Ranked) == 0 {
		return nil
	}
	cands, counts := Candidates(s.Ranked, s.Releases)
	accepted := 0
	for _, d := range s.Ranked {
		if d != nil && d.Accepted {
			accepted++
		}
	}
	cj, err := json.Marshal(cands)
	if err != nil {
		return err
	}
	rj, err := json.Marshal(counts)
	if err != nil {
		return err
	}

	var run any
	if s.RunID > 0 {
		run = s.RunID
	}
	rev := s.PolicyRev
	if rev == "" && s.Policy != nil {
		rev = s.Policy.Revision
	}
	_, err = db.ExecContext(ctx, `INSERT INTO journal_decision (source, run_id, title_id, version_key, target, results,
		accepted, candidates, rejections, notes, policy_rev, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.Source, run, s.TitleID, s.Version, s.Target, len(s.Ranked), accepted, string(cj), string(rj),
		strings.Join(s.Notes, "; "), rev, store.Millis(at))
	return err
}

// Route outcomes of a run's releases (RunOutcomes), by lower-case info hash.
const (
	OutcomeTaken  = "taken"  // a provider took it (added), whatever came of it after
	OutcomeMissed = "missed" // every provider missed it before any took it: not cached
	OutcomeFailed = "failed" // the route failed before a provider took it, for another reason
)

// Querier is a *sql.DB or *sql.Tx.
type Querier interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
}

// RunOutcomes is what the route of a stored search recorded for its releases: the downloads its source opened (an
// acquire run's, or an RSS read's since at), by hash. A release with no download has no outcome.
func RunOutcomes(ctx context.Context, db Querier, source string, at int64) (map[string]string, error) {
	q := `SELECT lower(info_hash), added_at IS NOT NULL, state = 'failed' AND added_at IS NULL AND (` + missedSQL +
		` OR COALESCE(provider, '') = '') FROM download WHERE source = ?`
	args := []any{source}
	if source == "rss" {
		q, args = q+` AND created_at >= ?`, append(args, at)
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var h string
		var taken, missed bool
		if err := rows.Scan(&h, &taken, &missed); err != nil {
			return nil, err
		}
		switch {
		case taken:
			out[h] = OutcomeTaken
		case out[h] == OutcomeTaken:
		case missed:
			if out[h] == "" {
				out[h] = OutcomeMissed
			}
		default:
			out[h] = OutcomeFailed
		}
	}
	return out, rows.Err()
}

// missedSQL is activity.MissedSQL on the download table (this package is a leaf): failed, never added, missed on
// every provider.
const missedSQL = `COALESCE(error, '') GLOB 'miss on every provider*'`

// ChosenLine is the one explanation of a stored search's choice (Last search, Why?, the journal): for the release the
// route took (taken, by hash: outcomes says it was taken), how many better-ranked accepted releases of its episodes
// the route recorded as not cached or failed, then why it ranked before the next one ("2 better releases were not
// cached. Chosen over <release>: higher quality (Remux-2160p over WEBDL-2160p)"). A better release with no recorded
// outcome is not counted. "" for a release that was not taken, or with nothing to say.
func ChosenLine(cands []Candidate, taken string, outcomes map[string]string) string {
	taken = strings.ToLower(taken)
	if taken == "" || outcomes[taken] != OutcomeTaken {
		return ""
	}
	i := slices.IndexFunc(cands, func(c Candidate) bool { return c.Accepted && strings.EqualFold(c.InfoHash, taken) })
	if i < 0 {
		return ""
	}
	eps := cands[i].Episodes
	missed, failed := 0, 0
	seen := map[string]bool{taken: true}
	for _, c := range cands[:i] {
		h := strings.ToLower(c.InfoHash)
		if !c.Accepted || h == "" || seen[h] {
			continue
		}
		seen[h] = true
		if len(eps) > 0 && len(c.Episodes) > 0 && !slices.ContainsFunc(c.Episodes, func(e string) bool { return slices.Contains(eps, e) }) {
			continue
		}
		switch outcomes[h] {
		case OutcomeMissed:
			missed++
		case OutcomeFailed:
			failed++
		}
	}
	var parts []string
	if missed > 0 {
		parts = append(parts, countWords(missed, "better release was", "better releases were")+" not cached")
	}
	if failed > 0 {
		parts = append(parts, countWords(failed, "better release", "better releases")+" failed")
	}
	line := strings.Join(parts, "; ")
	if line != "" {
		line += "."
	}
	if o := cands[i].Over; o != nil && o.Reason != "" {
		line = strings.TrimSpace(line + " Chosen over " + o.Release + ": " + o.Reason)
	}
	return line
}

func countWords(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
