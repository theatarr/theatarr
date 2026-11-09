package sonarr

import "github.com/dlclark/regexp2"

// dialect holds what differs between the Sonarr versions the package ports: the pattern tables of Parser,
// ReleaseGroupParser, LanguageParser and QualityParser, the languages, and v5 for the logic changes. The parsing
// functions are methods on it; the package's plain functions are the v4 dialect (v4d), parser_v5.go builds v5d.
type dialect struct {
	v5 bool
	// v4plus is the V4Plus dialect: v4 logic, plus a subtitle check in regexLanguage (parser_v4plus.go).
	v4plus bool

	// Parser
	preSubstitution     []regexReplace
	pathPreSubstitution []regexReplace // v5's ParserCommon.PathPreSubstitutionRegex; none in v4
	reportTitle         []*regexp2.Regexp
	reversedTitle       *regexp2.Regexp
	simpleTitle         regexReplace
	titleComponents     *regexp2.Regexp
	seasonFolder        *regexp2.Regexp
	multi               *regexp2.Regexp

	// Release groups (Parser in v4, ReleaseGroupParser in v5)
	releaseGroup               *regexp2.Regexp
	exceptionReleaseGroup      *regexp2.Regexp
	exceptionReleaseGroupExact *regexp2.Regexp
	cleanReleaseGroup          regexReplace

	// LanguageParser
	language              *regexp2.Regexp
	caseSensitiveLanguage *regexp2.Regexp
	languageGroups        []languageGroup
	caseSensitiveGroups   []languageGroup

	// QualityParser
	source     *regexp2.Regexp
	resolution *regexp2.Regexp

	// Languages and IsoLanguages
	allLanguages []Language
	isoLanguages []IsoLanguage
}

// v4d is Sonarr v4.0.20.3014: the tables of parser.go, languageparser.go, qualityparser.go and language.go.
var v4d = &dialect{
	preSubstitution:            preSubstitutionRegex,
	reportTitle:                reportTitleRegex,
	reversedTitle:              reversedTitleRegex,
	simpleTitle:                simpleTitleRegex,
	titleComponents:            titleComponentsRegex,
	seasonFolder:               seasonFolderRegex,
	multi:                      multiRegex,
	releaseGroup:               releaseGroupRegex,
	exceptionReleaseGroup:      exceptionReleaseGroupRegex,
	exceptionReleaseGroupExact: exceptionReleaseGroupRegexExact,
	cleanReleaseGroup:          cleanReleaseGroupRegex,
	language:                   languageRegex,
	caseSensitiveLanguage:      caseSensitiveLanguageRegex,
	languageGroups:             languageGroups4,
	caseSensitiveGroups:        caseSensitiveGroups4,
	source:                     sourceRegex,
	resolution:                 resolutionRegex,
	allLanguages:               AllLanguages,
	isoLanguages:               isoLanguages,
}
