// Port of Sonarr src/NzbDrone.Core/Organizer/FileNameBuilder.cs @ cab419a.

package naming

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/dlclark/regexp2"

	"github.com/theatarr/theatarr/internal/netregex"
)

var (
	sonarrTitleRegex = netregex.MustCompile(`(?<escaped>\{\{|\}\})|\{(?<prefix>[- ._\[(]*)(?<token>(?:[a-z0-9]+)(?:(?<separator>[- ._]+)(?:[a-z0-9]+))?)(?::(?<customFormat>[ ,a-z0-9+-]+(?<![- ])))?(?<suffix>[- ._)\]]*)\}`,
		netregex.IgnoreCase)
	episodeRegex                = netregex.MustCompile(`(?<episode>\{episode(?:\:0+)?})`, netregex.IgnoreCase)
	seasonRegex                 = netregex.MustCompile(`(?<season>\{season(?:\:0+)?})`, netregex.IgnoreCase)
	absoluteEpisodeRegex        = netregex.MustCompile(`(?<absolute>\{absolute(?:\:0+)?})`, netregex.IgnoreCase)
	seasonEpisodePatternRegex   = netregex.MustCompile(`(?<separator>(?<=})[- ._]+?)?(?<seasonEpisode>s?{season(?:\:0+)?}(?<episodeSeparator>[- ._]?[ex])(?<episode>{episode(?:\:0+)?}))(?<separator>[- ._]+?(?={))?`, netregex.IgnoreCase)
	absoluteEpisodePatternRegex = netregex.MustCompile(`(?<separator>(?<=})[- ._]+?)?(?<absolute>{absolute(?:\:0+)?})(?<separator>[- ._]+?(?={))?`, netregex.IgnoreCase)
	airDateRegex                = netregex.MustCompile(`\{Air(\s|\W|_)Date\}`, netregex.IgnoreCase)
	multiPartCleanupRegex       = netregex.MustCompile(`(?:\:?\s?(?:\(\d+\)|(Part|Pt\.?)\s?\d+))$`, netregex.IgnoreCase)
	yearRegex                   = netregex.MustCompile(`\(\d{4}\)$`, netregex.IgnoreCase)
)

// SeriesType is Sonarr's SeriesTypes.
type SeriesType string

const (
	SeriesStandard SeriesType = "standard"
	SeriesDaily    SeriesType = "daily"
	SeriesAnime    SeriesType = "anime"
)

// Series is what Sonarr's naming reads from a series.
type Series struct {
	Title    string
	Year     int
	Type     SeriesType
	ImdbID   string
	TvdbID   int
	TvMazeID int
	TmdbID   int
}

// Episode is what Sonarr's naming reads from an episode.
type Episode struct {
	Season   int
	Number   int
	Absolute *int   // TVDB absolute number
	Title    string // the episode title
	AirDate  string // yyyy-MM-dd, or empty
}

// EpisodeFile is what Sonarr's naming reads from an episode file.
type EpisodeFile struct {
	// ID is non-zero for a file the arr already has. A new file (0) falls back to its file name for
	// {Original Title} even when the pattern has an episode identifier.
	ID            int64
	Quality       Quality
	ReleaseGroup  string
	ReleaseHash   string
	SceneName     string
	RelativePath  string
	Path          string
	MediaInfo     *MediaInfo
	CustomFormats []string
}

// SceneOrFileName is EpisodeFile.GetSceneOrFileName.
func (f *EpisodeFile) SceneOrFileName() string {
	return sceneOrFileName(f.SceneName, f.RelativePath, f.Path)
}

func seriesTitleWithoutYear(t string) string { return replace(yearRegex, t, "") }

func seriesTitleYear(t string, year int) string {
	if year == 0 || isMatch(yearRegex, t) {
		return t
	}
	return fmt.Sprintf("%s (%d)", t, year)
}

func seriesCleanTitleTheYear(t string, year int) string {
	if year == 0 {
		return cleanTitleThe(t, cleanTitle)
	}
	if m := firstMatch(yearRegex, t); m != nil {
		before := string([]rune(t)[:m.Index])
		v := m.String()
		return cleanTitleThe(strings.TrimSpace(before), cleanTitle) + " " + v[1:5]
	}
	return fmt.Sprintf("%s %d", cleanTitleThe(t, cleanTitle), year)
}

func addSeriesTokens(h handlers, s *Series) {
	h.set("{Series Title}", func(t *tokenMatch) string { return truncate(s.Title, t.CustomFormat) })
	h.set("{Series CleanTitle}", func(t *tokenMatch) string { return truncate(cleanTitle(s.Title), t.CustomFormat) })
	h.set("{Series TitleYear}", func(t *tokenMatch) string { return truncate(seriesTitleYear(s.Title, s.Year), t.CustomFormat) })
	h.set("{Series CleanTitleYear}", func(t *tokenMatch) string {
		return truncate(cleanTitle(seriesTitleYear(s.Title, s.Year)), t.CustomFormat)
	})
	h.set("{Series TitleWithoutYear}", func(t *tokenMatch) string {
		return truncate(seriesTitleWithoutYear(s.Title), t.CustomFormat)
	})
	h.set("{Series CleanTitleWithoutYear}", func(t *tokenMatch) string {
		return truncate(cleanTitle(seriesTitleWithoutYear(s.Title)), t.CustomFormat)
	})
	h.set("{Series TitleThe}", func(t *tokenMatch) string { return truncate(titleThe(s.Title), t.CustomFormat) })
	h.set("{Series CleanTitleThe}", func(t *tokenMatch) string {
		return truncate(cleanTitleThe(s.Title, cleanTitle), t.CustomFormat)
	})
	h.set("{Series TitleTheYear}", func(t *tokenMatch) string {
		return truncate(seriesTitleYear(titleThe(s.Title), s.Year), t.CustomFormat)
	})
	h.set("{Series CleanTitleTheYear}", func(t *tokenMatch) string {
		return truncate(seriesCleanTitleTheYear(s.Title, s.Year), t.CustomFormat)
	})
	h.set("{Series TitleTheWithoutYear}", func(t *tokenMatch) string {
		return truncate(seriesTitleWithoutYear(titleThe(s.Title)), t.CustomFormat)
	})
	h.set("{Series CleanTitleTheWithoutYear}", func(t *tokenMatch) string {
		return truncate(cleanTitleThe(seriesTitleWithoutYear(s.Title), cleanTitle), t.CustomFormat)
	})
	h.set("{Series TitleFirstCharacter}", func(t *tokenMatch) string {
		return truncate(titleFirstCharacter(titleThe(s.Title)), t.CustomFormat)
	})
	h.set("{Series Year}", func(*tokenMatch) string { return strconv.Itoa(s.Year) })
}

func addSeriesIDTokens(h handlers, s *Series) {
	h.set("{ImdbId}", func(*tokenMatch) string { return s.ImdbID })
	h.set("{TvdbId}", func(*tokenMatch) string { return strconv.Itoa(s.TvdbID) })
	h.set("{TvMazeId}", func(*tokenMatch) string {
		if s.TvMazeID > 0 {
			return strconv.Itoa(s.TvMazeID)
		}
		return ""
	})
	h.set("{TmdbId}", func(*tokenMatch) string {
		if s.TmdbID > 0 {
			return strconv.Itoa(s.TmdbID)
		}
		return ""
	})
}

func addSeasonTokens(h handlers, season int) {
	h.set("{Season}", func(t *tokenMatch) string { return formatNumber(season, t.CustomFormat) })
}

// sonarrReplaceTokens is Sonarr's ReplaceTokens. escape keeps "{{"/"}}" and escapes braces in values, for the
// first of the two passes.
func sonarrReplaceTokens(pattern string, h handlers, c *Config, escape bool) string {
	return replaceFunc(sonarrTitleRegex, pattern, func(m regexp2.Match) string {
		if groupSuccess(&m, "escaped") {
			if escape {
				return m.String()
			}
			if m.String() == "{{" {
				return "{"
			}
			return "}"
		}
		tm := &tokenMatch{Prefix: group(&m, "prefix"), Separator: group(&m, "separator"), Suffix: group(&m, "suffix"),
			Token: group(&m, "token"), CustomFormat: group(&m, "customFormat")}
		v := h.get(tm.Token)(tm)
		if v == nil {
			return m.String()
		}
		text := applyCase(tm.Token, strings.TrimSpace(*v))
		if strings.TrimSpace(tm.Separator) != "" {
			text = strings.ReplaceAll(text, " ", tm.Separator)
		}
		text = cleanFileName(text, c)
		if strings.TrimSpace(text) != "" {
			text = tm.Prefix + text + tm.Suffix
		}
		if escape {
			text = strings.ReplaceAll(strings.ReplaceAll(text, "{", "{{"), "}", "}}")
		}
		return text
	})
}

type episodeFormat struct{ separator, episodeSeparator, episodePattern, seasonEpisodePattern string }

func episodeFormats(pattern string) []episodeFormat {
	var out []episodeFormat
	seen := map[string]bool{}
	for _, m := range allMatches(seasonEpisodePatternRegex, pattern) {
		f := episodeFormat{separator: group(m, "separator"), episodeSeparator: group(m, "episodeSeparator"),
			episodePattern: group(m, "episode"), seasonEpisodePattern: group(m, "seasonEpisode")}
		if !seen[f.seasonEpisodePattern] {
			seen[f.seasonEpisodePattern] = true
			out = append(out, f)
		}
	}
	return out
}

type absoluteFormat struct{ separator, absolutePattern string }

func absoluteFormats(pattern string) []absoluteFormat {
	var out []absoluteFormat
	seen := map[string]bool{}
	for _, m := range allMatches(absoluteEpisodePatternRegex, pattern) {
		sep := group(m, "separator")
		if strings.TrimSpace(sep) == "" {
			sep = "-"
		}
		f := absoluteFormat{separator: sep, absolutePattern: group(m, "absolute")}
		if !seen[f.absolutePattern] {
			seen[f.absolutePattern] = true
			out = append(out, f)
		}
	}
	return out
}

// numberToken is ReplaceNumberToken.
func numberToken(token string, v int) string {
	split := strings.Split(strings.Trim(token, "{}"), ":")
	if len(split) == 1 {
		return strconv.Itoa(v)
	}
	return formatNumber(v, split[1])
}

func replaceSeasonTokens(p string, season int) string {
	return replaceFunc(seasonRegex, p, func(m regexp2.Match) string { return numberToken(group(&m, "season"), season) })
}

func formatNumberTokens(base, format string, eps []Episode) string {
	var b strings.Builder
	for i, e := range eps {
		p := format
		if i == 0 {
			p = base
		}
		n := e.Number
		b.WriteString(replaceFunc(episodeRegex, p, func(m regexp2.Match) string { return numberToken(group(&m, "episode"), n) }))
	}
	return replaceSeasonTokens(b.String(), eps[0].Season)
}

func formatAbsoluteNumberTokens(base, format string, eps []Episode) string {
	var b strings.Builder
	for i, e := range eps {
		p := format
		if i == 0 {
			p = base
		}
		n := *e.Absolute
		b.WriteString(replaceFunc(absoluteEpisodeRegex, p, func(m regexp2.Match) string { return numberToken(group(&m, "absolute"), n) }))
	}
	return replaceSeasonTokens(b.String(), eps[0].Season)
}

func firstLast(eps []Episode) []Episode {
	out := []Episode{eps[0]}
	if len(eps) > 1 {
		out = append(out, eps[len(eps)-1])
	}
	return out
}

func addSeasonEpisodeNumberingTokens(pattern string, h handlers, eps []Episode, c *Config) string {
	for i, f := range episodeFormats(pattern) {
		var sep string
		switch c.MultiEpisodeStyle {
		case MultiDuplicate:
			sep = formatNumberTokens(f.seasonEpisodePattern, f.separator+f.seasonEpisodePattern, eps)
		case MultiRepeat:
			sep = formatNumberTokens(f.seasonEpisodePattern, f.episodeSeparator+f.episodePattern, eps)
		case MultiScene:
			sep = formatNumberTokens(f.seasonEpisodePattern, "-"+f.episodeSeparator+f.episodePattern, eps)
		case MultiRange:
			sep = formatNumberTokens(f.seasonEpisodePattern, "-"+f.episodePattern, firstLast(eps))
		case MultiPrefixedRange:
			sep = formatNumberTokens(f.seasonEpisodePattern, "-"+f.episodeSeparator+f.episodePattern, firstLast(eps))
		default:
			sep = formatNumberTokens(f.seasonEpisodePattern, "-"+f.episodePattern, eps)
		}
		token := fmt.Sprintf("{Season Episode%d}", i+1)
		pattern = strings.ReplaceAll(pattern, f.seasonEpisodePattern, token)
		value := sep
		h.set(token, func(*tokenMatch) string { return value })
	}
	addSeasonTokens(h, eps[0].Season)
	if len(eps) > 1 {
		h.set("{Episode}", func(t *tokenMatch) string {
			return formatNumber(eps[0].Number, t.CustomFormat) + "-" + formatNumber(eps[len(eps)-1].Number, t.CustomFormat)
		})
	} else {
		h.set("{Episode}", func(t *tokenMatch) string { return formatNumber(eps[0].Number, t.CustomFormat) })
	}
	return pattern
}

func allAbsolute(eps []Episode) bool {
	for _, e := range eps {
		if e.Absolute == nil {
			return false
		}
	}
	return true
}

func addAbsoluteNumberingTokens(pattern string, h handlers, s *Series, eps []Episode, c *Config) string {
	for i, f := range absoluteFormats(pattern) {
		if s.Type != SeriesAnime || !allAbsolute(eps) {
			pattern = strings.ReplaceAll(pattern, f.absolutePattern, "")
			continue
		}
		var v string
		switch c.MultiEpisodeStyle {
		case MultiDuplicate:
			v = formatAbsoluteNumberTokens(f.absolutePattern, f.separator+f.absolutePattern, eps)
		case MultiRepeat:
			rs := strings.TrimSpace(f.separator)
			if rs == "" {
				rs = " "
			}
			v = formatAbsoluteNumberTokens(f.absolutePattern, rs+f.absolutePattern, eps)
		case MultiScene:
			v = formatAbsoluteNumberTokens(f.absolutePattern, "-"+f.absolutePattern, eps)
		case MultiRange, MultiPrefixedRange:
			v = formatAbsoluteNumberTokens(f.absolutePattern, "-"+f.absolutePattern, firstLast(eps))
		default:
			v = formatAbsoluteNumberTokens(f.absolutePattern, "-"+f.absolutePattern, eps)
		}
		token := fmt.Sprintf("{Absolute Pattern%d}", i+1)
		pattern = strings.ReplaceAll(pattern, f.absolutePattern, token)
		value := v
		h.set(token, func(*tokenMatch) string { return value })
	}
	return pattern
}

func patternHasEpisodeIdentifier(p string) bool {
	return isMatch(seasonEpisodePatternRegex, p) || isMatch(absoluteEpisodePatternRegex, p) || isMatch(airDateRegex, p)
}

var episodeTitleTrim = " .?"

func episodeTitles(eps []Episode) []string {
	if len(eps) == 1 {
		return []string{strings.TrimRight(eps[0].Title, episodeTitleTrim)}
	}
	var titles []string
	for _, e := range eps {
		titles = append(titles, strings.TrimSpace(replace(multiPartCleanupRegex, strings.TrimRight(e.Title, episodeTitleTrim), "")))
	}
	titles = distinct(titles)
	allBlank := true
	for _, t := range titles {
		if strings.TrimSpace(t) != "" {
			allBlank = false
		}
	}
	if allBlank {
		titles = nil
		for _, e := range eps {
			titles = append(titles, strings.TrimRight(e.Title, episodeTitleTrim))
		}
		titles = distinct(titles)
	}
	return titles
}

// episodeTitle is GetEpisodeTitle.
func episodeTitle(titles []string, separator string, maxLength int, formatter string) string {
	if n := maxLengthFromFormatter(formatter); n > 0 && n < maxLength {
		maxLength = n
	}
	separator = " " + strings.TrimSpace(separator) + " "
	joined := strings.Join(titles, separator)
	if len(joined) <= maxLength {
		return joined
	}
	first := titles[0]
	if len(titles) >= 2 {
		last := titles[len(titles)-1]
		if len(first)+len(last)+3 <= maxLength {
			return strings.TrimRight(first, " .") + "{ellipsis}" + last
		}
	}
	if len(titles) > 1 && len(first)+3 <= maxLength {
		return strings.TrimRight(first, " .") + "{ellipsis}"
	}
	if len(titles) == 1 && len(first) <= maxLength {
		return first
	}
	return strings.TrimRight(truncateBytes(first, maxLength-3), " .") + "{ellipsis}"
}

// SeriesFileName is Sonarr's BuildFileName with the path limits BuildFilePath gives it: the file name with
// extension. seasonPath is the absolute season folder the file goes in (it bounds the name's length).
func SeriesFileName(c *Config, s *Series, eps []Episode, f *EpisodeFile, extension, seasonPath string) (name string, err error) {
	defer catch(&err)
	if len(eps) == 0 {
		return "", fmt.Errorf("naming: no episodes")
	}
	maxPath := maxFilePathLength - len(seasonPath) - 1
	if !c.Rename {
		return emptyName(episodeOriginalTitle(f, true), extension)
	}
	switch {
	case strings.TrimSpace(c.StandardEpisodeFormat) == "" && s.Type == SeriesStandard:
		return "", fmt.Errorf("naming: standard episode format cannot be empty")
	case strings.TrimSpace(c.DailyEpisodeFormat) == "" && s.Type == SeriesDaily:
		return "", fmt.Errorf("naming: daily episode format cannot be empty")
	case strings.TrimSpace(c.AnimeEpisodeFormat) == "" && s.Type == SeriesAnime:
		return "", fmt.Errorf("naming: anime episode format cannot be empty")
	}
	pattern := c.StandardEpisodeFormat
	eps = append([]Episode(nil), eps...)
	sort.SliceStable(eps, func(i, j int) bool {
		if eps[i].Season != eps[j].Season {
			return eps[i].Season < eps[j].Season
		}
		return eps[i].Number < eps[j].Number
	})
	if s.Type == SeriesDaily && eps[0].Season > 0 {
		pattern = c.DailyEpisodeFormat
	}
	if s.Type == SeriesAnime && (allAbsolute(eps) || !isMatch(absoluteEpisodeRegex, c.AnimeEpisodeFormat)) {
		pattern = c.AnimeEpisodeFormat
	}

	splits := splitPattern(pattern)
	var comps []string
	for i, sp := range splits {
		h := handlers{}
		hasID := patternHasEpisodeIdentifier(sp)
		sp = addSeasonEpisodeNumberingTokens(sp, h, eps, c)
		sp = addAbsoluteNumberingTokens(sp, h, s, eps, c)
		sp = strings.ReplaceAll(sp, "...", "{{ellipsis}}")

		addSeriesTokens(h, s)
		addSeriesIDTokens(h, s)
		airDate := "Unknown"
		if strings.TrimSpace(eps[0].AirDate) != "" {
			airDate = strings.ReplaceAll(eps[0].AirDate, "-", " ")
		}
		h.set("{Air Date}", func(*tokenMatch) string { return airDate })
		h.setNull("{Episode Title}")
		h.setNull("{Episode CleanTitle}")
		useCurrent := !hasID || f.ID == 0
		h.set("{Original Title}", func(*tokenMatch) string { return episodeOriginalTitle(f, useCurrent) })
		h.set("{Original Filename}", func(*tokenMatch) string { return originalFileName(f.RelativePath, f.Path, useCurrent) })
		h.set("{Release Group}", func(t *tokenMatch) string {
			if strings.TrimSpace(f.ReleaseGroup) == "" {
				return t.defaultValue("Sonarr")
			}
			return truncate(f.ReleaseGroup, t.CustomFormat)
		})
		h.set("{Release Hash}", func(*tokenMatch) string { return f.ReleaseHash })
		proper := ""
		if f.Quality.Version > 1 {
			proper = "Proper"
			if s.Type == SeriesAnime {
				proper = "v" + strconv.Itoa(f.Quality.Version)
			}
		}
		addQualityTokens(h, f.Quality, proper)
		addMediaInfoTokens(h, f.MediaInfo, true, false)
		addCustomFormatTokens(h, f.CustomFormats)

		comp := strings.TrimSpace(sonarrReplaceTokens(sp, h, c, true))
		maxSeg := maxFileNameLength
		if maxPath < maxSeg {
			maxSeg = maxPath
		}
		if i == len(splits)-1 {
			maxSeg -= len(extension)
		}
		maxTitle := maxSeg - lengthWithoutEpisodeTitle(comp, c)
		titles := episodeTitles(eps)
		h.set("{Episode Title}", func(t *tokenMatch) string { return episodeTitle(titles, "+", maxTitle, t.CustomFormat) })
		clean := make([]string, len(titles))
		for k, t := range titles {
			clean[k] = cleanTitle(t)
		}
		h.set("{Episode CleanTitle}", func(t *tokenMatch) string { return episodeTitle(clean, "and", maxTitle, t.CustomFormat) })
		comp = strings.TrimSpace(sonarrReplaceTokens(comp, h, c, false))
		comp = collapseSeparators(comp)
		comp = trimSeparators(comp)
		comp = strings.ReplaceAll(comp, "{ellipsis}", "...")
		comp = replaceReservedDeviceNames(comp)
		comps = append(comps, comp)
	}
	return emptyName(strings.Join(comps, "/"), extension)
}

func lengthWithoutEpisodeTitle(p string, c *Config) int {
	h := handlers{}
	h.set("{Episode Title}", func(*tokenMatch) string { return "" })
	h.set("{Episode CleanTitle}", func(*tokenMatch) string { return "" })
	h.set("{ellipsis}", func(*tokenMatch) string { return "..." })
	return len(sonarrReplaceTokens(p, h, c, false))
}

// episodeOriginalTitle is Sonarr's GetOriginalTitle (cleaned with NamingConfig.Default).
func episodeOriginalTitle(f *EpisodeFile, useCurrent bool) string {
	def := DefaultSeriesConfig()
	if strings.TrimSpace(f.SceneName) == "" {
		return cleanFileName(originalFileName(f.RelativePath, f.Path, useCurrent), &def)
	}
	return cleanFileName(f.SceneName, &def)
}

// SeriesFolder is Sonarr's GetSeriesFolder.
func SeriesFolder(c *Config, s *Series) (folder string, err error) {
	defer catch(&err)
	h := handlers{}
	addSeriesTokens(h, s)
	addSeriesIDTokens(h, s)
	name := sonarrReplaceTokens(c.SeriesFolderFormat, h, c, false)
	name = cleanFolderName(name)
	name = replaceReservedDeviceNames(name)
	return strings.ReplaceAll(name, "{ellipsis}", "..."), nil
}

// SeasonFolder is Sonarr's GetSeasonFolder, cleaned as BuildSeasonPath does.
func SeasonFolder(c *Config, s *Series, season int) (folder string, err error) {
	defer catch(&err)
	h := handlers{}
	addSeriesTokens(h, s)
	addSeriesIDTokens(h, s)
	addSeasonTokens(h, season)
	format := c.SeasonFolderFormat
	if season == 0 {
		format = c.SpecialsFolderFormat
	}
	name := sonarrReplaceTokens(format, h, c, false)
	name = cleanFolderName(name)
	name = replaceReservedDeviceNames(name)
	name = strings.ReplaceAll(name, "{ellipsis}", "...")
	def := DefaultSeriesConfig()
	return cleanFileName(name, &def), nil
}
