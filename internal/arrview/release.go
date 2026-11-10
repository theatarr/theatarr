package arrview

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/availability"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/indexer"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// The decision endpoints: GET release (interactive search with our decisions), POST release (grab one),
// POST release/push (decide a pushed release and route it). A release is decided for each of the view's versions
// the title has; it is approved when any of them accepts it, and a grab goes to the router for the versions that
// accepted it (all of them when none did: the arrs let a user grab a rejected release). The router writes only for
// active versions while router.enabled is true; for a shadow version it records a dry-run plan.

// releaseTTL is how long a searched release can be grabbed (the arrs' release cache: 30 minutes).
const releaseTTL = 30 * time.Minute

var errMovieUnavailable = errors.New("movie has not reached minimum availability")

// found is one release with the decisions of the view's versions, as cached for a grab.
type found struct {
	view      string
	titleID   int64
	rel       indexer.Release
	decisions []*decide.Decision // in the view's version order
	at        time.Time
	seq       int64 // put order, newest highest
}

// accepted are the decisions that accepted the release, or every decision when none did.
func (f *found) grabbable() []*decide.Decision {
	var out []*decide.Decision
	for _, d := range f.decisions {
		if d.Accepted {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		return f.decisions
	}
	return out
}

// best is the decision that speaks for the release: the first accepted one, else the first.
func (f *found) best() *decide.Decision {
	for _, d := range f.decisions {
		if d.Accepted {
			return d
		}
	}
	if len(f.decisions) > 0 {
		return f.decisions[0]
	}
	return &decide.Decision{}
}

// ReleaseCache keeps interactive search results across runtime generations.
type ReleaseCache struct {
	mu  sync.Mutex
	m   map[string]*found
	seq int64
	now func() time.Time
}

// NewReleaseCache creates a cache with the interactive release lifetime.
func NewReleaseCache(now func() time.Time) *ReleaseCache {
	if now == nil {
		now = time.Now
	}
	return &ReleaseCache{m: map[string]*found{}, now: now}
}

func (c *ReleaseCache) put(f *found, indexerID int64, guid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for k, x := range c.m {
		if now.Sub(x.at) > releaseTTL {
			delete(c.m, k)
		}
	}
	c.seq++
	f.at, f.seq = now, c.seq
	c.m[cacheKey(f.view, f.titleID, indexerID, guid)] = f
}

// get finds a cached release of the view. With a title id it is that title's entry; without one (Ruddarr, LunaSea
// and upgrade scripts send only guid and indexerId) the title searched last, as the arrs' cache answers.
func (c *ReleaseCache) get(view string, titleID, indexerID int64, guid string) *found {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if titleID > 0 {
		f := c.m[cacheKey(view, titleID, indexerID, guid)]
		if f == nil || now.Sub(f.at) > releaseTTL {
			return nil
		}
		return f
	}
	var best *found
	suffix := "|" + strconv.FormatInt(indexerID, 10) + "_" + guid
	for k, f := range c.m {
		if f.view != view || !strings.HasSuffix(k, suffix) || now.Sub(f.at) > releaseTTL {
			continue
		}
		if best == nil || f.seq > best.seq {
			best = f
		}
	}
	return best
}

// cacheKey is one release of one title as one view showed it: views decide for their own versions, and two titles
// can return the same release (one info hash, one guid).
func cacheKey(view string, titleID, indexerID int64, guid string) string {
	return view + "|" + strconv.FormatInt(titleID, 10) + "|" + strconv.FormatInt(indexerID, 10) + "_" + guid
}

// indexerIDs numbers the policy's indexers for the arr API: their keys sorted, from 1. The same in every view.
func (s *Service) indexerIDs() map[string]int64 {
	out := map[string]int64{}
	for i, k := range sortedKeys(s.pol.Indexers) {
		out[k] = int64(i + 1)
	}
	return out
}

// guidOf is the guid the views give a release: never the source's own (it can be a URL with a passkey).
func guidOf(rel *indexer.Release) string {
	if h := indexer.NormalizeHash(rel.InfoHash); h != "" {
		return releaseGUID(h, rel.Indexer)
	}
	sum := sha1.Sum([]byte(rel.Indexer + "\x00" + rel.Title + "\x00" + strconv.FormatInt(rel.Size, 10)))
	return "theatarr:" + rel.Indexer + ":t" + hex.EncodeToString(sum[:10])
}

// releaseResource is ReleaseResource (src/Radarr.Api.V3/Indexers/ReleaseResource.cs @ a96bf7c, src/Sonarr.Api.V3/
// Indexers/ReleaseResource.cs @ cab419a). Ruddarr requires guid, title, size, age, ageMinutes, rejected,
// downloadAllowed, customFormatScore, protocol, indexerId, quality, languages, rejections (strings), qualityWeight
// and releaseWeight (Sonarr also fullSeason, episodeRequested, special, isPossibleSpecialEpisode, seasonNumber);
// autobrr and decypharr read approved, rejected, temporarilyRejected and rejections as strings.
type releaseResource struct {
	GUID                string           `json:"guid"`
	Quality             qualityModel     `json:"quality"`
	QualityWeight       int              `json:"qualityWeight"`
	Age                 int              `json:"age"`
	AgeHours            float64          `json:"ageHours"`
	AgeMinutes          float64          `json:"ageMinutes"`
	Size                int64            `json:"size"`
	IndexerID           int64            `json:"indexerId"`
	Indexer             string           `json:"indexer,omitempty"`
	ReleaseGroup        string           `json:"releaseGroup,omitempty"`
	ReleaseHash         string           `json:"releaseHash,omitempty"`
	Title               string           `json:"title"`
	SceneSource         bool             `json:"sceneSource"`
	Languages           []language       `json:"languages"`
	Approved            bool             `json:"approved"`
	TemporarilyRejected bool             `json:"temporarilyRejected"`
	Rejected            bool             `json:"rejected"`
	Rejections          []string         `json:"rejections"`
	PublishDate         arrTime          `json:"publishDate"`
	DownloadAllowed     bool             `json:"downloadAllowed"`
	ReleaseWeight       int              `json:"releaseWeight"`
	CustomFormats       []customFormatID `json:"customFormats"`
	CustomFormatScore   int              `json:"customFormatScore"`
	MagnetURL           string           `json:"magnetUrl,omitempty"`
	InfoHash            string           `json:"infoHash,omitempty"`
	Seeders             *int             `json:"seeders,omitempty"`
	Leechers            *int             `json:"leechers,omitempty"`
	Protocol            string           `json:"protocol"`

	// Radarr.
	MovieTitles    []string `json:"movieTitles,omitempty"`
	MappedMovieID  *int64   `json:"mappedMovieId,omitempty"`
	TmdbID         *int     `json:"tmdbId,omitempty"`
	MovieRequested *bool    `json:"movieRequested,omitempty"`
	Edition        string   `json:"edition,omitempty"`

	// Sonarr.
	FullSeason               *bool                    `json:"fullSeason,omitempty"`
	SeasonNumber             *int                     `json:"seasonNumber,omitempty"`
	SeriesTitle              string                   `json:"seriesTitle,omitempty"`
	EpisodeNumbers           []int                    `json:"episodeNumbers,omitempty"`
	AbsoluteEpisodeNumbers   []int                    `json:"absoluteEpisodeNumbers,omitempty"`
	MappedSeasonNumber       *int                     `json:"mappedSeasonNumber,omitempty"`
	MappedEpisodeNumbers     []int                    `json:"mappedEpisodeNumbers,omitempty"`
	MappedSeriesID           *int64                   `json:"mappedSeriesId,omitempty"`
	MappedEpisodeInfo        []releaseEpisodeResource `json:"mappedEpisodeInfo,omitempty"`
	TvdbID                   *int                     `json:"tvdbId,omitempty"`
	TvRageID                 *int                     `json:"tvRageId,omitempty"`
	EpisodeRequested         *bool                    `json:"episodeRequested,omitempty"`
	IsDaily                  *bool                    `json:"isDaily,omitempty"`
	IsAbsoluteNumbering      *bool                    `json:"isAbsoluteNumbering,omitempty"`
	IsPossibleSpecialEpisode *bool                    `json:"isPossibleSpecialEpisode,omitempty"`
	Special                  *bool                    `json:"special,omitempty"`

	// imdbId is an int in Radarr and a string in Sonarr; indexerFlags a list of names in Radarr, a number in
	// Sonarr.
	ImdbID       any `json:"imdbId,omitempty"`
	IndexerFlags any `json:"indexerFlags,omitempty"`

	// Theatarr extension, not in the arrs: the debrid providers cache evidence says hold the release cached ("rd", "ad",
	// "tb"; DMM's cached_on, the same hash in another DMM feed, stored evidence, TorBox's check). Left out when
	// unknown, which never means uncached. Clients decode with encoding/json, sonic or Swift's Decodable,
	// which all skip unknown keys; decypharr sends the element back verbatim and POST release ignores it.
	CachedOn []string `json:"cachedOn,omitempty"`
}

type releaseEpisodeResource struct {
	ID                    int64  `json:"id"`
	SeasonNumber          int    `json:"seasonNumber"`
	EpisodeNumber         int    `json:"episodeNumber"`
	AbsoluteEpisodeNumber *int   `json:"absoluteEpisodeNumber,omitempty"`
	Title                 string `json:"title,omitempty"`
}

// customFormatID is CustomFormatResource as the release and parse resources write it (ToResource(false)): id and
// name. Our formats have no arr ids; the id is a stable number made from the name.
type customFormatID struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func formatIDs(fs []decide.FormatScore) []customFormatID {
	out := []customFormatID{}
	for _, f := range fs {
		out = append(out, customFormatID{ID: int64(crc32.ChecksumIEEE([]byte(f.Name))%1_000_000) + 1, Name: f.Name})
	}
	return out
}

// flagNames are the arr indexer flags of a release (TorznabRssParser.GetFlags over the torznab tags and volume
// factors), with Radarr's names.
func flagNames(rel *indexer.Release) []string {
	var out []string
	factor := func(k string) float64 {
		if s, ok := rel.Extras[k]; ok {
			if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
				return f
			}
		}
		return 1
	}
	switch factor("downloadvolumefactor") {
	case 0:
		out = append(out, "G_Freeleech")
	case 0.5:
		out = append(out, "G_Halfleech")
	case 0.25:
		out = append(out, "G_Freeleech75")
	case 0.75:
		out = append(out, "G_Freeleech25")
	}
	if factor("uploadvolumefactor") == 2 {
		out = append(out, "G_DoubleUpload")
	}
	for _, t := range rel.Flags {
		switch strings.ToLower(t) {
		case "internal":
			out = append(out, "G_Internal")
		case "scene":
			out = append(out, "G_Scene")
		}
	}
	return out
}

// cachedOn are the providers cache evidence says hold a release cached, as rd, ad and tb in that order; nil when
// unknown: its source's cached_on (DMM), the same hash in a DMM feed of the same search, stored evidence and the
// TorBox check (indexer.Release.CachedList).
func cachedOn(rel *indexer.Release) []string {
	return rel.CachedList()
}

// qualityWeight is the arrs' ReleaseControllerBase weight: the quality's index in the default list × 100 plus the
// revision.
func qualityWeight(flavour, name string, rev profile.Revision) int {
	fl := format.Radarr
	if flavour == Sonarr {
		fl = format.Sonarr
	}
	w := 0
	for i, q := range profile.Qualities(fl) {
		if q.Name == name {
			w = i * 100
			break
		}
	}
	return w + rev.Real*10 + rev.Version
}

// rejectionStrings are a decision's rejections as the arrs write them (messages); prefix names the version when
// the view has several.
func rejectionStrings(f *found, multi bool) (out []string, temporary bool) {
	out = []string{}
	temporary = true
	any := false
	for _, d := range f.decisions {
		if d.Accepted {
			return []string{}, false
		}
		for _, r := range d.Rejections {
			any = true
			msg := r.Message
			if msg == "" {
				msg = r.Reason
			}
			if multi {
				msg = d.Version + ": " + msg
			}
			out = append(out, msg)
			temporary = temporary && r.Temporary
		}
	}
	return out, temporary && any
}

// releaseFrom builds the resource for one release.
func (s *Service) releaseFrom(v *View, f *found, weight int) releaseResource {
	d := f.best()
	rel := &f.rel
	now := s.now()
	age := now.Sub(rel.PublishDate)
	if rel.PublishDate.IsZero() || age < 0 {
		age = 0
	}
	hash := indexer.NormalizeHash(rel.InfoHash)
	r := releaseResource{GUID: guidOf(rel), Quality: qualityModelFor(v.Flavour, d.Quality, d.Revision.Version),
		QualityWeight: qualityWeight(v.Flavour, d.Quality, d.Revision), Age: int(age.Hours() / 24),
		AgeHours: math.Round(age.Hours()*100) / 100, AgeMinutes: math.Round(age.Minutes()*100) / 100,
		Size: rel.Size, IndexerID: s.indexerIDs()[rel.Indexer], Indexer: rel.Indexer, ReleaseGroup: d.ReleaseGroup,
		Title: rel.Title, Languages: languagesFrom(v.Flavour, d.Languages), PublishDate: at(rel.PublishDate),
		DownloadAllowed: hash != "", ReleaseWeight: weight, CustomFormats: formatIDs(d.Formats),
		CustomFormatScore: d.Score, Protocol: "torrent", CachedOn: cachedOn(rel)}
	if rel.PublishDate.IsZero() {
		r.PublishDate = at(minTime)
	}
	if r.Quality.Revision.Real == 0 && d.Revision.Real > 0 {
		r.Quality.Revision.Real = d.Revision.Real
	}
	r.Quality.Revision.IsRepack = d.Revision.IsRepack
	rej, temp := rejectionStrings(f, len(f.decisions) > 1)
	r.Approved = len(rej) == 0 && len(f.decisions) > 0
	r.Rejections = rej
	r.TemporarilyRejected = !r.Approved && temp
	r.Rejected = !r.Approved && !temp
	if hash != "" {
		r.InfoHash = hash
		r.MagnetURL = indexer.BareMagnet(hash).URL()
	}
	if rel.Seeders >= 0 {
		n := rel.Seeders
		r.Seeders = &n
		if rel.Peers >= rel.Seeders {
			l := rel.Peers - rel.Seeders
			r.Leechers = &l
		}
	}
	mapped := f.titleID > 0 && d.TitleID == f.titleID
	if v.Flavour == Radarr {
		r.MovieTitles = d.MovieTitles
		if r.MovieTitles == nil {
			r.MovieTitles = []string{}
		}
		t, b := rel.TMDb, true
		r.TmdbID, r.MovieRequested, r.Edition = &t, &b, d.Edition
		imdb, _ := strconv.Atoi(strings.TrimPrefix(rel.IMDb, "tt"))
		r.ImdbID = imdb
		r.IndexerFlags = append([]string{}, flagNames(rel)...)
		if mapped {
			id := viewID(f.titleID)
			r.MappedMovieID = &id
		}
		return r
	}
	full := d.ReleaseType == "seasonPack"
	season := 0
	if d.Season != nil {
		season = *d.Season
	}
	t, zero, yes := rel.TVDB, 0, true
	daily, absolute, special := false, len(d.Absolute) > 0 && len(d.Numbers) == 0, season == 0 && !full
	r.FullSeason, r.SeasonNumber, r.SeriesTitle = &full, &season, d.SeriesTitle
	r.EpisodeNumbers, r.AbsoluteEpisodeNumbers = d.Numbers, d.Absolute
	r.TvdbID, r.TvRageID, r.EpisodeRequested = &t, &zero, &yes
	r.IsDaily, r.IsAbsoluteNumbering, r.IsPossibleSpecialEpisode, r.Special = &daily, &absolute, &special, &special
	r.ImdbID = rel.IMDb
	flags, _ := format.Sonarr.IndexerFlagsValue(strings.ReplaceAll(strings.Join(flagNames(rel), ","), "G_", ""))
	r.IndexerFlags = flags
	if mapped {
		id := viewID(f.titleID)
		r.MappedSeriesID = &id
		if d.Season != nil {
			ms := *d.Season
			r.MappedSeasonNumber = &ms
		}
		r.MappedEpisodeNumbers = d.Numbers
	}
	return r
}

// mapEpisodes fills mappedEpisodeInfo from the decision's episode ids.
func (s *Service) mapEpisodes(ctx context.Context, rs []releaseResource, fs []*found) {
	ids := map[int64]bool{}
	for _, f := range fs {
		for _, id := range f.best().EpisodeIDs {
			ids[id] = true
		}
	}
	if len(ids) == 0 {
		return
	}
	args := []any{}
	for id := range ids {
		args = append(args, id)
	}
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT e.id, e.season, e.number, e.name, (SELECT n.number FROM
		episode_number n WHERE n.episode_id = e.id AND n.scheme = 'absolute_tvdb' LIMIT 1) FROM episode e
		WHERE e.id IN (`+store.Placeholders(len(args))+`)`, args...)
	if err != nil {
		return
	}
	info := map[int64]releaseEpisodeResource{}
	for rows.Next() {
		var x releaseEpisodeResource
		var abs *int
		if rows.Scan(&x.ID, &x.SeasonNumber, &x.EpisodeNumber, &x.Title, &abs) == nil {
			x.AbsoluteEpisodeNumber = abs
			info[x.ID] = x
		}
	}
	_ = closeRows(rows)
	for i, f := range fs {
		for _, id := range f.best().EpisodeIDs {
			if x, ok := info[id]; ok {
				rs[i].MappedEpisodeInfo = append(rs[i].MappedEpisodeInfo, x)
			}
		}
	}
}

// releaseKey identifies one release across the versions' searches.
func releaseKey(rel *indexer.Release) string {
	if h := indexer.NormalizeHash(rel.InfoHash); h != "" {
		return "h:" + h + "|" + rel.Indexer
	}
	return "t:" + rel.Indexer + "|" + rel.Title
}

// GET release?movieId= (Radarr) or ?episodeId= / ?seriesId=&seasonNumber= (Sonarr): an interactive search of the
// title's versions in the view, each through its own indexers, with our decision per release. Without a title the
// arrs answer their RSS cache; we keep none, so the list is empty.
func (s *Service) searchReleases(w http.ResponseWriter, r *request) {
	ctx := r.Context()
	var titleID int64
	var season *int
	var episodes []int64
	if r.v.Flavour == Radarr {
		id := int64(r.queryInt("movieId", 0))
		if id == 0 {
			writeJSON(w, http.StatusOK, []releaseResource{})
			return
		}
		tid, err := s.resolve(ctx, r.v, id)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		titleID = tid
	} else {
		if eid := int64(r.queryInt("episodeId", 0)); eid > 0 {
			var sn int
			err := s.st.Reader().QueryRowContext(ctx, `SELECT title_id, season FROM episode WHERE id = ?`, eid).
				Scan(&titleID, &sn)
			if err != nil {
				notFound(w, "Episode not found")
				return
			}
			season, episodes = &sn, []int64{eid}
		} else if sid, sn := int64(r.queryInt("seriesId", 0)), r.queryInt("seasonNumber", -1); sid > 0 && sn >= 0 {
			tid, err := s.resolve(ctx, r.v, sid)
			if err != nil {
				s.internal(w, r, err)
				return
			}
			titleID, season = tid, &sn
			rows, err := s.st.Reader().QueryContext(ctx, `SELECT id FROM episode WHERE title_id = ? AND season = ?
				ORDER BY number`, tid, sn)
			if err != nil {
				s.internal(w, r, err)
				return
			}
			for rows.Next() {
				var id int64
				if rows.Scan(&id) == nil {
					episodes = append(episodes, id)
				}
			}
			_ = closeRows(rows)
		} else {
			writeJSON(w, http.StatusOK, []releaseResource{})
			return
		}
	}
	e, err := s.loadOne(ctx, r.v, titleID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if e == nil {
		notFound(w, "Title not found in this view")
		return
	}
	if s.decider == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody{Message: "Search failed: the decision engine is not running"})
		return
	}
	byKey := map[string]*found{}
	versionKeys := make([]string, 0, len(e.versions))
	for _, x := range e.versions {
		versionKeys = append(versionKeys, x.key)
	}
	searched := map[string]*router.Searched{}
	if batch, ok := s.decider.(interface {
		InteractiveMany(context.Context, int64, []string, *int, []int64) (map[string]*router.Searched, error)
	}); ok {
		searched, err = batch.InteractiveMany(ctx, e.id, versionKeys, season, episodes)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			writeJSON(w, http.StatusBadRequest, errorBody{Message: "Search failed: " + err.Error()})
			return
		}
	} else {
		for _, x := range e.versions {
			sr, err := s.decider.Interactive(ctx, e.id, x.key, season, episodes)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				writeJSON(w, http.StatusBadRequest, errorBody{Message: "Search failed: " + err.Error()})
				return
			}
			searched[x.key] = sr
		}
	}
	type ranked struct {
		f    *found
		rank int
	}
	var order []*ranked
	var notes []string
	for _, x := range e.versions {
		sr := searched[x.key]
		if sr == nil {
			continue
		}
		notes = append(notes, sr.Notes...)
		for i, d := range sr.Ranked {
			rel, ok := sr.Releases[d]
			if !ok {
				continue
			}
			k := releaseKey(&rel)
			f := byKey[k]
			if f == nil {
				f = &found{view: r.v.Key, titleID: e.id, rel: rel}
				byKey[k] = f
				order = append(order, &ranked{f: f, rank: i})
			}
			f.decisions = append(f.decisions, d)
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		ai, aj := order[i].f.best().Accepted, order[j].f.best().Accepted
		if ai != aj {
			return ai
		}
		return order[i].rank < order[j].rank
	})
	out := make([]releaseResource, 0, len(order))
	var fs []*found
	for i, o := range order {
		rr := s.releaseFrom(r.v, o.f, i+1)
		s.releases.put(o.f, rr.IndexerID, rr.GUID)
		out = append(out, rr)
		fs = append(fs, o.f)
	}
	if r.v.Flavour == Sonarr {
		s.mapEpisodes(ctx, out, fs)
	}
	if len(notes) > 0 {
		s.log.Info("interactive search notes", "view", r.v.Key, "title", e.id, "notes", notes)
	}
	writeJSON(w, http.StatusOK, out)
}

// grabBody is what POST release reads: guid and indexerId (Ruddarr, LunaSea and upgrade scripts send only those;
// decypharr echoes the whole resource).
type grabBody struct {
	GUID      string `json:"guid"`
	IndexerID int64  `json:"indexerId"`
	// TitleID is the view's movie or series id when the body names one (movieId/seriesId, or mappedMovieId/
	// mappedSeriesId in an echoed resource).
	TitleID int64
}

// POST release (ReleaseController.DownloadRelease): grab a release from the last search. 404 when it is not cached
// any more, as the arrs answer. The grab goes to the router for the versions that accepted it. 200 with the body.
func (s *Service) grabRelease(w http.ResponseWriter, r *request) {
	var raw map[string]any
	if err := decodeBody(r, &raw); err != nil {
		badRequest(w, "", err.Error(), nil)
		return
	}
	b := grabBody{}
	for k, v := range raw {
		switch strings.ToLower(k) {
		case "guid":
			b.GUID, _ = v.(string)
		case "indexerid":
			if f, ok := v.(float64); ok {
				b.IndexerID = int64(f)
			}
		case "movieid", "mappedmovieid", "seriesid", "mappedseriesid":
			if f, ok := v.(float64); ok && f > 0 && b.TitleID == 0 {
				b.TitleID = int64(f)
			}
		}
	}
	if b.GUID == "" {
		badRequest(w, "Guid", "'Guid' must not be empty.", "")
		return
	}
	if b.IndexerID <= 0 {
		badRequest(w, "IndexerId", "'Indexer Id' must be greater than '0'.", b.IndexerID)
		return
	}
	var titleID int64
	if b.TitleID > 0 {
		tid, err := s.resolve(r.Context(), r.v, b.TitleID)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if tid == 0 {
			notFound(w, "Couldn't find requested release in cache, try searching again")
			return
		}
		titleID = tid
	}
	f := s.releases.get(r.v.Key, titleID, b.IndexerID, b.GUID)
	if f == nil || f.view != r.v.Key || (titleID > 0 && f.titleID != titleID) {
		notFound(w, "Couldn't find requested release in cache, try searching again")
		return
	}
	if s.pol.Indexers[f.rel.Indexer] == nil {
		writeJSON(w, http.StatusConflict, errorBody{Message: "release indexer is no longer enabled: " + f.rel.Indexer})
		return
	}
	if s.router == nil {
		writeJSON(w, http.StatusConflict, errorBody{Message: "Getting release from indexer failed",
			Description: "the router is not running"})
		return
	}
	if err := s.routeRelease(r.Context(), f, f.grabbable(), "arrview:release:"+r.v.Key, true); err != nil {
		if errors.Is(err, errMovieUnavailable) {
			writeJSON(w, http.StatusConflict, errorBody{Message: err.Error()})
			return
		}
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, raw)
}

// routeRelease hands one release to the router for each decision's version. A shadow version's route is a dry run
// recorded in router_plan; the router alone decides whether it writes.
func (s *Service) routeRelease(ctx context.Context, f *found, ds []*decide.Decision, source string, userInvoked bool) error {
	// Radarr AvailabilitySpecification.cs:22-26: user-invoked searches and grabs skip availability.
	var ready []*decide.Decision
	unavailable := false
	for _, d := range ds {
		if vp := s.pol.Versions[d.Version]; !userInvoked && vp != nil && vp.Kind == policy.KindMovie {
			available, err := availability.Movie(ctx, s.st.Reader(), s.pol, f.titleID, d.VersionID, d.Version, s.now())
			if err != nil {
				return err
			}
			if !available {
				unavailable = true
				continue
			}
		}
		ready = append(ready, d)
	}
	if len(ready) == 0 && unavailable {
		return errMovieUnavailable
	}
	for _, d := range ready {
		if d.VersionID == 0 {
			continue
		}
		c := router.Candidate{Release: f.rel, Quality: d.Quality, Score: d.Score, Episodes: d.EpisodeIDs,
			Formats: append([]decide.FormatScore{}, d.Formats...), EpisodeMap: d.EpisodeMapping()}
		req := router.Request{TitleID: f.titleID, VersionID: d.VersionID, Version: d.Version,
			Candidates: []router.Candidate{c}, Source: source}
		job, err := s.router.Enqueue(ctx, req, time.Time{})
		if err != nil {
			return fmt.Errorf("arrview: routing %s for %s: %w", f.rel.Title, d.Version, err)
		}
		s.log.Info("release routed", "title", f.titleID, "version", d.Version, "release", f.rel.Title,
			"accepted", d.Accepted, "job", job, "source", source)
	}
	return nil
}

// pushBody is ReleaseResource as release/push reads it. imdbId is a number from Radarr clients (autobrr,
// upgrade scripts) and a "tt…" string from Sonarr clients.
type pushBody struct {
	Title            string  `json:"title"`
	DownloadURL      string  `json:"downloadUrl"`
	MagnetURL        string  `json:"magnetUrl"`
	InfoHash         string  `json:"infoHash"`
	InfoURL          string  `json:"infoUrl"`
	Size             int64   `json:"size"`
	Indexer          string  `json:"indexer"`
	IndexerID        int64   `json:"indexerId"`
	Protocol         any     `json:"protocol"`
	DownloadProtocol any     `json:"downloadProtocol"`
	PublishDate      string  `json:"publishDate"`
	TmdbID           int     `json:"tmdbId"`
	TvdbID           int     `json:"tvdbId"`
	ImdbID           any     `json:"imdbId"`
	Seeders          *int    `json:"seeders"`
	Leechers         *int    `json:"leechers"`
	IndexerFlags     any     `json:"indexerFlags"`
	DownloadClientID *int64  `json:"downloadClientId"`
	DownloadClient   *string `json:"downloadClient"`
}

func protocolOf(v any) string {
	switch x := v.(type) {
	case string:
		return strings.ToLower(x)
	case float64:
		switch int(x) {
		case 1:
			return "usenet"
		case 2:
			return "torrent"
		}
	}
	return ""
}

// pushError is the arrs' 400 for push validation: attemptedValue is a string, as autobrr decodes it.
func pushError(w http.ResponseWriter, prop, msg, attempted string) {
	writeJSON(w, http.StatusBadRequest, []validationError{{PropertyName: prop, ErrorMessage: msg,
		AttemptedValue: attempted, Severity: "error", ErrorCode: "NotEmptyValidator"}})
}

// POST release/push (ReleasePushController.Create): decide a pushed release (autobrr, upgrade scripts) for the title it
// names (tmdbId, tvdbId or imdbId, else the parsed title among the view's titles) and the view's versions of it;
// when a version accepts it, it goes to the router for those versions. 200 with a one-element list.
//
// The API key is the gate. The pushed indexer is matched to a configured indexer by name (IndexerByName; the
// positional indexerId is not used), and the version's indexer list decides as for a search result: an unmatched
// indexer, or one the version does not list, is rejected (IndexerNotInVersion, naming the indexer). Only a v1 info
// hash (infoHash or the magnet's) can be routed: a .torrent link is never fetched.
func (s *Service) releasePush(w http.ResponseWriter, r *request) {
	ctx := r.Context()
	var b pushBody
	if err := decodeBody(r, &b); err != nil {
		badRequest(w, "", err.Error(), nil)
		return
	}
	proto := protocolOf(b.Protocol)
	if proto == "" || proto == "unknown" {
		proto = protocolOf(b.DownloadProtocol)
	}
	switch {
	case strings.TrimSpace(b.Title) == "":
		pushError(w, "Title", "'Title' must not be empty.", "")
		return
	case b.DownloadURL == "" && b.MagnetURL == "" && b.InfoHash == "":
		pushError(w, "DownloadUrl", "'Download Url' must not be empty.", "")
		return
	case proto == "" || proto == "unknown":
		pushError(w, "Protocol", "'Protocol' must not be empty.", "")
		return
	case b.PublishDate == "":
		pushError(w, "PublishDate", "'Publish Date' must not be empty.", "")
		return
	}
	if s.decider == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody{Message: "the decision engine is not running"})
		return
	}
	rel := indexer.Release{Title: strings.TrimSpace(b.Title), Size: b.Size, Seeders: -1, Peers: -1}
	rel.Indexer = s.pol.IndexerByName(b.Indexer)
	if ix := s.pol.Indexers[rel.Indexer]; ix != nil {
		rel.Kind, rel.Privacy = ix.Kind, ix.Privacy
	} else {
		// Not a configured indexer: kept as pushed (or "push" when none was named), so the decision rejects it as an
		// indexer the version does not list.
		rel.Indexer = strings.TrimSpace(b.Indexer)
		if rel.Indexer == "" {
			rel.Indexer = "push"
		}
		// The placeholder of a release added by hand is AddRelease's alone: a pushed release naming it would skip
		// the version's indexer list.
		if policy.IsManualAdd(rel.Indexer) {
			pushError(w, "Indexer", fmt.Sprintf("'Indexer' must not be %q: the name is kept for releases added by hand.", policy.ManualIndexer), rel.Indexer)
			return
		}
	}
	if t, err := time.Parse(time.RFC3339, b.PublishDate); err == nil {
		rel.PublishDate = t.UTC()
	}
	rel.InfoHash = b.InfoHash
	rel.Magnet, rel.Link = indexer.NewLink(b.MagnetURL), indexer.NewLink(b.DownloadURL)
	hash, conflict := indexer.ReleaseIdentity(rel)
	if conflict {
		s.log.Warn("release push rejected: conflicting torrent identities", "indexer", rel.Indexer, "info_hash", indexer.NormalizeHash(b.InfoHash))
		pushError(w, "InfoHash", "Reported hash and magnet identities conflict", "")
		return
	}
	rel.InfoHash = hash

	if b.Seeders != nil {
		rel.Seeders = *b.Seeders
		rel.Peers = *b.Seeders
		if b.Leechers != nil {
			rel.Peers += *b.Leechers
		}
	}
	rel.TMDb, rel.TVDB = b.TmdbID, b.TvdbID
	switch x := b.ImdbID.(type) {
	case float64:
		if x > 0 {
			rel.IMDb = fmt.Sprintf("tt%07d", int64(x))
		}
	case string:
		if x != "" && x != "0" {
			rel.IMDb = "tt" + strings.TrimPrefix(x, "tt")
		}
	}
	if !parses(r.v.Flavour, rel.Title) {
		pushError(w, "Title", "Unable to parse", rel.Title)
		return
	}
	titleID, err := s.mapRelease(ctx, r.v, &rel)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	f := &found{view: r.v.Key, titleID: titleID, rel: rel}
	var why []string
	e, err := s.loadOne(ctx, r.v, titleID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	switch {
	case titleID == 0:
		what := "Movie"
		if r.v.Flavour == Sonarr {
			what = "Series"
		}
		why = append(why, "Unknown "+what+". Unable to identify the title from the release name or ids")
	case e == nil || len(e.versions) == 0:
		why = append(why, "The title has no version in this view")
	case proto == "usenet":
		why = append(why, "Usenet releases are not routed (debrid by info hash only)")
	}
	if len(why) == 0 {
		for _, x := range e.versions {
			d, err := s.decider.Decide(ctx, decide.Release{Release: rel}, e.id, x.key)
			if err != nil {
				s.internal(w, r, err)
				return
			}
			if rel.InfoHash == "" && d.Accepted {
				d.Accepted = false
				d.Rejections = append(d.Rejections, decide.Rejection{Reason: "NoInfoHash", Ours: true,
					Message: "No info hash: a .torrent link is never fetched; push a magnet or infoHash"})
			}
			f.decisions = append(f.decisions, d)
		}
	} else {
		f.decisions = []*decide.Decision{{Rejections: []decide.Rejection{{Reason: "Unknown", Message: why[0]}}}}
	}
	rr := s.releaseFrom(r.v, f, 0)
	if titleID == 0 || e == nil {
		rr.MappedMovieID, rr.MappedSeriesID = nil, nil
	}
	var accepted []*decide.Decision
	for _, d := range f.decisions {
		if d.Accepted {
			accepted = append(accepted, d)
		}
	}
	if len(accepted) > 0 {
		if s.router == nil {
			writeJSON(w, http.StatusConflict, errorBody{Message: "the router is not running"})
			return
		}
		if err := s.routeRelease(ctx, f, accepted, "arrview:push:"+r.v.Key, false); err != nil {
			if errors.Is(err, errMovieUnavailable) {
				writeJSON(w, http.StatusConflict, errorBody{Message: err.Error()})
				return
			}
			s.internal(w, r, err)
			return
		}
	}
	out := []releaseResource{rr}
	if r.v.Flavour == Sonarr {
		s.mapEpisodes(ctx, out, []*found{f})
	}
	writeJSON(w, http.StatusOK, out)
}
