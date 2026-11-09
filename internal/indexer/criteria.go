package indexer

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Sonarr's search criteria on a Torznab feed: NewznabRequestGenerator.GetSearchRequests per criteria (Sonarr v4;
// anime seasons as v5, see the router). The requests come in tiers: a later tier is asked only when every request
// of the earlier ones found nothing (HttpIndexerBase.FetchReleases).

var (
	beginningThe     = regexp.MustCompile(`(?i)^the\s`)
	specialCharacter = regexp.MustCompile("['.`´‘’]")
	repeatedPlus     = regexp.MustCompile(`\+{2,}`)
)

// CleanSceneTitle is SearchCriteriaBase.GetCleanSceneTitle: no leading "The", "&" as "and", no apostrophes or
// dots, every other non-word character a "+", accents removed.
func CleanSceneTitle(title string) string {
	s := beginningThe.ReplaceAllString(title, "")
	s = strings.ReplaceAll(s, "&", "and")
	s = specialCharacter.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		// .NET's \w: letters, decimal digits, connector punctuation and non-spacing marks.
		if unicode.IsLetter(r) || unicode.Is(unicode.Nd, r) || unicode.Is(unicode.Pc, r) || unicode.Is(unicode.Mn, r) {
			return r
		}
		return '+'
	}, s)
	s = repeatedPlus.ReplaceAllString(s, "+")
	s = removeAccent(s)
	return strings.Trim(s, "+ ")
}

// removeAccent is Sonarr's StringExtensions.RemoveAccent: drop non-spacing marks after NFD, then NFC.
func removeAccent(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return norm.NFC.String(b.String())
}

// IsEnglishTitle is SceneMappingService.IsEnglish: every character within Latin-1.
func IsEnglishTitle(s string) bool {
	for _, r := range s {
		if r > 255 {
			return false
		}
	}
	return true
}

// queryTitles are the criteria's titles as a text search sends them: the raw and clean titles for a "raw" search
// engine, the clean ones otherwise (SearchCriteriaBase.AllSceneTitles and CleanSceneTitles), with NewsnabifyTitle's
// "+" as a space.
func queryTitles(titles []string, raw bool) []string {
	var out []string
	seen := map[string]bool{}
	add := func(t string) {
		t = strings.ReplaceAll(t, "+", " ")
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	if raw {
		for _, t := range titles {
			add(t)
		}
	}
	for _, t := range titles {
		add(CleanSceneTitle(t))
	}
	return out
}

func containsFold(ts []string, s string) bool {
	for _, t := range ts {
		if strings.EqualFold(t, s) {
			return true
		}
	}
	return false
}

// newznabSeason is NewznabifySeasonNumber: season 0 as "00" (NNTMux reads 0 as none).
func newznabSeason(n int) string {
	if n == 0 {
		return "00"
	}
	return strconv.Itoa(n)
}

// criteriaRequests are the tiers of requests for a series query with criteria; none when the caps allow none.
func criteriaRequests(q Query, c *caps, cats string) [][]url.Values {
	req := func(t string, kv ...string) url.Values {
		p := url.Values{"t": {t}}
		if cats != "" {
			p.Set("cat", cats)
		}
		for i := 0; i+1 < len(kv); i += 2 {
			p.Set(kv[i], kv[i+1])
		}
		return p
	}
	// ids is AddTvIdPageableRequests without aggregate id searches: the first id the caps take.
	ids := func(kv ...string) []url.Values {
		switch {
		case q.TVDB != 0 && c.tv.has("tvdbid"):
			return []url.Values{req("tvsearch", append([]string{"tvdbid", strconv.Itoa(q.TVDB)}, kv...)...)}
		case q.IMDb != "" && c.tv.has("imdbid"):
			return []url.Values{req("tvsearch", append([]string{"imdbid", q.IMDb}, kv...)...)}
		case q.TMDb != 0 && c.tv.has("tmdbid"):
			return []url.Values{req("tvsearch", append([]string{"tmdbid", strconv.Itoa(q.TMDb)}, kv...)...)}
		}
		return nil
	}
	sceneTitles := q.SceneTitles
	if len(sceneTitles) == 0 && q.Title != "" {
		sceneTitles = []string{q.Title}
	}
	// titles is AddTitlePageableRequests: the title parameter with each term, else q with each query title.
	titles := func(kv ...string) []url.Values {
		var out []url.Values
		switch {
		case c.tv.has("title"):
			for _, t := range sceneTitles {
				out = append(out, req("tvsearch", append([]string{"title", t}, kv...)...))
			}
		case c.tv.has("q"):
			for _, t := range queryTitles(sceneTitles, c.tv.raw) {
				out = append(out, req("tvsearch", append([]string{"q", t}, kv...)...))
			}
		}
		return out
	}
	tvIDs := c.tv.has("tvdbid") || c.tv.has("imdbid") || c.tv.has("tmdbid")
	tvText := c.tv.has("q") || c.tv.has("title")
	standard := func(kv ...string) [][]url.Values {
		if !tvText && !tvIDs {
			return nil
		}
		var first, second []url.Values
		if q.Mode&SearchID != 0 || q.Mode == SearchDefault {
			first = append(first, ids(kv...)...)
		}
		if q.Mode&SearchTitle != 0 {
			first = append(first, titles(kv...)...)
		}
		if q.Mode == SearchDefault {
			second = titles(kv...)
		}
		return [][]url.Values{first, second}
	}
	episodeCaps := c.tv.has("season") && c.tv.has("ep")
	switch q.Criteria {
	case CriteriaEpisode:
		if !episodeCaps {
			return nil
		}
		return standard("season", newznabSeason(q.Season), "ep", strconv.Itoa(q.Episode))
	case CriteriaSeason:
		if !c.tv.has("season") {
			return nil
		}
		return standard("season", newznabSeason(q.Season))
	case CriteriaDaily:
		if !episodeCaps {
			return nil
		}
		// YYYY-MM-DD: season=YYYY, ep=MM/DD.
		return standard("season", q.AirDate[:4], "ep", q.AirDate[5:7]+"/"+q.AirDate[8:10])
	case CriteriaDailySeason:
		if !episodeCaps {
			return nil
		}
		return standard("season", strconv.Itoa(q.AirYear))
	case CriteriaAnimeEpisode:
		if !c.search.has("q") {
			return nil
		}
		var tier []url.Values
		// An episode without any absolute number would be searched as "00"; it gets only the standard format.
		abs := fmt.Sprintf("%02d", q.Absolute)
		if q.Absolute > 0 {
			tier = append(tier, ids("q", abs)...)
		}
		// AnimeStandardFormatSearch, on as Prowlarr syncs it.
		std := q.Season > 0 && q.Episode > 0 && episodeCaps
		if std {
			tier = append(tier, ids("season", newznabSeason(q.Season), "ep", strconv.Itoa(q.Episode))...)
		}
		for _, t := range queryTitles(sceneTitles, c.search.raw) {
			if q.Absolute > 0 {
				tier = append(tier, req("search", "q", t+" "+abs))
			}
			if std {
				tier = append(tier, req("tvsearch", "q", t, "season", newznabSeason(q.Season), "ep", strconv.Itoa(q.Episode)))
			}
		}
		return [][]url.Values{tier}
	case CriteriaAnimeSeason:
		if !c.search.has("q") || q.Season <= 0 {
			return nil
		}
		tier := ids("season", newznabSeason(q.Season))
		seasonTitles := queryTitles(q.SeasonTitles, c.search.raw)
		for _, t := range queryTitles(sceneTitles, c.search.raw) {
			if containsFold(seasonTitles, t) {
				// A season title already names the season.
				tier = append(tier, req("search", "q", t))
			} else {
				tier = append(tier, req("tvsearch", "q", t, "season", newznabSeason(q.Season)))
			}
		}
		return [][]url.Values{tier}
	}
	return nil
}
