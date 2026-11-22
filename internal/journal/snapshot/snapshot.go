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
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/profile"
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
	Kept       []string         `json:"kept,omitempty"`
	Existing   *decide.Existing `json:"existing,omitempty"`
	Upgrade    bool             `json:"upgrade,omitempty"`
	UpgradeWhy string           `json:"upgrade_reason,omitempty"`
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

// Over is the comparer's own first difference between an accepted release and the next accepted torrent after it
// (decide.FirstDifference), with what the words need of that next one.
type Over struct {
	Release string `json:"release"`
	// Step is the comparer's step (profile.Step*: quality, score, cached, peers, ...).
	Step     string   `json:"step"`
	Quality  string   `json:"quality,omitempty"`
	Revision int      `json:"revision,omitempty"`
	Score    int      `json:"score"`
	Formats  []string `json:"formats,omitempty"`
	CachedOn []string `json:"cached_on,omitempty"`
	Seeders  int      `json:"seeders,omitempty"`
	Size     int64    `json:"size,omitempty"`
	Episodes int      `json:"episodes,omitempty"`
}

// firstDifference is decide.FirstDifference (a test replaces it: its decisions have no engine).
var firstDifference = decide.FirstDifference

// over is why d ranks before the next accepted torrent after it in ranked (from index i), nil when none is after it
// or the comparer ties them.
func over(ranked []*decide.Decision, i int, rels map[*decide.Decision]indexer.Release) *Over {
	d := ranked[i]
	hash := strings.ToLower(rels[d].InfoHash)
	for _, n := range ranked[i+1:] {
		if n == nil || !n.Accepted || n.Version != d.Version {
			continue
		}
		r := rels[n]
		if hash != "" && strings.EqualFold(r.InfoHash, hash) {
			continue // the same torrent from another indexer
		}
		if len(d.Episodes) > 0 && len(n.Episodes) > 0 && !slices.ContainsFunc(n.Episodes, func(e string) bool {
			return slices.Contains(d.Episodes, e)
		}) {
			continue // another episode's release in a season's search: not an alternative to this one
		}
		step := firstDifference(d, n)
		if step == "" {
			return nil
		}
		o := &Over{Release: n.Release, Step: step, Quality: n.Quality, Revision: n.Revision.Version, Score: n.Score,
			CachedOn: r.CachedList(), Seeders: r.Seeders, Size: r.Size, Episodes: len(n.Episodes)}
		if o.Release == "" {
			o.Release = r.Title
		}
		for _, f := range n.Formats {
			o.Formats = append(o.Formats, f.Name)
		}
		return o
	}
	return nil
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
				c.Over = over(ranked, i, rels)
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
		Kept: d.Kept, Existing: d.Existing, Upgrade: d.Upgrade, UpgradeWhy: d.UpgradeReason, Source: rel.Kind,
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

// ChosenOver says why c ranks before the next accepted torrent: "Chosen over <release>: higher quality (Remux-2160p
// over WEBDL-2160p)", "…: higher score (+450: DV, TrueHD Atmos)", "…: cached on Real-Debrid", "…: more seeders (120
// over 30)"; "" when nothing was ranked after it (a run with one candidate, or one stored before it was recorded).
func ChosenOver(c Candidate) string {
	o := c.Over
	if o == nil {
		return ""
	}
	return "Chosen over " + o.Release + ": " + overWords(c, o)
}

func overWords(c Candidate, o *Over) string {
	switch o.Step {
	case profile.StepSeaDex:
		return "SeaDex ranks it higher"
	case profile.StepQuality:
		if c.Quality == o.Quality && c.Revision != o.Revision {
			return fmt.Sprintf("a newer revision (v%d over v%d)", c.Revision, o.Revision)
		}
		return "higher quality (" + c.Quality + " over " + o.Quality + ")"
	case profile.StepScore:
		var more []string
		for _, f := range c.Formats {
			if f.Score > 0 && !slices.Contains(o.Formats, f.Name) {
				more = append(more, f.Name)
			}
		}
		s := fmt.Sprintf("higher score (+%d", c.Score-o.Score)
		if len(more) > 0 {
			s += ": " + strings.Join(more, ", ")
		}
		return s + ")"
	case profile.StepCached:
		if len(c.CachedOn) == 0 {
			return "cached"
		}
		names := make([]string, len(c.CachedOn))
		for i, code := range c.CachedOn {
			names[i] = providerName(code)
		}
		if len(o.CachedOn) > 0 && len(c.CachedOn) > len(o.CachedOn) {
			return "cached on more providers (" + strings.Join(names, ", ") + ")"
		}
		return "cached on " + names[0]
	case profile.StepProtocol:
		return "the preferred protocol"
	case profile.StepEpisodeCount:
		if n := len(c.Episodes); n != o.Episodes {
			if n > o.Episodes {
				return fmt.Sprintf("more episodes (%d over %d)", n, o.Episodes)
			}
			return fmt.Sprintf("fewer episodes (%d over %d)", n, o.Episodes)
		}
		return "a season pack"
	case profile.StepEpisodeNumber:
		return "an earlier first episode"
	case profile.StepIndexerPriority:
		return "a higher indexer priority"
	case profile.StepIndexerFlags:
		return "better indexer flags"
	case profile.StepPeers:
		if c.Seeders != o.Seeders {
			return fmt.Sprintf("more seeders (%d over %d)", c.Seeders, o.Seeders)
		}
		return "more peers"
	case profile.StepAge:
		return "published more recently"
	case profile.StepSize:
		return "a size closer to the profile's"
	}
	return "ranked higher"
}

// providerName names a cache code (rd, ad, tb) for people.
func providerName(code string) string {
	switch strings.ToLower(code) {
	case "rd":
		return "Real-Debrid"
	case "ad":
		return "AllDebrid"
	case "tb":
		return "TorBox"
	}
	return strings.ToUpper(code)
}
