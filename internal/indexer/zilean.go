package indexer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// zilean searches Zilean's DMM hash-list index through GET /dmm/filtered.
//
// Request and response shapes are from Zilean's API (iPromKnight/zilean: the filtered search endpoint takes Query,
// Season, Episode, Year, Language, Resolution and ImdbId; it answers a JSON array of TorrentInfo with snake_case
// fields such as raw_title, info_hash, size, imdb_id, ingested_at). Zilean answers at most 200 results per query
// and knows nothing about debrid caches.
//
// Zilean's IMDb lookup misses most releases, so an IMDb search is also run by title and the two are merged by hash,
// (Stranger Things: 26 results by IMDb, 204 with the title search).
type zilean struct {
	base
	endpoint *url.URL
}

func newZilean(b base) (*zilean, error) {
	u, err := url.Parse(b.ix.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("indexers[%s].url: not an http(s) URL", b.key)
	}
	if u.RawQuery != "" || u.User != nil {
		return nil, fmt.Errorf("indexers[%s].url: no query string or credentials", b.key)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/dmm/filtered"
	return &zilean{base: b, endpoint: u}, nil
}

func (z *zilean) Supports(q Query) bool {
	switch q.Type {
	case QueryMovie, QuerySeries:
		return q.IMDb != "" || q.Title != ""
	case QueryText:
		return q.Title != ""
	}
	return false
}

type zileanTorrent struct {
	RawTitle   string          `json:"raw_title"`
	InfoHash   string          `json:"info_hash"`
	Size       json.RawMessage `json:"size"`
	IMDbID     string          `json:"imdb_id"`
	IngestedAt string          `json:"ingested_at"`
	Category   string          `json:"category"`
	Seasons    []int           `json:"seasons"`
	Episodes   []int           `json:"episodes"`
	Complete   bool            `json:"complete"`
}

func (z *zilean) Search(ctx context.Context, q Query) (*Result, error) {
	if !z.Supports(q) {
		return nil, fmt.Errorf("%w: zilean needs an IMDb id or a title", ErrUnsupported)
	}
	key, err := z.apiKey()
	if err != nil {
		return nil, err
	}
	res := &Result{}
	seen := map[string]bool{}
	add := func(list []zileanTorrent, byTitle bool) (added, foreign int) {
		for i := range list {
			t := &list[i]
			if byTitle && q.IMDb != "" && t.IMDbID != "" && !strings.EqualFold(t.IMDbID, q.IMDb) {
				foreign++ // the title search found another title
				continue
			}
			r, ok := z.release(ctx, t, q)
			if !ok || seen[r.InfoHash] {
				continue
			}
			seen[r.InfoHash] = true
			res.Releases = append(res.Releases, r)
			added++
		}
		return added, foreign
	}
	if q.IMDb != "" && q.Type != QueryText {
		list, err := z.fetch(ctx, key, q, true)
		res.Requests++
		if err != nil {
			return nil, err
		}
		add(list, false)
	}
	if q.Title != "" {
		list, err := z.fetch(ctx, key, q, false)
		res.Requests++
		switch {
		case err != nil && len(res.Releases) == 0 && res.Requests == 1:
			return nil, err
		case err != nil:
			res.Notes = append(res.Notes, fmt.Sprintf("title search failed, IMDb results only: %v", err))
		default:
			added, foreign := add(list, q.IMDb != "")
			if q.IMDb != "" {
				res.Notes = append(res.Notes, fmt.Sprintf("title search added %d, dropped %d for another IMDb id", added, foreign))
			}
		}
	}
	return res, nil
}

func (z *zilean) fetch(ctx context.Context, key string, q Query, byIMDb bool) ([]zileanTorrent, error) {
	p := url.Values{}
	if byIMDb {
		p.Set("ImdbId", q.IMDb)
	} else {
		p.Set("Query", q.Title)
		if q.Year > 0 && q.Type == QueryMovie {
			p.Set("Year", strconv.Itoa(q.Year))
		}
	}
	if q.Type == QuerySeries && q.Season > 0 {
		p.Set("Season", strconv.Itoa(q.Season))
		if q.Episode > 0 {
			p.Set("Episode", strconv.Itoa(q.Episode))
		}
	}
	if key != "" {
		p.Set("apikey", key)
	}
	u := *z.endpoint
	u.RawQuery = p.Encode()
	body, err := z.get(ctx, u.String(), "application/json")
	if err != nil {
		return nil, err
	}
	var list []zileanTorrent
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("indexer %s: not a result list: %w", z.key, err)
	}
	return list, nil
}

func (z *zilean) release(ctx context.Context, t *zileanTorrent, q Query) (Release, bool) {
	hash := NormalizeHash(t.InfoHash)
	title := strings.TrimSpace(t.RawTitle)
	if hash == "" || title == "" {
		return Release{}, false
	}
	r := Release{Title: title, InfoHash: hash, Seeders: -1, Peers: -1, Magnet: BareMagnet(hash),
		Size: flexInt(t.Size), PublishDate: parseDate(t.IngestedAt)}
	if strings.HasPrefix(t.IMDbID, "tt") {
		r.IMDb = t.IMDbID
	}
	switch {
	case q.Type == QueryMovie || strings.EqualFold(t.Category, "movie"):
		r.Categories = []int{2000}
	case q.Type == QuerySeries || strings.EqualFold(t.Category, "tvSeries"):
		r.Categories = []int{5000}
	}
	z.stamp(ctx, &r)
	return r, true
}

// flexInt reads a number written as a JSON number or a string.
func flexInt(raw json.RawMessage) int64 {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var s string
	if raw[0] == '"' {
		if json.Unmarshal(raw, &s) != nil {
			return 0
		}
	} else {
		s = string(raw)
	}
	if n := parseInt64(s); n > 0 {
		return n
	}
	if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && f > 0 {
		return int64(f)
	}
	return 0
}
