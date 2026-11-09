package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// torrentio searches the Torrentio Stremio addon's stream API by IMDb id.
//
// Response shape: Stremio's stream object as Torrentio fills it (torrentio-scraper addon/lib/streamInfo.js; the
// same fields riven's plugin-torrentio reads): {"streams": [{"name": "Torrentio\n4k DV", "title":
// "<torrent name>\n[<file name>\n]👤 <seeders> 💾 <size> ⚙️ <source>\n<flags>", "infoHash", "fileIdx",
// "behaviorHints": {"bingeGroup", "filename"}}]}. Newer Stremio SDKs call "title" "description"; both are read.
type torrentio struct {
	base
	endpoint *url.URL
	// counter fills EpisodeCounts for a series query without them (from TMDb).
	counter EpisodeCounter
}

// torrentioOptions are the config options a search may carry. Anything else (realdebrid=, alldebrid=, torbox=,
// premiumize=, debridlink=, offcloud=, putio=, ...) would put a debrid key in the URL: refused, fail closed.
var torrentioOptions = map[string]bool{"providers": true, "sort": true, "qualityfilter": true, "language": true,
	"limit": true, "sizefilter": true}

func newTorrentio(b base) (*torrentio, error) {
	u, err := url.Parse(b.ix.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("indexers[%s].url: not an http(s) URL", b.key)
	}
	if u.RawQuery != "" || u.User != nil {
		return nil, fmt.Errorf("indexers[%s].url: no query string or credentials", b.key)
	}
	if b.ix.APIKeyEnv != "" {
		return nil, fmt.Errorf("indexers[%s]: torrentio is searched with the plain config; api_key_env is not used", b.key)
	}
	// The path may hold one config segment: "providers=yts,eztv|sort=qualitysize".
	path := strings.Trim(u.Path, "/")
	if strings.Contains(path, "/") {
		return nil, fmt.Errorf("indexers[%s].url: at most one config segment after the host", b.key)
	}
	if path != "" {
		for _, opt := range strings.Split(path, "|") {
			k, _, ok := strings.Cut(opt, "=")
			if !ok || !torrentioOptions[strings.ToLower(k)] {
				return nil, fmt.Errorf("indexers[%s].url: torrentio option is not allowed: searches use the plain "+
					"config, never debrid keys (allowed: providers, sort, qualityfilter, language, limit, sizefilter)",
					b.key)
			}
		}
	}
	u.Path = "/" + path
	return &torrentio{base: b, endpoint: u}, nil
}

func (t *torrentio) Supports(q Query) bool {
	switch q.Type {
	case QueryMovie:
		return q.IMDb != ""
	case QuerySeries:
		return q.IMDb != "" && q.Season > 0
	}
	return false
}

type tioResponse struct {
	Streams []tioStream `json:"streams"`
}

type tioStream struct {
	Name          string `json:"name"`
	Title         string `json:"title"`
	Description   string `json:"description"`
	InfoHash      string `json:"infoHash"`
	FileIdx       *int   `json:"fileIdx"`
	BehaviorHints struct {
		BingeGroup string `json:"bingeGroup"`
		Filename   string `json:"filename"`
	} `json:"behaviorHints"`
}

func (t *torrentio) Search(ctx context.Context, q Query) (*Result, error) {
	if !t.Supports(q) {
		return nil, fmt.Errorf("%w: torrentio needs an IMDb id (and a season for series)", ErrUnsupported)
	}
	// Torrentio answers only show:season:episode; a blank episode returns nothing. A season search asks for
	// episode 1 and keeps the packs.
	seasonSearch := q.Type == QuerySeries && q.Episode == 0
	var stream string
	if q.Type == QueryMovie {
		stream = "/stream/movie/" + q.IMDb + ".json"
	} else {
		ep := q.Episode
		if seasonSearch {
			ep = 1
		}
		stream = fmt.Sprintf("/stream/series/%s:%d:%d.json", q.IMDb, q.Season, ep)
	}
	u := *t.endpoint
	u.Path = strings.TrimSuffix(u.Path, "/") + stream
	body, err := t.get(ctx, u.String(), "application/json")
	if err != nil {
		return nil, err
	}
	var resp tioResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("indexer %s: not a stream list: %w", t.key, err)
	}
	res := &Result{Requests: 1}
	if q.Type == QuerySeries && len(q.EpisodeCounts) == 0 && t.counter != nil {
		counts, err := t.counter.EpisodeCounts(ctx, q)
		switch {
		case err != nil:
			res.Notes = append(res.Notes, fmt.Sprintf("episode counts: %v; pack sizes are file sizes", err))
		case len(counts) == 0:
			res.Notes = append(res.Notes, "no episode counts for this series; pack sizes are file sizes")
		default:
			q.EpisodeCounts = counts
		}
	}
	singles, packs := 0, 0
	for _, s := range resp.Streams {
		r, pack, ok := t.release(ctx, &s, q)
		if !ok {
			continue
		}
		if pack {
			packs++
		} else {
			singles++
			if seasonSearch {
				continue
			}
		}
		res.Releases = append(res.Releases, r)
	}
	if seasonSearch {
		res.Notes = append(res.Notes, fmt.Sprintf("season search: kept %d packs, dropped %d single episodes", packs, singles))
	}
	return res, nil
}

var (
	tioSeedersRE = regexp.MustCompile(`\x{1F464}\s*(\d+)`)
	tioSizeRE    = regexp.MustCompile(`\x{1F4BE}\s*(\d+(?:\.\d+)?)\s*([KMGT]B)`)
	tioSourceRE  = regexp.MustCompile(`\x{2699}\x{FE0F}?\s*(.+)$`)
	// Torrentio formats sizes in powers of 1024 (torrentio-scraper addon/lib/titleHelper.js formatSize).
	tioUnits = map[string]float64{"KB": 1 << 10, "MB": 1 << 20, "GB": 1 << 30, "TB": 1 << 40}
)

// release maps a stream. pack reports a season pack: the torrent name comes first and the matched file second,
// while a single episode goes straight from the name to the stats line.
func (t *torrentio) release(ctx context.Context, s *tioStream, q Query) (r Release, pack, ok bool) {
	text := s.Title
	if text == "" {
		text = s.Description
	}
	hash := NormalizeHash(s.InfoHash)
	lines := strings.Split(text, "\n")
	name := strings.TrimSpace(lines[0])
	if name == "" || hash == "" {
		return r, false, false
	}
	r = Release{Title: name, InfoHash: hash, Seeders: -1, Peers: -1, Magnet: BareMagnet(hash)}
	isStats := func(l string) bool { return strings.Contains(l, "\U0001F464") || strings.Contains(l, "\U0001F4BE") }
	if q.Type == QuerySeries {
		pack = len(lines) > 1 && !isStats(lines[1])
	}
	for _, l := range lines[1:] {
		if !isStats(l) {
			if pack && r.Extras["file"] == "" {
				r.setExtra("file", strings.TrimSpace(l))
			}
			continue
		}
		if m := tioSeedersRE.FindStringSubmatch(l); m != nil {
			r.Seeders = atoiOr(m[1], -1)
		}
		if m := tioSizeRE.FindStringSubmatch(l); m != nil {
			f, _ := strconv.ParseFloat(m[1], 64)
			r.Size = int64(f * tioUnits[m[2]])
		}
		if m := tioSourceRE.FindStringSubmatch(l); m != nil {
			r.setExtra("source", strings.TrimSpace(m[1]))
		}
		break
	}
	if s.FileIdx != nil {
		r.setExtra("file_idx", strconv.Itoa(*s.FileIdx))
	}
	if s.BehaviorHints.Filename != "" && r.Extras["file"] == "" && pack {
		r.setExtra("file", s.BehaviorHints.Filename)
	}
	if _, quality, ok := strings.Cut(s.Name, "\n"); ok {
		r.setExtra("torrentio_quality", strings.TrimSpace(quality))
	}
	if q.Type == QueryMovie {
		r.Categories = []int{2000}
	} else {
		r.Categories = []int{5000}
	}
	r.IMDb = q.IMDb
	if pack {
		// Torrentio sizes a pack as the matched file. Scale by the episode count when we know it.
		if n := packEpisodeCount(name, q.Season, q.EpisodeCounts); n > 1 && r.Size > 0 {
			r.Size *= int64(n)
			r.setExtra("size_scaled_by", strconv.Itoa(n))
		} else {
			r.setExtra("size_is_file", "true")
		}
		r.setExtra("pack", "true")
	}
	t.stamp(ctx, &r)
	return r, pack, true
}

// packEpisodeCount: how many episodes a pack holds, given the season searched.
var (
	packRangeRE    = regexp.MustCompile(`(?i)\bS(?:eason)?[ ._]?(\d{1,2})\s?(?:-|to|~|–)\s?S?(?:eason)?[ ._]?(\d{1,2})\b`)
	packCompleteRE = regexp.MustCompile(`(?i)\b(complete[ ._]series|all[ ._]seasons|the[ ._]complete[ ._]collection)\b`)
)

func packEpisodeCount(name string, season int, seasons map[int]int) int {
	regular := map[int]int{}
	total := 0
	for s, c := range seasons {
		if s > 0 && c > 0 {
			regular[s] = c
			total += c
		}
	}
	if m := packRangeRE.FindStringSubmatch(name); m != nil {
		lo, hi := atoiOr(m[1], 0), atoiOr(m[2], 0)
		if lo > hi {
			lo, hi = hi, lo
		}
		if lo <= season && season <= hi {
			n := 0
			for s, c := range regular {
				if lo <= s && s <= hi {
					n += c
				}
			}
			if n > 0 {
				return n
			}
		}
	}
	if packCompleteRE.MatchString(name) && total > 0 {
		return total
	}
	return seasons[season]
}
