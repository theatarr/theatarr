package arrview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/theatarr/theatarr/internal/availability"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/notify"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// enumText is an arr enum in a request: the camelCase name, or its number (JsonStringEnumConverter with integer
// values allowed).
type enumText string

func (e *enumText) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*e = enumText(s)
		return nil
	}
	var n int
	if json.Unmarshal(b, &n) == nil {
		*e = enumText("#" + strconv.Itoa(n))
		return nil
	}
	return nil
}

func (e enumText) value(names ...string) string {
	s := string(e)
	if strings.HasPrefix(s, "#") {
		n, _ := strconv.Atoi(s[1:])
		if n >= 0 && n < len(names) {
			return names[n]
		}
		return ""
	}
	for _, n := range names {
		if strings.EqualFold(n, s) {
			return n
		}
	}
	return ""
}

func (e enumText) minAvail() string {
	if strings.HasPrefix(string(e), "#") {
		// MovieStatusType: TBA = 0, Announced = 1, InCinemas = 2, Released = 3 (Deleted = -1).
		return e.value("tba", "announced", "inCinemas", "released")
	}
	return e.value("tba", "announced", "inCinemas", "released", "deleted")
}

// titleIn is what POST and PUT movie|series read: the fields Seerr, the phone apps and the arrs' own UI send
// (arr-api-surface: POST bodies; PUT is the whole object). Anything else in the body is ignored.
type titleIn struct {
	ID                  int64    `json:"id"`
	Title               string   `json:"title"`
	Year                int      `json:"year"`
	TmdbID              int64    `json:"tmdbId"`
	TvdbID              int64    `json:"tvdbId"`
	ImdbID              string   `json:"imdbId"`
	QualityProfileID    int64    `json:"qualityProfileId"`
	ProfileID           int64    `json:"profileId"`
	RootFolderPath      string   `json:"rootFolderPath"`
	Path                string   `json:"path"`
	MinimumAvailability enumText `json:"minimumAvailability"`
	Monitored           *bool    `json:"monitored"`
	Tags                []int64  `json:"tags"`
	SeasonFolder        *bool    `json:"seasonFolder"`
	SeriesType          enumText `json:"seriesType"`
	MonitorNewItems     enumText `json:"monitorNewItems"`
	Status              string   `json:"status"`
	Seasons             []struct {
		SeasonNumber int  `json:"seasonNumber"`
		Monitored    bool `json:"monitored"`
	} `json:"seasons"`
	AddOptions *struct {
		SearchForMovie           bool     `json:"searchForMovie"`
		SearchForMissingEpisodes bool     `json:"searchForMissingEpisodes"`
		Monitor                  enumText `json:"monitor"`
	} `json:"addOptions"`
}

func (in *titleIn) profile() int64 {
	if in.QualityProfileID > 0 {
		return in.QualityProfileID
	}
	return in.ProfileID
}

func (in *titleIn) monitored(def bool) bool {
	if in.Monitored != nil {
		return *in.Monitored
	}
	return def
}

// seriesTypes are Sonarr's SeriesTypes.
var seriesTypes = []string{"standard", "daily", "anime"}

var errNoVersion = errors.New("no version of this view takes this title")

// animeHint reads what a request says about anime: Sonarr's series type, a root folder that is an anime version's
// library, or a profile of anime versions only. nil when it says nothing.
func (s *Service) animeHint(v *View, in *titleIn) *bool {
	t, f := true, false
	if v.Flavour == Sonarr {
		switch in.SeriesType.value(seriesTypes...) {
		case "anime":
			return &t
		}
	}
	if in.RootFolderPath != "" {
		root := strings.TrimSuffix(in.RootFolderPath, "/")
		for _, k := range v.Versions {
			if vp := s.pol.Versions[k]; vp != nil && vp.Library == root && vp.Anime != nil {
				if *vp.Anime {
					return &t
				}
				return &f
			}
		}
	}
	if p := v.profileByID(in.profile()); p != nil {
		all, any := true, false
		for _, k := range p.Versions {
			if vp := s.pol.Versions[k]; vp != nil && vp.Anime != nil && *vp.Anime {
				any = true
			} else {
				all = false
			}
		}
		if all && any {
			return &t
		}
	}
	return nil
}

func (s *Service) animeOK(key string, anime bool) bool {
	vp := s.pol.Versions[key]
	return vp != nil && (vp.Anime == nil || *vp.Anime == anime)
}

// chooseVersions is the versions an add or a profile change turns on (docs/design/arrview.md): the
// chosen profile's versions that fit the title (anime or not; a view with one profile, like every per-version view,
// always uses it). When none of the profile's versions fits: under RulesDefault (let Theatarr's rules decide) or with no
// profile given, the view's versions enabled by default that fit (so "Rules default" of non-anime versions puts an
// anime title in the view's anime versions); a profile picked on purpose ("WEB only") is honoured, all its versions.
// With no known profile: the versions whose library is the given root folder, else the view's versions enabled by
// default that fit.
func (s *Service) chooseVersions(v *View, in *titleIn, anime bool) ([]string, error) {
	var out []string
	p := v.profileByID(in.profile())
	rules := p == nil || p.Name == RulesDefault
	if p == nil && len(v.Profiles) == 1 {
		// A view with one profile (every per-version view) has nothing else to choose.
		p = v.Profiles[0]
	}
	if p != nil {
		for _, k := range p.Versions {
			if s.animeOK(k, anime) {
				out = append(out, k)
			}
		}
		if len(out) == 0 && rules {
			out = s.defaultVersions(v, anime)
		}
		if len(out) == 0 {
			out = append(out, p.Versions...)
		}
		return out, nil
	}
	if in.RootFolderPath != "" {
		root := strings.TrimSuffix(in.RootFolderPath, "/")
		for _, k := range v.Versions {
			if vp := s.pol.Versions[k]; vp != nil && vp.Library == root {
				out = append(out, k)
			}
		}
		if len(out) > 0 {
			return out, nil
		}
	}
	if out = s.defaultVersions(v, anime); len(out) == 0 {
		return nil, errNoVersion
	}
	return out, nil
}

// defaultVersions is the view's versions enabled by default that fit the title.
func (s *Service) defaultVersions(v *View, anime bool) []string {
	var out []string
	for _, k := range v.Versions {
		if vp := s.pol.Versions[k]; vp != nil && vp.Enable != "request" && s.animeOK(k, anime) {
			out = append(out, k)
		}
	}
	return out
}

// Where an add's anime decision came from (arrview_title.anime_source, migration 00072).
const (
	animeFromRequest = "request" // the series type, root folder or profile
	animeFromMapping = "mapping" // Theatarr's anime mapping data
	animeFromTMDb    = "tmdb"    // TMDb: Japanese-language animation, or not
	animeFromNone    = "none"    // no data: not anime
	// TMDb could not be asked (an error other than not found; migration 00103): not anime for now, asked again at
	// the title's next add, PUT or refresh (reclassify).
	animeFromUnknown = "unknown"
)

// decideAnime is Theatarr's own anime decision for a title an add creates when the request says nothing about anime:
// the anime mapping data first (an entry of a franchise naming the TMDb movie, or a mapping into the TVDB series),
// then TMDb's Japanese-language animation (tmdb.LooksAnime over the movie's or series' details; a series is found by
// the request's TMDb id, else by its TVDB id). No data, no TMDb client or TMDb has no such title: not anime
// ("none"). Any other TMDb error: not anime for now ("unknown").
func (s *Service) decideAnime(ctx context.Context, v *View, in *titleIn, ext int64) (bool, string) {
	q := `SELECT EXISTS (SELECT 1 FROM anime_entry e, json_each(e.tmdb_movie) j WHERE j.value = ?)`
	if v.Flavour == Sonarr {
		q = `SELECT EXISTS (SELECT 1 FROM anime_mapping WHERE tvdb_series = ? AND status <> 'rejected')`
	}
	var mapped bool
	if err := s.st.Reader().QueryRowContext(ctx, q, ext).Scan(&mapped); err != nil {
		s.log.Warn("anime mapping lookup failed", "view", v.Key, "id", ext, "err", err)
	} else if mapped {
		return true, animeFromMapping
	}
	if s.tmdb == nil {
		return false, animeFromNone
	}
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var anime bool
	var err error
	if v.Flavour == Sonarr {
		id := int(in.TmdbID)
		if id <= 0 {
			id, err = s.tmdb.FindTVByTVDB(tctx, int(ext))
		}
		if err == nil {
			var show *tmdb.TVSeries
			if show, err = s.tmdb.TVShow(tctx, id); err == nil {
				anime = show.LooksAnime()
			}
		}
	} else {
		var m *tmdb.Movie
		if m, err = s.tmdb.Movie(tctx, int(ext)); err == nil {
			anime = m.LooksAnime()
		}
	}
	if errors.Is(err, tmdb.ErrNotFound) {
		return false, animeFromNone
	}
	if err != nil {
		s.log.Warn("anime check on TMDb failed; asked again at the next add, PUT or refresh", "view", v.Key,
			"id", ext, "err", err)
		return false, animeFromUnknown
	}
	return anime, animeFromTMDb
}

// reclassify asks again about a title whose anime check could not reach TMDb (anime_source unknown in this view's
// record). Once TMDb answers, every unknown record of the title takes the answer; an anime answer flags
// the title anime and turns on the view's versions for it (chooseVersions with the record's profile and root),
// monitored as the title is. The view's other versions stay.
func (s *Service) reclassify(ctx context.Context, v *View, titleID int64) error {
	var src, root string
	var qp, tmdbID, tvdbID int64
	err := s.st.Reader().QueryRowContext(ctx, `SELECT a.anime_source, a.quality_profile_id, a.root_folder_path,
		COALESCE(t.tmdb_id, 0), COALESCE(t.tvdb_id, 0) FROM arrview_title a JOIN title t ON t.id = a.title_id
		WHERE a.view = ? AND a.title_id = ?`, v.Key, titleID).Scan(&src, &qp, &root, &tmdbID, &tvdbID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && src != animeFromUnknown {
		return nil
	}
	if err != nil {
		return err
	}
	in := titleIn{TmdbID: tmdbID, TvdbID: tvdbID, QualityProfileID: qp, RootFolderPath: root}
	ext := tmdbID
	if v.Flavour == Sonarr {
		ext = tvdbID
	}
	anime, source := s.decideAnime(ctx, v, &in, ext)
	if source == animeFromUnknown {
		return nil
	}
	return s.st.WriteTx(ctx, func(tx *sqlTx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE arrview_title SET anime_source = ? WHERE title_id = ?
			AND anime_source = ?`, source, titleID, animeFromUnknown); err != nil {
			return err
		}
		s.log.Info("anime decided", "view", v.Key, "title", titleID, "anime", anime, "source", source, "retry", true)
		if !anime {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE title SET anime = 1 WHERE id = ?`, titleID); err != nil {
			return err
		}
		keys, err := s.chooseVersions(v, &in, true)
		if err != nil {
			return err
		}
		var mon bool
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(monitored), 1) FROM version WHERE title_id = ?`,
			titleID).Scan(&mon); err != nil {
			return err
		}
		created, err := s.turnOn(ctx, tx, v, titleID, keys, mon)
		if err != nil {
			return err
		}
		if v.Flavour == Sonarr {
			if err := s.copySeasonIntent(ctx, tx, v, titleID, created); err != nil {
				return err
			}
		}
		return syncTitleMonitored(ctx, tx, titleID, store.Millis(s.now()))
	})
}

// copySeasonIntent gives the series versions with these keys (just created by reclassify) the season monitoring
// the original request gave the view's other version of the title: its series_monitor intent and its episodes'
// monitored flags. Nothing when the view has no other version of the title.
func (s *Service) copySeasonIntent(ctx context.Context, tx *sqlTx, v *View, titleID int64, created []string) error {
	if len(created) == 0 {
		return nil
	}
	var src int64
	for _, k := range v.Versions {
		if slices.Contains(created, k) {
			continue
		}
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT v.id FROM version v WHERE v.title_id = ? AND v.policy_key = ?
			AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id)`, titleID, k).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		src = id
		break
	}
	if src == 0 {
		return nil
	}
	m, err := catalog.LoadMonitor(ctx, tx, src)
	if err != nil {
		return err
	}
	now := s.now()
	for _, k := range created {
		var id int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM version WHERE title_id = ? AND policy_key = ?`, titleID,
			k).Scan(&id); err != nil {
			return err
		}
		if err := catalog.SaveMonitor(ctx, tx, id, m, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE episode_version_state SET monitored = s.monitored, updated_at = ?
			FROM episode_version_state s WHERE s.version_id = ? AND s.episode_id = episode_version_state.episode_id
			AND episode_version_state.version_id = ?`, store.Millis(now), src, id); err != nil {
			return err
		}
	}
	return nil
}

// holds reports whether the catalog has the title an add names (a movie by TMDb id; a series by TVDB id, else by
// the request's TMDb id), as catalog.Add finds it.
func (s *Service) holds(ctx context.Context, v *View, in *titleIn, ext int64) (bool, error) {
	q, args := `SELECT EXISTS (SELECT 1 FROM title WHERE kind = 'movie' AND tmdb_id = ?)`, []any{ext}
	if v.Flavour == Sonarr {
		q, args = `SELECT EXISTS (SELECT 1 FROM title WHERE kind = 'series' AND (tvdb_id = ? OR tmdb_id = ?))`,
			[]any{ext, in.TmdbID}
	}
	var ok bool
	err := s.st.Reader().QueryRowContext(ctx, q, args...).Scan(&ok)
	return ok, err
}

// heldSeriesType is the series type a held series has once this add is stored (docs/design/series.md "Series type"):
// its own, or the request's when it has none stored (catalog.AddSeriesType then stores it). "" for a series
// Theatarr does not hold.
func (s *Service) heldSeriesType(ctx context.Context, in *titleIn, ext int64) (string, error) {
	var stored, effective string
	err := s.st.Reader().QueryRowContext(ctx, `SELECT COALESCE(t.series_type, ''), `+store.SeriesTypeSQL("t")+`
		FROM title t WHERE t.kind = 'series' AND (t.tvdb_id = ? OR t.tmdb_id = ?) ORDER BY t.tvdb_id = ? DESC LIMIT 1`,
		ext, in.TmdbID, ext).Scan(&stored, &effective)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if typ := in.SeriesType.value(seriesTypes...); stored == "" && typ != "" {
		return typ, err
	}
	return effective, err
}

// turnOn makes sure the title has each version, monitored as asked: a new version starts wanted; a removed one is
// restored (catalog.TurnOn). It returns the keys of the versions it created.
func (s *Service) turnOn(ctx context.Context, tx *sqlTx, v *View, titleID int64, keys []string,
	monitored bool) ([]string, error) {
	created, err := catalog.TurnOn(ctx, tx, titleID, keys, monitored, s.now())
	for _, k := range created {
		s.log.Info("version turned on", "view", v.Key, "title", titleID, "version", k)
	}
	return created, err
}

// offVersions is which of the keys the title has no active version for: none yet, removed from the views, or
// unmonitored.
func offVersions(ctx context.Context, tx *sqlTx, titleID int64, keys []string) ([]string, error) {
	var out []string
	for _, k := range keys {
		var on bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM version v WHERE v.title_id = ? AND
			v.policy_key = ? AND v.monitored AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r
			WHERE r.version_id = v.id))`, titleID, k).Scan(&on)
		if err != nil {
			return nil, err
		}
		if !on {
			out = append(out, k)
		}
	}
	return out, nil
}

// activeVersions is which versions the movie with this TMDb id has on: monitored and not removed from the views.
func activeVersions(ctx context.Context, tx interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}, tmdbID int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT v.policy_key FROM version v JOIN title t ON t.id = v.title_id
		WHERE t.kind = 'movie' AND t.tmdb_id = ? AND v.monitored AND NOT EXISTS (SELECT 1 FROM
		arrview_removed_version r WHERE r.version_id = v.id)`, tmdbID)
	if err != nil {
		return nil, err
	}
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, k)
	}
	return out, closeRows(rows)
}

// viewVersionIDs lists the title's versions in the view that are not removed.
func viewVersionIDs(ctx context.Context, tx *sqlTx, v *View, titleID int64) ([]int64, error) {
	where, args := scope(v, []int64{titleID})
	rows, err := tx.QueryContext(ctx, `SELECT v.id FROM version v JOIN title t ON t.id = v.title_id WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, id)
	}
	return out, closeRows(rows)
}

// saveRecord upserts the view's record of the title from a request. Fields the request leaves empty keep their value.
// The series type is the title's, not the view's: the callers write it (catalog.SetSeriesType, AddSeriesType), and
// the record's series_type column is no longer read.
// updated_at orders the minimum availability choices (availability.Rules), so it moves only when one is written; the
// movie writers blank the request's value first and store a choice per library (chooseMinAvail).
func (s *Service) saveRecord(ctx context.Context, tx *sqlTx, v *View, titleID int64, in *titleIn) error {
	now := store.Millis(s.now())
	qp := in.profile()
	if v.profileByID(qp) == nil {
		qp = 0
	}
	sf := 1
	if in.SeasonFolder != nil && !*in.SeasonFolder {
		sf = 0
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO arrview_title (view, title_id, quality_profile_id, root_folder_path,
		minimum_availability, season_folder, series_type, monitor_new_items, added_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (view, title_id) DO UPDATE SET
		added_at = CASE WHEN added_at = 0 THEN excluded.added_at ELSE added_at END,
		quality_profile_id = CASE WHEN excluded.quality_profile_id > 0 THEN excluded.quality_profile_id ELSE quality_profile_id END,
		root_folder_path = CASE WHEN excluded.root_folder_path <> '' THEN excluded.root_folder_path ELSE root_folder_path END,
		minimum_availability = CASE WHEN excluded.minimum_availability <> '' THEN excluded.minimum_availability ELSE minimum_availability END,
		season_folder = CASE WHEN ? THEN excluded.season_folder ELSE season_folder END,
		series_type = CASE WHEN excluded.series_type <> '' THEN excluded.series_type ELSE series_type END,
		monitor_new_items = CASE WHEN excluded.monitor_new_items <> '' THEN excluded.monitor_new_items ELSE monitor_new_items END,
		updated_at = CASE WHEN excluded.minimum_availability <> '' THEN excluded.updated_at ELSE updated_at END`,
		v.Key, titleID, qp, strings.TrimSuffix(in.RootFolderPath, "/"), in.MinimumAvailability.minAvail(), sf,
		in.SeriesType.value(seriesTypes...), in.MonitorNewItems.value("all", "none"), now, now,
		in.SeasonFolder != nil)
	return err
}

// chooseMinAvail stores a movie's minimum availability as an explicit choice of each library in keys, on the
// library's own v/<key> row: never on a combined view's row, which would also reach copies the view does not show
// and libraries added to it later.
func (s *Service) chooseMinAvail(ctx context.Context, tx *sqlTx, titleID int64, keys []string, value string) error {
	if value == "" || len(keys) == 0 {
		return nil
	}
	return availability.Choose(ctx, tx, titleID, keys, value, s.now())
}

// chooseTurnedOn decides a client add's minimum availability per library the add turned on (in after, not in
// before): one with its own minimum_availability uses it and the client's value is not stored for it; another stores
// the client's value as its choice. A copy already on keeps its rule.
func (s *Service) chooseTurnedOn(ctx context.Context, tx *sqlTx, titleID int64, before, after []string,
	value string) error {
	var keys []string
	for _, k := range after {
		if vp := s.pol.Versions[k]; !slices.Contains(before, k) && (vp == nil || vp.MinimumAvailability == "") {
			keys = append(keys, k)
		}
	}
	return s.chooseMinAvail(ctx, tx, titleID, keys, value)
}

// setTags adds (replace false) or sets (replace true) the title's tags on the view's versions of the title it shows
// (scope: not removed), never another version's (tags are per version, docs/design/arrview.md "Tags per version"). A
// request that turns versions on does so before. Unknown tag ids are skipped.
func setTags(ctx context.Context, tx *sqlTx, v *View, titleID int64, tags []int64, replace bool) error {
	if replace {
		if err := removeTags(ctx, tx, v, titleID, nil); err != nil {
			return err
		}
	}
	where, args := scope(v, []int64{titleID})
	for _, t := range tags {
		if _, err := tx.ExecContext(ctx, `INSERT INTO arrview_title_tag (version, title_id, tag_id)
			SELECT DISTINCT v.policy_key, v.title_id, g.id FROM version v JOIN title t ON t.id = v.title_id
			JOIN arrview_tag g ON g.id = ? WHERE `+where+` ON CONFLICT DO NOTHING`, append([]any{t}, args...)...); err != nil {
			return err
		}
	}
	return nil
}

// removeTags takes the tags (all of them when nil) off the view's versions of the title it shows.
func removeTags(ctx context.Context, tx *sqlTx, v *View, titleID int64, tags []int64) error {
	where, args := scope(v, []int64{titleID})
	q := `DELETE FROM arrview_title_tag WHERE title_id = ? AND version IN
		(SELECT v.policy_key FROM version v JOIN title t ON t.id = v.title_id WHERE ` + where + `)`
	args = append([]any{titleID}, args...)
	if tags != nil {
		q += ` AND tag_id IN (SELECT value FROM json_each(?))`
		args = append(args, jsonIDs(tags))
	}
	_, err := tx.ExecContext(ctx, q, args...)
	return err
}

// setEpisodesMonitored sets the per-version monitored flag of episodes for the view's versions of a series,
// creating wanted state rows where none exist (catalog.SetEpisodesMonitored).
func (s *Service) setEpisodesMonitored(ctx context.Context, tx *sqlTx, v *View, titleID int64, where string,
	whereArgs []any, monitored bool) error {
	return catalog.SetEpisodesMonitored(ctx, tx, titleID, v.Versions, where, whereArgs, monitored, s.now())
}

// reportedSeasons is each season's monitored flag as the view reports it (seriesResource: monitored when one of its
// episodes is).
func (s *Service) reportedSeasons(ctx context.Context, v *View, e *entry) (map[int]bool, error) {
	eps, err := s.loadEpisodes(ctx, v, []*entry{e})
	if err != nil {
		return nil, err
	}
	out := map[int]bool{}
	for _, x := range eps[e.id] {
		out[x.season] = out[x.season] || x.monitored
	}
	return out, nil
}

// applySeasons applies Sonarr's season monitoring and monitorNewItems of a PUT to the view's versions, and keeps
// them as the versions' intent for episodes a metadata refresh adds later (catalog.SetSeasonMonitored). Like
// Sonarr's SeriesService.UpdateSeries, only a season whose flag differs from the one the view reports (reported)
// changes its episodes: Seerr PUTs the whole lookup back to request one more season, and a partly monitored season
// it echoes as monitored must keep its unmonitored episodes. A season the view does not report (no episodes yet)
// only records the choice. Versions this PUT turned on (fresh: new, restored or unmonitored before) take every season of the body.
// A version the PUT's newly chosen profile selects (picked) that was already on takes a season the body monitors
// when none of its episodes of that season is monitored (another copy reported the season monitored).
func (s *Service) applySeasons(ctx context.Context, tx *sqlTx, v *View, titleID int64, in *titleIn,
	reported map[int]bool, fresh, picked []string) error {
	for _, se := range in.Seasons {
		keys := v.Versions
		if cur, ok := reported[se.SeasonNumber]; ok && cur == se.Monitored {
			keys = slices.Clone(fresh)
			if se.Monitored {
				for _, k := range picked {
					if slices.Contains(keys, k) {
						continue
					}
					off, err := seasonOff(ctx, tx, titleID, k, se.SeasonNumber)
					if err != nil {
						return err
					}
					if off {
						keys = append(keys, k)
					}
				}
			}
			if len(keys) == 0 {
				continue
			}
		}
		if err := catalog.SetSeasonMonitored(ctx, tx, titleID, keys, se.SeasonNumber, se.Monitored,
			s.now()); err != nil {
			return err
		}
	}
	if mn := in.MonitorNewItems.value("all", "none"); mn != "" {
		return catalog.SetMonitorNew(ctx, tx, titleID, v.Versions, mn, s.now())
	}
	return nil
}

// seasonOff reports whether the title's version with this key has episodes in the season and none of them
// monitored.
func seasonOff(ctx context.Context, tx *sqlTx, titleID int64, key string, season int) (bool, error) {
	var n, on int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(s.monitored), 0) FROM episode_version_state s
		JOIN version v ON v.id = s.version_id JOIN episode e ON e.id = s.episode_id
		WHERE v.title_id = ? AND v.policy_key = ? AND e.season = ?`, titleID, key, season).Scan(&n, &on)
	return n > 0 && on == 0, err
}

func syncTitleMonitored(ctx context.Context, tx *sqlTx, titleID, now int64) error {
	return catalog.SyncMonitored(ctx, tx, titleID, store.FromMillis(now))
}

// POST movie | series: create the title if needed and turn on the versions the view's rules call for. A title the
// view already has gets the extra versions instead of Radarr's "already added" 400 (docs/design/arrview.md). 201 with the
// resource.
func (s *Service) addTitle(w http.ResponseWriter, r *request) {
	ctx := r.Context()
	var in titleIn
	if err := decodeBody(r, &in); err != nil {
		badRequest(w, "", err.Error(), nil)
		return
	}
	v := r.v
	ext := in.TmdbID
	prop := "TmdbId"
	if v.Flavour == Sonarr {
		ext, prop = in.TvdbID, "TvdbId"
	}
	if ext <= 0 {
		badRequest(w, prop, "'"+prop[:len(prop)-2]+" Id' must be greater than '0'.", ext)
		return
	}
	// Anime: what the request says (Sonarr's anime series type, an anime root folder or profile) wins; else Theatarr
	// decides for a title it does not hold yet (decideAnime), outside the write transaction.
	// A series Theatarr holds is anime to acquisition by its flag or its type, and an add never changes a stored
	// type: its libraries are chosen by the type it has after this add, not by the request's.
	hint := in
	if v.Flavour == Sonarr {
		typ, err := s.heldSeriesType(ctx, &in, ext)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if typ != "" {
			hint.SeriesType = enumText(typ)
		}
	}
	anime, source := s.animeHint(v, &hint), ""
	held := false
	if anime != nil && *anime {
		source = animeFromRequest
	} else if h, err := s.holds(ctx, v, &in, ext); err != nil {
		s.internal(w, r, err)
		return
	} else if held = h; !held {
		var a bool
		if a, source = s.decideAnime(ctx, v, &in, ext); a {
			anime = &a
		}
	}
	// A movie's minimum availability is stored per library, never on the view's record.
	var minAvail string
	if v.Flavour == Radarr {
		minAvail, in.MinimumAvailability = in.MinimumAvailability.minAvail(), ""
	}
	// The add itself is catalog.Add, shared with the native API; the view keeps its own record and tags.
	var titleID int64
	err := s.st.WriteTx(ctx, func(tx *sqlTx) error {
		ci := catalog.Input{Kind: v.kind(), Title: in.Title, Year: in.Year, ImdbID: in.ImdbID,
			Anime: anime, Monitored: in.monitored(true), Source: v.Key,
			Versions: func(anime bool) ([]string, error) { return s.chooseVersions(v, &in, anime) }}
		if v.Flavour == Sonarr {
			ci.TvdbID = ext
			ci.SeasonVersions = v.Versions
			ci.MonitorNew = in.MonitorNewItems.value("all", "none")
			for _, se := range in.Seasons {
				ci.Seasons = append(ci.Seasons, catalog.Season{Number: se.SeasonNumber, Monitored: se.Monitored})
			}
			// Ruddarr and LunaSea send Sonarr's addOptions.monitor; Seerr sends none and keeps its seasons.
			if in.AddOptions != nil {
				ci.Monitor, ci.Status = in.AddOptions.Monitor.value(catalog.MonitorTypes...), in.Status
			}
		} else {
			ci.TmdbID = ext
		}
		// The copies already on before the add: a movie's minimum availability is decided for the others.
		var on []string
		if v.Flavour == Radarr {
			var err error
			if on, err = activeVersions(ctx, tx, ext); err != nil {
				return err
			}
		}
		res, err := catalog.Add(ctx, tx, ci, s.now(), s.log.With("view", v.Key))
		if err != nil {
			return err
		}
		titleID = res.TitleID
		if v.Flavour == Sonarr {
			// An add gives a series its type only when it has none: a later add never changes a stored one.
			if err := catalog.AddSeriesType(ctx, tx, titleID, in.SeriesType.value(seriesTypes...)); err != nil {
				return err
			}
		}
		if v.Flavour == Radarr {
			if err := s.chooseTurnedOn(ctx, tx, titleID, on, res.Versions, minAvail); err != nil {
				return err
			}
		}
		if err := s.saveRecord(ctx, tx, v, titleID, &in); err != nil {
			return err
		}
		if res.Created && source != "" {
			// The decision is recorded where it was made, so a wrong guess is visible (and fixed by editing the
			// title's versions).
			if _, err := tx.ExecContext(ctx, `UPDATE arrview_title SET anime_source = ? WHERE view = ? AND title_id = ?`,
				source, v.Key, titleID); err != nil {
				return err
			}
			s.log.Info("anime decided", "view", v.Key, "title", titleID, "anime", res.Anime, "source", source,
				"versions", res.Versions)
		}
		return setTags(ctx, tx, v, titleID, in.Tags, false)
	})
	if errors.Is(err, errNoVersion) {
		badRequest(w, "QualityProfileId", "No version of this view takes this title: pick a profile", in.profile())
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if held {
		// A title whose anime check failed before is asked about again. A library that answer turns on is turned on
		// by this add, and takes its minimum availability as the others did.
		var on []string
		if v.Flavour == Radarr && minAvail != "" {
			if on, err = activeVersions(ctx, s.st.Reader(), ext); err != nil {
				s.internal(w, r, err)
				return
			}
		}
		if err := s.reclassify(ctx, v, titleID); err != nil {
			s.internal(w, r, err)
			return
		}
		if v.Flavour == Radarr && minAvail != "" {
			err := s.st.WriteTx(ctx, func(tx *sqlTx) error {
				now, err := activeVersions(ctx, tx, ext)
				if err != nil {
					return err
				}
				return s.chooseTurnedOn(ctx, tx, titleID, on, now, minAvail)
			})
			if err != nil {
				s.internal(w, r, err)
				return
			}
		}
	}
	if in.AddOptions != nil && (in.AddOptions.SearchForMovie || in.AddOptions.SearchForMissingEpisodes) {
		name, body := "MoviesSearch", map[string]any{"movieIds": []any{float64(viewID(titleID))}}
		if v.Flavour == Sonarr {
			name, body = "MissingEpisodeSearch", map[string]any{"seriesId": float64(viewID(titleID))}
		}
		if err := s.recordSearch(ctx, v, name, body, r.Header.Get("User-Agent")); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	s.respondTitle(w, r, titleID, http.StatusCreated)
}

func (s *Service) respondTitle(w http.ResponseWriter, r *request, titleID int64, code int) {
	e, err := s.loadOne(r.Context(), r.v, titleID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if e == nil {
		notFound(w, "not found")
		return
	}
	if r.v.Flavour == Sonarr {
		rs, err := s.seriesResources(r.Context(), r.v, []*entry{e})
		if err != nil {
			s.internal(w, r, err)
			return
		}
		writeJSON(w, code, rs[0])
		return
	}
	writeJSON(w, code, s.movieResource(r.v, e))
}

// PUT movie | series (full object, MovieController.UpdateMovie): changes only this view's versions of the title:
// monitored, a newly chosen profile's versions turned on, root folder, minimum availability, tags (replaced),
// Sonarr's season monitoring. Never moves files. 202 with the resource.
func (s *Service) updateTitle(w http.ResponseWriter, r *request) {
	ctx := r.Context()
	var in titleIn
	if err := decodeBody(r, &in); err != nil {
		badRequest(w, "", err.Error(), nil)
		return
	}
	if id, ok := r.pathID(1); ok {
		in.ID = id
	}
	e, err := s.resolveEntry(ctx, r.v, in.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if e == nil {
		notFound(w, strings.TrimSuffix(r.raw[0], "s")+" with ID "+strconv.FormatInt(in.ID, 10)+" does not exist")
		return
	}
	v := r.v
	var reported map[int]bool
	if v.Flavour == Sonarr {
		if reported, err = s.reportedSeasons(ctx, v, e); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	// A movie's minimum availability is stored per library, never on the view's record.
	var minAvail string
	if v.Flavour == Radarr {
		minAvail, in.MinimumAvailability = in.MinimumAvailability.minAvail(), ""
	}
	err = s.st.WriteTx(ctx, func(tx *sqlTx) error {
		now := store.Millis(s.now())
		// The body is the whole object, usually the view's own answer sent back with one field changed:
		// only what differs from that answer changes. The view reports monitored when any copy is and one profile
		// for all copies, so echoing either back must not turn on, or restore, a copy the user left off.
		mon := in.monitored(e.monitored())
		after := e.seriesType // the type once this PUT is stored
		if v.Flavour == Sonarr {
			// The series type is one value per title, shown on every view. The value the view showed, sent back,
			// says nothing: it is not stored. Another value is an edit of the title's type.
			typ := in.SeriesType.value(seriesTypes...)
			if typ == e.seriesType {
				in.SeriesType = ""
			} else if err := catalog.SetSeriesType(ctx, tx, e.id, typ); err != nil {
				return err
			} else if typ != "" {
				after = typ
			}
		}
		var fresh, picked []string
		if p := v.profileByID(in.profile()); p != nil && p.ID != qualityProfileID(v, e) {
			// Anime as acquisition will see the title after this PUT: its flag, or its type.
			anime := e.anime || after == "anime"
			if h := s.animeHint(v, &in); h != nil && *h {
				anime = true
			}
			keys, err := s.chooseVersions(v, &in, anime)
			if err != nil {
				return err
			}
			// Versions this PUT turns on (new, removed or unmonitored before) take every season of the body
			// The view's season flags describe the copies already on, not these.
			if fresh, err = offVersions(ctx, tx, e.id, keys); err != nil {
				return err
			}
			picked = keys
			if _, err = s.turnOn(ctx, tx, v, e.id, keys, mon); err != nil {
				return err
			}
		}
		if mon != e.monitored() {
			vids, err := viewVersionIDs(ctx, tx, v, e.id)
			if err != nil {
				return err
			}
			for _, id := range vids {
				if _, err := tx.ExecContext(ctx, `UPDATE version SET monitored = ?, updated_at = ? WHERE id = ?
					AND monitored <> ?`, mon, now, id, mon); err != nil {
					return err
				}
			}
		}
		if v.Flavour == Radarr {
			// A minimum availability equal to the one this view shows is the view's answer sent back and stores
			// nothing; a different one is a choice for each library the view showed for the movie. A copy this
			// PUT turns on takes its library's own rule.
			if minAvail != s.minAvail(e) {
				if err := s.chooseMinAvail(ctx, tx, e.id, e.versionKeys(), minAvail); err != nil {
					return err
				}
			}
		}
		if err := s.saveRecord(ctx, tx, v, e.id, &in); err != nil {
			return err
		}
		if in.Tags != nil {
			if err := setTags(ctx, tx, v, e.id, in.Tags, true); err != nil {
				return err
			}
		}
		if v.Flavour == Sonarr {
			if err := s.applySeasons(ctx, tx, v, e.id, &in, reported, fresh, picked); err != nil {
				return err
			}
		}
		return syncTitleMonitored(ctx, tx, e.id, now)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if err := s.reclassify(ctx, v, e.id); err != nil {
		s.internal(w, r, err)
		return
	}
	s.respondTitle(w, r, e.id, http.StatusAccepted)
}

// DELETE movie|series/{id}[?deleteFiles&addImportExclusion]: unlinks only this view's versions of the title
// (docs/design/arrview.md). They are unmonitored and hidden from every view; their rows, arr files and history stay. deleteFiles=true
// checks Theatarr's links first (a refusal changes nothing) and moves them to trash after the hide. The title stays for
// other views. The versions' downloads in hand end in Theatarr's records (router.EndRemoved; never at the provider) in the
// transaction that hides them, and the links are enumerated after it: an import that committed before the hide has
// its links moved to trash too, and one still running then finds its download ended and takes its links back.
// Nothing is unlinked before the hide commits: a link moved away while the version was
// still wanted could be replaced by a search, and a failed hide left it unlinked. A link whose move fails stays: the
// answer says so, and Theatarr's title page moves the removed copies' leftover links (web RemovalScope.Leftover). A
// version an arr instance still owns is refused (409), as in the web UI. 200, no body, like the arrs.
// addImportExclusion=true on a Radarr view also puts the movie on the exclusion list (movie_exclusion, by TMDb id, for
// every view, as the exclusions POST does), so lists and collections do not add it again; Sonarr's
// addImportListExclusion is ignored (there is no series exclusion list).
func (s *Service) deleteTitle(w http.ResponseWriter, r *request) {
	ctx := r.Context()
	id, _ := r.pathID(1)
	e, err := s.resolveEntry(ctx, r.v, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if e == nil {
		notFound(w, strings.TrimSuffix(r.raw[0], "s")+" with ID "+strconv.FormatInt(id, 10)+" does not exist")
		return
	}
	// A version an arr instance still owns is refused before anything changes, as the web UI's remove does.
	for _, x := range e.versions {
		var inst string
		err := s.st.Reader().QueryRowContext(ctx, `SELECT instance FROM version_source WHERE version_id = ?`,
			x.id).Scan(&inst)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			s.internal(w, r, err)
			return
		}
		if m := s.pol.ManagedBy(inst); m != "" {
			writeJSON(w, http.StatusConflict, errorBody{Message: "nothing removed",
				Description: x.key + ": " + policy.ManagedReason(m)})
			return
		}
	}
	if r.queryBool("deleteFiles", false) {
		// A row per version: a link shared by two versions of the view is checked for each.
		fs, err := s.viewFiles(ctx, r.v, []int64{e.id})
		if err != nil {
			s.internal(w, r, err)
			return
		}
		for _, f := range fs {
			if f.linkID == 0 {
				continue
			}
			if s.library == nil {
				writeJSON(w, http.StatusConflict, errorBody{Message: "library is unavailable: nothing removed"})
				return
			}
			if err := s.library.CheckUnlink(f.version, f.f.path); err != nil {
				if errors.Is(err, library.ErrRefused) {
					writeJSON(w, http.StatusConflict, errorBody{Message: "nothing removed", Description: err.Error()})
				} else {
					s.internal(w, r, err)
				}
				return
			}
		}
	}
	reason := "deleted through " + r.v.Key + " by " + clientName(r)
	// The webhook payload is built now, while the view still holds the title.
	if len(e.versions) > 0 {
		s.emit(ctx, notify.Event{Kind: notify.Delete, View: r.v.Key, TitleID: e.id, Version: e.versions[0].key,
			Reason: "manual"})
	}
	// Radarr's addImportExclusion=true: the movie also goes on the exclusion list, with the remove.
	exclude := r.v.Flavour == Radarr && e.kind == "movie" && e.tmdb > 0 && r.queryBool("addImportExclusion", false)
	ended := 0
	err = s.st.WriteTx(ctx, func(tx *sqlTx) error {
		now := store.Millis(s.now())
		for _, x := range e.versions {
			if _, err := tx.ExecContext(ctx, `INSERT INTO arrview_removed_version (version_id, view, removed_at)
				VALUES (?, ?, ?) ON CONFLICT (version_id) DO NOTHING`, x.id, r.v.Key, now); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE version SET monitored = 0, updated_at = ? WHERE id = ?`, now,
				x.id); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM arrview_title WHERE view = ? AND title_id = ?`, r.v.Key,
			e.id); err != nil {
			return err
		}
		// The removed copies' minimum availability choices go with them, so a later add takes the library's rule
		// again; whatever else a library's own row holds stays.
		for _, x := range e.versions {
			if _, err := tx.ExecContext(ctx, `UPDATE arrview_title SET minimum_availability = '' WHERE view = ?
				AND title_id = ?`, "v/"+x.key, e.id); err != nil {
				return err
			}
		}
		if err := syncTitleMonitored(ctx, tx, e.id, now); err != nil {
			return err
		}
		if exclude {
			// The statement of the exclusions POST (lists.go): lists and collections skip the movie from now on.
			if _, err := tx.ExecContext(ctx, `INSERT INTO movie_exclusion(tmdb_id,created_at) VALUES(?,?)
				ON CONFLICT DO NOTHING`, e.tmdb, now); err != nil {
				return err
			}
		}
		// Downloads in hand of the removed versions end in our records with the hide, so they are not imported
		// back and the remove never half-succeeds; the provider keeps its torrents.
		ended, err = router.EndRemoved(ctx, tx, e.id, e.versionKeys(), reason, now)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if r.queryBool("deleteFiles", false) {
		// After the fence: every link the versions have, including any an import committed before the hide.
		kept, failed, err := s.unlinkRemoved(ctx, e, reason)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if len(failed) > 0 {
			s.log.Warn("versions removed; moving files to trash failed", "view", r.v.Key, "title", e.id,
				"failed", strings.Join(failed, "; "))
			writeJSON(w, http.StatusInternalServerError, errorBody{Message: "versions removed; moving some files to " +
				"trash failed (Theatarr's title page moves the links left)", Description: strings.Join(failed, "; ")})
			return
		}
		if len(kept) > 0 {
			writeJSON(w, http.StatusConflict, errorBody{Message: "versions removed; some files were kept",
				Description: strings.Join(kept, "; ")})
			return
		}
	}
	s.log.Info("versions removed", "view", r.v.Key, "title", e.id, "versions", strings.Join(e.versionKeys(), ","),
		"delete_files", r.queryBool("deleteFiles", false), "downloads_ended", ended)
	w.WriteHeader(http.StatusOK)
}

// unlinkRemoved moves to trash the links still active for e's versions after they were removed (deleteTitle's
// enumeration after the hide). It returns the links it kept (refused), and the ones whose move failed, with why; a
// failing link does not stop the others. err is a failure to read the links.
func (s *Service) unlinkRemoved(ctx context.Context, e *entry, reason string) (kept, failed []string, err error) {
	if len(e.versions) == 0 {
		return nil, nil, nil
	}
	ids := make([]any, len(e.versions))
	for i, x := range e.versions {
		ids[i] = x.id
	}
	type left struct {
		id            int64
		version, path string
	}
	var links []left
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT l.id, v.policy_key, l.path FROM link l
		JOIN link_version lv ON lv.link_id = l.id JOIN version v ON v.id = lv.version_id
		WHERE lv.version_id IN (`+store.Placeholders(len(ids))+`) AND l.state = 'active' ORDER BY l.id, v.id`, ids...)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var x left
		if err := rows.Scan(&x.id, &x.version, &x.path); err != nil {
			rows.Close()
			return nil, nil, err
		}
		links = append(links, x)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	for _, x := range links {
		if s.library == nil {
			kept = append(kept, x.path+": library is unavailable")
			continue
		}
		if err := s.library.CheckUnlink(x.version, x.path); err != nil {
			if errors.Is(err, library.ErrRefused) {
				kept = append(kept, x.path+": "+err.Error())
			} else {
				failed = append(failed, x.path+": "+err.Error())
			}
			continue
		}
		res, err := s.library.Unlink(ctx, x.version, x.path, x.id, reason)
		switch {
		case err != nil:
			failed = append(failed, x.path+": "+err.Error())
		case res.DryRun:
			kept = append(kept, x.path+": dry run: "+res.Refused)
		}
	}
	return kept, failed, nil
}

// PUT episode/monitor {episodeIds, monitored} (EpisodeController.SetEpisodesMonitored): per-version monitoring of
// the view's versions. 202 with the episodes.
func (s *Service) monitorEpisodes(w http.ResponseWriter, r *request) {
	ctx := r.Context()
	var in struct {
		EpisodeIDs []int64 `json:"episodeIds"`
		Monitored  bool    `json:"monitored"`
	}
	if err := decodeBody(r, &in); err != nil {
		badRequest(w, "", err.Error(), nil)
		return
	}
	if len(in.EpisodeIDs) == 0 {
		badRequest(w, "EpisodeIds", "'Episode Ids' must not be empty.", nil)
		return
	}
	es, err := s.entriesForEpisodes(ctx, r.v, in.EpisodeIDs)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	ph := store.Placeholders(len(in.EpisodeIDs))
	args := make([]any, len(in.EpisodeIDs))
	for i, id := range in.EpisodeIDs {
		args[i] = id
	}
	err = s.st.WriteTx(ctx, func(tx *sqlTx) error {
		for _, e := range es {
			if err := s.setEpisodesMonitored(ctx, tx, r.v, e.id, `e.id IN (`+ph+`)`, args, in.Monitored); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	eps, err := s.loadEpisodes(ctx, r.v, es)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	want := map[int64]bool{}
	for _, id := range in.EpisodeIDs {
		want[id] = true
	}
	out := []episodeResource{}
	for _, e := range es {
		for _, x := range eps[e.id] {
			if want[x.id] {
				out = append(out, episodeFrom(e, x))
			}
		}
	}
	writeJSON(w, http.StatusAccepted, out)
}
