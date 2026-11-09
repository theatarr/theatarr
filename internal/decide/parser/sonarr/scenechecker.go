package sonarr

// Port of Sonarr src/NzbDrone.Core/Parser/SceneChecker.cs and ValidateParsedEpisodeInfo.cs @ cab419a.

import "strings"

// IsSceneTitle is SceneChecker.IsSceneTitle. It prefers false negatives over false positives.
func IsSceneTitle(title string) bool {
	if !strings.Contains(title, ".") {
		return false
	}

	if strings.Contains(title, " ") {
		return false
	}

	parsed := ParseTitle(title)

	if parsed == nil ||
		parsed.ReleaseGroup == nil ||
		parsed.Quality.Quality == QualityUnknown ||
		strings.TrimSpace(parsed.SeriesTitle) == "" {
		return false
	}

	return true
}

// SeriesType is Sonarr's SeriesTypes enum (src/NzbDrone.Core/Tv/SeriesTypes.cs), which ValidateForSeriesType needs.
type SeriesType int

const (
	SeriesTypeStandard SeriesType = iota
	SeriesTypeDaily
	SeriesTypeAnime
)

var seriesTypeNames = [...]string{"standard", "daily", "anime"}

func (s SeriesType) String() string { return seriesTypeNames[s] }

// ParseSeriesType maps "standard", "daily" or "anime" to the enum.
func ParseSeriesType(s string) (SeriesType, bool) {
	for i, n := range seriesTypeNames {
		if strings.EqualFold(n, s) {
			return SeriesType(i), true
		}
	}
	return 0, false
}

// ValidateForSeriesType is ValidateParsedEpisodeInfo.ValidateForSeriesType: a daily-style result is invalid for a
// standard series. Sonarr also logs a warning; the port leaves logging to the caller.
func ValidateForSeriesType(info *ParsedEpisodeInfo, seriesType SeriesType) bool {
	return !(info.IsDaily() && seriesType == SeriesTypeStandard)
}
