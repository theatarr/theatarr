package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
)

// Invite links (docs/design/users.md, internal/auth/invite.go):
//
//	POST   users/invites          an admin makes an invite: role, auto-approve, name, days; the answer holds the token
//	DELETE users/{id}/invite      revoke an invite that is not redeemed yet
//	POST   auth/invite            public: what the invite page shows for a token, or 404 / 410
//	POST   auth/invite/accept     public: redeem with a username and password; signs in
//
// The Plex way redeems through POST auth/plex/poll with the token as "invite". The token travels in the link's
// fragment (/invite#<token>), which the browser never sends, and in request bodies only, so no request line or log
// holds it.

func (w *Web) mountInviteAPI(mux *http.ServeMux) {
	b := APIBase
	w.handle(mux, "POST "+b+"/users/invites", SignedIn, auth.PermUsers, w.apiCreateInvite)
	w.handle(mux, "DELETE "+b+"/users/{id}/invite", SignedIn, auth.PermUsers, w.apiDeleteInvite)
	w.handle(mux, "POST "+b+"/auth/invite", Public, 0, w.apiLookupInvite)
	w.handle(mux, "POST "+b+"/auth/invite/accept", Public, 0, w.apiAcceptInvite)
}

// maxInviteDays bounds an invite's life.
const maxInviteDays = 30

// CreatedInvite is the answer to POST users/invites: the token (shown this once) and the invite as the users list
// shows it.
type CreatedInvite struct {
	Token string    `json:"token"`
	User  auth.User `json:"user"`
}

func (w *Web) apiCreateInvite(rw http.ResponseWriter, r *http.Request) {
	var req struct {
		Role        string `json:"role"`
		AutoApprove bool   `json:"auto_approve"`
		Name        string `json:"name"`
		Days        int    `json:"days"`
	}
	if err := decodeJSON(r, &req); err != nil {
		badRequest(rw, "invalid JSON: "+err.Error())
		return
	}
	if req.Days < 0 || req.Days > maxInviteDays {
		badRequest(rw, "days: 1 to 30 (0 is 7)")
		return
	}
	// The inviter holds the users permission, which sets any role on another account (the self-role rule is about
	// one's own account), so any role may be invited.
	id, token, err := w.auth.CreateInvite(r.Context(), auth.NewInvite{Role: req.Role, AutoApprove: req.AutoApprove,
		Name: req.Name, TTL: time.Duration(req.Days) * 24 * time.Hour})
	if err != nil && (strings.HasPrefix(err.Error(), "role") || strings.HasPrefix(err.Error(), "name")) {
		badRequest(rw, err.Error())
		return
	}
	if err != nil {
		w.internal(rw, err)
		return
	}
	u, err := w.auth.GetUser(r.Context(), id)
	if err != nil {
		w.internal(rw, err)
		return
	}
	w.log.Info("invite created", "user", id, "by", auth.FromContext(r.Context()).UserID, "role", u.Role,
		"days", req.Days)
	w.publishUsersChanged()
	writeJSON(rw, http.StatusCreated, CreatedInvite{Token: token, User: *u})
}

func (w *Web) apiDeleteInvite(rw http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(rw)
		return
	}
	if err := w.auth.DeleteInvite(r.Context(), id); errors.Is(err, auth.ErrUserNotFound) {
		notFound(rw)
		return
	} else if err != nil {
		w.internal(rw, err)
		return
	}
	w.log.Info("invite revoked", "user", id, "by", auth.FromContext(r.Context()).UserID)
	w.publishUsersChanged()
	rw.WriteHeader(http.StatusNoContent)
}

// InviteInfo is the answer to POST auth/invite: the name the admin gave, the expiry, and whether the page may offer
// Plex sign-in.
type InviteInfo struct {
	auth.Invite
	Plex bool `json:"plex"`
}

type inviteRequest struct {
	Token    string `json:"token"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func decodeInvite(rw http.ResponseWriter, r *http.Request) (inviteRequest, bool) {
	var req inviteRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Token == "" {
		badRequest(rw, "want JSON {\"token\"}")
		return req, false
	}
	return req, true
}

// What a dead invite link says.
const (
	inviteExpiredMsg = "This invite link has expired. Ask for a new one."
	inviteUnknownMsg = "This invite link is not valid or has already been used."
)

// inviteFailed answers a refused invite; it reports whether err was one.
func (w *Web) inviteFailed(rw http.ResponseWriter, err error) bool {
	var thr *auth.Throttled
	switch {
	case err == nil:
		return false
	case errors.Is(err, auth.ErrInviteExpired):
		writeJSON(rw, http.StatusGone, apiError{Error: inviteExpiredMsg})
	case errors.Is(err, auth.ErrInviteUnknown):
		writeJSON(rw, http.StatusNotFound, apiError{Error: inviteUnknownMsg})
	case errors.As(err, &thr):
		rw.Header().Set("Retry-After", retryAfter(thr, w.now()))
		writeJSON(rw, http.StatusTooManyRequests, apiError{Error: "Too many tries. Try again later."})
	default:
		w.internal(rw, err)
	}
	return true
}

func (w *Web) apiLookupInvite(rw http.ResponseWriter, r *http.Request) {
	req, ok := decodeInvite(rw, r)
	if !ok {
		return
	}
	inv, err := w.auth.LookupInvite(r.Context(), req.Token, auth.ClientAddr(r))
	if w.inviteFailed(rw, err) {
		return
	}
	writeJSON(rw, http.StatusOK, InviteInfo{Invite: inv, Plex: w.plex != nil})
}

func (w *Web) apiAcceptInvite(rw http.ResponseWriter, r *http.Request) {
	req, ok := decodeInvite(rw, r)
	if !ok {
		return
	}
	token, err := w.auth.AcceptInvite(r.Context(), req.Token, req.Username, req.Password, auth.ClientAddr(r),
		r.UserAgent())
	var bad auth.InvalidAccount
	if errors.As(err, &bad) {
		badRequest(rw, bad.Error())
		return
	}
	if w.inviteFailed(rw, err) {
		return
	}
	w.publishUsersChanged()
	w.auth.SetSessionCookie(rw, r, token)
	writeJSON(rw, http.StatusOK, map[string]any{"username": strings.TrimSpace(req.Username),
		"csrf_token": w.auth.SessionCSRF(token)})
}
