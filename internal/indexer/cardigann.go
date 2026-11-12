package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/indexer/cardigann"
	"github.com/theatarr/theatarr/internal/policy"
)

// cardigannSearcher runs a site from the definition catalog natively (kind cardigann). The engine drives the site
// (login, search pages, scraping) and sends every hop through the gateway as this indexer's request: VPN check,
// allowlist (the indexer's url and hosts), its own budget and back-off.
type cardigannSearcher struct {
	base
	m *cardigann.Manager
}

// ErrNoCatalog means a cardigann indexer was asked to search while the definition catalog is not running.
var ErrNoCatalog = errors.New("the definition catalog is not running (set cardigann.definitions)")

func newCardigann(b base, m *cardigann.Manager) (Searcher, error) {
	if b.ix.Definition == "" {
		return nil, fmt.Errorf("indexers[%s].definition: required for a cardigann indexer", b.key)
	}
	return &cardigannSearcher{base: b, m: m}, nil
}

// Supports: the keyword searches need a title (an id search goes first where the definition lists the id, with
// the keywords as its fallback), so a movie or series search needs one. RSS is the
// definition's search without keywords (RSSProblem says when a definition has none).
func (c *cardigannSearcher) Supports(q Query) bool {
	switch q.Type {
	case QueryRSS:
		return true
	case QueryMovie, QuerySeries, QueryText:
		return q.Title != ""
	}
	return false
}

func (c *cardigannSearcher) Search(ctx context.Context, q Query) (*Result, error) {
	if !c.Supports(q) {
		return nil, fmt.Errorf("%w: %s %s", ErrUnsupported, c.kind, q.Type)
	}
	if q.Type == QueryRSS && q.RSSOffset > 0 {
		// A definition's latest releases are one search (its own paths may read several site pages); there is no
		// offset to page further back. Prowlarr answers an offset with nothing too (SupportsPagination false).
		return &Result{Notes: []string{"a catalog definition's latest releases have no further pages"}}, nil
	}
	if c.m == nil {
		return nil, fmt.Errorf("indexer %s: %w", c.key, ErrNoCatalog)
	}
	inst, def, defPrivacy, err := c.instance(ctx)
	if err != nil {
		return nil, err
	}
	if q.Type == QueryRSS {
		if p := cardigann.RSSProblem(def); p != "" {
			return nil, fmt.Errorf("%w: indexer %s: no RSS: %s", ErrUnsupported, c.key, p)
		}
	}
	var requests atomic.Int64
	inst.Transport = &gatewayTransport{gw: c.gw, target: c.target(), count: &requests}
	inst.Solver = &gatewayTransport{gw: c.gw, target: c.solverTarget(), count: &requests}
	tiers, ok := cardigannRequests(q, def)
	if !ok {
		return nil, fmt.Errorf("%w: indexer %s: definition %s lists none of the title's ids for an id-only search",
			ErrUnsupported, c.key, c.ix.Definition)
	}
	ctx, cancel := context.WithTimeout(ctx, c.m.SearchBudget())
	defer cancel()
	res := &Result{}
	var firstErr error
	answered := false
	seen := map[string]bool{}
	// A later tier is asked only when the earlier ones found nothing, as on a Torznab feed.
	for _, tier := range tiers {
		for _, cq := range tier {
			results, err := c.m.SearchQuery(ctx, inst, cq)
			if errors.Is(err, gateway.ErrVPNRequired) {
				return nil, err
			}
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				res.Truncated, res.failed = true, true
				res.Notes = append(res.Notes, fmt.Sprintf("indexer %s: %v", c.key, err))
				continue
			}
			answered = true
			from := len(res.Releases)
			for _, f := range results {
				c.addRelease(ctx, res, seen, f, defPrivacy)
			}
			if cq.ByID {
				dropUnrelated(q, res, from)
			}
		}
		if len(res.Releases) > 0 {
			break
		}
	}
	res.Requests = int(requests.Load())
	if !answered {
		return nil, fmt.Errorf("indexer %s: %w", c.key, firstErr)
	}
	return res, nil
}

// addRelease adds one engine result to res as a torrent release, once per info hash (or title and links).
func (c *cardigannSearcher) addRelease(ctx context.Context, res *Result, seen map[string]bool, f cardigann.Result,
	defPrivacy policy.Privacy) {
	if f.Protocol == cardigann.ProtocolUsenet {
		return
	}
	r := Release{Title: f.Title, InfoHash: NormalizeHash(f.InfoHash), Size: f.SizeBytes, Seeders: -1, Peers: -1,
		PublishDate: f.PublishDate, Categories: f.Categories, IMDb: f.IMDb, TMDb: f.TMDb, TVDB: f.TVDB,
		Flags: slices.Clone(f.Flags), GUID: f.InfoURL, Link: NewLink(f.Link), Magnet: NewLink(f.Magnet),
		Privacy: defPrivacy}
	if f.SeedersKnown {
		r.Seeders, r.Peers = f.Seeders, f.Peers
	}
	for k, v := range f.Extras {
		r.setExtra(k, v)
	}
	c.stamp(ctx, &r)
	key := r.InfoHash
	if key == "" {
		key = r.Title + "\x00" + f.Link + "\x00" + f.Magnet
	}
	if seen[key] {
		return
	}
	seen[key] = true
	res.Releases = append(res.Releases, r)
}

// solverTarget is the indexer's target for the FlareSolverr leg: a solve sends no headers until its browser is done,
// so the gateway waits a little longer than the engine's own SolverTimeout (which then ends it with a clear
// message), and a solve that runs out of time is not counted against the indexer.
func (c *cardigannSearcher) solverTarget() gateway.Target {
	t := c.target()
	t.Wait = cardigann.SolverTimeout + 15*time.Second
	return t
}

// RSSProblem says why the indexer cannot feed the RSS sync, or "" when it can: the catalog is off, the definition
// is missing, or the definition has no search without keywords for movies or TV (cardigann.RSSProblem).
func (c *cardigannSearcher) RSSProblem(ctx context.Context) string {
	if c.m == nil || c.m.Definitions == nil {
		return "the definition catalog is not running"
	}
	def, _, err := c.m.Definitions.Load(ctx, c.ix.Definition)
	if err != nil {
		return fmt.Sprintf("definition %s: %v", c.ix.Definition, err)
	}
	return cardigann.RSSProblem(def)
}

// instance builds the engine's view of the indexer: settings with the secret ones from the credential, and the
// definition's privacy type as a label for its releases.
func (c *cardigannSearcher) instance(ctx context.Context) (cardigann.Instance, *cardigann.Definition, policy.Privacy, error) {
	if c.m.Definitions == nil {
		return cardigann.Instance{}, nil, "", fmt.Errorf("indexer %s: %w", c.key, ErrNoCatalog)
	}
	def, _, err := c.m.Definitions.Load(ctx, c.ix.Definition)
	if err != nil {
		return cardigann.Instance{}, nil, "", fmt.Errorf("indexer %s: %w", c.key, err)
	}
	defPrivacy := policy.Privacy(cardigann.Privacy(def.Type))
	secret := map[string]bool{}
	for _, s := range def.Settings {
		if cardigann.SecretSetting(s.Type, s.Name) {
			secret[s.Name] = true
		}
	}
	settings := map[string]string{}
	for k, v := range c.ix.Settings {
		if secret[k] {
			return cardigann.Instance{}, nil, "", fmt.Errorf("indexer %s: settings.%s holds a credential: put it in the indexer's secret", c.key, k)
		}
		settings[k] = v
	}
	creds, err := c.credentials()
	if err != nil {
		return cardigann.Instance{}, nil, "", err
	}
	for k, v := range creds {
		settings[k] = v
	}
	return cardigann.Instance{Key: c.key, DefinitionID: c.ix.Definition, BaseURL: c.ix.URL, Settings: settings,
		Hosts: c.ix.Hosts}, def, defPrivacy, nil
}

// credentials reads the indexer's secret: a JSON object of the definition's secret settings ({"username": ...,
// "password": ...}). None configured: no credentials.
func (c *cardigannSearcher) credentials() (map[string]string, error) {
	raw, err := c.apiKey()
	if err != nil || raw == "" {
		return nil, err
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		// The error would quote the secret: say what is wrong without it.
		return nil, fmt.Errorf("indexer %s: the credential must be a JSON object of the secret settings, e.g. {\"username\":\"...\",\"password\":\"...\"}", c.key)
	}
	return out, nil
}

// cardigannRequests are the tiers of engine searches for q, as a Torznab feed is asked (criteria.go, torznab.go): an
// id search first when the definition's caps.modes lists an id the title has, then the keyword searches only when
// the id search found nothing (a plain series query without criteria is asked by id alone when it has one). A scene
// mapping's search mode picks id, title or both for criteria. ok is false for an id-only
// search of criteria the Torznab path also asks by id only, when there is no id to send: it does not become a title
// search.
func cardigannRequests(q Query, def *cardigann.Definition) (tiers [][]cardigann.Query, ok bool) {
	cats := cardigannCategories(q)
	vars := cardigann.Query{Categories: cats}
	if q.Type == QueryMovie && q.Year > 0 && def.SearchParam("movie-search", "year") {
		vars.Year = strconv.Itoa(q.Year)
	}
	tvVars := q.Type == QuerySeries
	if tvVars {
		switch q.Criteria {
		case CriteriaEpisode:
			vars.Season, vars.Ep = strconv.Itoa(q.Season), strconv.Itoa(q.Episode)
			vars.Episode = fmt.Sprintf("S%02dE%02d", q.Season, q.Episode)
		case CriteriaSeason:
			vars.Season, vars.Episode = strconv.Itoa(q.Season), fmt.Sprintf("S%02d", q.Season)
		case CriteriaDaily:
			if len(q.AirDate) == 10 {
				vars.Season, vars.Ep = q.AirDate[:4], q.AirDate[5:7]+"/"+q.AirDate[8:10]
				vars.Episode = strings.ReplaceAll(q.AirDate, "-", ".")
			}
		case "":
			switch {
			case q.Episode > 0:
				vars.Season, vars.Ep = strconv.Itoa(q.Season), strconv.Itoa(q.Episode)
				vars.Episode = fmt.Sprintf("S%02dE%02d", q.Season, q.Episode)
			case q.Season > 0:
				vars.Season, vars.Episode = strconv.Itoa(q.Season), fmt.Sprintf("S%02d", q.Season)
			}
		default:
			// Anime and daily-season criteria: keywords only.
			tvVars = false
		}
	}
	var keywords []cardigann.Query
	for _, kw := range cardigannQueries(q) {
		kq := vars
		kq.Keywords = kw
		keywords = append(keywords, kq)
	}

	// The search mode applies to Sonarr's criteria only; movies and plain series queries ignore it, as on a Torznab
	// feed (torznab.go requests and params).
	criteria := q.Type == QuerySeries && q.Criteria != ""
	var id *cardigann.Query
	if !criteria || q.Mode != SearchTitle {
		iq := vars
		iq.ByID, iq.Keywords = true, vars.Episode
		switch {
		case q.Type == QueryMovie && q.IMDb != "" && def.SearchParam("movie-search", "imdbid"):
			iq.IMDBID, id = q.IMDb, &iq
		case q.Type == QueryMovie && q.TMDb > 0 && def.SearchParam("movie-search", "tmdbid"):
			iq.TMDBID, id = q.TMDb, &iq
		case !tvVars:
		case q.TVDB > 0 && def.SearchParam("tv-search", "tvdbid"):
			iq.TVDBID, id = q.TVDB, &iq
		case q.IMDb != "" && def.SearchParam("tv-search", "imdbid"):
			iq.IMDBID, id = q.IMDb, &iq
		case q.TMDb > 0 && def.SearchParam("tv-search", "tmdbid"):
			iq.TMDBID, id = q.TMDb, &iq
		}
	}
	switch {
	case id == nil && q.Mode == SearchID && q.Type == QuerySeries && q.Criteria != "" &&
		q.Criteria != CriteriaAnimeEpisode && q.Criteria != CriteriaAnimeSeason:
		// Sonarr's criteria asked by id only (criteria.go): no id, no search.
		return nil, false
	case id == nil:
		return [][]cardigann.Query{keywords}, true
	case q.Type == QuerySeries && !criteria:
		// A plain series query is one request on a Torznab feed: the id when there is one, with no title after it.
		return [][]cardigann.Query{{*id}}, true
	case !criteria:
		// A movie: the id, then the title when the id found nothing.
		return [][]cardigann.Query{{*id}, keywords}, true
	case q.Mode == SearchID:
		return [][]cardigann.Query{{*id}}, true
	case q.Mode == SearchID|SearchTitle:
		return [][]cardigann.Query{append([]cardigann.Query{*id}, keywords...)}, true
	}
	return [][]cardigann.Query{{*id}, keywords}, true
}

// cardigannKeywords turns a query into the keywords a site's search box takes.
func cardigannKeywords(q Query) string {
	switch q.Type {
	case QueryMovie:
		if q.Year > 0 {
			return fmt.Sprintf("%s %d", q.Title, q.Year)
		}
		return q.Title
	case QuerySeries:
		switch q.Criteria {
		case CriteriaDaily:
			return q.Title + " " + strings.ReplaceAll(q.AirDate, "-", " ")
		case CriteriaDailySeason:
			return fmt.Sprintf("%s %d", q.Title, q.AirYear)
		case CriteriaAnimeEpisode:
			if q.Absolute > 0 {
				return fmt.Sprintf("%s %02d", q.Title, q.Absolute)
			}
		}
		switch {
		case q.Episode > 0:
			return fmt.Sprintf("%s S%02dE%02d", q.Title, q.Season, q.Episode)
		case q.Season > 0:
			return fmt.Sprintf("%s S%02d", q.Title, q.Season)
		}
		return q.Title
	case QueryText:
		return q.Title
	}
	return ""
}

// cardigannQueries expands the scene names of keyword criteria. Names that already include the anime season
// need no Sxx suffix. Anime episodes also get the standard-number query Sonarr sends alongside absolute numbers.
func cardigannQueries(q Query) []string {
	if q.Type != QuerySeries || q.Criteria == "" {
		return []string{cardigannKeywords(q)}
	}
	titles := q.SceneTitles
	if len(titles) == 0 {
		titles = []string{q.Title}
	}
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, title := range titles {
		qq := q
		qq.Title = title
		if q.Criteria == CriteriaAnimeSeason && containsFold(q.SeasonTitles, title) {
			add(title)
		} else {
			add(cardigannKeywords(qq))
		}
		if q.Criteria == CriteriaAnimeEpisode && q.Absolute > 0 && q.Season > 0 && q.Episode > 0 {
			qq.Criteria = CriteriaEpisode
			add(cardigannKeywords(qq))
		}
	}
	return out
}

func cardigannCategories(q Query) []int {
	if len(q.Categories) > 0 {
		return q.Categories
	}
	switch q.Type {
	case QueryMovie:
		return []int{2000}
	case QuerySeries:
		return q.defaultCategories()
	}
	return nil
}

// gatewayTransport sends the engine's requests through the gateway as one indexer's: one hop each (the engine
// follows redirects itself, so every hop is checked again), with the engine's browser User-Agent.
type gatewayTransport struct {
	gw     Gateway
	target gateway.Target
	count  *atomic.Int64
}

func (t *gatewayTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tg := t.target
	tg.NoFollow = true
	tg.UserAgent = strings.TrimSpace(cardigann.RequestUserAgent(req))
	if t.count != nil {
		t.count.Add(1)
	}
	return t.gw.Do(req.Context(), tg, req)
}
