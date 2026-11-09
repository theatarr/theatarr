package arrview

import (
	"context"
	"database/sql"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/metadata/tvdb"
	"github.com/theatarr/theatarr/internal/store"
)

// seriesResource is Sonarr's SeriesResource (src/Sonarr.Api.V3/Series/SeriesResource.cs @ cab419a).
//
// Read by: Seerr (scanners/sonarr/index.ts L165-258: id, tvdbId, titleSlug, title, seasons[].{seasonNumber,
// monitored, statistics.episodeFileCount, statistics.totalEpisodeCount}; availabilitySync.ts: tvdbId,
// statistics.episodeFileCount, seasons[]; sonarr.ts L208-290: lookup id, seasons, tags, monitored), Homepage
// (widgets/sonarr/widget.js L79-85: id, title), decypharr (pkg/arr/content.go: id, tvdbId, path).
type seriesResource struct {
	ID                int64             `json:"id,omitempty"`
	Title             string            `json:"title"`
	AlternateTitles   []any             `json:"alternateTitles"`
	SortTitle         string            `json:"sortTitle,omitempty"`
	Status            string            `json:"status"`
	Ended             bool              `json:"ended"`
	Images            []any             `json:"images"`
	Seasons           []seasonResource  `json:"seasons"`
	Year              int               `json:"year"`
	Path              string            `json:"path,omitempty"`
	QualityProfileID  int64             `json:"qualityProfileId"`
	SeasonFolder      bool              `json:"seasonFolder"`
	Monitored         bool              `json:"monitored"`
	MonitorNewItems   string            `json:"monitorNewItems"`
	UseSceneNumbering bool              `json:"useSceneNumbering"`
	Runtime           int               `json:"runtime"`
	TvdbID            int               `json:"tvdbId"`
	TvRageID          int               `json:"tvRageId"`
	TvMazeID          int               `json:"tvMazeId"`
	TmdbID            int               `json:"tmdbId"`
	FirstAired        *arrTime          `json:"firstAired,omitempty"`
	LastAired         *arrTime          `json:"lastAired,omitempty"`
	SeriesType        string            `json:"seriesType"`
	CleanTitle        string            `json:"cleanTitle,omitempty"`
	ImdbID            string            `json:"imdbId,omitempty"`
	TitleSlug         string            `json:"titleSlug,omitempty"`
	RootFolderPath    string            `json:"rootFolderPath,omitempty"`
	Folder            string            `json:"folder,omitempty"`
	Genres            []string          `json:"genres"`
	Tags              []int64           `json:"tags"`
	Added             arrTime           `json:"added"`
	Ratings           map[string]any    `json:"ratings"`
	Statistics        *seriesStatistics `json:"statistics,omitempty"`
	LanguageProfileID int               `json:"languageProfileId"`
}

// seasonResource is SeasonResource.
type seasonResource struct {
	SeasonNumber int               `json:"seasonNumber"`
	Monitored    bool              `json:"monitored"`
	Statistics   *seasonStatistics `json:"statistics,omitempty"`
}

// seasonStatistics is SeasonStatisticsResource. Counts follow SeriesStatisticsRepository (@ cab419a L78-81):
// episodeCount = monitored and aired, or with a file; totalEpisodeCount = all; episodeFileCount = with a file.
type seasonStatistics struct {
	NextAiring        *arrTime `json:"nextAiring,omitempty"`
	PreviousAiring    *arrTime `json:"previousAiring,omitempty"`
	EpisodeFileCount  int      `json:"episodeFileCount"`
	EpisodeCount      int      `json:"episodeCount"`
	TotalEpisodeCount int      `json:"totalEpisodeCount"`
	SizeOnDisk        int64    `json:"sizeOnDisk"`
	ReleaseGroups     []string `json:"releaseGroups"`
	PercentOfEpisodes float64  `json:"percentOfEpisodes"`
}

// seriesStatistics is SeriesStatisticsResource.
type seriesStatistics struct {
	SeasonCount       int      `json:"seasonCount"`
	EpisodeFileCount  int      `json:"episodeFileCount"`
	EpisodeCount      int      `json:"episodeCount"`
	TotalEpisodeCount int      `json:"totalEpisodeCount"`
	SizeOnDisk        int64    `json:"sizeOnDisk"`
	ReleaseGroups     []string `json:"releaseGroups"`
	PercentOfEpisodes float64  `json:"percentOfEpisodes"`
}

// episodeResource is EpisodeResource (src/Sonarr.Api.V3/Episodes/EpisodeResource.cs @ cab419a).
//
// Read by: Seerr (sonarr.ts L240-262: id, seasonNumber, monitored), decypharr (pkg/arr/library.go: id,
// episodeFileId, seasonNumber, episodeNumber), Homepage (queue details: episode.title).
type episodeResource struct {
	ID                       int64           `json:"id,omitempty"`
	SeriesID                 int64           `json:"seriesId"`
	TvdbID                   int             `json:"tvdbId"`
	EpisodeFileID            int64           `json:"episodeFileId"`
	SeasonNumber             int             `json:"seasonNumber"`
	EpisodeNumber            int             `json:"episodeNumber"`
	Title                    string          `json:"title,omitempty"`
	AirDate                  string          `json:"airDate,omitempty"`
	AirDateUtc               *arrTime        `json:"airDateUtc,omitempty"`
	Runtime                  int             `json:"runtime"`
	HasFile                  bool            `json:"hasFile"`
	Monitored                bool            `json:"monitored"`
	AbsoluteEpisodeNumber    *int            `json:"absoluteEpisodeNumber,omitempty"`
	UnverifiedSceneNumbering bool            `json:"unverifiedSceneNumbering"`
	Series                   *seriesResource `json:"series,omitempty"`
}

// ep is an episode with the view's state of it.
type ep struct {
	id        int64
	titleID   int64
	season    int
	number    int
	name      string
	airDate   string
	airUTC    sql.NullInt64
	runtime   int
	monitored bool // the view's: see loadEpisodes
	hasFile   bool
	fileID    int64
	size      int64
	absolute  sql.NullInt64
}

// loadEpisodes reads the episodes of the given series with the view's state. An episode is monitored when a
// per-version state row of a view version says so; with no such row, when the episode and some view version of the
// series are monitored. It has a file when a view version's state is filled or cutoff_met, or an active link of a
// view version holds it (the link id is its episodeFileId). Episodes the metadata source no longer lists (removed_at,
// kept only for their files and history) are left out, as Sonarr deletes them on refresh.
func (s *Service) loadEpisodes(ctx context.Context, v *View, es []*entry) (map[int64][]*ep, error) {
	return s.loadEpisodesWith(ctx, v, es, false)
}

// loadEpisodesWith is loadEpisodes, with the removed episodes too when removed is set: for resources that name an
// episode a download, file or parse already points at (queue, manual import, parse).
func (s *Service) loadEpisodesWith(ctx context.Context, v *View, es []*entry, removed bool, selected ...[]int64) (map[int64][]*ep, error) {
	out := map[int64][]*ep{}
	if len(es) == 0 {
		return out, nil
	}
	ids := make([]int64, len(es))
	seriesMon := map[int64]bool{}
	for i, e := range es {
		ids[i] = e.id
		seriesMon[e.id] = e.monitored()
	}
	where, args := scope(v, ids)
	db := s.st.Reader()
	byID := map[int64]*ep{}
	idArgs := []any{jsonIDs(ids)}
	episodeFilter := ""
	if len(selected) > 0 {
		episodeFilter = ` AND e.id IN (SELECT value FROM json_each(?))`
	}
	episodeArgs := append(idArgs, removed)
	if len(selected) > 0 {
		episodeArgs = append(episodeArgs, jsonIDs(selected[0]))
	}
	rows, err := db.QueryContext(ctx, `SELECT e.id, e.title_id, e.season, e.number, e.name, COALESCE(e.air_date, ''),
		e.air_date_utc_at, COALESCE(e.runtime_minutes, 0), e.monitored,
		(SELECT n.number FROM episode_number n WHERE n.episode_id = e.id AND n.scheme = 'absolute_tvdb' LIMIT 1)
		FROM episode e WHERE e.title_id IN (SELECT value FROM json_each(?))
		AND (e.removed_at IS NULL OR ?)`+episodeFilter+` ORDER BY e.title_id, e.season, e.number`, episodeArgs...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		x := &ep{}
		if err := rows.Scan(&x.id, &x.titleID, &x.season, &x.number, &x.name, &x.airDate, &x.airUTC, &x.runtime,
			&x.monitored, &x.absolute); err != nil {
			rows.Close()
			return nil, err
		}
		x.monitored = x.monitored && seriesMon[x.titleID]
		byID[x.id] = x
		out[x.titleID] = append(out[x.titleID], x)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	rows, err = db.QueryContext(ctx, `SELECT s.episode_id, MAX(s.monitored AND v.monitored),
		MAX(s.state IN ('filled', 'cutoff_met'))
		FROM episode_version_state s JOIN version v ON v.id = s.version_id JOIN title t ON t.id = v.title_id
		WHERE `+where+` `+selectedEpisodeSQL("s.episode_id", selected)+` GROUP BY s.episode_id`, selectedEpisodeArgs(args, selected)...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var mon, has bool
		if err := rows.Scan(&id, &mon, &has); err != nil {
			rows.Close()
			return nil, err
		}
		if x := byID[id]; x != nil {
			x.monitored, x.hasFile = mon, has
		}
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	// The arr's files (version_file_episode) give the file id where no link of ours holds the episode.
	rows, err = db.QueryContext(ctx, `SELECT fe.episode_id, MIN(vf.id), MAX(vf.size_bytes) FROM version_file_episode fe
		JOIN version_file vf ON vf.id = fe.version_file_id JOIN version v ON v.id = vf.version_id
		JOIN title t ON t.id = v.title_id WHERE `+where+` `+selectedEpisodeSQL("fe.episode_id", selected)+` GROUP BY fe.episode_id`, selectedEpisodeArgs(args, selected)...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, fid, size int64
		if err := rows.Scan(&id, &fid, &size); err != nil {
			rows.Close()
			return nil, err
		}
		if x := byID[id]; x != nil {
			x.hasFile, x.fileID, x.size = true, fid, size
		}
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	rows, err = db.QueryContext(ctx, `SELECT le.episode_id, MIN(l.id), MAX(COALESCE(cf.size_bytes, 0))
		FROM link_episode le JOIN link l ON l.id = le.link_id JOIN link_version lv ON lv.link_id = l.id
		JOIN version v ON v.id = lv.version_id JOIN title t ON t.id = v.title_id
		LEFT JOIN content_file cf ON cf.id = l.content_file_id
		WHERE l.state = 'active' AND `+where+` `+selectedEpisodeSQL("le.episode_id", selected)+` GROUP BY le.episode_id`, selectedEpisodeArgs(args, selected)...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, link, size int64
		if err := rows.Scan(&id, &link, &size); err != nil {
			rows.Close()
			return nil, err
		}
		if x := byID[id]; x != nil {
			x.hasFile, x.fileID, x.size = true, LinkFileBase+link, size
		}
	}
	return out, closeRows(rows)
}

func (x *ep) aired(now time.Time) bool {
	return x.airUTC.Valid && !millis(x.airUTC.Int64).After(now)
}

func (s *Service) seriesResource(v *View, e *entry, eps []*ep) seriesResource {
	now := s.now()
	root, p := s.rootAndPath(v, e)
	status := "continuing"
	if a := e.arr(); a != nil && a.status != "" {
		status = a.status
	}
	sr := seriesResource{ID: e.viewID(), Title: e.name, AlternateTitles: []any{}, SortTitle: sortTitle(e),
		Status: status, Ended: status == "ended", Images: []any{}, Seasons: []seasonResource{}, Year: e.year, Path: p,
		QualityProfileID: qualityProfileID(v, e), SeasonFolder: true, Monitored: e.monitored(), MonitorNewItems: "all",
		TvdbID: e.tvdb, TmdbID: 0, SeriesType: "standard", ImdbID: e.imdb, TitleSlug: slug(e.name),
		RootFolderPath: root, Genres: []string{}, Tags: tagsOrEmpty(e.tags), Added: at(s.added(e)),
		Ratings: map[string]any{}, LanguageProfileID: 1}
	if e.anime {
		sr.SeriesType = "anime"
	}
	if r := e.rec; r != nil {
		sr.SeasonFolder = r.seasonFolder
		if r.seriesType != "" {
			sr.SeriesType = r.seriesType
		}
		if r.monitorNew != "" {
			sr.MonitorNewItems = r.monitorNew
		}
	}
	if mn := monitorNewOf(e); mn != "" {
		sr.MonitorNewItems = mn
	}
	type acc struct {
		st  seasonStatistics
		mon bool
	}
	seasons := map[int]*acc{}
	var nums []int
	total := &seriesStatistics{ReleaseGroups: []string{}}
	var first, last time.Time
	for _, x := range eps {
		a := seasons[x.season]
		if a == nil {
			a = &acc{st: seasonStatistics{ReleaseGroups: []string{}}}
			seasons[x.season] = a
			nums = append(nums, x.season)
		}
		a.st.TotalEpisodeCount++
		if x.monitored {
			a.mon = true
		}
		if x.hasFile {
			a.st.EpisodeFileCount++
			a.st.SizeOnDisk += x.size
		}
		if (x.monitored && x.aired(now)) || x.hasFile {
			a.st.EpisodeCount++
		}
		if x.airUTC.Valid {
			t := millis(x.airUTC.Int64)
			if t.After(now) {
				if a.st.NextAiring == nil || t.Before(time.Time(*a.st.NextAiring)) {
					a.st.NextAiring = atPtr(t)
				}
			} else if a.st.PreviousAiring == nil || t.After(time.Time(*a.st.PreviousAiring)) {
				a.st.PreviousAiring = atPtr(t)
			}
			if x.season > 0 {
				if first.IsZero() || t.Before(first) {
					first = t
				}
				if t.After(last) && !t.After(now) {
					last = t
				}
			}
		}
	}
	sort.Ints(nums)
	for _, n := range nums {
		a := seasons[n]
		if a.st.EpisodeCount > 0 {
			a.st.PercentOfEpisodes = float64(a.st.EpisodeFileCount) / float64(a.st.EpisodeCount) * 100
		}
		st := a.st
		sr.Seasons = append(sr.Seasons, seasonResource{SeasonNumber: n, Monitored: a.mon, Statistics: &st})
		// Series statistics count seasons other than specials, but every episode (SeriesStatistics.cs).
		if n > 0 {
			total.SeasonCount++
		}
		total.EpisodeFileCount += st.EpisodeFileCount
		total.EpisodeCount += st.EpisodeCount
		total.TotalEpisodeCount += st.TotalEpisodeCount
		total.SizeOnDisk += st.SizeOnDisk
	}
	if total.EpisodeCount > 0 {
		total.PercentOfEpisodes = float64(total.EpisodeFileCount) / float64(total.EpisodeCount) * 100
	}
	sr.Statistics = total
	sr.FirstAired = atPtr(first)
	sr.LastAired = atPtr(last)
	return sr
}

// monitorNewOf is the series' monitorNewItems from its view versions' intent (series_monitor), which a metadata
// refresh follows: none when every version refuses new seasons, all when one takes them (a version without a choice
// takes them), "" when no version has a choice (the view's record then says).
func monitorNewOf(e *entry) string {
	none, any := 0, false
	for _, x := range e.versions {
		switch x.monitorNew {
		case "none":
			none++
			any = true
		case "all":
			any = true
		}
	}
	switch {
	case !any:
		return ""
	case none == len(e.versions):
		return "none"
	}
	return "all"
}

// slug is Sonarr's title slug: lower-case, runs of anything but letters and digits as one '-'.
func slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

func episodeFrom(e *entry, x *ep) episodeResource {
	r := episodeResource{ID: x.id, SeriesID: e.viewID(), EpisodeFileID: x.fileID, SeasonNumber: x.season,
		EpisodeNumber: x.number, Title: x.name, AirDate: x.airDate, Runtime: x.runtime, HasFile: x.hasFile,
		Monitored: x.monitored}
	if x.airUTC.Valid {
		r.AirDateUtc = atPtr(millis(x.airUTC.Int64))
	}
	if x.absolute.Valid {
		n := int(x.absolute.Int64)
		r.AbsoluteEpisodeNumber = &n
	}
	return r
}

func (s *Service) seriesResources(ctx context.Context, v *View, es []*entry) ([]seriesResource, error) {
	eps, err := s.loadEpisodes(ctx, v, es)
	if err != nil {
		return nil, err
	}
	out := make([]seriesResource, 0, len(es))
	for _, e := range es {
		out = append(out, s.seriesResource(v, e, eps[e.id]))
	}
	return out, nil
}

// GET series[?tvdbId=] (SeriesController.AllSeries).
func (s *Service) seriesList(w http.ResponseWriter, r *request) {
	ctx := r.Context()
	var ids []int64
	if t, ok := r.query("tvdbId"); ok {
		n, _ := strconv.ParseInt(t, 10, 64)
		tid, err := s.titleByExternal(ctx, r.v, 0, n)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		ids = []int64{tid}
	}
	es, err := s.load(ctx, r.v, ids)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := s.seriesResources(ctx, r.v, es)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GET series/{id}.
func (s *Service) series(w http.ResponseWriter, r *request) {
	id, ok := r.pathID(1)
	if !ok {
		notFound(w, "Series not found")
		return
	}
	e, err := s.resolveEntry(r.Context(), r.v, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if e == nil {
		notFound(w, "Series with ID "+strconv.FormatInt(id, 10)+" does not exist")
		return
	}
	out, err := s.seriesResources(r.Context(), r.v, []*entry{e})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out[0])
}

// GET series/lookup?term= (SeriesLookupController.Search): the view's resource when it has the series, else the
// title as not added, else metadata from TVDB/TMDb, falling back to a stub with the TVDB id.
func (s *Service) seriesLookup(w http.ResponseWriter, r *request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	term, _ := r.query("term")
	prefix, val := lookupTerm(term)
	var bs []titleBrief
	var err error
	switch prefix {
	case "tvdb":
		n, perr := strconv.Atoi(val)
		if perr != nil || n <= 0 {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
		bs, err = s.briefs(ctx, `kind = 'series' AND tvdb_id = ?`, n)
		if err == nil && len(bs) == 0 {
			bs = []titleBrief{{tvdb: n}}
		}
	case "imdb":
		bs, err = s.briefs(ctx, `kind = 'series' AND imdb_id = ?`, val)
	case "tmdb":
		n, _ := strconv.Atoi(val)
		bs, err = s.briefs(ctx, `kind = 'series' AND tmdb_id = ?`, n)
	default:
		if val == "" {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
		bs, err = s.briefs(ctx, `kind = 'series' AND lower(name) LIKE ? ESCAPE '\' ORDER BY name LIMIT 20`, likeArg(val))
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if prefix == "" && len(bs) == 0 {
		if search, ok := s.tvmeta.(interface {
			SearchSeries(context.Context, string) ([]tvdb.SearchResult, error)
		}); ok {
			results, err := search.SearchSeries(ctx, val)
			if err != nil {
				s.log.Warn("series search failed", "view", r.v.Key, "err", err)
			} else {
				seen := map[int]bool{}
				for _, result := range results {
					id, err := strconv.Atoi(result.TVDBID)
					if err != nil || id <= 0 || seen[id] || result.Type != "series" {
						continue
					}
					seen[id] = true
					year, _ := strconv.Atoi(result.Year)
					bs = append(bs, titleBrief{tvdb: id, name: result.Name, year: year})
					if len(bs) == 20 {
						break
					}
				}
			}
		}
	}
	out := make([]seriesResource, 0, len(bs))
	for _, b := range bs {
		var e *entry
		if b.id > 0 {
			if e, err = s.loadOne(ctx, r.v, b.id); err != nil {
				s.internal(w, r, err)
				return
			}
		}
		if e != nil {
			rs, err := s.seriesResources(ctx, r.v, []*entry{e})
			if err != nil {
				s.internal(w, r, err)
				return
			}
			out = append(out, rs[0])
			continue
		}
		sr := seriesResource{Title: b.name, AlternateTitles: []any{}, Status: "continuing", Images: []any{},
			Seasons: []seasonResource{}, Year: b.year, SeasonFolder: true, Monitored: true, MonitorNewItems: "all",
			TvdbID: b.tvdb, SeriesType: "standard", ImdbID: b.imdb, Genres: []string{}, Tags: []int64{},
			Added: at(minTime), Ratings: map[string]any{}, LanguageProfileID: 1}
		if b.id == 0 && b.tvdb > 0 && s.tvmeta != nil {
			meta, err := s.tvmeta.LookupSeries(ctx, b.tvdb)
			switch {
			case err == nil:
				sr.Title, sr.Year, sr.Runtime = meta.Title, meta.Year, meta.Runtime
				sr.ImdbID, sr.TmdbID = meta.IMDbID, meta.TMDbID
				if meta.Status != "" {
					sr.Status = strings.ToLower(meta.Status)
					sr.Ended = sr.Status == "ended"
				}
				for _, n := range meta.Seasons {
					sr.Seasons = append(sr.Seasons, seasonResource{SeasonNumber: n, Monitored: n > 0})
				}
			default:
				// Keep the local answer when the metadata source is unavailable.
				s.log.Warn("series lookup failed", "view", r.v.Key, "tvdb", b.tvdb, "err", err)
			}
		}
		if sr.Title != "" {
			sr.TitleSlug = slug(sr.Title)
			sr.Folder = folderName(sr.Title, sr.Year)
		}
		if b.id > 0 {
			// Known to us but not in this view: its seasons, so Seerr can say which to monitor.
			eps, err := s.loadEpisodes(ctx, r.v, []*entry{{id: b.id}})
			if err != nil {
				s.internal(w, r, err)
				return
			}
			seen := map[int]bool{}
			for _, x := range eps[b.id] {
				if !seen[x.season] {
					seen[x.season] = true
					sr.Seasons = append(sr.Seasons, seasonResource{SeasonNumber: x.season})
				}
			}
		}
		out = append(out, sr)
	}
	writeJSON(w, http.StatusOK, out)
}

// GET episode?seriesId=[&seasonNumber=] or ?episodeIds= (EpisodeController.GetEpisodes).
func (s *Service) episodes(w http.ResponseWriter, r *request) {
	ctx := r.Context()
	var es []*entry
	var err error
	if sid := r.queryInt("seriesId", 0); sid > 0 {
		e, err := s.resolveEntry(ctx, r.v, int64(sid))
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if e == nil {
			notFound(w, "Series "+strconv.Itoa(sid)+" does not exist")
			return
		}
		es = []*entry{e}
	} else if ids := r.queryInts("episodeIds"); len(ids) > 0 {
		if es, err = s.entriesForEpisodes(ctx, r.v, ids); err != nil {
			s.internal(w, r, err)
			return
		}
	} else {
		badRequest(w, "", "seriesId or episodeIds must be provided", nil)
		return
	}
	eps, err := s.loadEpisodes(ctx, r.v, es)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	season, hasSeason := r.query("seasonNumber")
	want := map[int64]bool{}
	for _, id := range r.queryInts("episodeIds") {
		want[id] = true
	}
	// includeEpisodeFile (Ruddarr, Bazarr) embeds the episode's file.
	files := map[int64]*episodeFileResource{}
	if r.queryBool("includeEpisodeFile", false) {
		var tids []int64
		for _, e := range es {
			tids = append(tids, e.id)
		}
		fs, ents, err := s.filesFor(ctx, r.v, tids, nil)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		seasons, err := s.episodeSeasons(ctx, tids)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		for _, x := range fs {
			f := s.episodeFileFrom(r.v, ents[x.titleID], x, seasons)
			files[x.id] = &f
		}
	}
	out := []calendarEpisode{}
	for _, e := range es {
		for _, x := range eps[e.id] {
			if hasSeason && strconv.Itoa(x.season) != season {
				continue
			}
			if len(want) > 0 && !want[x.id] {
				continue
			}
			c := calendarEpisode{episodeResource: episodeFrom(e, x)}
			if x.fileID != 0 {
				c.EpisodeFile = files[x.fileID]
			}
			out = append(out, c)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// entriesForEpisodes loads the view's series that hold the given episodes.
func (s *Service) entriesForEpisodes(ctx context.Context, v *View, ids []int64) ([]*entry, error) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT DISTINCT title_id FROM episode WHERE id IN (`+
		store.Placeholders(len(ids))+`)`, args...)
	if err != nil {
		return nil, err
	}
	var tids []int64
	for rows.Next() {
		var t int64
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return nil, err
		}
		tids = append(tids, t)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	if len(tids) == 0 {
		return nil, nil
	}
	return s.load(ctx, v, tids)
}

// GET episode/{id}.
func (s *Service) episode(w http.ResponseWriter, r *request) {
	id, ok := r.pathID(1)
	if !ok {
		notFound(w, "Episode not found")
		return
	}
	es, err := s.entriesForEpisodes(r.Context(), r.v, []int64{id})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	eps, err := s.loadEpisodes(r.Context(), r.v, es)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for _, e := range es {
		for _, x := range eps[e.id] {
			if x.id == id {
				writeJSON(w, http.StatusOK, episodeFrom(e, x))
				return
			}
		}
	}
	notFound(w, "Episode with ID "+strconv.FormatInt(id, 10)+" does not exist")
}

func selectedEpisodeSQL(column string, selected [][]int64) string {
	if len(selected) == 0 {
		return ""
	}
	return " AND " + column + " IN (SELECT value FROM json_each(?))"
}

func selectedEpisodeArgs(args []any, selected [][]int64) []any {
	if len(selected) == 0 {
		return args
	}
	return append(args, jsonIDs(selected[0]))
}
