package live

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/metadata/anime"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
)

// URLs are where each source lives. Zero fields take the anime package's defaults (the public endpoints); the policy's
// metadata.sources[<key>].url overrides them (see FromPolicy in cmd/theatarr).
type URLs struct {
	AniList, ScudLee, Fribb, Anibridge, Manami, AniDB, SeaDex string
}

func (u URLs) or(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

// batches splits ids: sorted, de-duplicated, 50 per request.
func batches(ids []int) [][]int {
	seen := map[int]bool{}
	var u []int
	for _, x := range ids {
		if !seen[x] {
			seen[x] = true
			u = append(u, x)
		}
	}
	sort.Ints(u)
	var out [][]int
	for i := 0; i < len(u); i += 50 {
		out = append(out, u[i:min(i+50, len(u))])
	}
	return out
}

// AniList is the AniList GraphQL source (30 requests/min, batched 50 ids per request, fetched per franchise on
// demand: AniList's terms forbid mirroring it).
type AniList struct {
	F   *Fetcher
	URL string // default https://graphql.anilist.co
}

type anilistResp struct {
	Data struct {
		Page struct {
			Media []*anime.Media `json:"media"`
		} `json:"Page"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// anilistTTL: a day while any media in the batch is airing or announced, a month once all are finished.
func anilistTTL(b []byte) time.Duration {
	var r anilistResp
	if json.Unmarshal(b, &r) != nil {
		return 0
	}
	for _, m := range r.Data.Page.Media {
		if m.Status == nil || *m.Status != "FINISHED" && *m.Status != "CANCELLED" {
			return 24 * time.Hour
		}
	}
	return 30 * 24 * time.Hour
}

// Media implements anime.AniList.
func (a AniList) Media(ctx context.Context, ids []int) ([]*anime.Media, error) {
	u := a.URL
	if u == "" {
		u = "https://graphql.anilist.co"
	}
	var out []*anime.Media
	for _, chunk := range batches(ids) {
		body, _ := json.Marshal(map[string]any{"query": anime.AniListQuery, "variables": map[string]any{"ids": chunk}})
		b, err := a.F.Get(ctx, Request{Source: SourceAniList, Method: http.MethodPost, URL: u, Body: body,
			Header: http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json"}}, TTL: anilistTTL,
			Check: func(b []byte) error {
				var r anilistResp
				if err := json.Unmarshal(b, &r); err != nil {
					return err
				}
				if len(r.Errors) > 0 {
					return fmt.Errorf("anilist: %s", r.Errors[0].Message)
				}
				return nil
			}})
		if err != nil {
			return nil, err
		}
		var r anilistResp
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("anilist: %w", err)
		}
		out = append(out, r.Data.Page.Media...)
	}
	return out, nil
}

// Kitsu is Kitsu's JSON:API (keyless, 1 request/s).
type Kitsu struct{ F *Fetcher }

// Anime implements anime.Kitsu.
func (k Kitsu) Anime(ctx context.Context, id int) (*anime.KitsuDoc, error) {
	b, err := k.F.Get(ctx, Request{Source: SourceKitsu, URL: anime.KitsuURL(id),
		Header: http.Header{"Accept": {"application/vnd.api+json"}}})
	if err != nil {
		return nil, err
	}
	var d anime.KitsuDoc
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("kitsu: %w", err)
	}
	return &d, nil
}

// XEM is TheXEM (weekly).
type XEM struct{ F *Fetcher }

// Map implements anime.XEM.
func (x XEM) Map(ctx context.Context, tvdbID int) (*anime.XEMMap, error) {
	b, err := x.F.Get(ctx, Request{Source: SourceXEM, URL: anime.XEMURL(tvdbID)})
	if err != nil {
		return nil, err
	}
	var m anime.XEMMap
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("thexem: %w", err)
	}
	return &m, nil
}

// Lists downloads (daily, conditionally) and parses the four mapping lists. Anime-Lists and Fribb have no licence
// file: they are inputs only, kept in the cache and never redistributed.
func Lists(ctx context.Context, f *Fetcher, u URLs) (*anime.Lists, error) {
	l := &anime.Lists{}
	for _, s := range []struct {
		source, url string
		parse       func(io.Reader) error
	}{
		{SourceScudLee, u.or(u.ScudLee, anime.URLScudLee), l.ParseScudLee},
		{SourceFribb, u.or(u.Fribb, anime.URLFribb), l.ParseFribb},
		{SourceAnibridge, u.or(u.Anibridge, anime.URLAnibridge), l.ParseAnibridge},
		{SourceManami, u.or(u.Manami, anime.URLManami), l.ParseManami},
	} {
		b, err := f.Get(ctx, Request{Source: s.source, URL: s.url})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.source, err)
		}
		if err := s.parse(bytes.NewReader(b)); err != nil {
			return nil, err
		}
	}
	return l, nil
}

// SeaDex returns SeaDex's entries for an AniList id (PocketBase JSON, as internal/indexer reads it), daily.
func SeaDex(ctx context.Context, f *Fetcher, u URLs, anilistID int) (json.RawMessage, error) {
	q := url.Values{"filter": {"alID=" + strconv.Itoa(anilistID)}, "expand": {"trs"}, "page": {"1"}, "perPage": {"100"}}
	base := strings.TrimRight(u.or(u.SeaDex, anime.URLSeaDex), "/")
	b, err := f.Get(ctx, Request{Source: SourceSeaDex, URL: base + "/api/collections/entries/records?" + q.Encode()})
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

// TMDbMovies is the live movie lookup and title search, through our TMDb client (the golden outputs used
// Radarr's lookup, which proxies TMDb). Both are cached by the client (search answers for a week).
type TMDbMovies struct{ C *tmdb.Client }

// LookupTMDb implements anime.Movies.
func (t TMDbMovies) LookupTMDb(ctx context.Context, id int) (*anime.MovieInfo, error) {
	m, err := t.C.Movie(ctx, id)
	if errors.Is(err, tmdb.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	info := &anime.MovieInfo{TMDbID: m.ID, Title: &m.Title, OriginalTitle: &m.OriginalTitle}
	if y := m.Year(); y != 0 {
		info.Year = &y
	}
	if m.ReleaseDate != "" {
		info.InCinemas = &m.ReleaseDate
	}
	if c := m.BelongsToCollection; c != nil {
		name := c.Name
		info.Collection = &struct {
			Title *string `json:"title"`
		}{&name}
	}
	return info, nil
}

// Search implements anime.Movies with TMDb's search/movie (no year: the pipeline checks the date itself, within a
// week of the entry's). The result is shaped like Radarr's lookup: title, original title, year and the release date
// as inCinemas.
func (t TMDbMovies) Search(ctx context.Context, term string) ([]anime.MovieInfo, error) {
	res, err := t.C.SearchMovie(ctx, term, 0)
	if err != nil {
		return nil, err
	}
	out := make([]anime.MovieInfo, 0, len(res))
	for _, r := range res {
		info := anime.MovieInfo{TMDbID: r.ID, Title: &r.Title, OriginalTitle: &r.OriginalTitle}
		if y := r.Year(); y != 0 {
			info.Year = &y
		}
		if r.ReleaseDate != "" {
			info.InCinemas = &r.ReleaseDate
		}
		out = append(out, info)
	}
	return out, nil
}
