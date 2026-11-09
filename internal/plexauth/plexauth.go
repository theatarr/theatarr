// Package plexauth is Plex sign-in (docs/design/users.md), the way Seerr does it: a plex.tv PIN that the person
// confirms at app.plex.tv in a popup, then two checks with the account's token: who the account is
// (GET /api/v2/user) and whether its resources hold our Plex server (GET /api/v2/resources, our server's
// machineIdentifier from the local server's /identity).
//
// Every plex.tv request goes through the gateway as metadata source plex-tv, with its budget. The pin is created and
// polled server side (the browser only opens app.plex.tv, a navigation the CSP does not govern), so the account's
// token never reaches the browser or the database: it lives in one Poll call, is used for the two checks, and is
// dropped. Nothing logs it; errors name the step and the HTTP status only.
package plexauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/policy"
)

// Doer is the gateway's Do.
type Doer interface {
	Do(ctx context.Context, t gateway.Target, req *http.Request) (*http.Response, error)
}

// Target is the gateway target of plex.tv.
var Target = gateway.Target{Class: gateway.ClassMetadata, Key: policy.PlexTVSource}

// AppAuthURL is where the browser confirms a pin.
const AppAuthURL = "https://app.plex.tv/auth"

// Limits.
const (
	maxPins      = 200
	pinTTL       = 15 * time.Minute
	startsPerWin = 10
	startWindow  = 10 * time.Minute
)

// Errors from Poll.
var (
	ErrUnknownPin = errors.New("plexauth: no such sign-in, or it expired; start again")
	ErrNoAccess   = errors.New("plexauth: this Plex account has no access to our Plex server")
	ErrTooMany    = errors.New("plexauth: too many sign-ins started from here; try again later")
)

// Config is what New needs.
type Config struct {
	Doer Doer
	// BaseURL is metadata.sources.plex-tv's URL, normally https://plex.tv.
	BaseURL string
	// ClientID is this install's X-Plex-Client-Identifier (stable across restarts).
	ClientID func(ctx context.Context) (string, error)
	// ServerID gives our Plex server's machineIdentifier (plex.Client.Identity through the gateway).
	ServerID func(ctx context.Context) (string, error)
	// Product names us to plex.tv and in the app.plex.tv page.
	Product string
	Now     func() time.Time
}

// Service runs sign-ins. Safe for concurrent use.
type Service struct {
	cfg  Config
	base *url.URL

	mu     sync.Mutex
	pins   map[string]*pin
	starts map[string][]time.Time
	server string // cached machineIdentifier
}

type pin struct {
	id      int64
	code    string
	expires time.Time
	addr    string
}

// New builds the service.
func New(cfg Config) (*Service, error) {
	if cfg.Doer == nil || cfg.ClientID == nil || cfg.ServerID == nil {
		return nil, errors.New("plexauth: doer, client id and server id are required")
	}
	u, err := url.Parse(strings.TrimRight(cfg.BaseURL, "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("plexauth: bad plex.tv URL %q", cfg.BaseURL)
	}
	if cfg.Product == "" {
		cfg.Product = "Theatarr"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, base: u, pins: map[string]*pin{}, starts: map[string][]time.Time{}}, nil
}

// Started is a sign-in in progress: the browser opens AuthURL and polls with ID.
type Started struct {
	ID        string    `json:"id"`
	AuthURL   string    `json:"auth_url"`
	ExpiresAt time.Time `json:"expires_at"`
}

func handle() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Start creates a pin at plex.tv for a browser at addr. forwardURL, when set, is where app.plex.tv sends the popup
// after the person confirms (our "you can close this window" page).
func (s *Service) Start(ctx context.Context, addr, forwardURL string) (*Started, error) {
	now := s.cfg.Now()
	s.mu.Lock()
	for k, p := range s.pins {
		if now.After(p.expires) {
			delete(s.pins, k)
		}
	}
	recent := s.starts[addr][:0]
	for _, t := range s.starts[addr] {
		if now.Sub(t) < startWindow {
			recent = append(recent, t)
		}
	}
	s.starts[addr] = recent
	if len(recent) >= startsPerWin || len(s.pins) >= maxPins {
		s.mu.Unlock()
		return nil, ErrTooMany
	}
	s.starts[addr] = append(recent, now)
	if len(s.starts) > 10000 {
		clear(s.starts)
	}
	s.mu.Unlock()

	cid, err := s.cfg.ClientID(ctx)
	if err != nil {
		return nil, err
	}
	var out struct {
		ID        int64  `json:"id"`
		Code      string `json:"code"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := s.call(ctx, http.MethodPost, "/api/v2/pins", url.Values{"strong": {"true"}}, cid, "", &out); err != nil {
		return nil, fmt.Errorf("plexauth: create pin: %w", err)
	}
	if out.ID == 0 || out.Code == "" {
		return nil, errors.New("plexauth: create pin: plex.tv answered without a pin")
	}
	exp := now.Add(pinTTL)
	if t, err := time.Parse(time.RFC3339, out.ExpiresAt); err == nil && t.Before(exp) && t.After(now) {
		exp = t
	}
	h, err := handle()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.pins[h] = &pin{id: out.ID, code: out.Code, expires: exp, addr: addr}
	s.mu.Unlock()

	// app.plex.tv reads its parameters from the fragment.
	q := url.Values{"clientID": {cid}, "code": {out.Code}, "context[device][product]": {s.cfg.Product}}
	if forwardURL != "" {
		q.Set("forwardUrl", forwardURL)
	}
	return &Started{ID: h, AuthURL: AppAuthURL + "#?" + q.Encode(), ExpiresAt: exp}, nil
}

// Poll asks plex.tv whether the pin behind id was confirmed. It returns nil, nil while waiting. Once confirmed, it
// checks the account's access to our server with the account's token, forgets the pin and the token, and returns
// the account (ErrNoAccess when the account cannot see our server).
func (s *Service) Poll(ctx context.Context, id string) (*auth.PlexAccount, error) {
	s.mu.Lock()
	p := s.pins[id]
	if p != nil && s.cfg.Now().After(p.expires) {
		delete(s.pins, id)
		p = nil
	}
	s.mu.Unlock()
	if p == nil {
		return nil, ErrUnknownPin
	}
	cid, err := s.cfg.ClientID(ctx)
	if err != nil {
		return nil, err
	}
	var pv struct {
		AuthToken *string `json:"authToken"`
	}
	if err := s.call(ctx, http.MethodGet, "/api/v2/pins/"+strconv.FormatInt(p.id, 10), nil, cid, "",
		&pv); err != nil {
		var se *statusError
		if errors.As(err, &se) && se.code == http.StatusNotFound {
			s.forget(id)
			return nil, ErrUnknownPin
		}
		return nil, fmt.Errorf("plexauth: check pin: %w", err)
	}
	if pv.AuthToken == nil || *pv.AuthToken == "" {
		return nil, nil
	}
	// One use: whatever happens next, this pin is done.
	s.forget(id)
	token := *pv.AuthToken
	pv.AuthToken = nil
	return s.verify(ctx, cid, token)
}

func (s *Service) forget(id string) {
	s.mu.Lock()
	delete(s.pins, id)
	s.mu.Unlock()
}

// verify reads the account and checks its resources for our server. token is not kept.
func (s *Service) verify(ctx context.Context, cid, token string) (*auth.PlexAccount, error) {
	var u struct {
		ID       int64  `json:"id"`
		UUID     string `json:"uuid"`
		Username string `json:"username"`
		Title    string `json:"title"`
		Email    string `json:"email"`
		Thumb    string `json:"thumb"`
	}
	if err := s.call(ctx, http.MethodGet, "/api/v2/user", nil, cid, token, &u); err != nil {
		return nil, fmt.Errorf("plexauth: read account: %w", err)
	}
	if u.ID <= 0 {
		return nil, errors.New("plexauth: read account: no account id")
	}
	server, err := s.serverID(ctx)
	if err != nil {
		return nil, fmt.Errorf("plexauth: our server's identity: %w", err)
	}
	var res []struct {
		ClientIdentifier string `json:"clientIdentifier"`
		Provides         string `json:"provides"`
	}
	if err := s.call(ctx, http.MethodGet, "/api/v2/resources", url.Values{"includeHttps": {"1"}}, cid, token,
		&res); err != nil {
		return nil, fmt.Errorf("plexauth: read resources: %w", err)
	}
	for _, r := range res {
		if r.ClientIdentifier == server && strings.Contains(r.Provides, "server") {
			return &auth.PlexAccount{ID: u.ID, UUID: u.UUID, Username: u.Username, Title: u.Title, Email: u.Email,
				Thumb: u.Thumb}, nil
		}
	}
	return &auth.PlexAccount{ID: u.ID, Username: u.Username}, ErrNoAccess
}

func (s *Service) serverID(ctx context.Context) (string, error) {
	s.mu.Lock()
	id := s.server
	s.mu.Unlock()
	if id != "" {
		return id, nil
	}
	id, err := s.cfg.ServerID(ctx)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", errors.New("empty machineIdentifier")
	}
	s.mu.Lock()
	s.server = id
	s.mu.Unlock()
	return id, nil
}

type statusError struct{ code int }

func (e *statusError) Error() string { return "HTTP " + strconv.Itoa(e.code) }

// call sends one plex.tv request through the gateway and decodes its JSON answer. The token, when given, goes in
// the X-Plex-Token header only (never the URL, so no log line can carry it).
func (s *Service) call(ctx context.Context, method, path string, q url.Values, cid, token string, out any) error {
	u := *s.base
	u.Path += path
	if q != nil {
		u.RawQuery = q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return err
	}
	h := req.Header
	h.Set("Accept", "application/json")
	h.Set("X-Plex-Product", s.cfg.Product)
	h.Set("X-Plex-Client-Identifier", cid)
	if token != "" {
		h.Set("X-Plex-Token", token)
	}
	resp, err := s.cfg.Doer.Do(ctx, Target, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return &statusError{resp.StatusCode}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("unreadable answer: %w", err)
	}
	return nil
}
