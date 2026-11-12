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
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/ceiling"
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
	Existing   *decide.Existing     `json:"existing,omitempty"`
	Upgrade    bool                 `json:"upgrade,omitempty"`
	UpgradeWhy string               `json:"upgrade_reason,omitempty"`
	// Source is the indexer kind (torznab, cardigann, dmm, ...); Privacy the release's privacy label.
	Source  string `json:"source,omitempty"`
	Privacy string `json:"privacy,omitempty"`
	// Leechers is peers minus seeders; nil when the source gave no peer count.
	Leechers *int `json:"leechers,omitempty"`
	// Flags are the indexer flags (Release.Flags) plus those the volume factors give, as the decision engine reads
	// them: freeleech, halfleech, freeleech25, freeleech75, doubleupload.
	Flags []string `json:"flags,omitempty"`
	// QualityRank is Decision.ProfileRank: 1 is the profile's best quality, 0 not allowed by the profile.
	QualityRank int `json:"quality_rank,omitempty"`
}

// Search is one decided search.
type Search struct {
	Policy   *policy.Policy
	Coverage []ceiling.Coverage
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
		Existing: d.Existing, Upgrade: d.Upgrade, UpgradeWhy: d.UpgradeReason, Source: rel.Kind,
		Privacy: string(rel.Privacy), Flags: releaseFlags(rel), QualityRank: d.ProfileRank}
	if rel.Seeders >= 0 && rel.Peers >= rel.Seeders {
		n := rel.Peers - rel.Seeders
		c.Leechers = &n
	}
	if c.Release == "" {
		c.Release = rel.Title
	}
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
// reads them (decide's indexerFlags: the Extras downloadvolumefactor and uploadvolumefactor, as float32).
func releaseFlags(rel indexer.Release) []string {
	out := slices.Clone(rel.Flags)
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
		add("freeleech")
	case 0.5:
		add("halfleech")
	case 0.75:
		add("freeleech25")
	case 0.25:
		add("freeleech75")
	}
	if factor("uploadvolumefactor") == 2 {
		add("doubleupload")
	}
	return out
}

// Record writes one search. Empty searches still record ceiling coverage, but have no journal candidate sample.
func Record(ctx context.Context, db Execer, s Search) error {
	if s.TitleID == 0 || s.Version == "" {
		return nil
	}
	at := s.CreatedAt
	if at.IsZero() {
		at = time.Now()
	}
	var ceilingErr error
	if db, ok := db.(ceiling.DB); ok && s.Target != "rss" {
		source := "search"
		ceilingErr = ceiling.Record(ctx, db, s.Policy, s.TitleID, s.Version, s.Target, source, s.Ranked, s.Releases, s.Coverage, at)
	}
	if len(s.Ranked) == 0 {
		return ceilingErr
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
	return errors.Join(err, ceilingErr)
}
