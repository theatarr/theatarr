// Port of Mediarium internal/indexers/cardigann_engine.go @ f7d1e26 (github.com/rdborg/Mediarium, AGPL-3.0,
// Copyright (C) Ryan Borg and the Mediarium contributors). Changes are listed in the package doc.

package cardigann

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/html/charset"
	"golang.org/x/net/publicsuffix"
)

// Manager runs definition-based ("Cardigann") indexers: sites without a Torznab API, driven by a community
// definition that says how to log in, search, scrape the results and fetch a download.
//
// It keeps one session per configured indexer (cookie jar, login state, polite delay, Cloudflare clearance), so
// logging in happens once, not on every search. It sends nothing itself: every request goes through the
// instance's Transport (the gateway), one hop at a time.
type Manager struct {
	// Definitions loads a definition by id.
	Definitions Definitions
	// FlareSolverrURL returns the FlareSolverr-compatible (trawl) address, or "" when none is set up.
	FlareSolverrURL func() string
	// Sessions persists cookie jars between restarts; nil keeps them in memory only.
	Sessions SessionStore
	// DefaultDelay is the polite minimum gap between two requests to the same site, used when the definition's
	// own requestDelay is smaller.
	DefaultDelay time.Duration
	// UserAgent is sent to sites unless FlareSolverr supplied its own.
	UserAgent string
	Logger    *slog.Logger

	mu       sync.Mutex
	sessions map[string]*cgSession
}

// Definitions loads definitions. Hash identifies the loaded text, so a changed definition starts a new session.
type Definitions interface {
	Load(ctx context.Context, id string) (def *Definition, hash string, err error)
}

// Instance is one configured indexer: its definition, site address and settings.
type Instance struct {
	// Key is the indexer's policy key; Name is shown in errors (Key when empty).
	Key          string
	Name         string
	DefinitionID string
	// BaseURL is the site address (one of the definition's links, or the person's own mirror).
	BaseURL string
	// Settings are the definition's settings, secrets included.
	Settings map[string]string
	// Hosts are further hosts the definition may reach besides the site (an API host); the gateway's allowlist
	// holds them too.
	Hosts []string
	// Transport sends one request for this indexer without following redirects (the gateway). Required.
	Transport http.RoundTripper
	// Solver sends the FlareSolverr calls of this indexer (the gateway). Nil: Cloudflare checks cannot be passed.
	Solver http.RoundTripper
}

func (inst Instance) name() string {
	if inst.Name != "" {
		return inst.Name
	}
	return inst.Key
}

// DefaultPoliteDelay is the minimum gap between requests to one site.
const DefaultPoliteDelay = time.Second

// cardigannTimeout bounds one whole search (login, several paths, polite delays) against a definition-based site.
const cardigannTimeout = 60 * time.Second

// cardigannFlareSolverrTimeout is the same limit for a site that may need FlareSolverr: passing a Cloudflare check
// alone can take a minute.
const cardigannFlareSolverrTimeout = 150 * time.Second

// SearchBudget is how long one whole search of a site may take.
func (m *Manager) SearchBudget() time.Duration {
	if m != nil && m.FlareSolverrURL != nil && strings.TrimSpace(m.FlareSolverrURL()) != "" {
		return cardigannFlareSolverrTimeout
	}
	return cardigannTimeout
}

const defaultUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"

// ErrCloudflare is returned when a site shows a Cloudflare browser check and no FlareSolverr is configured to pass
// it.
var ErrCloudflare = errors.New("the site is behind a Cloudflare check; set cardigann.flaresolverr_url (trawl) to use it")

// NewManager returns a manager reading definitions from defs.
func NewManager(defs Definitions, flareSolverrURL func() string) *Manager {
	return &Manager{
		Definitions:     defs,
		FlareSolverrURL: flareSolverrURL,
		DefaultDelay:    DefaultPoliteDelay,
		UserAgent:       defaultUserAgent,
		sessions:        map[string]*cgSession{},
	}
}

func (m *Manager) log() *slog.Logger {
	if m.Logger != nil {
		return m.Logger
	}
	return slog.Default()
}

// Forget drops an indexer's session (cookies, login), e.g. after its settings changed or it was deleted.
func (m *Manager) Forget(key string) {
	m.mu.Lock()
	delete(m.sessions, key)
	m.mu.Unlock()
}

// Search runs a keyword search on a definition-based indexer.
func (m *Manager) Search(ctx context.Context, inst Instance, query string, categories []int) ([]Result, error) {
	sess, err := m.session(ctx, inst)
	if err != nil {
		return nil, err
	}
	defer sess.unlock()
	res, err := sess.search(ctx, query, categories)
	return res, sess.clean(err)
}

// TestResult is what a test found: how many releases came back, and whether FlareSolverr had to pass a Cloudflare
// check on the way.
type TestResult struct {
	Releases        int
	ViaFlareSolverr bool
}

// Test signs in if needed (running the definition's login test) and runs a search without keywords, falling back to
// the keyword "test".
func (m *Manager) Test(ctx context.Context, inst Instance) (res TestResult, err error) {
	sess, err := m.session(ctx, inst)
	if err != nil {
		return TestResult{}, err
	}
	defer sess.unlock()
	defer func() { err = sess.clean(err) }()
	// A signed-in session is checked by the search itself (a lost session signs in again); changed settings always
	// start a new session.
	cats := []int{2000, 5000}
	results, err := sess.search(ctx, "", cats)
	if err != nil {
		return TestResult{}, err
	}
	if len(results) == 0 {
		results, err = sess.search(ctx, "test", cats)
		if err != nil {
			return TestResult{}, err
		}
	}
	via := sess.viaFlareSolverr.Load()
	return TestResult{Releases: len(results), ViaFlareSolverr: via}, nil
}

// Download resolves a result link (Result.Link) into a magnet link or the bytes of a .torrent file, logging in and
// following the definition's download rules as needed.
func (m *Manager) Download(ctx context.Context, inst Instance, link string) (*Download, error) {
	sess, err := m.session(ctx, inst)
	if err != nil {
		return nil, err
	}
	defer sess.unlock()
	d, err := sess.download(ctx, link)
	return d, sess.clean(err)
}

// cleanError is an engine error with the indexer's credentials masked; Unwrap keeps errors.Is working.
type cleanedError struct {
	msg string
	err error
}

func (e *cleanedError) Error() string { return e.msg }
func (e *cleanedError) Unwrap() error { return e.err }

// clean masks the session's secret settings (as given and URL-encoded) in err. Engine errors can quote what a site
// sent (its error message, a login failure) or a URL built from a setting, and they are returned to the API and
// logged.
func (s *cgSession) clean(err error) error {
	if err == nil {
		return nil
	}
	var secrets []string
	types := map[string]string{}
	for _, st := range s.def.Settings {
		types[st.Name] = st.Type
	}
	for k, v := range s.settings {
		if len(v) < 4 || !SecretSetting(types[k], k) {
			continue
		}
		secrets = append(secrets, v, url.QueryEscape(v), url.PathEscape(v))
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	msg := err.Error()
	for _, v := range secrets {
		msg = strings.ReplaceAll(msg, v, "***")
	}
	if msg == err.Error() {
		return err
	}
	return &cleanedError{msg: msg, err: err}
}

// Download is a resolved release: either a magnet link or file contents.
type Download struct {
	Magnet string
	Data   []byte
}

// session returns with the flow lock held; callers release it after cleaning the operation's errors.
func (m *Manager) session(ctx context.Context, inst Instance) (*cgSession, error) {
	if m.Definitions == nil {
		return nil, fmt.Errorf("%s: the definition catalog is not available", inst.name())
	}
	if inst.Transport == nil {
		return nil, fmt.Errorf("%s: no transport", inst.name())
	}
	def, hash, err := m.Definitions.Load(ctx, inst.DefinitionID)
	if err != nil {
		return nil, err
	}
	key := sessionKey(hash, inst)
	m.mu.Lock()
	if m.sessions == nil {
		m.sessions = map[string]*cgSession{}
	}
	s := m.sessions[inst.Key]
	if s == nil || s.key != key || inst.Key == "" {
		s, err = newSession(m, def, inst)
		if err != nil {
			m.mu.Unlock()
			return nil, err
		}
		s.key = key
		s.restore(ctx)
		if inst.Key != "" {
			m.sessions[inst.Key] = s
		}
	}
	m.mu.Unlock()
	// Bind the caller's generation only after taking the flow lock. A queued or canceled caller cannot change
	// the gateway used by an in-progress login/search/download.
	if err := s.lock(ctx); err != nil {
		return nil, err
	}
	s.setTransports(inst)
	return s, nil
}

func sessionKey(hash string, inst Instance) string {
	keys := make([]string, 0, len(inst.Settings))
	for k := range inst.Settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00", hash, inst.DefinitionID, inst.BaseURL)
	hosts := append([]string(nil), inst.Hosts...)
	for i, host := range hosts {
		hosts[i] = normHost(host)
	}
	sort.Strings(hosts)
	for i, host := range hosts {
		if host != "" && (i == 0 || host != hosts[i-1]) {
			fmt.Fprintf(h, "host:%q\x00", host)
		}
	}
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s\x00", k, inst.Settings[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// cgSession is one configured indexer's live state.
type cgSession struct {
	m        *Manager
	ikey     string // the configured indexer's key
	key      string
	def      *Definition
	name     string
	base     *url.URL
	settings map[string]string
	hosts    map[string]bool // lower-case host names the transport may reach
	jar      http.CookieJar
	client   *http.Client

	trMu   sync.Mutex
	site   http.RoundTripper
	solver http.RoundTripper

	decode func([]byte) ([]byte, error)
	encode func(string) string

	// flow lets one flow (login/search/download) run at a time. It is a channel rather than a mutex so a search
	// waiting its turn gives up when its own time runs out, instead of queueing behind a stuck one for ever.
	flow            chan struct{}
	lastRequest     time.Time
	loggedIn        bool
	userAgent       string      // set by FlareSolverr
	viaFlareSolverr atomic.Bool // a Cloudflare check was passed by FlareSolverr
}

func newSession(m *Manager, def *Definition, inst Instance) (*cgSession, error) {
	baseStr := strings.TrimSpace(inst.BaseURL)
	if baseStr == "" && len(def.Links) > 0 {
		baseStr = def.Links[0]
	}
	base, err := url.Parse(baseStr)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return nil, fmt.Errorf("%s: invalid site address", inst.name())
	}
	if !strings.HasSuffix(base.Path, "/") {
		base.Path += "/"
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create cookie jar: %w", err)
	}
	s := &cgSession{
		m: m, ikey: inst.Key, def: def, name: inst.name(), base: base, jar: jar,
		flow:     make(chan struct{}, 1),
		settings: map[string]string{},
		hosts:    map[string]bool{normHost(base.Hostname()): true},
		decode:   func(b []byte) ([]byte, error) { return b, nil },
		encode:   func(v string) string { return v },
	}
	for _, h := range inst.Hosts {
		if h = normHost(h); h != "" {
			s.hosts[h] = true
		}
	}
	for k, v := range inst.Settings {
		s.settings[k] = v
	}
	if dec, enc, err := lookupEncoding(def.Encoding); err != nil {
		return nil, err
	} else if dec != nil {
		s.decode, s.encode = dec, enc
	}
	s.setTransports(inst)
	s.client = &http.Client{Transport: &cgTransport{s: s}, Jar: jar, Timeout: 30 * time.Second}
	return s, nil
}

func (s *cgSession) setTransports(inst Instance) {
	s.trMu.Lock()
	s.site, s.solver = inst.Transport, inst.Solver
	s.trMu.Unlock()
}

func (s *cgSession) transports() (site, solver http.RoundTripper) {
	s.trMu.Lock()
	defer s.trMu.Unlock()
	return s.site, s.solver
}

// normHost is a host name as the session compares it: lower case, without a trailing dot or a port.
func normHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.Trim(strings.TrimSuffix(h, "."), "[]")
}

// cgTransport sends a site's requests through the indexer's transport (the gateway). Only the site's own host
// and the indexer's configured hosts are reached; the gateway checks its allowlist and the VPN again.
type cgTransport struct {
	s *cgSession
}

func (t *cgTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.s.allowedHost(req.URL.Hostname()); err != nil {
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, err
	}
	site, _ := t.s.transports()
	if site == nil {
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, fmt.Errorf("%s: no transport", t.s.name)
	}
	return site.RoundTrip(req)
}

// allowedHost refuses a host that is neither the site's nor one of the indexer's configured hosts.
func (s *cgSession) allowedHost(host string) error {
	if s.hosts[normHost(host)] {
		return nil
	}
	return fmt.Errorf("%s: the site wants to connect to %s, which is not one of the indexer's hosts, so Theatarr refused", s.name, normHost(host))
}

// lock waits for the session's turn, giving up when ctx ends.
func (s *cgSession) lock(ctx context.Context) error {
	select {
	case s.flow <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%s: waited too long for the site's previous search to finish: %w", s.name, ctx.Err())
	}
}

func (s *cgSession) unlock() { <-s.flow }

// lookupEncoding returns converters for a definition's page encoding, or nils for UTF-8.
func lookupEncoding(name string) (decode func([]byte) ([]byte, error), encode func(string) string, err error) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" || n == "utf-8" || n == "utf8" {
		return nil, nil, nil
	}
	enc, _ := charset.Lookup(n)
	if enc == nil {
		return nil, nil, fmt.Errorf("page encoding %q is not supported", name)
	}
	decode = func(b []byte) ([]byte, error) { return enc.NewDecoder().Bytes(b) }
	encode = func(v string) string {
		out, err := enc.NewEncoder().String(v)
		if err != nil {
			return v
		}
		return out
	}
	return decode, encode, nil
}

// vars returns the template variables every request sees.
func (s *cgSession) vars() *tmplVars {
	v := newTmplVars()
	v.Config["sitelink"] = s.base.String()
	for _, st := range s.def.Settings {
		val, ok := s.settings[st.Name]
		if !ok {
			val = st.Default
		}
		switch st.Type {
		case "checkbox":
			if parseBoolSetting(val) {
				val = checkboxTrue
			} else {
				val = ""
			}
		case "info", "info_cookie", "info_flaresolverr", "info_useragent", "info_category_8000":
			continue
		}
		v.Config[st.Name] = val
	}
	return v
}

// customUserAgent returns the User-Agent the person typed into the site's "useragent" setting, if the definition
// has one. Sites that tie the session cookie to the browser need every request, the sign-in check included, to
// say the same thing as that browser.
func (s *cgSession) customUserAgent() string {
	for _, st := range s.def.Settings {
		if st.Name != "useragent" || st.Type != "text" {
			continue
		}
		val, ok := s.settings[st.Name]
		if !ok {
			val = st.Default
		}
		// a header value can't hold line breaks or other control characters
		val = strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return -1
			}
			return r
		}, val)
		return strings.TrimSpace(val)
	}
	return ""
}

// --- HTTP ---

type cgRequest struct {
	method   string
	url      string
	form     string // encoded body for POST
	headers  map[string]string
	noFollow bool // return redirects instead of following them
	binary   bool // expect a file, not a page
}

type cgResponse struct {
	status   int
	url      *url.URL
	header   http.Header
	raw      []byte // bytes as received
	body     []byte // decoded to UTF-8
	location string // redirect target when noFollow
}

const maxResponseBytes = 16 << 20

// maxRequestDelaySeconds is the longest pause between two requests a definition can ask for.
const maxRequestDelaySeconds = 10

// errMagnetRedirect carries a redirect to a magnet link, which net/http can't follow.
type errMagnetRedirect struct{ uri string }

func (e errMagnetRedirect) Error() string { return "redirect to magnet link" }

func (s *cgSession) wait(ctx context.Context) error {
	gap := s.m.DefaultDelay
	if d := s.def.RequestDelay; d > 0 {
		// a definition asks for its own pause; not longer than a search may last
		if d > maxRequestDelaySeconds {
			d = maxRequestDelaySeconds
		}
		if own := time.Duration(d * float64(time.Second)); own > gap {
			gap = own
		}
	}
	if !s.lastRequest.IsZero() && gap > 0 {
		if d := time.Until(s.lastRequest.Add(gap)); d > 0 {
			t := time.NewTimer(d)
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
		}
	}
	s.lastRequest = time.Now()
	return nil
}

// sameSite reports whether u belongs to the configured site, so the definition's headers (which may carry API keys)
// are only sent there.
func (s *cgSession) sameSite(u *url.URL) bool {
	return strings.EqualFold(u.Hostname(), s.base.Hostname())
}

func (s *cgSession) do(ctx context.Context, r cgRequest) (*cgResponse, error) {
	resp, err := s.doOnce(ctx, r)
	if err != nil {
		return nil, err
	}
	if !isCloudflareChallenge(resp) {
		return resp, nil
	}
	fsURL := ""
	if s.m.FlareSolverrURL != nil {
		fsURL = strings.TrimSpace(s.m.FlareSolverrURL())
	}
	if _, solver := s.transports(); fsURL == "" || solver == nil {
		return nil, fmt.Errorf("%s: %w", s.name, ErrCloudflare)
	}
	sol, err := s.solveChallenge(ctx, fsURL, r)
	if err != nil {
		return nil, fmt.Errorf("%s: FlareSolverr could not pass the Cloudflare check: %w", s.name, err)
	}
	if !r.binary && sol.Response != "" && sol.Status != 0 {
		u, _ := url.Parse(sol.URL)
		if u == nil {
			u, _ = url.Parse(r.url)
		}
		body := []byte(sol.Response)
		return &cgResponse{status: sol.Status, url: u, header: http.Header{}, raw: body, body: body}, nil
	}
	// files can't come through FlareSolverr: retry directly with the clearance cookies and user agent it obtained
	resp, err = s.doOnce(ctx, r)
	if err != nil {
		return nil, err
	}
	if isCloudflareChallenge(resp) {
		return nil, fmt.Errorf("%s: %w", s.name, ErrCloudflare)
	}
	return resp, nil
}

// uaKey carries the User-Agent a request must keep through the indexer's transport (the gateway otherwise sends
// its own).
type uaKey struct{}

// RequestUserAgent is the User-Agent the engine chose for req, for a transport that would otherwise replace it.
func RequestUserAgent(req *http.Request) string {
	ua, _ := req.Context().Value(uaKey{}).(string)
	return ua
}

func (s *cgSession) doOnce(ctx context.Context, r cgRequest) (*cgResponse, error) {
	if err := s.wait(ctx); err != nil {
		return nil, err
	}
	method := strings.ToUpper(r.method)
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader(r.form)
	}
	ua := s.m.UserAgent
	if ua == "" {
		ua = defaultUserAgent
	}
	if custom := s.customUserAgent(); custom != "" {
		ua = custom
	}
	if s.userAgent != "" {
		ua = s.userAgent
	}
	req, err := http.NewRequestWithContext(context.WithValue(ctx, uaKey{}, ua), method, r.url, body)
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", s.name, cleanError(err))
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,application/json;q=0.8,*/*;q=0.7")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if s.sameSite(req.URL) {
		for k, v := range r.headers {
			req.Header.Set(k, v)
		}
	}

	client := *s.client
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if next.URL.Scheme == "magnet" {
			return errMagnetRedirect{uri: next.URL.String()}
		}
		if r.noFollow {
			return http.ErrUseLastResponse
		}
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		if err := s.checkPageAddress(next.URL.String()); err != nil {
			return err
		}
		// a 307/308 sends the form again: never the login (or any posted form) to another host
		if next.Method != http.MethodGet && next.Method != http.MethodHead && !strings.EqualFold(next.URL.Host, via[len(via)-1].URL.Host) {
			return fmt.Errorf("%s: the site redirected a form to %s, another host, so Theatarr did not send it there", s.name, normHost(next.URL.Hostname()))
		}
		// keep the definition's headers on the same site only
		if !strings.EqualFold(next.URL.Hostname(), s.base.Hostname()) {
			for k := range r.headers {
				next.Header.Del(k)
			}
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		var mr errMagnetRedirect
		if errors.As(err, &mr) {
			return &cgResponse{status: http.StatusFound, url: req.URL, header: http.Header{}, location: mr.uri}, nil
		}
		// url.Error repeats the full URL, which may hold a passkey
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("%s: request failed: %w", s.name, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s: read response: %w", s.name, err)
	}
	if len(raw) > maxResponseBytes {
		// a page cut short reads as fewer results, or a file that is broken
		return nil, fmt.Errorf("%s: the site sent more than %d MB, so Theatarr stopped reading", s.name, maxResponseBytes>>20)
	}
	out := &cgResponse{status: resp.StatusCode, url: resp.Request.URL, header: resp.Header, raw: raw, body: raw}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		if loc := resp.Header.Get("Location"); loc != "" {
			if u, err := resp.Request.URL.Parse(loc); err == nil {
				out.location = u.String()
			} else {
				out.location = loc
			}
		}
	}
	if !r.binary {
		if dec, err := s.decode(raw); err == nil {
			out.body = dec
		}
	}
	return out, nil
}

// isCloudflareChallenge recognises the pages Cloudflare (and DDoS-Guard) show instead of the site while checking
// for a real browser.
func isCloudflareChallenge(r *cgResponse) bool {
	if r.status != http.StatusForbidden && r.status != http.StatusServiceUnavailable && r.status != http.StatusTooManyRequests {
		return false
	}
	server := strings.ToLower(r.header.Get("Server"))
	body := string(r.body)
	if len(body) > 64<<10 {
		body = body[:64<<10]
	}
	markers := []string{
		"<title>Just a moment...</title>", "cf-browser-verification", "cf_chl_opt", "challenge-platform",
		"_cf_chl", "cf-challenge-running", "<title>Attention Required! | Cloudflare</title>",
		"<title>DDoS-Guard</title>", "Checking your browser before accessing", "ddos-guard",
	}
	for _, m := range markers {
		if strings.Contains(body, m) {
			return true
		}
	}
	return strings.Contains(server, "cloudflare") && strings.EqualFold(r.header.Get("cf-mitigated"), "challenge")
}

// --- FlareSolverr ---

type fsSolution struct {
	URL       string            `json:"url"`
	Status    int               `json:"status"`
	Response  string            `json:"response"`
	UserAgent string            `json:"userAgent"`
	Cookies   []fsCookie        `json:"cookies"`
	Headers   map[string]string `json:"headers"`
}

type fsCookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Expires  float64 `json:"expires"`
	HTTPOnly bool    `json:"httpOnly"`
	Secure   bool    `json:"secure"`
}

const flareSolverrMaxTimeout = 60000 // ms

// SolverTimeout is how long one FlareSolverr call may take: its maxTimeout plus time to answer. The transport behind
// Instance.Solver must allow at least this long for the answer's headers.
const SolverTimeout = time.Duration(flareSolverrMaxTimeout)*time.Millisecond + 15*time.Second

// solveChallenge asks FlareSolverr (trawl's /v1) to load the page in a real browser and keeps the clearance cookies
// and user agent for later requests.
func (s *cgSession) solveChallenge(ctx context.Context, fsURL string, r cgRequest) (*fsSolution, error) {
	u, err := url.Parse(r.url)
	if err != nil {
		return nil, fmt.Errorf("bad url: %w", cleanError(err))
	}
	// FlareSolverr's browser loads the page itself, out of reach of the transport's check, so the page is checked
	// here first.
	if err := s.allowedHost(u.Hostname()); err != nil {
		return nil, err
	}
	payload := map[string]any{
		"cmd":        "request.get",
		"url":        r.url,
		"maxTimeout": flareSolverrMaxTimeout,
	}
	if strings.EqualFold(r.method, http.MethodPost) {
		payload["cmd"] = "request.post"
		payload["postData"] = r.form
	}
	var cookies []map[string]string
	for _, c := range s.jar.Cookies(u) {
		cookies = append(cookies, map[string]string{"name": c.Name, "value": c.Value})
	}
	if len(cookies) > 0 {
		payload["cookies"] = cookies
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	endpoint := strings.TrimRight(fsURL, "/") + "/v1"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("build request: bad FlareSolverr address")
	}
	req.Header.Set("Content-Type", "application/json")
	_, solver := s.transports()
	client := &http.Client{Transport: solver, Timeout: SolverTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		var nerr net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &nerr) && nerr.Timeout()) {
			return nil, fmt.Errorf("FlareSolverr didn't finish the Cloudflare check after %d seconds; check that it is running and its traffic leaves through the VPN", int(time.Since(started).Round(time.Second)/time.Second))
		}
		return nil, fmt.Errorf("contact FlareSolverr: %w", cleanError(err))
	}
	defer resp.Body.Close()
	var out struct {
		Status   string      `json:"status"`
		Message  string      `json:"message"`
		Solution *fsSolution `json:"solution"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&out); err != nil {
		return nil, fmt.Errorf("read FlareSolverr reply (status %d): %w", resp.StatusCode, err)
	}
	if out.Status != "ok" || out.Solution == nil {
		msg := out.Message
		if msg == "" {
			msg = fmt.Sprintf("status %q", out.Status)
		}
		if strings.Contains(strings.ToLower(msg), "timeout") {
			return nil, fmt.Errorf("FlareSolverr couldn't pass the Cloudflare check within %d seconds", flareSolverrMaxTimeout/1000)
		}
		return nil, errors.New(shortenText(msg, 200))
	}
	sol := out.Solution
	// The browser follows redirects on its own: a page that ended on another host is thrown away.
	if end, err := url.Parse(sol.URL); err == nil && end.Host != "" {
		if err := s.allowedHost(end.Hostname()); err != nil {
			return nil, fmt.Errorf("FlareSolverr was sent elsewhere: %w", err)
		}
	}
	var jarCookies []*http.Cookie
	for _, c := range sol.Cookies {
		hc := &http.Cookie{Name: c.Name, Value: c.Value, Path: c.Path, Domain: strings.TrimPrefix(c.Domain, "."), HttpOnly: c.HTTPOnly, Secure: c.Secure}
		if c.Expires > 0 {
			hc.Expires = time.Unix(int64(c.Expires), 0)
		}
		if hc.Path == "" {
			hc.Path = "/"
		}
		jarCookies = append(jarCookies, hc)
	}
	if len(jarCookies) > 0 {
		s.jar.SetCookies(&url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/"}, jarCookies)
	}
	if sol.UserAgent != "" {
		s.userAgent = sol.UserAgent
		s.viaFlareSolverr.Store(true)
	}
	s.m.log().Info("cardigann: passed a Cloudflare check via FlareSolverr", "indexer", s.ikey, "cookies", len(jarCookies))
	s.persist(ctx)
	return sol, nil
}

// --- URLs ---

// resolve turns a (possibly relative) link from a page into an absolute URL against base (the page it came from, or
// the site root).
func (s *cgSession) resolve(ref string, base *url.URL) (string, error) {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "magnet:") {
		return ref, nil
	}
	if base == nil {
		base = s.base
	}
	u, err := base.Parse(ref)
	if err != nil {
		// the link can carry a passkey, and so can net/url's message about it
		return "", fmt.Errorf("bad link: %w", cleanError(err))
	}
	return u.String(), nil
}

// resolvePath resolves a definition path (relative to the site root). A path that is a full address must lead to
// the site itself: the definitions come from a public repository anyone can send changes to, and these requests
// carry the person's login, cookie or passkey. An address on a host that has nothing to do with the site would hand
// those to a stranger. (A site that sends no credentials may use another configured host: see checkSiteAddress.)
func (s *cgSession) resolvePath(p string) (string, error) {
	p = strings.TrimSpace(p)
	out, err := s.resolve(p, s.base)
	if err != nil {
		return "", fmt.Errorf("%s: bad address in the site definition: %w", s.name, cleanError(err))
	}
	// Judge the address that results, not how it was written: "//host/x", "HTTPS://host/x" and "http:host" all
	// leave the site as well.
	if err := s.checkSiteAddress(out); err != nil {
		return "", err
	}
	if strings.HasPrefix(strings.ToLower(p), "http://") || strings.HasPrefix(strings.ToLower(p), "https://") {
		return p, nil
	}
	return out, nil
}

// checkSiteAddress refuses an address that is not a web address of the site itself (see resolvePath). A site that
// sends no login, cookie, key or passkey has nothing to hand over, so its definition may name another host (a site
// whose search runs on a separate API host), as long as the indexer's configuration lists it (see allowedHost).
func (s *cgSession) checkSiteAddress(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s: bad address in the site definition: %w", s.name, cleanError(err))
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s: the site definition wants to use an address that is not a web address, so Theatarr refused", s.name)
	}
	if s.siteRelated(u.Hostname()) {
		return nil
	}
	if !s.sendsCredentials() {
		if err := s.allowedHost(u.Hostname()); err != nil {
			return fmt.Errorf("%s: the site definition wants to connect to %s, which is not one of the indexer's hosts (add it to the indexer's hosts), so Theatarr refused", s.name, normHost(u.Hostname()))
		}
		return nil
	}
	return fmt.Errorf("%s: the site definition wants to connect to %s, which is not one of the site's own addresses, so Theatarr refused", s.name, normHost(u.Hostname()))
}

// credentialNames are setting names that hold a sign-in, beyond the secret ones SecretSetting already knows.
var credentialNames = []string{"username", "user", "uid", "login", "email", "userid", "user_id"}

// sendsCredentials reports whether requests to this site can carry something the person would not want a stranger
// to have: the definition signs in, or a setting that holds a login, cookie, key or passkey is filled in, or a
// header the definition adds carries an authorisation or cookie.
func (s *cgSession) sendsCredentials() bool {
	if s.def == nil {
		return true // nothing known about the site: stay strict
	}
	if s.def.Login != nil {
		return true
	}
	for _, st := range s.def.Settings {
		v := strings.TrimSpace(s.settings[st.Name])
		if v == "" {
			continue
		}
		if SecretSetting(st.Type, st.Name) {
			return true
		}
		// A value that is just what the definition itself suggests (the API address YTS and The Pirate Bay save
		// with the form) is not something the person typed in, so it cannot be a sign-in.
		if d := strings.TrimSpace(st.Default); d != "" && v == d && !credentialSettingName(st.Name) {
			continue
		}
		n := strings.ToLower(st.Name)
		if credentialSettingName(st.Name) {
			return true
		}
		// Any other filled-in text or password setting is taken for a credential too (a session id, a hash, an
		// "auth" value), apart from the few that are plain preferences. Choices and check boxes hold nothing secret.
		if t := strings.ToLower(strings.TrimSpace(st.Type)); (t == "" || t == "text" || t == "password") && !plainSettings[n] {
			return true
		}
	}
	headerBlocks := []map[string][]string{s.def.Search.Headers}
	if s.def.Download != nil {
		headerBlocks = append(headerBlocks, s.def.Download.Headers)
	}
	for _, h := range headerBlocks {
		for k := range h {
			if credentialHeader(k) {
				return true
			}
		}
	}
	return false
}

// credentialSettingName reports whether a setting is named like a sign-in.
func credentialSettingName(name string) bool {
	n := strings.ToLower(name)
	for _, c := range credentialNames {
		if n == c {
			return true
		}
	}
	return false
}

// plainSettings are text settings that are preferences, not sign-ins.
var plainSettings = map[string]bool{
	"useragent": true, "user-agent": true, "user_agent": true,
	"language": true, "lang": true, "sort": true, "sortby": true, "sort_by": true,
	"order": true, "orderby": true, "order_by": true, "category": true, "categories": true,
	"cat": true, "sitelink": true, "apiurl": true, "api_url": true, "uploader": true,
}

// credentialHeader reports whether a header the definition adds can carry a sign-in: its name speaks of
// authorisation, a token, a key, a session or a cookie.
func credentialHeader(name string) bool {
	n := strings.ToLower(name)
	for _, p := range []string{"auth", "token", "key", "session", "cookie"} {
		if strings.Contains(n, p) {
			return true
		}
	}
	return false
}

// siteRelated reports whether host is the site's address (the one the person configured) or shares its registered
// domain, such as api.example.org for www.example.org. The definition's other links don't count: a host added to
// that list would otherwise be handed the login, cookie or API key.
func (s *cgSession) siteRelated(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return false
	}
	k := strings.ToLower(strings.TrimSuffix(s.base.Hostname(), "."))
	if k == host {
		return true
	}
	hostDomain, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return false // an IP address or a name without a registered domain: only an exact match counts
	}
	d, err := publicsuffix.EffectiveTLDPlusOne(k)
	return err == nil && d == hostDomain
}

// checkPageAddress refuses an address a page handed out (a redirect, a result's link, a download link) that is not
// a web address, or that names this machine or the home network when the site itself is on the internet. Such a
// page is trying to make Theatarr call something it was never meant to reach. Other hosts are kept as links (sites
// link to file mirrors); the transport reaches only the indexer's hosts.
func (s *cgSession) checkPageAddress(raw string) error {
	if strings.HasPrefix(raw, "magnet:") {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s: bad link from the site: %w", s.name, cleanError(err))
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s: the site sent a link that is not a web address, so Theatarr refused it", s.name)
	}
	if s.siteRelated(u.Hostname()) || localHost(s.base.Hostname()) || !localHost(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("%s: the site sent a link to %s, which is on your own network, so Theatarr refused it", s.name, normHost(u.Hostname()))
}

// localHost reports whether host names this machine or the home network: a private, loopback or shared address, or
// a name such as localhost.
func localHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if addr, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return notPublic(addr)
	}
	if host == "localhost" || !strings.Contains(host, ".") {
		return true // a bare name only the local network can resolve
	}
	for _, suffix := range []string{".localhost", ".local", ".lan", ".home", ".internal", ".home.arpa", ".localdomain"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// cgnat is the shared address space (RFC 6598), which carrier and VPN networks use.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// notPublic reports whether addr is not a public internet address.
func notPublic(addr netip.Addr) bool {
	addr = addr.Unmap()
	return !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || cgnat.Contains(addr)
}

// cleanError drops the URL net/url repeats in its errors: it may hold a passkey.
func cleanError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// templatedHeaders renders a definition header block.
func templatedHeaders(h map[string][]string, vars *tmplVars) (map[string]string, error) {
	out := map[string]string{}
	for k, vals := range h {
		if len(vals) == 0 {
			continue
		}
		v, err := applyTemplate(vals[0], vars, nil)
		if err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}

// encodeForm url-encodes an ordered input list (plus an optional raw suffix) in the site's encoding.
func (s *cgSession) encodeForm(keys []string, vals map[string]string, raw string) string {
	var sb strings.Builder
	for _, k := range keys {
		if sb.Len() > 0 {
			sb.WriteByte('&')
		}
		sb.WriteString(url.QueryEscape(s.encode(k)))
		sb.WriteByte('=')
		sb.WriteString(url.QueryEscape(s.encode(vals[k])))
	}
	if raw = strings.Trim(raw, "&"); raw != "" {
		if sb.Len() > 0 {
			sb.WriteByte('&')
		}
		sb.WriteString(raw)
	}
	return sb.String()
}

func appendQuery(u, q string) string {
	if q == "" {
		return u
	}
	if strings.Contains(u, "?") {
		if strings.HasSuffix(u, "?") || strings.HasSuffix(u, "&") {
			return u + q
		}
		return u + "&" + q
	}
	return u + "?" + q
}
