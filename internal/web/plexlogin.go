package web

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/plexauth"
)

// Plex sign-in (docs/design/users.md), the way Seerr does it. The SPA opens a popup, asks POST auth/plex/start for a
// pin and points the popup at app.plex.tv with it, then polls POST auth/plex/poll once a second. When the person has
// confirmed, the poll checks the account's access to our server (plexauth), finds or creates its account and starts
// a session. The routes are public (there is no session yet) and, like the JSON login, take same-origin JSON only.
// The Plex token never leaves plexauth.Poll: it is not stored, logged or sent to the browser.

// PlexLogin is plexauth.Service.
type PlexLogin interface {
	Start(ctx context.Context, addr, forwardURL string) (*plexauth.Started, error)
	Poll(ctx context.Context, id string) (*auth.PlexAccount, error)
}

func (w *Web) mountPlexLogin(mux *http.ServeMux) {
	b := APIBase
	w.handle(mux, "GET "+b+"/auth/plex", Public, 0, w.apiPlexInfo)
	w.handle(mux, "POST "+b+"/auth/plex/start", Public, 0, w.apiPlexStart)
	w.handle(mux, "POST "+b+"/auth/plex/poll", Public, 0, w.apiPlexPoll)
}

func (w *Web) apiPlexInfo(rw http.ResponseWriter, r *http.Request) {
	writeJSON(rw, http.StatusOK, map[string]any{"enabled": w.plex != nil})
}

// requestOrigin is scheme://host of the request as the browser sees it.
func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (w *Web) apiPlexStart(rw http.ResponseWriter, r *http.Request) {
	if w.plex == nil {
		writeJSON(rw, http.StatusNotFound, apiError{Error: "Plex sign-in is not set up"})
		return
	}
	st, err := w.plex.Start(r.Context(), auth.ClientAddr(r), requestOrigin(r)+"/login/plex/done")
	switch {
	case errors.Is(err, plexauth.ErrTooMany):
		rw.Header().Set("Retry-After", "600")
		writeJSON(rw, http.StatusTooManyRequests, apiError{Error: "too many Plex sign-ins started; try again later"})
		return
	case err != nil:
		w.log.Warn("plex sign-in: start", "err", err, "remote", auth.ClientAddr(r))
		writeJSON(rw, http.StatusBadGateway, apiError{Error: "plex.tv did not answer; try again"})
		return
	}
	writeJSON(rw, http.StatusOK, st)
}

// PlexPoll is the answer to POST auth/plex/poll. Status: waiting, signed_in, pending (the account waits for an
// admin), denied (no access to our server), disabled, expired, error.
type PlexPoll struct {
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	Username  string `json:"username,omitempty"`
	CSRFToken string `json:"csrf_token,omitempty"`
}

func (w *Web) apiPlexPoll(rw http.ResponseWriter, r *http.Request) {
	if w.plex == nil {
		writeJSON(rw, http.StatusNotFound, apiError{Error: "Plex sign-in is not set up"})
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil || req.ID == "" {
		badRequest(rw, "want JSON {\"id\"}")
		return
	}
	ctx := r.Context()
	addr := auth.ClientAddr(r)
	acct, err := w.plex.Poll(ctx, req.ID)
	switch {
	case errors.Is(err, plexauth.ErrUnknownPin):
		writeJSON(rw, http.StatusGone, PlexPoll{Status: "expired", Error: "The Plex sign-in expired. Start again."})
		return
	case errors.Is(err, plexauth.ErrNoAccess):
		var pid int64
		if acct != nil {
			pid = acct.ID
		}
		w.log.Warn("plex sign-in refused: no access to our server", "plex_id", pid, "remote", addr)
		writeJSON(rw, http.StatusForbidden, PlexPoll{Status: "denied",
			Error: "This Plex account has no access to our Plex server."})
		return
	case err != nil:
		w.log.Warn("plex sign-in: poll", "err", err, "remote", addr)
		writeJSON(rw, http.StatusBadGateway, PlexPoll{Status: "error", Error: "Plex did not answer; try again."})
		return
	case acct == nil:
		writeJSON(rw, http.StatusOK, PlexPoll{Status: "waiting"})
		return
	}
	uid, status, created, err := w.auth.PlexSignIn(ctx, *acct)
	if err != nil {
		w.internal(rw, err)
		return
	}
	switch status {
	case auth.StatusPending:
		w.log.Info("plex sign-in: account waits for an admin", "user", uid, "new", created, "remote", addr)
		writeJSON(rw, http.StatusOK, PlexPoll{Status: "pending",
			Error: "Your account waits for an admin to let you in. Try again once they have."})
		if created {
			w.publishUsersChanged()
		}
		return
	case auth.StatusDisabled:
		w.log.Warn("plex sign-in refused: account disabled", "user", uid, "remote", addr)
		writeJSON(rw, http.StatusForbidden, PlexPoll{Status: "disabled", Error: "This account is disabled."})
		return
	}
	token, err := w.auth.StartSession(ctx, uid, addr, r.UserAgent())
	if err != nil {
		w.internal(rw, err)
		return
	}
	w.auth.SetSessionCookie(rw, r, token)
	u, _ := w.auth.GetUser(ctx, uid)
	name := ""
	if u != nil {
		name = u.Username
	}
	w.log.Info("login", "username", name, "via", "plex", "new", created, "remote", addr)
	writeJSON(rw, http.StatusOK, PlexPoll{Status: "signed_in", Username: name, CSRFToken: w.auth.SessionCSRF(token)})
}
