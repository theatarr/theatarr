// Package web is the native API (/api/theatarr/v1, docs/design/web.md) and the server-rendered pages, served by
// internal/server.
//
// # Access
//
// Every route is registered through one function that takes an access level, and the default is "signed in": a
// route is public only when it says so. The public routes are the login page and form, the first-account status and
// create (auth/first-account; the create works only while no account exists), the static assets, and (in
// internal/server) /healthz, /readyz and /metrics, whose policy is [MetricsGuard]. API routes answer 401 JSON to an
// unauthenticated request; UI routes redirect to /login. API key management and the auth endpoints accept sessions
// only, so a client key cannot mint more keys. State-changing requests on a session need a CSRF token (see
// internal/auth).
//
// # Read model
//
// Everything is read-only for now apart from login, logout, API keys, the anime metadata review (accept or reject
// an item, anime.go), journal verdicts (journal.go), the recovery actions
// (ops.go: grab, remove or retry a download, unblock, remove a copy or title, unlink a file), and adds, version toggles and search now (add.go, through
// internal/catalog; searches only for active versions with the router on), and the admin settings API (settingsapi.go:
// indexers, providers and budgets through the settings coordinator, and bounded source tests). Queries run on the store's reader pool.
// Budgets and back-offs are read from the gateway's write-through tables (migration 00002); the egress state comes
// from the running gateway. The UI makes no outbound requests: no CDN, every asset is embedded, and the
// Content-Security-Policy allows only this origin.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/health"
	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/journal"
	"github.com/theatarr/theatarr/internal/logring"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/webui"
)

// APIBase is the native API's path prefix.
const APIBase = "/api/theatarr/v1"

// EgressSource is the part of the gateway the web layer reads.
type EgressSource interface {
	EgressSummary() map[string]any
	// State is the egress, budget and back-off snapshot for the dashboard and /gateway.
	State() gateway.State
}

// Config is what New needs.
type Config struct {
	Backups Backups
	Restart func() // Cancels the serve context, just like SIGTERM.
	Store   *store.Store
	Auth    *auth.Service
	Policy  *policy.Policy // the policy the service runs with
	// PolicyDir is re-read by the settings page's validate; default Policy.Dir.
	PolicyDir string
	Egress    EgressSource // nil: no gateway (tests)
	Logger    *slog.Logger
	Version   string
	Now       func() time.Time
	// PollInterval is how often the event stream looks for job and egress changes. Default 2s.
	PollInterval time.Duration
	// Devices serves POST /decide and /import (arrview.Service); nil answers 503.
	Devices Devices
	// UI is the single-page app bundle; nil is the embedded one (webui.Bundle).
	UI fs.FS
	// Logs is the recent log lines for GET /logs; nil answers with none.
	Logs     *logring.Ring
	LogLevel LogLevelControl
	LogDir   string
	// Art serves /img and POST titles/{id}/refresh (art.Service); nil answers 404 and 503.
	Art ArtService
	// Journal serves /journal (internal/journal); nil builds one on Store and Policy that records verdicts but
	// queues no re-search.
	Journal *journal.Journal
	// Add serves search, add, search now and the releases panel (add.go); its parts may be nil.
	Add AddConfig
	// Perms gives a caller's permissions (perms.go); nil is the account's own (its role, extra bits and auto-approve).
	Perms PermsFunc
	// Plex runs Plex sign-in (plexauth.Service); nil turns it off (the login page shows the local form only).
	Plex PlexLogin
	// OnRequest, when set, hears every request change (created, approved, declined, canceled, status moved); the
	// hook queues notifications in internal/notify. It runs after the change is stored.
	OnRequest func(RequestEvent)
	// Jobs is the queue the workers run, so a retry wakes them at once; nil builds one on Store (workers then find
	// retries on their next poll).
	Jobs *jobs.Queue
	// Health serves GET /health and the health summary in /status (internal/health.Checker); nil answers with no
	// checks.
	Health HealthChecker
	// Ops runs the recovery actions that need the router or the library (ops.go: grab, retry, unlink); the arr
	// views' service in theatarr serve. nil answers those 503.
	Ops Ops
	// ReloadSettings publishes a prepared settings generation after an admin save; the settings handler owns the route.
	ReloadSettings func(context.Context, int64) error
	// CaptureRuntime pins each request to its settings generation before handlers call dependent services.
	CaptureRuntime func(context.Context) context.Context
	// Settings is the settings coordinator behind /settings (settingsapi.go); nil means theatarr serve runs with
	// Without a coordinator, settings requests report that the service is unavailable.
	Settings    SettingsAdmin
	QualitySync *settings.QualitySync
	// TrashRoot is the local TRaSH guides tree (THEATARR_TRASH) the profile pages read presets, groups and TRaSH's scores
	// from, in either settings source; empty falls back to QualitySync's.
	TrashRoot string
	// SettingsTester runs the bounded outbound source tests; nil answers them 503.
	SettingsTester SettingsTester
	// Cardigann is the definition catalog behind /cardigann (cardigann.go); nil answers 503 (cardigann.definitions
	// is unset).
	Cardigann CardigannCatalog
	// NotifyTest sends a saved notification target's test message now (notify.Notifier.SendTest); nil answers that
	// the notifier is not running.
	NotifyTest func(ctx context.Context, target string) error
	// SettingsTests bounds those tests. serve shares one value across runtime generations, so the concurrency cap
	// and cooldowns survive a save; nil builds one with the defaults.
	SettingsTests *SettingsTestLimits
	// Preflight are the install checks that need the running service (setup.go); its fields may be nil.
	Preflight PreflightHooks
	// SettingsLookup resolves *_env names for an explicit YAML import (os.LookupEnv in serve); nil refuses imports
	// that need a credential.
	SettingsLookup settings.LookupSecret
	// Torrents is the provider torrent manager (docs/design/torrents.md); nil answers 503.
	Torrents TorrentsSource
	// ProviderCopies is the request's router for "Add to other providers" (providercopies.go); nil, or a nil answer,
	// answers 503.
	ProviderCopies func(context.Context) Copier
	// NowPlaying is the now playing tracker's state (docs/design/now-playing.md); nil, or a nil answer, shows it off.
	NowPlaying        NowPlayingSource
	RcloneTestGateway RcloneTestGateway
	// AnimeBuilds is the request's anime franchise builder (animebuild.go); nil, or a nil answer, answers the build
	// endpoints 503.
	AnimeBuilds func(context.Context) AnimeBuilder
	// ArrImport runs the one-time import from Sonarr/Radarr (arrimport.go); nil answers 503.
	ArrImport ArrImporter
}

// HealthChecker is the service's health checks (internal/health.Checker).
type HealthChecker interface {
	Results(ctx context.Context) []health.Result
	Refresh()
}

// Web holds the handlers.
type Web struct {
	mux               *http.ServeMux
	backups           Backups
	restart           func()
	st                *store.Store
	auth              *auth.Service
	pol               *policy.Policy
	policyDir         string
	egress            EgressSource
	log               *slog.Logger
	version           string
	now               func() time.Time
	poll              time.Duration
	hub               *hub
	devices           Devices
	ui                fs.FS
	logs              *logring.Ring
	logMu             sync.Mutex
	logLevel          LogLevelControl
	logDir            string
	reloadSettings    func(context.Context, int64) error
	captureRuntime    func(context.Context) context.Context
	settings          SettingsAdmin
	qualitySync       *settings.QualitySync
	trashRoot         string
	trashCache        trashCache
	settingsTester    SettingsTester
	cardigann         CardigannCatalog
	settingsTests     *SettingsTestLimits
	settingsLookup    settings.LookupSecret
	preflight         PreflightHooks
	lastPreflight     preflightMemory
	art               ArtService
	journal           *journal.Journal
	add               AddConfig
	perms             PermsFunc
	jobs              *jobs.Queue
	plex              PlexLogin
	health            HealthChecker
	ops               Ops
	live              liveCache
	onRequest         func(RequestEvent)
	torrents          TorrentsSource
	copies            func(context.Context) Copier
	nowPlaying        NowPlayingSource
	rcloneTestGateway RcloneTestGateway
	animeBuilds       func(context.Context) AnimeBuilder
	notifyTest        func(ctx context.Context, target string) error
	arrImport         ArrImporter
	arrImportRuns     arrImportRuns

	// polls counts the event poller's completed polls with a listener, the baseline included (tests wait on it).
	polls atomic.Int64
	// activityPolls is the same for the activity poller (activity_events.go).
	activityPolls atomic.Int64

	mu     sync.Mutex
	routes []Route
}

// New builds the web layer. Call Mount to add its routes and Run for the event stream.
func New(cfg Config) (*Web, error) {
	if cfg.Store == nil || cfg.Auth == nil {
		return nil, errors.New("web: store and auth are required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Second
	}
	if cfg.PolicyDir == "" && cfg.Policy != nil {
		cfg.PolicyDir = cfg.Policy.Dir
	}
	if cfg.UI == nil {
		cfg.UI = webui.Bundle()
	}
	if cfg.Perms == nil {
		cfg.Perms = identityPerms
	}
	if cfg.Journal == nil {
		cfg.Journal = journal.New(journal.Config{Store: cfg.Store, Policy: cfg.Policy, Logger: cfg.Logger, Now: cfg.Now})
	}
	if cfg.Jobs == nil {
		cfg.Jobs = jobs.NewQueue(cfg.Store)
	}
	if cfg.SettingsTests == nil {
		cfg.SettingsTests = &SettingsTestLimits{}
	}
	cfg.SettingsTests.init()
	return &Web{st: cfg.Store, jobs: cfg.Jobs, auth: cfg.Auth, pol: cfg.Policy, policyDir: cfg.PolicyDir, egress: cfg.Egress,
		log: cfg.Logger.With("component", "web"), version: cfg.Version, now: cfg.Now, poll: cfg.PollInterval,
		hub: newHub(), devices: cfg.Devices, art: cfg.Art, journal: cfg.Journal, ui: cfg.UI, logs: cfg.Logs, add: cfg.Add,
		perms: cfg.Perms, plex: cfg.Plex, health: cfg.Health, ops: cfg.Ops, onRequest: cfg.OnRequest,
		logLevel: cfg.LogLevel, logDir: cfg.LogDir, reloadSettings: cfg.ReloadSettings,
		captureRuntime: cfg.CaptureRuntime, settings: cfg.Settings, settingsTester: cfg.SettingsTester, qualitySync: cfg.QualitySync, trashRoot: cfg.TrashRoot,
		settingsTests: cfg.SettingsTests, settingsLookup: cfg.SettingsLookup, cardigann: cfg.Cardigann, preflight: cfg.Preflight,
		backups: cfg.Backups, restart: cfg.Restart, torrents: cfg.Torrents, copies: cfg.ProviderCopies, nowPlaying: cfg.NowPlaying, rcloneTestGateway: cfg.RcloneTestGateway, animeBuilds: cfg.AnimeBuilds,
		notifyTest: cfg.NotifyTest, arrImport: cfg.ArrImport}, nil
}

// LogLevelControl changes the process threshold for every slog handler.
type LogLevelControl interface {
	Level() string
	Set(string) error
}

// Access is who may call a route.
type Access int

const (
	// SignedIn needs a session or an API key. The default.
	SignedIn Access = iota
	// SessionOnly needs a browser session; API keys get 403.
	SessionOnly
	// Public needs nothing.
	Public
)

func (a Access) String() string {
	switch a {
	case SessionOnly:
		return "session"
	case Public:
		return "public"
	}
	return "signed-in"
}

// Route is a registered route (for tests and docs).
type Route struct {
	Pattern string
	Access  Access
	// Perm is the permission the route needs (none for public routes).
	Perm auth.Perm
	API  bool
}

// Routes lists every route Mount registered.
func (w *Web) Routes() []Route {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Route(nil), w.routes...)
}

// Mount registers the API and UI routes on mux.
func (w *Web) Mount(mux *http.ServeMux) {
	w.mux = mux // api_request (MCP) dispatches through it
	w.mountAPI(mux)
	w.mountPageRedirects(mux)
	w.mountSPA(mux)
	w.mountArt(mux)
	w.mountAddAPI(mux)
	w.mountManageAPI(mux)
	w.mountOpsAPI(mux)
	w.mountSettingsAPI(mux)
	w.mountCardigannAPI(mux)
	w.mountArrImportAPI(mux)
	w.mountTorrentsAPI(mux)
	w.mountProviderCopiesAPI(mux)
	w.mountBackupsAPI(mux)
	w.mountNowPlayingAPI(mux)
	w.mountSetupAPI(mux)
	w.mountMCP(mux)
	w.mountCollectionsAPI(mux)
	w.mountWantedAPI(mux)
}

// handle registers a route with who may call it: acc (public, signed in, session only) and, unless public, the
// permission it needs. A signed-in route without a permission is a programming error.
func (w *Web) handle(mux *http.ServeMux, pattern string, acc Access, need auth.Perm, h http.HandlerFunc) {
	if (acc == Public) != (need == 0) {
		panic("web: route " + pattern + ": a public route needs no permission, every other route one")
	}
	_, path, _ := strings.Cut(pattern, " ")
	api := strings.HasPrefix(path, APIBase)
	w.mu.Lock()
	w.routes = append(w.routes, Route{Pattern: pattern, Access: acc, Perm: need, API: api})
	w.mu.Unlock()
	guarded := w.guard(api, acc, need, h)
	mux.Handle(pattern, guarded)
}

// guard authenticates, authorises and checks CSRF before h runs.
func (w *Web) guard(api bool, acc Access, need auth.Perm, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if w.captureRuntime != nil {
			r = r.WithContext(w.captureRuntime(r.Context()))
		}
		securityHeaders(rw, api)
		id, err := w.auth.Authenticate(r)
		if err != nil {
			w.log.Error("authenticate", "err", err)
			w.fail(rw, r, api, http.StatusInternalServerError, "internal error")
			return
		}
		if id != nil {
			ctx := auth.WithIdentity(r.Context(), id)
			if keyMayConfirm(id) {
				// A user's API key (and MCP on one) confirms like a session: its tokens are bound to the key. Route
				// permissions still decide what it reaches.
				ctx = settings.WithKeyConfirmer(ctx, id.KeyID, "api key "+id.KeyName)
			} else if !id.Session() {
				// No other identity confirms risky settings changes.
				ctx = settings.WithoutConfirmation(ctx)
			} else {
				// A confirmation token is issued to this signed-in user and refused to anyone else.
				ctx = settings.WithConfirmer(ctx, "user:"+strconv.FormatInt(id.UserID, 10))
			}
			r = r.WithContext(ctx)
		}
		if acc != Public {
			if id == nil {
				if api {
					rw.Header().Set("WWW-Authenticate", `ApiKey header="X-Api-Key"`)
					writeJSON(rw, http.StatusUnauthorized, apiError{Error: "authentication required"})
					return
				}
				http.Redirect(rw, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
				return
			}
			if acc == SessionOnly && !id.Session() {
				w.fail(rw, r, api, http.StatusForbidden, "this endpoint needs a browser session, not an API key")
				return
			}
			if !w.perms(id).Has(need) {
				w.fail(rw, r, api, http.StatusForbidden, "your account may not do this ("+need.String()+")")
				return
			}
		}
		if !safeMethod(r.Method) {
			if api && acc == Public {
				// API login: no session yet and no form. It takes JSON only, which a cross-site form cannot send
				// without a CORS preflight, and a foreign Origin is refused.
				if !jsonRequest(r) || !sameOrigin(r) {
					w.fail(rw, r, api, http.StatusForbidden, "want a same-origin application/json request")
					return
				}
			} else if (id.Session() || acc == Public) && !w.auth.CheckCSRF(r) {
				w.fail(rw, r, api, http.StatusForbidden, "missing or invalid CSRF token")
				return
			}
		}
		h(rw, r)
	})
}

// keyMayConfirm reports whether id is a user-owned API key (the guard authenticates no other key kind; arr-view keys
// are checked by the views themselves and are left as they are).
func keyMayConfirm(id *auth.Identity) bool {
	return id != nil && id.Kind == "apikey" && id.UserID > 0
}

func jsonRequest(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	return ct == "application/json" || strings.HasPrefix(ct, "application/json;")
}

func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func securityHeaders(rw http.ResponseWriter, api bool) {
	h := rw.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; "+
		"connect-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'")
	if api {
		h.Set("Cache-Control", "no-store")
	}
}

type apiError struct {
	Error  string   `json:"error"`
	Detail []string `json:"detail,omitempty"`
}

func writeJSON(rw http.ResponseWriter, code int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(code)
	enc := json.NewEncoder(rw)
	enc.SetEscapeHTML(true)
	enc.Encode(v)
}

func (w *Web) fail(rw http.ResponseWriter, r *http.Request, api bool, code int, msg string) {
	if api {
		writeJSON(rw, code, apiError{Error: msg})
		return
	}
	http.Error(rw, msg, code)
}

// MetricsPolicy is who may read /metrics.
type MetricsPolicy string

const (
	// MetricsOpen serves anyone who can reach the port (as before auth existed): Prometheus scrapes without a key.
	MetricsOpen MetricsPolicy = "open"
	// MetricsLAN serves loopback and private addresses (RFC 1918, RFC 4193, link-local) only.
	MetricsLAN MetricsPolicy = "lan"
	// MetricsKey needs an API key or a session, like the API.
	MetricsKey MetricsPolicy = "key"
)

// ParseMetricsPolicy parses open, lan or key.
func ParseMetricsPolicy(s string) (MetricsPolicy, error) {
	switch p := MetricsPolicy(s); p {
	case MetricsOpen, MetricsLAN, MetricsKey:
		return p, nil
	}
	return "", errors.New("metrics policy " + s + ": want open, lan or key")
}

// MetricsGuard wraps the /metrics handler with the policy.
func MetricsGuard(p MetricsPolicy, a *auth.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		switch p {
		case MetricsOpen:
			return next
		case MetricsLAN:
			return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				if !lanAddr(auth.ClientAddr(r)) {
					http.Error(rw, "forbidden", http.StatusForbidden)
					return
				}
				next.ServeHTTP(rw, r)
			})
		}
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			id, err := a.Authenticate(r)
			if err != nil || id == nil {
				rw.Header().Set("WWW-Authenticate", `ApiKey header="X-Api-Key"`)
				http.Error(rw, "authentication required", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(rw, r)
		})
	}
}

func lanAddr(s string) bool {
	a, err := netip.ParseAddr(s)
	if err != nil {
		return false
	}
	a = a.Unmap()
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast()
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Run feeds the event stream until ctx ends. It polls only while a client listens.
func (w *Web) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Go(func() { w.runActivityEvents(ctx) })
	wg.Go(func() { w.runRequestSync(ctx) })
	w.runEvents(ctx)
	wg.Wait()
}
