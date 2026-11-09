package tvrefresh

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/metadata/tvdb"
)

// Sources of a series' episodes.
const (
	SourceTVDB = "tvdb"
	SourceTMDb = "tmdb"
)

// Series statuses, as TVDB names them (TMDb's are mapped to these).
const (
	StatusContinuing = "Continuing"
	StatusEnded      = "Ended"
	StatusUpcoming   = "Upcoming"
)

// show is a series as the refresh stores it, from either source.
type show struct {
	source     string
	id         int
	name       string
	aliases    []string
	year       int
	runtime    int
	status     string
	airTime    string
	zone       string
	firstAired string
	imdbID     string
	tmdbID     int
	episodes   []remoteEp
}

// remoteEp is one episode at the source.
type remoteEp struct {
	extID          int
	season, number int
	absolute       *int
	name           string
	airDate        string
	airUTC         *time.Time
	runtime        *int
	airsBeforeS    *int
	airsBeforeE    *int
	airsAfterS     *int
}

func (e *remoteEp) slot() slot { return slot{e.season, e.number} }

type slot struct{ season, number int }

// errNotFound: no source has the series. It is recorded on the series, not retried.
var errNotFound = errors.New("not found")

// fromTVDB builds the show from TVDB's extended record and default-order episodes.
func fromTVDB(s *tvdb.Series, eps []tvdb.Episode) *show {
	sh := &show{source: SourceTVDB, id: s.ID, name: s.TranslatedName("eng"), status: s.Status.Name,
		airTime: strings.TrimSpace(s.AirsTime), firstAired: s.FirstAired}
	// Only English and the original language: release names use those (romaji aliases are tagged with the original
	// language), and every other translation only adds chances to collide with another series' name.
	named := func(lang string) bool { return lang == "eng" || lang == "" || lang == s.OriginalLanguage }
	sh.aliases = append(sh.aliases, s.Name)
	for _, a := range s.Aliases {
		if named(a.Language) {
			sh.aliases = append(sh.aliases, a.Name)
		}
	}
	if s.Translations != nil {
		for _, tr := range s.Translations.NameTranslations {
			if named(tr.Language) {
				sh.aliases = append(sh.aliases, tr.Name)
				sh.aliases = append(sh.aliases, tr.Aliases...)
			}
		}
	}
	if y, err := strconv.Atoi(s.Year); err == nil {
		sh.year = y
	}
	if s.AverageRuntime != nil {
		sh.runtime = *s.AverageRuntime
	}
	for _, r := range s.RemoteIDs {
		switch r.SourceName {
		case "IMDB":
			if strings.HasPrefix(r.ID, "tt") && sh.imdbID == "" {
				sh.imdbID = r.ID
			}
		case "TheMovieDB.com":
			if n, err := strconv.Atoi(r.ID); err == nil && n > 0 && sh.tmdbID == 0 {
				sh.tmdbID = n
			}
		}
	}
	var countries []string
	if s.LatestNetwork != nil {
		countries = append(countries, s.LatestNetwork.Country)
	}
	if s.OriginalNetwork != nil {
		countries = append(countries, s.OriginalNetwork.Country)
	}
	loc, name := zoneFor(append(countries, s.OriginalCountry)...)
	sh.zone = name
	for _, e := range eps {
		r := remoteEp{extID: e.ID, season: e.SeasonNumber, number: e.Number, absolute: e.AbsoluteNumber,
			runtime: e.Runtime, airsBeforeS: e.AirsBeforeSeason, airsBeforeE: e.AirsBeforeEpisode,
			airsAfterS: e.AirsAfterSeason}
		if e.Name != nil {
			r.name = strings.TrimSpace(*e.Name)
		}
		if e.Aired != nil {
			r.airDate = strings.TrimSpace(*e.Aired)
			r.airUTC = airUTC(r.airDate, sh.airTime, loc)
		}
		sh.episodes = append(sh.episodes, r)
	}
	return sh
}

// tmdbStatus maps TMDb's status to TVDB's.
func tmdbStatus(s string) string {
	switch s {
	case "Ended", "Canceled":
		return StatusEnded
	case "Planned", "Pilot", "In Production":
		return StatusUpcoming
	}
	return StatusContinuing
}

// fromTMDb fetches a series and its seasons from TMDb. TMDb has no airs time or absolute numbers: episodes air at
// midnight in the origin country's zone.
func fromTMDb(ctx context.Context, c TMDb, id int) (*show, error) {
	s, err := c.TVShow(ctx, id)
	if err != nil {
		return nil, err
	}
	loc, zone := zoneFor(s.OriginCountry...)
	sh := &show{source: SourceTMDb, id: s.ID, name: s.Name, status: tmdbStatus(s.Status), zone: zone,
		firstAired: s.FirstAirDate, tmdbID: s.ID}
	if len(s.FirstAirDate) >= 4 {
		sh.year, _ = strconv.Atoi(s.FirstAirDate[:4])
	}
	if len(s.EpisodeRunTime) > 0 {
		sh.runtime = s.EpisodeRunTime[0]
	}
	for _, se := range s.Seasons {
		if se.EpisodeCount <= 0 {
			continue
		}
		ss, err := c.TVSeasonEpisodes(ctx, id, se.SeasonNumber)
		if errors.Is(err, tmdb.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range ss.Episodes {
			r := remoteEp{extID: e.ID, season: e.SeasonNumber, number: e.EpisodeNumber, name: strings.TrimSpace(e.Name),
				airDate: strings.TrimSpace(e.AirDate), runtime: e.Runtime}
			r.airUTC = airUTC(r.airDate, "", loc)
			sh.episodes = append(sh.episodes, r)
		}
	}
	return sh, nil
}

// prepare applies what Sonarr's RefreshEpisodeService does to the remote list before storing it (ported from
// src/NzbDrone.Core/Tv/RefreshEpisodeService.cs @ cab419a): one episode per season and number; for an anime series
// one per absolute number, the highest season's (MapAbsoluteEpisodeNumbers); "TBA" for a missing name; episodes of a
// season airing on one day spread by the series runtime (AdjustMultiEpisodeAirTime, at most 3 outside specials);
// an ended series with no air dates at all takes its first-aired date (AdjustDirectToDvdAirDate).
func (sh *show) prepare(anime bool) {
	seen := map[slot]bool{}
	var eps []remoteEp
	for _, e := range sh.episodes {
		if seen[e.slot()] {
			continue
		}
		seen[e.slot()] = true
		eps = append(eps, e)
	}
	if anime {
		var withAbs, without []remoteEp
		for _, e := range eps {
			if e.absolute != nil {
				withAbs = append(withAbs, e)
			} else {
				without = append(without, e)
			}
		}
		sort.SliceStable(withAbs, func(i, j int) bool { return withAbs[i].season > withAbs[j].season })
		absSeen := map[int]bool{}
		eps = eps[:0:0]
		for _, e := range withAbs {
			if absSeen[*e.absolute] {
				continue
			}
			absSeen[*e.absolute] = true
			eps = append(eps, e)
		}
		eps = append(eps, without...)
	}
	sort.SliceStable(eps, func(i, j int) bool {
		if eps[i].season != eps[j].season {
			return eps[i].season < eps[j].season
		}
		return eps[i].number < eps[j].number
	})
	for i := range eps {
		if eps[i].name == "" {
			eps[i].name = "TBA"
		}
	}
	// AdjustMultiEpisodeAirTime.
	type day struct {
		season int
		date   string
	}
	groups := map[day][]int{}
	for i, e := range eps {
		if e.airUTC != nil {
			k := day{e.season, e.airDate}
			groups[k] = append(groups[k], i)
		}
	}
	for k, idx := range groups {
		if len(idx) < 2 || (k.season != 0 && len(idx) > 3) {
			continue
		}
		for n, i := range idx { // in season and number order already
			t := eps[i].airUTC.Add(time.Duration(sh.runtime*n) * time.Minute)
			eps[i].airUTC = &t
		}
	}
	// AdjustDirectToDvdAirDate.
	if sh.status == StatusEnded && sh.firstAired != "" {
		none := true
		for _, e := range eps {
			if e.airUTC != nil {
				none = false
				break
			}
		}
		if t, err := time.Parse("2006-01-02", sh.firstAired); none && err == nil {
			for i := range eps {
				eps[i].airDate, eps[i].airUTC = sh.firstAired, &t
			}
		}
	}
	sh.episodes = eps
}
