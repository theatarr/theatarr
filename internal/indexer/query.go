package indexer

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// QueryType is what a search looks for.
type QueryType string

const (
	// QueryMovie searches for a movie by its ids, title and year.
	QueryMovie QueryType = "movie"
	// QuerySeries searches for a series: a season (Episode 0) or one episode. Season 0 means the whole series where
	// the source allows it.
	QuerySeries QueryType = "series"
	// QueryText is a free-text search (Title).
	QueryText QueryType = "text"
	// QueryRSS asks for the source's recent releases.
	QueryRSS QueryType = "rss"
)

// Query is one search. Sources use the ids they understand and fall back to the title.
type Query struct {
	Type QueryType
	// Title is the title to search for (or the text, for QueryText). Year helps movies.
	Title string
	Year  int
	// Aliases are other titles (later: one query per alias and numbering scheme, docs/design/indexers.md).
	Aliases []string
	// IMDb is "tt" plus digits.
	IMDb    string
	TMDb    int
	TVDB    int
	AniList int
	Season  int
	Episode int
	Anime   bool
	// Categories override the Newznab categories sent to Torznab feeds.
	Categories []int
	// RSSOffset and RSSLimit page a recent-release feed during bounded catch-up.
	RSSOffset int
	RSSLimit  int
	// RSSFeed names one of the indexer's recent-release feeds (Service.RSSFeeds); empty for its single feed.
	RSSFeed string
	// EpisodeCounts is episodes per season, from our metadata. Torrentio reports a season pack with the size of
	// one file; with the counts the adapter scales it to the pack.
	EpisodeCounts map[int]int

	// Criteria makes a series query one of Sonarr's search criteria (ReleaseSearchService), which Prowlarr's feeds
	// are asked as Sonarr's NewznabRequestGenerator asks them: Season and Episode are then the scene numbers,
	// Absolute an anime episode's (scene) absolute number, AirDate (YYYY-MM-DD) a daily episode and AirYear a daily
	// season. SceneTitles are the criteria's search terms (the scene mappings' names, the series title included);
	// SeasonTitles those of them that name the season themselves (Sonarr v5's SeasonSceneTitles). Mode is the
	// mapping's search mode. Torznab and keyword (Cardigann) adapters read them; empty Criteria is a plain series query.
	Criteria     Criteria
	Absolute     int
	AirDate      string
	AirYear      int
	SceneTitles  []string
	SeasonTitles []string
	Mode         SearchMode
}

// Criteria is Sonarr's SearchCriteriaBase subclass of a series query.
type Criteria string

const (
	CriteriaEpisode      Criteria = "episode"      // SingleEpisodeSearchCriteria
	CriteriaSeason       Criteria = "season"       // SeasonSearchCriteria
	CriteriaDaily        Criteria = "daily"        // DailyEpisodeSearchCriteria
	CriteriaDailySeason  Criteria = "daily-season" // DailySeasonSearchCriteria
	CriteriaAnimeEpisode Criteria = "anime-episode"
	CriteriaAnimeSeason  Criteria = "anime-season"
)

// SearchMode is Sonarr's SearchMode flags of a scene mapping: the default searches by id first and by title only
// when the ids find nothing; SearchID and SearchTitle search only (or also) that way.
type SearchMode int

const (
	SearchDefault SearchMode = 0
	SearchID      SearchMode = 1
	SearchTitle   SearchMode = 2
)

var airDateRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

var imdbRE = regexp.MustCompile(`^tt\d{5,10}$`)

// ErrBadQuery wraps every query validation error.
var ErrBadQuery = errors.New("indexer: bad query")

// Validate checks the query is well-formed. It normalises IMDb ids written without "tt".
func (q *Query) Validate() error {
	q.Title = strings.TrimSpace(q.Title)
	if q.IMDb != "" {
		q.IMDb = strings.ToLower(strings.TrimSpace(q.IMDb))
		if !strings.HasPrefix(q.IMDb, "tt") {
			q.IMDb = "tt" + q.IMDb
		}
		if !imdbRE.MatchString(q.IMDb) {
			return fmt.Errorf("%w: IMDb id %q", ErrBadQuery, q.IMDb)
		}
	}
	if q.Season < 0 || q.Episode < 0 || q.Year < 0 || q.TMDb < 0 || q.TVDB < 0 || q.AniList < 0 || q.Absolute < 0 ||
		q.AirYear < 0 {
		return fmt.Errorf("%w: negative number", ErrBadQuery)
	}
	if q.Criteria != "" || q.Absolute != 0 || q.AirDate != "" || q.AirYear != 0 {
		if q.Type != QuerySeries {
			return fmt.Errorf("%w: only a series search has search criteria", ErrBadQuery)
		}
		if err := q.validateCriteria(); err != nil {
			return err
		}
	}
	switch q.Type {
	case QueryMovie:
		if q.IMDb == "" && q.TMDb == 0 && q.AniList == 0 && q.Title == "" {
			return fmt.Errorf("%w: a movie search needs an id or a title", ErrBadQuery)
		}
		if q.Season != 0 || q.Episode != 0 {
			return fmt.Errorf("%w: a movie search has no season or episode", ErrBadQuery)
		}
	case QuerySeries:
		if q.IMDb == "" && q.TVDB == 0 && q.TMDb == 0 && q.AniList == 0 && q.Title == "" {
			return fmt.Errorf("%w: a series search needs an id or a title", ErrBadQuery)
		}
		// A scene-numbered special (S00E05) has an episode in season 0.
		if q.Episode != 0 && q.Season == 0 && q.Criteria != CriteriaEpisode && q.Criteria != CriteriaAnimeEpisode {
			return fmt.Errorf("%w: an episode needs a season", ErrBadQuery)
		}
	case QueryText:
		if q.Title == "" {
			return fmt.Errorf("%w: a text search needs a title", ErrBadQuery)
		}
	case QueryRSS:
	default:
		return fmt.Errorf("%w: type %q", ErrBadQuery, q.Type)
	}
	return nil
}

func (q *Query) validateCriteria() error {
	switch q.Criteria {
	case CriteriaEpisode, CriteriaSeason, CriteriaAnimeEpisode, CriteriaAnimeSeason:
		if q.AirDate != "" || q.AirYear != 0 {
			return fmt.Errorf("%w: %s criteria have no air date", ErrBadQuery, q.Criteria)
		}
	case CriteriaDaily:
		if !airDateRE.MatchString(q.AirDate) {
			return fmt.Errorf("%w: daily criteria need an air date YYYY-MM-DD (got %q)", ErrBadQuery, q.AirDate)
		}
	case CriteriaDailySeason:
		if q.AirYear == 0 {
			return fmt.Errorf("%w: daily season criteria need an air year", ErrBadQuery)
		}
	default:
		return fmt.Errorf("%w: criteria %q", ErrBadQuery, q.Criteria)
	}
	if q.Absolute != 0 && q.Criteria != CriteriaAnimeEpisode {
		return fmt.Errorf("%w: only anime episode criteria have an absolute number", ErrBadQuery)
	}
	if q.Mode < SearchDefault || q.Mode > SearchID|SearchTitle {
		return fmt.Errorf("%w: search mode %d", ErrBadQuery, q.Mode)
	}
	return nil
}

// Key is the normalised query: two queries with the same key make the same upstream requests.
func (q Query) Key() string {
	var b strings.Builder
	b.WriteString(string(q.Type))
	add := func(k, v string) {
		if v != "" && v != "0" {
			b.WriteString("|" + k + "=" + v)
		}
	}
	add("t", normTitle(q.Title))
	add("y", strconv.Itoa(q.Year))
	add("imdb", q.IMDb)
	add("tmdb", strconv.Itoa(q.TMDb))
	add("tvdb", strconv.Itoa(q.TVDB))
	add("al", strconv.Itoa(q.AniList))
	add("s", strconv.Itoa(q.Season))
	add("e", strconv.Itoa(q.Episode))
	if q.Anime {
		add("anime", "1")
	}
	if len(q.Categories) > 0 {
		c := append([]int(nil), q.Categories...)
		sort.Ints(c)
		add("cat", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(c)), ","), "[]"))
	}
	if q.RSSOffset > 0 {
		add("rss_offset", strconv.Itoa(q.RSSOffset))
	}
	if q.RSSLimit > 0 {
		add("rss_limit", strconv.Itoa(q.RSSLimit))
	}
	add("rss_feed", q.RSSFeed)
	if len(q.EpisodeCounts) > 0 {
		seasons := make([]int, 0, len(q.EpisodeCounts))
		for s := range q.EpisodeCounts {
			seasons = append(seasons, s)
		}
		sort.Ints(seasons)
		parts := make([]string, len(seasons))
		for i, s := range seasons {
			parts[i] = fmt.Sprintf("%d:%d", s, q.EpisodeCounts[s])
		}
		add("eps", strings.Join(parts, ","))
	}
	if q.Criteria != "" {
		add("c", string(q.Criteria))
		add("abs", strconv.Itoa(q.Absolute))
		add("date", q.AirDate)
		add("ay", strconv.Itoa(q.AirYear))
		add("mode", strconv.Itoa(int(q.Mode)))
		add("st", joinTitles(q.SceneTitles))
		add("sst", joinTitles(q.SeasonTitles))
	}
	return b.String()
}

func normTitle(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// joinTitles is the titles normalised, distinct and sorted: the order the router collected them in does not change
// the requests.
func joinTitles(ts []string) string {
	seen := map[string]bool{}
	var out []string
	for _, t := range ts {
		if n := normTitle(t); n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return strings.Join(out, "\x1f")
}

// imdbDigits is the id without "tt" (Radarr sends Torznab imdbid that way).
func (q Query) imdbDigits() string { return strings.TrimPrefix(q.IMDb, "tt") }

// categoryList is defaultCategories as the Newznab cat parameter.
func (q Query) categoryList(c *caps) string {
	cats := q.defaultCategories()
	var s []string
	for _, cat := range cats {
		if c != nil && c.categories != nil && !c.categories[cat] {
			continue
		}
		s = append(s, strconv.Itoa(cat))
	}
	return strings.Join(s, ",")
}

// defaultCategories are the Newznab categories for a query: movies 2000, TV 5000, plus anime 5070 (Prowlarr's
// anime sync categories).
func (q Query) defaultCategories() []int {
	if len(q.Categories) > 0 {
		return q.Categories
	}
	switch {
	case q.Type == QueryMovie:
		return []int{2000}
	case q.Type == QuerySeries && q.Anime:
		return []int{5000, 5070}
	case q.Type == QuerySeries:
		return []int{5000}
	}
	return nil
}
