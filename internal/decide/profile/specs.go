package profile

// Ports of the decision specifications under src/NzbDrone.Core/DecisionEngine/Specifications: Radarr @ a96bf7c,
// Sonarr @ cab419a. Each function heads with its class; flavour differences are inline.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dlclark/regexp2"
	"github.com/theatarr/theatarr/internal/decide/format"

	"github.com/theatarr/theatarr/internal/netregex"
)

// Specification order. The arrs resolve every IDownloadDecisionEngineSpecification by assembly scanning and keep
// that order within a priority group; it only decides the order of rejection messages. We list them by class
// name, which is the source order.
var radarrSpecs = []Spec{
	{"AcceptableSizeSpecification", PriorityDefault, Permanent, acceptableSize},
	{"AlreadyImportedSpecification", PriorityDatabase, Permanent, alreadyImported},
	{"CustomFormatAllowedbyProfileSpecification", PriorityDefault, Permanent, customFormatAllowedByProfile},
	{"LanguageSpecification", PriorityDefault, Permanent, language},
	{"MaximumSizeSpecification", PriorityDefault, Permanent, maximumSize},
	{"NotSampleSpecification", PriorityDefault, Permanent, notSample},
	{"ProperSpecification", PriorityDefault, Permanent, proper},
	{"QualityAllowedByProfileSpecification", PriorityDefault, Permanent, qualityAllowedByProfile},
	{"RawDiskSpecification", PriorityDefault, Permanent, rawDisk},
	{"RepackSpecification", PriorityDatabase, Permanent, repack},
	{"TorrentSeedingSpecification", PriorityDefault, Permanent, torrentSeeding},
	{"UpgradeAllowedSpecification", PriorityDefault, Permanent, upgradeAllowed},
	{"UpgradeDiskSpecification", PriorityDefault, Permanent, upgradeDisk},
}

var sonarrSpecs = []Spec{
	{"AcceptableSizeSpecification", PriorityDefault, Permanent, acceptableSize},
	{"AlreadyImportedSpecification", PriorityDatabase, Permanent, alreadyImported},
	{"AnimeVersionUpgradeSpecification", PriorityDefault, Permanent, animeVersionUpgrade},
	{"CustomFormatAllowedbyProfileSpecification", PriorityDefault, Permanent, customFormatAllowedByProfile},
	{"FullSeasonSpecification", PriorityDefault, Permanent, fullSeason},
	{"MaximumSizeSpecification", PriorityDefault, Permanent, maximumSize},
	{"MultiSeasonSpecification", PriorityDefault, Permanent, multiSeason},
	{"NotSampleSpecification", PriorityDefault, Permanent, notSample},
	{"ProperSpecification", PriorityDefault, Permanent, proper},
	{"QualityAllowedByProfileSpecification", PriorityDefault, Permanent, qualityAllowedByProfile},
	{"RawDiskSpecification", PriorityDefault, Permanent, rawDisk},
	{"RepackSpecification", PriorityDatabase, Permanent, repack},
	{"SameEpisodesGrabSpecification", PriorityDefault, Permanent, sameEpisodesGrab},
	{"SceneMappingSpecification", PriorityDefault, Temporary, sceneMapping},
	{"SeasonPackOnlySpecification", PriorityDefault, Permanent, seasonPackOnly},
	{"SplitEpisodeSpecification", PriorityDefault, Permanent, splitEpisode},
	{"TorrentSeedingSpecification", PriorityDefault, Permanent, torrentSeeding},
	{"UpgradeAllowedSpecification", PriorityDefault, Permanent, upgradeAllowed},
	{"UpgradeDiskSpecification", PriorityDefault, Permanent, upgradeDisk},
	{"MonitoredEpisodeSpecification", PriorityDefault, Permanent, monitoredEpisode}, // RssSync
	{"EpisodeRequestedSpecification", PriorityDefault, Permanent, episodeRequested}, // Search
	{"SeasonMatchSpecification", PriorityDefault, Permanent, seasonMatch},
	{"SingleEpisodeSearchMatchSpecification", PriorityDefault, Permanent, singleEpisodeSearchMatch},
}

// QualityAllowedByProfileSpecification.
func qualityAllowedByProfile(e *Engine, s *Subject) Decision {
	q := s.Release.Quality.Quality
	if !s.Profile.item(q.ID).Allowed {
		return Reject(ReasonQualityNotWanted, "{0} is not wanted in profile", q)
	}
	return Accept()
}

// CustomFormatAllowedbyProfileSpecification.
func customFormatAllowedByProfile(e *Engine, s *Subject) Decision {
	minScore := s.Profile.MinFormatScore
	score := s.Release.FormatScore
	if score < minScore {
		owner := "Movie's"
		if e.Flavour == format.Sonarr {
			owner = "Series"
		}
		return Reject(ReasonCustomFormatMinimumScore, "Custom Formats {0} have score {1} below "+owner+" profile minimum {2}",
			concatFormats(s.Release.Formats), score, minScore)
	}
	return Accept()
}

// AcceptableSizeSpecification. Radarr multiplies the definition's MB per minute by the movie's runtime (110 when
// unknown, written back to the movie). Sonarr sums the episodes' runtimes, falling back to the series runtime, and
// to 45 minutes for a new series' first season within a day of its premiere; specials are not checked.
func acceptableSize(e *Engine, s *Subject) Decision {
	q := s.Release.Quality.Quality
	size := s.Release.Size
	if e.Flavour == format.Sonarr {
		if s.Release.Special {
			return Accept()
		}
		if size == 0 {
			return Accept()
		}
		runtime, d, ok := sonarrRuntime(s)
		if !ok {
			return d
		}
		def := e.definition(q)
		msg := func() string {
			if len(s.Episodes) == 1 {
				return fmt.Sprintf("%dmin", runtime)
			}
			return fmt.Sprintf("%dx %dmin", len(s.Episodes), runtime)
		}
		if def.MinSize != nil {
			minSize := Megabytes(*def.MinSize) * int64(runtime)
			if size < minSize {
				return Reject(ReasonBelowMinimumSize, "{0} is smaller than minimum allowed {1} (for {2})",
					SizeSuffix(size), SizeSuffix(minSize), msg())
			}
		}
		if def.MaxSize != nil && *def.MaxSize != 0 {
			maxSize := Megabytes(*def.MaxSize) * int64(runtime)
			if size > maxSize {
				return Reject(ReasonAboveMaximumSize, "{0} is larger than maximum allowed {1} (for {2})",
					SizeSuffix(size), SizeSuffix(maxSize), msg())
			}
		}
		return Accept()
	}

	if size == 0 {
		return Accept()
	}
	def := e.definition(q)
	m := s.Media
	if m.Runtime == 0 {
		m.Runtime = 110 // "using median movie runtime of 110 minutes"
	}
	if def.MinSize != nil {
		minSize := Megabytes(*def.MinSize) * int64(m.Runtime)
		if size < minSize {
			return Reject(ReasonBelowMinimumSize, "{0} is smaller than minimum allowed {1} (for {2})",
				SizeSuffix(size), SizeSuffix(minSize), m.Title)
		}
	}
	if def.MaxSize == nil || *def.MaxSize == 0 {
		// unlimited
	} else if m.Runtime == 0 {
		return Reject(ReasonUnknownRuntime, "Movie runtime is 0, unable to validate size until it is available")
	} else {
		maxSize := Megabytes(*def.MaxSize) * int64(m.Runtime)
		if size > maxSize {
			return Reject(ReasonAboveMaximumSize, "{0} is larger than maximum allowed {1} (for {2})",
				SizeSuffix(size), SizeSuffix(maxSize), m.Title)
		}
	}
	return Accept()
}

// sonarrRuntime is the runtime part of Sonarr's AcceptableSizeSpecification.
func sonarrRuntime(s *Subject) (int, Decision, bool) {
	seriesRuntime := s.Media.Runtime
	if seriesRuntime == 0 {
		pilot := s.Media.Pilot
		if pilot == nil {
			// Seasons.Min or GetEpisodesBySeason(...).First() throws in the arr.
			panic("Sequence contains no elements")
		}
		if s.Episodes[0].SeasonNumber == pilot.SeasonNumber {
			start := s.Release.PublishDate
			if pilot.AirDateUtc != nil {
				start = *pilot.AirDateUtc
			}
			grace := start.Add(24 * time.Hour)
			all := true
			for _, ep := range s.Episodes {
				if ep.AirDateUtc != nil && ep.AirDateUtc.After(grace) { // !Before: Before is <=
					all = false
				}
			}
			if all {
				seriesRuntime = 45
			}
		}
	}
	runtime := 0
	for _, ep := range s.Episodes {
		if ep.Runtime > 0 {
			runtime += ep.Runtime
		} else {
			runtime += seriesRuntime
		}
	}
	if runtime == 0 {
		return 0, Reject(ReasonUnknownRuntime, "Runtime of all episodes is 0, unable to validate size until it is available"), false
	}
	return runtime, Decision{}, true
}

// MaximumSizeSpecification.
func maximumSize(e *Engine, s *Subject) Decision {
	size := s.Release.Size
	maxSize := int64(e.Config.MaximumSize) * 1024 * 1024
	if maxSize == 0 || size == 0 {
		return Accept()
	}
	if size > maxSize {
		return Reject(ReasonMaximumSizeExceeded, fmt.Sprintf("%s is too big, maximum size is %s (Settings->Indexers->Maximum Size)",
			SizeSuffix(size), SizeSuffix(maxSize)))
	}
	return Accept()
}

// NotSampleSpecification.
func notSample(e *Engine, s *Subject) Decision {
	if strings.Contains(strings.ToLower(s.Release.Title), "sample") && s.Release.Size < 70*1024*1024 {
		return Reject(ReasonSample, "Sample")
	}
	return Accept()
}

// LanguageSpecification (Radarr): the profile's language, Any, or the movie's original language.
func language(e *Engine, s *Subject) Decision {
	wanted := s.Profile.Language
	if wanted == format.LanguageAny {
		return Accept()
	}
	langs := langNames(e.Flavour, s.Release.Languages)
	if wanted == format.LanguageOriginal {
		orig := s.Media.OriginalLanguage
		if !containsInt(s.Release.Languages, orig) {
			return Reject(ReasonWantedLanguage, "Original Language ({0}) is wanted, but found {1}", e.Flavour.LanguageName(orig), langs)
		}
		return Accept()
	}
	if !containsInt(s.Release.Languages, wanted) {
		return Reject(ReasonWantedLanguage, "{0} is wanted, but found {1}", e.Flavour.LanguageName(wanted), langs)
	}
	return Accept()
}

var rawDiscRegex = []*regexp2.Regexp{
	netregex.MustCompile(`(?:dis[ck])(?:[-_. ]\d+[-_. ])(?:(?:(?:480|720|1080|2160)[ip]|)[-_. ])?(?:Blu\-?ray)`, netregex.IgnoreCase),
	netregex.MustCompile(`(?:(?:480|720|1080|2160)[ip]|)[-_. ](?:full)[-_. ](?:Blu\-?ray)`, netregex.IgnoreCase),
	netregex.MustCompile(`(?:\d?x?M?DVD-?[R59])(?:[ ._]|$)`, netregex.IgnoreCase),
}

// RawDiskSpecification.
func rawDisk(e *Engine, s *Subject) Decision {
	if s.Release == nil {
		return Accept()
	}
	for _, re := range rawDiscRegex {
		ok, err := re.MatchString(s.Release.Title)
		if err != nil {
			panic(err)
		}
		if ok {
			return Reject(ReasonRaw, "Raw Bluray/DVD release")
		}
	}
	c := strings.ToLower(s.Release.Container)
	if strings.TrimSpace(c) == "" {
		return Accept()
	}
	switch c {
	case "vob", "iso":
		return Reject(ReasonRaw, "Raw DVD release")
	case "m2ts":
		return Reject(ReasonRaw, "Raw Bluray release")
	}
	return Accept()
}

// RepackSpecification: a repack of the file on disk must come from the same release group.
func repack(e *Engine, s *Subject) Decision {
	if !s.Release.Quality.Revision.IsRepack {
		return Accept()
	}
	mode := e.Config.DownloadPropersAndRepacks
	if mode == DoNotPrefer {
		return Accept()
	}
	for _, file := range e.files(s) {
		if !isRevisionUpgrade(file.Quality, s.Release.Quality) {
			continue
		}
		if mode == DoNotUpgrade {
			return Reject(ReasonRepackDisabled, "Repack downloading is disabled")
		}
		group, fileGroup := s.Release.ReleaseGroup, file.ReleaseGroup
		if strings.TrimSpace(fileGroup) == "" {
			return Reject(ReasonRepackUnknownReleaseGroup, "Unable to determine release group for the existing file")
		}
		if strings.TrimSpace(group) == "" {
			return Reject(ReasonRepackUnknownReleaseGroup, "Unable to determine release group for this release")
		}
		if !strings.EqualFold(fileGroup, group) {
			return Reject(ReasonRepackReleaseGroupDoesNotMatch,
				"Release is a repack for a different release group. Release Group: {0}. File release group: {1}", group, fileGroup)
		}
	}
	return Accept()
}

// AnimeVersionUpgradeSpecification (Sonarr): a new version (v2) of an anime episode must come from the same group.
func animeVersionUpgrade(e *Engine, s *Subject) Decision {
	group := s.Release.ReleaseGroup
	if s.Media == nil || !s.Media.Anime {
		return Accept()
	}
	if e.Config.DownloadPropersAndRepacks == DoNotPrefer {
		return Accept()
	}
	for _, file := range e.files(s) {
		if isRevisionUpgrade(file.Quality, s.Release.Quality) {
			if strings.TrimSpace(file.ReleaseGroup) == "" {
				return Reject(ReasonUnknownReleaseGroup, "Existing release group is unknown")
			}
			if strings.TrimSpace(group) == "" {
				return Reject(ReasonUnknownReleaseGroup, "Release group is unknown")
			}
			if file.ReleaseGroup != group {
				return Reject(ReasonReleaseGroupDoesNotMatch, "{0} does not match existing release group {1}", group, file.ReleaseGroup)
			}
		}
	}
	return Accept()
}

// UpgradeAllowedSpecification.
func upgradeAllowed(e *Engine, s *Subject) Decision {
	for _, file := range e.files(s) {
		if !e.IsUpgradeAllowed(s.Profile, file.Quality, file.Formats, s.Release.Quality, s.Release.Formats) {
			return Reject(ReasonQualityUpgradesDisabled, "Existing file and the Quality profile does not allow upgrades")
		}
	}
	return Accept()
}

// UpgradeDiskSpecification.
func upgradeDisk(e *Engine, s *Subject) Decision {
	if e.Flavour == format.Sonarr && e.Config.AcceptReleaseAnyUpgradable {
		return upgradeDiskAny(e, s)
	}
	for _, file := range e.files(s) {
		if d, upgradable := upgradeDiskFile(e, s, file); !upgradable {
			return d
		}
	}
	return Accept()
}

// upgradeDiskAny is UpgradeDiskSpecification as the actuallyevan Sonarr fork changes it under
// ACCEPT_RELEASE_ANY_UPGRADABLE (fork v4.0.20.3014-1, same file): every episode is looked at, an episode with no file
// (or a file record that is gone) counts as upgradable, and the release is accepted when any episode is upgradable;
// otherwise the first episode's rejection stands.
func upgradeDiskAny(e *Engine, s *Subject) Decision {
	var rejection *Decision
	anyUpgradable := false
	for _, ep := range s.Episodes {
		if ep.File == nil {
			anyUpgradable = true
			continue
		}
		d, upgradable := upgradeDiskFile(e, s, ep.File)
		if upgradable {
			anyUpgradable = true
			continue
		}
		if rejection == nil {
			rejection = &d
		}
	}
	if anyUpgradable || rejection == nil {
		return Accept()
	}
	return *rejection
}

// upgradeDiskFile is the loop body of UpgradeDiskSpecification for one existing file: whether the release upgrades
// it, and the rejection when it does not.
func upgradeDiskFile(e *Engine, s *Subject, file *File) (Decision, bool) {
	p := s.Profile
	if !e.CutoffNotMet(p, file.Quality, file.Formats, &s.Release.Quality) {
		cutoff := p.Cutoff
		if !p.UpgradeAllowed {
			cutoff = p.FirstAllowedQuality().ID
		}
		qualityCutoff := p.item(cutoff)
		if e.Flavour == format.Sonarr {
			return Reject(ReasonDiskCutoffMet, "Existing file meets cutoff: {0}", qualityCutoff), false
		}
		return Reject(ReasonDiskCutoffMet, "Existing file meets cutoff: {0} [{1}]", qualityCutoff, concatFormats(file.Formats)), false
	}
	switch e.IsUpgradable(p, file.Quality, file.Formats, s.Release.Quality, s.Release.Formats) {
	case UpgradeBetterQuality:
		return Reject(ReasonDiskHigherPreference, "Existing file on disk is of equal or higher preference: {0}", file.Quality), false
	case UpgradeBetterRevision:
		return Reject(ReasonDiskHigherRevision, "Existing file on disk is of equal or higher revision: {0}", file.Quality.Revision), false
	case UpgradeQualityCutoff:
		return Reject(ReasonDiskCutoffMet, "Existing file on disk meets quality cutoff: {0}", p.item(p.Cutoff)), false
	case UpgradeCustomFormatCutoff:
		return Reject(ReasonDiskCustomFormatCutoffMet, "Existing file on disk meets Custom Format cutoff: {0}", p.CutoffFormatScore), false
	case UpgradeCustomFormatScore:
		return Reject(ReasonDiskCustomFormatScore, "Existing file on disk has a equal or higher Custom Format score: {0}", p.FormatScore(file.Formats)), false
	case UpgradeMinCustomFormatScore:
		return Reject(ReasonDiskCustomFormatScoreIncrement, "Existing file on disk has Custom Format score within Custom Format score increment: {0}", p.MinUpgradeFormatScore), false
	case UpgradesNotAllowed:
		return Reject(ReasonDiskUpgradesNotAllowed, "Existing file on disk and Quality Profile '{0}' does not allow upgrades", p.Name), false
	}
	return Accept(), true
}

// concatFormats is ConcatToString over custom formats: names joined by ", ".
func concatFormats(fs []*format.Format) string {
	names := make([]string, len(fs))
	for i, f := range fs {
		names[i] = f.Name
	}
	return strings.Join(names, ", ")
}

// langNames is Languages.ToExtendedString: names joined by ", ".
func langNames(fl format.Flavour, ids []int) string {
	names := make([]string, len(ids))
	for i, id := range ids {
		names[i] = fl.LanguageName(id)
	}
	return strings.Join(names, ", ")
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool { return strings.EqualFold(a, b) }

// netFormat is string.Format for the plain {n} placeholders the arr messages use.
func netFormat(msg string, args ...any) string {
	var b strings.Builder
	for i := 0; i < len(msg); i++ {
		c := msg[i]
		if c == '{' {
			if j := strings.IndexByte(msg[i:], '}'); j > 0 {
				if n, err := strconv.Atoi(msg[i+1 : i+j]); err == nil && n >= 0 && n < len(args) {
					b.WriteString(netString(args[n]))
					i += j
					continue
				}
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}

func netString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case *Item:
		if x == nil {
			return ""
		}
		return x.String()
	case fmt.Stringer:
		return x.String()
	case bool:
		if x {
			return "True"
		}
		return "False"
	}
	return fmt.Sprint(v)
}
