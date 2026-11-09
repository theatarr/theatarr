package radarr

import (
	"encoding/json"
	"fmt"
)

// Call runs one named parser operation, as used by the extracted ParserTests cases
// (testdata/parsertests) and by `theatarr radarr-parse`. It returns the arr-shaped JSON value of the
// result: what Radarr's own types would serialise to, nil for a .NET null.
//
// Calls: parseMovieTitle (args.isDir), parseMoviePath, parseQuality, parseQualityDetection,
// parseLanguages, parseSubtitleLanguage, parseSubtitleLanguageInformation, findIsoLanguage,
// parseReleaseGroup, parseEdition, cleanMovieTitle, normalizeImdbId, toUrlSlug
// (args.invalidDashReplacement, args.trimEndChars, args.deduplicateChars), isSceneTitle,
// getSceneTitle, arabicToRoman, romanToArabic.
func Call(name, input string, args map[string]any) (any, error) {
	out, err := Run(name, input, args)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	var v any
	err = json.Unmarshal(b, &v)
	return v, err
}

// Run is Call without the JSON round trip: it returns the typed result, which marshals with
// Radarr's field order.
func Run(name, input string, args map[string]any) (any, error) {
	var out any
	switch name {
	case "parseMovieTitle":
		isDir, _ := args["isDir"].(bool)
		out = ParseMovieTitle(input, isDir)
	case "parseMoviePath":
		out = ParseMoviePath(input)
	case "parseQuality":
		out = ParseQuality(input)
	case "parseQualityDetection":
		q := ParseQuality(input)
		out = map[string]QualityDetectionSource{
			"sourceDetectionSource":     q.SourceDetectionSource,
			"resolutionDetectionSource": q.ResolutionDetectionSource,
			"modifierDetectionSource":   q.ModifierDetectionSource,
			"revisionDetectionSource":   q.RevisionDetectionSource,
		}
	case "parseLanguages":
		out = ParseLanguages(input)
	case "parseSubtitleLanguage":
		out = ParseSubtitleLanguage(input)
	case "parseSubtitleLanguageInformation":
		out = ParseSubtitleLanguageInformation(input)
	case "findIsoLanguage":
		out = FindIsoLanguage(input)
	case "parseReleaseGroup":
		out = ParseReleaseGroup(input)
	case "parseEdition":
		out = ParseEdition(input)
	case "cleanMovieTitle":
		out = CleanMovieTitle(input)
	case "normalizeImdbId":
		out = NormalizeImdbID(input)
	case "toUrlSlug":
		dash, _ := args["invalidDashReplacement"].(bool)
		trim, dedupe := optString(args, "trimEndChars"), optString(args, "deduplicateChars")
		out = ToURLSlug(input, dash, trim, dedupe)
	case "isSceneTitle":
		out = IsSceneTitle(input)
	case "getSceneTitle":
		out = GetSceneTitle(input)
	case "arabicToRoman":
		n, ok := tryParseInt(input, 32)
		if !ok {
			return nil, fmt.Errorf("arabicToRoman: %q is not a number", input)
		}
		r, err := NewRomanNumeralFromInt(int(n))
		if err != nil {
			return nil, err
		}
		out = r.ToRomanNumeral()
	case "romanToArabic":
		r, err := NewRomanNumeralFromString(input)
		if err != nil {
			return nil, err
		}
		out = r.ToInt()
	default:
		return nil, fmt.Errorf("unknown call %q", name)
	}
	return out, nil
}

// optString reads an optional string argument: absent means the C# default "-_", JSON null means
// .NET null.
func optString(args map[string]any, key string) *string {
	v, present := args[key]
	if !present {
		s := DefaultSlugChars
		return &s
	}
	s, ok := v.(string)
	if !ok {
		return nil
	}
	return &s
}
