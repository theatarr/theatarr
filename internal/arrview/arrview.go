package arrview

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/acquire"
	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/decide"
	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/metadata/tmdb"
	"github.com/theatarr/theatarr/internal/metadata/tvrefresh"
	"github.com/theatarr/theatarr/internal/notify"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/router"
	"github.com/theatarr/theatarr/internal/store"
)

// The versions the views claim to be: the arr releases our ports and imports are based on (docs/dev.md). Seerr parses
// the major (Sonarr 4 has no language profiles); Prowlarr and decypharr check the version and appName.
const (
	RadarrVersion = "6.4.4.10685"
	SonarrVersion = "4.0.20.3014"
)

// IDBase is added to a title id to make the movie or series id a view answers. Ids below it are the replaced arr
// instances' ids, answered through the alias table (docs/migration.md); see the package doc.
const IDBase = 1_000_000

// Config is what New needs.
type Config struct {
	// ReleaseCache is shared by runtime generations; nil creates a private cache.
	ReleaseCache *ReleaseCache
	Store        *store.Store
	Auth         *auth.Service
	Policy       *policy.Policy
	// Engine is the immutable decision snapshot used by qualityprofile/customformat reads.
	Engine *decide.Engine
	Logger *slog.Logger
	Now    func() time.Time
	// CaptureRuntime pins each request to the settings generation active when it entered the view.
	CaptureRuntime func(context.Context) context.Context
	// Getenv reads the views' own keys (policy views[*].api_key_env). Default os.Getenv.
	Getenv func(string) string
	// Started is reported as startTime. Default: when New ran.
	Started time.Time
	// Searcher runs the search commands of active versions (internal/acquire). nil leaves them queued.
	Searcher Searcher
	// RSSSync queues an immediate shadow RSS sync for the arr RssSync command. nil reports that no sync is configured.
	RSSSync func(context.Context) error
	// Decider searches and decides for the decision endpoints (release, release/push, parse, manualimport);
	// acquire.Acquirer in theatarr serve. nil: those endpoints answer without decisions where they can, else 503.
	Decider Decider
	// Cutoff judges the quality cutoff for GET wanted/cutoff (acquire.Acquirer in theatarr serve). nil: every filled item
	// is listed (the full cutoff).
	Cutoff Cutoff
	// Router takes grabs (POST release, release/push); router.Router in theatarr serve, which writes only for active
	// versions while router.enabled is true. nil: grabs are refused.
	Router acquire.Enqueuer
	// Library unlinks files and imports (DELETE moviefile/episodefile, ManualImport) through its gate. nil:
	// refused.
	Library Library
	// Jobs runs maintenance commands; Metadata queues the same refresh as the native title API.
	Jobs     *jobs.Queue
	Metadata Metadata
	// SeriesLookup reads uncatalogued series through the configured metadata gateway clients. nil: local only.
	SeriesLookup SeriesLookup
	// TMDb answers movie lookups by name for movies not in the catalog, and the anime guess of new titles
	// (*tmdb.Client through the gateway). nil: catalog lookups only, and adds decide anime from Theatarr's mapping data.
	TMDb TMDb
	// Notifier receives the events the views cause (deletes, tests) for the webhooks; nil: none.
	Notifier Notifier
}

// SeriesLookup reads metadata for series absent from the catalog (tvrefresh.Service).
type SeriesLookup interface {
	LookupSeries(context.Context, int) (tvrefresh.LookupResult, error)
}

// TMDb is what the views read from TMDb (*tmdb.Client): the title search of movie/lookup, and a new title's
// details for the anime guess.
type TMDb interface {
	SearchMovie(ctx context.Context, query string, year int) ([]tmdb.SearchResult, error)
	Movie(ctx context.Context, id int) (*tmdb.Movie, error)
	FindTVByTVDB(ctx context.Context, tvdbID int) (int, error)
	TVShow(ctx context.Context, tvID int) (*tmdb.TVSeries, error)
}

// Metadata queues a title's metadata refresh (art.Service).
type Metadata interface {
	Refresh(ctx context.Context, titleID int64) (int64, error)
}

// Decider is the decision engine as the views use it (acquire.Acquirer).
type Decider interface {
	Interactive(ctx context.Context, titleID int64, version string, season *int, episodes []int64) (*router.Searched,
		error)
	Decide(ctx context.Context, rel decide.Release, titleID int64, version string) (*decide.Decision, error)
}

// Cutoff is the arrs' Cutoff Unmet judgement: a file whose quality is below the profile's quality cutoff, the
// format score aside (acquire.Acquirer.BelowQualityCutoff).
type Cutoff interface {
	BelowQualityCutoff(ctx context.Context, key string, versionID, titleID int64) (bool, error)
	EpisodesBelowQualityCutoff(ctx context.Context, key string, versionID, titleID int64) (map[int64]bool, error)
}

// Notifier records webhook events (internal/notify); Emit never blocks on a send.
type Notifier interface {
	Emit(ctx context.Context, ev notify.Event)
	// TestNotification sends a Test payload to an installed notification's settings at once (POST
	// notification/test); an error says why it failed.
	TestNotification(ctx context.Context, view string, n json.RawMessage) error
}

// Searcher starts the searches a command asks for; acquire.Acquirer is the one in theatarr serve. It searches only the
// active versions of the selection.
type Searcher interface {
	Submit(ctx context.Context, commandID int64, sel acquire.Selection) (queued, found int, err error)
}

// Service serves every view.
type Service struct {
	st             *store.Store
	auth           *auth.Service
	pol            *policy.Policy
	engine         *decide.Engine
	captureRuntime func(context.Context) context.Context
	log            *slog.Logger
	now            func() time.Time
	started        time.Time
	views          []*View
	searcher       Searcher
	rssSync        func(context.Context) error
	decider        Decider
	cutoff         Cutoff
	router         acquire.Enqueuer
	library        Library
	jobs           *jobs.Queue
	metadata       Metadata
	tvmeta         SeriesLookup
	tmdb           TMDb
	notifier       Notifier
	health         HealthSource
	releases       *ReleaseCache
	keys           map[string]string // view key -> its own API key, when set
}

// New builds the views from the policy and gives their profiles ids (stored, so they stay the same across restarts).
func New(ctx context.Context, cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Auth == nil || cfg.Policy == nil {
		return nil, errors.New("arrview: store, auth and policy are required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Getenv == nil {
		cfg.Getenv = cfg.Policy.Getenv
	}
	if cfg.Started.IsZero() {
		cfg.Started = cfg.Now()
	}
	if cfg.ReleaseCache == nil {
		cfg.ReleaseCache = NewReleaseCache(cfg.Now)
	}
	views, err := BuildViews(cfg.Policy)
	if err != nil {
		return nil, err
	}
	s := &Service{st: cfg.Store, auth: cfg.Auth, pol: cfg.Policy, engine: cfg.Engine, captureRuntime: cfg.CaptureRuntime,
		log: cfg.Logger.With("component", "arrview"),
		now: cfg.Now, started: cfg.Started, views: views, keys: map[string]string{}, searcher: cfg.Searcher,
		decider: cfg.Decider, cutoff: cfg.Cutoff, router: cfg.Router, library: cfg.Library, notifier: cfg.Notifier,
		rssSync: cfg.RSSSync,
		jobs:    cfg.Jobs, metadata: cfg.Metadata, tvmeta: cfg.SeriesLookup, tmdb: cfg.TMDb,
		releases: cfg.ReleaseCache}
	for _, v := range views {
		if v.keyEnv != "" {
			if k := cfg.Getenv(v.keyEnv); k != "" {
				s.keys[v.Key] = k
			}
		}
		for _, p := range v.Profiles {
			if p.ID, err = s.profileID(ctx, v.Key, p.Name); err != nil {
				return nil, err
			}
		}
	}
	return s, nil
}

func (s *Service) profileID(ctx context.Context, view, name string) (int64, error) {
	var id int64
	err := s.st.WriteTx(ctx, func(tx *sqlTx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO arrview_profile (view, name, created_at) VALUES (?, ?, ?)
			ON CONFLICT (view, name) DO NOTHING`, view, name, store.Millis(s.now())); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT id FROM arrview_profile WHERE view = ? AND name = ?`, view, name).Scan(&id)
	})
	return id, err
}

// SetNotifier sets the webhook notifier after New (it needs the service for its payloads).
func (s *Service) SetNotifier(n Notifier) { s.notifier = n }

// HealthSource is the service-wide health checks (internal/health.Checker).
type HealthSource interface {
	Issues(ctx context.Context) []health.Result
}

// SetHealth adds the service-wide health checks to every view's GET health and to the Health webhook.
func (s *Service) SetHealth(h HealthSource) { s.health = h }

// Views lists the views served.
func (s *Service) Views() []*View { return s.views }

// Mount registers one handler per view base path. Routing below the base is the service's own, case-insensitive
// like ASP.NET's (Seerr asks for /qualityProfile).
func (s *Service) Mount(mux *http.ServeMux) {
	for _, v := range s.views {
		mux.Handle(v.Base+"/", s.handler(v))
	}
}

// Handler serves one view at its base path, for tests.
func (s *Service) Handler(key string) http.Handler {
	for _, v := range s.views {
		if v.Key == key {
			return s.handler(v)
		}
	}
	return nil
}

type request struct {
	*http.Request
	v    *View
	seg  []string // lower-cased path segments after /api/v3
	raw  []string // the same, as sent
	vers string   // X-Application-Version
	// client is who the request authenticated as: an API key's name, or "view:" plus the view key for the view's own
	// key (for logs and decided_via).
	client string
}

func (s *Service) handler(v *View) http.Handler {
	version := RadarrVersion
	if v.Flavour == Sonarr {
		version = SonarrVersion
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.captureRuntime != nil {
			r = r.WithContext(s.captureRuntime(r.Context()))
		}
		rest := r.URL.Path[len(v.Base):]
		low := strings.ToLower(rest)
		if low != "/api/v3" && !strings.HasPrefix(low, "/api/v3/") {
			// Not the API: the arrs serve their UI here. Nothing to serve.
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("X-Application-Version", version)
		h.Set("Cache-Control", "no-cache, no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		client, ok, err := s.authorised(r, v)
		if err != nil {
			s.log.Error("authenticate", "view", v.Key, "err", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if !ok {
			// The arr's own answer: ApiKeyAuthenticationHandler.HandleChallengeAsync sets 401 and writes no body.
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		raw := strings.Split(strings.Trim(rest[len("/api/v3"):], "/"), "/")
		if len(raw) == 1 && raw[0] == "" {
			raw = nil
		}
		seg := make([]string, len(raw))
		for i, p := range raw {
			seg[i] = strings.ToLower(p)
		}
		req := &request{Request: r, v: v, seg: seg, raw: raw, vers: version, client: client}
		s.route(w, req)
	})
}

// authorised accepts the view's own key (policy api_key_env) or an active API key from internal/auth whose owner is
// an active admin (docs/design/users.md: a user's or manager's key carries their permissions, which do not include the
// arr views), sent as the arrs take it (ApiKeyAuthenticationHandler.ParseApiKey @ a96bf7c): the apikey query parameter
// first, then the X-Api-Key header, then a bearer token. client names who it is: policy.PushViewPrefix plus the view
// key for the view's own key, else the API key's name.
func (s *Service) authorised(r *http.Request, v *View) (client string, ok bool, err error) {
	key := ""
	if q := r.URL.Query(); q.Has("apikey") {
		key = q.Get("apikey")
	} else if h := r.Header.Get(auth.APIKeyHeader); h != "" {
		key = h
	} else {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", false, nil
	}
	if own := s.keys[v.Key]; own != "" && subtle.ConstantTimeCompare([]byte(own), []byte(key)) == 1 {
		return "view:" + v.Key, true, nil
	}
	id, err := s.auth.CheckAPIKey(r.Context(), key)
	if err != nil || id == nil || id.Role != auth.RoleAdmin {
		return "", false, err
	}
	return id.KeyName, true, nil
}

// route dispatches on method and path. Unknown endpoints get the arrs' bare 404.
func (s *Service) route(w http.ResponseWriter, r *request) {
	fl := r.v.Flavour
	m := r.Method
	if m == http.MethodHead {
		m = http.MethodGet
	}
	seg := r.seg
	at := func(n int, names ...string) bool {
		if len(seg) != n {
			return false
		}
		for i, p := range names {
			if p != "*" && seg[i] != p {
				return false
			}
		}
		return true
	}
	if m == http.MethodGet && (at(1, "history") || at(1, "queue") || at(1, "blocklist") || at(2, "wanted", "missing") || at(2, "wanted", "cutoff")) && !validatePaging(w, r) {
		return
	}
	switch {
	case at(2, "system", "status") && m == "GET":
		s.systemStatus(w, r)
	case at(1, "qualityprofile") && m == "GET":
		s.qualityProfiles(w, r)
	case at(2, "qualityprofile", "*") && m == "GET":
		s.qualityProfile(w, r)
	case at(1, "customformat") && m == "GET":
		s.customFormats(w, r)
	case at(2, "customformat", "*") && m == "GET":
		s.customFormat(w, r)
	case at(1, "rootfolder") && m == "GET":
		s.rootFolders(w, r)
	case (at(2, "tag", "detail") || at(3, "tag", "detail", "*")) && m == "GET":
		s.tagDetails(w, r)
	case at(1, "tag") && m == "GET":
		s.tags(w, r)
	case at(1, "tag") && m == "POST":
		s.createTag(w, r)
	case at(2, "tag", "*") && m == "GET":
		s.tag(w, r)
	case (at(1, "tag") || at(2, "tag", "*")) && m == "PUT":
		s.updateTag(w, r)
	case at(2, "tag", "*") && m == "DELETE":
		s.deleteTag(w, r)
	case at(1, "delayprofile") && m == "GET":
		s.delayProfiles(w, r)
	case at(2, "delayprofile", "*") && m == "GET":
		s.delayProfile(w, r)
	case at(1, "releaseprofile") && m == "GET":
		writeJSON(w, http.StatusOK, []releaseProfileResource{})
	case at(2, "releaseprofile", "*") && m == "GET":
		notFound(w, "ReleaseProfile with ID "+r.raw[1]+" does not exist")
	case (seg0(seg) == "delayprofile" || seg0(seg) == "releaseprofile") && m != "GET":
		s.selectionNotSupported(w, r)
	case at(1, "queue") && m == "GET":
		s.queue(w, r)
	case at(2, "queue", "status") && m == "GET":
		s.queueStatus(w, r)
	case at(2, "queue", "details") && m == "GET":
		s.queueDetails(w, r)
	case at(2, "queue", "*") && m == "DELETE":
		s.deleteQueue(w, r)
	case at(2, "wanted", "missing") && m == "GET":
		s.wantedMissing(w, r)
	case at(2, "history", "since") && m == "GET":
		s.historySince(w, r)
	case at(1, "history") && m == "GET":
		s.history(w, r)
	case (at(2, "history", "movie") && fl == Radarr || at(2, "history", "series") && fl == Sonarr) && m == "GET":
		s.titleHistory(w, r)
	case at(3, "history", "failed", "*") && m == "POST":
		s.historyFailed(w, r)
	case at(1, "blocklist") && m == "GET":
		s.blocklist(w, r)
	case fl == Radarr && at(2, "blocklist", "movie") && m == "GET":
		s.movieBlocklist(w, r)
	case at(2, "blocklist", "*") && m == "DELETE":
		s.deleteBlocklist(w, r)
	case at(1, "release") && m == "GET":
		s.searchReleases(w, r)
	case at(1, "release") && m == "POST":
		s.grabRelease(w, r)
	case at(1, "rename") && m == "GET":
		s.rename(w, r)
	case at(2, "release", "push") && m == "POST":
		s.releasePush(w, r)
	case at(1, "parse") && m == "GET":
		s.parse(w, r)
	case at(1, "manualimport") && m == "GET":
		s.manualImportList(w, r)
	case at(1, "manualimport") && m == "POST":
		s.manualImportReprocess(w, r)
	case at(1, "calendar") && m == "GET":
		s.calendar(w, r)
	case at(2, "wanted", "cutoff") && m == "GET":
		s.wantedCutoff(w, r)
	case (at(1, "indexer") || at(2, "indexer", "*")) && m == "GET":
		s.indexers(w, r)
	case at(2, "notification", "schema") && m == "GET":
		s.notificationSchema(w, r)
	case (at(1, "notification") || at(2, "notification", "*")) && m == "GET":
		s.notifications(w, r)
	case at(2, "notification", "test") && m == "POST":
		s.testNotification(w, r)
	case at(1, "notification") && m == "POST", (at(1, "notification") || at(2, "notification", "*")) && m == "PUT":
		s.saveNotification(w, r)
	case at(2, "notification", "*") && m == "DELETE":
		s.deleteNotification(w, r)
	case fl == Radarr && at(1, "collection") && m == "GET":
		s.collections(w, r)
	case fl == Radarr && at(2, "collection", "*") && (m == "GET" || m == "PUT"):
		s.collection(w, r)
	case fl == Radarr && at(1, "collection") && m == "PUT":
		s.collection(w, r)
	case fl == Radarr && at(2, "importlist", "schema") && m == "GET":
		s.importLists(w, r)
	case fl == Radarr && at(2, "importlist", "movie") && m == "GET":
		s.emptyList(w, r)
	case fl == Radarr && at(1, "importlist") && (m == "GET" || m == "POST" || m == "PUT"):
		s.importLists(w, r)
	case fl == Radarr && at(2, "importlist", "*") && (m == "GET" || m == "PUT" || m == "DELETE"):
		s.importLists(w, r)
	case fl == Radarr && (at(1, "exclusions") && m == "POST" || at(2, "exclusions", "*") && m == "DELETE"):
		s.exclusions(w, r)
	case fl == Radarr && at(1, "exclusions") && m == "GET":
		s.exclusions(w, r)
	case (at(1, "extrafile") || at(1, "credit") || at(1, "exclusions") || at(1, "importlistexclusion") ||
		at(2, "importlist", "movie")) && m == "GET":
		s.emptyList(w, r)
	case at(1, "filesystem") && m == "GET":
		s.filesystem(w, r)
	case at(1, "language") && m == "GET":
		s.languages(w, r)
	case at(1, "qualitydefinition") && m == "GET":
		s.qualityDefinitions(w, r)
	case fl == Sonarr && at(1, "languageprofile") && m == "GET":
		s.languageProfiles(w, r)
	case at(1, "health") && m == "GET":
		s.getHealth(w, r)
	case at(1, "diskspace") && m == "GET":
		s.diskSpace(w, r)
	case at(2, "config", "downloadclient") && m == "GET":
		s.downloadClientConfig(w, r)
	case at(1, "command") && m == "GET":
		s.commands(w, r)
	case at(1, "command") && m == "POST":
		s.postCommand(w, r)
	case at(2, "command", "*") && m == "GET":
		s.command(w, r)
	case (fl == Radarr && at(2, "movie", "editor") || fl == Sonarr && at(2, "series", "editor")) && m == "PUT":
		s.editor(w, r)

	case fl == Radarr && at(1, "movie") && m == "GET":
		s.movies(w, r)
	case fl == Radarr && at(2, "movie", "lookup") && m == "GET":
		s.movieLookup(w, r)
	case fl == Radarr && at(3, "movie", "lookup", "tmdb") && m == "GET":
		s.movieLookupTmdb(w, r)
	case fl == Radarr && at(2, "movie", "*") && m == "GET":
		s.movie(w, r)
	case fl == Radarr && at(1, "movie") && m == "POST":
		s.addTitle(w, r)
	case fl == Radarr && (at(1, "movie") || at(2, "movie", "*")) && m == "PUT":
		s.updateTitle(w, r)
	case fl == Radarr && at(2, "movie", "*") && m == "DELETE":
		s.deleteTitle(w, r)
	case fl == Radarr && (at(1, "moviefile") || at(2, "moviefile", "*")) && m == "GET":
		s.movieFiles(w, r)
	case fl == Radarr && at(2, "moviefile", "*") && m == "DELETE":
		s.deleteFiles(w, r)

	case fl == Sonarr && at(1, "series") && m == "GET":
		s.seriesList(w, r)
	case fl == Sonarr && at(2, "series", "lookup") && m == "GET":
		s.seriesLookup(w, r)
	case fl == Sonarr && at(2, "series", "*") && m == "GET":
		s.series(w, r)
	case fl == Sonarr && at(1, "series") && m == "POST":
		s.addTitle(w, r)
	case fl == Sonarr && (at(1, "series") || at(2, "series", "*")) && m == "PUT":
		s.updateTitle(w, r)
	case fl == Sonarr && at(2, "series", "*") && m == "DELETE":
		s.deleteTitle(w, r)
	case fl == Sonarr && at(1, "episode") && m == "GET":
		s.episodes(w, r)
	case fl == Sonarr && at(2, "episode", "*") && m == "GET":
		s.episode(w, r)
	case fl == Sonarr && at(2, "episode", "monitor") && m == "PUT":
		s.monitorEpisodes(w, r)
	case fl == Sonarr && (at(1, "episodefile") || at(2, "episodefile", "*")) && m == "GET":
		s.episodeFiles(w, r)
	case fl == Sonarr && at(2, "episodefile", "*") && m == "DELETE":
		s.deleteFiles(w, r)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// writeJSON writes v as the arrs do: camelCase names (set on the types), nulls left out (omitempty on the types).
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// validationError is one entry of the arrs' 400 body (FluentValidation's ValidationFailure).
type validationError struct {
	PropertyName   string `json:"propertyName"`
	ErrorMessage   string `json:"errorMessage"`
	AttemptedValue any    `json:"attemptedValue,omitempty"`
	Severity       string `json:"severity"`
	ErrorCode      string `json:"errorCode,omitempty"`
}

func badRequest(w http.ResponseWriter, prop, msg string, attempted any) {
	writeJSON(w, http.StatusBadRequest, []validationError{{PropertyName: prop, ErrorMessage: msg,
		AttemptedValue: attempted, Severity: "error"}})
}

// errorBody is the arrs' error JSON for exceptions mapped to a status (NzbDroneErrorPipeline): {message, description}.
type errorBody struct {
	Message     string `json:"message"`
	Description string `json:"description,omitempty"`
}

func notFound(w http.ResponseWriter, what string) {
	writeJSON(w, http.StatusNotFound, errorBody{Message: what})
}

// internal answers 500, except a link that a concurrent rename, relink or refresh changed: that is a 409, and the
// client can retry.
func (s *Service) internal(w http.ResponseWriter, r *request, err error) {
	if errors.Is(err, library.ErrLinkChanged) {
		s.log.Info("request lost a race on a link", "view", r.v.Key, "path", r.URL.Path, "err", err)
		writeJSON(w, http.StatusConflict, errorBody{Message: "the file changed while it was being removed; try again"})
		return
	}
	s.log.Error("request failed", "view", r.v.Key, "path", r.URL.Path, "err", err)
	writeJSON(w, http.StatusInternalServerError, errorBody{Message: "internal error"})
}

func decodeBody(r *request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

// pathID parses the id segment at i.
func (r *request) pathID(i int) (int64, bool) {
	if i >= len(r.seg) {
		return 0, false
	}
	n, err := strconv.ParseInt(r.seg[i], 10, 64)
	return n, err == nil && n > 0
}

// queryInt reads an int query parameter, case-insensitively as ASP.NET binds them.
func (r *request) query(name string) (string, bool) {
	for k, vs := range r.URL.Query() {
		if strings.EqualFold(k, name) && len(vs) > 0 {
			return vs[0], true
		}
	}
	return "", false
}

func (r *request) queryInt(name string, def int) int {
	if s, ok := r.query(name); ok {
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
	}
	return def
}

func (r *request) queryBool(name string, def bool) bool {
	if s, ok := r.query(name); ok {
		if b, err := strconv.ParseBool(s); err == nil {
			return b
		}
	}
	return def
}

// queryInts reads a repeated or comma-separated int parameter (movieIds=1&movieIds=2 or movieIds=1,2).
func (r *request) queryInts(name string) []int64 {
	var out []int64
	for k, vs := range r.URL.Query() {
		if !strings.EqualFold(k, name) {
			continue
		}
		for _, v := range vs {
			for _, p := range strings.Split(v, ",") {
				if n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil {
					out = append(out, n)
				}
			}
		}
	}
	return out
}
