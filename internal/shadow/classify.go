package shadow

import (
	"cmp"
	"fmt"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/profile"
)

// Buckets of the diff report.
const (
	ReasonPendingMetadata = "metadata_pending"
	BucketAgree           = "agree"
	BucketAnother         = "we_grab_another"
	BucketWouldnt         = "arr_grabbed_we_wouldnt"
	BucketRSSOnly         = "we_grab_arr_didnt"
	agreementWindow       = 14 * 24 * time.Hour
	AgreementBar          = 0.95
	agreementWinName      = "14d"
)

// Buckets lists the buckets in report order.
var Buckets = []string{BucketAgree, BucketAnother, BucketWouldnt, BucketRSSOnly}

// Reason categories. Agree: same_release, same_quality_score, accepted_not_searched, same_rss_pick, rss_accepted,
// manual_grab_accepted, parser_mode (a difference the version's Sonarr parser mode explains, parsermode.go). We'd grab another: newer_release, better_quality, higher_score. Arr grabbed, we wouldn't:
// manual_grab_rejected, else the first rejection's category below. We'd grab, arr didn't: arr_no_grab.
const (
	ReasonSameRelease      = "same_release"
	ReasonSameQualityScore = "same_quality_score"
	ReasonNotSearched      = "accepted_not_searched"
	ReasonSameRSSPick      = "same_rss_pick"
	ReasonRSSAccepted      = "rss_accepted"
	ReasonManualAccepted   = "manual_grab_accepted"
	ReasonManualRejected   = "manual_grab_rejected"
	ReasonNewerRelease     = "newer_release"
	ReasonBetterQuality    = "better_quality"
	ReasonHigherScore      = "higher_score"
	ReasonSeaDexPreference = "preferred_seadex"
	ReasonNoArrGrab        = "arr_no_grab"
)

// Decided is the stored summary of one decision: what the report shows, not the whole decision.
type Decided struct {
	Release   string     `json:"release"`
	Indexer   string     `json:"indexer,omitempty"`
	InfoHash  string     `json:"info_hash,omitempty"`
	Size      int64      `json:"size,omitempty"`
	Seeders   int        `json:"seeders"`
	Published *time.Time `json:"published,omitempty"`
	// Arr marks the arr's own release.
	Arr bool `json:"arr,omitempty"`

	Accepted         bool                 `json:"accepted"`
	Rejections       []decide.Rejection   `json:"rejections,omitempty"`
	Quality          string               `json:"quality"`
	Revision         int                  `json:"revision,omitempty"`
	Formats          []decide.FormatScore `json:"formats,omitempty"`
	Score            int                  `json:"score"`
	SeaDexPreference int                  `json:"seadex_preference,omitempty"`
	Languages        []string             `json:"languages,omitempty"`
	Existing         *decide.Existing     `json:"existing,omitempty"`
	Upgrade          bool                 `json:"upgrade"`
	UpgradeWhy       string               `json:"upgrade_reason,omitempty"`
	// Parser is the release decided again with the arr's parser, for a series version on another Sonarr parser
	// mode (parsermode.go); on the arr's decision it also holds our search's pick with that parser.
	Parser *ParserCheck `json:"parser,omitempty"`
}

// summarize turns a decision and its release into a Decided.
func summarize(d *decide.Decision, rel decide.Release, arr bool) Decided {
	s := Decided{Release: rel.Title, Indexer: rel.Indexer, InfoHash: strings.ToLower(rel.InfoHash), Size: rel.Size,
		Seeders: rel.Seeders, Arr: arr, Accepted: d.Accepted, Rejections: d.Rejections, Quality: d.Quality,
		SeaDexPreference: d.SeaDexPreference(), Revision: d.Revision.Version, Formats: d.Formats, Score: d.Score, Languages: d.Languages,
		Existing: d.Existing, Upgrade: d.Upgrade, UpgradeWhy: d.UpgradeReason}
	if !rel.PublishDate.IsZero() {
		t := rel.PublishDate.UTC()
		s.Published = &t
	}
	return s
}

// withoutOurs drops our own rule (the version's indexer list) from a decision of the arr's release: the arr's
// release carries the arr's indexer name, not a policy key, and the arr has no such rules.
func withoutOurs(d *decide.Decision) {
	var keep []decide.Rejection
	for _, r := range d.Rejections {
		if !r.Ours || r.Reason == "UnknownRuntime" || r.Reason == "SeaDexQualityPending" {
			keep = append(keep, r)
		}
	}
	d.Rejections = keep
	d.Accepted = len(keep) == 0
}

// sameRelease is the same torrent (hash) or the same release title.
func sameRelease(a, b *Decided) bool {
	if a.InfoHash != "" && b.InfoHash != "" {
		return a.InfoHash == b.InfoHash
	}
	return normTitle(a.Release) == normTitle(b.Release)
}

func normTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, ext := range []string{".mkv", ".mp4", ".torrent"} {
		s = strings.TrimSuffix(s, ext)
	}
	return strings.NewReplacer(" ", ".", "_", ".").Replace(s)
}

// Outcome is a diff row's classification.
type Outcome struct {
	Bucket string
	Reason string
	Detail string
}

// Release sources of an arr grab (the history's releaseSource, the same in Radarr and Sonarr).
const (
	SourceRSS         = "Rss"
	SourceInteractive = "InteractiveSearch"
)

// RSSWindow is how far around an RSS grab our RSS picks count as the same read: the arr reads its feeds every 15
// minutes by default and we read ours every 30, so the same item reaches both within the hour either way.
const RSSWindow = time.Hour

// Reasons and the basis of comparison rows. Comparison imports (import.arr role compare) that wrote them were
// removed; reports still read the rows a database holds.
const (
	BasisLive               = "live"
	ReasonSameQualitySource = "same_quality_source"
	ReasonQualitySource     = "quality_source_difference"
	ReasonArrOnly           = "arr_only_missed"
	ReasonArrOnlyUnknown    = "arr_only_unknown"
	ReasonTheatarrOnly      = "theatarr_only"
)

// Basis says what an arr grab is compared against.
const (
	BasisSearch = "search" // our search (Search, UserInvokedSearch, ReleasePush and anything else)
	BasisRSS    = "rss"    // our RSS picks within RSSWindow of the grab
	BasisManual = "manual" // the arr's release alone: it was picked by hand
)

// BasisFor is the comparison basis of a release source.
func BasisFor(source string) string {
	switch {
	case strings.EqualFold(source, SourceRSS):
		return BasisRSS
	case strings.EqualFold(source, SourceInteractive):
		return BasisManual
	}
	return BasisSearch
}

// RSSCandidate is one of our RSS picks for the grab's title and version, seen within RSSWindow of the grab.
type RSSCandidate struct {
	ID      int64
	Indexer string
	SeenAt  time.Time
	Decided Decided
}

// RankFunc orders two accepted releases of one version by what a summary keeps, as the arr's comparer starts:
// quality in the profile's order, then revision, then format score. Above zero when a ranks above b.
type RankFunc func(a, b *Decided) int

// GrabInput is everything an arr grab's classification reads; all of it is stored, so a grab can be classified
// again without a search.
type GrabInput struct {
	Source    string
	GrabbedAt time.Time
	// Arr is our decision of the arr's release; Pick our search's pick ranked with it (nil when nothing was
	// acceptable); Searched whether the search ran; FoundArr whether it returned the arr's release.
	Arr      Decided
	Pick     *Decided
	Searched bool
	FoundArr bool
	// RSS are our RSS picks near the grab (RSS grabs only).
	RSS  []RSSCandidate
	Rank RankFunc // nil: same quality only, then revision and score
}

// Classification is an arr grab's diff row: the outcome, what it was compared against, our release for the row,
// the RSS pick it rests on, and (for RSS and manual grabs) our search's pick when it ranks above ours.
type Classification struct {
	Outcome
	Basis        string
	Ours         *Decided
	RSSPick      *RSSCandidate
	SearchBetter *Decided
}

// ClassifyGrab buckets one arr grab by its release source:
//
//   - RSS: against our RSS picks within RSSWindow of the grab. A pick that ranks above the arr's release is
//     we_grab_another (same quality and score: agree); else our pick of the same release is same_rss_pick; else, when
//     we accept the arr's release, rss_accepted; else the rejection's category. Our later search is information only.
//   - InteractiveSearch: the arr's release alone; accepted is manual_grab_accepted, rejected manual_grab_rejected.
//   - anything else: our search against the arr's release (Classify).
//
// A disagreement the version's Sonarr parser mode explains becomes agree / parser_mode (withParserCheck).
func ClassifyGrab(in GrabInput) Classification {
	if reason := pendingReason(in.Arr); reason != "" {
		return Classification{Outcome: Outcome{BucketWouldnt, ReasonPendingMetadata, "Waiting for runtime or quality metadata; excluded from agreement"}, Basis: BasisFor(in.Source)}
	}
	return withParserCheck(classifyGrab(in), in, classifyGrab)
}

func classifyGrab(in GrabInput) Classification {
	rank := in.Rank
	if rank == nil {
		rank = plainRank
	}
	switch BasisFor(in.Source) {
	case BasisRSS:
		return classifyRSS(in, rank)
	case BasisManual:
		return classifyManual(in, rank)
	}
	return Classification{Outcome: Classify(in.Arr, in.Pick, in.GrabbedAt, in.Searched, in.FoundArr), Basis: BasisSearch,
		Ours: in.Pick}
}

func describe(d *Decided) string { return fmt.Sprintf("%s, %s %d", d.Release, d.Quality, d.Score) }

func sameQualityScore(a, b *Decided) bool {
	return a.SeaDexPreference == b.SeaDexPreference && a.Quality == b.Quality && a.Score == b.Score
}

// searchBetter is our search's pick when it ranks above ours (nil ours: whenever it is not the arr's release).
func searchBetter(in GrabInput, ours *Decided, rank RankFunc) *Decided {
	p := in.Pick
	if p == nil || p.Arr || sameRelease(p, &in.Arr) {
		return nil
	}
	if ours != nil && (sameRelease(p, ours) || rank(p, ours) <= 0) {
		return nil
	}
	return p
}

func withSearchBetter(c Classification, in GrabInput, rank RankFunc) Classification {
	c.SearchBetter = searchBetter(in, c.Ours, rank)
	if c.SearchBetter != nil {
		c.Detail += "; our search (run later) found " + describe(c.SearchBetter) + ", kept as information"
	}
	return c
}

func classifyRSS(in GrabInput, rank RankFunc) Classification {
	arr := &in.Arr
	var best, same *RSSCandidate
	for i := range in.RSS {
		c := &in.RSS[i]
		if same == nil && sameRelease(&c.Decided, arr) {
			same = c
		}
		if best == nil || rank(&c.Decided, &best.Decided) > 0 {
			best = c
		}
	}
	window := fmt.Sprintf("within %s of the grab", RSSWindow)
	// The release to beat: the arr's when we accept it, else our RSS decision of it.
	var ref *Decided
	switch {
	case arr.Accepted:
		ref = arr
	case same != nil:
		ref = &same.Decided
	}
	c := Classification{Basis: BasisRSS}
	switch {
	case ref != nil && best != nil && !sameRelease(&best.Decided, ref) && rank(&best.Decided, ref) > 0:
		b := &best.Decided
		c.Ours, c.RSSPick = b, best
		if sameQualityScore(b, ref) {
			c.Outcome = Outcome{BucketAgree, ReasonSameQualityScore, fmt.Sprintf("an RSS grab; our RSS read of %s %s picked %s: the same quality and score as the arr's release",
				best.Indexer, window, b.Release)}
			break
		}
		reason := ReasonHigherScore
		if b.SeaDexPreference > ref.SeaDexPreference {
			reason = ReasonSeaDexPreference
		} else if b.Quality != ref.Quality {
			reason = ReasonBetterQuality
		}
		c.Outcome = Outcome{BucketAnother, reason, fmt.Sprintf("an RSS grab; our RSS read of %s %s would grab %s over the arr's %s",
			best.Indexer, window, describe(b), describe(ref))}
	case same != nil:
		c.Ours, c.RSSPick = &same.Decided, same
		d := fmt.Sprintf("an RSS grab; our RSS read of %s picked the same release at %s", same.Indexer,
			same.SeenAt.UTC().Format(time.RFC3339))
		if !arr.Accepted {
			r := arr.Rejections[0]
			d += fmt.Sprintf(" (decided again from the arr's history, we reject it: [%s] %s)", r.Reason, r.Message)
		}
		c.Outcome = Outcome{BucketAgree, ReasonSameRSSPick, d}
	case arr.Accepted:
		a := *arr
		c.Ours = &a
		d := "an RSS grab; we accept the arr's release and our RSS reads " + window + " picked nothing better"
		if best != nil {
			c.RSSPick = best
			d += fmt.Sprintf(" (our pick there: %s)", describe(&best.Decided))
		} else {
			d += " (no RSS pick of ours for this version)"
		}
		c.Outcome = Outcome{BucketAgree, ReasonRSSAccepted, d}
	default:
		r := arr.Rejections[0]
		d := fmt.Sprintf("an RSS grab; we reject the arr's release (%s %d): [%s] %s", arr.Quality, arr.Score, r.Reason, r.Message)
		if best != nil {
			c.Ours, c.RSSPick = &best.Decided, best
			d += fmt.Sprintf("; our RSS read of %s %s would grab %s", best.Indexer, window, describe(&best.Decided))
		} else {
			d += "; our RSS reads " + window + " picked nothing for this version"
		}
		c.Outcome = Outcome{BucketWouldnt, RejectionCategory(r.Reason), d}
	}
	return withSearchBetter(c, in, rank)
}

func classifyManual(in GrabInput, rank RankFunc) Classification {
	arr := &in.Arr
	c := Classification{Basis: BasisManual}
	if !arr.Accepted {
		r := arr.Rejections[0]
		c.Outcome = Outcome{BucketWouldnt, ReasonManualRejected, fmt.Sprintf(
			"the arr grabbed it by hand; we reject it (%s %d): [%s] %s (category %s)", arr.Quality, arr.Score, r.Reason,
			r.Message, RejectionCategory(r.Reason))}
		if in.Pick != nil {
			c.Ours = in.Pick
			c.Detail += "; our search would grab " + describe(in.Pick)
		}
		return c
	}
	a := *arr
	c.Ours = &a
	c.Outcome = Outcome{BucketAgree, ReasonManualAccepted, fmt.Sprintf("the arr grabbed it by hand; we accept it (%s %d)",
		arr.Quality, arr.Score)}
	return withSearchBetter(c, in, rank)
}

// Classify buckets an arr grab against our search: arr is our decision of the arr's release, pick our best release
// (the arr's own when nothing ranks above it; nil when nothing was acceptable), searched whether our search ran.
func Classify(arr Decided, pick *Decided, grabbedAt time.Time, searched, foundArr bool) Outcome {
	if reason := pendingReason(arr); reason != "" {
		return Outcome{BucketWouldnt, ReasonPendingMetadata, "Waiting for metadata; excluded from agreement"}
	}
	if !arr.Accepted {
		r := arr.Rejections[0]
		var b strings.Builder
		fmt.Fprintf(&b, "we reject the arr's release (%s %d): [%s] %s", arr.Quality, arr.Score, r.Reason, r.Message)
		if pick != nil {
			fmt.Fprintf(&b, "; we would grab %s", describe(pick))
		} else if searched {
			b.WriteString("; our search found nothing we would grab")
		}
		return Outcome{BucketWouldnt, RejectionCategory(r.Reason), b.String()}
	}
	if !searched {
		return Outcome{BucketAgree, ReasonNotSearched, "accepted; not searched, so only the arr's release was decided"}
	}
	if pick == nil || pick.Arr || sameRelease(pick, &arr) {
		d := "we pick the same release"
		if !foundArr {
			d += " (our search did not return it; decided from the arr's history)"
		}
		return Outcome{BucketAgree, ReasonSameRelease, d}
	}
	if sameQualityScore(pick, &arr) {
		return Outcome{BucketAgree, ReasonSameQualityScore,
			fmt.Sprintf("we pick %s: the same quality and score as the arr's release", pick.Release)}
	}
	detail := fmt.Sprintf("we would grab %s over the arr's %s", describe(pick), describe(&arr))
	if !foundArr {
		detail += "; our search did not return the arr's release"
	}
	switch {
	case pick.Published != nil && pick.Published.After(grabbedAt):
		return Outcome{BucketAnother, ReasonNewerRelease, detail + fmt.Sprintf("; ours was published %s, after the grab",
			pick.Published.Format(time.RFC3339))}
	case pick.SeaDexPreference > arr.SeaDexPreference:
		return Outcome{BucketAnother, ReasonSeaDexPreference, detail}
	case pick.Quality != arr.Quality:
		return Outcome{BucketAnother, ReasonBetterQuality, detail}
	}
	return Outcome{BucketAnother, ReasonHigherScore, detail}
}

// plainRank ranks without a profile: only releases of the same quality compare, by revision then score.
func plainRank(a, b *Decided) int {
	if a.SeaDexPreference != b.SeaDexPreference {
		return cmp.Compare(a.SeaDexPreference, b.SeaDexPreference)
	}
	if a.Quality != b.Quality {
		return 0
	}
	return cmp.Or(cmp.Compare(a.Revision, b.Revision), cmp.Compare(a.Score, b.Score))
}

// ProfileRank ranks by p's quality order (an item, or a group as one step, as the arr's comparer does), then
// revision, then score. A quality p does not list ranks lowest, as in the arr.
func ProfileRank(p *profile.Profile, preferSeaDex ...bool) RankFunc {
	if p == nil {
		return plainRank
	}
	index := func(name string) int {
		for i, it := range p.Items {
			if it.Quality != nil && strings.EqualFold(it.Quality.Name, name) {
				return i
			}
			for _, s := range it.Items {
				if s.Quality != nil && strings.EqualFold(s.Quality.Name, name) {
					return i
				}
			}
		}
		return 0
	}
	return func(a, b *Decided) int {
		pref := 0
		if len(preferSeaDex) > 0 && preferSeaDex[0] {
			pref = cmp.Compare(a.SeaDexPreference, b.SeaDexPreference)
		}
		return cmp.Or(pref, cmp.Compare(index(a.Quality), index(b.Quality)), cmp.Compare(a.Revision, b.Revision),
			cmp.Compare(a.Score, b.Score))
	}
}

// RejectionCategory maps a rejection reason to the report's category.
func RejectionCategory(reason string) string {
	switch {
	case strings.HasPrefix(reason, "Disk") || reason == "QualityUpgradesDisabled":
		return "not_an_upgrade"
	case reason == "QualityNotWanted":
		return "quality_not_wanted"
	case reason == "CustomFormatMinimumScore":
		return "below_min_score"
	case reason == "BelowMinimumSize" || reason == "AboveMaximumSize" || reason == "MaximumSizeExceeded" ||
		reason == "UnknownRuntime":
		return "size"
	case reason == "WantedLanguage":
		return "language"
	case reason == "UnknownMovie" || reason == "WrongMovie":
		return "unmapped_title"
	case reason == "UnableToParse":
		return "unparseable"
	case strings.HasPrefix(reason, "Repack") || strings.HasSuffix(reason, "ReleaseGroup") ||
		strings.HasSuffix(reason, "ReleaseGroupDoesNotMatch"):
		return "repack"
	}
	return "other"
}

// Categories lists every reason category per bucket (the API's filter values).
var Categories = map[string][]string{

	BucketAgree: {ReasonSameRelease, ReasonSameQualityScore, ReasonNotSearched, ReasonSameRSSPick, ReasonRSSAccepted,
		ReasonManualAccepted, ReasonParserMode, ReasonSameQualitySource},
	BucketAnother: {ReasonSeaDexPreference, ReasonNewerRelease, ReasonBetterQuality, ReasonHigherScore, ReasonQualitySource},
	BucketWouldnt: {ReasonPendingMetadata, ReasonManualRejected, ReasonArrOnly, ReasonArrOnlyUnknown, "not_an_upgrade", "quality_not_wanted", "below_min_score", "size", "language", "unmapped_title",
		"unparseable", "repack", "other"},
	BucketRSSOnly: {ReasonNoArrGrab, ReasonTheatarrOnly},
}

func pendingReason(d Decided) string {
	reason := ""
	for _, r := range d.Rejections {
		if r.Reason == "UnknownRuntime" || r.Reason == "SeaDexQualityPending" {
			reason = r.Reason
			break
		}
	}
	if reason == "" {
		return ""
	}
	for _, r := range d.Rejections {
		if r.Reason != "UnknownRuntime" && r.Reason != "SeaDexQualityPending" && !(hasQualityPending(d) && r.Reason == "CustomFormatMinimumScore") {
			return ""
		}
	}
	return reason
}

func hasQualityPending(d Decided) bool {
	for _, r := range d.Rejections {
		if r.Reason == "SeaDexQualityPending" {
			return true
		}
	}
	return false
}

// IsMetadataPending identifies rows excluded from agreement and minimum samples.
func IsMetadataPending(reason string) bool { return reason == ReasonPendingMetadata }
