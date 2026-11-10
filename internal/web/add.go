package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/acquire"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/metadata/art"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// Search and add (docs/design/ui.md "Search"): GET /search, GET /add/options, POST /titles (through
// catalog.Add, the arr views' add path), PUT /titles/{id}/versions/{key}, POST /titles/{id}/search and
// GET /titles/{id}/releases.
//
// Safety: an add never searches unless asked, and a search (from an add or a click) runs only for versions in mode
// active while router.enabled is true (catalog.Searchable); every other version is answered with the reason. The
// releases panel searches indexers live only for an active version; for a shadow version it answers from what is
// stored (the journal's decided searches, the shadow runner's last grab), so a click sends no indexer traffic.

// AddConfig is what search and add use beyond the store and the policy. Each may be nil.
type AddConfig struct {
	// Acquirer starts searches and runs the releases panel's live searches (acquire.Acquirer).
	Acquirer Acquirer
	// TMDb answers search and fills names and ids on add (tmdb.Client through the gateway and the tmdb budget).
	TMDb TMDbClient
	// Collections adds and refreshes collections from the Collections page (catalog.MovieIntake of the settings in
	// force, with its TMDb client); nil without TMDb.
	Collections MovieIntake
}

// MovieIntake is the part of the movie intake (catalog.MovieIntake and its TMDb client) the Collections page uses.
type MovieIntake interface {
	SearchCollection(ctx context.Context, query string) ([]tmdb.CollectionResult, error)
	Collection(ctx context.Context, id int) (*tmdb.Collection, error)
	// RefreshCollection refreshes one known collection now and adds a monitored one's missing movies.
	RefreshCollection(ctx context.Context, tmdbID int64) (catalog.IntakeResult, error)
}

// Acquirer is the part of acquire.Acquirer search and add use.
type Acquirer interface {
	WantedList(context.Context, acquire.WantedQuery) (*acquire.WantedList, error)
	Submit(ctx context.Context, commandID int64, sel acquire.Selection) (queued, found int, err error)
	Interactive(ctx context.Context, titleID int64, version string, season *int, episodes []int64) (*router.Searched,
		error)
}

// TMDbClient is the part of tmdb.Client search and add use.
type TMDbClient interface {
	SearchMulti(ctx context.Context, query string) ([]tmdb.MultiResult, error)
	MovieDetails(ctx context.Context, id int) (*tmdb.Movie, error)
	TVDetails(ctx context.Context, id int) (*tmdb.TV, error)
	SeasonEpisodeCounts(ctx context.Context, tvID int) (map[int]int, error)
}

func (w *Web) mountAddAPI(mux *http.ServeMux) {
	b := APIBase
	w.handle(mux, "GET "+b+"/wanted", SignedIn, auth.PermAdmin, w.apiWanted)
	w.handle(mux, "GET "+b+"/search", SignedIn, auth.PermBrowse, w.apiSearch)
	w.handle(mux, "GET "+b+"/add/options", SignedIn, auth.PermBrowse, w.apiAddOptions)
	w.handle(mux, "POST "+b+"/titles", SignedIn, auth.PermAdd, w.apiAddTitle)
	w.handle(mux, "PUT "+b+"/titles/{id}/versions/{key}", SignedIn, auth.PermAdd, w.apiSetVersion)
	w.handle(mux, "POST "+b+"/titles/{id}/search", SignedIn, auth.PermManage, w.apiSearchNow)
	w.handle(mux, "GET "+b+"/titles/{id}/releases", SignedIn, auth.PermManage, w.apiReleases)
}

// VersionOption is one version an add sheet offers.
type VersionOption struct {
	GrabState VersionGrabState `json:"grab_state"`
	Key       string           `json:"key"`
	Kind      string           `json:"kind"`
	// Label names the version for people ("4K WEB").
	Label string `json:"label"`
	// Anime is the version's anime rule: true (anime only), false (no anime), absent (both).
	Anime  *bool  `json:"anime,omitempty"`
	Mode   string `json:"mode"`
	Enable string `json:"enable"`
	// Fits: the version takes this title (kind and anime). Default: an add asks for it unless unticked.
	Fits    bool `json:"fits"`
	Default bool `json:"default"`
	// On: the title already has the version, monitored.
	On bool `json:"on"`
	// OpenSeasons (a series the title has with this version on): its seasons, specials aside, that the version does
	// not fully monitor. A request can still ask for them ("Request more seasons").
	OpenSeasons []int `json:"open_seasons,omitempty"`
	// Searchable: a search would run for it (mode active, router on); Reason says why not.
	Searchable bool   `json:"searchable"`
	Reason     string `json:"reason,omitempty"`
}

// SeasonOption is a season of a series in the add sheet.
type SeasonOption struct {
	Number   int `json:"season"`
	Episodes int `json:"episodes"`
	// Monitored is the season's state in the library: true when every known episode is monitored for some version.
	Monitored *bool `json:"monitored,omitempty"`
}

// AddOptions is what the add sheet shows for a title.
type AddOptions struct {
	Kind    string `json:"kind"`
	Anime   bool   `json:"anime"`
	TitleID *int64 `json:"title_id,omitempty"`
	// RouterEnabled is router.enabled; with it off nothing is searched or grabbed.
	RouterEnabled bool            `json:"router_enabled"`
	Versions      []VersionOption `json:"versions"`
	// Seasons come from the library's episodes (EpisodesKnown) or else from TMDb. Season choices apply to episodes
	// the library knows, so the sheet offers them only when EpisodesKnown.
	Seasons       []SeasonOption `json:"seasons"`
	EpisodesKnown bool           `json:"episodes_known"`
	// CanSearch: a search now would do something for at least one default version.
	CanSearch bool `json:"can_search"`
}

func parseKind(s string) (policy.Kind, bool) {
	switch s {
	case "movie":
		return policy.KindMovie, true
	case "series", "tv":
		return policy.KindSeries, true
	}
	return "", false
}

type titleBits struct {
	id     int64
	kind   policy.Kind
	name   string
	anime  bool
	tmdbID int64
}

// findTitle reads a title by id, or by kind and TMDb id (title or title_meta); nil when none.
func (w *Web) findTitle(ctx context.Context, id int64, kind policy.Kind, tmdbID int64) (*titleBits, error) {
	q := `SELECT t.id, t.kind, t.name, t.anime, COALESCE(t.tmdb_id, m.tmdb_id, 0) FROM title t
		LEFT JOIN title_meta m ON m.title_id = t.id WHERE `
	var args []any
	switch {
	case id > 0:
		q += `t.id = ?`
		args = []any{id}
	case tmdbID > 0 && kind != "":
		q += `t.kind = ? AND (t.tmdb_id = ? OR m.tmdb_id = ?) ORDER BY t.tmdb_id IS NULL, t.id LIMIT 1`
		args = []any{string(kind), tmdbID, tmdbID}
	default:
		return nil, nil
	}
	var t titleBits
	var k string
	err := w.st.Reader().QueryRowContext(ctx, q, args...).Scan(&t.id, &k, &t.name, &t.anime, &t.tmdbID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	t.kind = policy.Kind(k)
	return &t, err
}

// versionsOn is the title's monitored versions that are not removed from the views.
func (w *Web) versionsOn(ctx context.Context, titleID int64) ([]string, error) {
	var out []string
	err := scanAll(ctx, w.st.Reader(), `SELECT v.policy_key FROM version v WHERE v.title_id = ? AND v.monitored = 1
		AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id) ORDER BY v.policy_key`,
		[]any{titleID}, func(r *sql.Rows) error {
			var k string
			if err := r.Scan(&k); err != nil {
				return err
			}
			out = append(out, k)
			return nil
		})
	return out, err
}

// GET /add/options?kind=&anime=&tmdb_id=&title_id=: the versions an add offers (fitting ones ticked), whether a
// search would run, and a series' seasons.
func (w *Web) apiAddOptions(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	kind, _ := parseKind(q.Get("kind"))
	titleID, _ := strconv.ParseInt(q.Get("title_id"), 10, 64)
	tmdbID, _ := strconv.ParseInt(q.Get("tmdb_id"), 10, 64)
	t, err := w.findTitle(ctx, titleID, kind, tmdbID)
	if err != nil {
		w.internal(rw, err)
		return
	}
	if titleID > 0 && t == nil {
		notFound(rw)
		return
	}
	anime, _ := strconv.ParseBool(q.Get("anime"))
	var on []string
	if t != nil {
		kind = t.kind
		if t.anime {
			anime = true
		}
		if on, err = w.versionsOn(ctx, t.id); err != nil {
			w.internal(rw, err)
			return
		}
		if tmdbID == 0 {
			tmdbID = t.tmdbID
		}
	}
	if kind == "" {
		badRequest(rw, "kind must be movie or series (or give title_id)")
		return
	}
	if w.pol == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "no policy loaded"})
		return
	}
	out := AddOptions{Kind: string(kind), Anime: anime, RouterEnabled: w.pol.RouterSettings().Enabled,
		Versions: []VersionOption{}, Seasons: []SeasonOption{}}
	if t != nil {
		out.TitleID = &t.id
	}
	keys := make([]string, 0, len(w.pol.Versions))
	for k, vp := range w.pol.Versions {
		if vp.Kind == kind {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	var gaps map[string][]int
	if t != nil && kind == policy.KindSeries {
		if gaps, err = w.seasonGaps(ctx, t.id); err != nil {
			w.internal(rw, err)
			return
		}
	}
	names := w.keyLabels(keys)
	for _, k := range keys {
		vp := w.pol.Versions[k]
		var open []int
		for _, n := range gaps[k] {
			if n > 0 && slices.Contains(on, k) {
				open = append(open, n)
			}
		}
		o := VersionOption{GrabState: versionGrabState(w.pol, k), OpenSeasons: open, Key: k, Kind: string(vp.Kind), Label: names[k], Anime: vp.Anime, Mode: string(vp.Mode),
			Enable: vp.Enable,
			Fits:   catalog.Fits(vp, kind, anime), On: slices.Contains(on, k)}
		if o.Mode == "" {
			o.Mode = string(policy.ModeShadow)
		}
		o.Default = o.Fits
		active, skipped := catalog.Searchable(w.pol, []string{k})
		o.Searchable = len(active) > 0
		if len(skipped) > 0 {
			o.Reason = skipped[0].Reason
		}
		if o.Default && o.Searchable {
			out.CanSearch = true
		}
		out.Versions = append(out.Versions, o)
	}
	if kind == policy.KindSeries {
		if t != nil {
			if out.Seasons, err = w.librarySeasons(ctx, t.id); err != nil {
				w.internal(rw, err)
				return
			}
			out.EpisodesKnown = len(out.Seasons) > 0
		}
		if !out.EpisodesKnown && tmdbID > 0 && w.add.TMDb != nil {
			counts, err := w.add.TMDb.SeasonEpisodeCounts(ctx, int(tmdbID))
			if err != nil {
				w.log.Warn("add options: TMDb seasons", "tmdb", tmdbID, "err", err)
			}
			for n, c := range counts {
				out.Seasons = append(out.Seasons, SeasonOption{Number: n, Episodes: c})
			}
			slices.SortFunc(out.Seasons, func(a, b SeasonOption) int { return a.Number - b.Number })
		}
	}
	writeJSON(rw, http.StatusOK, out)
}

// librarySeasons are a series' seasons from its episode rows, with whether each is monitored for some version.
func (w *Web) librarySeasons(ctx context.Context, titleID int64) ([]SeasonOption, error) {
	out := []SeasonOption{}
	err := scanAll(ctx, w.st.Reader(), `SELECT e.season, COUNT(*), COALESCE(MIN((SELECT MAX(s.monitored) FROM
		episode_version_state s WHERE s.episode_id = e.id)), 1) FROM episode e WHERE e.title_id = ? GROUP BY e.season
		ORDER BY e.season`, []any{titleID}, func(r *sql.Rows) error {
		var s SeasonOption
		var mon bool
		if err := r.Scan(&s.Number, &s.Episodes, &mon); err != nil {
			return err
		}
		s.Monitored = &mon
		out = append(out, s)
		return nil
	})
	return out, err
}

// AddRequest is the body of POST /titles.
type AddRequest struct {
	// TitleID adds versions (and seasons) to a title already in the library, found by its id, so a title with only
	// a TVDB id, or none in the title row, works too. Kind may then be left out; given, it must match.
	TitleID int64  `json:"title_id"`
	Kind    string `json:"kind"`
	TMDbID  int64  `json:"tmdb_id"`
	TVDbID  int64  `json:"tvdb_id"`
	IMDbID  string `json:"imdb_id"`
	// Title and Year name a new title; when empty they come from TMDb.
	Title string `json:"title"`
	Year  int    `json:"year"`
	// Anime marks a new title anime (a stored anime title stays anime).
	Anime *bool `json:"anime"`
	// Versions to turn on; absent means every version that fits the title (catalog.DefaultVersions).
	Versions  []string         `json:"versions"`
	Seasons   []catalog.Season `json:"seasons"`
	Monitored *bool            `json:"monitored"`
	// MonitorNew is a series' monitoring of seasons that appear later: "all" (default for a new version) or "none".
	MonitorNew string `json:"monitor_new"`
	// Search asks for a search after the add, for the versions catalog.Searchable lets through.
	Search bool `json:"search"`

	// Set by a request's approval only: Seasons apply to seasonVersions (nil: every version of the add), and
	// moreSeasons are monitored in moreVersions, versions the series already has, leaving their other seasons as
	// they are ("Request more seasons").
	seasonVersions []string
	moreVersions   []string
	moreSeasons    []int
}

// AddedTitle is a title after an add or a version change.
type AddedTitle struct {
	ID        int64          `json:"id"`
	Kind      string         `json:"kind"`
	Name      string         `json:"name"`
	Year      *int64         `json:"year,omitempty"`
	TMDbID    *int64         `json:"tmdb_id,omitempty"`
	TVDbID    *int64         `json:"tvdb_id,omitempty"`
	Anime     bool           `json:"anime"`
	Monitored bool           `json:"monitored"`
	Versions  []VersionBrief `json:"versions"`
	TitleArt
}

// AddResult is the answer to POST /titles.
type AddResult struct {
	Title   AddedTitle `json:"title"`
	Created bool       `json:"created"`
	// Versions are the keys turned on; New the version rows this add created.
	Versions []string `json:"versions"`
	New      []string `json:"new"`
	// Search is what the search asked for did; absent when none was asked for.
	Search *SearchOutcome `json:"search,omitempty"`
}

// SearchOutcome is what a search now did: the versions searched, the ones skipped and why.
type SearchOutcome struct {
	Started  bool           `json:"started"`
	Versions []string       `json:"versions"`
	Skipped  []catalog.Skip `json:"skipped"`
	Queued   int            `json:"queued"`
	Found    int            `json:"found"`
	Message  string         `json:"message"`
}

func (w *Web) loadAdded(ctx context.Context, id int64) (*AddedTitle, error) {
	var t AddedTitle
	var year, tmdbID, tvdbID sql.NullInt64
	db := w.st.Reader()
	err := db.QueryRowContext(ctx, `SELECT id, kind, name, year, tmdb_id, tvdb_id, anime, monitored FROM title
		WHERE id = ?`, id).Scan(&t.ID, &t.Kind, &t.Name, &year, &tmdbID, &tvdbID, &t.Anime, &t.Monitored)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.Year, t.TMDbID, t.TVDbID = nullInt(year), nullInt(tmdbID), nullInt(tvdbID)
	t.Versions = []VersionBrief{}
	err = scanAll(ctx, db, `SELECT v.id, v.policy_key, v.state, v.monitored, `+hasFileSQL+` FROM version v
		WHERE v.title_id = ? AND NOT EXISTS (SELECT 1 FROM arrview_removed_version r WHERE r.version_id = v.id)
		ORDER BY v.policy_key`, []any{id}, func(r *sql.Rows) error {
		var v VersionBrief
		if err := r.Scan(&v.ID, &v.Key, &v.State, &v.Monitored, &v.HasFile); err != nil {
			return err
		}
		t.Versions = append(t.Versions, v)
		return nil
	})
	if err != nil {
		return nil, err
	}
	metas, err := art.LoadMeta(ctx, db, []int64{id})
	if err != nil {
		return nil, err
	}
	var mp *art.Meta
	if m, ok := metas[id]; ok {
		mp = &m
	}
	t.TitleArt = artFor(id, mp)
	return &t, nil
}

// checkVersions refuses keys the policy lacks or of another kind.
func (w *Web) checkVersions(keys []string, kind policy.Kind) error {
	for _, k := range keys {
		vp := w.pol.Versions[k]
		if vp == nil {
			return fmt.Errorf("unknown version %q", k)
		}
		if vp.Kind != kind {
			return fmt.Errorf("version %q is for %s titles", k, vp.Kind)
		}
	}
	return nil
}

// fillFromTMDb completes a request's name, year and ids from TMDb when they are missing (one details request).
func (w *Web) fillFromTMDb(ctx context.Context, req *AddRequest, kind policy.Kind) {
	if w.add.TMDb == nil || req.TMDbID <= 0 {
		return
	}
	if strings.TrimSpace(req.Title) != "" && (kind == policy.KindMovie || req.TVDbID > 0) && req.Year > 0 {
		return
	}
	if kind == policy.KindMovie {
		m, err := w.add.TMDb.MovieDetails(ctx, int(req.TMDbID))
		if err != nil {
			w.log.Warn("add: TMDb movie details", "tmdb", req.TMDbID, "err", err)
			return
		}
		if strings.TrimSpace(req.Title) == "" {
			req.Title = m.Title
		}
		if req.Year == 0 {
			req.Year = m.Year()
		}
		if req.IMDbID == "" {
			req.IMDbID = m.IMDbID
		}
		return
	}
	t, err := w.add.TMDb.TVDetails(ctx, int(req.TMDbID))
	if err != nil {
		w.log.Warn("add: TMDb TV details", "tmdb", req.TMDbID, "err", err)
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		req.Title = t.Name
	}
	if req.Year == 0 && len(t.FirstAirDate) >= 4 {
		req.Year, _ = strconv.Atoi(t.FirstAirDate[:4])
	}
	if req.TVDbID == 0 {
		req.TVDbID = int64(t.ExternalIDs.TVDbID)
	}
	if req.IMDbID == "" {
		req.IMDbID = t.ExternalIDs.IMDbID
	}
}

// POST /titles: add a title with versions (and seasons for a series) through catalog.Add, the arr views' add path.
// A title already in the library gets the versions it lacks. 201 with the title; the search, when asked for, runs
// only for versions catalog.Searchable lets through and says why for the rest.
func (w *Web) apiAddTitle(rw http.ResponseWriter, r *http.Request) {
	var req AddRequest
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	out, err := w.addTitle(r.Context(), req, "ui")
	var bad *addError
	switch {
	case errors.As(err, &bad):
		writeJSON(rw, bad.code, apiError{Error: bad.msg})
		return
	case err != nil:
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusCreated, out)
}

// addError is an add refused for the caller's input (or a missing policy), with its HTTP status.
type addError struct {
	code int
	msg  string
}

func (e *addError) Error() string { return e.msg }

// addTitle is POST /titles' work, shared with request approval (requests.go): check the input, run catalog.Add in one
// transaction, queue art for a new title, search when asked, and read the title back. source names the caller in the
// log ("ui", "request:<id>").
func (w *Web) addTitle(ctx context.Context, req AddRequest, source string) (*AddResult, error) {
	kind, ok := parseKind(req.Kind)
	switch {
	case req.TitleID > 0:
		t, err := w.findTitle(ctx, req.TitleID, "", 0)
		if err != nil {
			return nil, err
		}
		if t == nil {
			return nil, &addError{http.StatusNotFound, "not found"}
		}
		if req.Kind != "" && (!ok || kind != t.kind) {
			return nil, &addError{http.StatusBadRequest, fmt.Sprintf("title %d is a %s", t.id, t.kind)}
		}
		kind = t.kind
	case !ok:
		return nil, &addError{http.StatusBadRequest, "kind must be movie or series"}
	case req.TMDbID <= 0 && req.TVDbID <= 0:
		return nil, &addError{http.StatusBadRequest, "title_id, tmdb_id or (for a series) tvdb_id is required"}
	case kind == policy.KindMovie && req.TMDbID <= 0:
		return nil, &addError{http.StatusBadRequest, "a movie needs tmdb_id"}
	}
	if w.pol == nil {
		return nil, &addError{http.StatusServiceUnavailable, "no policy loaded"}
	}
	if req.Versions != nil {
		if len(req.Versions) == 0 {
			return nil, &addError{http.StatusBadRequest, "pick at least one version"}
		}
		if err := w.checkVersions(req.Versions, kind); err != nil {
			return nil, &addError{http.StatusBadRequest, err.Error()}
		}
	}
	for _, s := range req.Seasons {
		if s.Number < 0 {
			return nil, &addError{http.StatusBadRequest, "season numbers start at 0"}
		}
	}
	if req.MonitorNew != "" && req.MonitorNew != catalog.MonitorAll && req.MonitorNew != catalog.MonitorNone {
		return nil, &addError{http.StatusBadRequest, "monitor_new must be all or none"}
	}
	if req.TitleID <= 0 {
		w.fillFromTMDb(ctx, &req, kind)
	}
	monitored := req.Monitored == nil || *req.Monitored
	in := catalog.Input{Kind: kind, TitleID: req.TitleID, TmdbID: req.TMDbID, TvdbID: req.TVDbID, ImdbID: strings.TrimSpace(req.IMDbID),
		Title: req.Title, Year: req.Year, Anime: req.Anime, Monitored: monitored, Seasons: req.Seasons, Source: source,
		MonitorNew: req.MonitorNew, SeasonVersions: req.seasonVersions,
		Versions: func(anime bool) ([]string, error) {
			if req.Versions != nil {
				return req.Versions, nil
			}
			if ks := catalog.DefaultVersions(w.pol, kind, anime); len(ks) > 0 {
				return ks, nil
			}
			return nil, errNoFit
		}}
	var res *catalog.Result
	err := w.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		if res, err = catalog.Add(ctx, tx, in, w.now(), w.log); err != nil || len(req.moreVersions) == 0 {
			return err
		}
		for _, n := range req.moreSeasons {
			if err := catalog.SetSeasonMonitored(ctx, tx, res.TitleID, req.moreVersions, n, true, w.now()); err != nil {
				return err
			}
		}
		// An episode is monitored when any version monitors it (as the episode monitor action keeps it).
		_, err = tx.ExecContext(ctx, `UPDATE episode SET monitored = 1, updated_at = ? WHERE title_id = ? AND monitored = 0
			AND EXISTS (SELECT 1 FROM episode_version_state s WHERE s.episode_id = episode.id AND s.monitored = 1)`,
			store.Millis(w.now()), res.TitleID)
		return err
	})
	if errors.Is(err, catalog.ErrNoTitle) {
		return nil, &addError{http.StatusNotFound, "not found"}
	}
	if errors.Is(err, errNoFit) {
		return nil, &addError{http.StatusBadRequest, "no version of the policy takes this title: pick versions"}
	}
	if err != nil {
		return nil, err
	}
	if res.Created && w.art != nil {
		if _, err := w.art.Refresh(ctx, res.TitleID); err != nil && !errors.Is(err, art.ErrNoTMDb) {
			w.log.Warn("add: art refresh not queued", "title", res.TitleID, "err", err)
		}
	}
	out := &AddResult{Created: res.Created, Versions: res.Versions, New: res.New}
	if out.New == nil {
		out.New = []string{}
	}
	if req.Search {
		so, err := w.searchNow(ctx, res.TitleID, res.Versions)
		if err != nil {
			return nil, err
		}
		out.Search = so
	}
	t, err := w.loadAdded(ctx, res.TitleID)
	if err != nil || t == nil {
		return nil, fmt.Errorf("add: reading title %d back: %v", res.TitleID, err)
	}
	out.Title = *t
	w.log.Info("title added", "title", res.TitleID, "created", res.Created, "versions", strings.Join(res.Versions, ","),
		"search", req.Search, "source", source)
	return out, nil
}

var errNoFit = errors.New("no version fits")

// searchNow searches the title for the versions catalog.Searchable lets through; the rest are skipped with the reason.
func (w *Web) searchNow(ctx context.Context, titleID int64, keys []string) (*SearchOutcome, error) {
	active, skipped := catalog.Searchable(w.pol, keys)
	out := &SearchOutcome{Versions: active, Skipped: skipped}
	if out.Versions == nil {
		out.Versions = []string{}
	}
	if out.Skipped == nil {
		out.Skipped = []catalog.Skip{}
	}
	switch {
	case len(keys) == 0:
		out.Message = "Nothing to search: the title has no monitored library"
		return out, nil
	case len(active) == 0:
		why := map[string][]string{}
		var order []string
		for _, s := range skipped {
			if _, ok := why[s.Reason]; !ok {
				order = append(order, s.Reason)
			}
			why[s.Reason] = append(why[s.Reason], s.Version)
		}
		var parts []string
		for _, reason := range order {
			parts = append(parts, reason+" ("+strings.Join(why[reason], ", ")+")")
		}
		out.Message = "Nothing searched: " + strings.Join(parts, "; ")
		return out, nil
	case w.add.Acquirer == nil:
		out.Versions = []string{}
		out.Message = "Nothing searched: the acquirer is not running"
		return out, nil
	}
	queued, found, err := w.add.Acquirer.Submit(ctx, 0, acquire.Selection{Versions: active, TitleIDs: []int64{titleID}})
	if err != nil {
		return nil, err
	}
	out.Queued, out.Found, out.Started = queued, found, queued > 0
	if queued == 0 {
		out.Message = "Nothing to search: no monitored item of " + strings.Join(active, ", ")
	} else {
		out.Message = fmt.Sprintf("Searching %d item(s) for %s", queued, strings.Join(active, ", "))
	}
	if len(skipped) > 0 {
		var ks []string
		for _, s := range skipped {
			ks = append(ks, s.Version)
		}
		out.Message += "; not searched: " + strings.Join(ks, ", ")
	}
	w.log.Info("search now", "title", titleID, "versions", strings.Join(active, ","), "queued", queued)
	return out, nil
}

// POST /titles/{id}/search {versions?}: search now for the title's monitored versions (or the ones named), for the
// versions in mode active while router.enabled is true; the answer says why any other was skipped.
func (w *Web) apiSearchNow(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	var req struct {
		Versions []string `json:"versions"`
	}
	if err := decodeJSON(r, &req); err != nil && !errors.Is(err, io.EOF) {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	t, err := w.findTitle(ctx, id, "", 0)
	if err != nil {
		w.internal(rw, err)
		return
	}
	if t == nil {
		notFound(rw)
		return
	}
	if w.pol == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "no policy loaded"})
		return
	}
	on, err := w.versionsOn(ctx, id)
	if err != nil {
		w.internal(rw, err)
		return
	}
	keys := on
	if req.Versions != nil {
		keys = nil
		for _, k := range req.Versions {
			if !slices.Contains(on, k) {
				badRequest(rw, "the title has no monitored version "+strconv.Quote(k))
				return
			}
			keys = append(keys, k)
		}
	}
	out, err := w.searchNow(ctx, id, keys)
	if err != nil {
		w.internal(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, out)
}

// PUT /titles/{id}/versions/{key} {monitored}: turn a version on (created wanted, or restored and monitored) or off
// (unmonitored; its rows, files and links stay). Never searches. 200 with the title.
func (w *Web) apiSetVersion(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	var req struct {
		Monitored *bool `json:"monitored"`
	}
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	if req.Monitored == nil {
		badRequest(rw, "monitored is required")
		return
	}
	t, err := w.findTitle(ctx, id, "", 0)
	if err != nil {
		w.internal(rw, err)
		return
	}
	if t == nil {
		notFound(rw)
		return
	}
	if w.pol == nil {
		writeJSON(rw, http.StatusServiceUnavailable, apiError{Error: "no policy loaded"})
		return
	}
	key := r.PathValue("key")
	if err := w.checkVersions([]string{key}, t.kind); err != nil {
		badRequest(rw, err.Error())
		return
	}
	// A version an arr still imports gets its monitoring from the arr every 15 minutes (manage.go).
	if m, err := w.versionManagedBy(ctx, id, key); err != nil {
		w.internal(rw, err)
		return
	} else if m != "" {
		writeJSON(rw, http.StatusConflict, apiError{Error: managedReason(m)})
		return
	}
	now := w.now()
	err = w.st.WriteTx(ctx, func(tx *sql.Tx) error {
		if *req.Monitored {
			created, err := catalog.TurnOn(ctx, tx, id, []string{key}, true, now)
			if err != nil {
				return err
			}
			if len(created) > 0 {
				w.log.Info("version turned on", "source", "ui", "title", id, "version", key)
			}
		} else if _, err := tx.ExecContext(ctx, `UPDATE version SET monitored = 0, updated_at = ? WHERE title_id = ?
			AND policy_key = ? AND monitored <> 0`, store.Millis(now), id, key); err != nil {
			return err
		}
		return catalog.SyncMonitored(ctx, tx, id, now)
	})
	if err != nil {
		w.internal(rw, err)
		return
	}
	out, err := w.loadAdded(ctx, id)
	if err != nil || out == nil {
		w.internal(rw, fmt.Errorf("version: reading title %d back: %v", id, err))
		return
	}
	writeJSON(rw, http.StatusOK, out)
}
