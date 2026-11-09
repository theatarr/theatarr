package profile

// Port of Radarr src/NzbDrone.Core/DecisionEngine/{DownloadSpecDecision,DownloadRejection,DownloadRejectionReason,
// RejectionType,SpecificationPriority,DownloadDecisionMaker.GetDecisionForReport}.cs @ a96bf7c and the Sonarr files
// @ cab419a. The subject (RemoteMovie / RemoteEpisode) is reduced to what the ported specifications read.

import (
	"fmt"
	"sort"
	"time"

	"github.com/theatarr/theatarr/internal/decide/format"
)

// Reason is a DownloadRejectionReason, by its arr name. Both arrs number the enum differently; the name is stable.
type Reason string

// The reasons the ported specifications return.
const (
	ReasonDecisionError                  Reason = "DecisionError"
	ReasonUnknownRuntime                 Reason = "UnknownRuntime"
	ReasonBelowMinimumSize               Reason = "BelowMinimumSize"
	ReasonAboveMaximumSize               Reason = "AboveMaximumSize"
	ReasonCustomFormatMinimumScore       Reason = "CustomFormatMinimumScore"
	ReasonWantedLanguage                 Reason = "WantedLanguage"
	ReasonMaximumSizeExceeded            Reason = "MaximumSizeExceeded"
	ReasonSample                         Reason = "Sample"
	ReasonQualityNotWanted               Reason = "QualityNotWanted"
	ReasonQualityUpgradesDisabled        Reason = "QualityUpgradesDisabled"
	ReasonRaw                            Reason = "Raw"
	ReasonRepackDisabled                 Reason = "RepackDisabled"
	ReasonRepackUnknownReleaseGroup      Reason = "RepackUnknownReleaseGroup"
	ReasonRepackReleaseGroupDoesNotMatch Reason = "RepackReleaseGroupDoesNotMatch"
	ReasonUnknownReleaseGroup            Reason = "UnknownReleaseGroup"      // Sonarr
	ReasonReleaseGroupDoesNotMatch       Reason = "ReleaseGroupDoesNotMatch" // Sonarr
	ReasonDiskHigherPreference           Reason = "DiskHigherPreference"
	ReasonDiskHigherRevision             Reason = "DiskHigherRevision"
	ReasonDiskCutoffMet                  Reason = "DiskCutoffMet"
	ReasonDiskCustomFormatCutoffMet      Reason = "DiskCustomFormatCutoffMet"
	ReasonDiskCustomFormatScore          Reason = "DiskCustomFormatScore"
	ReasonDiskCustomFormatScoreIncrement Reason = "DiskCustomFormatScoreIncrement"
	ReasonDiskUpgradesNotAllowed         Reason = "DiskUpgradesNotAllowed"
	ReasonDiskNotUpgrade                 Reason = "DiskNotUpgrade" // Sonarr v5 SeasonPackUpgrade (packs: threshold)
)

// Decision is a DownloadSpecDecision: accepted, or rejected with a reason and the arr's message text.
type Decision struct {
	Accepted bool
	Reason   Reason
	Message  string
}

// Accept is DownloadSpecDecision.Accept.
func Accept() Decision { return Decision{Accepted: true} }

// Reject is DownloadSpecDecision.Reject(reason, message, args): string.Format of the message.
func Reject(reason Reason, msg string, args ...any) Decision {
	if len(args) > 0 {
		msg = netFormat(msg, args...)
	}
	return Decision{Reason: reason, Message: msg}
}

// RejectionType is Permanent or Temporary.
type RejectionType int

const (
	Permanent RejectionType = iota
	Temporary
)

// Priority is SpecificationPriority: Default, Parsing and Database are all 0; Disk is 1.
type Priority int

const (
	PriorityDefault  Priority = 0
	PriorityDatabase Priority = 0
	PriorityDisk     Priority = 1
)

// Rejection is a DownloadRejection: one failed specification.
type Rejection struct {
	Spec    string
	Reason  Reason
	Message string
	Type    RejectionType
}

func (r Rejection) String() string { return "[" + string(r.Reason) + "] " + r.Message }

// ProperDownloadTypes is the arrs' "Propers and Repacks" setting.
type ProperDownloadTypes int

const (
	PreferAndUpgrade ProperDownloadTypes = iota
	DoNotUpgrade
	DoNotPrefer
)

var properNames = []string{"preferAndUpgrade", "doNotUpgrade", "doNotPrefer"}

func (p ProperDownloadTypes) String() string {
	if int(p) < len(properNames) {
		return properNames[p]
	}
	return fmt.Sprint(int(p))
}

// ParseProperDownloadTypes reads the setting by name, ignoring case.
func ParseProperDownloadTypes(s string) (ProperDownloadTypes, error) {
	for i, n := range properNames {
		if equalFold(n, s) {
			return ProperDownloadTypes(i), nil
		}
	}
	return 0, fmt.Errorf("unknown propers setting %q", s)
}

// Protocol is DownloadProtocol.
type Protocol int

const (
	ProtocolUnknown Protocol = 0
	ProtocolUsenet  Protocol = 1
	ProtocolTorrent Protocol = 2
)

// Config holds the arr settings the ported specifications and the comparer read.
type Config struct {
	// PreferSeaDex is a native opt-in: curator standing precedes quality preference, without changing quality.
	PreferSeaDex bool
	// EpisodeTitleRequired is Sonarr's Always (default), Never or BulkSeasonReleases.
	EpisodeTitleRequired string
	// CompletedDownloadHandlingDisabled skips AlreadyImported when the arr has CDH off.
	CompletedDownloadHandlingDisabled bool
	// DownloadPropersAndRepacks is Settings > Media Management > Propers and Repacks.
	DownloadPropersAndRepacks ProperDownloadTypes
	// MaximumSize is Settings > Indexers > Maximum Size, in MB; 0 is unlimited.
	MaximumSize int
	// PreferIndexerFlags is Radarr's Settings > Indexers > Prefer Indexer Flags.
	PreferIndexerFlags bool
	// PreferredProtocol is the preferred protocol of the delay profile that applies to the title's tags
	// (DelayProfileService.BestForTags). The arr's default delay profile prefers Usenet.
	PreferredProtocol Protocol
	// AcceptReleaseAnyUpgradable is the actuallyevan Sonarr fork's ACCEPT_RELEASE_ANY_UPGRADABLE (Sonarr only):
	// UpgradeDisk accepts a release when any of its episodes has no file or a file it would upgrade, instead of
	// requiring every file to be upgraded.
	AcceptReleaseAnyUpgradable bool
	// SeasonPackUpgradeThreshold, when above 0 (and AcceptReleaseAnyUpgradable is off), is Sonarr v5's
	// SeasonPackUpgrade = Threshold with SeasonPackUpgradeThreshold, in percent (Sonarr only): a full-season release is
	// accepted when at least that share of its episodes has no file or a file it upgrades. Other releases keep the
	// all-files rule.
	SeasonPackUpgradeThreshold float64
}

// Engine evaluates releases for one flavour with its settings and quality definitions.
type Engine struct {
	Flavour     format.Flavour
	Config      Config
	Definitions *Definitions
	// Now is the clock for release ages and air dates; nil is time.Now.
	Now func() time.Time
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engine) definition(q Quality) *Definition {
	if e.Definitions == nil {
		return DefaultDefinitions(e.Flavour).Get(q)
	}
	return e.Definitions.Get(q)
}

// File is an existing file: Radarr's MovieFile or Sonarr's EpisodeFile, with the formats the arr computes for it
// (CustomFormatCalculationService.ParseCustomFormat(file)).
type File struct {
	Added        time.Time
	LastGrab     *ImportedGrab
	Quality      QualityModel
	ReleaseGroup string
	Formats      []*format.Format
	// EpisodeIDs are the episodes the file holds (Sonarr: GetEpisodesByFileId), for SameEpisodesGrab.
	EpisodeIDs []int64
}

// Episode is an episode the release maps to (Sonarr).
type Episode struct {
	Title         string
	LastGrab      *ImportedGrab
	ID            int64
	Monitored     bool
	SeasonNumber  int
	EpisodeNumber int
	Runtime       int // minutes, 0 unknown
	AirDateUtc    *time.Time
	// File is the episode's current file, nil when it has none.
	File *File
}

// Media is the movie or series the release maps to.
type Media struct {
	// Episodes is the whole series, used by EpisodeTitleRequired = BulkSeasonReleases.
	Episodes []*Episode
	// EpisodeTitleRequired is whether the selected naming pattern uses an episode title.
	EpisodeTitleRequired bool
	Title                string
	// Runtime is the movie's or the series' runtime in minutes. Radarr's AcceptableSize sets it to 110 when it is 0,
	// and that sticks to the movie for later checks, as in the arr.
	Runtime int
	// OriginalLanguage is the movie's original language (Radarr language id).
	OriginalLanguage int
	// Anime is Sonarr's SeriesType == Anime.
	Anime bool
	// MovieFile is Radarr's current file, nil when the movie has none.
	MovieFile *File
	// Pilot is the first episode of the lowest season above 0 (Sonarr AcceptableSize when the series runtime is 0).
	Pilot *Episode
	// Monitored is the series' monitored flag; SeriesType standard, daily or anime (Sonarr).
	Monitored  bool
	SeriesType string
}

// Release is the release under decision: ReleaseInfo plus the parsed fields the specifications read.
type Release struct {
	Indexer        string
	MinimumSeeders int
	InfoHash       string
	Title          string
	Size           int64 // bytes, 0 unknown
	Container      string
	Quality        QualityModel
	// ReleaseGroup is the parsed release group, "" when none.
	ReleaseGroup string
	// Languages are RemoteMovie.Languages (after augmentation), as language ids.
	Languages []int
	// Formats are the matched custom formats; FormatScore is their score in the subject's profile
	// (RemoteMovie.CustomFormatScore). Subject.Score fills FormatScore.
	Formats     []*format.Format
	FormatScore int

	Protocol        Protocol
	IndexerPriority int
	IndexerFlags    int
	Seeders, Peers  *int // torrents only
	PublishDate     time.Time

	// Sonarr parse facts.
	FullSeason     bool
	Special        bool
	SeasonNumber   int
	EpisodeNumbers []int
	MultiSeason    bool
	SplitEpisode   bool
	// V5 is a Sonarr v5 parse: the season search specifications match SeasonNumbers (every season of a pack), else
	// MappedSeasonNumber (nil: no season), instead of SeasonNumber.
	V5                 bool
	SeasonNumbers      []int
	MappedSeasonNumber *int
	// SeasonSearchMaximumSingleEpisodeAge is the indexer's setting (days, 0 off).
	SeasonSearchMaximumSingleEpisodeAge int
}

// ImportedGrab is the latest grab for one movie or episode and its matching import, if any.
type ImportedGrab struct {
	DownloadID      string
	SourceTitle     string
	GrabbedQuality  QualityModel
	ImportedQuality QualityModel
	Imported        bool
}

// Subject is a RemoteMovie or RemoteEpisode: a release mapped to a title, judged against a profile.
type Subject struct {
	Profile *Profile
	Media   *Media
	Release *Release
	// Episodes are the episodes the release maps to (Sonarr).
	Episodes []*Episode
	// Search is the search the release was found with, nil for RSS (Sonarr).
	Search *Search
	// SceneMapping is the scene mapping the release was mapped through, nil when none (Sonarr).
	SceneMapping *SceneMapping
	// Cached is Theatarr's cache standing of the release for the version (not an arr field), filled where the release
	// is decided; the comparer's tie-break after the format score reads it.
	Cached CacheStanding
}

// CacheStanding is where cache evidence says a release is held among the debrid providers its version may send it to
// (the version's providers as the release's indexer allows them, with a key).
type CacheStanding struct {
	// First is the version's list position (1 first) of the earliest such provider holding the release cached; 0
	// when none does, or when the version has no provider preference (provider_preference: none).
	First int
	// Count is how many such providers hold the release cached; 0 is no evidence.
	Count int
}

// Score sets Release.FormatScore from the profile, as the arr does when it maps a release.
func (s *Subject) Score() { s.Release.FormatScore = s.Profile.FormatScore(s.Release.Formats) }

// files are the existing files the upgrade checks compare with: the movie file, or each mapped episode's file in
// episode order (a multi-episode file appears once per episode, as in the arr's loop).
func (e *Engine) files(s *Subject) []*File {
	if e.Flavour == format.Radarr {
		if s.Media != nil && s.Media.MovieFile != nil {
			return []*File{s.Media.MovieFile}
		}
		return nil
	}
	var out []*File
	for _, ep := range s.Episodes {
		if ep.File != nil {
			out = append(out, ep.File)
		}
	}
	return out
}

// Spec is one ported decision specification.
type Spec struct {
	// Name is the arr class name.
	Name     string
	Priority Priority
	Type     RejectionType
	Check    func(e *Engine, s *Subject) Decision
}

// Specs returns the ported specifications for the engine's flavour, in the order the arr evaluates them.
func (e *Engine) Specs() []Spec {
	if e.Flavour == format.Sonarr {
		return sonarrSpecs
	}
	return radarrSpecs
}

// Spec returns one ported specification by its short name ("UpgradeDisk") or class name.
func (e *Engine) Spec(name string) (Spec, bool) {
	for _, s := range e.Specs() {
		if s.Name == name || s.Name == name+"Specification" || equalFold(s.Name, name+"Specification") {
			return s, true
		}
	}
	return Spec{}, false
}

// Run evaluates one specification. A panic (the arr's exception) becomes a DecisionError rejection, as
// DownloadDecisionMaker.EvaluateSpec does.
func (e *Engine) Run(sp Spec, s *Subject) (d Decision) {
	defer func() {
		if r := recover(); r != nil {
			d = Decision{Reason: ReasonDecisionError, Message: fmt.Sprintf("%s: %v", sp.Name, r)}
		}
	}()
	return sp.Check(e, s)
}

// Evaluate is DownloadDecisionMaker.GetDecisionForReport over the ported specifications: specifications are
// grouped by priority, lowest first; every specification of a group runs, and the first group with a rejection
// ends the evaluation. No rejections means approved.
func (e *Engine) Evaluate(s *Subject) []Rejection {
	specs := e.Specs()
	var prios []Priority
	seen := map[Priority]bool{}
	for _, sp := range specs {
		if !seen[sp.Priority] {
			seen[sp.Priority] = true
			prios = append(prios, sp.Priority)
		}
	}
	sort.Slice(prios, func(i, j int) bool { return prios[i] < prios[j] })
	for _, p := range prios {
		var out []Rejection
		for _, sp := range specs {
			if sp.Priority != p {
				continue
			}
			if d := e.Run(sp, s); !d.Accepted {
				out = append(out, Rejection{Spec: sp.Name, Reason: d.Reason, Message: d.Message, Type: sp.Type})
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}
