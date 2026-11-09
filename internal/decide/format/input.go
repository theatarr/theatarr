package format

// Input is what the conditions look at: the arr's CustomFormatInput reduced to the fields its specifications read.
// Port of Radarr src/NzbDrone.Core/CustomFormats/CustomFormatInput.cs @ a96bf7c and Sonarr's @ cab419a.
//
// It has no dependency on a parser. The corpus check fills it from an arr's own parse; our parsers get adapters.
// Every enum is in Flavour's numbering.
type Input struct {
	Flavour Flavour

	// ReleaseTitle is the string the Release Title condition matches: Radarr's ParsedMovieInfo.SimpleReleaseTitle
	// (the title with the movie name replaced, so a format cannot match on it), Sonarr's
	// ParsedEpisodeInfo.ReleaseTitle.
	ReleaseTitle *string
	// Filename is the file name, set when a file is scored (CustomFormatInput.Filename). The Release Title condition
	// matches it as well. Nil at grab time and in GET /api/v3/parse.
	Filename *string

	// ReleaseGroup and Edition (Radarr) as parsed. Nil when the arr has none (null), which never matches; an empty
	// string is matched like any other string, as in the arr.
	ReleaseGroup *string
	Edition      *string

	// Quality as the arr's QualityModel.Quality: Source in Flavour's QualitySource, Resolution in pixels (0 unknown),
	// Modifier in Radarr's Modifier (always 0 for Sonarr).
	Source     int
	Resolution int
	Modifier   int

	// Languages are the release's language ids (RemoteMovie/RemoteEpisode.Languages, after augmentation). Nil
	// means null, which matches no Language condition, negated or not.
	Languages []int
	// LanguagesAssumed is set when neither the name nor the indexer gave a language and Languages is the mapped
	// movie's or series' original language (the arr's fallback). No condition reads it.
	LanguagesAssumed bool

	IndexerFlags int
	Size         int64 // bytes

	// Year is ParsedMovieInfo.Year (Radarr), 0 when not parsed.
	Year int
	// ReleaseType is Sonarr's ReleaseType (0 unknown, 1 single episode, 2 multi-episode, 3 season pack).
	ReleaseType int

	// SeaDex is the release's SeaDex standing for the title (ours, not the arr's: SeaDexSpecification reads it).
	SeaDex SeaDexRank

	// Media is the movie or series the release mapped to, or nil when it mapped to none.
	Media *Media
}

// Media is the part of the mapped movie or series that conditions read.
type Media struct {
	// Year is the movie's year, used by Radarr's Year condition when the parse has none.
	Year int
	// OriginalLanguage resolves the "Original" language in Language conditions.
	OriginalLanguage int
}

// Str returns a pointer to s, for filling Input.
func Str(s string) *string { return &s }
