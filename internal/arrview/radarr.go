package arrview

import (
	"context"
	"encoding/json"
	"github.com/theatarr/theatarr/internal/availability"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// movieResource is Radarr's MovieResource (src/Radarr.Api.V3/Movies/MovieResource.cs @ a96bf7c). Fields the views
// have no data for are left out when the arr's type is nullable, and written empty when it is not.
//
// Read by: Seerr (scanners/radarr/index.ts: id, tmdbId, titleSlug, title, hasFile, monitored; availabilitySync.ts
// L664-790: tmdbId, hasFile, movieFile.mediaInfo.resolution; radarr.ts L118-216: lookup id, hasFile, monitored,
// isAvailable, tags), Homepage (widgets/radarr/widget.js L10-22: monitored, hasFile, isAvailable, title, id),
// decypharr (pkg/arr/content.go, library.go: id, tmdbId, path, movieFile).
type movieResource struct {
	ID                    int64              `json:"id,omitempty"`
	Title                 string             `json:"title"`
	OriginalTitle         string             `json:"originalTitle,omitempty"`
	AlternateTitles       []any              `json:"alternateTitles"`
	SecondaryYear         *int               `json:"secondaryYear,omitempty"`
	SecondaryYearSourceID int                `json:"secondaryYearSourceId"`
	SortTitle             string             `json:"sortTitle,omitempty"`
	SizeOnDisk            *int64             `json:"sizeOnDisk,omitempty"`
	Status                string             `json:"status"`
	InCinemas             *arrTime           `json:"inCinemas,omitempty"`
	PhysicalRelease       *arrTime           `json:"physicalRelease,omitempty"`
	DigitalRelease        *arrTime           `json:"digitalRelease,omitempty"`
	ReleaseDate           *arrTime           `json:"releaseDate,omitempty"`
	Images                []any              `json:"images"`
	RemotePoster          string             `json:"remotePoster,omitempty"`
	Overview              string             `json:"overview,omitempty"`
	Year                  int                `json:"year"`
	Path                  string             `json:"path,omitempty"`
	QualityProfileID      int64              `json:"qualityProfileId"`
	HasFile               *bool              `json:"hasFile,omitempty"`
	MovieFileID           int64              `json:"movieFileId"`
	Monitored             bool               `json:"monitored"`
	MinimumAvailability   string             `json:"minimumAvailability"`
	IsAvailable           bool               `json:"isAvailable"`
	FolderName            string             `json:"folderName,omitempty"`
	Runtime               int                `json:"runtime"`
	ImdbID                string             `json:"imdbId,omitempty"`
	TmdbID                int                `json:"tmdbId"`
	TitleSlug             string             `json:"titleSlug,omitempty"`
	RootFolderPath        string             `json:"rootFolderPath,omitempty"`
	Folder                string             `json:"folder,omitempty"`
	Genres                []string           `json:"genres"`
	Keywords              []string           `json:"keywords"`
	Tags                  []int64            `json:"tags"`
	Added                 arrTime            `json:"added"`
	Ratings               map[string]any     `json:"ratings"`
	MovieFile             *movieFileResource `json:"movieFile,omitempty"`
	Popularity            float64            `json:"popularity"`
	Statistics            *movieStatistics   `json:"statistics,omitempty"`
}

// movieStatistics is MovieStatisticsResource. Only the view's versions count (docs/design/arrview.md).
type movieStatistics struct {
	MovieFileCount     int       `json:"movieFileCount"`
	SizeOnDisk         int64     `json:"sizeOnDisk"`
	ReleaseGroups      []string  `json:"releaseGroups"`
	MovieFileQualities []quality `json:"movieFileQualities,omitempty"`
}

// movieStatus ports SkyHookProxy.MapMovie's status (src/NzbDrone.Core/MetadataSource/SkyHook/SkyHookProxy.cs
// L292-314 @ a96bf7c) from the stored release dates.
func movieStatus(d map[string]time.Time, now time.Time) string {
	status := "announced"
	if c, ok := d["theatrical"]; ok && now.After(c) {
		status = "inCinemas"
		_, ph := d["physical"]
		_, dg := d["digital"]
		if !ph && !dg && now.After(c.AddDate(0, 0, 90)) {
			status = "released"
		}
	}
	if p, ok := d["physical"]; ok && !now.Before(p) {
		status = "released"
	}
	if g, ok := d["digital"]; ok && !now.Before(g) {
		status = "released"
	}
	return status
}

// releaseDate ports Movie.GetReleaseDate.
func releaseDate(minAvail string, d map[string]time.Time) *arrTime {
	pick := func(types ...string) *arrTime {
		var best time.Time
		for _, k := range types {
			if t, ok := d[k]; ok && (best.IsZero() || t.Before(best)) {
				best = t
			}
		}
		return atPtr(best)
	}
	switch {
	case minAvail == "tba" || minAvail == "announced":
		return pick("theatrical", "digital", "physical")
	case minAvail == "inCinemas" && !d["theatrical"].IsZero():
		return pick("theatrical")
	}
	if r := pick("digital", "physical"); r != nil {
		return r
	}
	if c, ok := d["theatrical"]; ok {
		return atPtr(c.AddDate(0, 0, 90))
	}
	return nil
}

// minAvail is the minimum availability the view shows for a movie: the earliest among the libraries it shows for
// the title, each by its own rule (availability.Rules).
func (s *Service) minAvail(e *entry) string {
	values := make([]string, len(e.versions))
	for i, x := range e.versions {
		values[i] = x.rule.Minimum
	}
	return availability.Earliest(values...)
}

// isAvailable: any library the view shows for the movie is available under its own minimum and delay, over the
// canonical dates.
func isAvailable(e *entry, now time.Time) bool {
	for _, x := range e.versions {
		if availability.Available(x.rule.Minimum, e.dates, now, x.rule.Delay) {
			return true
		}
	}
	return false
}

// qualityProfileID is the profile the client set, else the view profile whose versions match the title's best.
func qualityProfileID(v *View, e *entry) int64 {
	if e.rec != nil && e.rec.qpid > 0 && v.profileByID(e.rec.qpid) != nil {
		return e.rec.qpid
	}
	have := map[string]bool{}
	for _, x := range e.versions {
		have[x.key] = true
	}
	var best *Profile
	bestScore := -1
	for _, p := range v.Profiles {
		score := 0
		for _, k := range p.Versions {
			if have[k] {
				score += 2
			} else {
				score--
			}
		}
		if len(p.Versions) == len(have) {
			score++
		}
		if score > bestScore {
			best, bestScore = p, score
		}
	}
	if best == nil {
		return 0
	}
	return best.ID
}

// rootAndPath give the title's root folder and folder: the client's root, else the primary version's library. The
// folder is the one a file of the title already has under that root, else computed from its name and year.
func (s *Service) rootAndPath(v *View, e *entry) (root, p string) {
	if e.rec != nil {
		root = e.rec.root
	}
	if root == "" {
		if pv := e.primary(); pv != nil {
			if vp := s.pol.Versions[pv.key]; vp != nil {
				root = vp.Library
			}
		}
	}
	if root == "" {
		return "", ""
	}
	clean := path.Clean(root)
	for _, f := range e.paths {
		for dir := path.Dir(f); dir != "/" && dir != "."; dir = path.Dir(dir) {
			if path.Dir(dir) == clean {
				return root, dir
			}
		}
	}
	return root, path.Join(root, folderName(e.name, e.year))
}

func folderName(name string, year int) string {
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) {
			return -1
		}
		return r
	}, name)
	if year > 0 {
		return name + " (" + strconv.Itoa(year) + ")"
	}
	return name
}

func (s *Service) added(e *entry) time.Time {
	if e.rec != nil && e.rec.added > 0 {
		return millis(e.rec.added)
	}
	var first int64
	for _, x := range e.versions {
		if first == 0 || x.created < first {
			first = x.created
		}
	}
	if first == 0 {
		return minTime
	}
	return millis(first)
}

func (s *Service) movieResource(v *View, e *entry) movieResource {
	now := s.now()
	minAvail := s.minAvail(e)
	root, p := s.rootAndPath(v, e)
	status := movieStatus(e.dates, now)
	if e.hasFile() {
		status = "released" // a file is proof of a release, whatever the dates say
	}
	m := movieResource{
		ID: e.viewID(), Title: e.name, OriginalTitle: e.name, AlternateTitles: []any{}, SortTitle: sortTitle(e),
		Status: status, Images: []any{}, Year: e.year, Path: p, QualityProfileID: qualityProfileID(v, e),
		Monitored: e.monitored(), MinimumAvailability: minAvail, IsAvailable: isAvailable(e, now),
		FolderName: p, ImdbID: e.imdb, TmdbID: e.tmdb, TitleSlug: strconv.Itoa(e.tmdb), RootFolderPath: root,
		Genres: []string{}, Keywords: []string{}, Tags: tagsOrEmpty(e.tags), Added: at(s.added(e)),
		Ratings: map[string]any{}, ReleaseDate: releaseDate(minAvail, e.dates),
	}
	if e.secondaryYear > 0 {
		y := e.secondaryYear
		m.SecondaryYear = &y
	}
	if d, ok := e.dates["theatrical"]; ok {
		m.InCinemas = atPtr(d)
	}
	if d, ok := e.dates["physical"]; ok {
		m.PhysicalRelease = atPtr(d)
	}
	if d, ok := e.dates["digital"]; ok {
		m.DigitalRelease = atPtr(d)
	}
	st := &movieStatistics{ReleaseGroups: []string{}}
	seen := map[int64]bool{}
	for _, x := range e.versions {
		if f := x.file; f != nil && !seen[f.id] {
			seen[f.id] = true
			st.MovieFileCount++
			st.SizeOnDisk += f.size
			if f.group != "" {
				st.ReleaseGroups = append(st.ReleaseGroups, f.group)
			}
			st.MovieFileQualities = append(st.MovieFileQualities, qualityFor(Radarr, f.quality))
		}
	}
	has := e.hasFile()
	m.HasFile = &has
	size := st.SizeOnDisk
	m.SizeOnDisk = &size
	m.Statistics = st
	if _, f := e.file(); f != nil {
		m.MovieFileID = f.id
		m.MovieFile = movieFileFrom(e, f, p)
	}
	return m
}

func sortTitle(e *entry) string {
	if e.sortName != "" {
		return e.sortName
	}
	return strings.ToLower(e.name)
}

func tagsOrEmpty(t []int64) []int64 {
	if t == nil {
		return []int64{}
	}
	return t
}

func movieFileFrom(e *entry, f *vfile, moviePath string) *movieFileResource {
	r := &movieFileResource{ID: f.id, MovieID: e.viewID(), RelativePath: f.relPath, Path: f.path, Size: f.size,
		DateAdded: at(minTime), SceneName: f.scene, ReleaseGroup: f.group, Edition: f.edition,
		Languages: languagesFrom(Radarr, f.languages), Quality: qualityModelFor(Radarr, f.quality, f.revision),
		MediaInfo: mediaInfoWithResolution(f.mediaInfo, f.quality), OriginalFilePath: f.origPath,
		QualityCutoffNotMet: f.cutoffNotMet}
	r.Quality.Revision.Real, r.Quality.Revision.IsRepack = f.real, f.isRepack
	if r.Path == "" && f.relPath != "" && moviePath != "" {
		r.Path = path.Join(moviePath, f.relPath)
	}
	if f.added.Valid {
		r.DateAdded = at(millis(f.added.Int64))
	}
	if f.cfScore.Valid {
		n := int(f.cfScore.Int64)
		r.CustomFormatScore = &n
		r.CustomFormats = []any{}
	}
	flags := f.flags
	r.IndexerFlags = &flags
	return r
}

// mediaInfoWithResolution preserves a file's probed resolution, or supplies the parsed quality resolution when none
// exists. Seerr uses this field to distinguish 4K from non-4K services.
func mediaInfoWithResolution(raw json.RawMessage, qualityName string) json.RawMessage {
	resolution := qualityResolutionText(qualityFor(Radarr, qualityName).Resolution)
	if len(raw) == 0 && resolution == "" {
		return nil
	}
	info := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &info); err != nil {
			return raw
		}
		if info == nil {
			info = map[string]json.RawMessage{}
		}
	}
	changed := len(raw) == 0
	// These non-nullable values are written by the arr serializer and required by Ruddarr's file model.
	for key, value := range map[string]any{
		"audioBitrate": 0, "audioChannels": float64(0), "audioStreamCount": 0,
		"videoBitDepth": 0, "videoBitrate": 0, "videoFps": float64(0),
	} {
		if current, ok := info[key]; !ok || strings.TrimSpace(string(current)) == "null" {
			info[key], _ = json.Marshal(value)
			changed = true
		}
	}
	if resolution != "" {
		var existing string
		value, ok := info["resolution"]
		if !ok || json.Unmarshal(value, &existing) != nil || existing == "" {
			info["resolution"], _ = json.Marshal(resolution)
			changed = true
		}
	}
	if !changed {
		return raw
	}
	out, err := json.Marshal(info)
	if err != nil {
		return raw
	}
	return out
}

func qualityResolutionText(resolution int) string {
	switch resolution {
	case 2160:
		return "3840x2160"
	case 1080:
		return "1920x1080"
	case 720:
		return "1280x720"
	case 576:
		return "720x576"
	case 480:
		return "720x480"
	default:
		return ""
	}
}

// GET movie[?tmdbId=] (MovieController.AllMovie): every movie of the view, or the one with that TMDb id (a list
// either way, empty when none).
func (s *Service) movies(w http.ResponseWriter, r *request) {
	ctx := r.Context()
	var ids []int64
	if t, ok := r.query("tmdbId"); ok {
		n, _ := strconv.ParseInt(t, 10, 64)
		tid, err := s.titleByExternal(ctx, r.v, n, 0)
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
	out := make([]movieResource, 0, len(es))
	for _, e := range es {
		out = append(out, s.movieResource(r.v, e))
	}
	writeJSON(w, http.StatusOK, out)
}

// GET movie/{id}: our id or a replaced instance's id (alias). 404 when the view does not have the title, which Seerr
// reads as "removed" (availabilitySync.ts L664-790).
func (s *Service) movie(w http.ResponseWriter, r *request) {
	id, ok := r.pathID(1)
	if !ok {
		notFound(w, "Movie not found")
		return
	}
	e, err := s.resolveEntry(r.Context(), r.v, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if e == nil {
		notFound(w, "Movie with ID "+strconv.FormatInt(id, 10)+" does not exist")
		return
	}
	writeJSON(w, http.StatusOK, s.movieResource(r.v, e))
}

// lookupMovie answers one lookup result: the view's own resource when it has the title (id, hasFile and monitored
// are the view's: docs/design/arrview.md "Lookups"), else the title as a not-added movie (no id), else a stub with the TMDb id
// only, since the views fetch no metadata.
func (s *Service) lookupMovie(ctx context.Context, v *View, b titleBrief) (movieResource, error) {
	if b.id > 0 {
		e, err := s.loadOne(ctx, v, b.id)
		if err != nil {
			return movieResource{}, err
		}
		if e != nil {
			return s.movieResource(v, e), nil
		}
	}
	m := movieResource{Title: b.name, OriginalTitle: b.name, AlternateTitles: []any{}, Status: "announced",
		Images: []any{}, Year: b.year, Monitored: true, MinimumAvailability: "released", ImdbID: b.imdb, TmdbID: b.tmdb,
		Genres: []string{}, Keywords: []string{}, Tags: []int64{}, Added: at(minTime), Ratings: map[string]any{}}
	if b.tmdb > 0 {
		m.TitleSlug = strconv.Itoa(b.tmdb)
	}
	if b.name != "" {
		m.Folder = folderName(b.name, b.year)
	}
	if b.secondaryYear > 0 {
		y := b.secondaryYear
		m.SecondaryYear = &y
	}
	return m, nil
}

// lookupTerm parses a lookup term the way Radarr's SearchForNewMovie and Sonarr's SearchForNewSeries do:
// "tmdb:N", "imdb:tt…" / a bare "tt…", "tvdb:N", or free text.
func lookupTerm(term string) (prefix, value string) {
	t := strings.TrimSpace(term)
	low := strings.ToLower(t)
	for _, p := range []string{"tmdb:", "tmdbid:", "imdb:", "tvdb:", "tvdbid:"} {
		if strings.HasPrefix(low, p) {
			return strings.TrimSuffix(strings.TrimSuffix(p, ":"), "id"), strings.TrimSpace(t[len(p):])
		}
	}
	if strings.HasPrefix(low, "tt") {
		if _, err := strconv.Atoi(low[2:]); err == nil {
			return "imdb", low
		}
	}
	return "", t
}

func likeArg(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(strings.ToLower(s)) + "%"
}

// GET movie/lookup?term= (MovieLookupController.Search).
func (s *Service) movieLookup(w http.ResponseWriter, r *request) {
	ctx := r.Context()
	term, _ := r.query("term")
	prefix, val := lookupTerm(term)
	var bs []titleBrief
	var err error
	switch prefix {
	case "tmdb":
		n, perr := strconv.Atoi(val)
		if perr != nil || n <= 0 {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
		bs, err = s.briefs(ctx, `kind = 'movie' AND tmdb_id = ?`, n)
		if err == nil && len(bs) == 0 {
			bs = []titleBrief{{tmdb: n}}
		}
	case "imdb":
		bs, err = s.briefs(ctx, `kind = 'movie' AND imdb_id = ?`, val)
	case "":
		if val == "" {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
		bs, err = s.briefs(ctx, `kind = 'movie' AND lower(name) LIKE ? ESCAPE '\' ORDER BY name LIMIT 20`, likeArg(val))
	default:
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := make([]movieResource, 0, len(bs))
	seen := map[int]bool{}
	for _, b := range bs {
		m, err := s.lookupMovie(ctx, r.v, b)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		seen[m.TmdbID] = true
		out = append(out, m)
	}
	if prefix == "" {
		// Movies the catalog lacks: TMDb's title search, as SkyHook's SearchForNewMovie answers Radarr.
		more, err := s.searchTMDbMovies(ctx, r.v, val, seen, lookupLimit-len(out))
		if err != nil {
			s.internal(w, r, err)
			return
		}
		out = append(out, more...)
	}
	writeJSON(w, http.StatusOK, out)
}

// lookupLimit caps a lookup by name: the catalog's matches and TMDb's first page together (as series lookups).
const lookupLimit = 20

// tmdbImage is the URL of a TMDb image path at its original size, as SkyHook gives Radarr its remote covers.
func tmdbImage(path string) string {
	if path == "" {
		return ""
	}
	return "https://image.tmdb.org/t/p/original" + path
}

// trailingYear splits a lookup term like Radarr's SearchForNewMovie does with the parser's year: "Akira 1988" or
// "Akira (1988)" is the title "Akira" in 1988. Years after next year are part of the title ("Blade Runner 2049").
func trailingYear(term string, now time.Time) (string, int) {
	m := trailingYearRe.FindStringSubmatch(strings.TrimSpace(term))
	if m == nil {
		return term, 0
	}
	y, _ := strconv.Atoi(m[2])
	if y < 1880 || y > now.Year()+1 {
		return term, 0
	}
	return strings.TrimSpace(m[1]), y
}

var trailingYearRe = regexp.MustCompile(`^(.*\S)[\s.]+[(\[]?(\d{4})[)\]]?$`)

// searchTMDbMovies is the TMDb title search of a lookup by name for movies not already answered (seen, by TMDb
// id): our title when the catalog has the TMDb id under another name, else an un-added movie (no id) with TMDb's
// title, year, overview, images and rating. A trailing year narrows the search; when that finds nothing the whole
// term is searched. No TMDb client, or a failed search: nothing (the catalog's answer stands), logged.
func (s *Service) searchTMDbMovies(ctx context.Context, v *View, term string, seen map[int]bool,
	limit int) ([]movieResource, error) {
	if s.tmdb == nil || limit <= 0 {
		return nil, nil
	}
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	title, year := trailingYear(term, s.now())
	results, err := s.tmdb.SearchMovie(sctx, title, year)
	if err == nil && len(results) == 0 && year > 0 {
		results, err = s.tmdb.SearchMovie(sctx, term, 0)
	}
	if err != nil {
		s.log.Warn("movie search failed", "view", v.Key, "err", err)
		return nil, nil
	}
	var out []movieResource
	for _, x := range results {
		if x.ID <= 0 || seen[x.ID] {
			continue
		}
		seen[x.ID] = true
		bs, err := s.briefs(ctx, `kind = 'movie' AND tmdb_id = ?`, x.ID)
		if err != nil {
			return nil, err
		}
		var m movieResource
		if len(bs) > 0 {
			if m, err = s.lookupMovie(ctx, v, bs[0]); err != nil {
				return nil, err
			}
		} else {
			if m, err = s.lookupMovie(ctx, v, titleBrief{tmdb: x.ID, name: x.Title, year: x.Year()}); err != nil {
				return nil, err
			}
			if x.OriginalTitle != "" {
				m.OriginalTitle = x.OriginalTitle
			}
			m.Overview, m.Popularity = x.Overview, x.Popularity
			m.RemotePoster = tmdbImage(x.PosterPath)
			for _, im := range []struct{ kind, path string }{{"poster", x.PosterPath}, {"fanart", x.BackdropPath}} {
				if u := tmdbImage(im.path); u != "" {
					m.Images = append(m.Images, map[string]any{"coverType": im.kind, "url": u, "remoteUrl": u})
				}
			}
			if x.VoteCount > 0 {
				m.Ratings = map[string]any{"tmdb": map[string]any{"votes": x.VoteCount, "value": x.VoteAverage,
					"type": "user"}}
			}
		}
		out = append(out, m)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// GET movie/lookup/tmdb?tmdbId= (MovieLookupController.SearchByTmdbId): one object.
func (s *Service) movieLookupTmdb(w http.ResponseWriter, r *request) {
	n := r.queryInt("tmdbId", 0)
	if n <= 0 {
		notFound(w, "Movie not found")
		return
	}
	bs, err := s.briefs(r.Context(), `kind = 'movie' AND tmdb_id = ?`, n)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	b := titleBrief{tmdb: n}
	if len(bs) > 0 {
		b = bs[0]
	}
	m, err := s.lookupMovie(r.Context(), r.v, b)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}
