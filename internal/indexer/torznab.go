package indexer

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
)

// Torznab paging. A feed with no page_cap is still bounded: DefaultPageCap results per search.
const (
	defaultPageSize = 100
	DefaultPageCap  = 1000
	// DMMPageCap is DMM's cap when the policy sets none: 200 results per query. DMM
	// returns the largest first, so the first pages hold nearly all 2160p releases and season packs.
	DMMPageCap = 200
	capsTTL    = 24 * time.Hour
	// maxPages bounds requests per search whatever the page size.
	maxPages = 50
)

// torznab searches a Torznab feed: Prowlarr's per-indexer feeds (kind torznab) and DMM's feed (kind dmm).
type torznab struct {
	base
	endpoint *url.URL
	pageCap  int
	// dmm: no caps request (fixed caps), tag cached_on. Every feed strips the {imdb-tt…} title suffix.
	dmm      bool
	cachedOn string
	// providers are the DMM path's providers in path order (nil for plain /cached); rssEndpoint is the feed the RSS
	// sync reads (nil when the path names no rd or ad). A path naming both rd and ad has one feed per provider too
	// (rssFeeds, by provider), which the RSS sync reads instead (RSSFeeds).
	providers    []string
	rssEndpoint  *url.URL
	rssCachedOn  string
	rssProviders []string
	rssFeeds     map[string]*url.URL
	now          func() time.Time

	capsMu sync.Mutex
	caps   *caps
	capsAt time.Time
}

func newTorznab(b base, dmm bool, now func() time.Time) (*torznab, error) {
	u, err := url.Parse(b.ix.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("indexers[%s].url: not an http(s) URL", b.key)
	}
	if u.RawQuery != "" {
		return nil, fmt.Errorf("indexers[%s].url: no query string; the key comes from api_key_env", b.key)
	}
	t := &torznab{base: b, endpoint: u, pageCap: b.ix.PageCap, dmm: dmm, now: now}
	if dmm {
		// DMM's feeds are https://debridmediamanager.com/api/torznab/cached (what Real-Debrid or AllDebrid hold) and
		// .../api/torznab/<p>[/<p>…]/cached, p one of rd, ad, tb, each at most once, in any order: cached on any of
		// them (one provider: Prowlarr "DMM TB"). Like Prowlarr's Generic
		// Torznab, the API answers on that URL plus "/api".
		m := dmmPathRE.FindStringSubmatchIndex(u.Path)
		var providers []string
		if m != nil && m[3] > m[2] {
			providers = strings.Split(strings.TrimSuffix(u.Path[m[2]:m[3]], "/"), "/")
		}
		for i, p := range providers {
			if slices.Contains(providers[:i], p) {
				m = nil
			}
		}
		if m == nil {
			return nil, fmt.Errorf("indexers[%s].url: a dmm url is DMM's Torznab feed, e.g. "+
				"https://debridmediamanager.com/api/torznab/rd/cached, or several providers (rd, ad, tb, each once) "+
				"as in https://debridmediamanager.com/api/torznab/rd/ad/tb/cached (got path %q)", b.key, u.Path)
		}
		t.providers = providers
		t.cachedOn = dmmCachedOn(providers)
		prefix := u.Path[:m[2]] // through "/torznab/"
		if m[4] < 0 {
			u.Path = strings.TrimSuffix(u.Path, "/") + "/api"
		}
		// DMM serves its newly cached list to an untargeted request on a path naming only rd and ad (or plain
		// /cached); with any other provider in the path it answers the old way. So the RSS sync asks the path's rd and
		// ad providers only, in path order; a path naming neither has no feed.
		var rss []string
		for _, p := range providers {
			if p == "rd" || p == "ad" {
				rss = append(rss, p)
			}
		}
		if len(providers) == 0 || len(rss) > 0 {
			e := *u
			e.Path = prefix + strings.Join(append(rss, "cached", "api"), "/")
			e.RawPath = ""
			t.rssEndpoint = &e
			t.rssCachedOn, t.rssProviders = dmmCachedOn(rss), rss
		}
		// DMM lists a hash once on a combined list, at its first listing on either provider, so a later cache on
		// the other provider never shows there: with both, each provider's own list is read.
		if len(rss) > 1 {
			t.rssFeeds = map[string]*url.URL{}
			for _, p := range rss {
				e := *u
				e.Path = prefix + p + "/cached/api"
				e.RawPath = ""
				t.rssFeeds[p] = &e
			}
		}
		if t.pageCap == 0 {
			t.pageCap = DMMPageCap
		}
	}
	if t.pageCap <= 0 {
		t.pageCap = DefaultPageCap
	}
	return t, nil
}

// RSSFeeds are the DMM path's per-provider newly cached lists (rd, ad in path order) when it names both; nil
// otherwise (one feed).
func (t *torznab) RSSFeeds() []string {
	if t.rssFeeds == nil {
		return nil
	}
	return slices.Clone(t.rssProviders)
}

var dmmPathRE = regexp.MustCompile(`/torznab/((?:(?:rd|ad|tb)/)*)cached(?:/(api)?)?$`)

// dmmCachedOn is the cached_on a DMM path gives its items: plain /cached "rd,ad" (what it lists is held by either),
// one provider that provider, and "" for several, where each item's own cachedon attribute says which hold it.
func dmmCachedOn(providers []string) string {
	switch len(providers) {
	case 0:
		return "rd,ad"
	case 1:
		return providers[0]
	}
	return ""
}

func (t *torznab) Supports(q Query) bool {
	if t.dmm {
		// DMM indexes by IMDb id; its recent feed is the newly cached list on rd and ad.
		switch q.Type {
		case QueryRSS:
			return t.rssEndpoint != nil && (q.RSSFeed == "" || t.rssFeeds[q.RSSFeed] != nil)
		case QueryMovie, QuerySeries:
			return q.IMDb != ""
		case QueryText:
			return q.Title != ""
		}
		return false
	}
	return true
}

func (t *torznab) Search(ctx context.Context, q Query) (*Result, error) {
	if !t.Supports(q) {
		return nil, fmt.Errorf("%w: %s %s", ErrUnsupported, t.kind, q.Type)
	}
	key, err := t.apiKey()
	if err != nil {
		return nil, err
	}
	res := &Result{}
	c, err := t.capabilities(ctx, key, res)
	if err != nil {
		return nil, err
	}
	tiers, ok := t.requests(q, c)
	if !ok {
		return nil, fmt.Errorf("%w: %s caps allow no %s search with the ids given", ErrUnsupported, t.key, q.Type)
	}
	seen := map[string]bool{}
	answered := false
	var firstErr error
	for _, tier := range tiers {
		for _, params := range tier {
			err := t.searchPaged(ctx, key, q, c, params, seen, res)
			if err == nil {
				answered = true
				continue
			}
			if errors.Is(err, gateway.ErrVPNRequired) || ctx.Err() != nil {
				return nil, err
			}
			if firstErr == nil {
				firstErr = err
			}
			// One request of several failing leaves the answer incomplete; it does not take back the others.
			res.Truncated, res.failed = true, true
			res.Notes = append(res.Notes, err.Error())
		}
		if len(res.Releases) > 0 {
			break
		}
	}
	if !answered {
		return nil, firstErr
	}
	res.Capped = res.Truncated && !res.failed
	return res, nil
}

// searchPaged runs one request, page by page, adding new releases to res.
func (t *torznab) searchPaged(ctx context.Context, key string, q Query, c *caps, params url.Values, seen map[string]bool,
	res *Result) error {
	limit := c.pageSize()
	offset := 0
	endpoint, cachedOn, named := t.endpoint, t.cachedOn, t.providers
	if t.dmm && q.Type == QueryRSS {
		endpoint, cachedOn, named = t.rssEndpoint, t.rssCachedOn, t.rssProviders
		if f := t.rssFeeds[q.RSSFeed]; f != nil {
			endpoint, cachedOn, named = f, q.RSSFeed, []string{q.RSSFeed}
		}
	}
	for page := 1; ; page++ {
		n := min(limit, t.pageCap-offset)
		p := cloneValues(params)
		if q.Type == QueryRSS {
			if q.RSSOffset > 0 {
				p.Set("offset", strconv.Itoa(q.RSSOffset))
			}
			switch {
			case t.dmm:
				// DMM answers ten without a limit. page_cap bounds a search, not one RSS page: the RSS sync takes a
				// page shorter than it asked for as the end of the feed.
				rl := limit
				if q.RSSLimit > 0 {
					rl = min(q.RSSLimit, limit)
				}
				p.Set("limit", strconv.Itoa(rl))
			case q.RSSLimit > 0:
				p.Set("limit", strconv.Itoa(min(q.RSSLimit, t.pageCap)))
			}
		} else {
			p.Set("limit", strconv.Itoa(n))
			if offset > 0 {
				p.Set("offset", strconv.Itoa(offset))
			}
		}
		body, err := t.fetch(ctx, endpoint, key, p)
		res.Requests++
		if err != nil {
			// Once a page is in, a later page failing (a spent budget, a back-off, an error) ends paging; it does not
			// take back what answered.
			if page > 1 {
				res.Truncated, res.failed = true, true
				res.Notes = append(res.Notes, fmt.Sprintf("paging stopped at offset %d: %v", offset, err))
				break
			}
			return err
		}
		items, total, err := parseTorznab(body)
		if err != nil {
			if page > 1 {
				res.Truncated, res.failed = true, true
				res.Notes = append(res.Notes, fmt.Sprintf("paging stopped at offset %d: %v", offset, err))
				break
			}
			return fmt.Errorf("indexer %s: %w", t.key, err)
		}
		for i := range items {
			r := t.release(ctx, &items[i], cachedOn, named)
			if q.Type == QueryRSS {
				// The RSS cursor sees the upstream page as it came, before the GUID dedupe below.
				res.RSSPage = append(res.RSSPage, rssCursorFields(r))
			}
			id := dedupeKey(&r)
			if seen[id] {
				if t.dmm {
					t.mergeCachedOn(res.Releases, id, r.Extras["cached_on"], named)
				}
				continue
			}
			seen[id] = true
			res.Releases = append(res.Releases, r)
		}
		// The next offset counts what the page held: a feed pages however it likes whatever limit it is sent (DMM
		// answers ten a page). A feed that reports a total is believed; one that doesn't has finished when a page
		// comes back short.
		offset += len(items)
		more := len(items) > 0 && ((total > 0 && offset < total) || (total == 0 && len(items) >= n))
		if q.Type == QueryRSS || !more {
			break
		}
		if offset >= t.pageCap || page >= maxPages {
			res.Truncated = true
			res.Notes = append(res.Notes, fmt.Sprintf("page_cap %d reached after %d pages", t.pageCap, page))
			break
		}
	}
	return nil
}

// requests are the tiers of requests for q: one request, or Sonarr's criteria requests for a series query with
// criteria on a Prowlarr feed. DMM answers only by IMDb id with TVDB numbering, so it keeps the plain query.
func (t *torznab) requests(q Query, c *caps) ([][]url.Values, bool) {
	if q.Type == QueryMovie {
		var tiers [][]url.Values
		if p, ok := t.params(q, c); ok {
			// Radarr's NewznabRequestGenerator tries the movie ids first, then t=movie by title/year
			// when the id tier returns no releases (Radarr src/NzbDrone.Core/Indexers/NewznabRequestGenerator.cs).
			if q.IMDb != "" && c.movie.available && c.movie.has("imdbid") || q.TMDb != 0 && c.movie.available && c.movie.has("tmdbid") {
				tiers = append(tiers, []url.Values{p})
				if q.Title != "" {
					if fallback, ok := t.movieTitleParams(q, c); ok {
						tiers = append(tiers, []url.Values{fallback})
					}
				}
				return tiers, true
			}
			return [][]url.Values{{p}}, true
		}
	}
	if q.Type == QuerySeries && q.Criteria != "" && !t.dmm {
		tiers := criteriaRequests(q, c, q.categoryList(c))
		for _, tier := range tiers {
			if len(tier) > 0 {
				return tiers, true
			}
		}
		return nil, false
	}
	p, ok := t.params(q, c)
	if !ok {
		return nil, false
	}
	return [][]url.Values{{p}}, true
}

func (t *torznab) movieTitleParams(q Query, c *caps) (url.Values, bool) {
	p := url.Values{}
	if cats := q.categoryList(c); cats != "" {
		p.Set("cat", cats)
	}
	if c.search.has("q") {
		p.Set("t", "search")
		titles := append([]string{q.Title}, q.Aliases...)
		for _, title := range titles {
			if c.search.raw {
				p.Add("q", newsnabifyTitle(titleYear(Query{Title: title, Year: q.Year})))
			} else {
				p.Add("q", newsnabifyTitle(titleYear(Query{Title: cleanRadarrTitle(title), Year: q.Year})))
			}
		}
		return p, true
	}
	if c.movie.available && c.movie.has("q") {
		p.Set("t", "movie")
		p.Set("q", titleYear(q))
		return p, true
	}
	return nil, false
}

// cleanRadarrTitle mirrors SearchCriteriaBase.GetCleanSceneTitle (Radarr src/NzbDrone.Core/IndexerSearch/Definitions/SearchCriteriaBase.cs:24-39).
func cleanRadarrTitle(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 4 && strings.EqualFold(s[:4], "the ") {
		s = s[4:]
	}
	s = strings.ReplaceAll(s, "&", "and")
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\'', '.', '`', '´', '‘', '’':
			continue
		default:
			if r > 127 {
				b.WriteRune(r)
			} else if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
				b.WriteRune(r)
			} else {
				b.WriteByte('+')
			}
		}
	}
	return strings.Trim(b.String(), "+ ")
}

// newsnabifyTitle mirrors NewznabRequestGenerator.NewsnabifyTitle (Radarr src/NzbDrone.Core/Indexers/Newznab/NewznabRequestGenerator.cs:206-210).
func newsnabifyTitle(s string) string { return strings.ReplaceAll(s, "+", " ") }

func (t *torznab) fetch(ctx context.Context, endpoint *url.URL, key string, p url.Values) ([]byte, error) {
	if key != "" {
		p.Set("apikey", key)
	}
	u := *endpoint
	u.RawQuery = p.Encode()
	return t.get(ctx, u.String(), "application/rss+xml, application/xml, text/xml")
}

// params builds the Torznab query the way Radarr's and Sonarr's NewznabRequestGenerator choose: ids the caps
// support first, then the title.
func (t *torznab) params(q Query, c *caps) (url.Values, bool) {
	p := url.Values{}
	if cats := q.categoryList(c); cats != "" {
		p.Set("cat", cats)
	}
	switch q.Type {
	case QueryRSS:
		p.Set("t", "search")
		return p, true
	case QueryText:
		if !c.search.has("q") {
			return nil, false
		}
		p.Set("t", "search")
		p.Set("q", q.Title)
		return p, true
	case QueryMovie:
		if c.movie.available {
			p.Set("t", "movie")
			switch {
			case q.IMDb != "" && c.movie.has("imdbid"):
				p.Set("imdbid", q.imdbDigits()) // Radarr sends the digits only
			case q.TMDb != 0 && c.movie.has("tmdbid"):
				p.Set("tmdbid", strconv.Itoa(q.TMDb))
			case q.Title != "" && c.movie.has("q"):
				p.Set("q", titleYear(q))
			default:
				return nil, false
			}
			return p, true
		}
		if q.Title == "" || !c.search.has("q") {
			return nil, false
		}
		p.Set("t", "search")
		p.Set("q", titleYear(q))
		return p, true
	case QuerySeries:
		if c.tv.available {
			p.Set("t", "tvsearch")
			switch {
			case q.TVDB != 0 && c.tv.has("tvdbid"):
				p.Set("tvdbid", strconv.Itoa(q.TVDB))
			case q.IMDb != "" && c.tv.has("imdbid"):
				p.Set("imdbid", q.IMDb) // Sonarr sends it with "tt"
			case q.TMDb != 0 && c.tv.has("tmdbid"):
				p.Set("tmdbid", strconv.Itoa(q.TMDb))
			case q.Title != "" && c.tv.has("q"):
				p.Set("q", q.Title)
			default:
				return nil, false
			}
			if q.Season > 0 && c.tv.has("season") {
				p.Set("season", strconv.Itoa(q.Season))
				if q.Episode > 0 && c.tv.has("ep") {
					p.Set("ep", strconv.Itoa(q.Episode))
				}
			}
			return p, true
		}
		if q.Title == "" || !c.search.has("q") {
			return nil, false
		}
		p.Set("t", "search")
		s := q.Title
		if q.Season > 0 {
			s += fmt.Sprintf(" S%02d", q.Season)
			if q.Episode > 0 {
				s += fmt.Sprintf("E%02d", q.Episode)
			}
		}
		p.Set("q", s)
		return p, true
	}
	return nil, false
}

func titleYear(q Query) string {
	if q.Year > 0 {
		return fmt.Sprintf("%s %d", q.Title, q.Year)
	}
	return q.Title
}

func cloneValues(v url.Values) url.Values {
	c := make(url.Values, len(v))
	for k, s := range v {
		c[k] = append([]string(nil), s...)
	}
	return c
}

// release maps one Torznab item. cachedOn is a DMM endpoint's tag for every item, or "" on an endpoint naming several
// providers (named), where the item's cachedon attribute names those holding it, kept to named.
func (t *torznab) release(ctx context.Context, it *tzItem, cachedOn string, named []string) Release {
	r := Release{Title: strings.TrimSpace(it.Title), GUID: strings.TrimSpace(it.GUID), Seeders: -1, Peers: -1}
	r.Size = parseInt64(it.Size)
	if r.Size == 0 {
		r.Size = parseInt64(it.Enclosure.Length)
	}
	r.PublishDate = parseDate(it.PubDate)
	link := it.Link
	if link == "" {
		link = it.Enclosure.URL
	}
	r.Link = NewLink(link)
	r.addMagnetIdentity(it.Enclosure.URL)

	for _, c := range it.Categories {
		if n, err := strconv.Atoi(strings.TrimSpace(c)); err == nil {
			r.Categories = appendUnique(r.Categories, n)
		}
	}
	if it.Indexer != nil {
		switch strings.ToLower(strings.TrimSpace(it.Indexer.Type)) {
		case "public":
			r.Privacy = policy.PrivacyPublic
		case "private":
			r.Privacy = policy.PrivacyPrivate
		default: // Prowlarr writes anything else as semi-private
			r.Privacy = policy.PrivacySemiPrivate
		}
	}
	var holders string
	for _, a := range it.Attrs {
		v := strings.TrimSpace(a.Value)
		switch strings.ToLower(a.Name) {
		case "cachedon":
			holders = v
		case "seeders":
			r.Seeders = atoiOr(v, -1)
		case "peers":
			r.Peers = atoiOr(v, -1)
		case "size":
			if r.Size == 0 {
				r.Size = parseInt64(v)
			}
		case "infohash":
			r.InfoHash = NormalizeHash(v)
			r.identityHashes = append(r.identityHashes, r.InfoHash)
		case "magneturl":
			r.addMagnetIdentity(v)
			if r.Magnet.Empty() {
				r.Magnet = NewLink(v)
			}
		case "category":
			if n, err := strconv.Atoi(v); err == nil {
				r.Categories = appendUnique(r.Categories, n)
			}
		case "tag":
			if v != "" {
				r.Flags = append(r.Flags, v)
			}
		case "language":
			if v != "" {
				r.Languages = append(r.Languages, v)
			}
		case "imdb", "imdbid":
			if d := strings.TrimPrefix(v, "tt"); d != "" && atoiOr(d, 0) > 0 {
				r.IMDb = "tt" + fmt.Sprintf("%07d", atoiOr(d, 0))
			}
		case "tmdbid":
			r.TMDb = atoiOr(v, 0)
		case "tvdbid":
			r.TVDB = atoiOr(v, 0)
		case "downloadvolumefactor", "uploadvolumefactor", "minimumratio", "minimumseedtime", "grabs", "files":
			r.setExtra(strings.ToLower(a.Name), v)
		}
	}
	// TorznabRssParser.GetLanguages: the language attributes, else <language> elements.
	if len(r.Languages) == 0 {
		for _, l := range it.Languages {
			if l = strings.TrimSpace(l); l != "" {
				r.Languages = append(r.Languages, l)
			}
		}
	}
	// DMM's suffix is stripped from every feed, not only kind dmm: Prowlarr's DMM indexers and feeds that copy DMM
	// titles carry it too.
	if m := dmmSuffixRE.FindStringSubmatch(r.Title); m != nil {
		if r.IMDb == "" {
			r.IMDb = m[1]
		}
		r.Title = strings.TrimSpace(dmmSuffixRE.ReplaceAllString(r.Title, ""))
	}
	if t.dmm {
		if cachedOn == "" {
			var on []string
			for _, c := range parseCachedOn(holders) {
				if slices.Contains(named, c) {
					on = append(on, c)
				}
			}
			cachedOn = strings.Join(on, ",")
		}
		r.setExtra("cached_on", cachedOn)
	}
	if r.Magnet.Empty() && r.Link.IsMagnet() {
		r.Magnet = r.Link
	}
	t.stamp(ctx, &r)
	return r
}

// dedupeKey identifies a release within one search: its GUID, else its hash and title.
func dedupeKey(r *Release) string {
	if r.GUID != "" {
		return r.GUID
	}
	return r.InfoHash + "|" + r.Title
}

// mergeCachedOn adds a repeated DMM release's providers to the copy already kept: DMM asks TorBox per request, so a
// later page can name more holders. The codes are kept in path order (rd, ad, tb for a plain path).
func (t *torznab) mergeCachedOn(rs []Release, id, more string, named []string) {
	order := named
	if len(order) == 0 {
		order = cacheCodes
	}
	for i := range rs {
		r := &rs[i]
		if dedupeKey(r) != id {
			continue
		}
		have := parseCachedOn(r.Extras["cached_on"])
		var on []string
		for _, c := range order {
			if slices.Contains(have, c) || slices.Contains(parseCachedOn(more), c) {
				on = append(on, c)
			}
		}
		r.setExtra("cached_on", strings.Join(on, ","))
		return
	}
}

// DMM appends "{imdb-tt1234567}" to titles; with it the arrs cannot read the release group.
var dmmSuffixRE = regexp.MustCompile(`\s*\{imdb-(tt\d+)\}`)

func (r *Release) setExtra(k, v string) {
	if v == "" {
		return
	}
	if r.Extras == nil {
		r.Extras = map[string]string{}
	}
	r.Extras[k] = v
}

func appendUnique(s []int, n int) []int {
	for _, x := range s {
		if x == n {
			return s
		}
	}
	return append(s, n)
}

func atoiOr(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		if f, ferr := strconv.ParseFloat(strings.TrimSpace(s), 64); ferr == nil {
			return int(f)
		}
		return def
	}
	return n
}

func parseInt64(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

var dateLayouts = []string{time.RFC1123Z, time.RFC1123, "Mon, 2 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04:05 MST",
	time.RFC3339, "2006-01-02 15:04:05Z07:00", "2006-01-02 15:04:05.999Z07:00", "2006-01-02 15:04:05"}

func parseDate(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// Torznab documents.

type tzItem struct {
	Title      string   `xml:"title"`
	GUID       string   `xml:"guid"`
	Link       string   `xml:"link"`
	PubDate    string   `xml:"pubDate"`
	Size       string   `xml:"size"`
	Categories []string `xml:"category"`
	Enclosure  struct {
		URL    string `xml:"url,attr"`
		Length string `xml:"length,attr"`
	} `xml:"enclosure"`
	Indexer *struct {
		Type string `xml:"type,attr"`
	} `xml:"prowlarrindexer"`
	Attrs []struct {
		Name  string `xml:"name,attr"`
		Value string `xml:"value,attr"`
	} `xml:"attr"`
	// Languages are <language> elements, which some indexers write instead of the attribute.
	Languages []string `xml:"language"`
}

type tzDoc struct {
	XMLName     xml.Name
	Code        string `xml:"code,attr"`
	Description string `xml:"description,attr"`
	Channel     struct {
		// Response is <torznab:response offset total/> (or newznab:response): the whole result's size.
		Response struct {
			Total string `xml:"total,attr"`
		} `xml:"response"`
		Items []tzItem `xml:"item"`
	} `xml:"channel"`
}

// TorznabError is an <error code description/> answer.
type TorznabError struct {
	Code        string
	Description string
}

func (e *TorznabError) Error() string { return "torznab error " + e.Code + ": " + e.Description }

func decodeXML(body []byte, v any) error {
	d := xml.NewDecoder(bytes.NewReader(body))
	d.Strict = false
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	return d.Decode(v)
}

// parseTorznab returns the items and the reported total (0 when the feed reports none).
func parseTorznab(body []byte) ([]tzItem, int, error) {
	var doc tzDoc
	if err := decodeXML(body, &doc); err != nil {
		return nil, 0, fmt.Errorf("not a Torznab feed: %w", err)
	}
	switch doc.XMLName.Local {
	case "error":
		return nil, 0, &TorznabError{Code: doc.Code, Description: doc.Description}
	case "rss":
		return doc.Channel.Items, atoiOr(doc.Channel.Response.Total, 0), nil
	}
	return nil, 0, fmt.Errorf("not a Torznab feed: root <%s>", doc.XMLName.Local)
}

// Caps.

type capsMode struct {
	available bool
	params    map[string]bool
	// raw is searchEngine="raw" (Prowlarr's feeds): text searches send the titles as they are as well as clean.
	raw bool
}

func (m capsMode) has(p string) bool { return m.available && m.params[p] }

type caps struct {
	search, tv, movie capsMode
	max               int
	categories        map[int]bool
}

func (c *caps) pageSize() int {
	// The largest page the feed allows, up to 100: fewer requests per search.
	n := defaultPageSize
	if c.max > 0 && c.max < n {
		n = c.max
	}
	return n
}

// dmmCaps are DMM's: it answers by IMDb id (both with and without "tt", as Radarr and Sonarr send them through
// Prowlarr's Generic Torznab), with season and episode for TV.
var dmmCaps = &caps{
	search:     capsMode{available: true, params: map[string]bool{"q": true}},
	tv:         capsMode{available: true, params: map[string]bool{"imdbid": true, "season": true, "ep": true}},
	movie:      capsMode{available: true, params: map[string]bool{"imdbid": true}},
	categories: map[int]bool{2000: true, 5000: true, 5070: true},
	max:        defaultPageSize,
}

func (t *torznab) capabilities(ctx context.Context, key string, res *Result) (*caps, error) {
	if t.dmm {
		return dmmCaps, nil
	}
	t.capsMu.Lock()
	defer t.capsMu.Unlock()
	if t.caps != nil && t.now().Sub(t.capsAt) < capsTTL {
		return t.caps, nil
	}
	body, err := t.fetch(ctx, t.endpoint, key, url.Values{"t": {"caps"}})
	res.Requests++
	if err != nil {
		return nil, fmt.Errorf("indexer %s: caps: %w", t.key, err)
	}
	c, err := parseCaps(body)
	if err != nil {
		return nil, fmt.Errorf("indexer %s: caps: %w", t.key, err)
	}
	t.caps, t.capsAt = c, t.now()
	return c, nil
}

func parseCaps(body []byte) (*caps, error) {
	type mode struct {
		Available       string `xml:"available,attr"`
		SupportedParams string `xml:"supportedParams,attr"`
		SearchEngine    string `xml:"searchEngine,attr"`
	}
	var doc struct {
		XMLName     xml.Name
		Code        string `xml:"code,attr"`
		Description string `xml:"description,attr"`
		Limits      struct {
			Max string `xml:"max,attr"`
		} `xml:"limits"`
		Searching struct {
			Search mode `xml:"search"`
			TV     mode `xml:"tv-search"`
			Movie  mode `xml:"movie-search"`
		} `xml:"searching"`
		Categories struct {
			Items []struct {
				ID      int `xml:"id,attr"`
				Subcats []struct {
					ID int `xml:"id,attr"`
				} `xml:"subcat"`
			} `xml:"category"`
		} `xml:"categories"`
	}
	if err := decodeXML(body, &doc); err != nil {
		return nil, fmt.Errorf("not a caps document: %w", err)
	}
	if doc.XMLName.Local == "error" {
		return nil, &TorznabError{Code: doc.Code, Description: doc.Description}
	}
	if doc.XMLName.Local != "caps" {
		return nil, fmt.Errorf("not a caps document: root <%s>", doc.XMLName.Local)
	}
	conv := func(m mode) capsMode {
		c := capsMode{available: strings.EqualFold(m.Available, "yes"), params: map[string]bool{},
			raw: strings.EqualFold(m.SearchEngine, "raw")}
		for _, p := range strings.Split(m.SupportedParams, ",") {
			if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
				c.params[p] = true
			}
		}
		return c
	}
	c := &caps{search: conv(doc.Searching.Search), tv: conv(doc.Searching.TV), movie: conv(doc.Searching.Movie),
		max: atoiOr(doc.Limits.Max, 0), categories: map[int]bool{}}
	for _, cat := range doc.Categories.Items {
		c.categories[cat.ID] = true
		for _, sub := range cat.Subcats {
			c.categories[sub.ID] = true
		}
	}
	if c.search.available && len(c.search.params) == 0 {
		c.search.params["q"] = true // the Newznab default
	}
	return c, nil
}
