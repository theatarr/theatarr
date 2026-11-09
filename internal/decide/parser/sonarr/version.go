package sonarr

import "fmt"

// Version picks the Sonarr parser a call follows: V4 is the port of v4.0.20.3014 (cab419a), which the package's
// plain functions (ParseTitle, ParsePath, ...) are; V5 is Sonarr v5 (develop @ a6171f0ca, the v5 betas): its parser,
// quality, language and release group changes (parser_v5.go, docs/design/sonarr-v5.md). V4Plus is ours: V4 with the v5
// changes we chose one by one and a few fixes of our own (parser_v4plus.go). The zero value is V4.
type Version int

const (
	V4 Version = iota
	V5
	V4Plus
)

// Policy values of versions.<k>.sonarr.
const (
	VersionNameV4     = "v4"
	VersionNameV5     = "v5"
	VersionNameV4Plus = "v4plus"
)

func (v Version) String() string {
	switch v {
	case V5:
		return VersionNameV5
	case V4Plus:
		return VersionNameV4Plus
	}
	return VersionNameV4
}

// ParseVersion reads a policy value: "" and "v4" are V4, "v5" is V5, "v4plus" is V4Plus.
func ParseVersion(s string) (Version, error) {
	switch s {
	case "", VersionNameV4:
		return V4, nil
	case VersionNameV5:
		return V5, nil
	case VersionNameV4Plus:
		return V4Plus, nil
	}
	return V4, fmt.Errorf("sonarr version must be %s, %s or %s, got %q", VersionNameV4, VersionNameV4Plus,
		VersionNameV5, s)
}

func (v Version) d() *dialect {
	switch v {
	case V5:
		return v5d
	case V4Plus:
		return v4plusd
	}
	return v4d
}

// ParseTitle is Parser.ParseTitle of the version.
func (v Version) ParseTitle(title string) *ParsedEpisodeInfo { return v.d().parseTitle(title) }

// ParsePath is Parser.ParsePath of the version.
func (v Version) ParsePath(path string) *ParsedEpisodeInfo { return v.d().parsePath(path) }

// ParseSeasonTitle is Sonarr v5's Parser.ParseSeasonTitle: a release named by a season's own title, with no season or
// episode numbers (anime season packs), as a full-season parse with IsSeasonTitle and no season. V4 has no such
// parse: nil.
func (v Version) ParseSeasonTitle(title string) *ParsedEpisodeInfo {
	if v != V5 {
		return nil
	}
	return v5d.parseSeasonTitle(title)
}

// CleanSeriesTitle is Parser.CleanSeriesTitle of the version (v5 removes diacritics as Diacritical.Net does).
func (v Version) CleanSeriesTitle(title string) string { return v.d().cleanSeriesTitle(title) }

// ParseSeriesName is Parser.ParseSeriesName of the version.
func (v Version) ParseSeriesName(title string) string { return v.d().parseSeriesName(title) }

// ParseQuality is QualityParser.ParseQuality of the version.
func (v Version) ParseQuality(name string) QualityModel { return v.d().parseQuality(name) }

// ParseLanguages is LanguageParser.ParseLanguages of the version.
func (v Version) ParseLanguages(title string) []Language { return v.d().parseLanguages(title) }

// ParseReleaseGroup is ReleaseGroupParser.ParseReleaseGroup (Parser.ParseReleaseGroup in v4) of the version.
func (v Version) ParseReleaseGroup(title string) *string { return v.d().parseReleaseGroup(title) }

// HasMultipleLanguages is Parser.HasMultipleLanguages of the version.
func (v Version) HasMultipleLanguages(title string) bool { return isMatch(v.d().multi, title) }

// FindIsoLanguage is IsoLanguages.Find of the version (v5 knows the languages 47 to 53).
func (v Version) FindIsoLanguage(isoCode string) *IsoLanguage { return v.d().findIsoLanguage(isoCode) }

// Languages is Language.All of the version.
func (v Version) Languages() []Language { return v.d().allLanguages }
