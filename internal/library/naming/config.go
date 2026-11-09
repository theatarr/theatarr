package naming

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ColonReplacement is the arrs' ColonReplacementFormat.
type ColonReplacement int

const (
	ColonDelete ColonReplacement = iota
	ColonDash
	ColonSpaceDash
	ColonSpaceDashSpace
	ColonSmart
	ColonCustom // Sonarr only
)

var colonNames = map[string]ColonReplacement{"delete": ColonDelete, "dash": ColonDash, "spacedash": ColonSpaceDash,
	"spacedashspace": ColonSpaceDashSpace, "smart": ColonSmart, "custom": ColonCustom}

// MultiEpisodeStyle is Sonarr's MultiEpisodeStyle.
type MultiEpisodeStyle int

const (
	MultiExtend MultiEpisodeStyle = iota
	MultiDuplicate
	MultiRepeat
	MultiScene
	MultiRange
	MultiPrefixedRange
)

// Config is an arr's NamingConfig. Movie fields are Radarr's, the others Sonarr's.
type Config struct {
	Rename                   bool
	ReplaceIllegalCharacters bool
	ColonReplacement         ColonReplacement
	CustomColonReplacement   string

	StandardMovieFormat string
	MovieFolderFormat   string

	MultiEpisodeStyle     MultiEpisodeStyle
	StandardEpisodeFormat string
	DailyEpisodeFormat    string
	AnimeEpisodeFormat    string
	SeriesFolderFormat    string
	SeasonFolderFormat    string
	SpecialsFolderFormat  string
}

// DefaultMovieConfig is Radarr's NamingConfig.Default.
func DefaultMovieConfig() Config {
	return Config{Rename: false, ReplaceIllegalCharacters: true, ColonReplacement: ColonSmart,
		StandardMovieFormat: "{Movie Title} ({Release Year}) {Quality Full}",
		MovieFolderFormat:   "{Movie Title} ({Release Year})"}
}

// DefaultSeriesConfig is Sonarr's NamingConfig.Default.
func DefaultSeriesConfig() Config {
	return Config{Rename: false, ReplaceIllegalCharacters: true, ColonReplacement: ColonSmart,
		MultiEpisodeStyle:     MultiPrefixedRange,
		StandardEpisodeFormat: "{Series Title} - S{season:00}E{episode:00} - {Episode Title} {Quality Full}",
		DailyEpisodeFormat:    "{Series Title} - {Air-Date} - {Episode Title} {Quality Full}",
		AnimeEpisodeFormat:    "{Series Title} - S{season:00}E{episode:00} - {Episode Title} {Quality Full}",
		SeriesFolderFormat:    "{Series Title}",
		SeasonFolderFormat:    "Season {season}",
		SpecialsFolderFormat:  "Specials"}
}

// arrConfig is the GET /api/v3/config/naming body of either arr.
type arrConfig struct {
	RenameMovies                 *bool           `json:"renameMovies"`
	RenameEpisodes               *bool           `json:"renameEpisodes"`
	ReplaceIllegalCharacters     bool            `json:"replaceIllegalCharacters"`
	ColonReplacementFormat       json.RawMessage `json:"colonReplacementFormat"`
	CustomColonReplacementFormat string          `json:"customColonReplacementFormat"`
	StandardMovieFormat          string          `json:"standardMovieFormat"`
	MovieFolderFormat            string          `json:"movieFolderFormat"`
	MultiEpisodeStyle            int             `json:"multiEpisodeStyle"`
	StandardEpisodeFormat        string          `json:"standardEpisodeFormat"`
	DailyEpisodeFormat           string          `json:"dailyEpisodeFormat"`
	AnimeEpisodeFormat           string          `json:"animeEpisodeFormat"`
	SeriesFolderFormat           string          `json:"seriesFolderFormat"`
	SeasonFolderFormat           string          `json:"seasonFolderFormat"`
	SpecialsFolderFormat         string          `json:"specialsFolderFormat"`
}

// ParseArrConfig reads an arr's GET /api/v3/config/naming body. Radarr sends colonReplacementFormat as a name
// ("delete"), Sonarr as a number.
func ParseArrConfig(body []byte) (Config, error) {
	var a arrConfig
	if err := json.Unmarshal(body, &a); err != nil {
		return Config{}, fmt.Errorf("naming: %w", err)
	}
	c := Config{ReplaceIllegalCharacters: a.ReplaceIllegalCharacters, CustomColonReplacement: a.CustomColonReplacementFormat,
		StandardMovieFormat: a.StandardMovieFormat, MovieFolderFormat: a.MovieFolderFormat,
		MultiEpisodeStyle: MultiEpisodeStyle(a.MultiEpisodeStyle), StandardEpisodeFormat: a.StandardEpisodeFormat,
		DailyEpisodeFormat: a.DailyEpisodeFormat, AnimeEpisodeFormat: a.AnimeEpisodeFormat,
		SeriesFolderFormat: a.SeriesFolderFormat, SeasonFolderFormat: a.SeasonFolderFormat,
		SpecialsFolderFormat: a.SpecialsFolderFormat}
	switch {
	case a.RenameMovies != nil:
		c.Rename = *a.RenameMovies
	case a.RenameEpisodes != nil:
		c.Rename = *a.RenameEpisodes
	}
	if len(a.ColonReplacementFormat) > 0 {
		var n int
		var s string
		switch {
		case json.Unmarshal(a.ColonReplacementFormat, &n) == nil:
			c.ColonReplacement = ColonReplacement(n)
		case json.Unmarshal(a.ColonReplacementFormat, &s) == nil:
			v, ok := colonNames[strings.ToLower(s)]
			if !ok {
				return Config{}, fmt.Errorf("naming: unknown colonReplacementFormat %q", s)
			}
			c.ColonReplacement = v
		default:
			return Config{}, fmt.Errorf("naming: bad colonReplacementFormat %s", a.ColonReplacementFormat)
		}
	}
	return c, nil
}
