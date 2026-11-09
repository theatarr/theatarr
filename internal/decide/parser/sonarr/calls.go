package sonarr

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Call runs one named parser operation, as used by the extracted ParserTests cases (testdata/parsertests). It
// returns the arr-shaped JSON value of the result: what Sonarr's own types serialise to, nil for a .NET null.
//
// Calls: parseTitle, parsePath, parseQuality, parseQualityDetection, parseTitleQualityDetection, parseLanguages,
// parseReleaseGroup, cleanSeriesTitle, parseSeriesName, normalizeEpisodeTitle, parseSubtitleLanguage,
// parseSubtitleLanguageInformation, findIsoLanguage, isSceneTitle, parseTitleExpand (args year/month/day or
// daysFromNow), parsedEpisodeInfo (args: the fields), validateForSeriesType (args parsedEpisodeInfo, seriesType).
func Call(name, input string, args map[string]any) (any, error) { return V4.Call(name, input, args) }

// Call is the package Call with the version's parser. V5 adds parseSeasonTitle, parseTitleExpandDate (args
// daysFromNow: {isNull, airDateIsTarget}, the target being today plus daysFromNow) and
// parsedEpisodeInfoStringContains (args fields, expected: ToString contains expected); its parsedEpisodeInfo takes
// seasonNumbers.
func (v Version) Call(name, input string, args map[string]any) (any, error) {
	out, err := v.Run(name, input, args)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	var res any
	err = json.Unmarshal(b, &res)
	return res, err
}

// detectionSources is the parseQualityDetection output: the [JsonIgnore] detection sources Sonarr's tests assert.
func detectionSources(m QualityModel) map[string]QualityDetectionSource {
	return map[string]QualityDetectionSource{
		"sourceDetectionSource":     m.SourceDetectionSource,
		"resolutionDetectionSource": m.ResolutionDetectionSource,
		"revisionDetectionSource":   m.RevisionDetectionSource,
	}
}

// Run is Call without the JSON round trip: it returns the typed result, which marshals with Sonarr's field order.
func Run(name, input string, args map[string]any) (any, error) { return V4.Run(name, input, args) }

// Run is the package Run with the version's parser.
func (v Version) Run(name, input string, args map[string]any) (any, error) {
	switch name {
	case "parseTitle":
		return v.ParseTitle(input), nil
	case "parsePath":
		return v.ParsePath(input), nil
	case "parseQuality":
		return v.ParseQuality(input), nil
	case "parseQualityDetection":
		return detectionSources(v.ParseQuality(input)), nil
	case "parseTitleQualityDetection":
		// The detection sources of ParseTitle(input).Quality, plus "quality" (the Quality itself).
		r := v.ParseTitle(input)
		if r == nil {
			return nil, nil
		}
		out := map[string]any{"quality": r.Quality.Quality}
		for k, v := range detectionSources(r.Quality) {
			out[k] = v
		}
		return out, nil
	case "parseLanguages":
		return v.ParseLanguages(input), nil
	case "parseReleaseGroup":
		return v.ParseReleaseGroup(input), nil
	case "cleanSeriesTitle":
		return v.CleanSeriesTitle(input), nil
	case "parseSeriesName":
		return v.ParseSeriesName(input), nil
	case "normalizeEpisodeTitle":
		return NormalizeEpisodeTitle(input), nil
	case "parseSubtitleLanguage":
		return ParseSubtitleLanguage(input), nil
	case "parseSubtitleLanguageInformation":
		return ParseSubtitleLanguageInformation(input), nil
	case "findIsoLanguage":
		return v.FindIsoLanguage(input), nil
	case "isSceneTitle":
		return IsSceneTitle(input), nil
	case "parseTitleExpand":
		// StringExtensions.Expand on the template: {year}, {month}, {day} from args, or from today plus daysFromNow
		// with two-digit month and day.
		return v.ParseTitle(expandDate(input, args)), nil
	case "parseTitleExpandDate":
		r := v.ParseTitle(expandDate(input, args))
		n, _ := args["daysFromNow"].(float64)
		target := time.Now().AddDate(0, 0, int(n)).Format("2006-01-02")
		return map[string]bool{"isNull": r == nil, "airDateIsTarget": r != nil && r.AirDate != nil && *r.AirDate == target}, nil
	case "parseSeasonTitle":
		return v.ParseSeasonTitle(input), nil
	case "parsedEpisodeInfo":
		return v.episodeInfoFromArgs(args)
	case "parsedEpisodeInfoStringContains":
		p, err := v.episodeInfoFromArgs(asMap(args["fields"]))
		if err != nil {
			return nil, err
		}
		return strings.Contains(p.String(), fmt.Sprint(args["expected"])), nil
	case "validateForSeriesType":
		p, err := v.episodeInfoFromArgs(asMap(args["parsedEpisodeInfo"]))
		if err != nil {
			return nil, err
		}
		st, ok := ParseSeriesType(fmt.Sprint(args["seriesType"]))
		if !ok {
			return nil, fmt.Errorf("unknown series type %v", args["seriesType"])
		}
		return ValidateForSeriesType(p, st), nil
	}
	return nil, fmt.Errorf("unknown call %q", name)
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// expandDate is StringExtensions.Expand on a template: {year}, {month}, {day} from args, or from today plus
// daysFromNow with two-digit month and day.
func expandDate(input string, args map[string]any) string {
	year, month, day := fmt.Sprint(args["year"]), fmt.Sprint(args["month"]), fmt.Sprint(args["day"])
	if n, ok := args["daysFromNow"].(float64); ok {
		d := time.Now().AddDate(0, 0, int(n))
		year, month, day = strconv.Itoa(d.Year()), fmt.Sprintf("%02d", int(d.Month())), fmt.Sprintf("%02d", d.Day())
	}
	return strings.NewReplacer("{year}", year, "{month}", month, "{day}", day).Replace(input)
}

// episodeInfoFromArgs builds a ParsedEpisodeInfo from JSON fields over the defaults of new ParsedEpisodeInfo().
// V5 reads seasonNumbers (default []).
func (v Version) episodeInfoFromArgs(fields map[string]any) (*ParsedEpisodeInfo, error) {
	p := NewParsedEpisodeInfo()
	b, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, p); err != nil {
		return nil, err
	}
	if v == V5 {
		var s struct {
			SeasonNumbers []int `json:"seasonNumbers"`
		}
		if err := json.Unmarshal(b, &s); err != nil {
			return nil, err
		}
		if s.SeasonNumbers == nil {
			s.SeasonNumbers = []int{}
		}
		p.Parser = V5
		p.setSeasonNumbers(s.SeasonNumbers)
	}
	return p, nil
}
