package shadow

import (
	"context"
	"fmt"
	"slices"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/parser/sonarr"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
)

// Parser mode. A shadowed Sonarr v4 parses as v4.0.20 does, so a series version on another parser mode
// (versions.<k>.sonarr: v4plus, or v5) will sometimes pick differently because it parses differently. Such a grab is
// decided a second time with Sonarr v4's parser (the arr's release, our search's results, our RSS picks), and kept on
// the arr's stored decision as a ParserCheck. When the grab's row is a disagreement but the same grab classified
// with the v4 decisions agrees, the difference is the parser mode's: the row is agree / parser_mode, its detail
// starting "parser: <mode>". Classify, reclassify and the report need nothing else: the check lives in the stored
// JSON (no migration). Known limit: an RSS item the mode rejects but v4 would accept is not stored as a pick, so the
// v4 side of an RSS grab only re-decides the picks the mode made. docs/design/sonarr-v5.md "Shadow".

// ReasonParserMode is an agree row whose difference the version's Sonarr parser mode explains.
const ReasonParserMode = "parser_mode"

// ParserCheck is a release decided again with the arr's parser (Sonarr v4) for a version on another parser mode.
type ParserCheck struct {
	// Mode is the version's parser mode (v4plus, v5).
	Mode string `json:"mode"`
	// Decided is this release decided with v4.
	Decided *Decided `json:"decided"`
	// Pick, on the arr's decision only, is our search's pick with v4 (the arr's own release when nothing ranks
	// above it; nil when nothing was acceptable or no search ran).
	Pick *Decided `json:"pick,omitempty"`
}

// arrParserVersion is v with the arr's parser (Sonarr v4), and the version's own mode; ok is false when v already
// parses as the arr does or is not a series version.
func arrParserVersion(v decide.Version) (v4 decide.Version, mode string, ok bool) {
	if v.Policy == nil || v.Policy.Kind != policy.KindSeries || v.Policy.Sonarr == "" ||
		v.Policy.Sonarr == policy.SonarrV4 {
		return v, "", false
	}
	p := *v.Policy
	p.Sonarr = sonarr.VersionNameV4
	v4 = v
	v4.Policy = &p
	return v4, v.Policy.Sonarr, true
}

// seriesParserCheck decides a series grab again with v4: the arr's release (decidedRel, as grabSeries decided it;
// arrRel is how it is summarized) with the arr's search, and our search's results (releases, already filtered to the
// version's indexers) with ours, ranked as grabSeries ranks them. nil when the version parses as the arr does.
func (r *Runner) seriesParserCheck(ctx context.Context, v decide.Version, t decide.Title, decidedRel, arrRel decide.Release,
	arrSearch, ours *decide.SeriesSearch, releases []indexer.Release, searched bool) (*ParserCheck, error) {
	v4, mode, ok := arrParserVersion(v)
	if !ok {
		return nil, nil
	}
	arrD, err := r.eng.DecideSearch(ctx, decidedRel, t, v4, arrSearch)
	if err != nil {
		return nil, err
	}
	withoutOurs(arrD)
	arr := summarize(arrD, arrRel, true)
	pc := &ParserCheck{Mode: mode, Decided: &arr}
	if !searched {
		return pc, nil
	}
	var cands []*decide.Decision
	rels := map[*decide.Decision]indexer.Release{}
	for _, rel := range releases {
		d, err := r.eng.DecideSearch(ctx, decide.Release{Release: rel}, t, v4, ours)
		if err != nil {
			return nil, err
		}
		cands = append(cands, d)
		rels[d] = rel
	}
	if arrD.Accepted {
		cands = append(slices.Clone(cands), arrD)
	}
	for _, d := range decide.Rank(cands) {
		if !d.Accepted {
			continue
		}
		if d == arrD {
			a := arr
			pc.Pick = &a
		} else {
			s := summarize(d, decide.Release{Release: rels[d]}, false)
			pc.Pick = &s
		}
		break
	}
	return pc, nil
}

// rssParserCheck decides an RSS item again with v4 for a version on another parser mode; nil otherwise.
func (r *Runner) rssParserCheck(ctx context.Context, rel indexer.Release, t decide.Title, v decide.Version) (*ParserCheck, error) {
	v4, mode, ok := arrParserVersion(v)
	if !ok {
		return nil, nil
	}
	d, err := r.eng.Decide(ctx, decide.Release{Release: rel}, t, v4, nil)
	if err != nil {
		return nil, err
	}
	s := summarize(d, decide.Release{Release: rel}, false)
	return &ParserCheck{Mode: mode, Decided: &s}, nil
}

// withParserCheck turns a disagreement into agree / parser_mode when the grab, classified with the v4 decisions of
// its ParserCheck, agrees. Hand grabs keep their row (they are outside the agreement ratio).
func withParserCheck(c Classification, in GrabInput, classify func(GrabInput) Classification) Classification {
	pc := in.Arr.Parser
	if pc == nil || pc.Decided == nil || c.Bucket == BucketAgree || c.Basis == BasisManual {
		return c
	}
	in4 := in
	in4.Arr = *pc.Decided
	in4.Arr.Parser = nil
	in4.Pick = pc.Pick
	in4.RSS = nil
	for _, x := range in.RSS {
		if p := x.Decided.Parser; p != nil && p.Decided != nil {
			if !p.Decided.Accepted {
				continue // not a pick with v4
			}
			x.Decided = *p.Decided
		}
		in4.RSS = append(in4.RSS, x)
	}
	c4 := classify(in4)
	if c4.Bucket != BucketAgree {
		c.Detail += fmt.Sprintf("; with Sonarr v4's parser (as the arr) it is still %s/%s, so not the parser mode (%s)",
			c4.Bucket, c4.Reason, pc.Mode)
		return c
	}
	c.Detail = fmt.Sprintf("parser: %s; %s/%s: %s; with Sonarr v4's parser, as the arr parses, %s/%s: %s", pc.Mode,
		c.Bucket, c.Reason, c.Detail, c4.Bucket, c4.Reason, c4.Detail)
	c.Outcome.Bucket, c.Outcome.Reason = BucketAgree, ReasonParserMode
	return c
}
