package sonarr

// Port of Sonarr src/NzbDrone.Core/Parser/Model/{ParsedEpisodeInfo,SeriesTitleInfo,ReleaseType}.cs @ cab419a.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ReleaseType is Sonarr's ReleaseType enum.
type ReleaseType int

const (
	ReleaseTypeUnknown ReleaseType = iota
	ReleaseTypeSingleEpisode
	ReleaseTypeMultiEpisode
	ReleaseTypeSeasonPack
)

var releaseTypeNames = [...]string{"unknown", "singleEpisode", "multiEpisode", "seasonPack"}

func (t ReleaseType) String() string {
	if int(t) < len(releaseTypeNames) {
		return releaseTypeNames[t]
	}
	return fmt.Sprint(int(t))
}

func (t ReleaseType) MarshalJSON() ([]byte, error) { return json.Marshal(t.String()) }

func (t *ReleaseType) UnmarshalJSON(b []byte) error {
	return unmarshalEnum(b, releaseTypeNames[:], (*int)(t))
}

// SeriesTitleInfo is Sonarr's SeriesTitleInfo. AllTitles is null unless the title has several components.
type SeriesTitleInfo struct {
	Title            string   `json:"title"`
	TitleWithoutYear string   `json:"titleWithoutYear"`
	Year             int      `json:"year"`
	AllTitles        []string `json:"allTitles,omitempty"`
}

// Decimal is a .NET decimal as the parser produces it (SpecialAbsoluteEpisodeNumbers). It keeps the scale of the
// parsed text, as .NET does: "06.50" is 6.50 and marshals as 6.50.
type Decimal struct {
	text string
}

// ParseDecimal parses an invariant-culture decimal number like decimal.TryParse(NumberStyles.Float).
func parseDecimalText(s string) (Decimal, bool) {
	s = strings.TrimFunc(s, isDotNetNumberWhite)
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	intPart, frac, hasDot := strings.Cut(s, ".")
	if intPart == "" && frac == "" {
		return Decimal{}, false
	}
	for _, part := range []string{intPart, frac} {
		for i := 0; i < len(part); i++ {
			if part[i] < '0' || part[i] > '9' {
				return Decimal{}, false
			}
		}
	}
	intPart = strings.TrimLeft(intPart, "0")
	if intPart == "" {
		intPart = "0"
	}
	text := intPart
	if hasDot && frac != "" {
		text += "." + frac
	}
	if neg && strings.Trim(text, "0.") != "" {
		text = "-" + text
	}
	return Decimal{text: text}, true
}

func (d Decimal) Float() float64 {
	f, _ := strconv.ParseFloat(d.text, 64)
	return f
}

// IsInteger is decimal % 1 == 0.
func (d Decimal) IsInteger() bool {
	_, frac, ok := strings.Cut(d.text, ".")
	return !ok || strings.Trim(frac, "0") == ""
}

func (d Decimal) String() string {
	if d.text == "" {
		return "0"
	}
	return d.text
}

func (d Decimal) MarshalJSON() ([]byte, error) { return []byte(d.String()), nil }

func (d *Decimal) UnmarshalJSON(b []byte) error {
	v, ok := parseDecimalText(string(b))
	if !ok {
		return fmt.Errorf("bad decimal %s", b)
	}
	*d = v
	return nil
}

// ParsedEpisodeInfo is Sonarr's ParsedEpisodeInfo. It marshals like parsedEpisodeInfo in GET /api/v3/parse:
// camelCase names, null fields (airDate, dailyPart, releaseGroup, seriesTitleInfo.allTitles) left out, and the
// computed properties appended.
type ParsedEpisodeInfo struct {
	ReleaseTitle                  string           `json:"releaseTitle"`
	SeriesTitle                   string           `json:"seriesTitle"`
	SeriesTitleInfo               *SeriesTitleInfo `json:"seriesTitleInfo,omitempty"`
	Quality                       QualityModel     `json:"quality"`
	SeasonNumber                  int              `json:"seasonNumber"`
	EpisodeNumbers                []int            `json:"episodeNumbers"`
	AbsoluteEpisodeNumbers        []int            `json:"absoluteEpisodeNumbers"`
	SpecialAbsoluteEpisodeNumbers []Decimal        `json:"specialAbsoluteEpisodeNumbers"`
	AirDate                       *string          `json:"airDate,omitempty"`
	Languages                     []Language       `json:"languages"`
	FullSeason                    bool             `json:"fullSeason"`
	IsPartialSeason               bool             `json:"isPartialSeason"`
	IsMultiSeason                 bool             `json:"isMultiSeason"`
	IsSeasonExtra                 bool             `json:"isSeasonExtra"`
	IsSplitEpisode                bool             `json:"isSplitEpisode"`
	IsMiniSeries                  bool             `json:"isMiniSeries"`
	Special                       bool             `json:"special"`
	ReleaseGroup                  *string          `json:"releaseGroup,omitempty"`
	ReleaseHash                   string           `json:"releaseHash"`
	SeasonPart                    int              `json:"seasonPart"`
	ReleaseTokens                 string           `json:"releaseTokens"`
	DailyPart                     *int             `json:"dailyPart,omitempty"`

	// Parser is the Sonarr version whose parser produced the value: V4 (the zero value), V5 or V4Plus. The fields
	// below and the computed properties follow it; V4 and V4Plus values have the v4 shape and marshal as before.
	Parser Version `json:"-"`
	// SeasonNumbers is Sonarr v5's ParsedEpisodeInfo.SeasonNumbers (V5 only; nil under V4): every season of a
	// multi-season pack, [0] for daily and absolute-only releases, empty for a season title. SeasonNumber is then
	// its first element (0 when empty) and IsMultiSeason is len(SeasonNumbers) > 1.
	SeasonNumbers []int `json:"-"`
	// IsSeasonTitle is Sonarr v5's ParsedEpisodeInfo.IsSeasonTitle: an anime season pack named by its season's own
	// title (ParseSeasonTitle), mapped to a season through scene mappings.
	IsSeasonTitle bool `json:"-"`
}

// NewParsedEpisodeInfo is new ParsedEpisodeInfo(): empty arrays, an empty language list and an Unknown quality.
func NewParsedEpisodeInfo() *ParsedEpisodeInfo {
	return &ParsedEpisodeInfo{
		EpisodeNumbers:                []int{},
		AbsoluteEpisodeNumbers:        []int{},
		SpecialAbsoluteEpisodeNumbers: []Decimal{},
		Languages:                     []Language{},
		Quality:                       NewQualityModel(),
	}
}

func isNullOrWhiteSpace(p *string) bool { return p == nil || strings.TrimSpace(*p) == "" }

// IsDaily is ParsedEpisodeInfo.IsDaily.
func (p *ParsedEpisodeInfo) IsDaily() bool { return !isNullOrWhiteSpace(p.AirDate) }

// IsAbsoluteNumbering is ParsedEpisodeInfo.IsAbsoluteNumbering.
func (p *ParsedEpisodeInfo) IsAbsoluteNumbering() bool { return len(p.AbsoluteEpisodeNumbers) > 0 }

// IsPossibleSpecialEpisode is ParsedEpisodeInfo.IsPossibleSpecialEpisode.
func (p *ParsedEpisodeInfo) IsPossibleSpecialEpisode() bool {
	seriesTitleBlank := strings.TrimSpace(p.SeriesTitle) == ""
	seasonZero := p.SeasonNumber == 0
	if p.Parser == V5 {
		seasonZero = p.firstSeason(-1) == 0 // SeasonNumbers.FirstOrDefault(-1) == 0
	}
	return (isNullOrWhiteSpace(p.AirDate) && seriesTitleBlank && (len(p.EpisodeNumbers) == 0 || seasonZero)) ||
		(!seriesTitleBlank && p.Special) ||
		(len(p.EpisodeNumbers) == 1 && p.EpisodeNumbers[0] == 0)
}

// IsPossibleSceneSeasonSpecial is ParsedEpisodeInfo.IsPossibleSceneSeasonSpecial.
func (p *ParsedEpisodeInfo) IsPossibleSceneSeasonSpecial() bool {
	if p.Parser == V5 {
		return p.firstSeason(0) != 0 && len(p.EpisodeNumbers) == 1 && p.EpisodeNumbers[0] == 0
	}
	return p.SeasonNumber != 0 && len(p.EpisodeNumbers) == 1 && p.EpisodeNumbers[0] == 0
}

// firstSeason is SeasonNumbers.FirstOrDefault(def) (V5).
func (p *ParsedEpisodeInfo) firstSeason(def int) int {
	if len(p.SeasonNumbers) == 0 {
		return def
	}
	return p.SeasonNumbers[0]
}

// setSeasonNumbers sets Sonarr v5's SeasonNumbers and keeps the V4 fields that derive from it in step: SeasonNumber
// (the first season, 0 when none) and IsMultiSeason.
func (p *ParsedEpisodeInfo) setSeasonNumbers(seasons []int) {
	p.SeasonNumbers = seasons
	p.SeasonNumber = p.firstSeason(0)
	p.IsMultiSeason = len(seasons) > 1
}

// ReleaseType is ParsedEpisodeInfo.ReleaseType.
func (p *ParsedEpisodeInfo) ReleaseType() ReleaseType {
	switch {
	case len(p.EpisodeNumbers) > 1 || len(p.AbsoluteEpisodeNumbers) > 1:
		return ReleaseTypeMultiEpisode
	case len(p.EpisodeNumbers) == 1 || len(p.AbsoluteEpisodeNumbers) == 1:
		return ReleaseTypeSingleEpisode
	case p.FullSeason:
		return ReleaseTypeSeasonPack
	}
	return ReleaseTypeUnknown
}

func (p *ParsedEpisodeInfo) MarshalJSON() ([]byte, error) {
	type plain ParsedEpisodeInfo
	q := *p
	if q.EpisodeNumbers == nil {
		q.EpisodeNumbers = []int{}
	}
	if q.AbsoluteEpisodeNumbers == nil {
		q.AbsoluteEpisodeNumbers = []int{}
	}
	if q.SpecialAbsoluteEpisodeNumbers == nil {
		q.SpecialAbsoluteEpisodeNumbers = []Decimal{}
	}
	if q.Languages == nil {
		q.Languages = []Language{}
	}
	if p.Parser == V5 {
		// Sonarr v5's ParsedEpisodeInfoResource (GET /api/v3/parse): seasonNumbers and isSeasonTitle besides the V4
		// fields, seasonNumber being the first season or -1.
		if q.SeasonNumbers == nil {
			q.SeasonNumbers = []int{}
		}
		q.SeasonNumber = p.firstSeason(-1)
		return json.Marshal(struct {
			*plain
			SeasonNumbers                []int       `json:"seasonNumbers"`
			IsSeasonTitle                bool        `json:"isSeasonTitle"`
			IsDaily                      bool        `json:"isDaily"`
			IsAbsoluteNumbering          bool        `json:"isAbsoluteNumbering"`
			IsPossibleSpecialEpisode     bool        `json:"isPossibleSpecialEpisode"`
			IsPossibleSceneSeasonSpecial bool        `json:"isPossibleSceneSeasonSpecial"`
			ReleaseType                  ReleaseType `json:"releaseType"`
		}{
			(*plain)(&q),
			q.SeasonNumbers,
			p.IsSeasonTitle,
			p.IsDaily(),
			p.IsAbsoluteNumbering(),
			p.IsPossibleSpecialEpisode(),
			p.IsPossibleSceneSeasonSpecial(),
			p.ReleaseType(),
		})
	}
	return json.Marshal(struct {
		*plain
		IsDaily                      bool        `json:"isDaily"`
		IsAbsoluteNumbering          bool        `json:"isAbsoluteNumbering"`
		IsPossibleSpecialEpisode     bool        `json:"isPossibleSpecialEpisode"`
		IsPossibleSceneSeasonSpecial bool        `json:"isPossibleSceneSeasonSpecial"`
		ReleaseType                  ReleaseType `json:"releaseType"`
	}{
		(*plain)(&q),
		p.IsDaily(),
		p.IsAbsoluteNumbering(),
		p.IsPossibleSpecialEpisode(),
		p.IsPossibleSceneSeasonSpecial(),
		p.ReleaseType(),
	})
}

// String is ParsedEpisodeInfo.ToString.
func (p *ParsedEpisodeInfo) String() string {
	episode := "[Unknown Episode]"
	switch {
	case p.IsDaily() && len(p.EpisodeNumbers) == 0:
		episode = *p.AirDate
	case p.FullSeason && p.Parser == V5:
		switch {
		case len(p.SeasonNumbers) > 1:
			episode = fmt.Sprintf("Season %02d-%02d", p.SeasonNumbers[0], p.SeasonNumbers[len(p.SeasonNumbers)-1])
		case len(p.SeasonNumbers) == 1:
			episode = fmt.Sprintf("Season %02d", p.SeasonNumbers[0])
		default:
			episode = "[Unknown Season]"
		}
	case p.FullSeason:
		episode = fmt.Sprintf("Season %02d", p.SeasonNumber)
	case len(p.EpisodeNumbers) > 0:
		parts := make([]string, len(p.EpisodeNumbers))
		for i, e := range p.EpisodeNumbers {
			parts[i] = fmt.Sprintf("%02d", e)
		}
		episode = fmt.Sprintf("S%02dE%s", p.SeasonNumber, strings.Join(parts, "-"))
	case len(p.AbsoluteEpisodeNumbers) > 0:
		parts := make([]string, len(p.AbsoluteEpisodeNumbers))
		for i, e := range p.AbsoluteEpisodeNumbers {
			parts[i] = fmt.Sprintf("%03d", e)
		}
		episode = strings.Join(parts, "-")
	case p.Special:
		if p.SeasonNumber != 0 {
			episode = fmt.Sprintf("[Unknown Season %02d Special]", p.SeasonNumber)
		} else {
			episode = "[Unknown Special]"
		}
	}
	return fmt.Sprintf("%s - %s %s", p.SeriesTitle, episode, p.Quality)
}
