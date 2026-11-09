package cardigann

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// SessionStore keeps a session's cookies and FlareSolverr user agent between restarts. Values are credentials:
// a store keeps them like the settings secrets and never returns them in an API.
type SessionStore interface {
	// LoadSession returns the saved session of an indexer, or nil.
	LoadSession(ctx context.Context, indexer string) (*SavedSession, error)
	SaveSession(ctx context.Context, indexer string, s SavedSession) error
}

// SavedSession is a session as saved. Key is the session key (definition, address and settings, credential
// included): a saved session with another key is not used, so a changed credential signs in afresh.
type SavedSession struct {
	Key       string        `json:"key"`
	Cookies   []SavedCookie `json:"cookies"`
	UserAgent string        `json:"user_agent,omitempty"`
	LoggedIn  bool          `json:"logged_in,omitempty"`
	SavedAt   time.Time     `json:"saved_at"`
}

// SavedCookie is one cookie of the site.
type SavedCookie struct {
	Host  string `json:"host"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// maxSessionAge is how long a saved session is used after it was saved: sites expire sessions, and the jar's own
// expiry times are not kept.
const maxSessionAge = 7 * 24 * time.Hour

// restore loads the indexer's saved session when it belongs to this one (same key, recent).
func (s *cgSession) restore(ctx context.Context) {
	if s.m.Sessions == nil || s.ikey == "" {
		return
	}
	saved, err := s.m.Sessions.LoadSession(ctx, s.ikey)
	if err != nil || saved == nil || saved.Key != s.key || time.Since(saved.SavedAt) > maxSessionAge {
		return
	}
	byHost := map[string][]*http.Cookie{}
	for _, c := range saved.Cookies {
		if s.hosts[normHost(c.Host)] {
			byHost[normHost(c.Host)] = append(byHost[normHost(c.Host)], &http.Cookie{Name: c.Name, Value: c.Value, Path: "/"})
		}
	}
	for host, cookies := range byHost {
		s.jar.SetCookies(&url.URL{Scheme: s.base.Scheme, Host: host, Path: "/"}, cookies)
	}
	s.userAgent = saved.UserAgent
	if s.userAgent != "" {
		s.viaFlareSolverr.Store(true)
	}
	// A restored login is checked by the next search: a lost session signs in again.
	s.loggedIn = saved.LoggedIn && len(saved.Cookies) > 0
}

// persist saves the session's cookies (after a login or a passed Cloudflare check).
func (s *cgSession) persist(ctx context.Context) {
	if s.m.Sessions == nil || s.ikey == "" {
		return
	}
	saved := SavedSession{Key: s.key, UserAgent: s.userAgent, LoggedIn: s.loggedIn, SavedAt: time.Now().UTC()}
	for host := range s.hosts {
		for _, c := range s.jar.Cookies(&url.URL{Scheme: s.base.Scheme, Host: host, Path: "/"}) {
			saved.Cookies = append(saved.Cookies, SavedCookie{Host: host, Name: c.Name, Value: c.Value})
		}
	}
	if err := s.m.Sessions.SaveSession(context.WithoutCancel(ctx), s.ikey, saved); err != nil {
		s.m.log().Warn("cardigann: could not save the session", "indexer", s.ikey, "err", err)
	}
}

// Throwaway returns a manager over the same definitions and FlareSolverr that keeps no sessions and saves none: a
// settings test runs on it, so testing a candidate never touches a configured indexer's live session.
func (m *Manager) Throwaway() *Manager {
	if m == nil {
		return nil
	}
	return &Manager{Definitions: m.Definitions, FlareSolverrURL: m.FlareSolverrURL, DefaultDelay: m.DefaultDelay,
		UserAgent: m.UserAgent, Logger: m.Logger, sessions: map[string]*cgSession{}}
}
