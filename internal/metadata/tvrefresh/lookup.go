package tvrefresh

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/theatarr/theatarr/internal/metadata/tvdb"
)

// LookupResult is the metadata needed to add a series through a Sonarr lookup.
type LookupResult struct {
	Title, Status, IMDbID string
	Year, Runtime, TMDbID int
	Seasons               []int
}

type lookupEntry struct {
	result  LookupResult
	err     error
	expires time.Time
}

// SearchSeries finds TVDB series by name through the configured client's cached gateway path.
func (s *Service) SearchSeries(ctx context.Context, query string) ([]tvdb.SearchResult, error) {
	search, ok := s.tvdb.(interface {
		Search(context.Context, string, string) ([]tvdb.SearchResult, error)
	})
	if !s.Enabled() || !ok {
		return nil, ErrOff
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return search.Search(ctx, query, "series")
}

// LookupSeries reads an uncatalogued TVDB title through the refresh's metadata clients. The lookup is bounded
// to five seconds and cached for five minutes (failures for thirty seconds); it never writes catalog metadata.
func (s *Service) LookupSeries(ctx context.Context, tvdbID int) (LookupResult, error) {
	if !s.Enabled() {
		return LookupResult{}, ErrOff
	}
	s.lookupMu.Lock()
	cached, ok := s.lookups[tvdbID]
	s.lookupMu.Unlock()
	if ok && s.now().Before(cached.expires) {
		cached.result.Seasons = append([]int(nil), cached.result.Seasons...)
		return cached.result, cached.err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := s.lookupSeries(ctx, tvdbID)
	ttl := 30 * time.Second
	if err == nil {
		ttl = 5 * time.Minute
	}
	if ctx.Err() == nil {
		s.lookupMu.Lock()
		if s.lookups == nil {
			s.lookups = map[int]lookupEntry{}
		}
		for id, e := range s.lookups {
			if !s.now().Before(e.expires) || len(s.lookups) >= 64 {
				delete(s.lookups, id)
			}
		}
		s.lookups[tvdbID] = lookupEntry{result: result, err: err, expires: s.now().Add(ttl)}
		s.lookupMu.Unlock()
	}
	result.Seasons = append([]int(nil), result.Seasons...)
	return result, err
}

// Lookup needs only series records, not an episode fetch for every season. TVDB's default order is the one
// SeriesEpisodes uses at refresh time; alternate orders must not introduce seasons into Seerr's POST.
func (s *Service) lookupSeries(ctx context.Context, id int) (LookupResult, error) {
	var sh *show
	seasons := map[int]bool{}
	if s.tvdb != nil {
		ser, err := s.tvdb.Series(ctx, id)
		if err == nil {
			sh = fromTVDB(ser, nil)
			for _, season := range ser.Seasons {
				if ser.DefaultSeasonType > 0 && season.Type.ID == ser.DefaultSeasonType ||
					ser.DefaultSeasonType == 0 && season.Type.Type == "official" {
					seasons[season.Number] = true
				}
			}
		} else if !errors.Is(err, tvdb.ErrNotFound) {
			return LookupResult{}, err
		}
	}
	if sh == nil {
		if s.tmdb == nil {
			return LookupResult{}, errNotFound
		}
		tmdbID, err := s.tmdb.FindTVByTVDB(ctx, id)
		if err != nil {
			return LookupResult{}, err
		}
		ser, err := s.tmdb.TVShow(ctx, tmdbID)
		if err != nil {
			return LookupResult{}, err
		}
		sh = &show{name: ser.Name, status: tmdbStatus(ser.Status), tmdbID: ser.ID}
		if len(ser.FirstAirDate) >= 4 {
			sh.year, _ = strconv.Atoi(ser.FirstAirDate[:4])
		}
		if len(ser.EpisodeRunTime) > 0 {
			sh.runtime = ser.EpisodeRunTime[0]
		}
		for _, season := range ser.Seasons {
			seasons[season.SeasonNumber] = true
		}
	}
	result := LookupResult{Title: sh.name, Status: sh.status, IMDbID: sh.imdbID, Year: sh.year,
		Runtime: sh.runtime, TMDbID: sh.tmdbID, Seasons: []int{}}
	for season := range seasons {
		if season >= 0 {
			result.Seasons = append(result.Seasons, season)
		}
	}
	sort.Ints(result.Seasons)
	return result, nil
}
