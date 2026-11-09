package tvdb

import (
	"context"
	"sort"

	"github.com/theatarr/theatarr/internal/metadata/anime"
)

// SeriesEpisodes returns a series' extended record and its episodes in the default order, every page, sorted by
// season and number, each as the series side stores it:
//
//   - Name and Overview: from the translated listing in the client's language (default English) when the series'
//     original language differs and the translation has them, else TVDB's own.
//   - AbsoluteNumber: the absolute order's number when the series has an absolute order, else the episode's own
//     absoluteNumber; nil when neither is above 0.
//   - airs fields: from the listing; with specialsAirs, a special without them is completed from its extended
//     record (one cached request per special; a failure leaves them empty rather than failing the series).
//
// An error means TVDB does not have the series (ErrNotFound) or is down with nothing cached.
func (c *Client) SeriesEpisodes(ctx context.Context, id int, specialsAirs bool) (*Series, []Episode, error) {
	s, err := c.Series(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	ttl := c.ttlFor(s.Status.Name)
	_, eps, err := c.Episodes(ctx, id, "default", "")
	if err != nil {
		return nil, nil, err
	}
	type text struct{ name, overview *string }
	tr := map[int]text{}
	if c.lang != "" && s.OriginalLanguage != c.lang {
		if _, teps, err := c.Episodes(ctx, id, "default", c.lang); err == nil {
			for _, e := range teps {
				tr[e.ID] = text{nonEmpty(e.Name), nonEmpty(e.Overview)}
			}
		} else if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
	}
	abs := map[int]int{}
	if s.HasSeasonType("absolute") {
		if _, aeps, err := c.Episodes(ctx, id, "absolute", ""); err == nil {
			for _, e := range aeps {
				if e.Number > 0 {
					abs[e.ID] = e.Number
				}
			}
		} else if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
	}
	for i := range eps {
		e := &eps[i]
		if specialsAirs && e.SeasonNumber == 0 && !e.hasAirs() && e.ID != 0 {
			if x, err := c.Episode(ctx, e.ID, ttl); err == nil {
				e.AirsAfterSeason, e.AirsBeforeSeason, e.AirsBeforeEpisode = x.AirsAfterSeason, x.AirsBeforeSeason, x.AirsBeforeEpisode
			} else if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
		}
		if t, ok := tr[e.ID]; ok {
			if t.name != nil {
				e.Name = t.name
			}
			if t.overview != nil {
				e.Overview = t.overview
			}
		}
		if n, ok := abs[e.ID]; ok {
			e.AbsoluteNumber = &n
		} else if e.AbsoluteNumber != nil && *e.AbsoluteNumber > 0 {
			n := *e.AbsoluteNumber
			e.AbsoluteNumber = &n
		} else {
			e.AbsoluteNumber = nil
		}
	}
	sort.SliceStable(eps, func(i, j int) bool {
		a, b := eps[i], eps[j]
		if a.SeasonNumber != b.SeasonNumber {
			return a.SeasonNumber < b.SeasonNumber
		}
		return a.Number < b.Number
	})
	return s, eps, nil
}

// Show implements anime.TVDB: the series in SkyHook's shape, from TVDB v4 ([Client.SeriesEpisodes]).
//
//   - Title: the series name in the client's language (default English), else TVDB's own.
//   - Episodes: the default order, every page; names and overviews from the translated listing when it has them.
//   - absoluteEpisodeNumber: the absolute order's number when the series has an absolute order, else the
//     episode's absoluteNumber.
//   - airs fields: from the listing, and for specials without them from the episode's extended record (one cached
//     request per special; a failure leaves them empty rather than failing the series).
//   - aniListIds: not known to TVDB; left empty.
//
// An error means TVDB does not have the series (404) or is down with nothing cached; anime.TVDBChain then asks
// the next backend.
func (c *Client) Show(ctx context.Context, id int) (*anime.Show, error) {
	s, eps, err := c.SeriesEpisodes(ctx, id, !c.opt.NoSpecialsAirs)
	if err != nil {
		return nil, err
	}
	title := s.TranslatedName(c.lang)
	show := &anime.Show{TVDBID: id, Title: &title, HasEpisodes: true, Episodes: make([]anime.ShowEpisode, 0, len(eps))}
	for i := range eps {
		e := &eps[i]
		show.Episodes = append(show.Episodes, anime.ShowEpisode{SeasonNumber: e.SeasonNumber, EpisodeNumber: e.Number,
			Title: nonEmpty(e.Name), AirDate: nonEmpty(e.Aired), Runtime: e.Runtime, Overview: nonEmpty(e.Overview),
			AbsoluteEpisodeNumber: e.AbsoluteNumber, AiredAfterSeasonNumber: e.AirsAfterSeason,
			AiredBeforeSeasonNumber: e.AirsBeforeSeason, AiredBeforeEpisodeNumber: e.AirsBeforeEpisode})
	}
	return show, nil
}

func nonEmpty(s *string) *string {
	if s == nil || *s == "" {
		return nil
	}
	v := *s
	return &v
}

var _ anime.TVDB = (*Client)(nil)
